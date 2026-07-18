package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/order"
)

// OrderHandler implements both the member self-service order API (change
// order-management, no RBAC — "the JWT's member_id owns this order", mirrors
// CartHandler) and the merchant back-office order API (RBAC-gated
// order.view/order.cancel, mirrors ProductsHandler.MountShop).
type OrderHandler struct {
	Client  *ent.Client
	Service *order.Service
	Authz   *AuthzMW
	Log     *slog.Logger
}

// writeOrderError maps order errors to design D12 responses (mirrors
// writeCartError/writeCatalogError).
func writeOrderError(w http.ResponseWriter, log *slog.Logger, err error) {
	var ve *order.ValidationError
	var ce *order.ConflictError
	switch {
	case errors.As(err, &ve):
		httpx.Unprocessable(w, ve.Message, ve.Details)
	case errors.As(err, &ce):
		httpx.Conflict(w, ce.Message)
	case errors.Is(err, order.ErrNotFound):
		httpx.NotFound(w)
	default:
		log.Error("order operation failed", "err", err)
		httpx.Internal(w)
	}
}

// MountShop: member self-service routes mounted inside the existing
// tenant-resolved, member JWT authenticated /shop group (same MemberMW-
// protected chi.Group as CartHandler.MountShop).
func (h *OrderHandler) MountShop(r chi.Router) {
	r.Post("/checkout", h.checkout)
	r.Get("/orders", h.listMine)
	r.Get("/orders/{orderID}", h.getMine)
	r.Post("/orders/{orderID}/cancel", h.cancelMine)
}

// MountShopAdmin: merchant back-office routes under /admin/shops/{shopID},
// gated by RBAC order.view/order.cancel (mirrors ProductsHandler.MountShop).
func (h *OrderHandler) MountShopAdmin(r chi.Router) {
	r.With(h.Authz.RequireShopPermission("order.view")).Get("/orders", h.listAdmin)
	r.With(h.Authz.RequireShopPermission("order.view")).Get("/orders/{orderID}", h.getAdmin)
	r.With(h.Authz.RequireShopPermission("order.cancel")).Post("/orders/{orderID}/cancel", h.cancelAdmin)
}

// ── JSON serialization ────────────────────────────────────────────────

func orderItemJSON(it *ent.OrderItem) map[string]any {
	return map[string]any{
		"id": it.ID, "product_id": it.ProductID, "product_title": it.ProductTitle,
		"sku_id": it.SkuID, "sku_code": it.SkuCode,
		"quantity": it.Quantity, "price_amount": it.PriceAmount, "currency": it.Currency,
		"line_total": it.PriceAmount * int64(it.Quantity),
	}
}

func orderJSON(o *ent.Order) map[string]any {
	items := make([]map[string]any, 0, len(o.Edges.Items))
	for _, it := range o.Edges.Items {
		items = append(items, orderItemJSON(it))
	}
	return map[string]any{
		"id": o.ID, "shop_id": o.ShopID, "member_id": o.MemberID,
		"status": o.Status, "payment_status": o.PaymentStatus, "fulfillment_status": o.FulfillmentStatus,
		"currency": o.Currency, "total_amount": o.TotalAmount,
		"shipping_address": o.ShippingAddress, "cancelled_at": o.CancelledAt,
		"items": items, "created_at": o.CreatedAt, "updated_at": o.UpdatedAt,
	}
}

func orderPageJSON(op *order.OrderPage) map[string]any {
	out := make([]map[string]any, 0, len(op.Orders))
	for _, o := range op.Orders {
		out = append(out, orderJSON(o))
	}
	return map[string]any{
		"orders": out, "page": op.Page, "page_size": op.PageSize, "total": op.Total,
	}
}

// ── member self-service ────────────────────────────────────────────────

func (h *OrderHandler) checkout(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	memberID, ok := memberFrom(w, r)
	if !ok {
		return
	}
	var addr order.ShippingAddress
	if !decodeJSON(w, r, &addr) {
		return
	}
	o, err := h.Service.Checkout(r.Context(), shopID, memberID, addr)
	if err != nil {
		writeOrderError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, orderJSON(o))
}

func (h *OrderHandler) listMine(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	memberID, ok := memberFrom(w, r)
	if !ok {
		return
	}
	params := order.ListParams{
		Page:     paginationParam(r, "page"),
		PageSize: paginationParam(r, "page_size"),
	}
	op, err := h.Service.ListOrders(r.Context(), shopID, memberID, params)
	if err != nil {
		h.Log.Error("list orders", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, orderPageJSON(op))
}

func (h *OrderHandler) getMine(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	memberID, ok := memberFrom(w, r)
	if !ok {
		return
	}
	orderID, ok := intParam(r, "orderID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	o, err := h.Service.GetOrder(r.Context(), shopID, memberID, orderID)
	if err != nil {
		writeOrderError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, orderJSON(o))
}

func (h *OrderHandler) cancelMine(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	memberID, ok := memberFrom(w, r)
	if !ok {
		return
	}
	orderID, ok := intParam(r, "orderID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	o, err := h.Service.CancelOrder(r.Context(), shopID, orderID, &memberID)
	if err != nil {
		writeOrderError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, orderJSON(o))
}

// ── merchant back office ─────────────────────────────────────────────

func (h *OrderHandler) listAdmin(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	params := order.AdminListParams{
		Page:     paginationParam(r, "page"),
		PageSize: paginationParam(r, "page_size"),
		Status:   optionalInt16Query(r, "status"),
	}
	op, err := h.Service.ListOrdersAdmin(r.Context(), shopID, params)
	if err != nil {
		h.Log.Error("list orders (admin)", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, orderPageJSON(op))
}

func (h *OrderHandler) getAdmin(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	orderID, ok := intParam(r, "orderID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	o, err := h.Service.GetOrderAdmin(r.Context(), shopID, orderID)
	if err != nil {
		writeOrderError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, orderJSON(o))
}

func (h *OrderHandler) cancelAdmin(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	orderID, ok := intParam(r, "orderID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	o, err := h.Service.CancelOrder(r.Context(), shopID, orderID, nil)
	if err != nil {
		writeOrderError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, orderJSON(o))
}
