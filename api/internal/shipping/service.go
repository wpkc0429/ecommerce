// Package shipping implements the shipping-logistics domain (change
// shipping-logistics): merchant-configured shipping methods (CRUD),
// shipment records (shop-scoped, at most one per order), and the controlled
// path that advances orders.fulfillment_status through the sole controlled
// entry point order.Service.UpdateFulfillmentStatus (order-management
// design D6) — this package never writes ent.Order directly.
//
// ShippingMethod and Shipment share one Service (unlike order/payment's
// separate packages) because the two tables are tightly coupled in this
// domain — shipping_methods is merchant configuration, shipments is the
// operational record that configuration informs — mirroring catalog.Service
// managing Category/Product/ProductSKU together (design D5).
package shipping

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"ksdevworks/ecommerce/api/internal/ent"
	entshipment "ksdevworks/ecommerce/api/internal/ent/shipment"
	entshippingmethod "ksdevworks/ecommerce/api/internal/ent/shippingmethod"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/order"
)

// ErrNotFound marks missing resources (handler → 404). Also returned when an
// orderID/shipmentID exists but is out of scope for the caller (foreign
// shop, foreign member) — callers must not distinguish "doesn't exist" from
// "not yours" (mirrors order.ErrNotFound/payment.ErrNotFound convention).
var ErrNotFound = errors.New("shipping: not found")

// Detail locates one validation problem (JSON Pointer-ish; same convention
// as order.Detail/payment.Detail/catalog.Detail).
type Detail struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

// ValidationError carries 422 payloads (design D12 error envelope, reused
// from order/payment/catalog's shared error envelope convention).
type ValidationError struct {
	Message string
	Details []Detail
}

func (e *ValidationError) Error() string { return e.Message }

func validationErr(pointer, msg string) *ValidationError {
	return &ValidationError{Message: msg, Details: []Detail{{Pointer: pointer, Message: msg}}}
}

// ConflictError carries 409 payloads (design D3/D4): an illegal shipment
// state transition, or an order that isn't in a shippable state.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// Shipment/fulfillment status enumeration (design D2) — shipments.status
// deliberately reuses orders.fulfillment_status's non-zero values rather
// than inventing a parallel enum. There is no "0" constant here: a
// nonexistent shipments row already means unfulfilled (order-management's
// own order.FulfillmentStatusUnfulfilled owns that value).
const (
	ShippedStatus   int16 = 1
	DeliveredStatus int16 = 2
	ReturnedStatus  int16 = 3
)

func statusName(status int16) string {
	switch status {
	case ShippedStatus:
		return "shipped"
	case DeliveredStatus:
		return "delivered"
	case ReturnedStatus:
		return "returned"
	default:
		return "unknown"
	}
}

// Service implements shipping-methods CRUD and shipment operations, all
// scoped by explicit shopID/orderID/memberID parameters (never ambient
// tenant/member context — same convention as order.Service/payment.Service).
//
// Orders is order.Service — the ONLY way this package ever advances
// orders.fulfillment_status is Orders.UpdateFulfillmentStatus
// (order-management design D6). Nothing in this package writes ent.Order
// directly.
//
// Dispatcher is optional (nil-safe — see AdvanceShipment) and publishes
// events.OrderReturned when a shipment transitions to returned (change
// member-tiers-and-points design D1/D5): this package still never imports
// points — it only publishes a generic domain event.
type Service struct {
	Client     *ent.Client
	Orders     *order.Service
	Dispatcher *events.Dispatcher
}

// ── shipping methods CRUD (design D1) ──────────────────────────────────

// ShippingMethodInput is the payload of shipping method creation.
type ShippingMethodInput struct {
	Name     string `json:"name"`
	Carrier  string `json:"carrier"`
	FlatRate int64  `json:"flat_rate"`
	IsActive *bool  `json:"is_active"`
}

// ShippingMethodUpdate is a partial update — nil fields are left unchanged.
type ShippingMethodUpdate struct {
	Name     *string `json:"name"`
	Carrier  *string `json:"carrier"`
	FlatRate *int64  `json:"flat_rate"`
	IsActive *bool   `json:"is_active"`
}

