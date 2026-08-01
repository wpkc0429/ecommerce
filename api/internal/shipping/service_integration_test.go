package shipping_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ksdevworks/ecommerce/api/internal/ent"
	entorderitem "ksdevworks/ecommerce/api/internal/ent/orderitem"
	entpointtransaction "ksdevworks/ecommerce/api/internal/ent/pointtransaction"
	entshipment "ksdevworks/ecommerce/api/internal/ent/shipment"
	entshipmentitem "ksdevworks/ecommerce/api/internal/ent/shipmentitem"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/points"
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

// newMember creates a customer of this fixture's shop. Since change
// member-identity-per-shop, members carries shop_id and is tenant-owned, so
// the shop must be set explicitly here (these fixtures run outside tenant
// context, so nothing stamps it for them).
func (f *shippingFixtures) newMember(t *testing.T, email string) int {
	t.Helper()
	return f.client.Member.Create().SetShopID(f.shopID).SetEmail(email).SetStatus(1).SaveX(context.Background()).ID
}

// newOrder creates a fresh, unfulfilled, uncancelled order with one line of
// quantity 1, directly via ent (bypassing checkout — this package only needs an
// order to exist, not a cart/checkout flow).
//
// The line item is not decoration: since change partial-shipment "ship
// everything outstanding" is defined in terms of order items, so an order with
// none of them has nothing to ship and is correctly refused. Real orders always
// have items (checkout rejects an empty cart), so a fixture without them was
// only ever modelling a state the system cannot produce.
func (f *shippingFixtures) newOrder(t *testing.T, memberID int) *ent.Order {
	t.Helper()
	return f.newOrderWithLines(t, memberID, 1)
}

// newOrderWithLines creates an order with one order item per given quantity.
func (f *shippingFixtures) newOrderWithLines(t *testing.T, memberID int, quantities ...int32) *ent.Order {
	t.Helper()
	ctx := context.Background()
	var subtotal int64
	for _, q := range quantities {
		subtotal += 1000 * int64(q)
	}
	ord := f.client.Order.Create().
		SetShopID(f.shopID).SetMemberID(memberID).
		SetStatus(order.StatusCreated).
		SetPaymentStatus(order.PaymentStatusUnpaid).
		SetFulfillmentStatus(order.FulfillmentStatusUnfulfilled).
		SetCurrency("TWD").
		SetSubtotalAmount(subtotal).
		SetTotalAmount(subtotal).
		SetShippingAddress(json.RawMessage(`{}`)).
		SaveX(ctx)
	for i, q := range quantities {
		f.client.OrderItem.Create().
			SetShopID(f.shopID).SetOrderID(ord.ID).
			SetProductTitle(fmt.Sprintf("品項 %d", i+1)).
			SetSkuCode(fmt.Sprintf("SKU-%d", i+1)).
			SetQuantity(q).SetPriceAmount(1000).SetCurrency("TWD").
			SaveX(ctx)
	}
	return ord
}

// orderItems returns an order's lines, ordered by id (the same order every
// production path uses).
func (f *shippingFixtures) orderItems(t *testing.T, orderID int) []*ent.OrderItem {
	t.Helper()
	return f.client.OrderItem.Query().
		Where(entorderitem.OrderIDEQ(orderID)).
		Order(entorderitem.ByID()).
		AllX(context.Background())
}

// fulfillment reads the order's current fulfillment_status.
func (f *shippingFixtures) fulfillment(t *testing.T, orderID int) int16 {
	t.Helper()
	return f.client.Order.GetX(context.Background(), orderID).FulfillmentStatus
}

func trackingNumber(s string) *string { return &s }

func items(in ...shipping.ShipmentItemInput) *[]shipping.ShipmentItemInput { return &in }

