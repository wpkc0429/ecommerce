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
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"ksdevworks/ecommerce/api/internal/cart"
	"ksdevworks/ecommerce/api/internal/ent"
	entcart "ksdevworks/ecommerce/api/internal/ent/cart"
	entorder "ksdevworks/ecommerce/api/internal/ent/order"
	entorderitem "ksdevworks/ecommerce/api/internal/ent/orderitem"
	"ksdevworks/ecommerce/api/internal/ent/predicate"
	entproductsku "ksdevworks/ecommerce/api/internal/ent/productsku"
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
type ShippingAddress struct {
	RecipientName string `json:"recipient_name"`
	Phone         string `json:"phone"`
	Line1         string `json:"line1"`
	Line2         string `json:"line2,omitempty"`
	City          string `json:"city"`
	PostalCode    string `json:"postal_code"`
	Country       string `json:"country"`
}

// validate checks the required subset of ShippingAddress fields (design D8).
// Line2 is the only optional field.
func (a ShippingAddress) validate() error {
	var details []Detail
	require := func(v, field string) {
		if v == "" {
			details = append(details, Detail{Pointer: "/shipping_address/" + field, Message: field + " is required"})
		}
	}
	require(a.RecipientName, "recipient_name")
	require(a.Phone, "phone")
	require(a.Line1, "line1")
	require(a.City, "city")
	require(a.PostalCode, "postal_code")
	require(a.Country, "country")
	if len(details) > 0 {
		return &ValidationError{Message: "shipping address is incomplete", Details: details}
	}
	return nil
}

// Service implements order operations, all scoped by explicit shopID/
// memberID parameters (never ambient tenant/member context — same
// convention as cart.Service/catalog.Service: keeps the package testable
// without faking context values, and the caller — the HTTP handler — is the
// single place responsible for extracting identity from the request).
type Service struct {
	Client *ent.Client
}

// ── checkout ────────────────────────────────────────────────────────────

// Checkout converts the member's active cart into an order (design D3), all
// within a single ent.Tx:
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
func (s *Service) Checkout(ctx context.Context, shopID, memberID int, addr ShippingAddress) (*ent.Order, error) {
	if err := addr.validate(); err != nil {
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
	var total int64
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
		total += it.PriceAmount * int64(it.Quantity)
	}

	addrJSON, err := json.Marshal(addr)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("order: marshal shipping address: %w", err)
	}

	o, err := tx.Order.Create().
		SetShopID(shopID).SetMemberID(memberID).
		SetStatus(StatusCreated).
		SetPaymentStatus(PaymentStatusUnpaid).
		SetFulfillmentStatus(FulfillmentStatusUnfulfilled).
		SetCurrency(c.Currency).
		SetTotalAmount(total).
		SetShippingAddress(addrJSON).
		Save(ctx)
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
			Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("order: create order item: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("order: commit checkout: %w", err)
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
// foreign order and a nonexistent order both surface as the same
// ErrNotFound (no existence leakage).
func (s *Service) GetOrder(ctx context.Context, shopID, memberID, orderID int) (*ent.Order, error) {
	o, err := s.Client.Order.Query().
		Where(entorder.IDEQ(orderID), entorder.ShopIDEQ(shopID), entorder.MemberIDEQ(memberID)).
		WithItems().
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
type AdminListParams struct {
	Page     int
	PageSize int
	Status   *int16
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
		WithItems().
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	return o, nil
}

// ListOrdersAdmin lists every order within shopID, newest first, optionally
// filtered by order status.
func (s *Service) ListOrdersAdmin(ctx context.Context, shopID int, params AdminListParams) (*OrderPage, error) {
	page, pageSize := normalizePage(params.Page, params.PageSize)
	q := s.Client.Order.Query().Where(entorder.ShopIDEQ(shopID))
	if params.Status != nil {
		q = q.Where(entorder.StatusEQ(*params.Status))
	}
	return paginate(ctx, q, page, pageSize)
}

func paginate(ctx context.Context, q *ent.OrderQuery, page, pageSize int) (*OrderPage, error) {
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("order: count orders: %w", err)
	}
	orders, err := q.
		WithItems().
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

// UpdateFulfillmentStatus is the fulfillment_status analogue of
// UpdatePaymentStatus, owned by shipping-logistics (design D6).
func (s *Service) UpdateFulfillmentStatus(ctx context.Context, shopID, orderID int, status int16) (*ent.Order, error) {
	if status < 0 {
		return nil, validationErr("/fulfillment_status", "fulfillment_status must be >= 0")
	}
	o, err := s.Client.Order.Query().Where(entorder.IDEQ(orderID), entorder.ShopIDEQ(shopID)).Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	updated, err := o.Update().SetFulfillmentStatus(status).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("order: update fulfillment status: %w", err)
	}
	return updated, nil
}
