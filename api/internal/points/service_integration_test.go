package points_test

import (
	"context"
	"encoding/json"
	"testing"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/points"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// pointsFixtures exercises points.Service directly (no HTTP/JWT/RBAC layer)
// to pin down the idempotency/level-recompute/clawback-capping branches
// (design D4/D5/D7) — the httpapi-level tests (points_integration_test.go)
// cover the same guarantees end-to-end through the router; this file is
// where the service branches themselves are exhaustively exercised.
type pointsFixtures struct {
	client *ent.Client
	svc    *points.Service
	shopID int
}

func setupPointsFixtures(t *testing.T) *pointsFixtures {
	t.Helper()
	client := testutil.OpenDB(t)
	ctx := context.Background()

	shop := client.Shop.Create().SetName("Points Test Shop").SetStatus(1).SaveX(ctx)

	return &pointsFixtures{
		client: client,
		svc:    &points.Service{Client: client},
		shopID: shop.ID,
	}
}

func newMember(t *testing.T, client *ent.Client, email string) int {
	t.Helper()
	return client.Member.Create().SetEmail(email).SetStatus(1).SaveX(context.Background()).ID
}

// newShopMember mirrors httpapi.MemberAuthHandler.ensureMembership's effect
// (lazily created at login/register — design Context) so service-level
// tests don't need to drive the full auth flow.
func (f *pointsFixtures) newShopMember(t *testing.T, memberID int) *ent.ShopMember {
	t.Helper()
	return f.client.ShopMember.Create().SetShopID(f.shopID).SetMemberID(memberID).SaveX(context.Background())
}

// newOrder creates a real, paid-and-shippable-looking order directly via ent
// (bypassing checkout — this package only needs a real order row for
// point_transactions.order_id's FK to resolve, mirroring payment/shipping
// fixtures' own newOrder helper). totalAmount is set on the row to match
// what tests also pass into the event payload, for realism.
func (f *pointsFixtures) newOrder(t *testing.T, memberID int, totalAmount int64) *ent.Order {
	t.Helper()
	ctx := context.Background()
	return f.client.Order.Create().
		SetShopID(f.shopID).SetMemberID(memberID).
		SetStatus(order.StatusCreated).
		SetPaymentStatus(order.PaymentStatusUnpaid).
		SetFulfillmentStatus(order.FulfillmentStatusUnfulfilled).
		SetCurrency("TWD").
		SetTotalAmount(totalAmount).
		SetShippingAddress(json.RawMessage(`{}`)).
		SaveX(ctx)
}

func paidEvent(shopID, orderID, memberID int, totalAmount int64) events.OrderPaymentSucceeded {
	return events.OrderPaymentSucceeded{ShopID: shopID, OrderID: orderID, MemberID: memberID, TotalAmount: totalAmount, Currency: "TWD"}
}

func returnedEvent(shopID, orderID, memberID int) events.OrderReturned {
	return events.OrderReturned{ShopID: shopID, OrderID: orderID, MemberID: memberID}
}

// ── award (design D3/D4) ────────────────────────────────────────────────

func TestHandleOrderPaidAwardsPointsAndUpdatesBalance(t *testing.T) {
	f := setupPointsFixtures(t)
	memberID := newMember(t, f.client, "award@t.dev")
	sm := f.newShopMember(t, memberID)
	ord := f.newOrder(t, memberID, 1290)
	ctx := context.Background()

	if err := f.svc.HandleOrderPaid(ctx, paidEvent(f.shopID, ord.ID, memberID, 1290)); err != nil {
		t.Fatalf("HandleOrderPaid: %v", err)
	}

	got := f.client.ShopMember.GetX(ctx, sm.ID)
	if got.Points != 12 { // 1290 / 100 = 12 (design D3)
		t.Fatalf("expected 12 points awarded, got %d", got.Points)
	}

	txs := f.client.PointTransaction.Query().AllX(ctx)
	if len(txs) != 1 {
		t.Fatalf("expected exactly one ledger row, got %d", len(txs))
	}
	if txs[0].Kind != points.KindOrderAward || txs[0].PointsDelta != 12 {
		t.Fatalf("expected an award row with delta 12, got %+v", txs[0])
	}
	if txs[0].OrderID == nil || *txs[0].OrderID != ord.ID {
		t.Fatalf("expected the ledger row to reference the order, got %+v", txs[0].OrderID)
	}
}

func TestHandleOrderPaidIsIdempotentOnDuplicateEvent(t *testing.T) {
	f := setupPointsFixtures(t)
	memberID := newMember(t, f.client, "award-dup@t.dev")
	sm := f.newShopMember(t, memberID)
	ord := f.newOrder(t, memberID, 500)
	ctx := context.Background()

	ev := paidEvent(f.shopID, ord.ID, memberID, 500)
	if err := f.svc.HandleOrderPaid(ctx, ev); err != nil {
		t.Fatalf("first HandleOrderPaid: %v", err)
	}
	// Same event delivered again (mirrors payment.Service.HandleWebhook's
	// re-assertion branch republishing on every duplicate delivery — design
	// D4).
	if err := f.svc.HandleOrderPaid(ctx, ev); err != nil {
		t.Fatalf("second HandleOrderPaid (must be a safe no-op, not an error): %v", err)
	}

	got := f.client.ShopMember.GetX(ctx, sm.ID)
	if got.Points != 5 { // 500/100 = 5, awarded exactly once
		t.Fatalf("expected points awarded exactly once (5), got %d", got.Points)
	}
	count := f.client.PointTransaction.Query().CountX(ctx)
	if count != 1 {
		t.Fatalf("expected exactly one ledger row after duplicate delivery, got %d", count)
	}
}

func TestHandleOrderPaidZeroAmountRecordsZeroDeltaAward(t *testing.T) {
	f := setupPointsFixtures(t)
	memberID := newMember(t, f.client, "zero@t.dev")
	sm := f.newShopMember(t, memberID)
	ord := f.newOrder(t, memberID, 50)
	ctx := context.Background()

	if err := f.svc.HandleOrderPaid(ctx, paidEvent(f.shopID, ord.ID, memberID, 50)); err != nil {
		t.Fatalf("HandleOrderPaid: %v", err)
	}
	got := f.client.ShopMember.GetX(ctx, sm.ID)
	if got.Points != 0 {
		t.Fatalf("expected 0 points for a sub-100-minor-unit order, got %d", got.Points)
	}
	count := f.client.PointTransaction.Query().CountX(ctx)
	if count != 1 {
		t.Fatalf("expected a zero-delta award row to still be recorded (idempotency guard), got %d rows", count)
	}
}

// ── level recompute (design D7) ─────────────────────────────────────────

func TestLevelUpgradesWhenBalanceCrossesTierThreshold(t *testing.T) {
	f := setupPointsFixtures(t)
	memberID := newMember(t, f.client, "level-up@t.dev")
	sm := f.newShopMember(t, memberID)
	ctx := context.Background()

	silver, err := f.svc.CreateMemberTier(ctx, f.shopID, points.MemberTierInput{Name: "銀卡", MinPoints: 10})
	if err != nil {
		t.Fatalf("CreateMemberTier(silver): %v", err)
	}
	gold, err := f.svc.CreateMemberTier(ctx, f.shopID, points.MemberTierInput{Name: "金卡", MinPoints: 50})
	if err != nil {
		t.Fatalf("CreateMemberTier(gold): %v", err)
	}

	// 1290 minor units -> 12 points: crosses silver (10) but not gold (50).
	ord1 := f.newOrder(t, memberID, 1290)
	if err := f.svc.HandleOrderPaid(ctx, paidEvent(f.shopID, ord1.ID, memberID, 1290)); err != nil {
		t.Fatalf("HandleOrderPaid: %v", err)
	}
	got := f.client.ShopMember.GetX(ctx, sm.ID)
	if got.LevelID == nil || *got.LevelID != silver.ID {
		t.Fatalf("expected level to be silver (%d) after 12 points, got %v", silver.ID, got.LevelID)
	}

	// Another 4000 minor units -> +40 points = 52 total: crosses gold (50).
	ord2 := f.newOrder(t, memberID, 4000)
	if err := f.svc.HandleOrderPaid(ctx, paidEvent(f.shopID, ord2.ID, memberID, 4000)); err != nil {
		t.Fatalf("second HandleOrderPaid: %v", err)
	}
	got = f.client.ShopMember.GetX(ctx, sm.ID)
	if got.LevelID == nil || *got.LevelID != gold.ID {
		t.Fatalf("expected level to be gold (%d) after 52 points, got %v", gold.ID, got.LevelID)
	}
}

func TestLevelClearsWhenNoTierQualifies(t *testing.T) {
	f := setupPointsFixtures(t)
	memberID := newMember(t, f.client, "level-none@t.dev")
	sm := f.newShopMember(t, memberID)
	ord := f.newOrder(t, memberID, 100)
	ctx := context.Background()

	if _, err := f.svc.CreateMemberTier(ctx, f.shopID, points.MemberTierInput{Name: "銀卡", MinPoints: 1000}); err != nil {
		t.Fatalf("CreateMemberTier: %v", err)
	}
	if err := f.svc.HandleOrderPaid(ctx, paidEvent(f.shopID, ord.ID, memberID, 100)); err != nil {
		t.Fatalf("HandleOrderPaid: %v", err)
	}
	got := f.client.ShopMember.GetX(ctx, sm.ID)
	if got.LevelID != nil {
		t.Fatalf("expected level_id to remain nil (1 point does not qualify for any tier), got %v", got.LevelID)
	}
}

// ── clawback (design D5) ────────────────────────────────────────────────

func TestHandleOrderReturnedClawsBackAwardedPoints(t *testing.T) {
	f := setupPointsFixtures(t)
	memberID := newMember(t, f.client, "clawback@t.dev")
	sm := f.newShopMember(t, memberID)
	ord := f.newOrder(t, memberID, 1000)
	ctx := context.Background()

	if err := f.svc.HandleOrderPaid(ctx, paidEvent(f.shopID, ord.ID, memberID, 1000)); err != nil {
		t.Fatalf("HandleOrderPaid: %v", err)
	}
	if got := f.client.ShopMember.GetX(ctx, sm.ID); got.Points != 10 {
		t.Fatalf("expected 10 points awarded, got %d", got.Points)
	}

	if err := f.svc.HandleOrderReturned(ctx, returnedEvent(f.shopID, ord.ID, memberID)); err != nil {
		t.Fatalf("HandleOrderReturned: %v", err)
	}
	got := f.client.ShopMember.GetX(ctx, sm.ID)
	if got.Points != 0 {
		t.Fatalf("expected all 10 points clawed back, got %d", got.Points)
	}

	clawbackRows := f.client.PointTransaction.Query().AllX(ctx)
	found := false
	for _, tx := range clawbackRows {
		if tx.Kind == points.KindOrderClawback {
			found = true
			if tx.PointsDelta != -10 {
				t.Fatalf("expected clawback delta -10, got %d", tx.PointsDelta)
			}
		}
	}
	if !found {
		t.Fatal("expected a clawback ledger row")
	}
}

func TestHandleOrderReturnedIsIdempotent(t *testing.T) {
	f := setupPointsFixtures(t)
	memberID := newMember(t, f.client, "clawback-dup@t.dev")
	sm := f.newShopMember(t, memberID)
	ord := f.newOrder(t, memberID, 1000)
	ctx := context.Background()

	if err := f.svc.HandleOrderPaid(ctx, paidEvent(f.shopID, ord.ID, memberID, 1000)); err != nil {
		t.Fatalf("HandleOrderPaid: %v", err)
	}
	ev := returnedEvent(f.shopID, ord.ID, memberID)
	if err := f.svc.HandleOrderReturned(ctx, ev); err != nil {
		t.Fatalf("first HandleOrderReturned: %v", err)
	}
	if err := f.svc.HandleOrderReturned(ctx, ev); err != nil {
		t.Fatalf("second HandleOrderReturned (must be a safe no-op): %v", err)
	}

	got := f.client.ShopMember.GetX(ctx, sm.ID)
	if got.Points != 0 {
		t.Fatalf("expected points to remain at 0 (not double-clawed-back into negative), got %d", got.Points)
	}
	// Exactly 2 ledger rows total (1 award + 1 clawback) — the duplicate
	// HandleOrderReturned call must not have inserted a second clawback row.
	if count := f.client.PointTransaction.Query().CountX(ctx); count != 2 {
		t.Fatalf("expected exactly 2 ledger rows (award + clawback), got %d", count)
	}
}

// TestHandleOrderReturnedClawbackCappedAtCurrentBalance proves design D5's
// core safety guarantee: a manual deduction (or any other point-decreasing
// event) that happens between award and return must never let the clawback
// drive the balance negative — the clawback is capped at whatever the
// member currently holds, not at what was originally awarded.
func TestHandleOrderReturnedClawbackCappedAtCurrentBalance(t *testing.T) {
	f := setupPointsFixtures(t)
	memberID := newMember(t, f.client, "capped@t.dev")
	sm := f.newShopMember(t, memberID)
	ord := f.newOrder(t, memberID, 1000)
	ctx := context.Background()

	if err := f.svc.HandleOrderPaid(ctx, paidEvent(f.shopID, ord.ID, memberID, 1000)); err != nil {
		t.Fatalf("HandleOrderPaid: %v", err)
	} // awards 10 points

	// Merchant manually deducts 7, leaving only 3.
	if _, err := f.svc.AdjustPoints(ctx, f.shopID, sm.ID, -7, "客服調整"); err != nil {
		t.Fatalf("AdjustPoints: %v", err)
	}
	if got := f.client.ShopMember.GetX(ctx, sm.ID); got.Points != 3 {
		t.Fatalf("expected 3 points after manual deduction, got %d", got.Points)
	}

	if err := f.svc.HandleOrderReturned(ctx, returnedEvent(f.shopID, ord.ID, memberID)); err != nil {
		t.Fatalf("HandleOrderReturned: %v", err)
	}
	got := f.client.ShopMember.GetX(ctx, sm.ID)
	if got.Points != 0 {
		t.Fatalf("expected clawback capped at the remaining 3 points (balance floors at 0), got %d", got.Points)
	}
}

func TestHandleOrderReturnedWithNoPriorAwardIsHarmlessNoOp(t *testing.T) {
	f := setupPointsFixtures(t)
	memberID := newMember(t, f.client, "never-paid@t.dev")
	sm := f.newShopMember(t, memberID)
	ord := f.newOrder(t, memberID, 1000)
	ctx := context.Background()

	// shipping-logistics allows shipping an unpaid order (COD) — a return of
	// such an order has no award row to claw back from.
	if err := f.svc.HandleOrderReturned(ctx, returnedEvent(f.shopID, ord.ID, memberID)); err != nil {
		t.Fatalf("HandleOrderReturned: %v", err)
	}
	got := f.client.ShopMember.GetX(ctx, sm.ID)
	if got.Points != 0 {
		t.Fatalf("expected points to remain 0, got %d", got.Points)
	}
}

// ── manual adjustment (design D10) ─────────────────────────────────────

func TestAdjustPointsRejectsGoingNegative(t *testing.T) {
	f := setupPointsFixtures(t)
	memberID := newMember(t, f.client, "adjust-neg@t.dev")
	sm := f.newShopMember(t, memberID)
	ctx := context.Background()

	_, err := f.svc.AdjustPoints(ctx, f.shopID, sm.ID, -1, "太多了")
	if _, ok := err.(*points.ValidationError); !ok {
		t.Fatalf("expected *points.ValidationError, got %T: %v", err, err)
	}
	got := f.client.ShopMember.GetX(ctx, sm.ID)
	if got.Points != 0 {
		t.Fatalf("expected balance unchanged, got %d", got.Points)
	}
}

func TestAdjustPointsSucceedsAndRecomputesLevel(t *testing.T) {
	f := setupPointsFixtures(t)
	memberID := newMember(t, f.client, "adjust-ok@t.dev")
	sm := f.newShopMember(t, memberID)
	ctx := context.Background()

	tier, err := f.svc.CreateMemberTier(ctx, f.shopID, points.MemberTierInput{Name: "銀卡", MinPoints: 20})
	if err != nil {
		t.Fatalf("CreateMemberTier: %v", err)
	}

	updated, err := f.svc.AdjustPoints(ctx, f.shopID, sm.ID, 25, "客服補償")
	if err != nil {
		t.Fatalf("AdjustPoints: %v", err)
	}
	if updated.Points != 25 {
		t.Fatalf("expected 25 points, got %d", updated.Points)
	}
	if updated.LevelID == nil || *updated.LevelID != tier.ID {
		t.Fatalf("expected level to be silver (%d), got %v", tier.ID, updated.LevelID)
	}
}

// ── cross-shop / cross-member isolation (spec Tenant data isolation) ───

func TestListTransactionsAdminCrossShopIsNotFound(t *testing.T) {
	f := setupPointsFixtures(t)
	otherShop := f.client.Shop.Create().SetName("Other Shop").SetStatus(1).SaveX(context.Background())
	memberID := newMember(t, f.client, "cross-shop@t.dev")
	sm := f.newShopMember(t, memberID)
	ctx := context.Background()

	_, err := f.svc.ListTransactionsAdmin(ctx, otherShop.ID, sm.ID, points.ListParams{})
	if err != points.ErrNotFound {
		t.Fatalf("expected ErrNotFound for cross-shop access, got %v", err)
	}
}

func TestGetMemberPointsSelfCrossMemberIsolation(t *testing.T) {
	f := setupPointsFixtures(t)
	memberA := newMember(t, f.client, "self-a@t.dev")
	memberB := newMember(t, f.client, "self-b@t.dev")
	f.newShopMember(t, memberA)
	f.newShopMember(t, memberB)
	ord := f.newOrder(t, memberA, 1000)
	ctx := context.Background()

	if err := f.svc.HandleOrderPaid(ctx, paidEvent(f.shopID, ord.ID, memberA, 1000)); err != nil {
		t.Fatalf("HandleOrderPaid: %v", err)
	}

	gotA, err := f.svc.GetMemberPointsSelf(ctx, f.shopID, memberA)
	if err != nil {
		t.Fatalf("GetMemberPointsSelf(A): %v", err)
	}
	if gotA.Points != 10 {
		t.Fatalf("expected member A to have 10 points, got %d", gotA.Points)
	}
	gotB, err := f.svc.GetMemberPointsSelf(ctx, f.shopID, memberB)
	if err != nil {
		t.Fatalf("GetMemberPointsSelf(B): %v", err)
	}
	if gotB.Points != 0 {
		t.Fatalf("expected member B's own balance to be unaffected by member A's award, got %d", gotB.Points)
	}
}

// ── member tier CRUD ─────────────────────────────────────────────────────

func TestMemberTierCRUD(t *testing.T) {
	f := setupPointsFixtures(t)
	ctx := context.Background()

	mt, err := f.svc.CreateMemberTier(ctx, f.shopID, points.MemberTierInput{Name: "一般會員", MinPoints: 0})
	if err != nil {
		t.Fatalf("CreateMemberTier: %v", err)
	}
	got, err := f.svc.GetMemberTier(ctx, f.shopID, mt.ID)
	if err != nil || got.Name != "一般會員" {
		t.Fatalf("GetMemberTier: %v %+v", err, got)
	}

	newName := "銀卡會員"
	updated, err := f.svc.UpdateMemberTier(ctx, f.shopID, mt.ID, points.MemberTierUpdate{Name: &newName})
	if err != nil || updated.Name != newName {
		t.Fatalf("UpdateMemberTier: %v %+v", err, updated)
	}

	list, err := f.svc.ListMemberTiers(ctx, f.shopID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListMemberTiers: %v (len=%d)", err, len(list))
	}

	if err := f.svc.DeleteMemberTier(ctx, f.shopID, mt.ID); err != nil {
		t.Fatalf("DeleteMemberTier: %v", err)
	}
	if _, err := f.svc.GetMemberTier(ctx, f.shopID, mt.ID); err != points.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestMemberTierDeleteClearsLevelIDButKeepsPoints(t *testing.T) {
	f := setupPointsFixtures(t)
	memberID := newMember(t, f.client, "tier-delete@t.dev")
	sm := f.newShopMember(t, memberID)
	ord := f.newOrder(t, memberID, 1000)
	ctx := context.Background()

	tier, err := f.svc.CreateMemberTier(ctx, f.shopID, points.MemberTierInput{Name: "銀卡", MinPoints: 5})
	if err != nil {
		t.Fatalf("CreateMemberTier: %v", err)
	}
	if err := f.svc.HandleOrderPaid(ctx, paidEvent(f.shopID, ord.ID, memberID, 1000)); err != nil {
		t.Fatalf("HandleOrderPaid: %v", err)
	}
	got := f.client.ShopMember.GetX(ctx, sm.ID)
	if got.LevelID == nil || *got.LevelID != tier.ID {
		t.Fatalf("expected member to hold the tier before deletion, got %v", got.LevelID)
	}

	if err := f.svc.DeleteMemberTier(ctx, f.shopID, tier.ID); err != nil {
		t.Fatalf("DeleteMemberTier: %v", err)
	}
	got = f.client.ShopMember.GetX(ctx, sm.ID)
	if got.LevelID != nil {
		t.Fatalf("expected level_id cleared to nil after tier deletion (ON DELETE SET NULL, design D7), got %v", got.LevelID)
	}
	if got.Points != 10 {
		t.Fatalf("expected points balance to be unaffected by tier deletion, got %d", got.Points)
	}
}
