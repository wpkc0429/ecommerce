package report_test

// 口徑的正確性只能靠一種方式證明：**刻意建立涵蓋各種邊界的訂單，再斷言數字**
// （design D16）。端點回 200 不算，schema 通過驗證也不算——一個把已取消訂單算進
// 營業額的報表，回的是完全合法的 200 與完全合法的 JSON。
//
// 每一筆 fixture 訂單都是為了一個特定的口徑決定而存在，見 seedCriteriaOrders。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/ent"
	entorder "ksdevworks/ecommerce/api/internal/ent/order"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/payment"
	"ksdevworks/ecommerce/api/internal/report"
	"ksdevworks/ecommerce/api/internal/shipping"
	"ksdevworks/ecommerce/api/internal/testutil"
)

const defaultTZ = "Asia/Taipei"

func asValidation(err error, target **report.ValidationError) bool { return errors.As(err, target) }

func isNotFound(err error) bool { return errors.Is(err, report.ErrNotFound) }

func onlyProductCurrency(t *testing.T, rep *report.ProductSalesReport, code string) []report.ProductRow {
	t.Helper()
	for _, c := range rep.Currencies {
		if c.Currency == code {
			return c.Items
		}
	}
	t.Fatalf("currency %s not present in the ranking", code)
	return nil
}

type fixtures struct {
	client   *ent.Client
	svc      *report.Service
	shopID   int
	otherID  int
	memberID int
}

func setup(t *testing.T) *fixtures {
	t.Helper()
	client := testutil.OpenDB(t)
	ctx := context.Background()

	shop := client.Shop.Create().SetName("Report Test Shop").SetStatus(1).SaveX(ctx)
	other := client.Shop.Create().SetName("Other Shop").SetStatus(1).SaveX(ctx)
	member := client.Member.Create().SetShopID(shop.ID).SetEmail("reports@t.dev").SetStatus(1).SaveX(ctx)

	return &fixtures{
		client:   client,
		svc:      &report.Service{Client: client, DefaultTimezone: defaultTZ},
		shopID:   shop.ID,
		otherID:  other.ID,
		memberID: member.ID,
	}
}

func (f *fixtures) setShopTimezone(t *testing.T, name string) {
	t.Helper()
	f.client.Shop.UpdateOneID(f.shopID).SetTimezone(name).SaveX(context.Background())
}

// orderSpec describes one fixture order. Amounts obey the DB CHECK
// (total = subtotal − discount + shipping), so these rows are indistinguishable
// from ones checkout would have produced.
type orderSpec struct {
	shopID            int
	createdAt         time.Time
	currency          string
	subtotal          int64
	discount          int64
	shippingFee       int64
	status            int16
	paymentStatus     int16
	fulfillmentStatus int16
	// items, if set, are (title, sku, unit price, qty) lines for the ranking.
	items []itemSpec
}

type itemSpec struct {
	title, sku string
	price      int64
	qty        int32
}

func (f *fixtures) newOrder(t *testing.T, spec orderSpec) *ent.Order {
	t.Helper()
	ctx := context.Background()
	shopID := spec.shopID
	if shopID == 0 {
		shopID = f.shopID
	}
	currency := spec.currency
	if currency == "" {
		currency = "TWD"
	}
	memberID := f.memberID
	if shopID != f.shopID {
		memberID = f.client.Member.Create().
			SetShopID(shopID).SetEmail("other-" + spec.createdAt.Format("150405.000000000") + "@t.dev").
			SetStatus(1).SaveX(ctx).ID
	}
	o := f.client.Order.Create().
		SetShopID(shopID).SetMemberID(memberID).
		SetStatus(spec.status).
		SetPaymentStatus(spec.paymentStatus).
		SetFulfillmentStatus(spec.fulfillmentStatus).
		SetCurrency(currency).
		SetSubtotalAmount(spec.subtotal).
		SetDiscountAmount(spec.discount).
		SetShippingFee(spec.shippingFee).
		SetTotalAmount(spec.subtotal - spec.discount + spec.shippingFee).
		SetShippingAddress(json.RawMessage(`{}`)).
		SetCreatedAt(spec.createdAt).
		SaveX(ctx)
	for _, it := range spec.items {
		f.client.OrderItem.Create().
			SetShopID(shopID).SetOrderID(o.ID).
			SetProductTitle(it.title).SetSkuCode(it.sku).
			SetQuantity(it.qty).SetPriceAmount(it.price).SetCurrency(currency).
			SaveX(ctx)
	}
	return o
}

