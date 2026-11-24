// Package shipping implements the shipping-logistics domain (change
// shipping-logistics): merchant-configured shipping methods (CRUD),
// shipment records (shop-scoped), and the controlled path that advances
// orders.fulfillment_status through the sole controlled entry point
// order.Service.UpdateFulfillmentStatus{,Tx} (order-management design D6) —
// this package never writes ent.Order directly.
//
// Since change partial-shipment an order may have many shipments, each naming
// the order items and quantities it carries. Two properties of that change are
// load-bearing everywhere below and are stated once here:
//
//   - **orders.fulfillment_status is derived, never asserted.** It is a pure
//     function of the order's shipments and its items' shipped quantities
//     (deriveFulfillmentStatus). Writing "the status of the shipment I just
//     touched" onto the order — which is what this package used to do — marks a
//     three-item order delivered because one of two parcels arrived.
//   - **Omitting the item list still means "ship everything outstanding".** So a
//     caller that predates line items gets byte-identical behavior, and the
//     common florist case (ship it all) stays one request.
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
	"sort"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"ksdevworks/ecommerce/api/internal/ent"
	entorder "ksdevworks/ecommerce/api/internal/ent/order"
	entorderitem "ksdevworks/ecommerce/api/internal/ent/orderitem"
	entshipment "ksdevworks/ecommerce/api/internal/ent/shipment"
	entshipmentitem "ksdevworks/ecommerce/api/internal/ent/shipmentitem"
	entshippingmethod "ksdevworks/ecommerce/api/internal/ent/shippingmethod"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/payment"
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
	// PartiallyShippedStatus (change partial-shipment, design D1) applies to
	// orders ONLY — a shipments row never takes it, and the DB CHECK
	// shipments_status_check (1, 2, 3) enforces that. "Partly" is a property of
	// an order; a parcel is always wholly a parcel.
	//
	// It is 4 rather than something slotted between 0 and 1 because the existing
	// values' numbers may not move: this is an additive extension of a live
	// enumeration, not a renumbering. The consequence is that the enum's numeric
	// order no longer matches its lifecycle order — nothing in this codebase
	// compares fulfillment_status with < or >, and nothing may start.
	PartiallyShippedStatus int16 = 4
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

// ShipmentItemInput names one order line and how many of it go in this parcel
// (change partial-shipment). The identifier is an order_items id, not a
// sku_id: the same SKU can appear on two lines of one order when the gift
// messages differ (change order-extras), and those two lines can genuinely
// travel in different boxes.
type ShipmentItemInput struct {
	OrderItemID int   `json:"order_item_id"`
	Quantity    int32 `json:"quantity"`
}

