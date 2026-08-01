// Package order implements the order-management domain (change
// order-management): converting a member's active cart into an immutable
// order via checkout, member/merchant order queries, cancellation with stock
// restoration, and the controlled entry points payment-integration/
// shipping-logistics use to advance payment_status/fulfillment_status
// (design D6).
//
// Unlike cart and catalog, this package deliberately imports
// api/internal/cart — not for superficial code reuse, but because checkout
// is fundamentally "read a cart, consume it" (shopping-cart design D9
// explicitly expects order-management to query cart's ent rows directly);
// the cart.Status* constants are imported to avoid duplicating magic numbers
// that already have a canonical owner.
package order

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"ksdevworks/ecommerce/api/internal/cart"
	"ksdevworks/ecommerce/api/internal/ent"
	entcart "ksdevworks/ecommerce/api/internal/ent/cart"
	entidem "ksdevworks/ecommerce/api/internal/ent/checkoutidempotencykey"
	entmember "ksdevworks/ecommerce/api/internal/ent/member"
	entorder "ksdevworks/ecommerce/api/internal/ent/order"
	entorderitem "ksdevworks/ecommerce/api/internal/ent/orderitem"
	"ksdevworks/ecommerce/api/internal/ent/predicate"
	entproductsku "ksdevworks/ecommerce/api/internal/ent/productsku"
	entshippingmethod "ksdevworks/ecommerce/api/internal/ent/shippingmethod"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/idempotency"
)

// ErrNotFound marks missing resources (handler → 404). Also returned when an
// orderID exists but belongs to a different member (member-scoped calls) —
// callers must not distinguish "doesn't exist" from "not yours" (mirrors
// cart.Service's ownedItem convention: avoid leaking cross-member
// existence).
var ErrNotFound = errors.New("order: not found")

// Detail locates one validation problem (JSON Pointer-ish; same convention
// as cart.Detail/catalog.Detail).
type Detail struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

// ValidationError carries 422 payloads (design D12 error envelope, reused
// from cart/catalog).
type ValidationError struct {
	Message string
	Details []Detail
}

func (e *ValidationError) Error() string { return e.Message }

func validationErr(pointer, msg string) *ValidationError {
	return &ValidationError{Message: msg, Details: []Detail{{Pointer: pointer, Message: msg}}}
}

// ConflictError carries 409 payloads (design D9): a double-submitted
// checkout racing itself (design D3 step 4), or a cancel request against an
// order that has moved off the cancellable initial state (design D7).
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// Order status enumeration (design D5) — this axis is fully owned by this
// change; no other proposal is expected to add values here.
const (
	StatusCreated   int16 = 0
	StatusCancelled int16 = 1
)

// PaymentStatusUnpaid is the only payment_status value this change ever
// creates or reads. Values >= 1 belong to payment-integration's own
// enumeration (design D5/D6/D11) — do not add more constants here on its
// behalf; UpdatePaymentStatus is the controlled entry point it must use.
const PaymentStatusUnpaid int16 = 0

// FulfillmentStatusUnfulfilled is the only fulfillment_status value this
// change ever creates or reads. Values >= 1 belong to shipping-logistics's
// own enumeration (design D5/D6/D11); UpdateFulfillmentStatus is the
// controlled entry point it must use.
const FulfillmentStatusUnfulfilled int16 = 0

// ShippingAddress is the structured shape stored in orders.shipping_address
// (design D8). The column itself is an opaque jsonb blob at the ent/DB layer
// (same convention as Product.meta/ProductSKU.options) — this struct is the
// only place the shape is defined; shipping-logistics may add further
// *optional* fields to it later without invalidating existing rows.
//
// Every key except line2 is always serialized, including the optional ones, so
// the stored shape and the API response shape stay stable whether or not the
// merchant's form collects them (change guest-checkout, design D8).
type ShippingAddress struct {
	RecipientName string `json:"recipient_name"`
	Phone         string `json:"phone"`
	Line1         string `json:"line1"`
	Line2         string `json:"line2,omitempty"`
	City          string `json:"city"`
	PostalCode    string `json:"postal_code"`
	Country       string `json:"country"`
}

// Validate checks the required subset of ShippingAddress fields.
//
// Required: recipient_name, phone, line1 — the minimum that is meaningful
// everywhere. city/postal_code/country are optional (change guest-checkout,
// design D8): no code in this API reads them, address structure differs by
// locale (a Taiwanese single-line address already contains the city), and
// demanding country:"TW" from a single-country florist produces ceremony, not
// validation. Relaxing is safe for v1 — every request that used to be accepted
// still is, byte for byte.
//
// When a later capability genuinely needs a structured city or postal code
// (shipping zones, say), it defines when it is required — rather than the
// platform teaching every merchant to fill these with placeholder values,
// which would leave them worthless precisely when they start to matter.
func (a ShippingAddress) Validate() error {
	var details []Detail
	require := func(v, field string) {
		if v == "" {
			details = append(details, Detail{Pointer: "/shipping_address/" + field, Message: field + " is required"})
		}
	}
	require(a.RecipientName, "recipient_name")
	require(a.Phone, "phone")
	require(a.Line1, "line1")
	if len(details) > 0 {
		return &ValidationError{Message: "shipping address is incomplete", Details: details}
	}
	return nil
}