func TestServiceCreateShipmentAdvancesFulfillmentStatus(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := f.newMember(t, "flow@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", trackingNumber("TRACK-1"), nil)
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
	memberID := f.newMember(t, "cancelled@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	f.client.Order.UpdateOneID(ord.ID).SetStatus(order.StatusCancelled).SaveX(ctx)

	_, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil, nil)
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
	memberID := f.newMember(t, "already@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	if _, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil, nil); err != nil {
		t.Fatalf("first CreateShipment: %v", err)
	}

	_, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "7-11 超商取貨", nil, nil)
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
	memberID := f.newMember(t, "blank@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	_, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "   ", nil, nil)
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
	memberID := f.newMember(t, "delivered@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil, nil)
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
	memberID := f.newMember(t, "returned@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil, nil)
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
	memberID := f.newMember(t, "repeat-delivered@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil, nil)
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
	memberID := f.newMember(t, "returned-to-delivered@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil, nil)
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
	memberID := f.newMember(t, "unshipped@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	_, err := f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, 999999, shipping.DeliveredStatus)
	if err != shipping.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestServiceGetShipmentForMemberCrossMemberIsNotFound(t *testing.T) {
	f := setupShippingFixtures(t)
	ownerID := f.newMember(t, "owner@t.dev")
	otherID := f.newMember(t, "other@t.dev")
	ord := f.newOrder(t, ownerID)
	ctx := context.Background()

	if _, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil, nil); err != nil {
		t.Fatalf("CreateShipment: %v", err)
	}

	_, err := f.svc.GetShipmentForMember(ctx, f.shopID, otherID, ord.ID)
	if err != shipping.ErrNotFound {
		t.Fatalf("expected ErrNotFound for cross-member access, got %v", err)
	}
}

func TestServiceGetShipmentForMemberNoShipmentYetIsNotFound(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := f.newMember(t, "no-shipment@t.dev")
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
	memberID := f.newMember(t, "publish-returned@t.dev")
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

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil, nil)
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
	memberID := f.newMember(t, "publish-delivered@t.dev")
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

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil, nil)
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
	memberID := f.newMember(t, "nil-dispatcher@t.dev")
	ord := f.newOrder(t, memberID)
	ctx := context.Background()

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil, nil)
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

// ── change partial-shipment ───────────────────────────────────────────
//
// Everything above this line is the pre-partial-shipment suite, and it was not
// edited beyond threading the new (always nil) items argument. That it still
// passes unchanged IS the compatibility claim of design D5: omitting the item
// list ships everything outstanding, so every request written before line items
// existed behaves byte for byte as it did.

func TestServicePartialShipmentMarksOrderPartiallyShipped(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := f.newMember(t, "partial@t.dev")
	ord := f.newOrderWithLines(t, memberID, 1, 2) // 兩列：1 件 + 2 件
	ctx := context.Background()
	lines := f.orderItems(t, ord.ID)

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil,
		items(shipping.ShipmentItemInput{OrderItemID: lines[0].ID, Quantity: 1}))
	if err != nil {
		t.Fatalf("CreateShipment (partial): %v", err)
	}
	if len(sh.Edges.Items) != 1 || sh.Edges.Items[0].Quantity != 1 {
		t.Fatalf("expected exactly one shipment item of quantity 1, got %+v", sh.Edges.Items)
	}
	if got := f.fulfillment(t, ord.ID); got != shipping.PartiallyShippedStatus {
		t.Fatalf("expected order fulfillment_status=4 (partially shipped), got %d", got)
	}

	after := f.orderItems(t, ord.ID)
	if after[0].ShippedQuantity != 1 || after[1].ShippedQuantity != 0 {
		t.Fatalf("expected shipped quantities [1 0], got [%d %d]",
			after[0].ShippedQuantity, after[1].ShippedQuantity)
	}
}

func TestServiceShippingTheRemainderCompletesTheOrder(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := f.newMember(t, "remainder@t.dev")
	ord := f.newOrderWithLines(t, memberID, 1, 2)
	ctx := context.Background()
	lines := f.orderItems(t, ord.ID)

	if _, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil,
		items(shipping.ShipmentItemInput{OrderItemID: lines[0].ID, Quantity: 1})); err != nil {
		t.Fatalf("first CreateShipment: %v", err)
	}
	// 第二次省略 items：意思是「把剩下的全出掉」，這正是後台那顆「整筆出貨」按鈕。
	second, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "7-11 超商取貨", nil, nil)
	if err != nil {
		t.Fatalf("second CreateShipment (remainder): %v", err)
	}
	if len(second.Edges.Items) != 1 || second.Edges.Items[0].Quantity != 2 {
		t.Fatalf("expected the remainder (one line, quantity 2), got %+v", second.Edges.Items)
	}
	if got := f.fulfillment(t, ord.ID); got != shipping.ShippedStatus {
		t.Fatalf("expected order fulfillment_status=1 (shipped), got %d", got)
	}

	// 出完之後再出＝409，並且訊息說的是「已全部出貨」而不是「已有出貨紀錄」——
	// 那是這條守衛從「這張訂單出過了嗎」變成「還剩下什麼嗎」的地方。
	_, err = f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil, nil)
	var ce *shipping.ConflictError
	if !asConflict(err, &ce) {
		t.Fatalf("expected *shipping.ConflictError, got %T: %v", err, err)
	}
}

