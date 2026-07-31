package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/shipping"
)

// ShippingHandler implements both the merchant back-office API (shipping
// method CRUD + shipment create/advance/list, RBAC-gated shipping_method.*/
// shipment.*, mirrors CategoriesHandler/PaymentHandler.MountShopAdmin) and
// the member self-service shipment read API (change shipping-logistics, no
// RBAC — mirrors OrderHandler/PaymentHandler.MountShop: "the JWT's
// member_id owns this order").
type ShippingHandler struct {
	Client  *ent.Client
	Service *shipping.Service
	Authz   *AuthzMW
	Log     *slog.Logger
}

// writeShippingError maps shipping errors to design D12 responses (mirrors
// writeOrderError/writePaymentError/writeCatalogError).
func writeShippingError(w http.ResponseWriter, log *slog.Logger, err error) {
	var ve *shipping.ValidationError
	var ce *shipping.ConflictError
	switch {
	case errors.As(err, &ve):
		httpx.Unprocessable(w, ve.Message, ve.Details)
	case errors.As(err, &ce):
		httpx.Conflict(w, ce.Message)
	case errors.Is(err, shipping.ErrNotFound):
		httpx.NotFound(w)
	default:
		log.Error("shipping operation failed", "err", err)
		httpx.Internal(w)
	}
}

// MountShopAdmin: merchant back-office routes under /admin/shops/{shopID},
// gated by RBAC shipping_method.*/shipment.* (mirrors
// CategoriesHandler.MountShop/PaymentHandler.MountShopAdmin).
func (h *ShippingHandler) MountShopAdmin(r chi.Router) {
	r.With(h.Authz.RequireShopPermission("shipping_method.view")).Get("/shipping-methods", h.listShippingMethods)
	r.With(h.Authz.RequireShopPermission("shipping_method.create")).Post("/shipping-methods", h.createShippingMethod)
	r.With(h.Authz.RequireShopPermission("shipping_method.view")).Get("/shipping-methods/{shippingMethodID}", h.getShippingMethod)
	r.With(h.Authz.RequireShopPermission("shipping_method.edit")).Put("/shipping-methods/{shippingMethodID}", h.updateShippingMethod)
	r.With(h.Authz.RequireShopPermission("shipping_method.delete")).Delete("/shipping-methods/{shippingMethodID}", h.deleteShippingMethod)

	r.With(h.Authz.RequireShopPermission("shipment.create")).Post("/orders/{orderID}/shipments", h.createShipment)
	r.With(h.Authz.RequireShopPermission("shipment.update")).Put("/orders/{orderID}/shipments/{shipmentID}", h.advanceShipment)
	r.With(h.Authz.RequireShopPermission("shipment.view")).Get("/orders/{orderID}/shipments", h.listShipments)
}

// MountShop: member self-service route mounted inside the existing
// tenant-resolved, member JWT authenticated /shop group (same MemberMW-
// protected chi.Group as OrderHandler.MountShop/PaymentHandler.MountShop).
func (h *ShippingHandler) MountShop(r chi.Router) {
	r.Get("/orders/{orderID}/shipment", h.getMineShipment)
}

// MountPublic: the storefront-facing shipping method list (change
// order-extras, design D4), mounted in the tenant-resolved /shop group with no
// authentication at all — same placement and reasoning as
// ProductsHandler.MountPublic. A buyer must be able to see what shipping costs
// before there is any identity to speak of.
func (h *ShippingHandler) MountPublic(r chi.Router) {
	r.Get("/shipping-methods", h.publicListShippingMethods)
}

// ── JSON serialization ────────────────────────────────────────────────

func shippingMethodJSON(sm *ent.ShippingMethod) map[string]any {
	return map[string]any{
		"id": sm.ID, "shop_id": sm.ShopID,
		"name": sm.Name, "carrier": sm.Carrier,
		"flat_rate": sm.FlatRate, "is_active": sm.IsActive,
		"created_at": sm.CreatedAt, "updated_at": sm.UpdatedAt,
	}
}

// publicShippingMethodJSON is deliberately smaller than the back-office shape
// (change order-extras, design D4). v1 accepts additive changes only, so every
// key published here is a permanent commitment; is_active would carry no
// information (all rows returned are active) and the timestamps are back-office
// bookkeeping. Publishing the minimum is the only direction that can still grow.
//
// flat_rate is minor currency units in the order's currency — the cart, and
// therefore the order, is single-currency this phase (design D4's known limit).
func publicShippingMethodJSON(sm *ent.ShippingMethod) map[string]any {
	return map[string]any{
		"id": sm.ID, "name": sm.Name, "carrier": sm.Carrier, "flat_rate": sm.FlatRate,
	}
}

