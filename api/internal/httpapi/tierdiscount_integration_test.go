package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"ksdevworks/ecommerce/api/internal/ent"
	entmembertier "ksdevworks/ecommerce/api/internal/ent/membertier"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/shipping"
)

// Change tier-discount-application: member_tiers.discount_percent stops being a
// column nobody reads and starts changing what the customer pays.
//
// These reuse guestEnv for the same reason change order-extras did — it is the
// only harness wiring member checkout, guest checkout, payments, shipping and
// points side by side, which is what "both entry points behave identically" and
// "points follow the discount" have to be proved against.

// ── fixtures ───────────────────────────────────────────────────────────

// newTier creates a member tier. percent < 0 means "this tier grants nothing"
// (discount_percent NULL), which is a distinct case from percent == 0.
func (e *guestEnv) newTier(t *testing.T, shopID int, name string, minPoints int32, percent int) int {
	t.Helper()
	e.seq++
	create := e.client.MemberTier.Create().
		SetShopID(shopID).SetName(name).SetMinPoints(minPoints)
	if percent >= 0 {
		create.SetDiscountPercent(int16(percent))
	}
	return create.SaveX(context.Background()).ID
}

// putInTier sets members.level_id directly, the way points.Service's recompute
// would have left it. Doing it here rather than by earning points keeps the
// discount assertions independent of the award ratio.
func (e *guestEnv) putInTier(t *testing.T, memberID, tierID int) {
	t.Helper()
	e.client.Member.UpdateOneID(memberID).SetLevelID(tierID).SaveX(context.Background())
}

func (e *guestEnv) reloadOrder(t *testing.T, orderID int) *ent.Order {
	t.Helper()
	o, err := e.client.Order.Get(context.Background(), orderID)
	if err != nil {
		t.Fatalf("reload order %d: %v", orderID, err)
	}
	return o
}

// discountOf pulls the discount snapshot out of an order response body. Every
// caller checks all three at once on purpose: they are one snapshot, and a test
// that asserted only the amount would not notice a missing tier name.
func discountOf(t *testing.T, body map[string]any) (amount float64, percent any, tierName any) {
	t.Helper()
	amt, ok := body["discount_amount"].(float64)
	if !ok {
		t.Fatalf("order response must carry discount_amount: %+v", body)
	}
	return amt, body["discount_percent"], body["member_tier_name"]
}

// assertComposes is the four-part identity, checked on the wire rather than in
// the database — a consumer reading these keys must be able to add them up.
func assertComposes(t *testing.T, body map[string]any) {
	t.Helper()
	subtotal, fee, total := amountsOf(t, body)
	discount, _, _ := discountOf(t, body)
	if total != subtotal-discount+fee {
		t.Fatalf("amounts do not compose: %v - %v + %v != %v", subtotal, discount, fee, total)
	}
}

// ── the discount is actually applied ───────────────────────────────────

// Scenario: 持有折扣等級的顧客結帳 (spec tier-discount-application).
func TestMemberCheckoutAppliesTierDiscount(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 10)
	methodID := e.newShippingMethod(t, e.shopA, "當日配送", 150, true)
	tierID := e.newTier(t, e.shopA, "金卡會員", 100, 10)
	e.putInTier(t, e.memberA1, tierID)

	code, body := e.memberCheckout(t, e.memberA1, skuID, 1, map[string]any{"shipping_method_id": methodID})
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %+v", code, body)
	}
	subtotal, fee, total := amountsOf(t, body)
	discount, percent, tierName := discountOf(t, body)
	// 3400 - 340 + 150. The fee is NOT discounted (design D2).
	if subtotal != 3400 || discount != 340 || fee != 150 || total != 3210 {
		t.Fatalf("want 3400/-340/+150 = 3210, got %v/-%v/+%v = %v", subtotal, discount, fee, total)
	}
	if percent != float64(10) || tierName != "金卡會員" {
		t.Fatalf("the tier snapshot must name the rate and the tier: %+v / %+v", percent, tierName)
	}
	assertComposes(t, body)
}