// utc is shorthand for a UTC instant.
func utc(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

// midJuly is an instant that is 2026-07-15 in every zone this file uses, so
// tests that are not about time zones cannot accidentally depend on one.
func midJuly(h int) time.Time { return utc(2026, 7, 15, h, 0) }

// ── 口徑：計入什麼、不計入什麼 ────────────────────────────────────────

// seedCriteriaOrders creates one order per criteria decision. Each is annotated
// with the decision it exists to prove; deleting any one of them removes the
// only evidence for that rule.
func (f *fixtures) seedCriteriaOrders(t *testing.T) {
	t.Helper()
	paid := payment.OrderPaymentStatusPaid

	// ① Counted: not cancelled, paid, not returned. 1000 − 100 + 60 = 960.
	f.newOrder(t, orderSpec{
		createdAt: midJuly(6), subtotal: 1000, discount: 100, shippingFee: 60,
		status: order.StatusCreated, paymentStatus: paid, fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
		items: []itemSpec{{"玫瑰花束", "RO-M", 500, 2}},
	})
	// ② Counted, shipped — shipping does not stop an order counting.
	f.newOrder(t, orderSpec{
		createdAt: midJuly(7), subtotal: 2000, discount: 0, shippingFee: 0,
		status: order.StatusCreated, paymentStatus: paid, fulfillmentStatus: shipping.ShippedStatus,
		items: []itemSpec{{"百合花束", "LI-L", 2000, 1}},
	})
	// ③ Cancelled: excluded (stock restored, points clawed back — the system
	// already treats it as not having happened).
	f.newOrder(t, orderSpec{
		createdAt: midJuly(8), subtotal: 9999, discount: 0, shippingFee: 0,
		status: order.StatusCancelled, paymentStatus: order.PaymentStatusUnpaid,
		fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
		items:             []itemSpec{{"玫瑰花束", "RO-M", 9999, 1}},
	})
	// ④ Unpaid: excluded (may never be paid; counting it makes the report
	// permanently exceed the bank account).
	f.newOrder(t, orderSpec{
		createdAt: midJuly(9), subtotal: 7777, discount: 0, shippingFee: 0,
		status: order.StatusCreated, paymentStatus: order.PaymentStatusUnpaid,
		fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
		items:             []itemSpec{{"玫瑰花束", "RO-M", 7777, 1}},
	})
	// ⑤ Paid then returned: excluded from revenue, reported separately.
	f.newOrder(t, orderSpec{
		createdAt: midJuly(10), subtotal: 500, discount: 0, shippingFee: 40,
		status: order.StatusCreated, paymentStatus: paid, fulfillmentStatus: shipping.ReturnedStatus,
		items: []itemSpec{{"玫瑰花束", "RO-M", 500, 1}},
	})
	// ⑥ Outside the period, one day before — proves the lower bound.
	f.newOrder(t, orderSpec{
		createdAt: utc(2026, 6, 30, 6, 0), subtotal: 8888, discount: 0, shippingFee: 0,
		status: order.StatusCreated, paymentStatus: paid, fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
		items: []itemSpec{{"期間外", "OUT-1", 8888, 1}},
	})
	// ⑦ Outside the period, one day after — proves the upper bound.
	f.newOrder(t, orderSpec{
		createdAt: utc(2026, 8, 1, 6, 0), subtotal: 6666, discount: 0, shippingFee: 0,
		status: order.StatusCreated, paymentStatus: paid, fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
		items: []itemSpec{{"期間外", "OUT-2", 6666, 1}},
	})
	// ⑧ Another shop, inside the period, paid — proves tenant isolation. Raw
	// SQL bypasses the tenant interceptor entirely, so the explicit shop_id in
	// every WHERE is the ONLY defense here, not a second one.
	f.newOrder(t, orderSpec{
		shopID: f.otherID, createdAt: midJuly(11), subtotal: 5555, discount: 0, shippingFee: 0,
		status: order.StatusCreated, paymentStatus: paid, fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
		items: []itemSpec{{"別家的花", "OTHER-1", 5555, 1}},
	})
}

func july(t *testing.T) report.SalesParams {
	t.Helper()
	return report.SalesParams{
		From:    time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		To:      time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
		GroupBy: report.GroupByDay,
	}
}

func onlyCurrency(t *testing.T, rep *report.SalesReport, code string) report.CurrencySales {
	t.Helper()
	for _, c := range rep.Currencies {
		if c.Currency == code {
			return c
		}
	}
	t.Fatalf("currency %s not present; got %d currencies", code, len(rep.Currencies))
	return report.CurrencySales{}
}

func TestSalesCountsOnlyUncancelledPaidUnreturnedOrders(t *testing.T) {
	f := setup(t)
	f.seedCriteriaOrders(t)

	rep, err := f.svc.Sales(context.Background(), f.shopID, july(t))
	if err != nil {
		t.Fatalf("Sales: %v", err)
	}
	if len(rep.Currencies) != 1 {
		t.Fatalf("want 1 currency, got %d", len(rep.Currencies))
	}
	s := onlyCurrency(t, rep, "TWD").Summary

	// Counted: orders ① and ②.
	if s.OrderCount != 2 {
		t.Errorf("order_count = %d, want 2", s.OrderCount)
	}
	if s.SubtotalAmount != 3000 {
		t.Errorf("subtotal_amount = %d, want 3000 (1000+2000)", s.SubtotalAmount)
	}
	if s.DiscountAmount != 100 {
		t.Errorf("discount_amount = %d, want 100", s.DiscountAmount)
	}
	// The composition identity, on aggregates.
	if s.NetAmount != s.SubtotalAmount-s.DiscountAmount {
		t.Errorf("net_amount = %d, want subtotal−discount = %d", s.NetAmount, s.SubtotalAmount-s.DiscountAmount)
	}
	if s.ShippingFee != 60 {
		t.Errorf("shipping_fee = %d, want 60", s.ShippingFee)
	}
	if s.TotalAmount != s.NetAmount+s.ShippingFee {
		t.Errorf("total_amount = %d, want net+shipping = %d", s.TotalAmount, s.NetAmount+s.ShippingFee)
	}
	if s.TotalAmount != 2960 {
		t.Errorf("total_amount = %d, want 2960", s.TotalAmount)
	}

	// Returned: order ⑤ — excluded from revenue, present as its own line.
	if s.ReturnedOrderCount != 1 {
		t.Errorf("returned_order_count = %d, want 1", s.ReturnedOrderCount)
	}
	if s.ReturnedTotalAmount != 540 {
		t.Errorf("returned_total_amount = %d, want 540", s.ReturnedTotalAmount)
	}
	// The returned order's money must NOT have leaked into the revenue lines.
	if s.SubtotalAmount == 3500 {
		t.Error("the returned order's subtotal was counted toward revenue")
	}

	// Excluded classes.
	if s.CancelledOrderCount != 1 {
		t.Errorf("cancelled_order_count = %d, want 1", s.CancelledOrderCount)
	}
	if s.UnpaidOrderCount != 1 {
		t.Errorf("unpaid_order_count = %d, want 1", s.UnpaidOrderCount)
	}
}

// TestReconciliationPartitionSumsToEveryOrderInThePeriod is design D4: the four
// classes are mutually exclusive and exhaustive, so a merchant can verify the
// report against the order list instead of having to trust it.
//
// The exclusivity half matters most: a cancelled order is NECESSARILY unpaid
// (CancelOrder only proceeds while all three axes are at their initial values),
// so four independent counts would count it twice — and "the four numbers add
// up to one more than the order list says" is an error nobody spots.
func TestReconciliationPartitionSumsToEveryOrderInThePeriod(t *testing.T) {
	f := setup(t)
	f.seedCriteriaOrders(t)
	ctx := context.Background()

	rep, err := f.svc.Sales(ctx, f.shopID, july(t))
	if err != nil {
		t.Fatalf("Sales: %v", err)
	}
	var partition int
	for _, c := range rep.Currencies {
		s := c.Summary
		partition += s.OrderCount + s.ReturnedOrderCount + s.CancelledOrderCount + s.UnpaidOrderCount
	}

	// The independent truth: how many orders this shop created in July, counted
	// with no criteria at all and through a completely different code path
	// (ent, not the report's raw SQL). If both used the same query the equality
	// would be a tautology.
	loc, err := time.LoadLocation(defaultTZ)
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, loc)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, loc)
	total, err := f.client.Order.Query().
		Where(
			entorder.ShopIDEQ(f.shopID),
			entorder.CreatedAtGTE(start),
			entorder.CreatedAtLT(end),
		).
		Count(ctx)
	if err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if partition != total {
		t.Errorf("partition sums to %d but the shop created %d orders in the period — "+
			"the four classes are not exhaustive, or they overlap", partition, total)
	}
	if partition != 5 {
		t.Errorf("partition = %d, want 5 (2 counted + 1 returned + 1 cancelled + 1 unpaid)", partition)
	}
}

