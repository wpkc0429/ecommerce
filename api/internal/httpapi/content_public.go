package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/content"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/render"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// PreviewTokenHeader carries a content preview token. It is a header, never a
// query parameter: query strings end up in access logs and Referer headers.
const PreviewTokenHeader = "X-Preview-Token"

// ContentPublicHandler serves the published content of a shop to its
// storefront (change headless-content-store slice 2, design D2). The shop comes
// from X-Publishable-Key (TenantMW); the key is an identifier, not a
// credential, so nothing here is access control: only published content is
// ever returned, and "does not exist" and "not published" are the same 404.
type ContentPublicHandler struct {
	Reader *content.Reader
	Cache  *render.Cache
	Issuer *auth.TokenIssuer
	Engine *rbac.Engine
	// MaxAge is the Cache-Control max-age in seconds (CONTENT_PUBLIC_MAX_AGE).
	MaxAge int
	Log    *slog.Logger
}

// MountPublic mounts the routes inside the tenant-resolved /shop group.
func (h *ContentPublicHandler) MountPublic(r chi.Router, rate func(http.Handler) http.Handler) {
	r = r.With(orPassthrough(rate))
	r.Get("/content", h.list)
	r.Get("/content/{key}", h.get)
	r.Get("/content/{key}/preview", h.preview)
}

// NewContentPublicRateLimit limits the public content endpoints per shop and
// source IP. ETag and caching make a generous default (120 per minute) safe.
func NewContentPublicRateLimit(l *ratelimit.Limiter, limit int, window time.Duration) func(http.Handler) http.Handler {
	return newAuthRateLimitMW(l, []rateLimitRule{shopIPRule("content_public", limit, window)})
}

func (h *ContentPublicHandler) cacheControl() string {
	if h.MaxAge <= 0 {
		return "public, max-age=0, must-revalidate"
	}
	return fmt.Sprintf("public, max-age=%d, stale-while-revalidate=300", h.MaxAge)
}

func addVary(w http.ResponseWriter, names ...string) {
	have := map[string]bool{}
	for _, v := range w.Header().Values("Vary") {
		for _, p := range strings.Split(v, ",") {
			have[strings.ToLower(strings.TrimSpace(p))] = true
		}
	}
	for _, n := range names {
		if !have[strings.ToLower(n)] {
			w.Header().Add("Vary", n)
		}
	}
}

// etagMatches implements the weak comparison of If-None-Match.
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, p := range strings.Split(header, ",") {
		p = strings.TrimSpace(p)
		if p == "*" || strings.TrimPrefix(p, "W/") == etag {
			return true
		}
	}
	return false
}

// serve writes a public body with its validators; 304 when the caller's
// If-None-Match still matches.
func (h *ContentPublicHandler) serve(w http.ResponseWriter, r *http.Request, body []byte, hit bool) {
	etag := content.ETagOf(body)
	hd := w.Header()
	hd.Set("ETag", etag)
	hd.Set("Cache-Control", h.cacheControl())
	addVary(w, PublishableKeyHeader, "Origin")
	if hit {
		hd.Set("X-Cache", "HIT")
	} else {
		hd.Set("X-Cache", "MISS")
	}
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	hd.Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *ContentPublicHandler) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, content.ErrUnavailable) {
		// One response for every "no such published document" case.
		httpx.NotFound(w)
		return
	}
	h.Log.Error("content public", "err", err)
	httpx.Internal(w)
}

func (h *ContentPublicHandler) list(w http.ResponseWriter, r *http.Request) {
	shopID, ok := tenant.ShopID(r.Context())
	if !ok {
		httpx.NotFound(w)
		return
	}
	include := r.URL.Query().Get("include") == "content"
	var keys []string
	if raw := r.URL.Query().Get("keys"); raw != "" {
		for _, k := range strings.Split(raw, ",") {
			if k = strings.TrimSpace(k); k != "" {
				keys = append(keys, k)
			}
		}
		if len(keys) > 50 {
			httpx.BadRequest(w, "at most 50 keys")
			return
		}
	}
	name := render.ContentListName
	if include {
		name = render.ContentAllName
	}
	assemble := func(ctx context.Context) ([]byte, error) { return h.Reader.ListBody(ctx, shopID, include, keys) }
	if len(keys) > 0 {
		// A filtered list is not cached under the shared names.
		body, err := assemble(r.Context())
		if err != nil {
			h.fail(w, err)
			return
		}
		h.serve(w, r, body, false)
		return
	}
	body, hit, err := h.Cache.GetContent(r.Context(), shopID, name, assemble)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.serve(w, r, body, hit)
}

func (h *ContentPublicHandler) get(w http.ResponseWriter, r *http.Request) {
	shopID, ok := tenant.ShopID(r.Context())
	if !ok {
		httpx.NotFound(w)
		return
	}
	key := chi.URLParam(r, "key")
	body, hit, err := h.Cache.GetContent(r.Context(), shopID, key, func(ctx context.Context) ([]byte, error) {
		return h.Reader.DocumentBody(ctx, shopID, key)
	})
	if err != nil {
		h.fail(w, err)
		return
	}
	h.serve(w, r, body, hit)
}

// preview serves the DRAFT of a document to the holder of a preview token. The
// token is re-checked against the holder's current permission on every use, so
// removing someone's access ends their previews at once.
func (h *ContentPublicHandler) preview(w http.ResponseWriter, r *http.Request) {
	shopID, ok := tenant.ShopID(r.Context())
	if !ok {
		httpx.NotFound(w)
		return
	}
	claims, userID, err := h.Issuer.VerifyContentPreview(r.Header.Get(PreviewTokenHeader))
	if err != nil || claims.ShopID != shopID {
		httpx.Unauthorized(w)
		return
	}
	key := chi.URLParam(r, "key")
	if claims.Scope == auth.PreviewScopeDocument && claims.Key != key {
		httpx.Unauthorized(w)
		return
	}
	snap, err := h.Engine.Snapshot(r.Context(), userID, shopID)
	if err != nil {
		httpx.Internal(w)
		return
	}
	if !snap.CanAccessShop() || (!snap.Allows("content.view") && !snap.Allows("page.view")) {
		httpx.Forbidden(w)
		return
	}
	body, err := h.Reader.PreviewBody(r.Context(), shopID, key)
	if err != nil {
		h.fail(w, err)
		return
	}
	hd := w.Header()
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Robots-Tag", "noindex, nofollow")
	hd.Set("Content-Type", "application/json; charset=utf-8")
	addVary(w, PublishableKeyHeader, "Origin", PreviewTokenHeader)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
