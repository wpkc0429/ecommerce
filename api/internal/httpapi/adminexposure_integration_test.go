package httpapi_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// Change admin-api-public-exposure: per-purpose CORS, admin host binding,
// PLATFORM_ADMIN_HOSTS, branding, account lockout and cookie-mode refresh.
// Every address, host and credential below is a throwaway test value on
// reserved example domains.

const (
	exPlatformOrigin = "https://platform-admin.example.com"
	exAdminA         = "https://admin.shop-a.example.com"
	exAdminB         = "https://admin.shop-b.example.com"
	exStorefrontA    = "https://shop-a.example.com"
	exPassword       = "ex-test-password-123"
	exImagePrefix    = "https://img.example.com/"
)

type exposureEnv struct {
	client   *ent.Client
	rdb      *redis.Client
	resolver *tenant.Resolver
	issuer   *auth.TokenIssuer
	router   http.Handler

	shopA, shopB int
	multi        int // member of A and B (owner role in both)
	ownerB       int
	platform     int // platform role holding user.manage_roles
}

type exposureOpts struct {
	platformHosts []string
	lockout       int
	deadRedis     bool
}

func newExposureEnv(t *testing.T, o exposureOpts) *exposureEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &exposureEnv{client: client, rdb: rdb}

	e.shopA = client.Shop.Create().SetName("Shop A").SetStatus(1).SaveX(ctx).ID
	e.shopB = client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(ctx).ID

	ownerRole := client.Role.Create().SetName("merchant_owner").SetScope("merchant").SaveX(ctx)
	for _, n := range []string{"shop.update", "publishable_key.view"} {
		p := client.Permission.Create().SetName(n).SaveX(ctx)
		client.RolePermission.Create().SetRoleID(ownerRole.ID).SetPermissionID(p.ID).SaveX(ctx)
	}
	permRole := client.Permission.Create().SetName("user.manage_roles").SaveX(ctx)
	platformRole := client.Role.Create().SetName("super_admin").SetScope("platform").SaveX(ctx)
	client.RolePermission.Create().SetRoleID(platformRole.ID).SetPermissionID(permRole.ID).SaveX(ctx)

	hash, err := auth.HashPassword(exPassword)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(email string, shops ...int) int {
		u := client.User.Create().SetEmail(email).SetPasswordHash(hash).SetStatus(1).SaveX(ctx)
		for _, s := range shops {
			client.ShopUser.Create().SetShopID(s).SetUserID(u.ID).SaveX(ctx)
			client.RoleUser.Create().SetUserID(u.ID).SetRoleID(ownerRole.ID).SetShopID(s).SaveX(ctx)
		}
		return u.ID
	}
	e.multi = mk("multi@example.com", e.shopA, e.shopB)
	e.ownerB = mk("owner-b@example.com", e.shopB)
	e.platform = mk("platform@example.com")
	client.RoleUser.Create().SetUserID(e.platform).SetRoleID(platformRole.ID).SaveX(ctx)

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	log := slog.New(slog.DiscardHandler)
	e.resolver = tenant.NewResolver(client, rdb, log)
	engine := rbac.NewEngine(client, rdb, log)
	authz := &httpapi.AuthzMW{Engine: engine}
	refresh := auth.NewRefreshService(client, 30*24*time.Hour)

	limRedis := rdb
	if o.deadRedis {
		limRedis = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1})
	}
	adminAuth := &httpapi.AdminAuthHandler{
		Client: client, Issuer: issuer, Refresh: refresh, Engine: engine, Log: log,
		RefreshTTL: 30 * 24 * time.Hour,
		Limiter:    ratelimit.New(limRedis, log), LockoutFailures: o.lockout, LockoutWindow: time.Minute,
		LockoutSecret: []byte("test-lockout-secret"),
	}
	e.router = httpapi.New(httpapi.Deps{
		Cfg:         &config.Config{CORSAllowedOrigins: []string{exPlatformOrigin}},
		Log:         log,
		AdminMW:     httpapi.NewAdminMiddleware(issuer),
		AdminHostMW: httpapi.NewAdminHostBindingMiddleware(e.resolver, o.platformHosts),
		CORSOrigins: e.resolver,
		AdminAuth:   adminAuth,
		Roles:       &httpapi.RolesHandler{Client: client, Engine: engine, Authz: authz, Log: log},
		ShopSettings: &httpapi.ShopSettingsHandler{
			Client: client, Authz: authz, Log: log, DefaultTimezone: "Asia/Taipei",
			ImagePrefixes: func() []string { return []string{exImagePrefix} },
		},
		Branding: &httpapi.BrandingHandler{Client: client, Registry: e.resolver, Authz: authz, Log: log},
	})
	return e
}

func (e *exposureEnv) registerOrigin(shopID int, origin, purpose string) {
	e.client.ShopAllowedOrigin.Create().SetShopID(shopID).SetOrigin(origin).SetPurpose(purpose).SaveX(context.Background())
}