// deriveFulfillmentStatus computes an order's fulfillment_status from its
// shipments (change partial-shipment, design D2). Pure — every input is passed
// in, so the branch table is unit-testable without a database.
//
// The branches are ordered, and the order is the design:
//
//  1. no shipments → unfulfilled. Unchanged from before line items existed.
//  2. any returned → returned. The order-level axis has no vocabulary for
//     "partly returned", and the points clawback it triggers is all-or-nothing
//     per order (change member-tiers-and-points), so this coarse answer is the
//     one consistent with the mechanism that already exists. An order with a
//     returned shipment also accepts no further shipments, which keeps this
//     branch from being something the merchant can ship their way back out of.
//  3. shipped < ordered → partially shipped. Deliberately ahead of the
//     delivered branch: "two of three items arrived" is not a delivered order,
//     and telling the customer it is would be the exact lie this value exists to
//     prevent.
//  4. all delivered → delivered.
//  5. otherwise → shipped.
//
// **The acceptance criterion for this function is that it reproduces history.**
// For the single full shipment that was the only possible shape before: created
// → 1, delivered → 2, returned → 3, no shipment → 0. Every one of those falls
// out of the branches above unchanged, which is why the migration back-fills
// shipment_items for pre-existing shipments — without that, a historically
// delivered order would re-derive through branch 3.
func deriveFulfillmentStatus(statuses []int16, orderedQty, shippedQty int32) int16 {
	if len(statuses) == 0 {
		return order.FulfillmentStatusUnfulfilled
	}
	allDelivered := true
	for _, st := range statuses {
		if st == ReturnedStatus {
			return ReturnedStatus
		}
		if st != DeliveredStatus {
			allDelivered = false
		}
	}
	if shippedQty < orderedQty {
		return PartiallyShippedStatus
	}
	if allDelivered {
		return DeliveredStatus
	}
	return ShippedStatus
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

// ListActiveShippingMethods returns only the shop's selectable shipping
// methods, ordered by id — the storefront-facing counterpart of
// ListShippingMethods (change order-extras, design D4).
//
// shipping-logistics design D7 declined to expose this, on the grounds that no
// consumer existed and a read-only endpoint nobody calls can only be tested for
// response shape. A merchant-owned storefront now needs the fees the platform
// will actually charge, so the premise no longer holds.
//
// Deactivated methods are omitted rather than flagged: a method the merchant
// retired is not offerable, and checkout refuses it anyway
// (order.Service.resolveShippingMethod).
func (s *Service) ListActiveShippingMethods(ctx context.Context, shopID int) ([]*ent.ShippingMethod, error) {
	rows, err := s.Client.ShippingMethod.Query().
		Where(entshippingmethod.ShopIDEQ(shopID), entshippingmethod.IsActiveEQ(true)).
		Order(entshippingmethod.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("shipping: list active shipping methods: %w", err)
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

// CreateShipment ships part or all of orderID within shopID (design D3,
// extended by change partial-shipment): creating the row IS the shipping event
// (no separate "draft" step).
//
// items is optional and its absence is a meaning, not a default: nil ships
// **every quantity not yet shipped**, which is both the behavior every caller
// had before line items existed (byte for byte — that is what keeps this v1
// endpoint additive) and the common case a florist actually wants. An empty
// non-nil slice is a contradiction ("ship nothing") and is rejected.
//
// Rejects with ErrNotFound if the order doesn't exist in this shop, with
// ConflictError if the order is cancelled, is not paid, is fully shipped, or
// already has a returned shipment, and with ValidationError if carrier is blank, an
// order_item_id is foreign or repeated, a quantity is non-positive, or a
// quantity would push a line past what was ordered.
//
// ── Concurrency (design D4) ───────────────────────────────────────────
//
// Everything happens in one transaction, and the transaction opens by taking a
// row lock on the order. Two guards, doing two different jobs:
//
//   - The conditional UPDATE on order_items.shipped_quantity plus its
//     affected-rows check is what makes over-shipping impossible. It is the same
//     shape as order.Service.Checkout's stock decrement — the only
//     concurrency-safe decrement in this API — and it would still be correct
//     with no lock at all.
//   - The row lock is what makes the *derived* order status correct. Without
//     it, two transactions shipping disjoint items of one order cannot see each
//     other's uncommitted rows under READ COMMITTED, so both derive "partially
//     shipped" and the order stays at 4 after everything has in fact shipped.
//     Two browser tabs are enough to produce that.
func (s *Service) CreateShipment(
	ctx context.Context, shopID, orderID int,
	carrier string, trackingNumber *string, items *[]ShipmentItemInput,
) (*ent.Shipment, error) {
	carrier = strings.TrimSpace(carrier)
	if carrier == "" {
		return nil, validationErr("/carrier", "carrier is required")
	}
	if items != nil && len(*items) == 0 {
		return nil, validationErr("/items", "items must not be empty; omit it entirely to ship everything outstanding")
	}

	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("shipping: begin tx: %w", err)
	}

	ord, err := lockOrder(ctx, tx, shopID, orderID)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if ord.Status == order.StatusCancelled {
		_ = tx.Rollback()
		return nil, &ConflictError{Message: "order is cancelled"}
	}
	// change order-and-invitation-polish, design D1: shipping is a post-payment
	// step. The rule is deliberately strict (paid only) because the data model
	// has no payment-method or cash-on-delivery marker to key an exception on.
	// Checked under the order row lock, so a payment landing concurrently is
	// seen consistently.
	if !isPaidForShipping(ord) {
		_ = tx.Rollback()
		return nil, &ConflictError{Message: "order is not paid"}
	}

	existing, err := tx.Shipment.Query().
		Where(entshipment.ShopIDEQ(shopID), entshipment.OrderIDEQ(orderID)).
		All(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("shipping: query shipments: %w", err)
	}
	for _, sh := range existing {
		// design D7: once anything has come back, this order's remaining items
		// are a customer-service conversation, not a second parcel. Allowing it
		// would produce the sequence returned → partially shipped → shipped, on
		// an order whose loyalty points have already been clawed back in full —
		// a state nothing downstream can interpret.
		if sh.Status == ReturnedStatus {
			_ = tx.Rollback()
			return nil, &ConflictError{Message: "order has a returned shipment and accepts no further shipments"}
		}
	}

	orderItems, err := tx.OrderItem.Query().
		Where(entorderitem.OrderIDEQ(orderID), entorderitem.ShopIDEQ(shopID)).
		Order(entorderitem.ByID()).
		All(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("shipping: query order items: %w", err)
	}

	lines, err := resolveShipmentLines(orderItems, items)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	sh, err := s.writeShipment(ctx, tx, shopID, orderID, carrier, trackingNumber, lines)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	statuses := make([]int16, 0, len(existing)+1)
	for _, e := range existing {
		statuses = append(statuses, e.Status)
	}
	statuses = append(statuses, ShippedStatus)
	if err := s.syncFulfillment(ctx, tx, shopID, orderID, statuses, orderItems, lines); err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("shipping: commit shipment: %w", err)
	}
	return s.loadShipment(ctx, sh.ID)
}

// isPaidForShipping reports whether the order may have shipments created
// (change order-and-invitation-polish, design D1). Refunded and partially
// refunded orders are not "paid" here either. This is the single place a future
// cash-on-delivery exception would be added.
func isPaidForShipping(o *ent.Order) bool {
	return o.PaymentStatus == payment.OrderPaymentStatusPaid
}

// lockOrder takes the row lock the derived status depends on, scoped by shop.
// A foreign or nonexistent order is ErrNotFound — callers must not be able to
// distinguish the two (package convention).
func lockOrder(ctx context.Context, tx *ent.Tx, shopID, orderID int) (*ent.Order, error) {
	ord, err := tx.Order.Query().
		Where(entorder.IDEQ(orderID), entorder.ShopIDEQ(shopID)).
		ForUpdate().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("shipping: lock order: %w", err)
	}
	return ord, nil
}