func (s *Service) CreateShippingMethod(ctx context.Context, shopID int, in ShippingMethodInput) (*ent.ShippingMethod, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, validationErr("/name", "name is required")
	}
	carrier := strings.TrimSpace(in.Carrier)
	if carrier == "" {
		return nil, validationErr("/carrier", "carrier is required")
	}
	if in.FlatRate < 0 {
		return nil, validationErr("/flat_rate", "flat_rate must be >= 0")
	}
	create := s.Client.ShippingMethod.Create().
		SetShopID(shopID).SetName(name).SetCarrier(carrier).SetFlatRate(in.FlatRate)
	if in.IsActive != nil {
		create.SetIsActive(*in.IsActive)
	}
	sm, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("shipping: create shipping method: %w", err)
	}
	return sm, nil
}

func (s *Service) GetShippingMethod(ctx context.Context, shopID, id int) (*ent.ShippingMethod, error) {
	sm, err := s.Client.ShippingMethod.Query().
		Where(entshippingmethod.IDEQ(id), entshippingmethod.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	return sm, nil
}

// ListShippingMethods returns every shipping method within shopID, ordered
// by id.
func (s *Service) ListShippingMethods(ctx context.Context, shopID int) ([]*ent.ShippingMethod, error) {
	rows, err := s.Client.ShippingMethod.Query().
		Where(entshippingmethod.ShopIDEQ(shopID)).
		Order(entshippingmethod.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("shipping: list shipping methods: %w", err)
	}
	return rows, nil
}

func (s *Service) UpdateShippingMethod(ctx context.Context, shopID, id int, in ShippingMethodUpdate) (*ent.ShippingMethod, error) {
	sm, err := s.Client.ShippingMethod.Query().
		Where(entshippingmethod.IDEQ(id), entshippingmethod.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	upd := sm.Update()
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, validationErr("/name", "name cannot be empty")
		}
		upd.SetName(name)
	}
	if in.Carrier != nil {
		carrier := strings.TrimSpace(*in.Carrier)
		if carrier == "" {
			return nil, validationErr("/carrier", "carrier cannot be empty")
		}
		upd.SetCarrier(carrier)
	}
	if in.FlatRate != nil {
		if *in.FlatRate < 0 {
			return nil, validationErr("/flat_rate", "flat_rate must be >= 0")
		}
		upd.SetFlatRate(*in.FlatRate)
	}
	if in.IsActive != nil {
		upd.SetIsActive(*in.IsActive)
	}
	sm, err = upd.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("shipping: update shipping method: %w", err)
	}
	return sm, nil
}

func (s *Service) DeleteShippingMethod(ctx context.Context, shopID, id int) error {
	sm, err := s.Client.ShippingMethod.Query().
		Where(entshippingmethod.IDEQ(id), entshippingmethod.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return ErrNotFound
	}
	return s.Client.ShippingMethod.DeleteOne(sm).Exec(ctx)
}

// ── shipments (design D3/D4) ────────────────────────────────────────────

