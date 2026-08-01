package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/idempotency"
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

	// Idempotency backs the optional Idempotency-Key header on member checkout
	// (change checkout-idempotency). The cart Active→Converted CAS already stops
	// a double submit from creating two orders, but it answers with a 409 that
	// does not say *which* order — so it cannot tell a client whose response was
	// lost whether its purchase exists. That is a different problem, and this is
	// what solves it.
	Idempotency *idempotency.Service
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
		// change order-extras: always a string, never null.
		"gift_message": it.GiftMessage,
	}
}

// deliveryDateJSON renders a requested delivery date as a bare YYYY-MM-DD
// string (or null): the column is a calendar day, and emitting a full timestamp
// would invite clients to reintroduce the timezone shift the date type exists
// to avoid (change order-extras, design D3).
func deliveryDateJSON(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format("2006-01-02")
}

func orderJSON(o *ent.Order) map[string]any {
	items := make([]map[string]any, 0, len(o.Edges.Items))
	for _, it := range o.Edges.Items {
		items = append(items, orderItemJSON(it))
	}
	return map[string]any{
		"id": o.ID, "shop_id": o.ShopID, "member_id": o.MemberID,
		"status": o.Status, "payment_status": o.PaymentStatus, "fulfillment_status": o.FulfillmentStatus,
		"currency": o.Currency,
		// change order-extras, design D6: total_amount is the PAYABLE total —
		// subtotal_amount + shipping_fee — and is what payment charges. It used
		// to be the item subtotal alone; subtotal_amount now carries that.
		"total_amount": o.TotalAmount, "subtotal_amount": o.SubtotalAmount, "shipping_fee": o.ShippingFee,
		"shipping_method_id": o.ShippingMethodID, "shipping_method_name": o.ShippingMethodName,
		"requested_delivery_date": deliveryDateJSON(o.RequestedDeliveryDate),
		"shipping_address":        o.ShippingAddress, "cancelled_at": o.CancelledAt,
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
	// The body has always been the shipping address itself, and change
	// order-extras keeps it that way: the two new keys are embedded flat
	// alongside the address fields rather than nesting the address under a
	// wrapper (design D11). Nesting would break every existing caller to buy
	// nothing but symmetry with the guest endpoint; adding sibling keys leaves
	// every previously-valid request valid, byte for byte.
	// Read whole rather than streamed: the idempotency fingerprint is taken over
	// the raw bytes (change checkout-idempotency, design D7).
	raw, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	// The member id belongs in the fingerprint scope, and this is not decoration
	// (design D7): this endpoint's body is nothing but a shipping address, so two
	// customers sending to the same address produce byte-identical bodies. Keyed
	// on the body alone, a key collision between them would hand one customer the
	// other's order.
	gate, replayOrderID, ok := beginIdempotency(w, r, h.Idempotency, h.Log, shopID,
		idempotency.MemberCheckoutScope(memberID), raw)
	if !ok {
		return
	}
	if replayOrderID > 0 {
		// Member-scoped lookup, not the shop-scoped one: defense in depth, so
		// that even a broken fingerprint could never become horizontal
		// escalation. A foreign order surfaces as the ordinary 404.
		o, err := h.Service.GetOrder(r.Context(), shopID, memberID, replayOrderID)
		if err != nil {
			writeOrderError(w, h.Log, err)
			return
		}
		w.Header().Set(IdempotencyReplayedHeader, "true")
		httpx.WriteJSON(w, http.StatusCreated, orderJSON(o))
		return
	}

	var body struct {
		order.ShippingAddress
		ShippingMethodID      *int    `json:"shipping_method_id"`
		RequestedDeliveryDate *string `json:"requested_delivery_date"`
	}
	if !unmarshalJSON(w, raw, &body) {
		gate.release(r)
		return
	}
	o, err := h.Service.Checkout(r.Context(), shopID, memberID, order.CheckoutInput{
		ShippingAddress:          body.ShippingAddress,
		ShippingMethodID:         body.ShippingMethodID,
		RequestedDeliveryDate:    body.RequestedDeliveryDate,
		IdempotencyReservationID: gate.reservation(),
	})
	if err != nil {
		gate.release(r)
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

// optionalDateQuery parses a query param as a bare calendar day (YYYY-MM-DD).
// A missing or unparseable value yields nil — "filter absent" — following the
// same tolerant-input rule as paginationParam/optionalIntQuery (design D3 of
// change product-catalog).
//
// The layout is deliberately date-only, matching orders.requested_delivery_date's
// Postgres `date` type: accepting a timestamp here would invite a timezone
// offset into a comparison that has none (change admin-commerce-operations,
// design D4).
func optionalDateQuery(r *http.Request, name string) *time.Time {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil
	}
	d, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil
	}
	return &d
}

func (h *OrderHandler) listAdmin(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	params := order.AdminListParams{
		Page:     paginationParam(r, "page"),
		PageSize: paginationParam(r, "page_size"),
		Status:   optionalInt16Query(r, "status"),
		// change admin-commerce-operations (design D3): every filter below is
		// optional, and a request carrying none of them behaves exactly as it
		// did before they existed.
		PaymentStatus:     optionalInt16Query(r, "payment_status"),
		FulfillmentStatus: optionalInt16Query(r, "fulfillment_status"),
		DeliveryFrom:      optionalDateQuery(r, "delivery_from"),
		DeliveryTo:        optionalDateQuery(r, "delivery_to"),
		Q:                 r.URL.Query().Get("q"),
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