// deliveryDateLayout is the only accepted wire format for a requested delivery
// date (change order-extras, design D3). time.Parse with this layout also
// rejects dates that do not exist (2026-02-31), not just malformed strings.
const deliveryDateLayout = "2006-01-02"

// CheckoutInput is everything a checkout request carries beyond the cart
// itself (change order-extras, design D11). Both entry points — member
// checkout and guest checkout — build this same value, so "what is a valid
// delivery date" and "what is a selectable shipping method" have exactly one
// definition and produce byte-identical 422s on both paths.
//
// RequestedDeliveryDate is the raw wire string rather than a parsed time on
// purpose: parsing it here keeps every date rule inside this package, so no
// HTTP handler has to know any of them.
type CheckoutInput struct {
	ShippingAddress       ShippingAddress
	ShippingMethodID      *int
	RequestedDeliveryDate *string

	// IdempotencyReservationID names a checkout_idempotency_keys row this
	// request already holds (change checkout-idempotency, design D3). nil — the
	// case for every caller that predates that change, and for every request
	// without an Idempotency-Key header — means the checkout behaves exactly as
	// it always did.
	//
	// It is finalized *inside* this method's transaction, immediately before
	// commit, so that "the order exists" and "the key is completed and points at
	// it" are one atomic fact. The obvious alternative — commit the order, then
	// mark the key in a second statement — leaves a window where a crash strands
	// an order behind an unfinished key, and after the key expires a retry
	// creates a second order: precisely the failure the mechanism exists to
	// prevent.
	IdempotencyReservationID *int
}

// parseRequestedDeliveryDate validates and normalizes the requested delivery
// date (change order-extras, design D3). nil/empty means "not requested".
//
// Exactly two rules, deliberately:
//
//   - strict YYYY-MM-DD, which also rejects impossible dates;
//   - not a day that has already passed in *every* timezone on earth.
//
// The second rule's one-day slack is not sloppiness. The system stores no
// per-shop timezone, so the server cannot know which calendar day "today" is
// for this merchant: at 07:00 in Taipei it is still yesterday in UTC, and at
// 18:00 in Los Angeles it is already tomorrow. Bounding at UTC-yesterday
// catches the thing worth catching (a mistyped year) without ever rejecting a
// customer ordering legitimately for today in their own timezone. Merchant
// lead times, cut-off hours and blackout dates are explicitly out of scope —
// they need a timezone and a holiday model, not one more field.
func parseRequestedDeliveryDate(raw *string) (*time.Time, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	d, err := time.Parse(deliveryDateLayout, *raw)
	if err != nil {
		return nil, validationErr("/requested_delivery_date", "requested_delivery_date must be a valid date in YYYY-MM-DD format")
	}
	now := time.Now().UTC()
	earliest := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	if d.Before(earliest) {
		return nil, validationErr("/requested_delivery_date", "requested_delivery_date must not be in the past")
	}
	return &d, nil
}

// Service implements order operations, all scoped by explicit shopID/
// memberID parameters (never ambient tenant/member context — same
// convention as cart.Service/catalog.Service: keeps the package testable
// without faking context values, and the caller — the HTTP handler — is the
// single place responsible for extracting identity from the request).
//
// Dispatcher is optional (nil-safe) and publishes events.OrderPlaced after
// Checkout's transaction commits (change order-notification-email, design
// D15/D1). Like payment.Service and shipping.Service before it, this package
// still imports nothing from whoever subscribes — it publishes a generic domain
// event and knows nothing about who (if anyone) reacts to it. A nil Dispatcher
// publishes nothing, which is exactly what every test that constructs
// order.Service{Client: …} did before this field existed.
type Service struct {
	Client     *ent.Client
	Dispatcher *events.Dispatcher
}

