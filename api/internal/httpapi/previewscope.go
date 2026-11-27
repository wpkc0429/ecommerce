package httpapi

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/previeworigin"
)

// Read-only preview sessions (change preview-readonly-session).
//
// Enforcement is central and default-deny:
//
//   - NewPreviewGuardMiddleware runs first on all of /api/v1. A request whose
//     Origin is a configured preview origin may only use safe methods, plus
//     POST on the three auth endpoints that keep a session alive or end it.
//     Everything else - every present and FUTURE non-safe route, any unknown
//     method - is 403 preview_read_only, with no per-route opt-in to forget.
//   - A small explicit denylist removes GET routes with side effects or
//     sensitive output (see previewDeniedGETs).
//   - NewAdminMiddleware (options) accepts preview_ro tokens only from the
//     exact origin they were bound to, and refuses ordinary admin tokens from
//     a preview origin, so neither token class works in the other's origin.

// PreviewReadOnlyCode is the error code of every preview read-only refusal.
const PreviewReadOnlyCode = "preview_read_only"

func writePreviewReadOnly(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusForbidden, PreviewReadOnlyCode,
		"this is a read-only preview session: the request is not allowed from a preview origin", nil)
}

// previewAllowedPOSTs are the only non-safe requests a preview origin may make.
var previewAllowedPOSTs = map[string]bool{
	adminPrefix + "/auth/login":   true,
	adminPrefix + "/auth/refresh": true,
	adminPrefix + "/auth/logout":  true,
}

// previewDeniedGETs lists safe-method routes a read-only session may not call:
// they mint credentials or signed URLs, expose service/credential management
// or platform-wide personal data. A segment "*" matches one segment, a final
// "**" matches any remainder. Every entry must be a real route (tested).
var previewDeniedGETs = []string{
	// mints a bearer preview token
	"/api/v1/admin/shops/*/pages/*/preview-token",
	// returns signed read URLs of stored screenshots
	"/api/v1/admin/shops/*/design/requests/*/screenshots",
	// payment provider credentials (identifiers) of the shop
	"/api/v1/admin/shops/*/ecpay-credentials",
	// publish hook configuration (deploy-hook host and masked secret tail)
	"/api/v1/admin/shops/*/content/publish-hook",
	// design-service credential management (platform level)
	"/api/v1/admin/design-service/**",
	// platform-wide personal data: accounts and merchant applications
	"/api/v1/admin/accounts/**",
	"/api/v1/admin/shop-applications/**",
	// the machine API family is never for a preview origin
	"/api/v1/design-service/**",
}

func splitSegments(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func matchPattern(pattern string, segs []string) bool {
	ps := splitSegments(pattern)
	for i, p := range ps {
		if p == "**" {
			return true
		}
		if i >= len(segs) {
			return false
		}
		if p != "*" && p != segs[i] {
			return false
		}
	}
	return len(ps) == len(segs)
}

// isSafeMethod reports whether a method is read-only by definition.
func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// pathCandidates returns the path spellings the router might route on.
func pathCandidates(r *http.Request) [][]string {
	out := [][]string{splitSegments(r.URL.Path)}
	if r.URL.RawPath != "" {
		out = append(out, splitSegments(r.URL.RawPath))
		if dec, err := url.PathUnescape(r.URL.RawPath); err == nil {
			out = append(out, splitSegments(dec))
		}
	}
	return out
}

// previewBlocked is THE read-only policy: true means refuse with 403.
func previewBlocked(r *http.Request) bool {
	cands := pathCandidates(r)
	if !isSafeMethod(r.Method) {
		if r.Method != http.MethodPost {
			return true
		}
		for _, segs := range cands {
			if !previewAllowedPOSTs["/"+strings.Join(segs, "/")] {
				return true
			}
		}
		return false
	}
	for _, segs := range cands {
		for _, d := range previewDeniedGETs {
			if matchPattern(d, segs) {
				return true
			}
		}
	}
	return false
}

// requestPreviewOrigin returns the canonical preview origin the request comes
// from. The Origin header is decisive when present (a browser always sends it
// cross-origin and on same-origin non-GET requests); without it the Host
// identifies a same-origin GET through a preview host's proxy.
func requestPreviewOrigin(set *previeworigin.Set, r *http.Request) (string, bool) {
	if !set.Enabled() {
		return "", false
	}
	if raw := r.Header.Get("Origin"); raw != "" {
		return set.Match(raw)
	}
	if r.Host == "" {
		return "", false
	}
	for _, scheme := range []string{"https", "http"} {
		if o, ok := set.Match(scheme + "://" + r.Host); ok {
			return o, true
		}
	}
	return "", false
}

// NewPreviewGuardMiddleware enforces the read-only policy for every request
// that comes from a preview origin. nil/empty set = pass-through.
func NewPreviewGuardMiddleware(set *previeworigin.Set, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !set.Enabled() {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if origin, ok := requestPreviewOrigin(set, r); ok && previewBlocked(r) {
				if log != nil {
					log.Warn("admin_preview_denied",
						"method", r.Method, "path", r.URL.Path, "origin_tag", auth.OriginTag(origin))
				}
				writePreviewReadOnly(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// AdminMWOption customizes NewAdminMiddleware.
type AdminMWOption func(*adminMW)

type adminMW struct {
	preview *previeworigin.Set
	log     *slog.Logger
}

// WithPreviewSessions enables read-only preview sessions in the admin
// middleware (nil/empty set = feature off: preview tokens are plain 401).
func WithPreviewSessions(set *previeworigin.Set, log *slog.Logger) AdminMWOption {
	return func(m *adminMW) { m.preview, m.log = set, log }
}