// Scenario: 不屬於任何等級的顧客結帳 — the pre-change behavior, byte for byte.
func TestCheckoutWithoutTierIsUnchanged(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 10)
	// A tier exists in the shop; the customer simply does not hold it.
	e.newTier(t, e.shopA, "金卡會員", 100, 10)

	code, body := e.memberCheckout(t, e.memberA1, skuID, 1, nil)
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %+v", code, body)
	}
	subtotal, fee, total := amountsOf(t, body)
	discount, percent, tierName := discountOf(t, body)
	if subtotal != 3400 || discount != 0 || fee != 0 || total != 3400 {
		t.Fatalf("want an undiscounted 3400, got %v/-%v/+%v = %v", subtotal, discount, fee, total)
	}
	if percent != nil || tierName != nil {
		t.Fatalf("no tier held must leave both snapshot fields null: %+v / %+v", percent, tierName)
	}
}

// Scenario: 等級存在但未設定折扣 — "in a tier that grants nothing" must stay
// distinguishable from "in no tier", or an audit can never explain a 0.
func TestCheckoutInATierWithNoDiscountSnapshotsZeroNotNull(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 10)
	tierID := e.newTier(t, e.shopA, "一般會員", 0, -1) // discount_percent NULL
	e.putInTier(t, e.memberA1, tierID)

	code, body := e.memberCheckout(t, e.memberA1, skuID, 1, nil)
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %+v", code, body)
	}
	discount, percent, tierName := discountOf(t, body)
	if discount != 0 || percent != float64(0) || tierName != "一般會員" {
		t.Fatalf("want 0 / 0 / 一般會員, got %v / %+v / %+v", discount, percent, tierName)
	}
}

// The rounding rule, proved through the real HTTP path rather than only in the
// order package's unit test: 3333 @ 10% is 333.3, and the customer is charged
// 3000, not 2999 (design D4).
func TestCheckoutDiscountRoundingIsTruncationEndToEnd(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3333, "TWD", 10)
	tierID := e.newTier(t, e.shopA, "金卡會員", 100, 10)
	e.putInTier(t, e.memberA1, tierID)

	code, body := e.memberCheckout(t, e.memberA1, skuID, 1, nil)
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %+v", code, body)
	}
	discount, _, _ := discountOf(t, body)
	_, _, total := amountsOf(t, body)
	if discount != 333 || total != 3000 {
		t.Fatalf("3333 @ 10%% must truncate to 333 (total 3000), got %v (total %v)", discount, total)
	}
	assertComposes(t, body)
}

// A 100% tier is inside the CHECK's range, so it has to work rather than merely
// not crash: the goods are free, the courier still is not.
func TestCheckoutWithFullDiscountLeavesOnlyTheShippingFee(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 10)
	methodID := e.newShippingMethod(t, e.shopA, "當日配送", 150, true)
	tierID := e.newTier(t, e.shopA, "招待", 0, 100)
	e.putInTier(t, e.memberA1, tierID)

	code, body := e.memberCheckout(t, e.memberA1, skuID, 1, map[string]any{"shipping_method_id": methodID})
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %+v", code, body)
	}
	subtotal, fee, total := amountsOf(t, body)
	discount, _, _ := discountOf(t, body)
	if subtotal != 3400 || discount != 3400 || fee != 150 || total != 150 {
		t.Fatalf("want the fee alone, got %v/-%v/+%v = %v", subtotal, discount, fee, total)
	}
}

// ── both entry points, one rule (spec order-management) ────────────────

// Scenario: 回頭訪客套用其已累積的等級折扣 (design D9).
//
// This is the decisive one for the driving consumer: ONDO has no login UI at
// all, so if the discount only applied to authenticated member checkout the
// feature would not exist for the first external merchant.
func TestGuestCheckoutAppliesTheIdentitysTierDiscount(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 20)
	tierID := e.newTier(t, e.shopA, "銀卡會員", 30, 5)

	// First visit: a brand-new email holds no tier, so nothing is discounted.
	code, first := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		guestBody("returning@t.dev", guestItem(skuID, 1)))
	if code != http.StatusCreated {
		t.Fatalf("first guest checkout: %d %+v", code, first)
	}
	firstOrder := first["order"].(map[string]any)
	if d, p, n := discountOf(t, firstOrder); d != 0 || p != nil || n != nil {
		t.Fatalf("a brand-new guest identity must get no discount, got %v/%+v/%+v", d, p, n)
	}

	// That visit created the identity. Promote it the way earning points would.
	identity := e.identity(t, e.shopA, "returning@t.dev")
	e.putInTier(t, identity.ID, tierID)

	// Second visit, same email: the tier now applies.
	code, second := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		guestBody("returning@t.dev", guestItem(skuID, 1)))
	if code != http.StatusCreated {
		t.Fatalf("second guest checkout: %d %+v", code, second)
	}
	secondOrder := second["order"].(map[string]any)
	discount, percent, tierName := discountOf(t, secondOrder)
	if discount != 170 || percent != float64(5) || tierName != "銀卡會員" {
		t.Fatalf("returning guest must get the tier discount, got %v/%+v/%+v", discount, percent, tierName)
	}
	assertComposes(t, secondOrder)
}