// resolveShippingMethod validates the selected shipping method and returns the
// values to snapshot onto the order (change order-extras, design D4). nil
// selection is the ordinary case and yields a zero fee, i.e. exactly the
// behavior every request had before this change.
//
// A missing, foreign or deactivated method is a 422 on a request-body value —
// the same treatment /items/{i}/sku_id already gets for a foreign SKU — not a
// 404, and never a silent "charge nothing": the merchant retiring an option
// means they no longer want to sell it at that price.
//
// This runs *outside* the checkout transaction, alongside the existing
// purchasability validation. A merchant re-pricing a method in that window is a
// benign race: the customer is charged the price they were shown.
func (s *Service) resolveShippingMethod(ctx context.Context, shopID int, methodID *int) (*ent.ShippingMethod, error) {
	if methodID == nil {
		return nil, nil
	}
	sm, err := s.Client.ShippingMethod.Query().
		Where(entshippingmethod.IDEQ(*methodID), entshippingmethod.ShopIDEQ(shopID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, validationErr("/shipping_method_id", "shipping method does not exist in this shop")
	}
	if err != nil {
		return nil, fmt.Errorf("order: query shipping method: %w", err)
	}
	if !sm.IsActive {
		return nil, validationErr("/shipping_method_id", "shipping method is not active")
	}
	return sm, nil
}

// tierDiscount is the member tier snapshot taken at checkout (change
// tier-discount-application, design D3/D8). The zero value means "held no tier",
// which is the ordinary case for a first-time buyer and produces byte-identical
// behavior to every checkout that predates this change.
type tierDiscount struct {
	TierName *string
	Percent  *int16
}

// amount computes the money actually taken off the item subtotal.
//
// Integer arithmetic, truncating (design D4). Three properties are load-bearing
// and each is easy to destroy by "tidying up":
//
//   - **Truncation, never rounding.** floor is what points already use
//     (floor(basis/100)); a second rounding direction would mean every future
//     reader of a money path has to look up which one applies here. Truncation is
//     also what Go's integer division does natively, so there is no `+ 50` for
//     someone to delete later — and a deleted `+ 50` shows up only as an
//     off-by-one on non-divisible subtotals, which nothing would catch.
//   - **Multiply before dividing.** subtotal/100*percent drops everything below
//     100 first: 3333 at 10% would yield 330 instead of 333.
//   - **The customer never gets more than the merchant advertised.** With
//     truncation the effective rate is always ≤ discount_percent, the difference
//     strictly less than one minor unit.
//
// Overflow bound, stated rather than guarded: subtotal × 100 exceeds int64 only
// past ~9.2e16, and the subtotal accumulator itself would have overflowed long
// before that. That limit predates this change.
func (d tierDiscount) amount(subtotal int64) int64 {
	if d.Percent == nil || *d.Percent <= 0 || subtotal <= 0 {
		return 0
	}
	return subtotal * int64(*d.Percent) / 100
}

// resolveTierDiscount reads the customer's current tier and the rate to apply
// (change tier-discount-application, design D8).
//
// It reads members.level_id rather than re-deriving the tier from the points
// balance: level_id is maintained by points.Service inside the same transaction
// as every ledger write (member-tiers-and-points design D6/D7), so it *is* the
// answer to "which tier is this customer in". Recomputing here would add a second
// judge of the same question, and two judges eventually disagree.
//
// A tier whose own discount_percent is NULL snapshots as 0, so the order can
// still tell "belonged to a tier that grants nothing" from "belonged to no tier"
// (design D3).
//
// Runs *outside* the checkout transaction, alongside resolveShippingMethod. A
// merchant re-pricing a tier in that window is the same benign race the shipping
// fee already has: the customer gets the rate in force when they pressed submit.
//
// shop_id is carried explicitly rather than left to the tenant interceptor —
// guest checkout runs inside tenant context and member checkout does too, but
// correctness here must not depend on ambient state (member-identity-per-shop
// design D4).
func (s *Service) resolveTierDiscount(ctx context.Context, shopID, memberID int) (tierDiscount, error) {
	m, err := s.Client.Member.Query().
		Where(entmember.IDEQ(memberID), entmember.ShopIDEQ(shopID)).
		WithMemberTier().
		Only(ctx)
	if err != nil {
		return tierDiscount{}, fmt.Errorf("order: query member tier: %w", err)
	}
	tier := m.Edges.MemberTier
	if tier == nil {
		return tierDiscount{}, nil
	}
	percent := int16(0)
	if tier.DiscountPercent != nil {
		percent = *tier.DiscountPercent
	}
	name := tier.Name
	return tierDiscount{TierName: &name, Percent: &percent}, nil
}

// ── checkout ────────────────────────────────────────────────────────────

// Checkout converts the member's active cart into an order (design D3), all
// within a single ent.Tx. It is the *only* implementation of order creation
// and stock deduction in this API: every entry point that places an order —
// member self-service checkout, guest checkout (change guest-checkout, design
// D1), and anything added later — MUST call this method rather than reproduce
// its logic. New entry points differ only in how they obtain the customer
// identity and the cart contents.
//
// Steps:
//  1. load the active cart with items/sku/product eagerly fetched
//  2. reject an empty or missing cart
//  3. validate every item's purchasability (sku exists, active, product
//     published) — stock is deliberately NOT checked here; that check is
//     folded into the atomic decrement in step 5 so there is exactly one
//     authoritative stock check, not two that could disagree
//  4. atomically flip the cart Active -> Converted; 0 rows affected means a
//     concurrent checkout already claimed this cart (ConflictError)
//  5. atomically decrement stock per line (design D2); 0 rows affected on
//     any line rolls back the entire transaction, including step 4's cart
//     flip
//  6. create orders + order_items with denormalized snapshots
//
// Any failure at any step rolls back the whole transaction — no partial
// stock deduction, no orphaned order, no converted cart left behind.
//
// Change order-extras added the delivery date and shipping method to this
// method rather than to its callers, and did so strictly *around* the
// transaction: both are parsed and validated before it opens (a bad shipping
// method must not consume the cart or touch stock), and the resulting fee only
// enters the arithmetic that composes the order row. Change
// tier-discount-application added the member tier discount the same way — the
// tier is resolved before the transaction opens, and the one multiplication
// happens after the stock loop, where the subtotal first exists. The conditional
// stock decrement and its affected-rows check below are unchanged, character for
// character — they remain the single implementation every order-placing entry
// point shares, and that includes the discount: no entry point may resolve a rate
// of its own, and no request body may name one.
func (s *Service) Checkout(ctx context.Context, shopID, memberID int, in CheckoutInput) (*ent.Order, error) {
	if err := in.ShippingAddress.Validate(); err != nil {
		return nil, err
	}
	deliveryDate, err := parseRequestedDeliveryDate(in.RequestedDeliveryDate)
	if err != nil {
		return nil, err
	}
	shippingMethod, err := s.resolveShippingMethod(ctx, shopID, in.ShippingMethodID)
	if err != nil {
		return nil, err
	}
	var shippingFee int64
	if shippingMethod != nil {
		shippingFee = shippingMethod.FlatRate
	}
	discount, err := s.resolveTierDiscount(ctx, shopID, memberID)
	if err != nil {
		return nil, err
	}

	c, err := s.Client.Cart.Query().
		Where(entcart.ShopIDEQ(shopID), entcart.MemberIDEQ(memberID), entcart.StatusEQ(cart.StatusActive)).
		WithItems(func(q *ent.CartItemQuery) {
			q.WithSku(func(sq *ent.ProductSKUQuery) { sq.WithProduct() })
		}).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, validationErr("/cart", "cart is empty")
	}
	if err != nil {
		return nil, fmt.Errorf("order: query active cart: %w", err)
	}
	if len(c.Edges.Items) == 0 {
		return nil, validationErr("/cart", "cart is empty")
	}

	if err := validatePurchasable(c.Edges.Items); err != nil {
		return nil, err
	}

	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("order: begin tx: %w", err)
	}

	// Step 4 (design D3): atomic guard against double-submitted checkout.
	converted, err := tx.Cart.Update().
		Where(entcart.IDEQ(c.ID), entcart.StatusEQ(cart.StatusActive)).
		SetStatus(cart.StatusConverted).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("order: convert cart: %w", err)
	}
	if converted == 0 {
		_ = tx.Rollback()
		return nil, &ConflictError{Message: "cart has already been checked out"}
	}

	// Step 5 (design D2): atomic conditional decrement per line. The first
	// insufficient-stock line rolls back everything, including the cart
	// flip above.
	var subtotal int64
	for i, it := range c.Edges.Items {
		affected, err := tx.ProductSKU.Update().
			Where(entproductsku.IDEQ(*it.SkuID), entproductsku.ShopIDEQ(shopID), entproductsku.StockQtyGTE(it.Quantity)).
			AddStockQty(-it.Quantity).
			Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("order: decrement stock: %w", err)
		}
		if affected == 0 {
			_ = tx.Rollback()
			return nil, &ValidationError{
				Message: "insufficient stock",
				Details: []Detail{{Pointer: fmt.Sprintf("/items/%d", i), Message: "insufficient stock"}},
			}
		}
		subtotal += it.PriceAmount * int64(it.Quantity)
	}

	addrJSON, err := json.Marshal(in.ShippingAddress)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("order: marshal shipping address: %w", err)
	}

	// change order-extras design D6/D7, amended by change
	// tier-discount-application design D5: total_amount is the payable total, now
	// net of the member tier discount. The DB's
	// orders_total_amount_composition_check enforces this same equation, so no
	// other path can create an order where it does not hold.
	//
	// The multiplication lives here — after the stock loop that produced the
	// subtotal, before the row is written — so the loop above stays untouched.
	discountAmount := discount.amount(subtotal)
	create := tx.Order.Create().
		SetShopID(shopID).SetMemberID(memberID).
		SetStatus(StatusCreated).
		SetPaymentStatus(PaymentStatusUnpaid).
		SetFulfillmentStatus(FulfillmentStatusUnfulfilled).
		SetCurrency(c.Currency).
		SetSubtotalAmount(subtotal).
		SetDiscountAmount(discountAmount).
		SetShippingFee(shippingFee).
		SetTotalAmount(subtotal - discountAmount + shippingFee).
		SetShippingAddress(addrJSON)
	if discount.Percent != nil {
		// Both or neither — orders_tier_snapshot_check makes that a database fact.
		create.SetDiscountPercent(*discount.Percent).SetMemberTierName(*discount.TierName)
	}
	if shippingMethod != nil {
		create.SetShippingMethodID(shippingMethod.ID).SetShippingMethodName(shippingMethod.Name)
	}
	if deliveryDate != nil {
		create.SetRequestedDeliveryDate(*deliveryDate)
	}
	o, err := create.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("order: create order: %w", err)
	}

	// Step 6: denormalized snapshots (design D1/D4) — product/sku are
	// guaranteed non-nil here by validatePurchasable above.
	for _, it := range c.Edges.Items {
		sku := it.Edges.Sku
		product := sku.Edges.Product
		if _, err := tx.OrderItem.Create().
			SetShopID(shopID).SetOrderID(o.ID).
			SetProductID(product.ID).SetProductTitle(product.Title).
			SetSkuID(*it.SkuID).SetSkuCode(sku.SkuCode).
			SetQuantity(it.Quantity).SetPriceAmount(it.PriceAmount).SetCurrency(it.Currency).
			SetGiftMessage(it.GiftMessage).
			Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("order: create order item: %w", err)
		}
	}

	// change checkout-idempotency, design D3: bind the reservation to this order
	// inside the same transaction. The conditional predicate + affected-rows
	// check is the same idiom used above for the cart flip and the stock
	// decrement; 0 rows means the reservation was released or completed by
	// someone else, which must abort the whole checkout rather than produce an
	// order no key points at.
	//
	// This runs *after* the stock loop, on a row this request already owns
	// exclusively via the unique index — no other transaction contends for it, so
	// it adds no lock wait and no new lock-ordering edge. The decrement loop
	// above is unchanged, character for character.
	if in.IdempotencyReservationID != nil {
		affected, err := tx.CheckoutIdempotencyKey.Update().
			Where(
				entidem.IDEQ(*in.IdempotencyReservationID),
				entidem.ShopIDEQ(shopID),
				entidem.StatusEQ(idempotency.StatusInProgress),
			).
			SetStatus(idempotency.StatusCompleted).
			SetOrderID(o.ID).
			Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("order: finalize idempotency reservation: %w", err)
		}
		if affected != 1 {
			_ = tx.Rollback()
			return nil, &ConflictError{Message: "checkout idempotency reservation is no longer held"}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("order: commit checkout: %w", err)
	}

	// change order-notification-email, design D15: the one place an
	// "order placed" domain event is published, and it is here rather than in
	// the two HTTP handlers on purpose.
	//
	// This method is the only implementation of order creation, so "exactly one
	// event per order" is a property of the code's shape, not of what each entry
	// point remembers to do. In particular the Idempotency-Key replay path never
	// reaches this line — it loads the existing order and re-serializes it — so a
	// retried checkout cannot produce a second confirmation email. Publishing
	// from the handlers would have made that a matter of two `if` statements
	// staying correct forever.
	//
	// After Commit, never before: a subscriber that reads the order from another
	// connection must be able to find it (the dispatcher runs handlers
	// synchronously on this goroutine, but the notification subscriber hands the
	// work to its own worker, which uses its own connection).
	if s.Dispatcher != nil {
		s.Dispatcher.Publish(ctx, events.OrderPlaced{ShopID: shopID, OrderID: o.ID, MemberID: memberID})
	}

	return s.GetOrderAdmin(ctx, shopID, o.ID)
}