// resolveShipmentLines turns the request's optional item list into the concrete
// (order item, quantity) pairs this parcel carries, rejecting everything the
// service can decide without touching the database.
//
// Ordering is by order item id, deterministically, so the created
// shipment_items rows and the response read the same way for identical requests.
func resolveShipmentLines(orderItems []*ent.OrderItem, items *[]ShipmentItemInput) ([]shipmentLine, error) {
	byID := make(map[int]*ent.OrderItem, len(orderItems))
	for _, oi := range orderItems {
		byID[oi.ID] = oi
	}

	if items == nil {
		// The whole outstanding remainder. Callers that predate line items land
		// here, and so does the back office's "ship it all" button.
		var lines []shipmentLine
		for _, oi := range orderItems {
			if remaining := oi.Quantity - oi.ShippedQuantity; remaining > 0 {
				lines = append(lines, shipmentLine{item: oi, quantity: remaining})
			}
		}
		if len(lines) == 0 {
			return nil, &ConflictError{Message: "order is fully shipped"}
		}
		return lines, nil
	}

	seen := make(map[int]bool, len(*items))
	var details []Detail
	lines := make([]shipmentLine, 0, len(*items))
	for i, in := range *items {
		ptr := fmt.Sprintf("/items/%d", i)
		oi, ok := byID[in.OrderItemID]
		switch {
		case !ok:
			// A value on the request body, exactly like a foreign sku_id at
			// checkout — 422, not 404. The order itself was found; this key was
			// wrong.
			details = append(details, Detail{Pointer: ptr + "/order_item_id", Message: "order item does not belong to this order"})
			continue
		case seen[in.OrderItemID]:
			// The UNIQUE(shipment_id, order_item_id) index would catch this too,
			// but a constraint error cannot say which line was the duplicate.
			details = append(details, Detail{Pointer: ptr + "/order_item_id", Message: "order item is named more than once"})
			continue
		case in.Quantity <= 0:
			details = append(details, Detail{Pointer: ptr + "/quantity", Message: "quantity must be > 0"})
			continue
		case in.Quantity > oi.Quantity-oi.ShippedQuantity:
			// A courtesy pre-check with a precise pointer. It is NOT the guard:
			// the conditional UPDATE below is, and it is what holds under
			// concurrency.
			details = append(details, Detail{Pointer: ptr + "/quantity", Message: "quantity exceeds the unshipped quantity of this order item"})
			continue
		}
		seen[in.OrderItemID] = true
		lines = append(lines, shipmentLine{item: oi, quantity: in.Quantity})
	}
	if len(details) > 0 {
		return nil, &ValidationError{Message: "shipment items are invalid", Details: details}
	}
	sort.Slice(lines, func(a, b int) bool { return lines[a].item.ID < lines[b].item.ID })
	return lines, nil
}

