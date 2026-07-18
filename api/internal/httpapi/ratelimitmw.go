package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// rateLimitRule pairs a quota with the key it applies to (design
// auth-rate-limiting D2). A rule whose keyFn returns ok=false is skipped for
// that request — e.g. the tight per-email rule when the body has no
// parseable email, or a shop-scoped rule outside tenant context.
type rateLimitRule struct {
	limit  int
	window time.Duration
	keyFn  func(r *http.Request) (key string, ok bool)
}

// newAuthRateLimitMW builds a chi-compatible middleware that evaluates every
// rule and rejects (429, Retry-After = the longest wait among violated
// rules) if any rule's quota is exceeded. All rules still run even after one
// is violated so the response reports the true wait time, not just the
// first rule checked.
func newAuthRateLimitMW(l *ratelimit.Limiter, rules []rateLimitRule) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var blocked bool
			var retryAfter time.Duration
			for _, rule := range rules {
				key, ok := rule.keyFn(r)
				if !ok {
					continue
				}
				res := l.Allow(r.Context(), key, rule.limit, rule.window)
				if !res.Allowed {
					blocked = true
					if res.RetryAfter > retryAfter {
						retryAfter = res.RetryAfter
					}
				}
			}
			if blocked {
				httpx.TooManyRequests(w, retryAfter)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// passthroughMW is a no-op middleware, used when rate limiting is disabled
// for a route (nil constructor result — most existing tests never wire a
// Limiter) so route registration never needs a nil-vs-non-nil branch.
func passthroughMW(next http.Handler) http.Handler { return next }

// orPassthrough returns mw, or passthroughMW if mw is nil.
func orPassthrough(mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	if mw == nil {
		return passthroughMW
	}
	return mw
}

// clientIP extracts the request's source IP, stripping the port. Falls back
// to the raw RemoteAddr string if it isn't in host:port form. The service
// does not sit behind a documented reverse proxy yet, so X-Forwarded-For is
// deliberately not trusted here (design Open Questions): trusting a
// client-controlled header without a fixed proxy allowlist would let a
// client forge its own rate-limit key and bypass the limiter entirely.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// peekEmail extracts the "email" field from a JSON request body without
// consuming it — the handler still needs to decode the same body afterward
// (design auth-rate-limiting D3). Bounded to 1 MiB, matching decodeJSON's
// own cap. Malformed/absent bodies yield "" (the tight rule's keyFn then
// skips itself; the handler's own decodeJSON reports the real 400).
func peekEmail(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body = io.NopCloser(bytes.NewReader(body))
	var payload struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(payload.Email))
}

// ipRule limits by source IP alone (admin endpoints: no tenant context).
func ipRule(scope string, limit int, window time.Duration) rateLimitRule {
	return rateLimitRule{limit: limit, window: window, keyFn: func(r *http.Request) (string, bool) {
		return scope + ":ip:" + clientIP(r), true
	}}
}

// ipEmailRule limits by source IP + submitted email (admin login: brute
// force guard on a single account).
func ipEmailRule(scope string, limit int, window time.Duration) rateLimitRule {
	return rateLimitRule{limit: limit, window: window, keyFn: func(r *http.Request) (string, bool) {
		email := peekEmail(r)
		if email == "" {
			return "", false
		}
		return scope + ":ipemail:" + clientIP(r) + ":" + email, true
	}}
}

// shopIPRule limits by (resolved shop_id + source IP) — member endpoints
// MUST include shop_id so one tenant's traffic never shares a bucket with
// another's (design auth-rate-limiting D2).
func shopIPRule(scope string, limit int, window time.Duration) rateLimitRule {
	return rateLimitRule{limit: limit, window: window, keyFn: func(r *http.Request) (string, bool) {
		shopID, ok := tenant.ShopID(r.Context())
		if !ok {
			return "", false
		}
		return scope + ":shop:" + strconv.Itoa(shopID) + ":ip:" + clientIP(r), true
	}}
}

// shopIPEmailRule limits by (resolved shop_id + source IP + submitted
// email) — member login/register brute-force guard, shop-scoped.
func shopIPEmailRule(scope string, limit int, window time.Duration) rateLimitRule {
	return rateLimitRule{limit: limit, window: window, keyFn: func(r *http.Request) (string, bool) {
		shopID, ok := tenant.ShopID(r.Context())
		if !ok {
			return "", false
		}
		email := peekEmail(r)
		if email == "" {
			return "", false
		}
		return scope + ":shop:" + strconv.Itoa(shopID) + ":ipemail:" + clientIP(r) + ":" + email, true
	}}
}

// NewAdminLoginRateLimit builds the rate limit middleware for
// POST /api/v1/admin/auth/login (design D2: IP-broad + IP+email-tight).
func NewAdminLoginRateLimit(l *ratelimit.Limiter, cfg config.RateLimitConfig) func(http.Handler) http.Handler {
	return newAuthRateLimitMW(l, []rateLimitRule{
		ipRule("admin_login", cfg.LoginBroadLimit, cfg.LoginBroadWindow),
		ipEmailRule("admin_login", cfg.LoginTightLimit, cfg.LoginTightWindow),
	})
}

// NewAdminRefreshRateLimit builds the rate limit middleware for
// POST /api/v1/admin/auth/refresh (design D2: IP-only — refresh tokens are
// already high-entropy secrets, so the concern is abuse/DoS, not brute
// force).
func NewAdminRefreshRateLimit(l *ratelimit.Limiter, cfg config.RateLimitConfig) func(http.Handler) http.Handler {
	return newAuthRateLimitMW(l, []rateLimitRule{
		ipRule("admin_refresh", cfg.RefreshLimit, cfg.RefreshWindow),
	})
}

// NewMemberLoginRateLimit builds the rate limit middleware for
// POST /api/v1/shop/auth/login. MUST run after TenantMW so tenant.ShopID is
// resolved (design D2: shop+IP-broad + shop+IP+email-tight).
func NewMemberLoginRateLimit(l *ratelimit.Limiter, cfg config.RateLimitConfig) func(http.Handler) http.Handler {
	return newAuthRateLimitMW(l, []rateLimitRule{
		shopIPRule("member_login", cfg.LoginBroadLimit, cfg.LoginBroadWindow),
		shopIPEmailRule("member_login", cfg.LoginTightLimit, cfg.LoginTightWindow),
	})
}

// NewMemberRegisterRateLimit builds the rate limit middleware for
// POST /api/v1/shop/auth/register. MUST run after TenantMW (design D2:
// shop+IP-broad + shop+IP+email-tight, tighter thresholds than login).
func NewMemberRegisterRateLimit(l *ratelimit.Limiter, cfg config.RateLimitConfig) func(http.Handler) http.Handler {
	return newAuthRateLimitMW(l, []rateLimitRule{
		shopIPRule("member_register", cfg.RegisterBroadLimit, cfg.RegisterBroadWindow),
		shopIPEmailRule("member_register", cfg.RegisterTightLimit, cfg.RegisterTightWindow),
	})
}

// NewMemberRefreshRateLimit builds the rate limit middleware for
// POST /api/v1/shop/auth/refresh. MUST run after TenantMW (design D2:
// shop+IP-only).
func NewMemberRefreshRateLimit(l *ratelimit.Limiter, cfg config.RateLimitConfig) func(http.Handler) http.Handler {
	return newAuthRateLimitMW(l, []rateLimitRule{
		shopIPRule("member_refresh", cfg.RefreshLimit, cfg.RefreshWindow),
	})
}