// ── 跨時區邊界（design D16，本 change 最重要的三條）────────────────────

// TestBucketsUseTheShopsLocalDay is assertion ① of design D16: an order placed
// at 22:00 UTC on 08-01 is 06:00 on 08-02 in Taipei, and MUST land in 08-02.
//
// If the implementation ever forgets AT TIME ZONE, this lands in 08-01 and the
// whole report is off by one day for every order placed after 16:00 UTC.
func TestBucketsUseTheShopsLocalDay(t *testing.T) {
	f := setup(t)
	f.setShopTimezone(t, "Asia/Taipei")
	f.newOrder(t, orderSpec{
		createdAt: utc(2026, 8, 1, 22, 0), subtotal: 1000, shippingFee: 0,
		status: order.StatusCreated, paymentStatus: payment.OrderPaymentStatusPaid,
		fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
	})

	rep, err := f.svc.Sales(context.Background(), f.shopID, report.SalesParams{
		From:    time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		To:      time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
		GroupBy: report.GroupByDay,
	})
	if err != nil {
		t.Fatalf("Sales: %v", err)
	}
	if rep.Timezone.Name != "Asia/Taipei" || rep.Timezone.Source != "shop" {
		t.Fatalf("timezone = %+v, want {Asia/Taipei shop}", rep.Timezone)
	}
	buckets := onlyCurrency(t, rep, "TWD").Buckets
	if len(buckets) != 1 {
		t.Fatalf("want exactly 1 bucket, got %d: %+v", len(buckets), buckets)
	}
	if buckets[0].Period != "2026-08-02" {
		t.Errorf("period = %q, want 2026-08-02 — 22:00Z on 08-01 is 06:00 on 08-02 in Taipei "+
			"(a 2026-08-01 answer means AT TIME ZONE is missing)", buckets[0].Period)
	}
}

