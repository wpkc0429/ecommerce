package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/order"
)

// TestFormatMoneyNeverScales is the one assertion in this package that guards a
// mistake nothing else would catch: a wrong unit reads as a plausible number.
//
// TWD's minor unit IS the dollar, so 3400 is NT$3,400 — not NT$34.00 and not
// NT$34. The whole file is written to contain no `/100` and no `*100`; this test
// is what turns "written that way" into "stays that way".
func TestFormatMoneyNeverScales(t *testing.T) {
	for _, tc := range []struct {
		amount   int64
		currency string
		want     string
	}{
		{3400, "TWD", "NT$3,400"},
		{0, "TWD", "NT$0"},
		{999, "TWD", "NT$999"},
		{1000, "TWD", "NT$1,000"},
		{1234567, "TWD", "NT$1,234,567"},
		{-340, "TWD", "-NT$340"},
		{3400, "twd", "NT$3,400"},
		// Unknown currency: still grouped, still unscaled, and honest about not
		// knowing the symbol. Deliberately NOT an exponent table — that belongs
		// to the backend money package if it is ever needed.
		{3400, "JPY", "JPY 3,400"},
	} {
		if got := FormatMoney(tc.amount, tc.currency); got != tc.want {
			t.Errorf("FormatMoney(%d, %q) = %q, want %q", tc.amount, tc.currency, got, tc.want)
		}
	}
	if got := FormatMoney(3400, "TWD"); strings.Contains(got, ".") {
		t.Errorf("FormatMoney produced a decimal point (%q) — that asserts the opposite unit convention", got)
	}
}

func testAddress(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(order.ShippingAddress{
		RecipientName: "王小明", Phone: "0912345678",
		Line1: "信義路五段7號", City: "台北市", PostalCode: "110", Country: "TW",
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// testOrder builds an order with a discount, a shipping fee and a gift message —
// i.e. every optional branch of the renderer at once.
func testOrder(t *testing.T) *ent.Order {
	t.Helper()
	tier := "VIP"
	percent := int16(10)
	method := "黑貓宅急便"
	delivery := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	return &ent.Order{
		ID: 1234, ShopID: 1, MemberID: 7,
		Currency:       "TWD",
		SubtotalAmount: 6800, DiscountAmount: 680, ShippingFee: 120, TotalAmount: 6240,
		MemberTierName: &tier, DiscountPercent: &percent,
		ShippingMethodName: &method, RequestedDeliveryDate: &delivery,
		ShippingAddress: testAddress(t),
		CreatedAt:       time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC),
		Edges: ent.OrderEdges{Items: []*ent.OrderItem{{
			ProductTitle: "週年紀念花束", SkuCode: "BQ-ANNI-M",
			Quantity: 2, PriceAmount: 3400, Currency: "TWD",
			GiftMessage: "生日快樂",
		}}},
	}
}

func renderData(t *testing.T) RenderData {
	t.Helper()
	return RenderData{ShopName: "ONDO", Order: testOrder(t), Recipient: "buyer@example.com"}
}

// TestOrderPlacedShowsTheAmountComposition — never a single total. total_amount's
// arithmetic has been redefined twice (order-extras added shipping,
// tier-discount-application subtracted the discount), and printing the
// composition is the only place a reader can see which rule is in force.
func TestOrderPlacedShowsTheAmountComposition(t *testing.T) {
	body := RenderOrderPlaced(renderData(t)).TextBody
	for _, want := range []string{
		"商品小計：NT$6,800",
		"會員折扣（VIP 10%）：-NT$680",
		"運費：NT$120",
		"應付總額：NT$6,240",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body is missing %q:\n%s", want, body)
		}
	}
	// The identity the DB CHECK enforces, asserted on the rendered numbers.
	o := testOrder(t)
	if o.SubtotalAmount-o.DiscountAmount+o.ShippingFee != o.TotalAmount {
		t.Fatal("fixture violates subtotal − discount + shipping = total")
	}
}

func TestOrderPlacedIncludesItemsAddressAndExtras(t *testing.T) {
	body := RenderOrderPlaced(renderData(t)).TextBody
	for _, want := range []string{
		"#1234",
		"週年紀念花束（BQ-ANNI-M）",
		"數量 2 × NT$3,400 ＝ NT$6,800",
		"卡片訊息：生日快樂",
		"運送方式：黑貓宅急便",
		"指定配送日：2026-08-05",
		"收件人：王小明",
		"電話：0912345678",
		"信義路五段7號",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body is missing %q:\n%s", want, body)
		}
	}
}