// 已出貨數量必須恆等於 shipment_items 的加總。這是 design D3 對「衍生欄位會漂移」
// 的直接反制：欄位是權威，但它必須與明細一致，而只有跨多筆出貨才驗得出來。
func TestServiceShippedQuantityEqualsSumOfShipmentItems(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := f.newMember(t, "sum@t.dev")
	ord := f.newOrderWithLines(t, memberID, 3)
	ctx := context.Background()
	lines := f.orderItems(t, ord.ID)

	for _, q := range []int32{1, 2} {
		if _, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil,
			items(shipping.ShipmentItemInput{OrderItemID: lines[0].ID, Quantity: q})); err != nil {
			t.Fatalf("CreateShipment(q=%d): %v", q, err)
		}
	}

	var summed int32
	for _, si := range f.client.ShipmentItem.Query().
		Where(entshipmentitem.OrderItemIDEQ(lines[0].ID)).AllX(ctx) {
		summed += si.Quantity
	}
	stored := f.orderItems(t, ord.ID)[0].ShippedQuantity
	if summed != stored {
		t.Fatalf("shipment_items 加總 %d != order_items.shipped_quantity %d（衍生欄位已漂移）",
			summed, stored)
	}
	if stored != 3 {
		t.Fatalf("expected shipped_quantity=3, got %d", stored)
	}
}

func TestServiceRejectsOverShipment(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := f.newMember(t, "overship@t.dev")
	ord := f.newOrderWithLines(t, memberID, 2)
	ctx := context.Background()
	lines := f.orderItems(t, ord.ID)

	if _, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil,
		items(shipping.ShipmentItemInput{OrderItemID: lines[0].ID, Quantity: 1})); err != nil {
		t.Fatalf("first CreateShipment: %v", err)
	}

	_, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil,
		items(shipping.ShipmentItemInput{OrderItemID: lines[0].ID, Quantity: 2}))
	var ve *shipping.ValidationError
	if !asValidation(err, &ve) {
		t.Fatalf("expected *shipping.ValidationError, got %T: %v", err, err)
	}

	// 被拒的請求不得留下任何痕跡——這是交易性的重點，不只是回傳碼。
	if n := f.client.Shipment.Query().CountX(ctx); n != 1 {
		t.Fatalf("expected exactly one shipment to exist, got %d", n)
	}
	if got := f.orderItems(t, ord.ID)[0].ShippedQuantity; got != 1 {
		t.Fatalf("expected shipped_quantity to stay 1, got %d", got)
	}
	if got := f.fulfillment(t, ord.ID); got != shipping.PartiallyShippedStatus {
		t.Fatalf("expected order to stay partially shipped, got %d", got)
	}
}

func TestServiceRejectsMalformedShipmentItems(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := f.newMember(t, "malformed@t.dev")
	ord := f.newOrderWithLines(t, memberID, 2)
	other := f.newOrderWithLines(t, f.newMember(t, "other@t.dev"), 1)
	ctx := context.Background()
	lines := f.orderItems(t, ord.ID)
	foreign := f.orderItems(t, other.ID)

	cases := []struct {
		name string
		in   *[]shipping.ShipmentItemInput
	}{
		{"空陣列", items()},
		{"外訂單的品項", items(shipping.ShipmentItemInput{OrderItemID: foreign[0].ID, Quantity: 1})},
		{"重複指名同一品項", items(
			shipping.ShipmentItemInput{OrderItemID: lines[0].ID, Quantity: 1},
			shipping.ShipmentItemInput{OrderItemID: lines[0].ID, Quantity: 1})},
		{"數量為零", items(shipping.ShipmentItemInput{OrderItemID: lines[0].ID, Quantity: 0})},
		{"數量為負", items(shipping.ShipmentItemInput{OrderItemID: lines[0].ID, Quantity: -1})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil, tc.in)
			var ve *shipping.ValidationError
			if !asValidation(err, &ve) {
				t.Fatalf("expected *shipping.ValidationError, got %T: %v", err, err)
			}
			if n := f.client.Shipment.Query().Where(entshipment.OrderIDEQ(ord.ID)).CountX(ctx); n != 0 {
				t.Fatalf("expected no shipment to have been created, got %d", n)
			}
		})
	}
}