// TestBucketsFollowTheShopsOwnZoneNotAConstant is assertion ② of design D16:
// the SAME instant, read by a New York shop, is the PREVIOUS day.
//
// Together with the test above this proves the conversion uses the shop's zone
// rather than any hard-coded one — either test alone passes for an
// implementation that always applies +08:00.
func TestBucketsFollowTheShopsOwnZoneNotAConstant(t *testing.T) {
	f := setup(t)
	f.setShopTimezone(t, "America/New_York")
	f.newOrder(t, orderSpec{
		createdAt: utc(2026, 8, 1, 22, 0), subtotal: 1000, shippingFee: 0,
		status: order.StatusCreated, paymentStatus: payment.OrderPaymentStatusPaid,
		fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
	})

	rep, err := f.svc.Sales(context.Background(), f.shopID, report.SalesParams{
		From:    time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC),
		To:      time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
		GroupBy: report.GroupByDay,
	})
	if err != nil {
		t.Fatalf("Sales: %v", err)
	}
	buckets := onlyCurrency(t, rep, "TWD").Buckets
	if len(buckets) != 1 {
		t.Fatalf("want exactly 1 bucket, got %d", len(buckets))
	}
	if buckets[0].Period != "2026-08-01" {
		t.Errorf("period = %q, want 2026-08-01 — 22:00Z is 18:00 EDT on 08-01", buckets[0].Period)
	}
}

