package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/design"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
)

// Version preview routes (design D25). Human side: design.view, mounted in
// DesignAdminHandler; machine side: list, claim, ready, failed in
// DesignServiceHandler. There is no machine route that creates one.

func previewIDParam(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "previewID"))
	return id, err == nil
}

// versionPreviewJSON is the descriptor. read_only is always true: the core
// disables every writing ctx.action in preview mode. origin is included only
// when withOrigin is set (the initiator, status ready and not expired); for
// everyone else the key is absent.
func versionPreviewJSON(p *ent.DesignVersionPreview, now time.Time, withOrigin bool) map[string]any {
	status := design.EffectiveVPStatus(p, now)
	m := map[string]any{
		"id": p.ID.String(), "site": p.Site, "target_request_id": p.TargetRequestID.String(), "target_tag": p.TargetTag,
		"status": status, "failure_code": p.FailureCode, "failure_detail": p.FailureDetail,
		"failure_rule_ids": p.FailureRuleIds, "core_version": p.CoreVersion, "design_floor_version": p.DesignFloorVersion,
		"created_at": p.CreatedAt.UTC(), "ready_at": ts(p.ReadyAt), "expires_at": p.ExpiresAt.UTC(), "read_only": true,
	}
	if withOrigin && status == design.VPReady && p.PreviewOrigin != nil {
		m["origin"] = *p.PreviewOrigin
	}
	return m
}

func (h *DesignAdminHandler) versionPreview(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := requestIDParam(r)
	if !ok {
		httpx.NotFound(w)
		return
	}
	user, _ := auth.AdminFrom(r.Context())
	row, err := h.Service.CreateVersionPreview(r.Context(), shopID, id, user.UserID)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusAccepted, versionPreviewJSON(row, h.Service.Clock(), false))
}

func (h *DesignAdminHandler) getVersionPreview(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	id, ok := previewIDParam(r)
	if !ok {
		httpx.NotFound(w)
		return
	}
	row, err := h.Service.GetVersionPreview(r.Context(), shopID, id)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	user, _ := auth.AdminFrom(r.Context())
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, versionPreviewJSON(row, h.Service.Clock(), row.RequestedByUserID == user.UserID))
}

// serviceVersionPreviewJSON is what the service reads back: no requester, no
// origin (it knows its own).
func serviceVersionPreviewJSON(p *ent.DesignVersionPreview) map[string]any {
	return map[string]any{
		"id": p.ID.String(), "site": p.Site, "target_request_id": p.TargetRequestID.String(), "target_tag": p.TargetTag,
		"status": p.Status, "core_version": p.CoreVersion, "design_floor_version": p.DesignFloorVersion,
		"created_at": p.CreatedAt.UTC(), "expires_at": p.ExpiresAt.UTC(), "claim_expires_at": ts(p.ClaimExpiresAt),
		"updated_at": p.UpdatedAt.UTC(), "read_only": true,
	}
}

func (h *DesignServiceHandler) vpList(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	q := r.URL.Query()
	var since *time.Time
	if v := q.Get("updated_since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			httpx.Unprocessable(w, "updated_since must be an RFC 3339 time", nil)
			return
		}
		since = &t
	}
	rows, err := h.Service.ListVersionPreviews(r.Context(), shopID, q.Get("status"), since, atoiOr(q.Get("limit"), 50))
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, serviceVersionPreviewJSON(row))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"version_previews": out})
}

func (h *DesignServiceHandler) vpDo(w http.ResponseWriter, r *http.Request, fn func(shopID int, id uuid.UUID) (*ent.DesignVersionPreview, error)) {
	shopID, _ := shopIDParam(r)
	id, ok := previewIDParam(r)
	if !ok {
		httpx.NotFound(w)
		return
	}
	row, err := fn(shopID, id)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, serviceVersionPreviewJSON(row))
}

func (h *DesignServiceHandler) vpClaim(w http.ResponseWriter, r *http.Request) {
	h.vpDo(w, r, func(shopID int, id uuid.UUID) (*ent.DesignVersionPreview, error) {
		return h.Service.ClaimVersionPreview(r.Context(), shopID, id, h.cred(r))
	})
}

func (h *DesignServiceHandler) vpReady(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Origin    string    `json:"origin"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	h.vpDo(w, r, func(shopID int, id uuid.UUID) (*ent.DesignVersionPreview, error) {
		return h.Service.ReadyVersionPreview(r.Context(), shopID, id, h.cred(r), b.Origin, b.ExpiresAt)
	})
}

func (h *DesignServiceHandler) vpFailed(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Code    string   `json:"code"`
		Detail  string   `json:"detail"`
		RuleIDs []string `json:"rule_ids"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	h.vpDo(w, r, func(shopID int, id uuid.UUID) (*ent.DesignVersionPreview, error) {
		return h.Service.FailVersionPreview(r.Context(), shopID, id, h.cred(r), b.Code, b.Detail, b.RuleIDs)
	})
}
