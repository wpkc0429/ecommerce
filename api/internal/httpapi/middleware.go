package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/previeworigin"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// OriginRegistry answers "which shop registered this origin, and for what?" -
// the only question a CORS preflight can ask, since an OPTIONS preflight
// carries no custom header values and therefore no tenant context (change
// storefront-client-auth, design D3; purpose added by admin-api-public-exposure
// D3). *tenant.Resolver implements it.
type OriginRegistry interface {
	LookupOrigin(ctx context.Context, origin string) (shopID int, purpose string, ok bool)
}

// adminPathKind classifies a request path for CORS and host binding (change
// admin-api-public-exposure, design D3).
type adminPathKind int

const (
	// pathNonAdmin: anything outside /api/v1/admin.
	pathNonAdmin adminPathKind = iota
	// pathAdminShop: /api/v1/admin/shops/{id}/... - one shop's back office.
	pathAdminShop
	// pathAdminShared: auth/*, me, branding, invitations/accept, media/config -
	// reachable from any shop's admin front end.
	pathAdminShared
	// pathAdminPlatform: every other admin path (including /shops and
	// /shops/{id} themselves, which are platform-scoped routes).
	pathAdminPlatform
)

const adminPrefix = "/api/v1/admin"

// classifyAdminPath is the pure path classifier behind the per-purpose CORS
// split and the admin host binding. It runs before chi routing (CORS) or at
// group level (binding), so it cannot use URL params: the shop id is parsed
// from the path text with the same strconv.Atoi the router-side shopIDParam
// uses, and anything that does not parse is treated as platform-level - the
// strictest class.
func classifyAdminPath(path string) (adminPathKind, int) {
	rest, ok := strings.CutPrefix(path, adminPrefix)
	if !ok || (rest != "" && rest[0] != '/') {
		return pathNonAdmin, 0
	}
	segs := strings.Split(rest, "/") // "" , seg1, seg2, ...
	if len(segs) < 2 {
		return pathAdminPlatform, 0
	}
	switch segs[1] {
	case "shops":
		if len(segs) >= 4 && segs[3] != "" {
			if id, err := strconv.Atoi(segs[2]); err == nil && id > 0 {
				return pathAdminShop, id
			}
		}
		return pathAdminPlatform, 0
	case "auth":
		if len(segs) >= 3 && segs[2] != "" {
			return pathAdminShared, 0
		}
	case "me", "branding":
		if len(segs) == 2 {
			return pathAdminShared, 0
		}
	case "invitations":
		if len(segs) == 3 && segs[2] == "accept" {
			return pathAdminShared, 0
		}
	case "media":
		if len(segs) == 3 && segs[2] == "config" {
			return pathAdminShared, 0
		}
	}
	return pathAdminPlatform, 0
}

