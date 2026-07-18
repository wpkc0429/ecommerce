// Package cart implements the shopping-cart domain (change shopping-cart):
// a self-service resource owned by an authenticated member. Structured like
// api/internal/catalog — one Service with ValidationError/ErrNotFound — but
// deliberately does not import catalog/cms to avoid coupling otherwise
// independent domains for the sake of superficial code reuse.
//
// No ConflictError type here (unlike catalog): this change has no RESTRICT
// deletion semantics — SKU deletion detaches a cart item via SET NULL
// (design D7) rather than blocking anything, so there is no 409 scenario to
// carry.
package cart

import (
	"context"
	"errors"
	"fmt"

	"ksdevworks/ecommerce/api/internal/ent"
	entcart "ksdevworks/ecommerce/api/internal/ent/cart"
	entcartitem "ksdevworks/ecommerce/api/internal/ent/cartitem"
	entproductsku "ksdevworks/ecommerce/api/internal/ent/productsku"
)

// ErrNotFound marks missing resources (handler → 404). Also returned when an
// itemID exists but belongs to a different member's cart — the handler must
// not distinguish "doesn't exist" from "not yours" (design Risks: avoid
// leaking cross-member existence).
var ErrNotFound = errors.New("cart: not found")

// Detail locates one validation problem (JSON Pointer-ish; cart inputs are
// flat so pointers are just "/field" — same convention as catalog.Detail).
type Detail struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

// ValidationError carries 422 payloads (design D12 error envelope).
type ValidationError struct {
	Message string
	Details []Detail
}

func (e *ValidationError) Error() string { return e.Message }

func validationErr(pointer, msg string) *ValidationError {
	return &ValidationError{Message: msg, Details: []Detail{{Pointer: pointer, Message: msg}}}
}

// Cart status enumeration (design D8) — the exact values order-management
// depends on. This change only ever creates/queries status=Active carts; it
// never transitions a cart to Converted or Abandoned.
const (
	StatusActive    int16 = 0 // in use; the only status this change creates or mutates
	StatusConverted int16 = 1 // checked out into an order — set by order-management, never by this package
	StatusAbandoned int16 = 2 // reserved for a future no-activity detector; unused by this change
)

// Unavailable reasons for CartItemView.UnavailableReason (design D6/D7).
// Empty string means the item is purchasable.
const (
	ReasonSKUInactive        = "sku_inactive"
	ReasonProductUnpublished = "product_unpublished"
	ReasonInsufficientStock  = "insufficient_stock"
	ReasonSKUDeleted         = "sku_deleted"
)

// Service implements cart operations, all scoped by explicit shopID/memberID
// parameters (never ambient tenant/member context — same convention as
// catalog.Service's explicit shopID: keeps the package testable without
// faking context values, and the caller — the HTTP handler — is the single
// place responsible for extracting identity from the request).
type Service struct {
	Client *ent.Client
}

// CartView is a read-only projection of a member's current cart (design D9),
// not an ent entity. ID is nil when no active cart row exists yet (design
// D3 — GetCartView never creates one).
type CartView struct {
	ID        *int
	Status    int16
	Currency  string
	Items     []CartItemView
	Subtotal  int64 // sum of Items[].LineTotal (design D2: snapshot-based, never a live SKU re-query)
	Total     int64 // == Subtotal in this phase (no tax/shipping/discount yet)
	ItemCount int
}

// CartItemView is a read-only projection of one cart_items row plus
// point-in-time purchasability, computed fresh on every read (design D6) —
// never stored.
type CartItemView struct {
	ID                int
	SKUID             *int   // nil if the SKU was deleted (design D7)
	SKUCode           string // "" if SKUID is nil
	Quantity          int32
	PriceAmount       int64  // snapshot (design D2)
	Currency          string // snapshot
	LineTotal         int64  // PriceAmount * int64(Quantity)
	Purchasable       bool
	UnavailableReason string
}

func emptyCartView() *CartView {
	return &CartView{Status: StatusActive, Items: []CartItemView{}}
}