// TestNoDiscountOmitsTheDiscountLine — "-NT$0" is noise that invites the reader
// to wonder what went wrong.
func TestNoDiscountOmitsTheDiscountLine(t *testing.T) {
	d := renderData(t)
	d.Order.DiscountAmount = 0
	d.Order.MemberTierName = nil
	d.Order.DiscountPercent = nil
	d.Order.TotalAmount = d.Order.SubtotalAmount + d.Order.ShippingFee

	body := RenderOrderPlaced(d).TextBody
	if strings.Contains(body, "折扣") {
		t.Errorf("a zero discount produced a discount line:\n%s", body)
	}
	if !strings.Contains(body, "商品小計：NT$6,800") || !strings.Contains(body, "應付總額：NT$6,920") {
		t.Errorf("composition wrong for a no-discount order:\n%s", body)
	}
}

// TestEmailsCarryNoLinksOrTokens — design D4. The order access token is a bearer
// credential with a 24h TTL; an email outlives it by years, and putting it there
// is how that token would quietly become the session guest-checkout's spec
// forbids. There is also no per-shop storefront URL in this system to link to.
func TestEmailsCarryNoLinksOrTokens(t *testing.T) {
	for name, msg := range map[string]Message{
		"placed": RenderOrderPlaced(renderData(t)),
		"paid":   RenderPaymentSucceeded(renderData(t)),
	} {
		t.Run(name, func(t *testing.T) {
			for _, forbidden := range []string{"http://", "https://", "www.", "eyJ"} {
				if strings.Contains(msg.TextBody, forbidden) {
					t.Errorf("body contains %q:\n%s", forbidden, msg.TextBody)
				}
			}
		})
	}
}

// TestEmailsShowNoPoints — design D8. points.Service and notify.Service are two
// independent subscribers to the same event with no ordering contract, so "have
// the points landed yet" has no answer at render time. The unit trap is the
// secondary reason: 34 points through a money formatter reads "NT$34".
func TestEmailsShowNoPoints(t *testing.T) {
	for name, msg := range map[string]Message{
		"placed": RenderOrderPlaced(renderData(t)),
		"paid":   RenderPaymentSucceeded(renderData(t)),
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(msg.TextBody, "點") {
				t.Errorf("body mentions points:\n%s", msg.TextBody)
			}
		})
	}
}

// TestPaymentSucceededStandsAlone — the two messages can arrive out of order or
// one can be lost, and neither carries a link back to anything (design D4), so
// each has to be a complete receipt on its own.
func TestPaymentSucceededStandsAlone(t *testing.T) {
	msg := RenderPaymentSucceeded(renderData(t))
	if !strings.Contains(msg.Subject, "付款成功") || !strings.Contains(msg.Subject, "#1234") {
		t.Errorf("subject = %q", msg.Subject)
	}
	for _, want := range []string{"付款金額：NT$6,240", "商品小計：NT$6,800", "應付總額：NT$6,240", "收件人：王小明"} {
		if !strings.Contains(msg.TextBody, want) {
			t.Errorf("body is missing %q:\n%s", want, msg.TextBody)
		}
	}
}

// TestTimestampCarriesItsOffset — the system stores no per-shop timezone, so a
// bare "21:00" would be a guess presented as a fact. A labelled offset cannot
// mislead.
func TestTimestampCarriesItsOffset(t *testing.T) {
	body := RenderOrderPlaced(renderData(t)).TextBody
	if !strings.Contains(body, "成立時間：2026-08-01 21:00（UTC+8）") {
		t.Errorf("timestamp is missing or unlabelled:\n%s", body)
	}
}

// TestUndecodableAddressStillSends — losing the address block beats losing the
// whole confirmation.
func TestUndecodableAddressStillSends(t *testing.T) {
	d := renderData(t)
	d.Order.ShippingAddress = json.RawMessage(`not json`)
	body := RenderOrderPlaced(d).TextBody
	if !strings.Contains(body, "應付總額：NT$6,240") {
		t.Errorf("a bad address blob broke the whole message:\n%s", body)
	}
}