// shipmentLine is one resolved (order item, quantity) pair.
type shipmentLine struct {
	item     *ent.OrderItem
	quantity int32
}

// writeShipment performs the guarded quantity increments and creates the
// shipment plus its items, inside the caller's transaction.
func (s *Service) writeShipment(
	ctx context.Context, tx *ent.Tx, shopID, orderID int,
	carrier string, trackingNumber *string, lines []shipmentLine,
) (*ent.Shipment, error) {
	// The over-shipment guard (design D3), one conditional UPDATE per line with
	// an affected-rows check — the same idiom as Checkout's stock decrement, and
	// for the same reason: a SUM cannot live in an UPDATE predicate without
	// SELECT … FOR UPDATE or an application-level read-then-write, and neither is
	// allowed here. Zero rows affected means someone else took the remainder
	// between the pre-check above and now; the whole transaction rolls back.
	for i, ln := range lines {
		affected, err := tx.OrderItem.Update().
			Where(
				entorderitem.IDEQ(ln.item.ID),
				entorderitem.OrderIDEQ(orderID),
				entorderitem.ShopIDEQ(shopID),
				entorderitem.ShippedQuantityLTE(ln.item.Quantity-ln.quantity),
			).
			AddShippedQuantity(ln.quantity).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("shipping: increment shipped quantity: %w", err)
		}
		if affected == 0 {
			return nil, &ValidationError{
				Message: "shipment items are invalid",
				Details: []Detail{{
					Pointer: fmt.Sprintf("/items/%d/quantity", i),
					Message: "quantity exceeds the unshipped quantity of this order item",
				}},
			}
		}
	}

	create := tx.Shipment.Create().
		SetShopID(shopID).SetOrderID(orderID).
		SetCarrier(carrier).SetStatus(ShippedStatus).SetShippedAt(time.Now())
	if trackingNumber != nil {
		if tn := strings.TrimSpace(*trackingNumber); tn != "" {
			create.SetTrackingNumber(tn)
		}
	}
	sh, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("shipping: create shipment: %w", err)
	}

	for _, ln := range lines {
		if _, err := tx.ShipmentItem.Create().
			SetShopID(shopID).SetShipmentID(sh.ID).
			SetOrderItemID(ln.item.ID).SetQuantity(ln.quantity).
			Save(ctx); err != nil {
			return nil, fmt.Errorf("shipping: create shipment item: %w", err)
		}
	}
	return sh, nil
}

