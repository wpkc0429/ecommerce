package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// clientAuthEnv exercises change storefront-client-auth end-to-end: the
// back-office key/origin APIs, the CORS grant they drive, and the guarantees
// the spec puts on the key itself (it identifies a client, it authorizes
// nothing, and it does not widen any quota).
//
// The dispatcher is wired exactly like api/internal/app/wire.go so cache
// invalidation travels the same path production uses.
type clientAuthEnv struct {
	client   *ent.Client
	rdb      *redis.Client
	resolver *tenant.Resolver
	router   http.Handler
	issuer   *auth.TokenIssuer

	shopA, shopB   int
	shopADomain    string
	ownerA, ownerB int // merchant_owner of shop A / shop B
	platform       int // platform role holding shop.manage_client_origins
	memberA        int
}

const platformOrigin = "http://localhost:3001" // stands in for the admin SPA

// clientAuthThresholds keeps the register broad rule at 2 so the "keys do not
// widen a quota" assertion needs only three requests. The window is long
// enough that no assertion can race it.
func clientAuthThresholds() config.RateLimitConfig {
	return config.RateLimitConfig{
		LoginBroadLimit: 50, LoginBroadWindow: time.Minute,
		LoginTightLimit: 50, LoginTightWindow: time.Minute,
		RegisterBroadLimit: 2, RegisterBroadWindow: time.Minute,
		RegisterTightLimit: 50, RegisterTightWindow: time.Minute,
		RefreshLimit: 50, RefreshWindow: time.Minute,
	}
}