// CreateShipment ships orderID within shopID (design D3): creating the row
// IS the shipping event (no separate "draft" step). Rejects with
// ErrNotFound if the order doesn't exist in this shop, with ConflictError
// if the order is cancelled or already has a shipment (fulfillment_status
// isn't still unfulfilled), and with ValidationError if carrier is blank.
//
// See design D4 for the full concurrency-safety walkthrough: the
// fulfillment_status advance happens first via the sole controlled entry
// point, and the shipments.order_id unique index — not this method's own
// read-then-write check — is the final arbiter against a concurrent
// double-ship attempt.
func (s *Service) CreateShipment(ctx context.Context, shopID, orderID int, carrier string, trackingNumber *string) (*ent.Shipment, error) {
	carrier = strings.TrimSpace(carrier)
	if carrier == "" {
		return nil, validationErr("/carrier", "carrier is required")
	}

	ord, err := s.Orders.GetOrderAdmin(ctx, shopID, orderID)
	if err != nil {
		if errors.Is(err, order.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("shipping: query order: %w", err)
	}
	if ord.Status == order.StatusCancelled {
		return nil, &ConflictError{Message: "order is cancelled"}
	}
	if ord.FulfillmentStatus != order.FulfillmentStatusUnfulfilled {
		return nil, &ConflictError{Message: "order already has a shipment"}
	}

	if _, err := s.Orders.UpdateFulfillmentStatus(ctx, shopID, orderID, ShippedStatus); err != nil {
		return nil, fmt.Errorf("shipping: advance fulfillment status: %w", err)
	}

	create := s.Client.Shipment.Create().
		SetShopID(shopID).SetOrderID(orderID).
		SetCarrier(carrier).SetStatus(ShippedStatus).SetShippedAt(time.Now())
	if trackingNumber != nil {
		if tn := strings.TrimSpace(*trackingNumber); tn != "" {
			create.SetTrackingNumber(tn)
		}
	}
	sh, err := create.Save(ctx)
	if ent.IsConstraintError(err) {
		// Lost a concurrent race (design D4): another request already
		// claimed this order's one-shipment slot via the order_id unique
		// index. The fulfillment_status advance above already happened and
		// is correct — it matches the winner's shipment — so there is
		// nothing to roll back here.
		return nil, &ConflictError{Message: "order already has a shipment"}
	}
	if err != nil {
		return nil, fmt.Errorf("shipping: create shipment: %w", err)
	}
	return sh, nil
}

// AdvanceShipment transitions shipmentID (scoped by shopID+orderID) to
// target, which MUST be DeliveredStatus or ReturnedStatus (design D3): the
// only legal transitions are shipped->delivered and shipped->returned, both
// terminal. Uses a conditional UPDATE (CAS on status=ShippedStatus) as the
// concurrency guard — mirrors order.Service.CancelOrder/
// payment.Service.HandleWebhook's own CAS pattern — so two concurrent
// conflicting advance calls can never both win.
func (s *Service) AdvanceShipment(ctx context.Context, shopID, orderID, shipmentID int, target int16) (*ent.Shipment, error) {
	if target != DeliveredStatus && target != ReturnedStatus {
		return nil, validationErr("/status", "status must be delivered or returned")
	}

	sh, err := s.Client.Shipment.Query().
		Where(entshipment.IDEQ(shipmentID), entshipment.ShopIDEQ(shopID), entshipment.OrderIDEQ(orderID)).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}

	upd := s.Client.Shipment.Update().
		Where(entshipment.IDEQ(shipmentID), entshipment.StatusEQ(ShippedStatus)).
		SetStatus(target)
	if target == DeliveredStatus {
		upd = upd.SetDeliveredAt(time.Now())
	}
	affected, err := upd.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("shipping: advance shipment: %w", err)
	}
	if affected == 0 {
		return nil, &ConflictError{
			Message: fmt.Sprintf("shipment is %s, cannot transition to %s", statusName(sh.Status), statusName(target)),
		}
	}

	ord, err := s.Orders.UpdateFulfillmentStatus(ctx, shopID, orderID, target)
	if err != nil {
		return nil, fmt.Errorf("shipping: advance fulfillment status: %w", err)
	}
	// design D5: only the returned transition triggers a points clawback —
	// delivered is not published, there is nothing for points to react to.
	if target == ReturnedStatus && s.Dispatcher != nil {
		s.Dispatcher.Publish(ctx, events.OrderReturned{
			ShopID: ord.ShopID, OrderID: ord.ID, MemberID: ord.MemberID,
		})
	}

	updated, err := s.Client.Shipment.Get(ctx, shipmentID)
	if err != nil {
		return nil, fmt.Errorf("shipping: reload shipment: %w", err)
	}
	return updated, nil
}

// ListShipmentsAdmin returns every shipment for orderID within shopID (0 or
// 1 row this phase — design Non-Goals), newest first. Verifies the order
// itself is visible in shopID first (order.Service.GetOrderAdmin) so a
// nonexistent/foreign order surfaces as ErrNotFound rather than silently
// returning an empty list.
func (s *Service) ListShipmentsAdmin(ctx context.Context, shopID, orderID int) ([]*ent.Shipment, error) {
	if _, err := s.Orders.GetOrderAdmin(ctx, shopID, orderID); err != nil {
		if errors.Is(err, order.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("shipping: query order: %w", err)
	}
	rows, err := s.Client.Shipment.Query().
		Where(entshipment.ShopIDEQ(shopID), entshipment.OrderIDEQ(orderID)).
		Order(entshipment.ByID(entsql.OrderDesc())).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("shipping: list shipments: %w", err)
	}
	return rows, nil
}

// GetShipmentForMember returns the shipment for orderID, scoped by both
// shopID and memberID ownership of the order (member self-service —
// mirrors order.Service.GetOrder/payment.Service.InitiatePayment's ownership
// check). Returns ErrNotFound if the order doesn't exist/isn't the member's,
// or if the order exists but has no shipment yet.
func (s *Service) GetShipmentForMember(ctx context.Context, shopID, memberID, orderID int) (*ent.Shipment, error) {
	if _, err := s.Orders.GetOrder(ctx, shopID, memberID, orderID); err != nil {
		if errors.Is(err, order.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("shipping: query order: %w", err)
	}
	sh, err := s.Client.Shipment.Query().
		Where(entshipment.ShopIDEQ(shopID), entshipment.OrderIDEQ(orderID)).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	return sh, nil
}