func shipmentJSON(s *ent.Shipment) map[string]any {
	return map[string]any{
		"id": s.ID, "order_id": s.OrderID,
		"carrier": s.Carrier, "tracking_number": s.TrackingNumber, "status": s.Status,
		"shipped_at": s.ShippedAt, "delivered_at": s.DeliveredAt,
		"created_at": s.CreatedAt, "updated_at": s.UpdatedAt,
	}
}

// ── shipping methods CRUD (merchant back office) ───────────────────────

func (h *ShippingHandler) listShippingMethods(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	rows, err := h.Service.ListShippingMethods(r.Context(), shopID)
	if err != nil {
		h.Log.Error("list shipping methods", "err", err)
		httpx.Internal(w)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, sm := range rows {
		out = append(out, shippingMethodJSON(sm))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"shipping_methods": out})
}

// publicListShippingMethods answers the storefront's "what can I choose and
// what does it cost". Tenant comes from the resolved shop context (publishable
// key, else Host), never from the request body.
func (h *ShippingHandler) publicListShippingMethods(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	rows, err := h.Service.ListActiveShippingMethods(r.Context(), shopID)
	if err != nil {
		h.Log.Error("list active shipping methods", "err", err)
		httpx.Internal(w)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, sm := range rows {
		out = append(out, publicShippingMethodJSON(sm))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"shipping_methods": out})
}

func (h *ShippingHandler) createShippingMethod(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	var in shipping.ShippingMethodInput
	if !decodeJSON(w, r, &in) {
		return
	}
	sm, err := h.Service.CreateShippingMethod(r.Context(), shopID, in)
	if err != nil {
		writeShippingError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, shippingMethodJSON(sm))
}

func (h *ShippingHandler) getShippingMethod(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := intParam(r, "shippingMethodID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	sm, err := h.Service.GetShippingMethod(r.Context(), shopID, id)
	if err != nil {
		writeShippingError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, shippingMethodJSON(sm))
}

func (h *ShippingHandler) updateShippingMethod(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := intParam(r, "shippingMethodID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var in shipping.ShippingMethodUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	sm, err := h.Service.UpdateShippingMethod(r.Context(), shopID, id, in)
	if err != nil {
		writeShippingError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, shippingMethodJSON(sm))
}

func (h *ShippingHandler) deleteShippingMethod(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := intParam(r, "shippingMethodID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	if err := h.Service.DeleteShippingMethod(r.Context(), shopID, id); err != nil {
		writeShippingError(w, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── shipments (merchant back office) ────────────────────────────────────

// createShipmentRequest is the body of POST .../orders/{orderID}/shipments
// (design D3): carrier is required, tracking_number is optional (a nil
// pointer vs. an explicit empty string are both treated as "not yet known"
// by shipping.Service.CreateShipment).
type createShipmentRequest struct {
	Carrier        string  `json:"carrier"`
	TrackingNumber *string `json:"tracking_number"`
}

func (h *ShippingHandler) createShipment(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	orderID, ok := intParam(r, "orderID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var in createShipmentRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	sh, err := h.Service.CreateShipment(r.Context(), shopID, orderID, in.Carrier, in.TrackingNumber)
	if err != nil {
		writeShippingError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, shipmentJSON(sh))
}

// advanceShipmentRequest is the body of PUT .../shipments/{shipmentID}
// (design D3): status is the target state, expressed as the same lowercase
// strings the rest of the admin API convention favors over raw integers on
// the wire (mirrors e.g. payment webhook outcome strings).
type advanceShipmentRequest struct {
	Status string `json:"status"`
}

func (h *ShippingHandler) advanceShipment(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	orderID, ok := intParam(r, "orderID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	shipmentID, ok := intParam(r, "shipmentID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var in advanceShipmentRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	var target int16
	switch in.Status {
	case "delivered":
		target = shipping.DeliveredStatus
	case "returned":
		target = shipping.ReturnedStatus
	default:
		httpx.Unprocessable(w, `status must be "delivered" or "returned"`, nil)
		return
	}
	sh, err := h.Service.AdvanceShipment(r.Context(), shopID, orderID, shipmentID, target)
	if err != nil {
		writeShippingError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, shipmentJSON(sh))
}

func (h *ShippingHandler) listShipments(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	orderID, ok := intParam(r, "orderID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	rows, err := h.Service.ListShipmentsAdmin(r.Context(), shopID, orderID)
	if err != nil {
		writeShippingError(w, h.Log, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, sh := range rows {
		out = append(out, shipmentJSON(sh))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"shipments": out})
}

// ── member self-service ────────────────────────────────────────────────

func (h *ShippingHandler) getMineShipment(w http.ResponseWriter, r *http.Request) {
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
	sh, err := h.Service.GetShipmentForMember(r.Context(), shopID, memberID, orderID)
	if err != nil {
		writeShippingError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, shipmentJSON(sh))
}