// validatePurchasable checks step 3 of Checkout (design D3): every item must
// have a live, active SKU whose product is published. Stock is deliberately
// excluded — see Checkout's doc comment. Returns a single ValidationError
// listing every violating line (by index) so the caller sees the whole
// picture in one response, not just the first failure.
func validatePurchasable(items []*ent.CartItem) error {
	var details []Detail
	for i, it := range items {
		sku := it.Edges.Sku
		switch {
		case it.SkuID == nil || sku == nil:
			details = append(details, Detail{Pointer: fmt.Sprintf("/items/%d", i), Message: "sku no longer exists"})
		case !sku.IsActive:
			details = append(details, Detail{Pointer: fmt.Sprintf("/items/%d", i), Message: "sku is not active"})
		case sku.Edges.Product == nil || sku.Edges.Product.Status != 1:
			details = append(details, Detail{Pointer: fmt.Sprintf("/items/%d", i), Message: "product is not published"})
		}
	}
	if len(details) > 0 {
		return &ValidationError{Message: "cart has unpurchasable items", Details: details}
	}
	return nil
}

// ── member self-service reads ──────────────────────────────────────────

// GetOrder returns one order owned by memberID within shopID, items loaded.
// The member_id predicate is applied directly in the query (unlike cart's
// two-step ownedItem — orders carry member_id as a direct column), so a
// orderItemsByID orders an order's lines by id — the sequence they were created
// in at checkout, which is the sequence every consumer displays.
//
// **This was load-bearing from the day it was missing, and change
// partial-shipment is what made it visible.** Until then no path ever UPDATEd an
// order_items row, so the physical heap order matched insertion order and an
// unordered eager-load happened to come back sorted. shipped_quantity is the
// first mutable column on the table (design D3), and Postgres writes an updated
// tuple as a new version at the end of the heap — so a seq scan returns the line
// that just shipped *last*. Concretely: a three-line order whose second line
// ships would render 1,3,2 to the customer, and 2,3,1 after the first line ships
// too. Nothing would error; the lines would simply shuffle every time a parcel
// went out.
//
// So this is not defensive tidying. It is the rule the project already states —
// a collection endpoint must never leave its order to the database — arriving at
// the moment the database finally had an opinion.
func orderItemsByID(q *ent.OrderItemQuery) { q.Order(entorderitem.ByID()) }