func newClientAuthEnv(t *testing.T) *clientAuthEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &clientAuthEnv{client: client, rdb: rdb, shopADomain: "clientauth-a.test"}

	e.shopA = client.Shop.Create().SetName("Shop A").SetStatus(1).SaveX(ctx).ID
	e.shopB = client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(ctx).ID
	siteA := client.Site.Create().SetDomain(e.shopADomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(siteA.ID).SetShopID(e.shopA).SetIsPrimary(true).SaveX(ctx)

	e.memberA = client.Member.Create().SetShopID(e.shopA).SetEmail("ca-member@t.dev").SetStatus(1).SaveX(ctx).ID

	// Merchant-scoped nodes (design D11: key rotation is self-service).
	merchantNodes := []string{"publishable_key.view", "publishable_key.create", "publishable_key.revoke"}
	ownerRole := client.Role.Create().SetName("merchant_owner").SetScope("merchant").SaveX(ctx)
	for _, n := range merchantNodes {
		p := client.Permission.Create().SetName(n).SaveX(ctx)
		client.RolePermission.Create().SetRoleID(ownerRole.ID).SetPermissionID(p.ID).SaveX(ctx)
	}
	// Platform-scoped node (design D11: origins are a contested global
	// namespace, like bound domains).
	originPerm := client.Permission.Create().SetName("shop.manage_client_origins").SaveX(ctx)
	platformRole := client.Role.Create().SetName("super_admin").SetScope("platform").SaveX(ctx)
	client.RolePermission.Create().SetRoleID(platformRole.ID).SetPermissionID(originPerm.ID).SaveX(ctx)
	hash, _ := auth.HashPassword("password-123")
	e.ownerA = client.User.Create().SetEmail("ca-owner-a@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.ownerA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.ownerA).SetRoleID(ownerRole.ID).SetShopID(e.shopA).SaveX(ctx)

	e.ownerB = client.User.Create().SetEmail("ca-owner-b@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopB).SetUserID(e.ownerB).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.ownerB).SetRoleID(ownerRole.ID).SetShopID(e.shopB).SaveX(ctx)

	e.platform = client.User.Create().SetEmail("ca-platform@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
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

	dispatcher := events.NewDispatcher(log)
	dispatcher.Subscribe(func(ctx context.Context, ev events.Event) {
		if m, ok := ev.(events.StorefrontClientChanged); ok {
			e.resolver.InvalidateKeys(ctx, m.Keys...)
			e.resolver.InvalidateOrigins(ctx, m.Origins...)
		}
	})

	refresh := auth.NewRefreshService(client, 30*24*time.Hour)
	limiter := ratelimit.New(rdb, log)
	e.router = httpapi.New(httpapi.Deps{
		Cfg:                     &config.Config{CORSAllowedOrigins: []string{platformOrigin}},
		Log:                     log,
		AdminMW:                 httpapi.NewAdminMiddleware(issuer),
		TenantMW:                httpapi.NewTenantMiddleware(e.resolver),
		MemberMW:                httpapi.NewMemberMiddleware(issuer),
		CORSOrigins:             e.resolver,
		MemberAuth:              &httpapi.MemberAuthHandler{Client: client, Issuer: issuer, Refresh: refresh, Log: log},
		ClientAuth:              &httpapi.ClientAuthHandler{Client: client, Dispatcher: dispatcher, Authz: authz, Log: log},
		MemberRegisterRateLimit: httpapi.NewMemberRegisterRateLimit(limiter, clientAuthThresholds()),
	})
	return e
}

func (e *clientAuthEnv) adminCall(t *testing.T, userID int, method, path string, body any) (int, map[string]any) {
	t.Helper()
	tok, err := e.issuer.IssueAdmin(userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var parsed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, parsed
}

func (e *clientAuthEnv) shopPath(shopID int, parts string) string {
	return fmt.Sprintf("/api/v1/admin/shops/%d%s", shopID, parts)
}

// storefrontProbe hits a member-only endpoint with the given key and no member
// token. 401 means the tenant resolved (the key worked and authentication
// then, correctly, failed); 404 means the key itself was rejected.
func (e *clientAuthEnv) storefrontProbe(key string) (int, string) {
	req := httptest.NewRequest("GET", "/api/v1/shop/me", nil)
	req.Host = "unbound.example" // no Host fallback available
	if key != "" {
		req.Header.Set(httpapi.PublishableKeyHeader, key)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// TestPublishableKeyAdminLifecycle covers spec storefront-client-auth's key
// lifecycle: minting, listing in full, several live keys at once (rotation),
// and revocation taking effect immediately.
func TestPublishableKeyAdminLifecycle(t *testing.T) {
	e := newClientAuthEnv(t)

	code, first := e.adminCall(t, e.ownerA, "POST", e.shopPath(e.shopA, "/publishable-keys"),
		map[string]any{"label": "ondo-prod"})
	if code != http.StatusCreated {
		t.Fatalf("create key: want 201, got %d (%v)", code, first)
	}
	keyOne, _ := first["key"].(string)
	if !strings.HasPrefix(keyOne, "pk_") {
		t.Fatalf("key must carry the pk_ prefix, got %q", keyOne)
	}
	if first["label"] != "ondo-prod" || first["revoked_at"] != nil {
		t.Fatalf("unexpected create response: %v", first)
	}

	// Scenario: 輪替期間新舊金鑰並存 / 產生的金鑰互異且帶前綴.
	code, second := e.adminCall(t, e.ownerA, "POST", e.shopPath(e.shopA, "/publishable-keys"), map[string]any{})
	if code != http.StatusCreated {
		t.Fatalf("create second key: %d (%v)", code, second)
	}
	keyTwo, _ := second["key"].(string)
	if keyTwo == keyOne || !strings.HasPrefix(keyTwo, "pk_") {
		t.Fatalf("second key %q must differ from %q and carry the prefix", keyTwo, keyOne)
	}
	for _, k := range []string{keyOne, keyTwo} {
		if got, _ := e.storefrontProbe(k); got != http.StatusUnauthorized {
			t.Fatalf("live key %q should resolve the tenant (401 from member auth), got %d", k, got)
		}
	}

	// Scenario: 金鑰列表回應完整金鑰值 (design D1 — a public identifier is never
	// masked).
	code, listed := e.adminCall(t, e.ownerA, "GET", e.shopPath(e.shopA, "/publishable-keys"), nil)
	if code != http.StatusOK {
		t.Fatalf("list keys: %d (%v)", code, listed)
	}
	keys, _ := listed["keys"].([]any)
	if len(keys) != 2 {
		t.Fatalf("want 2 keys, got %v", listed)
	}
	for _, raw := range keys {
		row, _ := raw.(map[string]any)
		if v, _ := row["key"].(string); v != keyOne && v != keyTwo {
			t.Fatalf("listing must return key values in full, got %v", row)
		}
	}

	// Scenario: 撤銷舊金鑰後僅新金鑰有效 + 撤銷即時生效 (the probes above already
	// primed the resolver cache for both keys).
	keyID := int(first["id"].(float64))
	code, body := e.adminCall(t, e.ownerA, "DELETE", e.shopPath(e.shopA, fmt.Sprintf("/publishable-keys/%d", keyID)), nil)
	if code != http.StatusNoContent {
		t.Fatalf("revoke: want 204, got %d (%v)", code, body)
	}
	if got, raw := e.storefrontProbe(keyOne); got != http.StatusNotFound {
		t.Fatalf("revoked key must stop resolving immediately, got %d (%s)", got, raw)
	}
	if got, raw := e.storefrontProbe(keyTwo); got != http.StatusUnauthorized {
		t.Fatalf("the surviving key must keep working, got %d (%s)", got, raw)
	}

	// The revoked row stays listed (soft delete, design D4).
	_, listed = e.adminCall(t, e.ownerA, "GET", e.shopPath(e.shopA, "/publishable-keys"), nil)
	keys, _ = listed["keys"].([]any)
	var revokedSeen bool
	for _, raw := range keys {
		row, _ := raw.(map[string]any)
		if row["key"] == keyOne && row["revoked_at"] != nil {
			revokedSeen = true
		}
	}
	if len(keys) != 2 || !revokedSeen {
		t.Fatalf("revoked key must remain listed with revoked_at set: %v", listed)
	}

	// Scenario: 商家管理員跨店操作金鑰被拒 (cross-shop access guard).
	if code, _ := e.adminCall(t, e.ownerB, "GET", e.shopPath(e.shopA, "/publishable-keys"), nil); code != http.StatusForbidden {
		t.Fatalf("cross-shop list: want 403, got %d", code)
	}
	if code, _ := e.adminCall(t, e.ownerB, "DELETE", e.shopPath(e.shopA, fmt.Sprintf("/publishable-keys/%d", keyID)), nil); code != http.StatusForbidden {
		t.Fatalf("cross-shop revoke: want 403, got %d", code)
	}
	// A key id that belongs to no shop of this merchant is a 404, not a 500.
	if code, _ := e.adminCall(t, e.ownerA, "DELETE", e.shopPath(e.shopA, "/publishable-keys/999999"), nil); code != http.StatusNotFound {
		t.Fatalf("unknown key id: want 404, got %d", code)
	}
}

// TestAllowedOriginRegistrationAndCORS covers the per-shop CORS grant and the
// platform-scoped authorization around it (design D3/D5/D11).
func TestAllowedOriginRegistrationAndCORS(t *testing.T) {
	e := newClientAuthEnv(t)
	const ondo = "https://ondo.example"

	cors := func(method, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/healthz", nil)
		req.Header.Set("Origin", origin)
		if method == http.MethodOptions {
			// A real preflight announces the header by NAME only — its value
			// is not sent, which is the whole reason origin lookup cannot be
			// tenant-scoped (design D3).
			req.Header.Set("Access-Control-Request-Method", "POST")
			req.Header.Set("Access-Control-Request-Headers", httpapi.PublishableKeyHeader)
		}
		rec := httptest.NewRecorder()
		e.router.ServeHTTP(rec, req)
		return rec
	}

	// Scenario: 商家角色登記 origin 被拒 (platform-scoped, design D11).
	if code, _ := e.adminCall(t, e.ownerA, "POST", e.shopPath(e.shopA, "/allowed-origins"),
		map[string]any{"origin": ondo}); code != http.StatusForbidden {
		t.Fatalf("merchant registering an origin: want 403, got %d", code)
	}

	// Before registration the origin gets no grant (and the negative result is
	// cached — the deletion assertion at the end depends on invalidation
	// clearing it).
	if got := cors(http.MethodGet, ondo).Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unregistered origin must not be granted, got %q", got)
	}

	// Scenario: 平台角色登記 origin + 登記後跨源請求獲得授權.
	code, created := e.adminCall(t, e.platform, "POST", e.shopPath(e.shopA, "/allowed-origins"),
		map[string]any{"origin": ondo + "/"}) // trailing slash is normalized away
	if code != http.StatusCreated || created["origin"] != ondo {
		t.Fatalf("register origin: %d (%v)", code, created)
	}
	rec := cors(http.MethodGet, ondo)
	if rec.Header().Get("Access-Control-Allow-Origin") != ondo {
		t.Fatalf("registered origin must be granted immediately: %v", rec.Header())
	}
	if rec.Header().Get("Vary") != "Origin" {
		t.Fatalf("Vary: Origin is required for a per-origin grant: %v", rec.Header())
	}

	// Scenario: preflight 未帶金鑰仍放行 + Allow-Headers 含 X-Publishable-Key.
	pre := cors(http.MethodOptions, ondo)
	if pre.Code != http.StatusNoContent {
		t.Fatalf("preflight: want 204, got %d", pre.Code)
	}
	if pre.Header().Get("Access-Control-Allow-Origin") != ondo {
		t.Fatalf("preflight must be granted without any key: %v", pre.Header())
	}
	if !strings.Contains(pre.Header().Get("Access-Control-Allow-Headers"), httpapi.PublishableKeyHeader) {
		t.Fatalf("Allow-Headers must list %s, got %q", httpapi.PublishableKeyHeader, pre.Header().Get("Access-Control-Allow-Headers"))
	}

	// Scenario: 未登記的 origin 不獲授權 — and the platform env list still works
	// (design D5: platform front ends stay in configuration).
	if got := cors(http.MethodGet, "https://evil.example").Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unregistered origin granted: %q", got)
	}
	if got := cors(http.MethodGet, platformOrigin).Header().Get("Access-Control-Allow-Origin"); got != platformOrigin {
		t.Fatalf("platform env origin must keep working, got %q", got)
	}

	// Scenario: 重複登記被拒 (globally unique — another shop cannot claim it).
	if code, _ := e.adminCall(t, e.platform, "POST", e.shopPath(e.shopB, "/allowed-origins"),
		map[string]any{"origin": ondo}); code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate origin: want 422, got %d", code)
	}

	// Scenario: 非法 origin 被拒.
	for _, bad := range []string{"https://ondo.example/path", "*.ondo.example", "ftp://ondo.example", "ondo.example", ""} {
		if code, _ := e.adminCall(t, e.platform, "POST", e.shopPath(e.shopA, "/allowed-origins"),
			map[string]any{"origin": bad}); code != http.StatusUnprocessableEntity {
			t.Fatalf("origin %q: want 422, got %d", bad, code)
		}
	}

	// Listing is platform-scoped too.
	code, listed := e.adminCall(t, e.platform, "GET", e.shopPath(e.shopA, "/allowed-origins"), nil)
	if code != http.StatusOK {
		t.Fatalf("list origins: %d (%v)", code, listed)
	}
	origins, _ := listed["origins"].([]any)
	if len(origins) != 1 {
		t.Fatalf("want exactly the one registered origin, got %v", listed)
	}
	originID := int(origins[0].(map[string]any)["id"].(float64))

	// Scenario: 移除登記後不再授權 (cache invalidation, not TTL expiry).
	if code, body := e.adminCall(t, e.platform, "DELETE",
		e.shopPath(e.shopA, fmt.Sprintf("/allowed-origins/%d", originID)), nil); code != http.StatusNoContent {
		t.Fatalf("delete origin: want 204, got %d (%v)", code, body)
	}
	if got := cors(http.MethodGet, ondo).Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("deleted origin must lose its grant immediately, got %q", got)
	}
}

// TestPublishableKeyGrantsNoAuthority pins the spec's central invariant: the
// key identifies a client, it authorizes nothing (design D1/D2).
func TestPublishableKeyGrantsNoAuthority(t *testing.T) {
	e := newClientAuthEnv(t)

	code, created := e.adminCall(t, e.ownerA, "POST", e.shopPath(e.shopA, "/publishable-keys"), map[string]any{})
	if code != http.StatusCreated {
		t.Fatalf("create key: %d (%v)", code, created)
	}
	key := created["key"].(string)

	// Scenario: 金鑰不取代會員認證.
	if got, raw := e.storefrontProbe(key); got != http.StatusUnauthorized {
		t.Fatalf("key without a member token must be 401, got %d (%s)", got, raw)
	}

	// Scenario: 金鑰不影響 token audience 判定 — shop A's key plus a shop B token.
	tokB, err := e.issuer.IssueMember(e.memberA, e.shopB)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/shop/me", nil)
	req.Host = "unbound.example"
	req.Header.Set(httpapi.PublishableKeyHeader, key)
	req.Header.Set("Authorization", "Bearer "+tokB)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cross-shop token must stay 401 regardless of the key, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestPublishableKeyDoesNotWidenRateLimit pins design D9: quotas are keyed by
// shop, so minting more keys must not buy more requests.
func TestPublishableKeyDoesNotWidenRateLimit(t *testing.T) {
	e := newClientAuthEnv(t)

	mint := func(shopID, userID int) string {
		code, created := e.adminCall(t, userID, "POST", e.shopPath(shopID, "/publishable-keys"), map[string]any{})
		if code != http.StatusCreated {
			t.Fatalf("create key: %d (%v)", code, created)
		}
		return created["key"].(string)
	}
	keyA1, keyA2 := mint(e.shopA, e.ownerA), mint(e.shopA, e.ownerA)
	keyB := mint(e.shopB, e.ownerB)

	register := func(key, email string) int {
		body, _ := json.Marshal(map[string]string{"email": email, "password": "password-123"})
		req := httptest.NewRequest("POST", "/api/v1/shop/auth/register", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(httpapi.PublishableKeyHeader, key)
		req.RemoteAddr = "203.0.113.7:5555"
		rec := httptest.NewRecorder()
		e.router.ServeHTTP(rec, req)
		return rec.Code
	}

	// Two requests exhaust shop A's broad quota (limit 2)…
	for i, key := range []string{keyA1, keyA1} {
		if code := register(key, fmt.Sprintf("ca-rl-%d@t.dev", i)); code == http.StatusTooManyRequests {
			t.Fatalf("request %d should be within quota, got 429", i)
		}
	}
	// …and the second key does not get a fresh one.
	if code := register(keyA2, "ca-rl-other@t.dev"); code != http.StatusTooManyRequests {
		t.Fatalf("a second key must share shop A's quota: want 429, got %d", code)
	}
	// Cross-shop isolation is unaffected: shop B has its own counter.
	if code := register(keyB, "ca-rl-shopb@t.dev"); code == http.StatusTooManyRequests {
		t.Fatalf("shop B must not be affected by shop A's quota")
	}
}