// Scenario: 兩條下單路徑套用相同規則. Same identity, same items, same tier —
// the two entry points must not be able to disagree.
func TestGuestAndMemberCheckoutProduceIdenticalDiscounts(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3801, "TWD", 20) // 5% of 3801 is 190.05
	tierID := e.newTier(t, e.shopA, "銀卡會員", 30, 5)
	e.putInTier(t, e.memberA1, tierID)

	code, memberBody := e.memberCheckout(t, e.memberA1, skuID, 1, nil)
	if code != http.StatusCreated {
		t.Fatalf("member checkout: %d %+v", code, memberBody)
	}

	// The guest path resolves the same identity from the same email.
	memberEmail := *e.client.Member.GetX(context.Background(), e.memberA1).Email
	code, guestBodyResp := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		guestBody(memberEmail, guestItem(skuID, 1)))
	if code != http.StatusCreated {
		t.Fatalf("guest checkout: %d %+v", code, guestBodyResp)
	}
	guestOrder := guestBodyResp["order"].(map[string]any)

	mDiscount, mPercent, mTier := discountOf(t, memberBody)
	gDiscount, gPercent, gTier := discountOf(t, guestOrder)
	if mDiscount != gDiscount || mPercent != gPercent || mTier != gTier {
		t.Fatalf("paths disagree: member %v/%+v/%+v vs guest %v/%+v/%+v",
			mDiscount, mPercent, mTier, gDiscount, gPercent, gTier)
	}
	if mDiscount != 190 {
		t.Fatalf("5%% of 3801 must truncate to 190, got %v", mDiscount)
	}
}

// The discount is the server's decision. A body key naming one must do nothing —
// otherwise the cheapest attack on this feature is a request field.
func TestCheckoutIgnoresClientSuppliedDiscountKeys(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 10)

	code, body := e.memberCheckout(t, e.memberA1, skuID, 1, map[string]any{
		"discount_amount":  3000,
		"discount_percent": 99,
	})
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %+v", code, body)
	}
	discount, percent, _ := discountOf(t, body)
	if discount != 0 || percent != nil {
		t.Fatalf("client-supplied discount keys must be ignored, got %v/%+v", discount, percent)
	}
}

// ── the snapshot never moves (design D8) ───────────────────────────────

func TestOrderDiscountSnapshotSurvivesTierChanges(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 10)
	tierID := e.newTier(t, e.shopA, "金卡會員", 100, 10)
	e.putInTier(t, e.memberA1, tierID)

	code, body := e.memberCheckout(t, e.memberA1, skuID, 1, nil)
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %+v", code, body)
	}
	orderID := int(body["id"].(float64))
	before := e.reloadOrder(t, orderID)

	assertUnchanged := func(label string) {
		t.Helper()
		after := e.reloadOrder(t, orderID)
		if after.DiscountAmount != before.DiscountAmount ||
			after.TotalAmount != before.TotalAmount ||
			after.SubtotalAmount != before.SubtotalAmount {
			t.Fatalf("%s changed the order's amounts: %d/-%d = %d became %d/-%d = %d", label,
				before.SubtotalAmount, before.DiscountAmount, before.TotalAmount,
				after.SubtotalAmount, after.DiscountAmount, after.TotalAmount)
		}
		if after.DiscountPercent == nil || *after.DiscountPercent != 10 {
			t.Fatalf("%s changed the rate snapshot: %+v", label, after.DiscountPercent)
		}
		if after.MemberTierName == nil || *after.MemberTierName != "金卡會員" {
			t.Fatalf("%s changed the tier name snapshot: %+v", label, after.MemberTierName)
		}
	}

	ctx := context.Background()
	// 1. The merchant re-prices the tier.
	e.client.MemberTier.UpdateOneID(tierID).SetDiscountPercent(20).SaveX(ctx)
	assertUnchanged("re-pricing the tier")

	// 2. The merchant renames it.
	e.client.MemberTier.UpdateOneID(tierID).SetName("白金會員").SaveX(ctx)
	assertUnchanged("renaming the tier")

	// 3. The customer drops out of it.
	e.client.Member.UpdateOneID(e.memberA1).ClearLevelID().SaveX(ctx)
	assertUnchanged("clearing the customer's level")

	// 4. The tier is deleted outright (members.level_id is ON DELETE SET NULL).
	e.client.MemberTier.DeleteOneID(tierID).ExecX(ctx)
	if n, _ := e.client.MemberTier.Query().Where(entmembertier.IDEQ(tierID)).Count(ctx); n != 0 {
		t.Fatal("tier should be gone")
	}
	assertUnchanged("deleting the tier")
}

