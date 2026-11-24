package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/auth"
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
	// change partial-shipment, design D6: the plural form. Additive — the
	// singular route above keeps its path, its shape and its 404 semantics.
	r.Get("/orders/{orderID}/shipments", h.listMineShipments)
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

// shipmentItemJSON is deliberately three integers (change partial-shipment).
//
// It carries no product title, no sku_code and no money: the order item it
// points at already holds an immutable snapshot of all of that, and every
// response that includes shipments is reachable by a caller that can also read
// the order (`order.items[].id` is published). A second copy of the item text
// here would be a third serialization of the same line — the drift member-directory
// design D2 refused for the same reason — and a per-parcel amount would give the
// system two authorities for what the customer was charged.
func shipmentItemJSON(it *ent.ShipmentItem) map[string]any {
	return map[string]any{
		"id": it.ID, "order_item_id": it.OrderItemID, "quantity": it.Quantity,
	}
}

func shipmentJSON(s *ent.Shipment) map[string]any {
	items := make([]map[string]any, 0, len(s.Edges.Items))
	for _, it := range s.Edges.Items {
		items = append(items, shipmentItemJSON(it))
	}
	return map[string]any{
		"id": s.ID, "order_id": s.OrderID,
		"carrier": s.Carrier, "tracking_number": s.TrackingNumber, "status": s.Status,
		"shipped_at": s.ShippedAt, "delivered_at": s.DeliveredAt,
		// What is actually in this parcel (change partial-shipment). Always
		// present, always an array: an order shipped in one go has one entry per
		// order line, which is also what the migration back-filled for every
		// shipment created before line items existed. It is never null, so a
		// caller never has to distinguish "no items" from "old row".
		"items":      items,
		"created_at": s.CreatedAt, "updated_at": s.UpdatedAt,
	}
}

func shipmentsJSON(rows []*ent.Shipment) map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, sh := range rows {
		out = append(out, shipmentJSON(sh))
	}
	return map[string]any{"shipments": out}
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
//
// Items is optional and **a pointer to a slice on purpose** (change
// partial-shipment, design D5): absent means "ship everything outstanding",
// which is what every request written before line items existed meant and still
// means, byte for byte. An explicitly empty array is a different statement — "I
// am shipping, and I am shipping nothing" — and is rejected. A plain []T would
// collapse those two into one value and silently turn the contradiction into the
// default.
type createShipmentRequest struct {
	Carrier        string                        `json:"carrier"`
	TrackingNumber *string                       `json:"tracking_number"`
	Items          *[]shipping.ShipmentItemInput `json:"items"`
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
	sh, err := h.Service.CreateShipment(r.Context(), shopID, orderID, in.Carrier, in.TrackingNumber, in.Items)
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
	// Returning a shipment needs shipment.return IN ADDITION to shipment.update
	// (the route guard). It is checked here, before the service touches
	// anything: a refusal leaves the shipment, the order and the customer's
	// points exactly as they were (change merchant-staff-role, design D2).
	if target == shipping.ReturnedStatus && !h.canReturn(w, r, shopID) {
		return
	}
	sh, err := h.Service.AdvanceShipment(r.Context(), shopID, orderID, shipmentID, target)
	if err != nil {
		writeShippingError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, shipmentJSON(sh))
}

// canReturn decides shipment.return for the caller at this shop with the same
// three-tier snapshot the route guards use; it writes the 401/403/500 itself
// and returns false when the request must stop.
func (h *ShippingHandler) canReturn(w http.ResponseWriter, r *http.Request, shopID int) bool {
	id, ok := auth.AdminFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return false
	}
	snap, err := h.Authz.Engine.Snapshot(r.Context(), id.UserID, shopID)
	if err != nil {
		h.Log.Error("load authorization snapshot", "err", err)
		httpx.Internal(w)
		return false
	}
	if !snap.Allows("shipment.return") {
		httpx.Forbidden(w)
		return false
	}
	return true
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
	httpx.WriteJSON(w, http.StatusOK, shipmentsJSON(rows))
}

// ── member self-service ────────────────────────────────────────────────

// listMineShipments answers "where are my parcels" (change partial-shipment,
// design D6). It exists because this change created its own consumer: with more
// than one shipment per order, the singular endpoint below can only ever show
// one of them, and the customer has no other way to reach the rest.
//
// An order with nothing shipped yet is 200 with an empty array — a collection's
// empty case is not a 404. That is the deliberate difference from the singular
// endpoint, whose 404 keeps its existing meaning for its existing callers.
func (h *ShippingHandler) listMineShipments(w http.ResponseWriter, r *http.Request) {
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
	rows, err := h.Service.ListShipmentsForMember(r.Context(), shopID, memberID, orderID)
	if err != nil {
		writeShippingError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, shipmentsJSON(rows))
}

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
