package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/points"
)

// PointsHandler implements both the merchant back-office API (member tier
// CRUD + a shop member's points balance/ledger/manual-adjustment, RBAC-gated
// member_tier.*/point.*, mirrors ShippingHandler.MountShopAdmin) and the
// member self-service points read API (change member-tiers-and-points, no
// RBAC — mirrors OrderHandler/ShippingHandler.MountShop: "the JWT's
// member_id owns this data").
type PointsHandler struct {
	Client  *ent.Client
	Service *points.Service
	Authz   *AuthzMW
	Log     *slog.Logger
}

// writePointsError maps points errors to design D12 responses (mirrors
// writeOrderError/writePaymentError/writeShippingError).
func writePointsError(w http.ResponseWriter, log *slog.Logger, err error) {
	var ve *points.ValidationError
	switch {
	case errors.As(err, &ve):
		httpx.Unprocessable(w, ve.Message, ve.Details)
	case errors.Is(err, points.ErrNotFound):
		httpx.NotFound(w)
	default:
		log.Error("points operation failed", "err", err)
		httpx.Internal(w)
	}
}

// MountShopAdmin: merchant back-office routes under /admin/shops/{shopID},
// gated by RBAC member_tier.*/point.* (mirrors ShippingHandler.MountShopAdmin).
func (h *PointsHandler) MountShopAdmin(r chi.Router) {
	r.With(h.Authz.RequireShopPermission("member_tier.view")).Get("/member-tiers", h.listMemberTiers)
	r.With(h.Authz.RequireShopPermission("member_tier.create")).Post("/member-tiers", h.createMemberTier)
	r.With(h.Authz.RequireShopPermission("member_tier.view")).Get("/member-tiers/{memberTierID}", h.getMemberTier)
	r.With(h.Authz.RequireShopPermission("member_tier.edit")).Put("/member-tiers/{memberTierID}", h.updateMemberTier)
	r.With(h.Authz.RequireShopPermission("member_tier.delete")).Delete("/member-tiers/{memberTierID}", h.deleteMemberTier)

	r.With(h.Authz.RequireShopPermission("point.view")).Get("/members/{shopMemberID}/points", h.getMemberPointsAdmin)
	r.With(h.Authz.RequireShopPermission("point.view")).Get("/members/{shopMemberID}/points/transactions", h.listMemberTransactionsAdmin)
	r.With(h.Authz.RequireShopPermission("point.adjust")).Post("/members/{shopMemberID}/points/adjust", h.adjustPoints)
}

// MountShop: member self-service routes mounted inside the existing
// tenant-resolved, member JWT authenticated /shop group (same MemberMW-
// protected chi.Group as OrderHandler.MountShop/ShippingHandler.MountShop).
func (h *PointsHandler) MountShop(r chi.Router) {
	r.Get("/points", h.getMinePoints)
	r.Get("/points/transactions", h.listMineTransactions)
}

// ── JSON serialization ────────────────────────────────────────────────

func memberTierJSON(mt *ent.MemberTier) map[string]any {
	return map[string]any{
		"id": mt.ID, "shop_id": mt.ShopID,
		"name": mt.Name, "min_points": mt.MinPoints, "discount_percent": mt.DiscountPercent,
		"created_at": mt.CreatedAt, "updated_at": mt.UpdatedAt,
	}
}

// shopMemberPointsJSON serializes a shop_member's points/level (design D7).
// "level" is nested only when the MemberTier edge was eager-loaded
// (WithMemberTier) — both GetMemberPointsAdmin/GetMemberPointsSelf do this.
func shopMemberPointsJSON(sm *ent.ShopMember) map[string]any {
	out := map[string]any{
		"shop_member_id": sm.ID, "shop_id": sm.ShopID, "member_id": sm.MemberID,
		"points": sm.Points, "level_id": sm.LevelID, "level": nil,
	}
	if sm.Edges.MemberTier != nil {
		out["level"] = memberTierJSON(sm.Edges.MemberTier)
	}
	return out
}

func pointTransactionJSON(pt *ent.PointTransaction) map[string]any {
	return map[string]any{
		"id": pt.ID, "shop_member_id": pt.ShopMemberID, "order_id": pt.OrderID,
		"points_delta": pt.PointsDelta, "kind": pt.Kind, "reason": pt.Reason,
		"created_at": pt.CreatedAt,
	}
}

