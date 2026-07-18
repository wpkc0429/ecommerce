package httpapi

import (
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

// NewCORSMiddleware allows the admin SPA (and storefront dev) origins to call
// the API from the browser. Origins come from config; credentials are not
// used (Bearer tokens travel in headers).
func NewCORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[strings.TrimSuffix(strings.TrimSpace(o), "/")] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && allowed[origin] {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Vary", "Origin")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Site-Domain, X-Site-Path")
				h.Set("Access-Control-Expose-Headers", "X-Cache")
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