// TestPeriodBoundsAndBucketsShareOneZone is assertion ③ of design D16, and it
// is the one that catches the subtlest bug: cutting buckets in the shop's zone
// while bounding the range in UTC produces an order that appears in a bucket
// outside the range it was selected by (or vanishes entirely).
//
// A single-day period of exactly 2026-08-02 in Taipei MUST contain the 22:00Z
// 08-01 order — its local day IS 08-02.
func TestPeriodBoundsAndBucketsShareOneZone(t *testing.T) {
	f := setup(t)
	f.setShopTimezone(t, "Asia/Taipei")
	f.newOrder(t, orderSpec{
		createdAt: utc(2026, 8, 1, 22, 0), subtotal: 1000, shippingFee: 0,
		status: order.StatusCreated, paymentStatus: payment.OrderPaymentStatusPaid,
		fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
	})
	// An order at 23:59:59 local on 08-02 (15:59:59Z) — inside.
	f.newOrder(t, orderSpec{
		createdAt: time.Date(2026, 8, 2, 15, 59, 59, 0, time.UTC), subtotal: 500, shippingFee: 0,
		status: order.StatusCreated, paymentStatus: payment.OrderPaymentStatusPaid,
		fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
	})
	// An order at exactly 00:00:00 local on 08-03 (16:00:00Z) — OUTSIDE. This is
	// the BETWEEN-vs-half-open case: `created_at BETWEEN a AND b` would include
	// it, and the bug would only ever show for orders placed at midnight sharp.
	f.newOrder(t, orderSpec{
		createdAt: time.Date(2026, 8, 2, 16, 0, 0, 0, time.UTC), subtotal: 9999, shippingFee: 0,
		status: order.StatusCreated, paymentStatus: payment.OrderPaymentStatusPaid,
		fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
	})

	oneDay := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	rep, err := f.svc.Sales(context.Background(), f.shopID, report.SalesParams{
		From: oneDay, To: oneDay, GroupBy: report.GroupByDay,
	})
	if err != nil {
		t.Fatalf("Sales: %v", err)
	}
	c := onlyCurrency(t, rep, "TWD")
	if len(c.Buckets) != 1 || c.Buckets[0].Period != "2026-08-02" {
		t.Fatalf("want one 2026-08-02 bucket, got %+v", c.Buckets)
	}
	if c.Summary.OrderCount != 2 {
		t.Errorf("order_count = %d, want 2 — the 22:00Z 08-01 order and the 15:59:59Z 08-02 order",
			c.Summary.OrderCount)
	}
	if c.Summary.SubtotalAmount != 1500 {
		t.Errorf("subtotal_amount = %d, want 1500; 10499 means midnight-sharp on the NEXT local day "+
			"was included (BETWEEN instead of a half-open range)", c.Summary.SubtotalAmount)
	}
}

// ── 回落標示 ──────────────────────────────────────────────────────────

func TestUnconfiguredShopFallsBackAndSaysSo(t *testing.T) {
	f := setup(t)
	rep, err := f.svc.Sales(context.Background(), f.shopID, july(t))
	if err != nil {
		t.Fatalf("Sales: %v", err)
	}
	if rep.Timezone.Name != defaultTZ {
		t.Errorf("timezone name = %q, want %q", rep.Timezone.Name, defaultTZ)
	}
	if rep.Timezone.Source != "default" {
		t.Errorf("timezone source = %q, want default — a fallback reported as the merchant's own "+
			"choice is exactly what the nullable column exists to prevent", rep.Timezone.Source)
	}
}

