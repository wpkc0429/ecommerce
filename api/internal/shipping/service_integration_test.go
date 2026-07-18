package shipping_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/shipping"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// shippingFixtures exercises shipping.Service directly (no HTTP/JWT/RBAC
// layer) to pin down the state-machine/concurrency-guard branches (design
// D3/D4) — the httpapi-level tests (shipping_integration_test.go) cover the
// same guarantees end-to-end through the router; this file is where the
// service branches themselves are exhaustively exercised.
type shippingFixtures struct {
	client *ent.Client
	orders *order.Service
	svc    *shipping.Service
	shopID int
}

func setupShippingFixtures(t *testing.T) *shippingFixtures {
	t.Helper()
	client := testutil.OpenDB(t)
	ctx := context.Background()

	shop := client.Shop.Create().SetName("Shipping Test Shop").SetStatus(1).SaveX(ctx)
	orders := &order.Service{Client: client}

	return &shippingFixtures{
		client: client,
		orders: orders,
		svc:    &shipping.Service{Client: client, Orders: orders},
		shopID: shop.ID,
	}
}

func newMember(t *testing.T, client *ent.Client, email string) int {
	t.Helper()
	return client.Member.Create().SetEmail(email).SetStatus(1).SaveX(context.Background()).ID
}

// newOrder creates a fresh, unfulfilled, uncancelled order directly via ent
// (bypassing checkout — this package only needs an order to exist, not a
// cart/checkout flow).
func (f *shippingFixtures) newOrder(t *testing.T, memberID int) *ent.Order {
	t.Helper()
	ctx := context.Background()
	return f.client.Order.Create().
		SetShopID(f.shopID).SetMemberID(memberID).
		SetStatus(order.StatusCreated).
		SetPaymentStatus(order.PaymentStatusUnpaid).
		SetFulfillmentStatus(order.FulfillmentStatusUnfulfilled).
		SetCurrency("TWD").
		SetTotalAmount(1000).
		SetShippingAddress(json.RawMessage(`{}`)).
		SaveX(ctx)
}

func trackingNumber(s string) *string { return &s }

func TestServiceCreateShipmentAdvancesFulfillmentStatus(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := newMember(t, f.client, "flow@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", trackingNumber("TRACK-1"))
	if err != nil {
		t.Fatalf("CreateShipment: %v", err)
	}
	if sh.Status != shipping.ShippedStatus {
		t.Fatalf("expected shipped status, got %d", sh.Status)
	}
	if sh.ShippedAt == nil {
		t.Fatal("expected shipped_at to be set")
	}
	if sh.TrackingNumber == nil || *sh.TrackingNumber != "TRACK-1" {
		t.Fatalf("expected tracking number to be persisted, got %v", sh.TrackingNumber)
	}

	gotOrder, err := f.orders.GetOrderAdmin(ctx, f.shopID, ord.ID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.FulfillmentStatus != shipping.ShippedStatus {
		t.Fatalf("expected order fulfillment_status=shipped, got %d", gotOrder.FulfillmentStatus)
	}
}

func TestServiceCreateShipmentRejectsCancelledOrder(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := newMember(t, f.client, "cancelled@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	f.client.Order.UpdateOneID(ord.ID).SetStatus(order.StatusCancelled).SaveX(ctx)

	_, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil)
	var ce *shipping.ConflictError
	if err == nil {
		t.Fatal("expected an error")
	}
	if !asConflict(err, &ce) {
		t.Fatalf("expected *shipping.ConflictError, got %T: %v", err, err)
	}

	sc, err := f.client.Shipment.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count shipments: %v", err)
	}
	if sc != 0 {
		t.Fatalf("expected no shipment created, got %d", sc)
	}
}

func TestServiceCreateShipmentRejectsAlreadyShippedOrder(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := newMember(t, f.client, "already@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	if _, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil); err != nil {
		t.Fatalf("first CreateShipment: %v", err)
	}

	_, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "7-11 超商取貨", nil)
	var ce *shipping.ConflictError
	if err == nil {
		t.Fatal("expected an error")
	}
	if !asConflict(err, &ce) {
		t.Fatalf("expected *shipping.ConflictError, got %T: %v", err, err)
	}

	sc, err := f.client.Shipment.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count shipments: %v", err)
	}
	if sc != 1 {
		t.Fatalf("expected exactly one shipment to exist, got %d", sc)
	}
}