// foreign order and a nonexistent order both surface as the same
// ErrNotFound (no existence leakage).
func (s *Service) GetOrder(ctx context.Context, shopID, memberID, orderID int) (*ent.Order, error) {
	o, err := s.Client.Order.Query().
		Where(entorder.IDEQ(orderID), entorder.ShopIDEQ(shopID), entorder.MemberIDEQ(memberID)).
		WithItems(orderItemsByID).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	return o, nil
}

// ListParams are pagination inputs for a member's own order list.
type ListParams struct {
	Page     int
	PageSize int
}

// AdminListParams are pagination + filter inputs for the merchant back
// office order list.
//
// Every field below Status was added by change admin-commerce-operations
// (design D3) and every one of them is optional: with all of them at their
// zero value ListOrdersAdmin runs the exact query it ran before they existed.
// They are not conveniences — the list is paginated, so a filter applied in the
// browser can only ever see the current page and would answer "no orders ship
// today" while one sits on page 3. That is a bouquet that never left the shop.
type AdminListParams struct {
	Page     int
	PageSize int
	Status   *int16
	// PaymentStatus/FulfillmentStatus are matched exactly. Deliberately not
	// range-validated here: the DB CHECK on both columns is only ">= 0" and the
	// enumerations belong to payment/shipping respectively (order-management
	// design D5). An unrecognized value simply matches nothing, which is the
	// honest answer.
	PaymentStatus     *int16
	FulfillmentStatus *int16
	// DeliveryFrom/DeliveryTo bound requested_delivery_date, a Postgres `date`
	// — a calendar day with no instant and no offset, so this filter means the
	// same thing to every caller in every timezone (design D4). Orders with no
	// requested delivery date are excluded whenever either bound is set: NULL
	// compares false, and "what ships on the 5th" genuinely does not include
	// "whenever".
	DeliveryFrom *time.Time
	DeliveryTo   *time.Time
	// MemberID matches orders.member_id exactly (change member-directory,
	// design D2). It exists so "what has this customer bought" is answered by
	// THIS list — with its existing pagination, its existing filters and its
	// existing serialization — rather than by growing a second copy of the order
	// payload inside the customer endpoint. The order shape has changed three
	// times in three changes (order-extras' three amounts, checkout-idempotency,
	// tier-discount-application's discount snapshot); two serializations would
	// have drifted by now, and the way that drift shows up is "the amount on the
	// customer page differs from the amount on the order page".
	MemberID *int
	// Q matches the recipient's name or phone (case-insensitive substring), and
	// additionally the order id when Q parses as a positive integer. It does NOT
	// match address lines: a merchant is looking for a person, and matching
	// line1 would turn "台北" into half the city's orders.
	Q string
}