// GetCartView returns the member's current (active) cart. When no active
// cart row exists, it returns an ephemeral empty view rather than creating
// one (design D3) — callers must not assume ID != nil.
func (s *Service) GetCartView(ctx context.Context, shopID, memberID int) (*CartView, error) {
	c, err := s.Client.Cart.Query().
		Where(entcart.ShopIDEQ(shopID), entcart.MemberIDEQ(memberID), entcart.StatusEQ(StatusActive)).
		WithItems(func(q *ent.CartItemQuery) {
			q.WithSku(func(sq *ent.ProductSKUQuery) { sq.WithProduct() })
		}).
		Only(ctx)
	if ent.IsNotFound(err) {
		return emptyCartView(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("cart: get cart view: %w", err)
	}
	return buildCartView(c), nil
}

func buildCartView(c *ent.Cart) *CartView {
	id := c.ID
	view := &CartView{
		ID:       &id,
		Status:   c.Status,
		Currency: c.Currency,
		Items:    make([]CartItemView, 0, len(c.Edges.Items)),
	}
	var subtotal int64
	for _, it := range c.Edges.Items {
		iv := buildItemView(it)
		subtotal += iv.LineTotal
		view.Items = append(view.Items, iv)
	}
	view.Subtotal = subtotal
	view.Total = subtotal
	view.ItemCount = len(view.Items)
	return view
}

// buildItemView computes the point-in-time purchasability flag (design D6):
// checked in order — SKU deleted, SKU inactive, product unpublished,
// insufficient stock, else purchasable. The price/currency snapshot is
// always taken from the stored columns, never the live SKU (design D2).
func buildItemView(it *ent.CartItem) CartItemView {
	iv := CartItemView{
		ID:          it.ID,
		SKUID:       it.SkuID,
		Quantity:    it.Quantity,
		PriceAmount: it.PriceAmount,
		Currency:    it.Currency,
		LineTotal:   it.PriceAmount * int64(it.Quantity),
	}
	sku := it.Edges.Sku
	switch {
	case it.SkuID == nil || sku == nil:
		iv.UnavailableReason = ReasonSKUDeleted
	case !sku.IsActive:
		iv.SKUCode = sku.SkuCode
		iv.UnavailableReason = ReasonSKUInactive
	case sku.Edges.Product == nil || sku.Edges.Product.Status != 1:
		iv.SKUCode = sku.SkuCode
		iv.UnavailableReason = ReasonProductUnpublished
	case sku.StockQty < it.Quantity:
		iv.SKUCode = sku.SkuCode
		iv.UnavailableReason = ReasonInsufficientStock
	default:
		iv.SKUCode = sku.SkuCode
		iv.Purchasable = true
	}
	return iv
}

// AddItem finds-or-creates the member's active cart and either creates a new
// line item or accumulates onto an existing one for the same SKU (design
// D5). Validates the SKU belongs to the shop, is active, its product is
// published, its currency matches the cart's locked currency (design D4),
// and the resulting quantity does not exceed current stock — any failure is
// a ValidationError (422), never a 500 (spec Add item validation).
func (s *Service) AddItem(ctx context.Context, shopID, memberID, skuID int, quantity int32) (*CartView, error) {
	if quantity <= 0 {
		return nil, validationErr("/quantity", "quantity must be greater than zero")
	}

	sku, err := s.Client.ProductSKU.Query().
		Where(entproductsku.IDEQ(skuID), entproductsku.ShopIDEQ(shopID)).
		WithProduct().
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, validationErr("/sku_id", "sku does not exist in this shop")
	}
	if err != nil {
		return nil, fmt.Errorf("cart: query sku: %w", err)
	}
	if !sku.IsActive {
		return nil, validationErr("/sku_id", "sku is not active")
	}
	if sku.Edges.Product == nil || sku.Edges.Product.Status != 1 {
		return nil, validationErr("/sku_id", "product is not published")
	}

	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("cart: begin tx: %w", err)
	}

	c, err := tx.Cart.Query().
		Where(entcart.ShopIDEQ(shopID), entcart.MemberIDEQ(memberID), entcart.StatusEQ(StatusActive)).
		Only(ctx)
	switch {
	case ent.IsNotFound(err):
		c, err = tx.Cart.Create().
			SetShopID(shopID).SetMemberID(memberID).SetStatus(StatusActive).SetCurrency(sku.Currency).
			Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("cart: create cart: %w", err)
		}
	case err != nil:
		_ = tx.Rollback()
		return nil, fmt.Errorf("cart: query active cart: %w", err)
	default:
		if c.Currency != sku.Currency {
			_ = tx.Rollback()
			return nil, &ValidationError{
				Message: "sku currency does not match the cart's currency",
				Details: []Detail{{Pointer: "/sku_id", Message: fmt.Sprintf("cart currency is %s, sku currency is %s", c.Currency, sku.Currency)}},
			}
		}
	}

	existing, err := tx.CartItem.Query().
		Where(entcartitem.CartIDEQ(c.ID), entcartitem.SkuIDEQ(skuID)).
		Only(ctx)
	switch {
	case ent.IsNotFound(err):
		if int32(sku.StockQty) < quantity {
			_ = tx.Rollback()
			return nil, validationErr("/quantity", "quantity exceeds available stock")
		}
		if _, err := tx.CartItem.Create().
			SetShopID(shopID).SetCartID(c.ID).SetSkuID(skuID).
			SetQuantity(quantity).SetPriceAmount(sku.PriceAmount).SetCurrency(sku.Currency).
			Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("cart: create item: %w", err)
		}
	case err != nil:
		_ = tx.Rollback()
		return nil, fmt.Errorf("cart: query existing item: %w", err)
	default:
		newQty := existing.Quantity + quantity
		if sku.StockQty < newQty {
			_ = tx.Rollback()
			return nil, validationErr("/quantity", "quantity exceeds available stock")
		}
		if _, err := existing.Update().SetQuantity(newQty).Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("cart: accumulate item quantity: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("cart: commit add item: %w", err)
	}
	return s.GetCartView(ctx, shopID, memberID)
}