// ── the database is the arbiter (design D5) ────────────────────────────

func TestOrderAmountConstraintsRejectImpossibleDiscounts(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()

	base := func() *ent.OrderCreate {
		return e.client.Order.Create().
			SetShopID(e.shopA).SetMemberID(e.memberA1).
			SetStatus(order.StatusCreated).
			SetPaymentStatus(order.PaymentStatusUnpaid).
			SetFulfillmentStatus(order.FulfillmentStatusUnfulfilled).
			SetCurrency("TWD").
			SetShippingAddress([]byte(`{}`))
	}

	cases := []struct {
		label string
		build func() *ent.OrderCreate
	}{
		{"total ignores the discount", func() *ent.OrderCreate {
			return base().SetSubtotalAmount(1000).SetDiscountAmount(100).SetShippingFee(0).SetTotalAmount(1000)
		}},
		{"discount exceeds the subtotal", func() *ent.OrderCreate {
			return base().SetSubtotalAmount(1000).SetDiscountAmount(1500).SetShippingFee(500).SetTotalAmount(0)
		}},
		{"negative discount", func() *ent.OrderCreate {
			return base().SetSubtotalAmount(1000).SetDiscountAmount(-100).SetShippingFee(0).SetTotalAmount(1100)
		}},
		{"rate out of range", func() *ent.OrderCreate {
			return base().SetSubtotalAmount(1000).SetDiscountAmount(0).SetShippingFee(0).SetTotalAmount(1000).
				SetDiscountPercent(200).SetMemberTierName("壞掉的等級")
		}},
		{"half a tier snapshot: a name with no rate", func() *ent.OrderCreate {
			return base().SetSubtotalAmount(1000).SetDiscountAmount(0).SetShippingFee(0).SetTotalAmount(1000).
				SetMemberTierName("金卡會員")
		}},
		{"half a tier snapshot: a rate with no name", func() *ent.OrderCreate {
			return base().SetSubtotalAmount(1000).SetDiscountAmount(0).SetShippingFee(0).SetTotalAmount(1000).
				SetDiscountPercent(10)
		}},
	}
	for _, c := range cases {
		if _, err := c.build().Save(ctx); err == nil {
			t.Errorf("%s: expected the database to refuse this row", c.label)
		}
	}

	// ...and the shapes that must still be accepted.
	if _, err := base().SetSubtotalAmount(1000).SetDiscountAmount(100).SetShippingFee(50).SetTotalAmount(950).
		SetDiscountPercent(10).SetMemberTierName("金卡會員").Save(ctx); err != nil {
		t.Fatalf("a well-formed discounted order must be accepted: %v", err)
	}
	if _, err := base().SetSubtotalAmount(1000).SetShippingFee(50).SetTotalAmount(1050).Save(ctx); err != nil {
		t.Fatalf("an undiscounted order must still be accepted: %v", err)
	}
}

// ── points follow the discount (spec member-tiers-and-points) ──────────

func TestPointsAwardedOnTheDiscountedSubtotal(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3800, "TWD", 10)
	methodID := e.newShippingMethod(t, e.shopA, "當日配送", 200, true)
	tierID := e.newTier(t, e.shopA, "銀卡會員", 30, 5)

	code, body := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		guestBody("points-basis@t.dev", guestItem(skuID, 1)))
	if code != http.StatusCreated {
		t.Fatalf("seed identity: %d %+v", code, body)
	}
	identity := e.identity(t, e.shopA, "points-basis@t.dev")
	e.putInTier(t, identity.ID, tierID)

	code, second := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		map[string]any{
			"email":              "points-basis@t.dev",
			"items":              []map[string]any{guestItem(skuID, 1)},
			"shipping_address":   guestBody("x", nil)["shipping_address"],
			"shipping_method_id": methodID,
		})
	if code != http.StatusCreated {
		t.Fatalf("discounted checkout: %d %+v", code, second)
	}
	o := second["order"].(map[string]any)
	discount, _, _ := discountOf(t, o)
	if discount != 190 { // 5% of 3800
		t.Fatalf("want a 190 discount, got %v", discount)
	}

	before := e.client.Member.GetX(context.Background(), identity.ID).Points
	e.payAndConfirm(t, second["order_access_token"].(string))
	after := e.client.Member.GetX(context.Background(), identity.ID).Points

	// 3800 - 190 = 3610 → 36 points.
	// The two wrong answers are 38 (ignoring the discount) and 40 (using the
	// payable total). All three differ, so this assertion actually discriminates.
	const want = 36
	if got := after - before; got != want {
		t.Fatalf("award basis must be the discounted subtotal: got %d, want %d "+
			"(list-price basis would be 38, payable-total basis 40)", got, want)
	}
}