// OrderPage is a page of orders plus pagination metadata (mirrors
// catalog.ProductPage).
type OrderPage struct {
	Orders   []*ent.Order
	Total    int
	Page     int
	PageSize int
}

func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	switch {
	case pageSize <= 0:
		pageSize = 20
	case pageSize > 100:
		pageSize = 100
	}
	return page, pageSize
}

// ListOrders lists memberID's own orders within shopID, newest first.
func (s *Service) ListOrders(ctx context.Context, shopID, memberID int, params ListParams) (*OrderPage, error) {
	page, pageSize := normalizePage(params.Page, params.PageSize)
	q := s.Client.Order.Query().Where(entorder.ShopIDEQ(shopID), entorder.MemberIDEQ(memberID))
	return paginate(ctx, q, page, pageSize)
}

// ── merchant back-office reads ─────────────────────────────────────────

// GetOrderAdmin returns one order within shopID (no member ownership check
// — the caller's shop access was already enforced by the RBAC middleware).
func (s *Service) GetOrderAdmin(ctx context.Context, shopID, orderID int) (*ent.Order, error) {
	o, err := s.Client.Order.Query().
		Where(entorder.IDEQ(orderID), entorder.ShopIDEQ(shopID)).
		WithItems(orderItemsByID).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	return o, nil
}