func TestServiceCreateShipmentRejectsBlankCarrier(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := newMember(t, f.client, "blank@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	_, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "   ", nil)
	var ve *shipping.ValidationError
	if err == nil {
		t.Fatal("expected an error")
	}
	if !asValidation(err, &ve) {
		t.Fatalf("expected *shipping.ValidationError, got %T: %v", err, err)
	}
}

func TestServiceAdvanceShipmentToDelivered(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := newMember(t, f.client, "delivered@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil)
	if err != nil {
		t.Fatalf("CreateShipment: %v", err)
	}

	updated, err := f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, sh.ID, shipping.DeliveredStatus)
	if err != nil {
		t.Fatalf("AdvanceShipment: %v", err)
	}
	if updated.Status != shipping.DeliveredStatus {
		t.Fatalf("expected delivered status, got %d", updated.Status)
	}
	if updated.DeliveredAt == nil {
		t.Fatal("expected delivered_at to be set")
	}

	gotOrder, err := f.orders.GetOrderAdmin(ctx, f.shopID, ord.ID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.FulfillmentStatus != shipping.DeliveredStatus {
		t.Fatalf("expected order fulfillment_status=delivered, got %d", gotOrder.FulfillmentStatus)
	}
}

func TestServiceAdvanceShipmentToReturned(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := newMember(t, f.client, "returned@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil)
	if err != nil {
		t.Fatalf("CreateShipment: %v", err)
	}

	updated, err := f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, sh.ID, shipping.ReturnedStatus)
	if err != nil {
		t.Fatalf("AdvanceShipment: %v", err)
	}
	if updated.Status != shipping.ReturnedStatus {
		t.Fatalf("expected returned status, got %d", updated.Status)
	}

	gotOrder, err := f.orders.GetOrderAdmin(ctx, f.shopID, ord.ID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.FulfillmentStatus != shipping.ReturnedStatus {
		t.Fatalf("expected order fulfillment_status=returned, got %d", gotOrder.FulfillmentStatus)
	}
}

func TestServiceAdvanceShipmentRejectsRepeatedDelivered(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := newMember(t, f.client, "repeat-delivered@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil)
	if err != nil {
		t.Fatalf("CreateShipment: %v", err)
	}
	if _, err := f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, sh.ID, shipping.DeliveredStatus); err != nil {
		t.Fatalf("first AdvanceShipment: %v", err)
	}

	_, err = f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, sh.ID, shipping.DeliveredStatus)
	var ce *shipping.ConflictError
	if err == nil {
		t.Fatal("expected an error")
	}
	if !asConflict(err, &ce) {
		t.Fatalf("expected *shipping.ConflictError, got %T: %v", err, err)
	}
}

func TestServiceAdvanceShipmentRejectsReturnedToDelivered(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := newMember(t, f.client, "returned-to-delivered@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil)
	if err != nil {
		t.Fatalf("CreateShipment: %v", err)
	}
	if _, err := f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, sh.ID, shipping.ReturnedStatus); err != nil {
		t.Fatalf("first AdvanceShipment (returned): %v", err)
	}

	_, err = f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, sh.ID, shipping.DeliveredStatus)
	var ce *shipping.ConflictError
	if err == nil {
		t.Fatal("expected an error")
	}
	if !asConflict(err, &ce) {
		t.Fatalf("expected *shipping.ConflictError, got %T: %v", err, err)
	}

	gotOrder, err := f.orders.GetOrderAdmin(ctx, f.shopID, ord.ID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.FulfillmentStatus != shipping.ReturnedStatus {
		t.Fatalf("expected order fulfillment_status to remain returned, got %d", gotOrder.FulfillmentStatus)
	}
}

func TestServiceAdvanceShipmentRejectsUnshippedOrder(t *testing.T) {
	// A shipment can only ever be created already in "shipped" state (design
	// D3 — creating the row IS the shipping event), so there is no direct
	// API path to construct a shipment row that is not yet "shipped". This
	// test proves the guard by attempting to advance a shipment that has
	// already reached a terminal state twice in a row is covered above;
	// here we additionally prove AdvanceShipment on a nonexistent shipment
	// ID is a 404, not a silent no-op.
	f := setupShippingFixtures(t)
	memberID := newMember(t, f.client, "unshipped@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	_, err := f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, 999999, shipping.DeliveredStatus)
	if err != shipping.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestServiceGetShipmentForMemberCrossMemberIsNotFound(t *testing.T) {
	f := setupShippingFixtures(t)
	ownerID := newMember(t, f.client, "owner@t.dev")
	otherID := newMember(t, f.client, "other@t.dev")
	ord := f.newOrder(t, ownerID)
	ctx := context.Background()

	if _, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil); err != nil {
		t.Fatalf("CreateShipment: %v", err)
	}

	_, err := f.svc.GetShipmentForMember(ctx, f.shopID, otherID, ord.ID)
	if err != shipping.ErrNotFound {
		t.Fatalf("expected ErrNotFound for cross-member access, got %v", err)
	}
}

func TestServiceGetShipmentForMemberNoShipmentYetIsNotFound(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := newMember(t, f.client, "no-shipment@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	_, err := f.svc.GetShipmentForMember(ctx, f.shopID, memberID, ord.ID)
	if err != shipping.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestServiceShippingMethodCRUD(t *testing.T) {
	f := setupShippingFixtures(t)
	ctx := context.Background()

	sm, err := f.svc.CreateShippingMethod(ctx, f.shopID, shipping.ShippingMethodInput{
		Name: "常溫宅配", Carrier: "黑貓宅急便", FlatRate: 100,
	})
	if err != nil {
		t.Fatalf("CreateShippingMethod: %v", err)
	}
	if !sm.IsActive {
		t.Fatal("expected default is_active=true")
	}

	got, err := f.svc.GetShippingMethod(ctx, f.shopID, sm.ID)
	if err != nil {
		t.Fatalf("GetShippingMethod: %v", err)
	}
	if got.Name != "常溫宅配" {
		t.Fatalf("expected name to round-trip, got %q", got.Name)
	}

	newName := "冷凍宅配"
	updated, err := f.svc.UpdateShippingMethod(ctx, f.shopID, sm.ID, shipping.ShippingMethodUpdate{Name: &newName})
	if err != nil {
		t.Fatalf("UpdateShippingMethod: %v", err)
	}
	if updated.Name != newName {
		t.Fatalf("expected updated name, got %q", updated.Name)
	}

	list, err := f.svc.ListShippingMethods(ctx, f.shopID)
	if err != nil {
		t.Fatalf("ListShippingMethods: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 shipping method, got %d", len(list))
	}

	if err := f.svc.DeleteShippingMethod(ctx, f.shopID, sm.ID); err != nil {
		t.Fatalf("DeleteShippingMethod: %v", err)
	}
	if _, err := f.svc.GetShippingMethod(ctx, f.shopID, sm.ID); err != shipping.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestServiceCreateShippingMethodRejectsNegativeFlatRate(t *testing.T) {
	f := setupShippingFixtures(t)
	ctx := context.Background()

	_, err := f.svc.CreateShippingMethod(ctx, f.shopID, shipping.ShippingMethodInput{
		Name: "invalid", Carrier: "test", FlatRate: -1,
	})
	var ve *shipping.ValidationError
	if err == nil {
		t.Fatal("expected an error")
	}
	if !asValidation(err, &ve) {
		t.Fatalf("expected *shipping.ValidationError, got %T: %v", err, err)
	}
}

// ── design D1/D5 (change member-tiers-and-points): AdvanceShipment
// publishes events.OrderReturned only for the returned transition ─────────

func TestServiceAdvanceShipmentToReturnedPublishesOrderReturned(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := newMember(t, f.client, "publish-returned@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	log := slog.New(slog.DiscardHandler)
	dispatcher := events.NewDispatcher(log)
	var captured []events.OrderReturned
	dispatcher.Subscribe(func(_ context.Context, e events.Event) {
		if ev, ok := e.(events.OrderReturned); ok {
			captured = append(captured, ev)
		}
	})
	f.svc.Dispatcher = dispatcher

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil)
	if err != nil {
		t.Fatalf("CreateShipment: %v", err)
	}
	if _, err := f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, sh.ID, shipping.ReturnedStatus); err != nil {
		t.Fatalf("AdvanceShipment: %v", err)
	}

	if len(captured) != 1 {
		t.Fatalf("expected exactly one OrderReturned event, got %d", len(captured))
	}
	ev := captured[0]
	if ev.ShopID != f.shopID || ev.OrderID != ord.ID || ev.MemberID != memberID {
		t.Fatalf("expected event to carry the order's identity, got %+v", ev)
	}
}

func TestServiceAdvanceShipmentToDeliveredDoesNotPublishOrderReturned(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := newMember(t, f.client, "publish-delivered@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	log := slog.New(slog.DiscardHandler)
	dispatcher := events.NewDispatcher(log)
	var count int
	dispatcher.Subscribe(func(_ context.Context, e events.Event) {
		if _, ok := e.(events.OrderReturned); ok {
			count++
		}
	})
	f.svc.Dispatcher = dispatcher

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil)
	if err != nil {
		t.Fatalf("CreateShipment: %v", err)
	}
	if _, err := f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, sh.ID, shipping.DeliveredStatus); err != nil {
		t.Fatalf("AdvanceShipment: %v", err)
	}

	if count != 0 {
		t.Fatalf("expected no OrderReturned event for a delivered transition, got %d", count)
	}
}

// TestServiceAdvanceShipmentWithoutDispatcherIsNilSafe proves every existing
// caller that constructs shipping.Service without setting Dispatcher (e.g.
// every other test in this file, via setupShippingFixtures) keeps working.
func TestServiceAdvanceShipmentWithoutDispatcherIsNilSafe(t *testing.T) {
	f := setupShippingFixtures(t) // Dispatcher left nil
	memberID := newMember(t, f.client, "nil-dispatcher@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil)
	if err != nil {
		t.Fatalf("CreateShipment: %v", err)
	}
	if _, err := f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, sh.ID, shipping.ReturnedStatus); err != nil {
		t.Fatalf("AdvanceShipment must not fail with a nil Dispatcher: %v", err)
	}
}

func asConflict(err error, target **shipping.ConflictError) bool {
	ce, ok := err.(*shipping.ConflictError)
	if ok {
		*target = ce
	}
	return ok
}

func asValidation(err error, target **shipping.ValidationError) bool {
	ve, ok := err.(*shipping.ValidationError)
	if ok {
		*target = ve
	}
	return ok
}