// The award rule must reproduce history exactly: an order shaped like every row
// that predates this change (discount_amount 0) awards what it always did.
func TestPointsAwardOnPreDiscountShapedOrderIsUnchanged(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 1290, "TWD", 10)

	code, body := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		guestBody("legacy-shape@t.dev", guestItem(skuID, 1)))
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %+v", code, body)
	}
	o := body["order"].(map[string]any)
	if d, _, _ := discountOf(t, o); d != 0 {
		t.Fatalf("this fixture must be shaped like a pre-change row, got discount %v", d)
	}
	identity := e.identity(t, e.shopA, "legacy-shape@t.dev")
	e.payAndConfirm(t, body["order_access_token"].(string))

	// floor(1290 / 100) — the exact number member-tiers-and-points' own spec
	// scenario has asserted since the day it shipped.
	if got := e.client.Member.GetX(context.Background(), identity.ID).Points; got != 12 {
		t.Fatalf("a pre-change-shaped order must still award 12 points, got %d", got)
	}
}

// A returned discounted order gives back exactly what it gave out — taken from
// the award row, never recomputed from a rate the merchant may have since edited.
func TestClawbackOfADiscountedOrderUsesTheAwardRow(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3800, "TWD", 10)
	tierID := e.newTier(t, e.shopA, "銀卡會員", 30, 5)
	ctx := context.Background()

	code, body := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		guestBody("clawback-discount@t.dev", guestItem(skuID, 1)))
	if code != http.StatusCreated {
		t.Fatalf("seed identity: %d %+v", code, body)
	}
	identity := e.identity(t, e.shopA, "clawback-discount@t.dev")
	e.putInTier(t, identity.ID, tierID)

	code, second := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		guestBody("clawback-discount@t.dev", guestItem(skuID, 1)))
	if code != http.StatusCreated {
		t.Fatalf("discounted checkout: %d %+v", code, second)
	}
	orderID := int(second["order"].(map[string]any)["id"].(float64))
	before := e.client.Member.GetX(ctx, identity.ID).Points
	e.payAndConfirm(t, second["order_access_token"].(string))
	awarded := e.client.Member.GetX(ctx, identity.ID).Points - before
	if awarded != 36 { // floor((3800 - 190) / 100)
		t.Fatalf("want 36 points awarded, got %d", awarded)
	}

	// The merchant re-prices the tier before the return — a clawback that
	// recomputed would now take back 34 instead of 36.
	e.client.MemberTier.UpdateOneID(tierID).SetDiscountPercent(10).SaveX(ctx)

	tracking := "TRK-1"
	sh, err := e.shippingSvc.CreateShipment(ctx, e.shopA, orderID, "黑貓宅急便", &tracking)
	if err != nil {
		t.Fatalf("create shipment: %v", err)
	}
	if _, err := e.shippingSvc.AdvanceShipment(ctx, e.shopA, orderID, sh.ID, shipping.ReturnedStatus); err != nil {
		t.Fatalf("advance to returned: %v", err)
	}
	if got := e.client.Member.GetX(ctx, identity.ID).Points; got != before {
		t.Fatalf("clawback must return the balance to %d, got %d", before, got)
	}
}

// ── idempotency is not weakened (design D11) ───────────────────────────