// ListOrdersAdmin lists every order within shopID, newest first, optionally
// filtered by order status and — since change admin-commerce-operations
// (design D3) — by payment/fulfillment status, requested delivery date range
// and a recipient/order-id keyword.
//
// Every filter is conditional: with an all-zero AdminListParams beyond
// Page/PageSize this builds byte for byte the query it built before those
// fields existed.
func (s *Service) ListOrdersAdmin(ctx context.Context, shopID int, params AdminListParams) (*OrderPage, error) {
	page, pageSize := normalizePage(params.Page, params.PageSize)
	q := s.Client.Order.Query().Where(entorder.ShopIDEQ(shopID))
	if params.Status != nil {
		q = q.Where(entorder.StatusEQ(*params.Status))
	}
	if params.PaymentStatus != nil {
		q = q.Where(entorder.PaymentStatusEQ(*params.PaymentStatus))
	}
	if params.FulfillmentStatus != nil {
		q = q.Where(entorder.FulfillmentStatusEQ(*params.FulfillmentStatus))
	}
	if params.DeliveryFrom != nil {
		q = q.Where(entorder.RequestedDeliveryDateGTE(*params.DeliveryFrom))
	}
	if params.DeliveryTo != nil {
		q = q.Where(entorder.RequestedDeliveryDateLTE(*params.DeliveryTo))
	}
	// The shop predicate above still applies, so a member id belonging to
	// another shop simply matches nothing — it cannot reach across the tenant
	// boundary even if a caller guesses one.
	if params.MemberID != nil {
		q = q.Where(entorder.MemberIDEQ(*params.MemberID))
	}
	if kw := strings.TrimSpace(params.Q); kw != "" {
		q = q.Where(recipientOrIDMatches(kw))
	}
	return paginate(ctx, q, page, pageSize)
}

// addressFieldMatches matches one string key inside the shipping_address jsonb
// against a case-insensitive LIKE pattern.
//
// ent has no typed predicate for jsonb key extraction, so this drops to a raw
// selector — but the value goes through Builder.Arg, which emits the dialect's
// own placeholder ($1, $2, …) and binds the argument. It is never concatenated
// into the SQL text, so a keyword containing a quote stays a keyword.
func addressFieldMatches(key, pattern string) predicate.Order {
	return func(s *entsql.Selector) {
		s.Where(entsql.P(func(b *entsql.Builder) {
			b.WriteString(s.C(entorder.FieldShippingAddress)).
				WriteString("->>'" + key + "' ILIKE ").
				Arg(pattern)
		}))
	}
}

// recipientOrIDMatches builds the keyword predicate for the back-office order
// list (design D3).
//
// Known limit (same as the product search of change admin-commerce-catalog,
// design D6): `ILIKE '%…%'` cannot use an index. At a single shop's order
// volume that is fine; when it stops being fine the path is a pg_trgm GIN index
// or full text search, and the API contract does not change.
func recipientOrIDMatches(kw string) predicate.Order {
	pattern := "%" + kw + "%"
	// The key names are literals owned by order.ShippingAddress's json tags,
	// never caller input — nothing from the request reaches the SQL text.
	preds := []predicate.Order{
		addressFieldMatches("recipient_name", pattern),
		addressFieldMatches("phone", pattern),
	}
	// "他說訂單編號是 1234" is the other half of the same question, and the
	// merchant should not have to know which box to type it into.
	if id, err := strconv.Atoi(kw); err == nil && id > 0 {
		preds = append(preds, entorder.IDEQ(id))
	}
	return entorder.Or(preds...)
}

func paginate(ctx context.Context, q *ent.OrderQuery, page, pageSize int) (*OrderPage, error) {
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("order: count orders: %w", err)
	}
	orders, err := q.
		WithItems(orderItemsByID).
		Order(entorder.ByID(entsql.OrderDesc())).
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("order: list orders: %w", err)
	}
	return &OrderPage{Orders: orders, Total: total, Page: page, PageSize: pageSize}, nil
}

// ── cancellation ────────────────────────────────────────────────────────