// syncFulfillment derives the order's fulfillment_status and writes it through
// order.Service's controlled transactional entry point.
//
// applied lets the caller account for increments made in this very transaction:
// the order items were read before they were updated, so their in-memory
// shipped_quantity is one step stale. Passing nil means "the rows are current"
// (the AdvanceShipment case, which changes no quantity).
func (s *Service) syncFulfillment(
	ctx context.Context, tx *ent.Tx, shopID, orderID int,
	statuses []int16, orderItems []*ent.OrderItem, applied []shipmentLine,
) error {
	var ordered, shipped int32
	for _, oi := range orderItems {
		ordered += oi.Quantity
		shipped += oi.ShippedQuantity
	}
	for _, ln := range applied {
		shipped += ln.quantity
	}
	target := deriveFulfillmentStatus(statuses, ordered, shipped)
	if _, err := s.Orders.UpdateFulfillmentStatusTx(ctx, tx, shopID, orderID, target); err != nil {
		return fmt.Errorf("shipping: advance fulfillment status: %w", err)
	}
	return nil
}

// loadShipment re-reads one shipment with its items eagerly loaded, which is
// what every caller serializes.
func (s *Service) loadShipment(ctx context.Context, shipmentID int) (*ent.Shipment, error) {
	sh, err := s.Client.Shipment.Query().
		Where(entshipment.IDEQ(shipmentID)).
		WithItems(func(q *ent.ShipmentItemQuery) { q.Order(entshipmentitem.ByOrderItemID()) }).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("shipping: reload shipment: %w", err)
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
//
// Change partial-shipment made two things different, and the second is the one
// that used to be wrong:
//
//   - It runs in a transaction that opens by locking the order row, so two
//     parcels of one order advancing at the same time cannot both derive the
//     order's status from a half-visible picture.
//   - **The order's fulfillment_status is derived from every shipment, not set
//     to this shipment's target.** Marking one of two parcels delivered used to
//     mark the whole order delivered.
func (s *Service) AdvanceShipment(ctx context.Context, shopID, orderID, shipmentID int, target int16) (*ent.Shipment, error) {
	if target != DeliveredStatus && target != ReturnedStatus {
		return nil, validationErr("/status", "status must be delivered or returned")
	}

	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("shipping: begin tx: %w", err)
	}
	ord, err := lockOrder(ctx, tx, shopID, orderID)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	sh, err := tx.Shipment.Query().
		Where(entshipment.IDEQ(shipmentID), entshipment.ShopIDEQ(shopID), entshipment.OrderIDEQ(orderID)).
		Only(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, ErrNotFound
	}

	upd := tx.Shipment.Update().
		Where(entshipment.IDEQ(shipmentID), entshipment.StatusEQ(ShippedStatus)).
		SetStatus(target)
	if target == DeliveredStatus {
		upd = upd.SetDeliveredAt(time.Now())
	}
	affected, err := upd.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("shipping: advance shipment: %w", err)
	}
	if affected == 0 {
		_ = tx.Rollback()
		return nil, &ConflictError{
			Message: fmt.Sprintf("shipment is %s, cannot transition to %s", statusName(sh.Status), statusName(target)),
		}
	}

	siblings, err := tx.Shipment.Query().
		Where(entshipment.ShopIDEQ(shopID), entshipment.OrderIDEQ(orderID)).
		All(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("shipping: query shipments: %w", err)
	}
	statuses := make([]int16, 0, len(siblings))
	for _, s := range siblings {
		// The CAS above updated this row inside the transaction, so the re-read
		// already reflects it; no local substitution is needed.
		statuses = append(statuses, s.Status)
	}
	orderItems, err := tx.OrderItem.Query().
		Where(entorderitem.OrderIDEQ(orderID), entorderitem.ShopIDEQ(shopID)).
		All(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("shipping: query order items: %w", err)
	}
	if err := s.syncFulfillment(ctx, tx, shopID, orderID, statuses, orderItems, nil); err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("shipping: commit shipment advance: %w", err)
	}

	// design D5: only the returned transition triggers a points clawback —
	// delivered is not published, there is nothing for points to react to.
	// Published after commit, never before, so a subscriber reading from another
	// connection sees the state the event describes.
	//
	// On an order with several parcels this fires once per returned parcel, and
	// the clawback is nonetheless applied once: point_transactions'
	// UNIQUE(order_id, kind) is the arbiter, and points.Service.HandleOrderReturned
	// already treats repeat delivery as a no-op. That correctness is inherited,
	// not added — but it is pinned by a test, because it is only true by accident
	// of a mechanism built for a different reason.
	if target == ReturnedStatus && s.Dispatcher != nil {
		s.Dispatcher.Publish(ctx, events.OrderReturned{
			ShopID: ord.ShopID, OrderID: ord.ID, MemberID: ord.MemberID,
		})
	}

	return s.loadShipment(ctx, shipmentID)
}

