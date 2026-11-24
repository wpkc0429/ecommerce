package httpapi

import (
	"net/http"
	"slices"
	"strings"

	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// AdminRefreshHeader is the custom header every cookie-mode refresh/logout
// MUST carry (change admin-api-public-exposure, design D5). A custom header
// forces a cross-origin request through a CORS preflight, which only the
// shop's own admin origin passes - defeating same-site subdomain CSRF that
// SameSite=Strict alone does not stop.
const AdminRefreshHeader = "X-Admin-Refresh"

// callerOrigins returns the origin identity of a back-office request: the
// browser's Origin header when present, else https://{Host}. A same-origin GET
// carries no Origin, and cloudflared forwards the original Host, so Host is
// what identifies a per-shop admin front end behind the tunnel. Both are
// normalized exactly as registered origins are (tenant.NormalizeOrigin).
func callerOrigins(r *http.Request) []string {
	var out []string
	if o := tenant.NormalizeOrigin(r.Header.Get("Origin")); o != "" {
		out = append(out, o)
	}
	if r.Host != "" {
		if h := tenant.NormalizeOrigin("https://" + r.Host); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// callerHost is the host[:port] part of the request's Origin (preferred) or
// Host, lowercased - the value PLATFORM_ADMIN_HOSTS is matched against.
func callerHost(r *http.Request) string {
	if o := tenant.NormalizeOrigin(r.Header.Get("Origin")); o != "" {
		_, host, _ := strings.Cut(o, "://")
		return host
	}
	return strings.ToLower(strings.TrimSpace(r.Host))
}

// boundAdminShop resolves the shop a request's admin front end is bound to
// (design D4): the Origin if it is a registered purpose=admin origin, else the
// Host if it is. ok=false means the request does not come from any registered
// per-shop admin front end (local development, the platform's own admin
// domain, a direct API call) and no binding applies.
func boundAdminShop(r *http.Request, registry OriginRegistry) (shopID int, ok bool) {
	if registry == nil {
		return 0, false
	}
	for _, o := range callerOrigins(r) {
		if sid, purpose, found := registry.LookupOrigin(r.Context(), o); found && purpose == tenant.PurposeAdmin {
			return sid, true
		}
	}
	return 0, false
}

// NewAdminHostBindingMiddleware enforces, on the server, that a per-shop admin
// front end can only reach its own shop (design D4), and that platform-level
// APIs are only callable from PLATFORM_ADMIN_HOSTS (design D10):
//
//   - bound to shop X: /admin/shops/{Y}/... with Y != X and every platform-level
//     path answer 403, even for a caller who is a member of both shops or a
//     platform administrator; auth, me, branding and invitation accept pass;
//   - platformHosts non-empty: platform-level paths answer 403 unless the
//     request's Origin/Host is listed;
//   - neither applies: untouched.
//
// It needs no identity, so it runs ahead of AdminMW at the /admin group level.
// CORS is not what enforces this - a non-browser client ignores CORS.
func NewAdminHostBindingMiddleware(registry OriginRegistry, platformHosts []string) func(http.Handler) http.Handler {
	hosts := make([]string, 0, len(platformHosts))
	for _, h := range platformHosts {
		hosts = append(hosts, strings.ToLower(strings.TrimSpace(h)))
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			kind, pathShop := classifyAdminPath(r.URL.Path)
			if kind == pathAdminShared || kind == pathNonAdmin {
				next.ServeHTTP(w, r)
				return
			}
			if boundShop, bound := boundAdminShop(r, registry); bound {
				if kind == pathAdminPlatform || pathShop != boundShop {
					httpx.Forbidden(w)
					return
				}
			}
			if kind == pathAdminPlatform && len(hosts) > 0 && !slices.Contains(hosts, callerHost(r)) {
				httpx.Forbidden(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