// A replay must hand back the snapshot, not a fresh calculation — even after the
// tier that priced the order has been re-priced.
func TestIdempotentReplayReturnsTheOriginalDiscountSnapshot(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 10)
	tierID := e.newTier(t, e.shopA, "金卡會員", 100, 10)
	ctx := context.Background()

	code, seed := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		guestBody("replay-discount@t.dev", guestItem(skuID, 1)))
	if code != http.StatusCreated {
		t.Fatalf("seed identity: %d %+v", code, seed)
	}
	identity := e.identity(t, e.shopA, "replay-discount@t.dev")
	e.putInTier(t, identity.ID, tierID)

	body := guestBody("replay-discount@t.dev", guestItem(skuID, 1))
	const key = "tier-discount-replay-key"
	code, first, _ := e.guestIdem(t, key, body)
	if code != http.StatusCreated {
		t.Fatalf("first keyed checkout: %d %+v", code, first)
	}
	firstOrder := first["order"].(map[string]any)
	if d, _, _ := discountOf(t, firstOrder); d != 340 {
		t.Fatalf("want a 340 discount on the first attempt, got %v", d)
	}

	// Everything that could change the answer, changes.
	e.client.MemberTier.UpdateOneID(tierID).SetDiscountPercent(50).SaveX(ctx)
	e.client.Member.UpdateOneID(identity.ID).ClearLevelID().SaveX(ctx)

	code, replay, hdr := e.guestIdem(t, key, body)
	if code != http.StatusCreated {
		t.Fatalf("replay: %d %+v", code, replay)
	}
	replayOrder := replay["order"].(map[string]any)
	if hdr.Get(httpapi.IdempotencyReplayedHeader) != "true" {
		t.Fatalf("replay must carry the diagnostic header, got %q", hdr.Get(httpapi.IdempotencyReplayedHeader))
	}
	if replayOrder["id"] != firstOrder["id"] {
		t.Fatalf("replay must be the same order: %v vs %v", replayOrder["id"], firstOrder["id"])
	}
	d, p, n := discountOf(t, replayOrder)
	if d != 340 || p != float64(10) || n != "金卡會員" {
		t.Fatalf("replay must return the original snapshot, got %v/%+v/%+v", d, p, n)
	}
}

// ── concurrency is not weakened (design D8) ────────────────────────────

// The mirror of TestGuestCheckoutConcurrentOversellingProtection, with every
// buyer holding a discount tier: the extra arithmetic must not have introduced a
// wait point inside the transaction, and every winner's discount must be right.
//
// Run with -race.
func TestConcurrentDiscountedCheckoutsDoNotOversell(t *testing.T) {
	e := newGuestEnv(t)
	const stock, attempts = 5, 10
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", stock)
	tierID := e.newTier(t, e.shopA, "金卡會員", 100, 10)

	// Pre-create every identity already sitting in the tier, so the race is purely
	// about stock — not about creating identities, and not about whether the tier
	// was resolved before or after the contended write.
	emails := make([]string, attempts)
	for i := range emails {
		emails[i] = fmt.Sprintf("concurrent-discount-%d@t.dev", i)
		e.putInTier(t, e.newPasswordlessMember(t, emails[i]), tierID)
	}

	var mu sync.Mutex
	var created, refused int
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, raw, err := e.guestRequest(e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
				guestBody(emails[i], guestItem(skuID, 1)))
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			switch code {
			case http.StatusCreated:
				created++
			case http.StatusUnprocessableEntity:
				refused++
			default:
				t.Errorf("unexpected status %d: %s", code, raw)
			}
		}(i)
	}
	wg.Wait()

	if created != stock || refused != attempts-stock {
		t.Fatalf("want exactly %d created and %d refused, got %d/%d", stock, attempts-stock, created, refused)
	}
	if left := e.client.ProductSKU.GetX(context.Background(), skuID).StockQty; left != 0 {
		t.Fatalf("stock must land on exactly 0, got %d", left)
	}
	// Every winner must have got its own correct discount — a race that produced
	// the right number of orders with the wrong amounts would still be a failure.
	orders := e.client.Order.Query().AllX(context.Background())
	if len(orders) != stock {
		t.Fatalf("want exactly %d orders in total, got %d", stock, len(orders))
	}
	for _, o := range orders {
		if o.TotalAmount != o.SubtotalAmount-o.DiscountAmount+o.ShippingFee {
			t.Fatalf("order %d does not compose: %d - %d + %d != %d",
				o.ID, o.SubtotalAmount, o.DiscountAmount, o.ShippingFee, o.TotalAmount)
		}
		if o.DiscountAmount != 340 || o.TotalAmount != 3060 {
			t.Fatalf("order %d lost its discount under contention: -%d, total %d",
				o.ID, o.DiscountAmount, o.TotalAmount)
		}
	}
}