// NewCORSMiddleware grants browser access to the API. The allowed set depends
// on the request PATH (change admin-api-public-exposure, design D3):
//
//	/api/v1/admin/shops/{id}/...   platform list + admin origins of shop {id}
//	admin shared (auth, me, ...)   platform list + any admin origin
//	other /api/v1/admin/...        platform list only
//	everything else                platform list + storefront origins (as before)
//
// A storefront origin therefore never gains CORS on the back-office API. The
// platform list (CORS_ALLOWED_ORIGINS) is the platform's own front ends; the
// registry (shop_allowed_origins) is merchant-owned front ends, effective
// immediately with no env change or restart.
//
// CORS is not authorization (curl ignores it): it stops other sites' scripts
// from reading responses. The real boundary is Access + RBAC + host binding.
//
// Credentials are not used (Bearer tokens travel in headers), so no
// Access-Control-Allow-Credentials is emitted.
func NewCORSMiddleware(allowedOrigins []string, registry OriginRegistry, opts ...CORSOption) func(http.Handler) http.Handler {
	var co corsOptions
	for _, o := range opts {
		o(&co)
	}
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[strings.TrimSuffix(strings.TrimSpace(o), "/")] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			// The platform list short-circuits first: admin/web traffic never
			// reaches the registry lookup, and server-to-server requests carry
			// no Origin at all.
			if origin != "" && corsAllows(r, origin, allowed, registry, co) {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Vary", "Origin")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				// X-Publishable-Key MUST be listed or browsers reject the
				// actual request after the preflight. X-Site-Domain /
				// X-Site-Path are gone: the only endpoint that still reads
				// X-Site-Domain is called server-to-server by the SSR, never
				// from a browser.
				//
				// Idempotency-Key carries the same MUST (change
				// checkout-idempotency): a browser that is not told the header
				// is allowed fails the actual checkout after a successful
				// preflight, and the symptom reads as "checkout broke when we
				// added retries".
				//
				// X-Admin-Refresh (change admin-api-public-exposure D5) is the
				// CSRF guard of cookie-mode refresh: a custom header forces a
				// cross-origin request through a preflight this middleware
				// answers only for the shop's own admin origin.
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, "+
					PublishableKeyHeader+", "+IdempotencyKeyHeader+", "+AdminRefreshHeader+", "+
					PreviewTokenHeader+", If-None-Match, If-Match")
				// Idempotency-Replayed is exposed so a storefront can verify its
				// own retry logic; without Expose-Headers, JS cannot read it and
				// the header's entire diagnostic value is lost.
				//
				// ETag is exposed for the public content API (change
				// headless-content-store slice 2): a browser cannot read it
				// otherwise, and X-Preview-Token / If-None-Match above are
				// request headers a cross-origin fetch must be allowed to send.
				h.Set("Access-Control-Expose-Headers", "X-Cache, ETag, "+IdempotencyReplayedHeader)
				h.Set("Access-Control-Max-Age", "600")
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CORSOption customizes NewCORSMiddleware.
type CORSOption func(*corsOptions)

type corsOptions struct {
	websiteOrigin string
	preview       *previeworigin.Set
}

// WithPreviewOrigins grants preview origins CORS on the shop-scoped and shared
// admin paths only (never platform-level paths, never as an admin-purpose
// registered origin). Read-only is enforced separately, on the server.
func WithPreviewOrigins(set *previeworigin.Set) CORSOption {
	return func(o *corsOptions) { o.preview = set }
}

// WithWebsiteOrigin names the marketing website's origin (WEBSITE_URL). It is
// the ONLY origin granted CORS on the public application API
// (/api/v1/public/*), and it is granted nothing anywhere else (change
// platform-website, design D7).
func WithWebsiteOrigin(origin string) CORSOption {
	return func(o *corsOptions) { o.websiteOrigin = strings.TrimSuffix(strings.TrimSpace(origin), "/") }
}

const publicPrefix = "/api/v1/public/"

// corsAllows decides one request. The public application API is a closed
// world of its own: the platform's front ends and merchants' registered
// origins get nothing there, the website gets nothing elsewhere.
func corsAllows(r *http.Request, origin string, allowed map[string]bool, registry OriginRegistry, co corsOptions) bool {
	if strings.HasPrefix(r.URL.Path, publicPrefix) {
		return co.websiteOrigin != "" && origin == co.websiteOrigin
	}
	if _, isPreview := co.preview.Match(origin); isPreview {
		kind, _ := classifyAdminPath(r.URL.Path)
		switch kind {
		case pathAdminShop, pathAdminShared:
			return true
		case pathAdminPlatform:
			return false
		}
		// non-admin paths: only a storefront registration can allow it
		return registryAllows(r, registry, origin)
	}
	return allowed[origin] || registryAllows(r, registry, origin)
}

// registryAllows decides a registered (non-platform) origin by path class.
func registryAllows(r *http.Request, registry OriginRegistry, origin string) bool {
	if registry == nil {
		return false
	}
	kind, pathShop := classifyAdminPath(r.URL.Path)
	if kind == pathAdminPlatform {
		return false // platform-level admin paths: platform list only
	}
	shopID, purpose, ok := registry.LookupOrigin(r.Context(), origin)
	if !ok {
		return false
	}
	switch kind {
	case pathAdminShop:
		return purpose == tenant.PurposeAdmin && shopID == pathShop
	case pathAdminShared:
		return purpose == tenant.PurposeAdmin
	default:
		return purpose == tenant.PurposeStorefront
	}
}

// decodeJSON reads a JSON body (1 MiB cap) into dst; on failure writes a 400
// and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		httpx.BadRequest(w, "invalid JSON body")
		return false
	}
	return true
}