func (e *exposureEnv) token(t *testing.T, uid int) string {
	t.Helper()
	tok, err := e.issuer.IssueAdmin(uid, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

type exReq struct {
	method, path, origin, host, remote, bearer string
	body                                       any
	headers                                    map[string]string
	cookies                                    []*http.Cookie
}

func (e *exposureEnv) do(t *testing.T, r exReq) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if r.body != nil {
		if err := json.NewEncoder(&buf).Encode(r.body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(r.method, r.path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if r.origin != "" {
		req.Header.Set("Origin", r.origin)
	}
	if r.host != "" {
		req.Host = r.host
	}
	if r.remote != "" {
		req.RemoteAddr = r.remote
	}
	if r.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+r.bearer)
	}
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	for _, c := range r.cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *exposureEnv) preflight(t *testing.T, origin, path string) *httptest.ResponseRecorder {
	t.Helper()
	return e.do(t, exReq{method: http.MethodOptions, path: path, origin: origin, headers: map[string]string{
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "authorization,x-admin-refresh",
	}})
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return m
}

// Scenario (admin-api-exposure): 後台路徑的 CORS 依用途與商家分流 — four
// scenarios from the spec plus every row of the path table.
func TestAdminCORSPerPurposeAndShop(t *testing.T) {
	e := newExposureEnv(t, exposureOpts{})
	e.registerOrigin(e.shopA, exStorefrontA, tenant.PurposeStorefront)
	e.registerOrigin(e.shopA, exAdminA, tenant.PurposeAdmin)
	e.registerOrigin(e.shopB, exAdminB, tenant.PurposeAdmin)

	shopPath := func(id int, rest string) string { return fmt.Sprintf("/api/v1/admin/shops/%d%s", id, rest) }
	allowed := func(origin, path string) bool {
		rec := e.preflight(t, origin, path)
		return rec.Header().Get("Access-Control-Allow-Origin") == origin
	}

	cases := []struct {
		name   string
		origin string
		path   string
		want   bool
	}{
		// storefront origin: nothing on /admin, everything it had on the rest.
		{"storefront on admin login", exStorefrontA, "/api/v1/admin/auth/login", false},
		{"storefront on own shop admin path", exStorefrontA, shopPath(1, "/orders"), false},
		{"storefront on platform path", exStorefrontA, "/api/v1/admin/accounts", false},
		{"storefront on shop API", exStorefrontA, "/api/v1/shop/products", true},
		// admin origin: only its own shop, plus the shared paths.
		{"admin A on shop A path", exAdminA, shopPath(e.shopA, "/orders"), true},
		{"admin A on shop B path", exAdminA, shopPath(e.shopB, "/orders"), false},
		{"admin B on shop B path", exAdminB, shopPath(e.shopB, "/orders"), true},
		{"admin A on platform accounts", exAdminA, "/api/v1/admin/accounts", false},
		{"admin A on platform shop list", exAdminA, "/api/v1/admin/shops", false},
		{"admin A on platform shop get", exAdminA, shopPath(e.shopA, ""), false},
		{"admin A on login", exAdminA, "/api/v1/admin/auth/login", true},
		{"admin A on refresh", exAdminA, "/api/v1/admin/auth/refresh", true},
		{"admin A on me", exAdminA, "/api/v1/admin/me", true},
		{"admin A on branding", exAdminA, "/api/v1/admin/branding", true},
		{"admin A on invitations accept", exAdminA, "/api/v1/admin/invitations/accept", true},
		{"admin A on media config", exAdminA, "/api/v1/admin/media/config", true},
		{"admin A on other invitations path", exAdminA, "/api/v1/admin/invitations/other", false},
		{"admin A on storefront API", exAdminA, "/api/v1/shop/products", false},
		{"admin A on healthz", exAdminA, "/healthz", false},
		// platform list: everywhere.
		{"platform on platform path", exPlatformOrigin, "/api/v1/admin/accounts", true},
		{"platform on shop path", exPlatformOrigin, shopPath(e.shopB, "/orders"), true},
		{"platform on shop API", exPlatformOrigin, "/api/v1/shop/products", true},
		// unknown origin: nothing.
		{"unknown on login", "https://evil.example.com", "/api/v1/admin/auth/login", false},
	}
	for _, c := range cases {
		if got := allowed(c.origin, c.path); got != c.want {
			t.Errorf("%s: origin %s path %s: allowed=%v, want %v", c.name, c.origin, c.path, got, c.want)
		}
	}

	// A denied preflight carries no Access-Control-* header at all.
	rec := e.preflight(t, exStorefrontA, "/api/v1/admin/auth/login")
	for k := range rec.Header() {
		if strings.HasPrefix(k, "Access-Control-") {
			t.Fatalf("denied preflight must carry no %s", k)
		}
	}
	// X-Admin-Refresh is announced for granted preflights.
	if h := e.preflight(t, exAdminA, "/api/v1/admin/auth/refresh").Header().Get("Access-Control-Allow-Headers"); !strings.Contains(h, "X-Admin-Refresh") {
		t.Fatalf("Allow-Headers must list X-Admin-Refresh, got %q", h)
	}
	// Removing the registration revokes the grant at once (cache invalidation).
	e.client.ShopAllowedOrigin.Delete().ExecX(context.Background())
	e.resolver.InvalidateOrigins(context.Background(), exAdminA, exAdminB, exStorefrontA)
	if allowed(exAdminA, "/api/v1/admin/auth/login") {
		t.Fatal("deleted admin origin must lose its grant")
	}
}

// Scenario: 多店成員在單店 admin 上 / 平台管理員在商家 admin 上 / 未登記主機不受限.
func TestAdminHostBinding(t *testing.T) {
	e := newExposureEnv(t, exposureOpts{})
	e.registerOrigin(e.shopA, exAdminA, tenant.PurposeAdmin)
	multi := e.token(t, e.multi)
	plat := e.token(t, e.platform)

	shopMe := func(id int) string { return fmt.Sprintf("/api/v1/admin/shops/%d/me", id) }
	type via struct{ name, origin, host string }
	boundVia := []via{
		{"Origin", exAdminA, ""},
		{"Host", "", "admin.shop-a.example.com"}, // https://host is composed and looked up
	}
	for _, v := range boundVia {
		get := func(path, bearer string) int {
			return e.do(t, exReq{method: "GET", path: path, origin: v.origin, host: v.host, bearer: bearer}).Code
		}
		if got := get(shopMe(e.shopB), multi); got != http.StatusForbidden {
			t.Errorf("[%s] member of A and B on A's admin host, shop B: want 403, got %d", v.name, got)
		}
		if got := get(shopMe(e.shopA), multi); got != http.StatusOK {
			t.Errorf("[%s] own shop: want 200, got %d", v.name, got)
		}
		if got := get("/api/v1/admin/permissions", plat); got != http.StatusForbidden {
			t.Errorf("[%s] platform admin on a shop admin host, platform path: want 403, got %d", v.name, got)
		}
		if got := get("/api/v1/admin/me", multi); got != http.StatusOK {
			t.Errorf("[%s] /me must pass on a bound host: got %d", v.name, got)
		}
		if got := get("/api/v1/admin/me", plat); got != http.StatusOK {
			t.Errorf("[%s] /me for the platform admin must pass: got %d", v.name, got)
		}
	}

	// Not a registered admin host: no binding, RBAC alone decides.
	for _, host := range []string{"localhost:8080", "api.example.com"} {
		if got := e.do(t, exReq{method: "GET", path: shopMe(e.shopB), host: host, bearer: multi}).Code; got != http.StatusOK {
			t.Errorf("host %s: cross-shop must follow RBAC (200 for a member), got %d", host, got)
		}
		if got := e.do(t, exReq{method: "GET", path: "/api/v1/admin/permissions", host: host, bearer: plat}).Code; got != http.StatusOK {
			t.Errorf("host %s: platform path must follow RBAC (200), got %d", host, got)
		}
	}
	// The platform's own list origin is not bound either.
	if got := e.do(t, exReq{method: "GET", path: "/api/v1/admin/permissions", origin: exPlatformOrigin, bearer: plat}).Code; got != http.StatusOK {
		t.Errorf("platform origin: want 200, got %d", got)
	}
	// A storefront-purpose origin never binds.
	e.registerOrigin(e.shopB, "https://shop-b.example.com", tenant.PurposeStorefront)
	if got := e.do(t, exReq{method: "GET", path: shopMe(e.shopA), origin: "https://shop-b.example.com", bearer: multi}).Code; got != http.StatusOK {
		t.Errorf("storefront origin must not bind: got %d", got)
	}
}

// Scenario (PLATFORM_ADMIN_HOSTS): 從共用商家網域呼叫平台 API / 從平台網域呼叫 / 未設定時不生效.
func TestPlatformAdminHosts(t *testing.T) {
	e := newExposureEnv(t, exposureOpts{platformHosts: []string{"platform-admin.example.com"}})
	plat := e.token(t, e.platform)
	multi := e.token(t, e.multi)

	get := func(host, origin, path, bearer string) int {
		return e.do(t, exReq{method: "GET", path: path, host: host, origin: origin, bearer: bearer}).Code
	}
	if got := get("merchant.example.com", "", "/api/v1/admin/permissions", plat); got != http.StatusForbidden {
		t.Errorf("platform API from a non-listed host: want 403, got %d", got)
	}
	if got := get("merchant.example.com", "https://merchant.example.com", "/api/v1/admin/permissions", plat); got != http.StatusForbidden {
		t.Errorf("platform API from a non-listed origin: want 403, got %d", got)
	}
	if got := get("platform-admin.example.com", "", "/api/v1/admin/permissions", plat); got != http.StatusOK {
		t.Errorf("platform API from the listed host: want 200, got %d", got)
	}
	if got := get("", exPlatformOrigin, "/api/v1/admin/permissions", plat); got != http.StatusOK {
		t.Errorf("platform API from the listed origin: want 200, got %d", got)
	}
	// Shop paths and the shared paths are not platform-level.
	if got := get("merchant.example.com", "", fmt.Sprintf("/api/v1/admin/shops/%d/me", e.shopA), multi); got != http.StatusOK {
		t.Errorf("shop path from a non-listed host: want 200, got %d", got)
	}
	if got := get("merchant.example.com", "", "/api/v1/admin/me", multi); got != http.StatusOK {
		t.Errorf("/me from a non-listed host: want 200, got %d", got)
	}
	// Unset = not enforced (the default env of every other test).
	open := newExposureEnv(t, exposureOpts{})
	if got := open.do(t, exReq{method: "GET", path: "/api/v1/admin/permissions", host: "anywhere.example.com", bearer: open.token(t, open.platform)}).Code; got != http.StatusOK {
		t.Errorf("unset PLATFORM_ADMIN_HOSTS must not enforce: got %d", got)
	}
}

// Scenario: 登入前顯示商家品牌 / 未登記主機 404 / 非法顏色 422 + settings semantics.
func TestAdminBranding(t *testing.T) {
	e := newExposureEnv(t, exposureOpts{})
	e.registerOrigin(e.shopA, exAdminA, tenant.PurposeAdmin)
	multi := e.token(t, e.multi)
	ownerB := e.token(t, e.ownerB)
	settings := fmt.Sprintf("/api/v1/admin/shops/%d/settings", e.shopA)

	// Give the shop a time zone first: a branding-only PUT must keep it.
	if rec := e.do(t, exReq{method: "PUT", path: settings, bearer: multi, body: map[string]any{"timezone": "Asia/Tokyo"}}); rec.Code != 200 {
		t.Fatalf("set timezone: %d %s", rec.Code, rec.Body.String())
	}
	// Scenario: 非法顏色.
	for _, bad := range []any{
		map[string]any{"primary_color": "red"},
		map[string]any{"primary_color": "#12345"},
		map[string]any{"accent_color": "#GGGGGG"},
		map[string]any{"logo_url": "https://evil.example.com/x.png"}, // off the image domain
		map[string]any{"logo_url": "javascript:alert(1)"},            // not http(s)
		map[string]any{"unknown": "x"},                               // unknown key
		map[string]any{"logo_url": 5},                                // wrong type
		"not-an-object",
	} {
		if rec := e.do(t, exReq{method: "PUT", path: settings, bearer: multi, body: map[string]any{"admin_branding": bad}}); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("admin_branding %v: want 422, got %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	// Valid write; branding only -> time zone untouched.
	rec := e.do(t, exReq{method: "PUT", path: settings, bearer: multi, body: map[string]any{"admin_branding": map[string]any{
		"logo_url": exImagePrefix + "logo.webp", "primary_color": "#1A73E8", "accent_color": "#ff6d00",
	}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("branding write: %d %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["timezone"] != "Asia/Tokyo" {
		t.Fatalf("a branding-only PUT must not clear the time zone: %v", body)
	}
	br, _ := body["admin_branding"].(map[string]any)
	if br["primary_color"] != "#1a73e8" || br["accent_color"] != "#ff6d00" || br["logo_url"] != exImagePrefix+"logo.webp" {
		t.Fatalf("unexpected branding: %v", br)
	}
	// A time-zone-only PUT leaves branding alone ("absent = unchanged").
	if rec := e.do(t, exReq{method: "PUT", path: settings, bearer: multi, body: map[string]any{"timezone": "Asia/Taipei"}}); rec.Code != 200 {
		t.Fatalf("tz-only: %d", rec.Code)
	}

	// Authenticated read: any member; another shop's owner is refused.
	get := fmt.Sprintf("/api/v1/admin/shops/%d/branding", e.shopA)
	rec = e.do(t, exReq{method: "GET", path: get, bearer: multi})
	if rec.Code != 200 {
		t.Fatalf("branding read: %d", rec.Code)
	}
	if b := decodeBody(t, rec); b["name"] != "Shop A" || b["branding"].(map[string]any)["primary_color"] != "#1a73e8" {
		t.Fatalf("branding read body: %v", b)
	}
	if rec := e.do(t, exReq{method: "GET", path: get, bearer: ownerB}); rec.Code != http.StatusForbidden {
		t.Fatalf("other shop's owner: want 403, got %d", rec.Code)
	}
	if rec := e.do(t, exReq{method: "GET", path: get}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: want 401, got %d", rec.Code)
	}

	// Scenario: 登入前顯示商家品牌 (resolved from the admin origin / host).
	for name, r := range map[string]exReq{
		"origin": {method: "GET", path: "/api/v1/admin/branding", origin: exAdminA},
		"host":   {method: "GET", path: "/api/v1/admin/branding", host: "admin.shop-a.example.com"},
	} {
		rec := e.do(t, r)
		if rec.Code != 200 {
			t.Fatalf("[%s] public branding: %d %s", name, rec.Code, rec.Body.String())
		}
		b := decodeBody(t, rec)
		if b["name"] != "Shop A" || b["branding"].(map[string]any)["accent_color"] != "#ff6d00" {
			t.Fatalf("[%s] public branding body: %v", name, b)
		}
		if _, leaked := b["shop_id"]; leaked {
			t.Fatalf("[%s] the unauthenticated endpoint must not reveal the shop id", name)
		}
	}
	// Scenario: 未登記主機.
	for _, r := range []exReq{
		{method: "GET", path: "/api/v1/admin/branding", host: "localhost:8080"},
		{method: "GET", path: "/api/v1/admin/branding", origin: "https://unregistered.example.com"},
		{method: "GET", path: "/api/v1/admin/branding", origin: exStorefrontA}, // storefront purpose does not count
	} {
		if rec := e.do(t, r); rec.Code != http.StatusNotFound {
			t.Errorf("unregistered host %+v: want 404, got %d", r, rec.Code)
		}
	}

	// Clearing: explicit null resets branding; empty body keeps the legacy
	// meaning (clears the time zone) and leaves branding alone.
	rec = e.do(t, exReq{method: "PUT", path: settings, bearer: multi, body: map[string]any{"admin_branding": nil}})
	if rec.Code != 200 {
		t.Fatalf("clear branding: %d", rec.Code)
	}
	if b := decodeBody(t, rec); b["admin_branding"].(map[string]any)["primary_color"] != "" || b["timezone"] != "Asia/Taipei" {
		t.Fatalf("clear branding body: %v", b)
	}
	rec = e.do(t, exReq{method: "PUT", path: settings, bearer: multi, body: map[string]any{}})
	if b := decodeBody(t, rec); b["timezone"] != nil {
		t.Fatalf("an empty body keeps the historical meaning (clear the time zone): %v", b)
	}
}

func (e *exposureEnv) login(t *testing.T, email, password, remote string, extra map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"email": email, "password": password}
	for k, v := range extra {
		body[k] = v
	}
	return e.do(t, exReq{method: "POST", path: "/api/v1/admin/auth/login", remote: remote, body: body})
}

// Scenario (authentication): 分散式猜測被鎖定 / 不存在的帳號行為相同 / 成功登入清除計數.
func TestAdminAccountLockout(t *testing.T) {
	e := newExposureEnv(t, exposureOpts{lockout: 3})
	ip := func(n int) string { return fmt.Sprintf("203.0.113.%d:5000", n) }

	// Distributed guessing: 3 failures from 3 different IPs, then the CORRECT
	// password from a 4th IP is refused.
	for i := 1; i <= 3; i++ {
		if rec := e.login(t, "multi@example.com", "wrong-password-xx", ip(i), nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: want 401, got %d", i, rec.Code)
		}
	}
	locked := e.login(t, "multi@example.com", exPassword, ip(4), nil)
	if locked.Code != http.StatusTooManyRequests {
		t.Fatalf("locked account with the right password: want 429, got %d %s", locked.Code, locked.Body.String())
	}
	if locked.Header().Get("Retry-After") == "" {
		t.Fatal("a lockout 429 must carry Retry-After")
	}
	if _, has := decodeBody(t, locked)["access_token"]; has {
		t.Fatal("no token may be issued while locked")
	}
	// The e-mail is normalized: case/space variants share the counter.
	if rec := e.login(t, "  MULTI@Example.com ", exPassword, ip(5), nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("normalized e-mail must be locked too: got %d", rec.Code)
	}
	// Other accounts are untouched.
	if rec := e.login(t, "owner-b@example.com", exPassword, ip(6), nil); rec.Code != http.StatusOK {
		t.Fatalf("other account: want 200, got %d", rec.Code)
	}

	// Scenario: 不存在的帳號行為相同 (same status, same body shape).
	var ghost *httptest.ResponseRecorder
	for i := 1; i <= 3; i++ {
		e.login(t, "ghost@example.com", "whatever-12345", ip(10+i), nil)
	}
	ghost = e.login(t, "ghost@example.com", "whatever-12345", ip(20), nil)
	if ghost.Code != http.StatusTooManyRequests || ghost.Header().Get("Retry-After") == "" {
		t.Fatalf("unknown e-mail after the limit: want 429 + Retry-After, got %d", ghost.Code)
	}
	if keys(decodeBody(t, ghost)) != keys(decodeBody(t, locked)) {
		t.Fatalf("lockout response shape differs between an unknown and a known e-mail: %v vs %v",
			decodeBody(t, ghost), decodeBody(t, locked))
	}

	// Scenario: 成功登入清除計數 — 2 failures, success, 2 failures: not locked.
	for i := 0; i < 2; i++ {
		e.login(t, "owner-b@example.com", "wrong-password-xx", ip(30+i), nil)
	}
	if rec := e.login(t, "owner-b@example.com", exPassword, ip(33), nil); rec.Code != http.StatusOK {
		t.Fatalf("success under the limit: %d", rec.Code)
	}
	for i := 0; i < 2; i++ {
		e.login(t, "owner-b@example.com", "wrong-password-xx", ip(34+i), nil)
	}
	if rec := e.login(t, "owner-b@example.com", exPassword, ip(37), nil); rec.Code != http.StatusOK {
		t.Fatalf("success cleared the counter, the next login must pass: got %d", rec.Code)
	}

	// No clear-text address in Redis.
	ks, err := e.rdb.Keys(context.Background(), "ratelimit:*").Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(ks) == 0 {
		t.Fatal("expected lockout counters in Redis")
	}
	for _, k := range ks {
		if strings.Contains(strings.ToLower(k), "example.com") || strings.Contains(k, "multi") || strings.Contains(k, "ghost") {
			t.Fatalf("redis key %q leaks an e-mail", k)
		}
	}
}

func keys(m map[string]any) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// Scenario: Redis 故障 fail-open — the lockout never locks anyone out when the
// limiter cannot answer.
func TestAdminAccountLockoutFailsOpen(t *testing.T) {
	e := newExposureEnv(t, exposureOpts{lockout: 1, deadRedis: true})
	for i := 0; i < 3; i++ {
		e.login(t, "multi@example.com", "wrong-password-xx", fmt.Sprintf("203.0.113.%d:1", i+1), nil)
	}
	if rec := e.login(t, "multi@example.com", exPassword, "203.0.113.9:1", nil); rec.Code != http.StatusOK {
		t.Fatalf("limiter down must fail open: got %d %s", rec.Code, rec.Body.String())
	}
}

func refreshCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == httpapi.RefreshCookieName {
			return c
		}
	}
	return nil
}

// Scenario (authentication): cookie 模式登入 / 缺少防 CSRF 標頭 / 預設模式不變 +
// rotation and replay detection in cookie mode.
func TestAdminCookieRefreshFlow(t *testing.T) {
	e := newExposureEnv(t, exposureOpts{})
	const email = "multi@example.com"

	// Default mode: byte-for-byte what it always was.
	def := e.login(t, email, exPassword, "", nil)
	if def.Code != 200 {
		t.Fatalf("default login: %d", def.Code)
	}
	if got := keys(decodeBody(t, def)); got != "access_token,expires_in,refresh_token,token_type" {
		t.Fatalf("default response keys changed: %s", got)
	}
	if len(def.Result().Cookies()) != 0 || def.Header().Get("Set-Cookie") != "" {
		t.Fatal("default mode must not set any cookie")
	}
	if rec := e.login(t, email, exPassword, "", map[string]any{"refresh_transport": "body"}); keys(decodeBody(t, rec)) != keys(decodeBody(t, def)) {
		t.Fatal(`explicit "body" must equal the default`)
	}
	if rec := e.login(t, email, exPassword, "", map[string]any{"refresh_transport": "carrier-pigeon"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown transport: want 422, got %d", rec.Code)
	}

	// Cookie mode login.
	rec := e.login(t, email, exPassword, "", map[string]any{"refresh_transport": "cookie"})
	if rec.Code != 200 {
		t.Fatalf("cookie login: %d %s", rec.Code, rec.Body.String())
	}
	if got := keys(decodeBody(t, rec)); got != "access_token,expires_in,token_type" {
		t.Fatalf("cookie mode body must not carry refresh_token: %s", got)
	}
	c1 := refreshCookie(t, rec)
	if c1 == nil || c1.Value == "" {
		t.Fatal("cookie mode must set the refresh cookie")
	}
	if !c1.HttpOnly || !c1.Secure || c1.SameSite != http.SameSiteStrictMode || c1.Path != "/api/v1/admin/auth" ||
		c1.MaxAge != int((30*24*time.Hour).Seconds()) || c1.Name != "__Secure-admin_refresh" {
		t.Fatalf("cookie attributes wrong: %+v", c1)
	}

	// Scenario: 缺少防 CSRF 標頭 -> 401, token NOT rotated.
	noHeader := e.do(t, exReq{method: "POST", path: "/api/v1/admin/auth/refresh", body: map[string]any{}, cookies: []*http.Cookie{{Name: c1.Name, Value: c1.Value}}})
	if noHeader.Code != http.StatusUnauthorized || refreshCookie(t, noHeader) != nil {
		t.Fatalf("cookie refresh without X-Admin-Refresh: want 401 and no new cookie, got %d", noHeader.Code)
	}
	wrongHeader := e.do(t, exReq{method: "POST", path: "/api/v1/admin/auth/refresh", body: map[string]any{}, headers: map[string]string{"X-Admin-Refresh": "0"},
		cookies: []*http.Cookie{{Name: c1.Name, Value: c1.Value}}})
	if wrongHeader.Code != http.StatusUnauthorized {
		t.Fatalf("X-Admin-Refresh must be exactly 1: got %d", wrongHeader.Code)
	}

	// With the header the SAME cookie still works (it was not consumed above),
	// the cookie is rotated and the body carries no refresh token.
	good := e.do(t, exReq{method: "POST", path: "/api/v1/admin/auth/refresh", body: map[string]any{}, headers: map[string]string{"X-Admin-Refresh": "1"},
		cookies: []*http.Cookie{{Name: c1.Name, Value: c1.Value}}})
	if good.Code != 200 {
		t.Fatalf("cookie refresh: %d %s", good.Code, good.Body.String())
	}
	if got := keys(decodeBody(t, good)); got != "access_token,expires_in,token_type" {
		t.Fatalf("cookie refresh body: %s", got)
	}
	c2 := refreshCookie(t, good)
	if c2 == nil || c2.Value == "" || c2.Value == c1.Value || !c2.HttpOnly || !c2.Secure {
		t.Fatalf("refresh must overwrite the cookie with a new token: %+v", c2)
	}

	// Replay of the old cookie: 401, and the whole chain is revoked, so the
	// new cookie stops working too.
	replay := e.do(t, exReq{method: "POST", path: "/api/v1/admin/auth/refresh", body: map[string]any{}, headers: map[string]string{"X-Admin-Refresh": "1"},
		cookies: []*http.Cookie{{Name: c1.Name, Value: c1.Value}}})
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed cookie: want 401, got %d", replay.Code)
	}
	after := e.do(t, exReq{method: "POST", path: "/api/v1/admin/auth/refresh", body: map[string]any{}, headers: map[string]string{"X-Admin-Refresh": "1"},
		cookies: []*http.Cookie{{Name: c2.Name, Value: c2.Value}}})
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("reuse detection must revoke the chain in cookie mode: got %d", after.Code)
	}

	// Logout: needs the header too; clears the cookie.
	rec = e.login(t, email, exPassword, "", map[string]any{"refresh_transport": "cookie"})
	c3 := refreshCookie(t, rec)
	if out := e.do(t, exReq{method: "POST", path: "/api/v1/admin/auth/logout", body: map[string]any{}, cookies: []*http.Cookie{{Name: c3.Name, Value: c3.Value}}}); out.Code != http.StatusUnauthorized {
		t.Fatalf("cookie logout without header: want 401, got %d", out.Code)
	}
	out := e.do(t, exReq{method: "POST", path: "/api/v1/admin/auth/logout", body: map[string]any{}, headers: map[string]string{"X-Admin-Refresh": "1"},
		cookies: []*http.Cookie{{Name: c3.Name, Value: c3.Value}}})
	if out.Code != http.StatusNoContent {
		t.Fatalf("cookie logout: %d", out.Code)
	}
	if cleared := refreshCookie(t, out); cleared == nil || cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Fatalf("logout must expire the cookie: %+v", cleared)
	}
	if again := e.do(t, exReq{method: "POST", path: "/api/v1/admin/auth/refresh", body: map[string]any{}, headers: map[string]string{"X-Admin-Refresh": "1"},
		cookies: []*http.Cookie{{Name: c3.Name, Value: c3.Value}}}); again.Code != http.StatusUnauthorized {
		t.Fatalf("a logged-out token must be dead: got %d", again.Code)
	}

	// Body mode needs no header and still works exactly as before.
	tr := decodeBody(t, def)
	bodyRefresh := e.do(t, exReq{method: "POST", path: "/api/v1/admin/auth/refresh", body: map[string]any{"refresh_token": tr["refresh_token"]}})
	if bodyRefresh.Code != 200 || keys(decodeBody(t, bodyRefresh)) != "access_token,expires_in,refresh_token,token_type" || len(bodyRefresh.Result().Cookies()) != 0 {
		t.Fatalf("body-mode refresh changed: %d %s", bodyRefresh.Code, bodyRefresh.Body.String())
	}
}

// Scenario (Cloudflare Access): 繞過 Access 的請求被拒 / 錯誤 audience / 未設定時不影響,
// and CF_ACCESS_BIND_EMAIL end to end through the real router.
func TestCFAccessThroughRouter(t *testing.T) {
	e := newExposureEnv(t, exposureOpts{})
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer jwks.Close()

	const team, aud = "team-example.cloudflareaccess.com", "aud-for-tests"
	verifier := httpapi.NewCFAccessVerifier(config.CFAccessConfig{TeamDomain: team, Audience: aud}, slog.New(slog.DiscardHandler))
	verifier.SetJWKSURL(jwks.URL)
	log := slog.New(slog.DiscardHandler)
	engine := rbac.NewEngine(e.client, e.rdb, log)
	build := func(bind bool) http.Handler {
		deps := httpapi.Deps{
			Cfg: &config.Config{}, Log: log,
			AdminMW:       httpapi.NewAdminMiddleware(e.issuer),
			AdminAccessMW: httpapi.NewCFAccessMiddleware(verifier, log),
			AdminAuth: &httpapi.AdminAuthHandler{Client: e.client, Issuer: e.issuer,
				Refresh: auth.NewRefreshService(e.client, time.Hour), Engine: engine, Log: log},
		}
		if bind {
			deps.AdminBindEmailMW = httpapi.NewCFAccessBindEmailMiddleware(e.client, log)
		}
		return httpapi.New(deps)
	}
	cfToken := func(email, audience string) string {
		claims := jwt.MapClaims{"iss": "https://" + team, "aud": []string{audience}, "email": email,
			"exp": time.Now().Add(time.Hour).Unix()}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = "k1"
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	call := func(h http.Handler, path, cf, bearer string) int {
		req := httptest.NewRequest("GET", path, nil)
		if cf != "" {
			req.Header.Set(httpapi.CFAccessHeader, cf)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	login := func(h http.Handler, cf string) int {
		req := httptest.NewRequest("POST", "/api/v1/admin/auth/login",
			strings.NewReader(`{"email":"multi@example.com","password":"`+exPassword+`"}`))
		req.Header.Set("Content-Type", "application/json")
		if cf != "" {
			req.Header.Set(httpapi.CFAccessHeader, cf)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	h := build(false)
	if got := login(h, ""); got != http.StatusForbidden {
		t.Errorf("login without the Access header: want 403, got %d", got)
	}
	if got := login(h, cfToken("multi@example.com", "wrong-aud")); got != http.StatusForbidden {
		t.Errorf("wrong audience: want 403, got %d", got)
	}
	if got := login(h, cfToken("multi@example.com", aud)); got != http.StatusOK {
		t.Errorf("valid Access token: want 200, got %d", got)
	}
	tok := e.token(t, e.multi)
	if got := call(h, "/api/v1/admin/me", cfToken("anyone@example.com", aud), tok); got != http.StatusOK {
		t.Errorf("bind off: any Access identity passes, got %d", got)
	}

	// Bind on: the Access e-mail must equal the account's.
	hb := build(true)
	if got := call(hb, "/api/v1/admin/me", cfToken("MULTI@example.com", aud), tok); got != http.StatusOK {
		t.Errorf("bind on, matching e-mail (case-insensitive): want 200, got %d", got)
	}
	if got := call(hb, "/api/v1/admin/me", cfToken("someone-else@example.com", aud), tok); got != http.StatusForbidden {
		t.Errorf("bind on, different e-mail: want 403, got %d", got)
	}
	if got := call(hb, "/api/v1/admin/me", "", tok); got != http.StatusForbidden {
		t.Errorf("bind on, no Access token: want 403, got %d", got)
	}
}

// Scenario (design D9): 邀請連結基底依商家的 admin origin.
func TestInviteLinkBaseUsesShopAdminOrigin(t *testing.T) {
	e := newAcctEnv(t)
	ctx := context.Background()
	owner := e.mkMember(t, "owner-a@t.dev", e.shopA, "merchant_owner")
	staff := e.roleID(t, "staff")

	// No admin origin registered: the shared ADMIN_PUBLIC_URL.
	_, body, _ := e.invite(t, e.tokenFor(t, owner), e.shopA, "first@t.dev", staff)
	if u, _ := body["invite_url"].(string); !strings.HasPrefix(u, acctPublicURL+"/#/accept-invite/") {
		t.Fatalf("default base: %v", body["invite_url"])
	}
	// A storefront origin never becomes a link base.
	e.client.ShopAllowedOrigin.Create().SetShopID(e.shopA).SetOrigin("https://shop-a.example.com").SetPurpose("storefront").SaveX(ctx)
	// Two admin origins: the smallest id wins.
	e.client.ShopAllowedOrigin.Create().SetShopID(e.shopA).SetOrigin("https://admin.shop-a.example.com").SetPurpose("admin").SaveX(ctx)
	e.client.ShopAllowedOrigin.Create().SetShopID(e.shopA).SetOrigin("https://admin2.shop-a.example.com").SetPurpose("admin").SaveX(ctx)
	_, body, _ = e.do(t, "POST", e.invitePath(e.shopA, ""), e.tokenFor(t, owner), map[string]any{"email": "second@t.dev", "role_id": staff})
	if u, _ := body["invite_url"].(string); !strings.HasPrefix(u, "https://admin.shop-a.example.com/#/accept-invite/") {
		t.Fatalf("invite must point at the shop's own admin front end: %v", body["invite_url"])
	}
	// A new shop's owner setup link (platform creates the account) follows the same rule.
	admin := e.tokenFor(t, e.adminID)
	code, acc, raw := e.do(t, "POST", "/api/v1/admin/accounts", admin, map[string]any{
		"email": "newowner@t.dev", "shop_id": e.shopA, "role_id": e.roleID(t, "merchant_owner")})
	if code != http.StatusCreated {
		t.Fatalf("create account: %d %s", code, raw)
	}
	if u, _ := acc["setup_url"].(string); !strings.HasPrefix(u, "https://admin.shop-a.example.com/#/set-password/") {
		t.Fatalf("setup link base: %v", acc["setup_url"])
	}
	// A password reset (no shop context) keeps the shared base.
	code, rst, raw := e.do(t, "POST", fmt.Sprintf("/api/v1/admin/accounts/%d/password-reset", e.adminID), admin, nil)
	if code != http.StatusOK {
		t.Fatalf("reset: %d %s", code, raw)
	}
	if u, _ := rst["reset_url"].(string); !strings.HasPrefix(u, acctPublicURL+"/#/set-password/") {
		t.Fatalf("reset link base: %v", rst["reset_url"])
	}
}