func TestServiceOneDeliveredParcelDoesNotDeliverTheOrder(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := f.newMember(t, "twoparcels@t.dev")
	ord := f.newOrderWithLines(t, memberID, 1, 1)
	ctx := context.Background()
	lines := f.orderItems(t, ord.ID)

	first, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil,
		items(shipping.ShipmentItemInput{OrderItemID: lines[0].ID, Quantity: 1}))
	if err != nil {
		t.Fatalf("first CreateShipment: %v", err)
	}
	second, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "7-11 超商取貨", nil,
		items(shipping.ShipmentItemInput{OrderItemID: lines[1].ID, Quantity: 1}))
	if err != nil {
		t.Fatalf("second CreateShipment: %v", err)
	}
	if got := f.fulfillment(t, ord.ID); got != shipping.ShippedStatus {
		t.Fatalf("expected order shipped after both parcels, got %d", got)
	}

	// 這是 change partial-shipment 之前會答錯的那一題：一箱送達不等於整張訂單送達。
	if _, err := f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, first.ID, shipping.DeliveredStatus); err != nil {
		t.Fatalf("AdvanceShipment(delivered): %v", err)
	}
	if got := f.fulfillment(t, ord.ID); got != shipping.ShippedStatus {
		t.Fatalf("one delivered parcel must not deliver the order; got fulfillment_status=%d", got)
	}
	if _, err := f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, second.ID, shipping.DeliveredStatus); err != nil {
		t.Fatalf("AdvanceShipment(delivered, second): %v", err)
	}
	if got := f.fulfillment(t, ord.ID); got != shipping.DeliveredStatus {
		t.Fatalf("expected order delivered once every parcel arrived, got %d", got)
	}
}

func TestServiceReturnedOrderAcceptsNoFurtherShipments(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := f.newMember(t, "returned-blocks@t.dev")
	ord := f.newOrderWithLines(t, memberID, 1, 1)
	ctx := context.Background()
	lines := f.orderItems(t, ord.ID)

	sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil,
		items(shipping.ShipmentItemInput{OrderItemID: lines[0].ID, Quantity: 1}))
	if err != nil {
		t.Fatalf("CreateShipment: %v", err)
	}
	if _, err := f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, sh.ID, shipping.ReturnedStatus); err != nil {
		t.Fatalf("AdvanceShipment(returned): %v", err)
	}
	// 退貨壓過「還沒出完」。
	if got := f.fulfillment(t, ord.ID); got != shipping.ReturnedStatus {
		t.Fatalf("expected order returned, got %d", got)
	}

	_, err = f.svc.CreateShipment(ctx, f.shopID, ord.ID, "7-11 超商取貨", nil,
		items(shipping.ShipmentItemInput{OrderItemID: lines[1].ID, Quantity: 1}))
	var ce *shipping.ConflictError
	if !asConflict(err, &ce) {
		t.Fatalf("expected *shipping.ConflictError, got %T: %v", err, err)
	}
	if n := f.client.Shipment.Query().Where(entshipment.OrderIDEQ(ord.ID)).CountX(ctx); n != 1 {
		t.Fatalf("expected the second shipment not to exist, got %d shipments", n)
	}
}

