package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
)

// ShopsHandler implements the platform-side shop management API (change
// platform-shop-crud): create/query/list/update, all gated by platform-scope
// permissions (design D1 — reuses the existing three-tier RBAC decision via
// AuthzMW.RequirePlatformPermission, no separate authorization path). Domain
// binding (site_shop) stays entirely in SitesHandler — this handler only
// touches the shops table.
type ShopsHandler struct {
	Client  *ent.Client
	Service *cms.Service
	Authz   *AuthzMW
	Log     *slog.Logger
}

// MountPlatform registers routes under /admin (platform guard).
func (h *ShopsHandler) MountPlatform(r chi.Router) {
	r.With(h.Authz.RequirePlatformPermission("shop.create")).Post("/shops", h.create)
	r.With(h.Authz.RequirePlatformPermission("shop.list")).Get("/shops", h.list)
	r.With(h.Authz.RequirePlatformPermission("shop.view")).Get("/shops/{shopID}", h.get)
	r.With(h.Authz.RequirePlatformPermission("shop.update")).Put("/shops/{shopID}", h.update)
}

func shopJSON(s *ent.Shop) map[string]any {
	return map[string]any{
		"id":           s.ID,
		"name":         s.Name,
		"theme_id":     s.ThemeID,
		"status":       s.Status,
		"content_json": s.ContentJSON,
		"meta":         s.Meta,
		"updated_at":   s.UpdatedAt,
	}
}

func (h *ShopsHandler) create(w http.ResponseWriter, r *http.Request) {
	var in cms.ShopInput
	if !decodeJSON(w, r, &in) {
		return
	}
	s, err := h.Service.CreateShop(r.Context(), in)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, shopJSON(s))
}

// paginationParam reads a positive-int query param; a missing or
// unparseable value falls back to 0 (treated by the service layer as "not
// provided" — design D3's tolerant-input rule, never a 400).
func paginationParam(r *http.Request, name string) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

func (h *ShopsHandler) list(w http.ResponseWriter, r *http.Request) {
	params := cms.ShopListParams{
		Page:     paginationParam(r, "page"),
		PageSize: paginationParam(r, "page_size"),
	}
	sp, err := h.Service.ListShops(r.Context(), params)
	if err != nil {
		h.Log.Error("list shops", "err", err)
		httpx.Internal(w)
		return
	}
	out := make([]map[string]any, 0, len(sp.Shops))
	for _, s := range sp.Shops {
		out = append(out, shopJSON(s))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"shops": out, "page": sp.Page, "page_size": sp.PageSize, "total": sp.Total,
	})
}

func (h *ShopsHandler) get(w http.ResponseWriter, r *http.Request) {
	shopID, ok := intParam(r, "shopID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	s, err := h.Service.GetShop(r.Context(), shopID)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, shopJSON(s))
}

func (h *ShopsHandler) update(w http.ResponseWriter, r *http.Request) {
	shopID, ok := intParam(r, "shopID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var in cms.ShopUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	s, err := h.Service.UpdateShop(r.Context(), shopID, in)
	if err != nil {
		writeCMSError(w, h.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, shopJSON(s))
}
