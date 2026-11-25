package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/design"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
)

// DesignCredentialsHandler issues and revokes the AI edit service's per-shop
// credentials (change design-history D8). Platform level only
// (design_service.manage, held by super_admin): minting a machine identity is
// an integration action, so no merchant role can do it.
type DesignCredentialsHandler struct {
	Credentials *design.Credentials
	Authz       *AuthzMW
	Log         *slog.Logger
}

// MountPlatform mounts the routes under /admin.
func (h *DesignCredentialsHandler) MountPlatform(r chi.Router) {
	manage := h.Authz.RequirePlatformPermission(design.PermServiceManage)
	g := r.With(designNoSniff, manage)
	g.Get("/design-service/credentials", h.list)
	g.Post("/design-service/credentials", h.issue)
	g.Post("/design-service/credentials/{credentialID}/revoke", h.revoke)
	g.Post("/design-service/credentials/{credentialID}/rotate", h.rotate)
}

func credentialJSON(c *ent.DesignServiceCredential) map[string]any {
	cidrs := c.AllowedCidrs
	if cidrs == nil {
		cidrs = []string{}
	}
	return map[string]any{
		"id": c.ID, "shop_id": c.ShopID, "name": c.Name, "key_prefix": c.KeyPrefix, "allowed_cidrs": cidrs,
		"created_by_user_id": c.CreatedByUserID, "created_at": c.CreatedAt.UTC(), "expires_at": c.ExpiresAt.UTC(),
		"last_used_at": ts(c.LastUsedAt), "revoked_at": ts(c.RevokedAt),
	}
}

func (h *DesignCredentialsHandler) audit(action string, c *ent.DesignServiceCredential, actor int) {
	// ids only: never the key, never the name
	h.Log.Info("design credential audit", "action", action, "credential_id", c.ID, "shop_id", c.ShopID, "actor_user_id", actor)
}

func (h *DesignCredentialsHandler) list(w http.ResponseWriter, r *http.Request) {
	shopID, err := strconv.Atoi(r.URL.Query().Get("shop_id"))
	if err != nil || shopID <= 0 {
		httpx.Unprocessable(w, "shop_id is required", nil)
		return
	}
	rows, err := h.Credentials.List(r.Context(), shopID)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		out = append(out, credentialJSON(c))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"credentials": out})
}

func (h *DesignCredentialsHandler) issue(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ShopID      int      `json:"shop_id"`
		Name        string   `json:"name"`
		AllowedCIDR []string `json:"allowed_cidrs"`
		ExpiresDays int      `json:"expires_in_days"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	user, _ := auth.AdminFrom(r.Context())
	iss, err := h.Credentials.Issue(r.Context(), design.IssueInput{
		ShopID: b.ShopID, Name: b.Name, CIDRs: b.AllowedCIDR, CreatedByID: user.UserID,
		TTL: time.Duration(b.ExpiresDays) * 24 * time.Hour,
	})
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	h.audit("issue", iss.Credential, user.UserID)
	h.writeIssued(w, http.StatusCreated, iss)
}

// writeIssued is the only response that ever carries the key.
func (h *DesignCredentialsHandler) writeIssued(w http.ResponseWriter, status int, iss *design.Issued) {
	w.Header().Set("Cache-Control", "no-store")
	out := credentialJSON(iss.Credential)
	out["key"] = iss.Key
	httpx.WriteJSON(w, status, out)
}

func (h *DesignCredentialsHandler) revoke(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "credentialID"))
	if err != nil || id <= 0 {
		httpx.NotFound(w)
		return
	}
	user, _ := auth.AdminFrom(r.Context())
	c, err := h.Credentials.Revoke(r.Context(), id)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	h.audit("revoke", c, user.UserID)
	httpx.WriteJSON(w, http.StatusOK, credentialJSON(c))
}

func (h *DesignCredentialsHandler) rotate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "credentialID"))
	if err != nil || id <= 0 {
		httpx.NotFound(w)
		return
	}
	user, _ := auth.AdminFrom(r.Context())
	iss, err := h.Credentials.Rotate(r.Context(), id, user.UserID)
	if err != nil {
		writeDesignError(w, h.Log, err)
		return
	}
	h.audit("rotate", iss.Credential, user.UserID)
	h.writeIssued(w, http.StatusCreated, iss)
}