// CancelOrder cancels an order and restores stock (design D7), within a
// single ent.Tx. memberID == nil is the admin path (shop-scope only);
// memberID != nil additionally enforces member ownership (member path) —
// both share this one implementation.
//
// Cancellable only when all three status axes are still at their initial
// value (status=Created, payment_status=Unpaid, fulfillment_status=
// Unfulfilled); any other combination means payment-integration or
// shipping-logistics has already advanced this order, and only they know
// whether unwinding it is safe — so this generic cancel path refuses
// (ConflictError, 409) rather than guessing.
//
// Change partial-shipment did not touch a character of this method, and that is
// worth stating because it easily could have gone the other way. A partially
// shipped order is fulfillment_status=4 (FulfillmentStatusPartiallyShipped), so
// the CAS below already matches zero rows and answers 409 — which is the right
// answer, because the restock loop further down returns each item's *ordered*
// quantity, not the unshipped remainder. Had partial shipment been modelled as
// "stay at 0 until everything ships", this same code would have returned 200 and
// resurrected inventory that had already left the shop.
func (s *Service) CancelOrder(ctx context.Context, shopID, orderID int, memberID *int) (*ent.Order, error) {
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("order: begin tx: %w", err)
	}

	preds := []predicate.Order{entorder.IDEQ(orderID), entorder.ShopIDEQ(shopID)}
	if memberID != nil {
		preds = append(preds, entorder.MemberIDEQ(*memberID))
	}

	// Existence/ownership check first (scoped query — no leakage), so we can
	// tell "doesn't exist / not yours" (404) apart from "exists but not
	// cancellable right now" (409).
	existing, err := tx.Order.Query().Where(preds...).Only(ctx)
	if ent.IsNotFound(err) {
		_ = tx.Rollback()
		return nil, ErrNotFound
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("order: query order: %w", err)
	}

	// Atomic conditional flip: doubles as both the cancellability check and
	// the state transition, closing the race window against a concurrent
	// payment/fulfillment update (design D7).
	affected, err := tx.Order.Update().
		Where(
			entorder.IDEQ(existing.ID),
			entorder.StatusEQ(StatusCreated),
			entorder.PaymentStatusEQ(PaymentStatusUnpaid),
			entorder.FulfillmentStatusEQ(FulfillmentStatusUnfulfilled),
		).
		SetStatus(StatusCancelled).
		SetCancelledAt(time.Now()).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("order: cancel order: %w", err)
	}
	if affected == 0 {
		_ = tx.Rollback()
		return nil, &ConflictError{Message: "order can no longer be cancelled"}
	}

	items, err := tx.OrderItem.Query().Where(entorderitem.OrderIDEQ(existing.ID)).All(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("order: query order items: %w", err)
	}
	for _, it := range items {
		if it.SkuID == nil {
			// design D4/D7: the SKU no longer exists; nothing to restock.
			continue
		}
		if _, err := tx.ProductSKU.Update().
			Where(entproductsku.IDEQ(*it.SkuID)).
			AddStockQty(it.Quantity).
			Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("order: restore stock: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("order: commit cancel: %w", err)
	}

	return s.GetOrderAdmin(ctx, shopID, existing.ID)
}

// ── controlled status updates for payment-integration / shipping-logistics
// (design D6) ──────────────────────────────────────────────────────────

// UpdatePaymentStatus sets orders.payment_status. Callers outside this
// package (payment-integration) MUST use this method rather than writing
// the ent client directly — it enforces tenant scoping and existence, and is
// the single place order-management can add invariants later without every
// caller having to change. status must be >= 0; order-management does not
// validate the value against a payment state machine — that machine belongs
// to payment-integration.
func (s *Service) UpdatePaymentStatus(ctx context.Context, shopID, orderID int, status int16) (*ent.Order, error) {
	if status < 0 {
		return nil, validationErr("/payment_status", "payment_status must be >= 0")
	}
	o, err := s.Client.Order.Query().Where(entorder.IDEQ(orderID), entorder.ShopIDEQ(shopID)).Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	updated, err := o.Update().SetPaymentStatus(status).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("order: update payment status: %w", err)
	}
	return updated, nil
}

// FulfillmentStatusPartiallyShipped is quoted here, not owned here. The
// fulfillment_status enumeration belongs to shipping-logistics (design D5/D6)
// and this package still never interprets a value beyond "unfulfilled"; the
// constant exists only so that CancelOrder's doc comment and this package's
// tests can name the state that makes an order uncancellable without hardcoding
// a bare 4. shipping.PartiallyShippedStatus is the definition.
const FulfillmentStatusPartiallyShipped int16 = 4

// UpdateFulfillmentStatus is the fulfillment_status analogue of
// UpdatePaymentStatus, owned by shipping-logistics (design D6).
func (s *Service) UpdateFulfillmentStatus(ctx context.Context, shopID, orderID int, status int16) (*ent.Order, error) {
	return updateFulfillmentStatus(ctx, s.Client.Order, shopID, orderID, status)
}

// UpdateFulfillmentStatusTx is UpdateFulfillmentStatus bound to a caller's
// transaction (change partial-shipment, design D4).
//
// It exists because an order's fulfillment status is no longer a value the
// shipping package decides on its own — it is derived from every shipment of the
// order, so it must be written in the same transaction that wrote the shipment
// rows justifying it. Splitting them would leave a window where the parcels and
// the status disagree, and the status is what the storefront reads.
//
// This is a parameter, not an abstraction layer: ent generates the same
// *ent.OrderClient type for tx.Order and client.Order, so both entry points run
// character-for-character identical code below. shipping still never touches
// ent.Order itself — the controlled entry point remains the only door.
func (s *Service) UpdateFulfillmentStatusTx(ctx context.Context, tx *ent.Tx, shopID, orderID int, status int16) (*ent.Order, error) {
	return updateFulfillmentStatus(ctx, tx.Order, shopID, orderID, status)
}

func updateFulfillmentStatus(ctx context.Context, oc *ent.OrderClient, shopID, orderID int, status int16) (*ent.Order, error) {
	if status < 0 {
		return nil, validationErr("/fulfillment_status", "fulfillment_status must be >= 0")
	}
	o, err := oc.Query().Where(entorder.IDEQ(orderID), entorder.ShopIDEQ(shopID)).Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	updated, err := o.Update().SetFulfillmentStatus(status).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("order: update fulfillment status: %w", err)
	}
	return updated, nil
}
