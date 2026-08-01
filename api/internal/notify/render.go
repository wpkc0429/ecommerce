package notify

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/order"
)

// ── money (design D8) ──────────────────────────────────────────────────

// FormatMoney renders an integer minor-unit amount for display.
//
// **This file contains no `/100` and no `*100`, and it must stay that way** —
// the same rule the admin SPA lives under. TWD's minor unit IS the dollar, so
// 3400 is NT$3,400. Two things are avoided on purpose:
//
//   - Any formatter that emits a decimal point. A trailing ".00" asserts the
//     opposite unit convention, and the next person to read the email will
//     "fix" it by adding a division.
//   - A per-currency exponent table. That belongs to the backend money package
//     if it is ever needed (product-catalog design D1); duplicating one here
//     would create a second answer to "how many minor units in a TWD".
//
// Unknown currencies fall back to "<CODE> 3,400" — grouped, still unscaled,
// still honest about the unit.
func FormatMoney(amount int64, currency string) string {
	neg := amount < 0
	if neg {
		amount = -amount
	}
	digits := group(strconv.FormatInt(amount, 10))

	var out string
	if strings.EqualFold(currency, "TWD") {
		out = "NT$" + digits
	} else {
		out = strings.ToUpper(currency) + " " + digits
	}
	if neg {
		out = "-" + out
	}
	return out
}

