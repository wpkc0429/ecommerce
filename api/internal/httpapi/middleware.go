package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// OriginRegistry answers "is this origin registered by some shop?" — the only
// question a CORS preflight can ask, since an OPTIONS preflight carries no
// custom header values and therefore no tenant context (change
// storefront-client-auth, design D3). *tenant.Resolver implements it.
type OriginRegistry interface {
	IsRegisteredOrigin(ctx context.Context, origin string) bool
}

// NewCORSMiddleware grants browser access to the API. The allowed set is the
// union of two sources:
//
//	platform list (CORS_ALLOWED_ORIGINS) — the platform's own front ends: the
//	    admin SPA, the storefront dev server, any future platform-hosted
//	    multi-tenant SPA.
//	registry (shop_allowed_origins)      — merchant-owned front ends, registered
//	    per shop and effective immediately, with no env change or restart
//	    (change storefront-client-auth).
//
// Credentials are not used (Bearer tokens travel in headers), so no
// Access-Control-Allow-Credentials is emitted.
func NewCORSMiddleware(allowedOrigins []string, registry OriginRegistry) func(http.Handler) http.Handler {
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
			if origin != "" && (allowed[origin] ||
				(registry != nil && registry.IsRegisteredOrigin(r.Context(), origin))) {
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
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, "+
					PublishableKeyHeader+", "+IdempotencyKeyHeader)
				// Idempotency-Replayed is exposed so a storefront can verify its
				// own retry logic; without Expose-Headers, JS cannot read it and
				// the header's entire diagnostic value is lost.
				h.Set("Access-Control-Expose-Headers", "X-Cache, "+IdempotencyReplayedHeader)
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
func NewAdminMiddleware(issuer *auth.TokenIssuer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid, sids, err := issuer.VerifyAdmin(bearerToken(r))
			if err != nil {
				httpx.Unauthorized(w)
				return
			}
			ctx := auth.WithAdminIdentity(r.Context(), auth.AdminIdentity{UserID: uid, SIDs: sids})
			next.ServeHTTP(w, r.WithContext(ctx))
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
