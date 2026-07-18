package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cart"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
)

// CartHandler implements the member self-service cart API (change
// shopping-cart, tasks 5.1-5.4): no RBAC — access control is solely "the
// JWT's member_id owns this cart" (spec Member-owned cart access control).
// Routes never take a cart id or member id path parameter; the only
// externally supplied id is itemID, whose ownership is verified inside
// cart.Service (design Risks — avoids IDOR by construction).
type CartHandler struct {
	Client  *ent.Client
	Service *cart.Service
	Log     *slog.Logger
}

// writeCartError maps cart errors to design D12 responses (mirrors
// writeCatalogError; no ConflictError variant here — see cart.Service doc).
func writeCartError(w http.ResponseWriter, log *slog.Logger, err error) {
	var ve *cart.ValidationError
	switch {
	case errors.As(err, &ve):
		httpx.Unprocessable(w, ve.Message, ve.Details)
	case errors.Is(err, cart.ErrNotFound):
		httpx.NotFound(w)
	default:
		log.Error("cart operation failed", "err", err)
		httpx.Internal(w)
	}
}

// MountShop: routes mounted inside the existing tenant-resolved, member JWT
// authenticated /shop group (router.go task 5.5 — same d.MemberMW-protected
// chi.Group as MemberAuthHandler.Me).
func (h *CartHandler) MountShop(r chi.Router) {
	r.Get("/cart", h.get)
	r.Post("/cart/items", h.addItem)
	r.Put("/cart/items/{itemID}", h.updateItem)
	r.Delete("/cart/items/{itemID}", h.removeItem)
	r.Delete("/cart/items", h.clearCart)
}

func cartItemJSON(it cart.CartItemView) map[string]any {
	return map[string]any{
		"id": it.ID, "sku_id": it.SKUID, "sku_code": it.SKUCode,
		"quantity": it.Quantity, "price_amount": it.PriceAmount, "currency": it.Currency,
		"line_total": it.LineTotal, "purchasable": it.Purchasable, "unavailable_reason": it.UnavailableReason,
	}
}

// cartViewJSON serializes a CartView; currency is emitted as null when no
// cart row exists yet (design D3 — an ephemeral empty cart has no locked
// currency until the first item is added).
func cartViewJSON(v *cart.CartView) map[string]any {
	items := make([]map[string]any, 0, len(v.Items))
	for _, it := range v.Items {
		items = append(items, cartItemJSON(it))
	}
	var currency any
	if v.ID != nil {
		currency = v.Currency
	}
	return map[string]any{
		"id": v.ID, "status": v.Status, "currency": currency,
		"items": items, "subtotal": v.Subtotal, "total": v.Total, "item_count": v.ItemCount,
	}
}

// memberFrom resolves the authenticated member; routes are always mounted
// behind d.MemberMW so this should never miss, but the defensive check
// mirrors MemberAuthHandler.Me.
func memberFrom(w http.ResponseWriter, r *http.Request) (int, bool) {
	mid, ok := auth.MemberFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return 0, false
	}
	return mid, true
}

func (h *CartHandler) get(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	memberID, ok := memberFrom(w, r)
	if !ok {
		return
	}
	view, err := h.Service.GetCartView(r.Context(), shopID, memberID)
	if err != nil {
		h.Log.Error("get cart", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, cartViewJSON(view))
}

func (h *CartHandler) addItem(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	memberID, ok := memberFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		SKUID    int   `json:"sku_id"`
		Quantity int32 `json:"quantity"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	view, err := h.Service.AddItem(r.Context(), shopID, memberID, in.SKUID, in.Quantity)
	if err != nil {
		writeCartError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, cartViewJSON(view))
}

func (h *CartHandler) updateItem(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	memberID, ok := memberFrom(w, r)
	if !ok {
		return
	}
	itemID, ok := intParam(r, "itemID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var in struct {
		Quantity int32 `json:"quantity"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	view, err := h.Service.UpdateItemQuantity(r.Context(), shopID, memberID, itemID, in.Quantity)
	if err != nil {
		writeCartError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, cartViewJSON(view))
}

func (h *CartHandler) removeItem(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	memberID, ok := memberFrom(w, r)
	if !ok {
		return
	}
	itemID, ok := intParam(r, "itemID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	if err := h.Service.RemoveItem(r.Context(), shopID, memberID, itemID); err != nil {
		writeCartError(w, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CartHandler) clearCart(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	memberID, ok := memberFrom(w, r)
	if !ok {
		return
	}
	if err := h.Service.ClearCart(r.Context(), shopID, memberID); err != nil {
		writeCartError(w, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