// 單數的顧客端點在多筆出貨下**必須不是 404**。它以前是 `.Only()`，而 `.Only()` 對
// 兩列回錯，錯誤處理又把任何錯誤變成 ErrNotFound——所以一個成功出了兩箱的商家，
// 會讓顧客查不到任何一箱。
func TestServiceMemberReadsAcrossMultipleShipments(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := f.newMember(t, "memberreads@t.dev")
	ord := f.newOrderWithLines(t, memberID, 1, 1)
	ctx := context.Background()
	lines := f.orderItems(t, ord.ID)

	if _, err := f.svc.ListShipmentsForMember(ctx, f.shopID, memberID, ord.ID); err != nil {
		t.Fatalf("尚未出貨時複數端點必須成功並回空集合，卻回了：%v", err)
	}

	var ids []int
	for _, ln := range lines {
		sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil,
			items(shipping.ShipmentItemInput{OrderItemID: ln.ID, Quantity: 1}))
		if err != nil {
			t.Fatalf("CreateShipment: %v", err)
		}
		ids = append(ids, sh.ID)
	}

	all, err := f.svc.ListShipmentsForMember(ctx, f.shopID, memberID, ord.ID)
	if err != nil {
		t.Fatalf("ListShipmentsForMember: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 shipments, got %d", len(all))
	}
	if all[0].ID != ids[1] || all[1].ID != ids[0] {
		t.Fatalf("expected newest-first ordering, got %d then %d", all[0].ID, all[1].ID)
	}
	for _, sh := range all {
		if len(sh.Edges.Items) != 1 {
			t.Fatalf("expected each shipment to carry its items, got %+v", sh.Edges.Items)
		}
	}

	single, err := f.svc.GetShipmentForMember(ctx, f.shopID, memberID, ord.ID)
	if err != nil {
		t.Fatalf("單數端點在多筆出貨下必須成功，卻回了：%v", err)
	}
	if single.ID != ids[1] {
		t.Fatalf("expected the most recent shipment (%d), got %d", ids[1], single.ID)
	}
}

// 兩箱各自退貨會發佈兩次 events.OrderReturned，而點數只能被扣回一次。
//
// 這個正確性是**繼承來的**，不是本 change 加的：point_transactions 的
// UNIQUE(order_id, kind) 是仲裁者，points.Service.HandleOrderReturned 對重複投遞
// 早就是 no-op。但它是為了另一個理由（金流 webhook 重送）建的機制，而「部分出貨讓
// 同一張訂單可以退兩次」是一條它從未被要求回答的新路徑——所以這裡把它釘住。
func TestServiceMultipleReturnedShipmentsClawBackPointsOnlyOnce(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := f.newMember(t, "doublereturn@t.dev")
	ord := f.newOrderWithLines(t, memberID, 1, 1)
	ctx := context.Background()
	lines := f.orderItems(t, ord.ID)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pointsSvc := &points.Service{Client: f.client, Log: log}
	dispatcher := events.NewDispatcher(log)
	dispatcher.Subscribe(pointsSvc.Handle)
	f.svc.Dispatcher = dispatcher

	// 先讓這張訂單真的核發過點數（付款成功的既有路徑）。
	if err := pointsSvc.HandleOrderPaid(ctx, events.OrderPaymentSucceeded{
		ShopID: f.shopID, OrderID: ord.ID, MemberID: memberID,
		AwardBasisAmount: 2000, Currency: "TWD",
	}); err != nil {
		t.Fatalf("HandleOrderPaid: %v", err)
	}
	if got := f.client.Member.GetX(ctx, memberID).Points; got != 20 {
		t.Fatalf("expected 20 points awarded, got %d", got)
	}

	var shipments []int
	for _, ln := range lines {
		sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil,
			items(shipping.ShipmentItemInput{OrderItemID: ln.ID, Quantity: 1}))
		if err != nil {
			t.Fatalf("CreateShipment: %v", err)
		}
		shipments = append(shipments, sh.ID)
	}

	if _, err := f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, shipments[0], shipping.ReturnedStatus); err != nil {
		t.Fatalf("first AdvanceShipment(returned): %v", err)
	}
	if got := f.client.Member.GetX(ctx, memberID).Points; got != 0 {
		t.Fatalf("expected the whole order's 20 points clawed back, got %d", got)
	}
	if _, err := f.svc.AdvanceShipment(ctx, f.shopID, ord.ID, shipments[1], shipping.ReturnedStatus); err != nil {
		t.Fatalf("second AdvanceShipment(returned): %v", err)
	}
	if got := f.client.Member.GetX(ctx, memberID).Points; got != 0 {
		t.Fatalf("expected the balance to stay 0 (never negative, never double-clawed), got %d", got)
	}
	clawbacks := f.client.PointTransaction.Query().
		Where(entpointtransaction.OrderIDEQ(ord.ID), entpointtransaction.KindEQ(points.KindOrderClawback)).
		CountX(ctx)
	if clawbacks != 1 {
		t.Fatalf("expected exactly one clawback ledger row, got %d", clawbacks)
	}
}