// A shop that explicitly chose the same string as the platform default must be
// reported as "shop", not "default". The arithmetic is identical; the sentence
// on screen must not be (design D1).
func TestShopThatChoseTheDefaultValueIsNotReportedAsFallback(t *testing.T) {
	f := setup(t)
	f.setShopTimezone(t, defaultTZ)
	rep, err := f.svc.Sales(context.Background(), f.shopID, july(t))
	if err != nil {
		t.Fatalf("Sales: %v", err)
	}
	if rep.Timezone.Source != "shop" {
		t.Errorf("source = %q, want shop", rep.Timezone.Source)
	}
}

// ── 多幣別 ────────────────────────────────────────────────────────────

func TestMultipleCurrenciesStayIndependent(t *testing.T) {
	f := setup(t)
	paid := payment.OrderPaymentStatusPaid
	f.newOrder(t, orderSpec{
		createdAt: midJuly(6), currency: "TWD", subtotal: 3400, shippingFee: 100,
		status: order.StatusCreated, paymentStatus: paid, fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
	})
	f.newOrder(t, orderSpec{
		createdAt: midJuly(7), currency: "JPY", subtotal: 5000, shippingFee: 0,
		status: order.StatusCreated, paymentStatus: paid, fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
	})

	rep, err := f.svc.Sales(context.Background(), f.shopID, july(t))
	if err != nil {
		t.Fatalf("Sales: %v", err)
	}
	if len(rep.Currencies) != 2 {
		t.Fatalf("want 2 currency blocks, got %d", len(rep.Currencies))
	}
	// Ordering is stated, not incidental (commerce-demo-seed design D8).
	if rep.Currencies[0].Currency != "JPY" || rep.Currencies[1].Currency != "TWD" {
		t.Errorf("currencies = [%s %s], want [JPY TWD] (ascending code)",
			rep.Currencies[0].Currency, rep.Currencies[1].Currency)
	}
	if got := onlyCurrency(t, rep, "TWD").Summary.TotalAmount; got != 3500 {
		t.Errorf("TWD total = %d, want 3500", got)
	}
	if got := onlyCurrency(t, rep, "JPY").Summary.TotalAmount; got != 5000 {
		t.Errorf("JPY total = %d, want 5000", got)
	}
	// The structural guarantee (design D13): there is no field anywhere in this
	// response that could hold 8500. Asserting it directly is impossible — the
	// point is that the type has no such field — so this asserts the observable
	// consequence: neither block was contaminated by the other.
	for _, c := range rep.Currencies {
		if c.Summary.TotalAmount == 8500 {
			t.Fatalf("%s block holds a cross-currency total", c.Currency)
		}
	}
}

// ── 分組與期間驗證 ────────────────────────────────────────────────────

func TestGroupByMonth(t *testing.T) {
	f := setup(t)
	paid := payment.OrderPaymentStatusPaid
	for _, d := range []time.Time{midJuly(6), midJuly(7), utc(2026, 8, 3, 6, 0)} {
		f.newOrder(t, orderSpec{
			createdAt: d, subtotal: 1000, shippingFee: 0,
			status: order.StatusCreated, paymentStatus: paid, fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
		})
	}
	rep, err := f.svc.Sales(context.Background(), f.shopID, report.SalesParams{
		From:    time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		To:      time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		GroupBy: report.GroupByMonth,
	})
	if err != nil {
		t.Fatalf("Sales: %v", err)
	}
	b := onlyCurrency(t, rep, "TWD").Buckets
	if len(b) != 2 {
		t.Fatalf("want 2 monthly buckets, got %d: %+v", len(b), b)
	}
	// Months report the first day of the month, so one shape serves both
	// granularities.
	if b[0].Period != "2026-07-01" || b[1].Period != "2026-08-01" {
		t.Errorf("periods = [%s %s], want [2026-07-01 2026-08-01]", b[0].Period, b[1].Period)
	}
	if b[0].OrderCount != 2 || b[1].OrderCount != 1 {
		t.Errorf("counts = [%d %d], want [2 1]", b[0].OrderCount, b[1].OrderCount)
	}
}