func transactionPageJSON(tp *points.TransactionPage) map[string]any {
	out := make([]map[string]any, 0, len(tp.Transactions))
	for _, pt := range tp.Transactions {
		out = append(out, pointTransactionJSON(pt))
	}
	return map[string]any{
		"transactions": out, "page": tp.Page, "page_size": tp.PageSize, "total": tp.Total,
	}
}

// ── member tiers CRUD (merchant back office) ───────────────────────────

func (h *PointsHandler) listMemberTiers(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	rows, err := h.Service.ListMemberTiers(r.Context(), shopID)
	if err != nil {
		h.Log.Error("list member tiers", "err", err)
		httpx.Internal(w)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, mt := range rows {
		out = append(out, memberTierJSON(mt))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"member_tiers": out})
}

func (h *PointsHandler) createMemberTier(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	var in points.MemberTierInput
	if !decodeJSON(w, r, &in) {
		return
	}
	mt, err := h.Service.CreateMemberTier(r.Context(), shopID, in)
	if err != nil {
		writePointsError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, memberTierJSON(mt))
}

func (h *PointsHandler) getMemberTier(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := intParam(r, "memberTierID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	mt, err := h.Service.GetMemberTier(r.Context(), shopID, id)
	if err != nil {
		writePointsError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, memberTierJSON(mt))
}

func (h *PointsHandler) updateMemberTier(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := intParam(r, "memberTierID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var in points.MemberTierUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	mt, err := h.Service.UpdateMemberTier(r.Context(), shopID, id, in)
	if err != nil {
		writePointsError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, memberTierJSON(mt))
}

func (h *PointsHandler) deleteMemberTier(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := intParam(r, "memberTierID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	if err := h.Service.DeleteMemberTier(r.Context(), shopID, id); err != nil {
		writePointsError(w, h.Log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── a shop member's points (merchant back office) ──────────────────────

func (h *PointsHandler) getMemberPointsAdmin(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	shopMemberID, ok := intParam(r, "shopMemberID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	sm, err := h.Service.GetMemberPointsAdmin(r.Context(), shopID, shopMemberID)
	if err != nil {
		writePointsError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, shopMemberPointsJSON(sm))
}

func (h *PointsHandler) listMemberTransactionsAdmin(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	shopMemberID, ok := intParam(r, "shopMemberID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	params := points.ListParams{
		Page:     paginationParam(r, "page"),
		PageSize: paginationParam(r, "page_size"),
	}
	tp, err := h.Service.ListTransactionsAdmin(r.Context(), shopID, shopMemberID, params)
	if err != nil {
		writePointsError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, transactionPageJSON(tp))
}

// adjustPointsRequest is the body of POST .../points/adjust (design D10):
// points_delta MUST be non-zero, reason MUST be non-empty.
type adjustPointsRequest struct {
	PointsDelta int32  `json:"points_delta"`
	Reason      string `json:"reason"`
}

func (h *PointsHandler) adjustPoints(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	shopMemberID, ok := intParam(r, "shopMemberID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var in adjustPointsRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	sm, err := h.Service.AdjustPoints(r.Context(), shopID, shopMemberID, in.PointsDelta, in.Reason)
	if err != nil {
		writePointsError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, shopMemberPointsJSON(sm))
}

// ── member self-service ────────────────────────────────────────────────

func (h *PointsHandler) getMinePoints(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	memberID, ok := memberFrom(w, r)
	if !ok {
		return
	}
	sm, err := h.Service.GetMemberPointsSelf(r.Context(), shopID, memberID)
	if err != nil {
		writePointsError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, shopMemberPointsJSON(sm))
}

func (h *PointsHandler) listMineTransactions(w http.ResponseWriter, r *http.Request) {
	shopID, ok := shopFrom(w, r)
	if !ok {
		return
	}
	memberID, ok := memberFrom(w, r)
	if !ok {
		return
	}
	params := points.ListParams{
		Page:     paginationParam(r, "page"),
		PageSize: paginationParam(r, "page_size"),
	}
	tp, err := h.Service.ListTransactionsSelf(r.Context(), shopID, memberID, params)
	if err != nil {
		writePointsError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, transactionPageJSON(tp))
}