// listShipments is the shared body of every "all parcels of this order" read:
// newest first, items eagerly loaded. The ordering is explicit because a
// collection endpoint must never leave its order to the database (change
// commerce-demo-seed design D8), and it matches the back office so the same
// order reads identically on both sides.
func (s *Service) listShipments(ctx context.Context, shopID, orderID int) ([]*ent.Shipment, error) {
	rows, err := s.Client.Shipment.Query().
		Where(entshipment.ShopIDEQ(shopID), entshipment.OrderIDEQ(orderID)).
		WithItems(func(q *ent.ShipmentItemQuery) { q.Order(entshipmentitem.ByOrderItemID()) }).
		Order(entshipment.ByID(entsql.OrderDesc())).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("shipping: list shipments: %w", err)
	}
	return rows, nil
}

// ListShipmentsAdmin returns every shipment for orderID within shopID, newest
// first — since change partial-shipment genuinely many rows, not the 0-or-1 this
// used to promise. Verifies the order itself is visible in shopID first
// (order.Service.GetOrderAdmin) so a nonexistent/foreign order surfaces as
// ErrNotFound rather than silently returning an empty list.
func (s *Service) ListShipmentsAdmin(ctx context.Context, shopID, orderID int) ([]*ent.Shipment, error) {
	if _, err := s.Orders.GetOrderAdmin(ctx, shopID, orderID); err != nil {
		if errors.Is(err, order.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("shipping: query order: %w", err)
	}
	return s.listShipments(ctx, shopID, orderID)
}

// ListShipmentsForMember returns every shipment of the member's own order
// (change partial-shipment, design D6), newest first.
//
// An order with no shipments yet is an empty list, not ErrNotFound: this is a
// collection, and "nothing has shipped" is a perfectly good answer to it. That
// is the deliberate difference from GetShipmentForMember below, whose 404 means
// "there is no single shipment to return" and must stay a 404 for the callers
// that already depend on it.
func (s *Service) ListShipmentsForMember(ctx context.Context, shopID, memberID, orderID int) ([]*ent.Shipment, error) {
	if _, err := s.Orders.GetOrder(ctx, shopID, memberID, orderID); err != nil {
		if errors.Is(err, order.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("shipping: query order: %w", err)
	}
	return s.listShipments(ctx, shopID, orderID)
}

// GetShipmentForMember returns **the most recent** shipment for orderID, scoped
// by both shopID and memberID ownership of the order (member self-service —
// mirrors order.Service.GetOrder/payment.Service.InitiatePayment's ownership
// check). Returns ErrNotFound if the order doesn't exist/isn't the member's, or
// if the order exists but has nothing shipped yet.
//
// This used to be an `.Only()`, which returns an error the moment a second row
// exists — and the error handling here turns any error into ErrNotFound. So on
// an order with two parcels the customer would have been told their shipment
// does not exist. Taking the newest row keeps every single-shipment response
// byte-identical while making the multi-parcel case answer something true;
// callers that need all of them use ListShipmentsForMember.
func (s *Service) GetShipmentForMember(ctx context.Context, shopID, memberID, orderID int) (*ent.Shipment, error) {
	if _, err := s.Orders.GetOrder(ctx, shopID, memberID, orderID); err != nil {
		if errors.Is(err, order.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("shipping: query order: %w", err)
	}
	sh, err := s.Client.Shipment.Query().
		Where(entshipment.ShopIDEQ(shopID), entshipment.OrderIDEQ(orderID)).
		WithItems(func(q *ent.ShipmentItemQuery) { q.Order(entshipmentitem.ByOrderItemID()) }).
		Order(entshipment.ByID(entsql.OrderDesc())).
		First(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	return sh, nil
}