// bearerToken extracts the Authorization: Bearer credential.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// NewAdminMiddleware authenticates back-office requests with admin JWTs
// (aud=admin). Member tokens fail here by signature and audience.
//
// With WithPreviewSessions it also accepts read-only preview tokens
// (aud=admin-preview, scope=preview_ro), but only from the exact preview
// origin they are bound to and only for requests the read-only policy allows;
// and it refuses an ordinary admin token presented from a preview origin.
func NewAdminMiddleware(issuer *auth.TokenIssuer, opts ...AdminMWOption) func(http.Handler) http.Handler {
	var cfg adminMW
	for _, o := range opts {
		o(&cfg)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := bearerToken(r)
			if uid, sids, err := issuer.VerifyAdmin(tok); err == nil {
				if _, fromPreview := requestPreviewOrigin(cfg.preview, r); fromPreview {
					// Token confusion: an ordinary session is not valid from a preview origin.
					writePreviewReadOnly(w)
					return
				}
				ctx := auth.WithAdminIdentity(r.Context(), auth.AdminIdentity{UserID: uid, SIDs: sids})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			if cfg.preview.Enabled() {
				if claims, uid, err := issuer.VerifyAdminPreview(tok); err == nil {
					origin, fromPreview := requestPreviewOrigin(cfg.preview, r)
					if !fromPreview || origin != claims.Origin {
						// A read-only token only works from the origin it is bound to.
						httpx.Unauthorized(w)
						return
					}
					if previewBlocked(r) {
						if cfg.log != nil {
							cfg.log.Warn("admin_preview_denied", "user_id", uid,
								"method", r.Method, "path", r.URL.Path, "origin_tag", auth.OriginTag(origin))
						}
						writePreviewReadOnly(w)
						return
					}
					ctx := auth.WithAdminIdentity(r.Context(), auth.AdminIdentity{UserID: uid, SIDs: claims.SIDs, PreviewRO: true})
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			httpx.Unauthorized(w)
		})
	}
}

// NewMemberMiddleware authenticates storefront member requests. It requires
// the tenant middleware to have resolved the shop first: the token audience
// must equal shop:{resolved shop} — cross-shop reuse → 401.
func NewMemberMiddleware(issuer *auth.TokenIssuer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			shopID, ok := tenant.ShopID(r.Context())
			if !ok {
				httpx.Unauthorized(w)
				return
			}
			mid, err := issuer.VerifyMember(bearerToken(r), shopID)
			if err != nil {
				httpx.Unauthorized(w)
				return
			}
			ctx := auth.WithMemberID(r.Context(), mid)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// NewOrderAccessMiddleware authenticates the guest order routes with an order
// access token (change guest-checkout, design D3/D4). Like the member
// middleware it requires the tenant middleware to have resolved the shop first:
// the token audience must equal order:{resolved shop}, so a token minted by one
// shop is inert at another.
//
// It puts only the order id the token names into the context — never a member
// id. Whatever this token can reach is therefore limited to that single order
// by construction, and a member-only handler reached with one of these tokens
// finds no member identity and answers 401.
func NewOrderAccessMiddleware(issuer *auth.TokenIssuer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			shopID, ok := tenant.ShopID(r.Context())
			if !ok {
				httpx.Unauthorized(w)
				return
			}
			orderID, err := issuer.VerifyOrderAccess(bearerToken(r), shopID)
			if err != nil {
				httpx.Unauthorized(w)
				return
			}
			ctx := auth.WithOrderAccess(r.Context(), orderID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// requestLogger emits one structured line per request.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			log.Info("http_request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
				"client_ip", clientIP(r),
				"remote", r.RemoteAddr,
			)
		})
	}
}

// recoverer converts panics into the unified 500 envelope instead of a bare crash.
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel comparison per net/http docs
						panic(rec)
					}
					log.Error("panic recovered",
						"panic", rec,
						"path", r.URL.Path,
						"stack", string(debug.Stack()),
					)
					httpx.Internal(w)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