// ownedItem loads a cart item scoped to shopID and verifies it belongs to a
// cart owned by memberID, returning ErrNotFound (not a permission error) on
// any mismatch so the handler's 404 does not confirm or deny another
// member's item exists (design Risks).
func (s *Service) ownedItem(ctx context.Context, shopID, memberID, itemID int) (*ent.CartItem, error) {
	item, err := s.Client.CartItem.Query().
		Where(entcartitem.IDEQ(itemID), entcartitem.ShopIDEQ(shopID)).
		WithCart().
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	if item.Edges.Cart == nil || item.Edges.Cart.MemberID != memberID {
		return nil, ErrNotFound
	}
	return item, nil
}

// UpdateItemQuantity sets an absolute quantity (design D5 — not an
// accumulation like AddItem). quantity <= 0 is rejected; RemoveItem is the
// way to delete a line. Re-validates against current stock when the SKU
// still exists; an item whose SKU was deleted (SkuID == nil) can still have
// its quantity adjusted (there is nothing left to validate stock against) —
// it remains unpurchasable regardless (design D7).
func (s *Service) UpdateItemQuantity(ctx context.Context, shopID, memberID, itemID int, quantity int32) (*CartView, error) {
	if quantity <= 0 {
		return nil, validationErr("/quantity", "quantity must be greater than zero")
	}
	item, err := s.ownedItem(ctx, shopID, memberID, itemID)
	if err != nil {
		return nil, err
	}
	if item.SkuID != nil {
		sku, err := s.Client.ProductSKU.Query().
			Where(entproductsku.IDEQ(*item.SkuID), entproductsku.ShopIDEQ(shopID)).
			Only(ctx)
		if err == nil && sku.StockQty < quantity {
			return nil, validationErr("/quantity", "quantity exceeds available stock")
		}
	}
	if _, err := item.Update().SetQuantity(quantity).Save(ctx); err != nil {
		return nil, fmt.Errorf("cart: update item quantity: %w", err)
	}
	return s.GetCartView(ctx, shopID, memberID)
}

// RemoveItem deletes one line item from the caller's own cart.
func (s *Service) RemoveItem(ctx context.Context, shopID, memberID, itemID int) error {
	item, err := s.ownedItem(ctx, shopID, memberID, itemID)
	if err != nil {
		return err
	}
	return s.Client.CartItem.DeleteOne(item).Exec(ctx)
}

// ClearCart deletes every item in the member's active cart. A member with no
// active cart has nothing to clear — this is a no-op success, not an error
// (spec Remove item and clear cart: 幂等性要求).
func (s *Service) ClearCart(ctx context.Context, shopID, memberID int) error {
	c, err := s.Client.Cart.Query().
		Where(entcart.ShopIDEQ(shopID), entcart.MemberIDEQ(memberID), entcart.StatusEQ(StatusActive)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cart: find active cart: %w", err)
	}
	if _, err := s.Client.CartItem.Delete().Where(entcartitem.CartIDEQ(c.ID)).Exec(ctx); err != nil {
		return fmt.Errorf("cart: clear cart: %w", err)
	}
	return nil
}