// migrationBackfillSQL returns the backfill half of this change's migration —
// **read from the migration file itself**, never copied into this test.
//
// A copy would pass forever after someone edited the real file, which is the
// one failure this test exists to catch. The marker is the section header in
// 20260801140031_add_partial_shipment.up.sql; everything after it is the
// backfill.
func migrationBackfillSQL(t *testing.T) string {
	t.Helper()
	const marker = "-- ── backfill"
	path := filepath.Join("..", "..", "migrations", "20260801140031_add_partial_shipment.up.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	idx := strings.Index(string(raw), marker)
	if idx < 0 {
		t.Fatalf("migration %s no longer contains the %q section — if the backfill moved, this test must follow it", path, marker)
	}
	return string(raw)[idx:]
}

// 資料遷移的回填不是方便，是正確性：沒有它，一筆歷史上已送達的訂單會在下一次推導時
// 掉進「已出數量 < 訂購數量」而變成部分出貨。
//
// 這支測試是 A/B 的，而且兩邊都走**真正的服務路徑**（`AdvanceShipment`），不是把推導
// 函式再算一次——後者只會證明純函式還是純函式。A 組不回填，用來證明症狀是真的；B 組
// 套用**從 migration 檔案讀出來的**同一段 SQL，證明它治好了。
//
// A 組必須在回填之前斷言完畢：回填是整表的，會一併治好 A。
func TestBackfillMakesHistoricalShipmentsDeriveToTheirStoredStatus(t *testing.T) {
	f := setupShippingFixtures(t)
	memberID := f.newMember(t, "backfill@t.dev")
	ctx := context.Background()

	// historical 造出 partial-shipment 之前唯一可能的形狀：一筆涵蓋整張訂單的出貨，
	// 但沒有 shipment_items、order_items.shipped_quantity 為 0。
	historical := func(t *testing.T) (int, int) {
		t.Helper()
		ord := f.newOrderWithLines(t, memberID, 2)
		sh, err := f.svc.CreateShipment(ctx, f.shopID, ord.ID, "黑貓宅急便", nil, nil)
		if err != nil {
			t.Fatalf("CreateShipment: %v", err)
		}
		if got := f.fulfillment(t, ord.ID); got != shipping.ShippedStatus {
			t.Fatalf("precondition: expected shipped, got %d", got)
		}
		f.client.ShipmentItem.Delete().Where(entshipmentitem.ShipmentIDEQ(sh.ID)).ExecX(ctx)
		f.client.OrderItem.Update().Where(entorderitem.OrderIDEQ(ord.ID)).SetShippedQuantity(0).ExecX(ctx)
		return ord.ID, sh.ID
	}

	// ── A：未回填。症狀必須真的重現，否則這支測試證明不了任何事。
	ordA, shA := historical(t)
	if _, err := f.svc.AdvanceShipment(ctx, f.shopID, ordA, shA, shipping.DeliveredStatus); err != nil {
		t.Fatalf("AdvanceShipment(delivered) on the un-backfilled order: %v", err)
	}
	if got := f.fulfillment(t, ordA); got != shipping.PartiallyShippedStatus {
		t.Fatalf("症狀沒有重現：未回填的歷史訂單被標成 %d，預期 %d（部分出貨）。"+
			"若推導規則改過而不再有這個症狀，回填的必要性也要跟著重新評估", got, shipping.PartiallyShippedStatus)
	}

	// ── B：套用 migration 逐字的回填。
	ordB, shB := historical(t)
	if _, err := f.client.ExecContext(ctx, migrationBackfillSQL(t)); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	restored := f.orderItems(t, ordB)
	if restored[0].ShippedQuantity != 2 {
		t.Fatalf("expected the backfill to restore shipped_quantity=2, got %d", restored[0].ShippedQuantity)
	}
	shipItems := f.client.ShipmentItem.Query().Where(entshipmentitem.ShipmentIDEQ(shB)).AllX(ctx)
	if len(shipItems) != 1 || shipItems[0].Quantity != 2 {
		t.Fatalf("expected one backfilled shipment item of quantity 2, got %+v", shipItems)
	}

	// 推導值 == 儲存值：走真正的服務路徑，結果必須是「已送達」而不是「部分出貨」。
	if _, err := f.svc.AdvanceShipment(ctx, f.shopID, ordB, shB, shipping.DeliveredStatus); err != nil {
		t.Fatalf("AdvanceShipment(delivered) on the backfilled order: %v", err)
	}
	if got := f.fulfillment(t, ordB); got != shipping.DeliveredStatus {
		t.Fatalf("回填後的歷史訂單推導成 %d，預期 %d（已送達）", got, shipping.DeliveredStatus)
	}
}
