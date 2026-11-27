package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/previeworigin"
	"ksdevworks/ecommerce/api/internal/testutil"
)

const (
	previewOriginA = "https://a.preview.example.test"
	previewOriginB = "https://b.preview.example.test"
	normalOrigin   = "https://admin.example.test"
)

func testPreviewSet(t *testing.T) *previeworigin.Set {
	t.Helper()
	s, err := previeworigin.Parse([]string{"https://*.preview.example.test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// previewRouter is the production route table (every Deps field set) with the
// preview guard mounted exactly as wire.go does.
func previewRouter(t *testing.T) http.Handler {
	t.Helper()
	deps := contractDeps(t)
	set := testPreviewSet(t)
	if deps.Cfg == nil {
		deps.Cfg = &config.Config{}
	}
	deps.Cfg.AdminPreview.Set = set
	deps.PreviewGuardMW = NewPreviewGuardMiddleware(set, quietLog())
	return New(deps)
}

func concretePath(route string) string { return pathParamRe.ReplaceAllString(route, "1") }

func serve(h http.Handler, method, path, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func isPreviewDenied(rec *httptest.ResponseRecorder) bool {
	return rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), `"`+PreviewReadOnlyCode+`"`)
}

// Every non-safe route of the real router MUST be refused from a preview
// origin, except exactly the three auth endpoints. The route list comes from
// the router itself, so a route added tomorrow is covered the day it exists.
func TestPreviewGuardRefusesEveryNonSafeRoute(t *testing.T) {
	h := previewRouter(t)
	routes := testutil.RouteTable(t, h)
	var checked, allowed int
	for _, route := range routes {
		method, tmpl, _ := strings.Cut(route, " ")
		path := concretePath(strings.TrimSuffix(tmpl, "/"))
		rec := serve(h, method, path, previewOriginA)
		if isSafeMethod(method) {
			continue
		}
		checked++
		if previewAllowedPOSTs[tmpl] && method == http.MethodPost {
			allowed++
			if isPreviewDenied(rec) {
				t.Errorf("%s must stay reachable from a preview origin (session keep-alive)", route)
			}
			continue
		}
		if !isPreviewDenied(rec) {
			t.Errorf("%s from a preview origin: status %d, want 403 %s", route, rec.Code, PreviewReadOnlyCode)
		}
		// The very same request from a normal origin / no origin is untouched.
		if rec2 := serve(h, method, path, normalOrigin); isPreviewDenied(rec2) {
			t.Errorf("%s from a normal origin must not be refused by the preview guard", route)
		}
		if rec3 := serve(h, method, path, ""); isPreviewDenied(rec3) {
			t.Errorf("%s without an Origin must not be refused by the preview guard", route)
		}
	}
	if allowed != len(previewAllowedPOSTs) {
		t.Fatalf("allowlisted auth endpoints found in the router: %d, want %d", allowed, len(previewAllowedPOSTs))
	}
	if checked < 100 {
		t.Fatalf("only %d non-safe routes enumerated: the router walk is broken", checked)
	}
	t.Logf("%d non-safe routes verified (%d allowlisted)", checked, allowed)
}

func TestPreviewGuardUnsafeMethodsAndSpellings(t *testing.T) {
	h := previewRouter(t)
	for _, m := range []string{"TRACE", "PROPFIND", "PURGE", "patch", "Post"} {
		// Methods chi does not know never reach any handler (405 before the
		// middleware); the known ones must be refused by the guard.
		rec := serve(h, m, "/api/v1/admin/shops/1/products", previewOriginA)
		if !isPreviewDenied(rec) && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("method %q from a preview origin: status %d", m, rec.Code)
		}
	}
	for _, p := range []string{
		"/api/v1/admin/auth/login/x", "/api/v1/admin/auth/login-as", "/api/v1/admin//auth//refresh/extra",
		"/api/v1/admin/auth/register", "/api/v1/shop/auth/login", "/api/v1/shop/guest/checkout",
	} {
		if rec := serve(h, http.MethodPost, p, previewOriginA); !isPreviewDenied(rec) {
			t.Errorf("POST %s from a preview origin: status %d", p, rec.Code)
		}
	}
	// Alternative spellings of an allowed path still count as the allowed path
	// only when they are exactly it.
	for _, p := range []string{"/api/v1/admin/auth/login", "/api/v1/admin/auth/login/", "/api/v1//admin/auth/login"} {
		if rec := serve(h, http.MethodPost, p, previewOriginA); isPreviewDenied(rec) {
			t.Errorf("POST %s must pass the guard", p)
		}
	}
}

// Same-origin reads carry no Origin header: the Host identifies the preview host.
func TestPreviewGuardFallsBackToHost(t *testing.T) {
	h := previewRouter(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/shops/1/products/2", http.NoBody)
	req.Host = "a.preview.example.test"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !isPreviewDenied(rec) {
		t.Fatalf("status %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/shops/1/pages/2/preview-token", http.NoBody)
	req.Host = "a.preview.example.test"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !isPreviewDenied(rec) {
		t.Fatalf("denylisted GET via Host: status %d", rec.Code)
	}
	// An Origin header is decisive: a normal Origin on a preview Host is not a preview request.
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/shops/1/products/2", http.NoBody)
	req.Host = "a.preview.example.test"
	req.Header.Set("Origin", normalOrigin)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if isPreviewDenied(rec) {
		t.Fatal("Origin header must be decisive when present")
	}
}

// The explicit GET denylist: every entry is a real route, every denied route
// is refused, and every other GET stays reachable.
func TestPreviewDeniedGETsAreRealRoutesAndRefused(t *testing.T) {
	h := previewRouter(t)
	var gets []string
	for _, route := range testutil.RouteTable(t, h) {
		if strings.HasPrefix(route, "GET ") {
			gets = append(gets, strings.TrimPrefix(route, "GET "))
		}
	}
	sort.Strings(gets)
	denied := map[string]bool{}
	for _, pat := range previewDeniedGETs {
		hits := 0
		for _, g := range gets {
			if matchPattern(pat, splitSegments(concretePath(g))) {
				hits++
				denied[g] = true
			}
		}
		if hits == 0 {
			t.Errorf("denylist entry %q matches no GET route: stale entry", pat)
		}
	}
	for _, g := range gets {
		rec := serve(h, http.MethodGet, concretePath(g), previewOriginA)
		if denied[g] != isPreviewDenied(rec) {
			t.Errorf("GET %s: denied=%v but refused=%v (status %d)", g, denied[g], isPreviewDenied(rec), rec.Code)
		}
	}
	t.Logf("%d GET routes, %d on the denylist", len(gets), len(denied))
	// The routes the review identified must be on it.
	for _, must := range []string{
		"/api/v1/admin/shops/{shopID}/pages/{pageID}/preview-token",
		"/api/v1/admin/shops/{shopID}/design/requests/{requestID}/screenshots",
		"/api/v1/admin/shops/{shopID}/ecpay-credentials",
		"/api/v1/admin/shops/{shopID}/content/publish-hook",
		"/api/v1/admin/design-service/credentials",
		"/api/v1/admin/accounts",
	} {
		if !denied[must] {
			t.Errorf("%s must be on the preview denylist", must)
		}
	}
}

func TestPreviewGuardIsPassThroughWhenOff(t *testing.T) {
	deps := contractDeps(t)
	deps.PreviewGuardMW = NewPreviewGuardMiddleware(nil, quietLog())
	h := New(deps)
	if rec := serve(h, http.MethodDelete, "/api/v1/admin/shops/1/products/2", previewOriginA); isPreviewDenied(rec) {
		t.Fatal("feature off: nothing is a preview origin")
	}
}

func newTestIssuerForPreview(t *testing.T) *auth.TokenIssuer {
	t.Helper()
	ti, err := auth.NewTokenIssuer("test-admin-secret-1", "test-member-secret-2", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return ti
}

func adminMWStatus(mw func(http.Handler) http.Handler, method, path, origin, token string) (int, bool, bool) {
	var ro, called bool
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if id, ok := auth.AdminFrom(r.Context()); ok {
			ro = id.PreviewRO
		}
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(method, path, http.NoBody)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, ro, called
}

func TestAdminMiddlewareTokenConfusionBothDirections(t *testing.T) {
	ti := newTestIssuerForPreview(t)
	mw := NewAdminMiddleware(ti, WithPreviewSessions(testPreviewSet(t), quietLog()))
	normal, _ := ti.IssueAdmin(5, []int{1})
	ro, _ := ti.IssueAdminPreview(5, []int{1}, previewOriginA, 10*time.Minute)
	roB, _ := ti.IssueAdminPreview(5, []int{1}, previewOriginB, 10*time.Minute)
	roForged, _ := ti.IssueAdminPreview(5, []int{1}, "https://evil.example.test", 10*time.Minute)
	roExpired, _ := ti.IssueAdminPreview(5, []int{1}, previewOriginA, -time.Minute)
	read := "/api/v1/admin/shops/1/products"

	type tc struct {
		name           string
		method, path   string
		origin, token  string
		want           int
		wantRO, called bool
	}
	cases := []tc{
		{"normal token from normal origin", "DELETE", read, normalOrigin, normal, 200, false, true},
		{"normal token without origin", "GET", read, "", normal, 200, false, true},
		{"normal token from preview origin: read", "GET", read, previewOriginA, normal, 403, false, false},
		{"normal token from preview origin: write", "DELETE", read, previewOriginA, normal, 403, false, false},
		{"ro token from its origin: read", "GET", read, previewOriginA, ro, 200, true, true},
		{"ro token from its origin: HEAD", "HEAD", read, previewOriginA, ro, 200, true, true},
		{"ro token from its origin: write", "POST", read, previewOriginA, ro, 403, false, false},
		{"ro token from its origin: delete", "DELETE", read + "/1", previewOriginA, ro, 403, false, false},
		{"ro token from its origin: denylisted GET", "GET", "/api/v1/admin/shops/1/ecpay-credentials", previewOriginA, ro, 403, false, false},
		{"ro token from normal admin origin", "GET", read, normalOrigin, ro, 401, false, false},
		{"ro token from normal admin origin: write", "POST", read, normalOrigin, ro, 401, false, false},
		{"ro token without origin", "GET", read, "", ro, 401, false, false},
		{"ro token from another preview origin", "GET", read, previewOriginB, ro, 401, false, false},
		{"other ro token from origin A", "GET", read, previewOriginA, roB, 401, false, false},
		{"ro token bound to a non preview origin", "GET", read, "https://evil.example.test", roForged, 401, false, false},
		{"expired ro token", "GET", read, previewOriginA, roExpired, 401, false, false},
		{"garbage token from preview origin", "GET", read, previewOriginA, "x.y.z", 401, false, false},
		{"no token", "GET", read, previewOriginA, "", 401, false, false},
	}
	for _, c := range cases {
		code, ro, called := adminMWStatus(mw, c.method, c.path, c.origin, c.token)
		if code != c.want || ro != c.wantRO || called != c.called {
			t.Errorf("%s: status %d ro=%v called=%v, want %d ro=%v called=%v", c.name, code, ro, called, c.want, c.wantRO, c.called)
		}
	}
	// Feature off: a preview token is a plain 401 even from its own origin.
	off := NewAdminMiddleware(ti)
	if code, _, _ := adminMWStatus(off, "GET", read, previewOriginA, ro); code != http.StatusUnauthorized {
		t.Fatalf("feature off: status %d", code)
	}
	if code, _, _ := adminMWStatus(off, "DELETE", read, previewOriginA, normal); code != http.StatusOK {
		t.Fatalf("feature off must not change normal behavior: status %d", code)
	}
}

func TestCORSPreviewOrigins(t *testing.T) {
	set := testPreviewSet(t)
	mw := NewCORSMiddleware([]string{normalOrigin}, nil, WithPreviewOrigins(set))
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	allow := func(path, origin string) bool {
		req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		mw(ok).ServeHTTP(rec, req)
		return rec.Header().Get("Access-Control-Allow-Origin") == origin
	}
	for _, p := range []string{"/api/v1/admin/shops/1/products", "/api/v1/admin/auth/login", "/api/v1/admin/me"} {
		if !allow(p, previewOriginA) {
			t.Errorf("preview origin must get CORS on %s", p)
		}
	}
	for _, p := range []string{"/api/v1/admin/accounts", "/api/v1/admin/shops", "/api/v1/admin/roles", "/api/v1/shop/products"} {
		if allow(p, previewOriginA) {
			t.Errorf("preview origin must not get CORS on %s", p)
		}
	}
	if allow("/api/v1/admin/shops/1/products", "https://a.b.preview.example.test") {
		t.Error("non matching origin must not get CORS")
	}
	if !allow("/api/v1/admin/shops/1/products", normalOrigin) {
		t.Error("normal platform origin keeps its CORS")
	}
}