// group inserts thousands separators into a run of decimal digits.
func group(s string) string {
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// There is deliberately NO points formatter in this package (design D8). Points
// are not money — awarding is floor((subtotal − discount)/100), so a NT$3,400
// order earns 34 points, and 34 passed to FormatMoney reads "NT$34": right
// number, wrong unit, entirely plausible, and nothing errors. Emails do not show
// points at all (the ordering between the points subscriber and this one is
// undefined), so the safest formatter is the one that does not exist.

// ── time (design: the system stores no per-shop timezone) ──────────────

// displayLocation is a fixed +08:00 display convention, not a per-shop setting.
//
// order-extras established that this system has no per-shop timezone, which is
// why the delivery date is a bare calendar day. A timestamp still has to be
// printed as *something*, so it is printed in UTC+8 with the offset spelled out
// — a labelled offset cannot mislead, whereas a bare "21:00" would. When shops
// gain a timezone, this is the one place that changes.
var displayLocation = time.FixedZone("UTC+8", 8*60*60)

func formatTimestamp(t time.Time) string {
	return t.In(displayLocation).Format("2006-01-02 15:04") + "（UTC+8）"
}

// ── message rendering ──────────────────────────────────────────────────

// RenderData is everything the two templates read. It is assembled by
// Service.deliver from rows it has already loaded with an explicit shop_id.
type RenderData struct {
	ShopName string
	Order    *ent.Order
	// Recipient is the buyer's email address — the buyer, never the person named
	// on the shipping address (we hold no address for them, and who the flowers
	// are for is the buyer's business).
	Recipient string
}

// RenderOrderPlaced builds the order confirmation email.
func RenderOrderPlaced(d RenderData) Message {
	var b strings.Builder
	fmt.Fprintf(&b, "您好，\n\n感謝您在「%s」下單，您的訂單已成立。\n\n", d.ShopName)
	writeOrderBody(&b, d)
	b.WriteString("\n訂單成立後我們會盡快為您處理。若有任何問題，請與「" + d.ShopName + "」聯繫。\n")
	b.WriteString(autoReplyNotice)
	return Message{
		To:       d.Recipient,
		Subject:  fmt.Sprintf("【%s】訂單成立通知（訂單編號 #%d）", d.ShopName, d.Order.ID),
		TextBody: b.String(),
	}
}

// RenderPaymentSucceeded builds the payment confirmation email.
//
// It repeats the full order body rather than saying "see your previous email":
// the two messages can arrive out of order or one can be lost, and each has to
// stand alone (design D4 — the email carries no link back to anything).
func RenderPaymentSucceeded(d RenderData) Message {
	var b strings.Builder
	fmt.Fprintf(&b, "您好，\n\n我們已收到您在「%s」的訂單付款，感謝您的購買。\n\n", d.ShopName)
	fmt.Fprintf(&b, "付款金額：%s\n\n", FormatMoney(d.Order.TotalAmount, d.Order.Currency))
	writeOrderBody(&b, d)
	b.WriteString("\n我們會盡快為您安排出貨。若有任何問題，請與「" + d.ShopName + "」聯繫。\n")
	b.WriteString(autoReplyNotice)
	return Message{
		To:       d.Recipient,
		Subject:  fmt.Sprintf("【%s】付款成功通知（訂單編號 #%d）", d.ShopName, d.Order.ID),
		TextBody: b.String(),
	}
}

// autoReplyNotice is the closing line both messages share.
//
// It says "do not reply" because replies go to the platform's mailbox, which
// nobody reads and which cannot answer a question about this shop's flowers —
// see design D3 for why that mailbox is the merchant's rather than the
// platform's the moment shops gain a sender identity. Until then, saying so is
// the honest option; a Reply-To pointing nowhere would be worse.
const autoReplyNotice = "\n本信件由系統自動發送，請勿直接回覆。\n"

const sectionRule = "────────────────────────────\n"

// writeOrderBody renders the parts both messages share: the header facts, the
// lines, the amounts and the delivery details.
func writeOrderBody(b *strings.Builder, d RenderData) {
	o := d.Order

	fmt.Fprintf(b, "訂單編號：#%d\n", o.ID)
	fmt.Fprintf(b, "成立時間：%s\n", formatTimestamp(o.CreatedAt))
	if o.ShippingMethodName != nil && *o.ShippingMethodName != "" {
		fmt.Fprintf(b, "運送方式：%s\n", *o.ShippingMethodName)
	}
	if o.RequestedDeliveryDate != nil {
		// A Postgres `date`: a calendar day with no instant, so it is printed as
		// a bare day and never shifted into a timezone.
		fmt.Fprintf(b, "指定配送日：%s\n", o.RequestedDeliveryDate.Format("2006-01-02"))
	}

	b.WriteString("\n── 訂購內容 ──\n")
	for i, it := range o.Edges.Items {
		fmt.Fprintf(b, "%d. %s（%s）\n", i+1, it.ProductTitle, it.SkuCode)
		fmt.Fprintf(b, "   數量 %d × %s ＝ %s\n",
			it.Quantity,
			FormatMoney(it.PriceAmount, it.Currency),
			FormatMoney(it.PriceAmount*int64(it.Quantity), it.Currency))
		if msg := strings.TrimSpace(it.GiftMessage); msg != "" {
			// The buyer wrote this; showing it back is how they catch a typo
			// before the card is printed.
			fmt.Fprintf(b, "   卡片訊息：%s\n", msg)
		}
	}

	// ── amounts, composed (design D8) ──────────────────────────────────
	//
	// Never a single total. total_amount's arithmetic has been redefined twice
	// (order-extras added shipping, tier-discount-application subtracted the
	// discount) and the DB CHECK now enforces
	// subtotal − discount + shipping = total. Printing the composition is the
	// only place a customer or a merchant can see which rule is in force.
	b.WriteString("\n── 金額 ──\n")
	writeAmount(b, "商品小計", o.SubtotalAmount, o.Currency)
	if o.DiscountAmount != 0 {
		label := "折扣"
		if o.MemberTierName != nil && *o.MemberTierName != "" {
			if o.DiscountPercent != nil {
				label = fmt.Sprintf("會員折扣（%s %d%%）", *o.MemberTierName, *o.DiscountPercent)
			} else {
				label = fmt.Sprintf("會員折扣（%s）", *o.MemberTierName)
			}
		}
		writeAmount(b, label, -o.DiscountAmount, o.Currency)
	}
	writeAmount(b, "運費", o.ShippingFee, o.Currency)
	b.WriteString(sectionRule)
	writeAmount(b, "應付總額", o.TotalAmount, o.Currency)

	b.WriteString("\n── 收件資訊 ──\n")
	writeShippingAddress(b, o.ShippingAddress)
}

func writeAmount(b *strings.Builder, label string, amount int64, currency string) {
	fmt.Fprintf(b, "%s：%s\n", label, FormatMoney(amount, currency))
}

// writeShippingAddress renders orders.shipping_address.
//
// The jsonb blob is decoded through order.ShippingAddress — the one definition
// of that shape — rather than a local copy, so a field added there shows up here
// instead of silently vanishing from the email. An undecodable blob prints
// nothing rather than failing the send: the customer losing the address block
// beats the customer losing the whole confirmation.
func writeShippingAddress(b *strings.Builder, raw json.RawMessage) {
	var addr order.ShippingAddress
	if len(raw) == 0 || json.Unmarshal(raw, &addr) != nil {
		return
	}
	fmt.Fprintf(b, "收件人：%s\n", addr.RecipientName)
	fmt.Fprintf(b, "電話：%s\n", addr.Phone)
	line := addr.Line1
	if addr.Line2 != "" {
		line += " " + addr.Line2
	}
	full := strings.TrimSpace(strings.Join(nonEmpty(addr.PostalCode, addr.City, line), " "))
	if addr.Country != "" {
		full = strings.TrimSpace(full + " " + addr.Country)
	}
	fmt.Fprintf(b, "地址：%s\n", full)
}

func nonEmpty(vals ...string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
