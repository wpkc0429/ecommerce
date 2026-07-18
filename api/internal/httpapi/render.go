package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/render"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// RenderHandler serves the storefront render bundle (tasks 7.1/7.2) and the
// authenticated working-copy preview (task 7.4).
type RenderHandler struct {
	Resolver  *tenant.Resolver
	Assembler *render.Assembler
	Cache     *render.Cache
	Issuer    *auth.TokenIssuer
	Engine    *rbac.Engine
	Log       *slog.Logger
}

// Page handles GET /api/v1/render/page?path=/... — the SSR (and native apps)
// pass the original storefront path; the site comes from X-Site-Domain or
// Host. Response: the design D8 bundle. X-Cache: HIT|MISS aids smoke tests.
func (h *RenderHandler) Page(w http.ResponseWriter, r *http.Request) {
	host := r.Header.Get("X-Site-Domain")
	if host == "" {
		host = r.Host
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	res, err := h.Resolver.Resolve(r.Context(), host, path)
	if err != nil {
		WriteResolveError(w, err)
		return
	}
	slug := render.NormalizeSlug(res.Path)
	// Tenant ctx: the data-layer interceptor scopes every query below.
	ctx := tenant.WithShopID(r.Context(), res.ShopID)

	raw, hit, err := h.Cache.GetPage(ctx, res.ShopID, slug, func(ctx2 context.Context) ([]byte, error) {
		b, aerr := h.Assembler.AssemblePublished(ctx2, res.ShopID, slug)
		if aerr != nil {
			return nil, aerr
		}
		return json.Marshal(b)
	})
	if err != nil {
		if errors.Is(err, render.ErrNotFound) {
			httpx.NotFound(w)
			return
		}
		h.Log.Error("render page", "err", err, "shop", res.ShopID, "slug", slug)
		httpx.Internal(w)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if hit {
		w.Header().Set("X-Cache", "HIT")
	} else {
		w.Header().Set("X-Cache", "MISS")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// Preview handles GET /api/v1/render/preview?token=... — outputs the working
// copy, never touches the cache, and verifies the token holder still has
// page.view on the shop (spec content-rendering/Authenticated preview).
func (h *RenderHandler) Preview(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	userID, shopID, slug, err := h.Issuer.VerifyPreview(token)
	if err != nil {
		httpx.Unauthorized(w)
		return
	}
	snap, err := h.Engine.Snapshot(r.Context(), userID, shopID)
	if err != nil {
		httpx.Internal(w)
		return
	}
	if !snap.CanAccessShop() || !snap.Allows("page.view") {
		httpx.Forbidden(w)
		return
	}
	ctx := tenant.WithShopID(r.Context(), shopID)
	bundle, err := h.Assembler.AssembleWorkingCopy(ctx, shopID, slug)
	if err != nil {
		if errors.Is(err, render.ErrNotFound) {
			httpx.NotFound(w)
			return
		}
		h.Log.Error("render preview", "err", err)
		httpx.Internal(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, bundle)
}
