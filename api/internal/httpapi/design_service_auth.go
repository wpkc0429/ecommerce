package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"ksdevworks/ecommerce/api/internal/design"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/ratelimit"
)

type designCredKey struct{}

// designCredentialFrom returns the authenticated service credential.
func designCredentialFrom(ctx context.Context) (*ent.DesignServiceCredential, bool) {
	c, ok := ctx.Value(designCredKey{}).(*ent.DesignServiceCredential)
	return c, ok
}

// Limits of the machine route family (design D8): per credential and minute.
const (
	designServiceWriteLimit  = 120
	designServiceUploadLimit = 60
	designServiceAuthFails   = 30 // failed attempts per client IP and minute
	designServiceWindow      = time.Minute
)

// NewDesignServiceAuth authenticates the AI edit service: Authorization:
// Bearer dsk_..., looked up by hash; the URL's {shopID} must be the
// credential's shop. Every failure is the same 401 (the key is wrong, expired,
// revoked or outside its CIDR: the caller cannot tell which) and a mismatched
// shop is 404 (it must not reveal that the shop exists). Wrong keys only count
// against a per-IP limit and leave a WARN without the key.
//
// This runs on /api/v1/design-service, which is deliberately NOT under /admin:
// the admin group's middleware (user JWT, RBAC, Cloudflare Access, host
// binding) does not apply to a machine, and the credential does not
// authenticate there.
func NewDesignServiceAuth(creds *design.Credentials, limiter *ratelimit.Limiter, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			key := bearerToken(r)
			cred, ok, err := creds.Authenticate(r.Context(), key, design.ClientAddr(ip))
			if err != nil {
				log.Error("design service auth", "err_class", "internal")
				httpx.Internal(w)
				return
			}
			if !ok {
				if limiter != nil {
					rk := "design_service_authfail:" + ip
					if res := limiter.Allow(r.Context(), rk, designServiceAuthFails, designServiceWindow); !res.Allowed {
						httpx.TooManyRequests(w, res.RetryAfter)
						return
					}
				}
				log.Warn("design service: credential rejected", "client_ip", ip)
				httpx.Unauthorized(w)
				return
			}
			if shopID, valid := shopIDParam(r); !valid || shopID != cred.ShopID {
				httpx.NotFound(w)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), designCredKey{}, cred)))
		})
	}
}

// NewDesignServiceRateLimit limits writes per credential. uploads (slice 2)
// will get their own, tighter limit.
func NewDesignServiceRateLimit(l *ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if l == nil || r.Method == http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}
			cred, ok := designCredentialFrom(r.Context())
			if !ok {
				httpx.Unauthorized(w)
				return
			}
			key, limit := fmt.Sprintf("design_service_write:%d", cred.ID), designServiceWriteLimit
			if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/screenshots/") {
				// Uploads are heavier (decode + re-encode): their own, tighter budget.
				key, limit = fmt.Sprintf("design_service_upload:%d", cred.ID), designServiceUploadLimit
			}
			if res := l.Allow(r.Context(), key, limit, designServiceWindow); !res.Allowed {
				httpx.TooManyRequests(w, res.RetryAfter)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