func TestSalesRejectsBadPeriods(t *testing.T) {
	f := setup(t)
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

	cases := []struct {
		name   string
		params report.SalesParams
	}{
		{"missing from", report.SalesParams{To: day(2026, 7, 31), GroupBy: report.GroupByDay}},
		{"missing to", report.SalesParams{From: day(2026, 7, 1), GroupBy: report.GroupByDay}},
		{"to before from", report.SalesParams{From: day(2026, 7, 31), To: day(2026, 7, 1), GroupBy: report.GroupByDay}},
		{"bad group_by", report.SalesParams{From: day(2026, 7, 1), To: day(2026, 7, 31), GroupBy: "week"}},
		{"too long for day", report.SalesParams{From: day(2025, 1, 1), To: day(2026, 12, 31), GroupBy: report.GroupByDay}},
		{"too long for month", report.SalesParams{From: day(2000, 1, 1), To: day(2026, 12, 31), GroupBy: report.GroupByMonth}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.Sales(context.Background(), f.shopID, tc.params)
			var ve *report.ValidationError
			if err == nil {
				t.Fatal("want a validation error, got nil")
			}
			if !asValidation(err, &ve) {
				t.Fatalf("want *report.ValidationError, got %T: %v", err, err)
			}
			if len(ve.Details) == 0 || ve.Details[0].Pointer == "" {
				t.Error("validation error carries no JSON pointer")
			}
		})
	}
}

// A period that spans exactly the day cap is accepted — off-by-one at the
// boundary would make a full year of daily figures unreachable.
func TestSalesAcceptsExactlyTheDayCap(t *testing.T) {
	f := setup(t)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := f.svc.Sales(context.Background(), f.shopID, report.SalesParams{
		From: from, To: from.AddDate(0, 0, 365), GroupBy: report.GroupByDay,
	}); err != nil {
		t.Fatalf("366-day period rejected: %v", err)
	}
}

func TestSalesUnknownShopIsNotFound(t *testing.T) {
	f := setup(t)
	_, err := f.svc.Sales(context.Background(), 999999, july(t))
	if err == nil {
		t.Fatal("want ErrNotFound, got nil")
	}
	if !isNotFound(err) {
		t.Fatalf("want report.ErrNotFound, got %v", err)
	}
}

// ── 商品銷售排行 ──────────────────────────────────────────────────────

func TestProductRankingUsesSnapshotsAndPreDiscountAmounts(t *testing.T) {
	f := setup(t)
	f.seedCriteriaOrders(t)

	rep, err := f.svc.Products(context.Background(), f.shopID, report.ProductParams{
		From: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Products: %v", err)
	}
	items := onlyProductCurrency(t, rep, "TWD")
	// Only the two counted orders contribute: 玫瑰花束 RO-M ×2 @500 = 1000, and
	// 百合花束 LI-L ×1 @2000 = 2000. Every excluded order also had items, and
	// none of them may appear.
	if len(items) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(items), items)
	}
	if items[0].SKUCode != "LI-L" || items[0].GrossAmount != 2000 || items[0].Quantity != 1 {
		t.Errorf("row 0 = %+v, want LI-L 2000 ×1 (highest gross first)", items[0])
	}
	// **Pre-discount**: order ① had a 100 discount, and RO-M's gross must still
	// be 1000. Apportioning the order-level discount back to lines would make
	// this 900 — a number that reconciles against no document.
	if items[1].SKUCode != "RO-M" || items[1].GrossAmount != 1000 || items[1].Quantity != 2 {
		t.Errorf("row 1 = %+v, want RO-M 1000 ×2 (gross is PRE-discount)", items[1])
	}
	for _, it := range items {
		if it.SKUCode == "OUT-1" || it.SKUCode == "OUT-2" || it.SKUCode == "OTHER-1" {
			t.Errorf("ranking leaked an out-of-period or cross-shop line: %+v", it)
		}
		if it.GrossAmount == 9999 || it.GrossAmount == 7777 {
			t.Errorf("ranking counted a cancelled or unpaid order: %+v", it)
		}
	}
}

