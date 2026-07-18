package httpapi

import (
	"log/slog"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/site"
	"ksdevworks/ecommerce/api/internal/ent/siteshop"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// SitesHandler implements the platform-side domain binding API (task 5.4):
// sites CRUD plus site↔shop mappings. DB constraint violations (duplicate
// default shop, duplicate primary domain, duplicate prefix) surface as 422.
type SitesHandler struct {
	Client     *ent.Client
	Dispatcher *events.Dispatcher
	Log        *slog.Logger
	Authz      *AuthzMW
}

var (
	domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	prefixRe = regexp.MustCompile(`^/[a-z0-9-]+(/[a-z0-9-]+)*$`)
)

// MountPlatform registers routes under /admin (platform guard).
func (h *SitesHandler) MountPlatform(r chi.Router) {
	guard := h.Authz.RequirePlatformPermission("shop.manage_domains")
	r.With(guard).Get("/sites", h.list)
	r.With(guard).Post("/sites", h.create)
	r.With(guard).Delete("/sites/{siteID}", h.delete)
	r.With(guard).Post("/sites/{siteID}/shops", h.bindShop)
	r.With(guard).Put("/sites/{siteID}/shops/{shopID}", h.updateBinding)
	r.With(guard).Delete("/sites/{siteID}/shops/{shopID}", h.unbindShop)
}

func (h *SitesHandler) list(w http.ResponseWriter, r *http.Request) {
	sites, err := h.Client.Site.Query().Order(site.ByID()).All(r.Context())
	if err != nil {
		httpx.Internal(w)
		return
	}
	out := make([]map[string]any, 0, len(sites))
	for _, s := range sites {
		mappings, err := h.Client.SiteShop.Query().Where(siteshop.SiteIDEQ(s.ID)).All(r.Context())
		if err != nil {
			httpx.Internal(w)
			return
		}
		mm := make([]map[string]any, 0, len(mappings))
		for _, m := range mappings {
			mm = append(mm, map[string]any{
				"shop_id":     m.ShopID,
				"path_prefix": m.PathPrefix,
				"is_primary":  m.IsPrimary,
			})
		}
		out = append(out, map[string]any{
			"id": s.ID, "domain": s.Domain, "ssl_status": s.SslStatus, "shops": mm,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sites": out})
}

func (h *SitesHandler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	domain := tenant.NormalizeHost(req.Domain)
	if domain == "" || !domainRe.MatchString(domain) {
		httpx.Unprocessable(w, "invalid domain", []httpx.ValidationDetail{{Pointer: "/domain", Message: "must be a lowercase hostname"}})
		return
	}
	s, err := h.Client.Site.Create().SetDomain(domain).Save(r.Context())
	if ent.IsConstraintError(err) {
		httpx.Unprocessable(w, "domain already bound", []httpx.ValidationDetail{{Pointer: "/domain", Message: "domain already exists"}})
		return
	}
	if err != nil {
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"id": s.ID, "domain": s.Domain})
}

func (h *SitesHandler) delete(w http.ResponseWriter, r *http.Request) {
	siteID, ok := intParam(r, "siteID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	ctx := r.Context()
	s, err := h.Client.Site.Get(ctx, siteID)
	if err != nil {
		httpx.NotFound(w)
		return
	}
	shopIDs, _ := h.Client.SiteShop.Query().Where(siteshop.SiteIDEQ(siteID)).Select(siteshop.FieldShopID).Ints(ctx)

	tx, err := h.Client.Tx(ctx)
	if err != nil {
		httpx.Internal(w)
		return
	}
	_, err = tx.SiteShop.Delete().Where(siteshop.SiteIDEQ(siteID)).Exec(ctx)
	if err == nil {
		err = tx.Site.DeleteOneID(siteID).Exec(ctx)
	}
	if err != nil {
		_ = tx.Rollback()
		httpx.Internal(w)
		return
	}
	if err := tx.Commit(); err != nil {
		httpx.Internal(w)
		return
	}
	h.Dispatcher.Publish(ctx, events.SiteMappingChanged{Hosts: []string{s.Domain}, ShopIDs: shopIDs})
	w.WriteHeader(http.StatusNoContent)
}

func (h *SitesHandler) bindShop(w http.ResponseWriter, r *http.Request) {
	siteID, ok := intParam(r, "siteID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var req struct {
		ShopID     int     `json:"shop_id"`
		PathPrefix *string `json:"path_prefix"`
		IsPrimary  bool    `json:"is_primary"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	s, err := h.Client.Site.Get(ctx, siteID)
	if err != nil {
		httpx.NotFound(w)
		return
	}
	if _, err := h.Client.Shop.Get(ctx, req.ShopID); err != nil {
		httpx.Unprocessable(w, "shop does not exist", []httpx.ValidationDetail{{Pointer: "/shop_id", Message: "unknown shop"}})
		return
	}
	prefix, details := normalizePrefixInput(req.PathPrefix)
	if details != nil {
		httpx.Unprocessable(w, "invalid path_prefix", details)
		return
	}

	create := h.Client.SiteShop.Create().
		SetSiteID(siteID).
		SetShopID(req.ShopID).
		SetIsPrimary(req.IsPrimary)
	if prefix != nil {
		create.SetPathPrefix(*prefix)
	}
	if _, err := create.Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			// Duplicate default shop / duplicate prefix / duplicate primary
			// domain / duplicate pair — all constraint-backed (design D5).
			httpx.Unprocessable(w, "binding violates site-shop constraints", nil)
			return
		}
		httpx.Internal(w)
		return
	}
	h.Dispatcher.Publish(ctx, events.SiteMappingChanged{Hosts: []string{s.Domain}, ShopIDs: []int{req.ShopID}})
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"site_id": siteID, "shop_id": req.ShopID, "path_prefix": prefix, "is_primary": req.IsPrimary,
	})
}

func (h *SitesHandler) updateBinding(w http.ResponseWriter, r *http.Request) {
	siteID, ok1 := intParam(r, "siteID")
	shopID, ok2 := intParam(r, "shopID")
	if !ok1 || !ok2 {
		httpx.NotFound(w)
		return
	}
	var req struct {
		PathPrefix *string `json:"path_prefix"`
		IsPrimary  *bool   `json:"is_primary"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	s, err := h.Client.Site.Get(ctx, siteID)
	if err != nil {
		httpx.NotFound(w)
		return
	}
	row, err := h.Client.SiteShop.Query().
		Where(siteshop.SiteIDEQ(siteID), siteshop.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		httpx.NotFound(w)
		return
	}

	upd := row.Update()
	if req.PathPrefix != nil {
		prefix, details := normalizePrefixInput(req.PathPrefix)
		if details != nil {
			httpx.Unprocessable(w, "invalid path_prefix", details)
			return
		}
		if prefix == nil {
			upd.ClearPathPrefix()
		} else {
			upd.SetPathPrefix(*prefix)
		}
	}
	if req.IsPrimary != nil {
		upd.SetIsPrimary(*req.IsPrimary)
	}
	if _, err := upd.Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			httpx.Unprocessable(w, "binding violates site-shop constraints", nil)
			return
		}
		httpx.Internal(w)
		return
	}
	h.Dispatcher.Publish(ctx, events.SiteMappingChanged{Hosts: []string{s.Domain}, ShopIDs: []int{shopID}})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"site_id": siteID, "shop_id": shopID})
}

func (h *SitesHandler) unbindShop(w http.ResponseWriter, r *http.Request) {
	siteID, ok1 := intParam(r, "siteID")
	shopID, ok2 := intParam(r, "shopID")
	if !ok1 || !ok2 {
		httpx.NotFound(w)
		return
	}
	ctx := r.Context()
	s, err := h.Client.Site.Get(ctx, siteID)
	if err != nil {
		httpx.NotFound(w)
		return
	}
	n, err := h.Client.SiteShop.Delete().
		Where(siteshop.SiteIDEQ(siteID), siteshop.ShopIDEQ(shopID)).
		Exec(ctx)
	if err != nil {
		httpx.Internal(w)
		return
	}
	if n == 0 {
		httpx.NotFound(w)
		return
	}
	h.Dispatcher.Publish(ctx, events.SiteMappingChanged{Hosts: []string{s.Domain}, ShopIDs: []int{shopID}})
	w.WriteHeader(http.StatusNoContent)
}

// normalizePrefixInput validates and canonicalizes a path_prefix input.
// nil (or "" / "/") means "default shop" (stored as NULL).
func normalizePrefixInput(in *string) (*string, []httpx.ValidationDetail) {
	if in == nil {
		return nil, nil
	}
	norm := tenant.NormalizePathPrefix(*in)
	if norm == "" {
		return nil, nil
	}
	if !prefixRe.MatchString(norm) {
		return nil, []httpx.ValidationDetail{{Pointer: "/path_prefix", Message: "must match ^/[a-z0-9-]+$ segments"}}
	}
	return &norm, nil
}