// A renamed product produces two rows, and that is the honest answer: those
// order lines really do carry two different names (design D8). Merging them
// would require product_id, which order_items' own schema says must never be
// depended on to resolve.
func TestRenamedProductRanksAsTwoRows(t *testing.T) {
	f := setup(t)
	paid := payment.OrderPaymentStatusPaid
	f.newOrder(t, orderSpec{
		createdAt: midJuly(6), subtotal: 500, shippingFee: 0,
		status: order.StatusCreated, paymentStatus: paid, fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
		items: []itemSpec{{"週年花束", "BQ-ANNI-M", 500, 1}},
	})
	f.newOrder(t, orderSpec{
		createdAt: midJuly(7), subtotal: 500, shippingFee: 0,
		status: order.StatusCreated, paymentStatus: paid, fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
		items: []itemSpec{{"週年紀念花束", "BQ-ANNI-M", 500, 1}},
	})
	rep, err := f.svc.Products(context.Background(), f.shopID, report.ProductParams{
		From: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Products: %v", err)
	}
	if got := len(onlyProductCurrency(t, rep, "TWD")); got != 2 {
		t.Errorf("want 2 rows for the same SKU under two titles, got %d", got)
	}
}

// The ordering must be fully determined, including for rows whose gross and
// quantity are identical — "usually the same" is not an order.
func TestProductRankingOrderIsDeterministic(t *testing.T) {
	f := setup(t)
	paid := payment.OrderPaymentStatusPaid
	f.newOrder(t, orderSpec{
		createdAt: midJuly(6), subtotal: 3000, shippingFee: 0,
		status: order.StatusCreated, paymentStatus: paid, fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
		items: []itemSpec{
			{"甲", "ZZZ-1", 1000, 1},
			{"乙", "AAA-1", 1000, 1},
			{"丙", "MMM-1", 1000, 1},
		},
	})
	params := report.ProductParams{
		From: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
	}
	var first []string
	for i := 0; i < 3; i++ {
		rep, err := f.svc.Products(context.Background(), f.shopID, params)
		if err != nil {
			t.Fatalf("Products: %v", err)
		}
		var got []string
		for _, it := range onlyProductCurrency(t, rep, "TWD") {
			got = append(got, it.SKUCode)
		}
		if i == 0 {
			first = got
			// Tiebreak is sku_code ascending.
			if len(got) != 3 || got[0] != "AAA-1" || got[1] != "MMM-1" || got[2] != "ZZZ-1" {
				t.Fatalf("order = %v, want [AAA-1 MMM-1 ZZZ-1]", got)
			}
			continue
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("run %d differs from run 0: %v vs %v", i, got, first)
			}
		}
	}
}

func TestProductLimitAppliesPerCurrency(t *testing.T) {
	f := setup(t)
	paid := payment.OrderPaymentStatusPaid
	for _, cur := range []string{"TWD", "JPY"} {
		f.newOrder(t, orderSpec{
			createdAt: midJuly(6), currency: cur, subtotal: 6000, shippingFee: 0,
			status: order.StatusCreated, paymentStatus: paid, fulfillmentStatus: order.FulfillmentStatusUnfulfilled,
			items: []itemSpec{
				{"A", cur + "-A", 3000, 1}, {"B", cur + "-B", 2000, 1}, {"C", cur + "-C", 1000, 1},
			},
		})
	}
	rep, err := f.svc.Products(context.Background(), f.shopID, report.ProductParams{
		From:  time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		To:    time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
		Limit: 2,
	})
	if err != nil {
		t.Fatalf("Products: %v", err)
	}
	if len(rep.Currencies) != 2 {
		t.Fatalf("want 2 currencies, got %d", len(rep.Currencies))
	}
	for _, c := range rep.Currencies {
		if len(c.Items) != 2 {
			t.Errorf("%s has %d rows, want 2 — the limit must apply per currency, "+
				"not to the whole result (a currency with larger numbers would otherwise crowd out the other)",
				c.Currency, len(c.Items))
		}
	}
}
