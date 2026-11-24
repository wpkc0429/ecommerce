package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/member"
	"ksdevworks/ecommerce/api/internal/ent/publishablekey"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

type tenancyEnv struct {
	client   *ent.Client
	resolver *tenant.Resolver
	router   http.Handler
	issuer   *auth.TokenIssuer
	admin    int // platform admin user id

	active, disabled, review, brandA int // shop ids
}

func newTenancyEnv(t *testing.T) *tenancyEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &tenancyEnv{client: client}

	e.active = client.Shop.Create().SetName("Active").SetStatus(1).SaveX(ctx).ID
	e.disabled = client.Shop.Create().SetName("Disabled").SetStatus(0).SaveX(ctx).ID
	e.review = client.Shop.Create().SetName("Review").SetStatus(2).SaveX(ctx).ID
	e.brandA = client.Shop.Create().SetName("Brand A").SetStatus(1).SaveX(ctx).ID

	// shop1.test → default active shop + /brand-a prefix shop.
	s1 := client.Site.Create().SetDomain("shop1.test").SaveX(ctx)
	client.SiteShop.Create().SetSiteID(s1.ID).SetShopID(e.active).SetIsPrimary(true).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(s1.ID).SetShopID(e.brandA).SetPathPrefix("/brand-a").SaveX(ctx)
	// disabled.test → disabled shop; review.test → shop under review.
	s2 := client.Site.Create().SetDomain("disabled.test").SaveX(ctx)
	client.SiteShop.Create().SetSiteID(s2.ID).SetShopID(e.disabled).SaveX(ctx)
	s3 := client.Site.Create().SetDomain("review.test").SaveX(ctx)
	client.SiteShop.Create().SetSiteID(s3.ID).SetShopID(e.review).SaveX(ctx)
	// noshops.test → site without any mapping.
	client.Site.Create().SetDomain("noshops.test").SaveX(ctx)

	// Platform admin for the sites API.
	perm := client.Permission.Create().SetName("shop.manage_domains").SaveX(ctx)
	superRole := client.Role.Create().SetName("super_admin").SetScope("platform").SaveX(ctx)
	client.RolePermission.Create().SetRoleID(superRole.ID).SetPermissionID(perm.ID).SaveX(ctx)
	hash, _ := auth.HashPassword("password-123")
	e.admin = client.User.Create().SetEmail("root@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.RoleUser.Create().SetUserID(e.admin).SetRoleID(superRole.ID).SaveX(ctx)

	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	e.resolver = tenant.NewResolver(client, rdb, log)
	dispatcher := events.NewDispatcher(log)
	dispatcher.Subscribe(func(ctx context.Context, ev events.Event) {
		if m, ok := ev.(events.ShopStatusChanged); ok {
			e.resolver.InvalidateHosts(ctx, m.Hosts...)
		}
	})

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	engine := rbac.NewEngine(client, rdb, log)
	authz := &httpapi.AuthzMW{Engine: engine}
	e.router = httpapi.New(httpapi.Deps{
		Cfg:     &config.Config{},
		Log:     log,
		AdminMW: httpapi.NewAdminMiddleware(issuer),
		Sites:   &httpapi.SitesHandler{Client: client, Dispatcher: dispatcher, Log: log, Authz: authz},
	})
	return e
}

// Task 5.5: 多網域/多前綴/預設商家/停用商家情境.
func TestResolveScenarios(t *testing.T) {
	e := newTenancyEnv(t)
	ctx := context.Background()

	// Default shop on bound domain (case-insensitive, port stripped).
	res, err := e.resolver.Resolve(ctx, "SHOP1.test:8443", "/about")
	if err != nil || res.ShopID != e.active || res.Path != "/about" {
		t.Fatalf("default resolution: res=%+v err=%v", res, err)
	}
	// Prefix hit → brand shop, prefix stripped.
	res, err = e.resolver.Resolve(ctx, "shop1.test", "/brand-a/about")
	if err != nil || res.ShopID != e.brandA || res.Path != "/about" {
		t.Fatalf("prefix resolution: res=%+v err=%v", res, err)
	}
	// Unknown domain → 404.
	if _, err := e.resolver.Resolve(ctx, "unknown.test", "/"); !errors.Is(err, tenant.ErrSiteNotFound) {
		t.Fatalf("unknown domain: %v", err)
	}
	// Site without default shop → 404.
	if _, err := e.resolver.Resolve(ctx, "noshops.test", "/x"); !errors.Is(err, tenant.ErrNoDefaultShop) {
		t.Fatalf("no default shop: %v", err)
	}
	// Disabled shop → 503; review shop → 404.
	if _, err := e.resolver.Resolve(ctx, "disabled.test", "/"); !errors.Is(err, tenant.ErrShopDisabled) {
		t.Fatalf("disabled shop: %v", err)
	}
	if _, err := e.resolver.Resolve(ctx, "review.test", "/"); !errors.Is(err, tenant.ErrShopUnderReview) {
		t.Fatalf("review shop: %v", err)
	}
}

func (e *tenancyEnv) adminCall(t *testing.T, method, path string, body any) (int, string) {
	t.Helper()
	tok, err := e.issuer.IssueAdmin(e.admin, nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// Spec multi-tenancy/Site-shop mapping constraints + Route resolution cache.
func TestSitesAPIConstraintsAndInvalidation(t *testing.T) {
	e := newTenancyEnv(t)
	ctx := context.Background()

	// Duplicate default shop → 422 (constraint-backed).
	code, body := e.adminCall(t, "POST", "/api/v1/admin/sites/1/shops",
		map[string]any{"shop_id": e.brandA})
	if code != 422 {
		t.Fatalf("duplicate default shop: want 422, got %d (%s)", code, body)
	}
	// Duplicate primary domain for the active shop → 422.
	code, _ = e.adminCall(t, "POST", "/api/v1/admin/sites",
		map[string]any{"domain": "second.test"})
	if code != 201 {
		t.Fatalf("create site: %d", code)
	}
	var sites struct {
		Sites []struct {
			ID     int    `json:"id"`
			Domain string `json:"domain"`
		} `json:"sites"`
	}
	_, listBody := e.adminCall(t, "GET", "/api/v1/admin/sites", nil)
	_ = json.Unmarshal([]byte(listBody), &sites)
	var secondID int
	for _, s := range sites.Sites {
		if s.Domain == "second.test" {
			secondID = s.ID
		}
	}
	code, body = e.adminCall(t, "POST", fmt.Sprintf("/api/v1/admin/sites/%d/shops", secondID),
		map[string]any{"shop_id": e.active, "is_primary": true})
	if code != 422 {
		t.Fatalf("duplicate primary domain: want 422, got %d (%s)", code, body)
	}
	// Duplicate prefix on the same site → 422.
	code, body = e.adminCall(t, "POST", "/api/v1/admin/sites/1/shops",
		map[string]any{"shop_id": e.disabled, "path_prefix": "/brand-a"})
	if code != 422 {
		t.Fatalf("duplicate prefix: want 422, got %d (%s)", code, body)
	}
	// Invalid prefix charset → 422.
	code, _ = e.adminCall(t, "POST", "/api/v1/admin/sites/1/shops",
		map[string]any{"shop_id": e.disabled, "path_prefix": "/Bad_Prefix!"})
	if code != 422 {
		t.Fatalf("invalid prefix: want 422, got %d", code)
	}

	// ── Route cache invalidation (spec: 對應異動後立即生效) ──
	// Prime the cache for second.test (no default shop yet → 404).
	if _, err := e.resolver.Resolve(ctx, "second.test", "/"); !errors.Is(err, tenant.ErrNoDefaultShop) {
		t.Fatalf("prime: %v", err)
	}
	// Bind the brand shop as default via the API (publishes ShopStatusChanged).
	code, body = e.adminCall(t, "POST", fmt.Sprintf("/api/v1/admin/sites/%d/shops", secondID),
		map[string]any{"shop_id": e.brandA})
	if code != 201 {
		t.Fatalf("bind default: %d (%s)", code, body)
	}
	// Without waiting for the 5-minute TTL, the next resolution sees it.
	res, err := e.resolver.Resolve(ctx, "second.test", "/")
	if err != nil || res.ShopID != e.brandA {
		t.Fatalf("post-invalidation resolution: res=%+v err=%v", res, err)
	}
}

// The tenant middleware end-to-end: Host channel + status gating.
//
// change storefront-client-auth (design D6): the storefront group no longer
// reads X-Site-Domain / X-Site-Path at all, so the Host is the only
// keyless channel left — the "header wins over Host" case below is inverted
// into "the header is ignored".
func TestTenantMiddlewareHTTP(t *testing.T) {
	e := newTenancyEnv(t)

	var gotShop int
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotShop, _ = tenant.ShopID(r.Context())
		w.WriteHeader(200)
	})
	mw := httpapi.NewTenantMiddleware(e.resolver)(inner)

	cases := []struct {
		name     string
		host     string
		want     int
		wantShop int
	}{
		{"Host resolves to the site's default shop", "shop1.test", 200, e.active},
		{"unknown domain 404", "nope.test", 404, 0},
		{"disabled shop 503", "disabled.test", 503, 0},
		{"review shop 404", "review.test", 404, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotShop = 0
			req := httptest.NewRequest("POST", "/api/v1/shop/auth/login", nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.wantShop != 0 && gotShop != tc.wantShop {
				t.Fatalf("shop ctx %d, want %d", gotShop, tc.wantShop)
			}
		})
	}

	// Scenario (multi-tenancy): 前台 API 忽略 X-Site-Domain. The header names a
	// disabled shop, which would have produced a 503 before this change; the
	// Host wins because the header is no longer read at all.
	t.Run("X-Site-Domain is ignored by the storefront group", func(t *testing.T) {
		gotShop = 0
		req := httptest.NewRequest("POST", "/api/v1/shop/auth/login", nil)
		req.Host = "shop1.test"
		req.Header.Set("X-Site-Domain", "disabled.test")
		req.Header.Set("X-Site-Path", "/brand-a")
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if rec.Code != 200 || gotShop != e.active {
			t.Fatalf("status=%d shop=%d, want 200/%d (%s)", rec.Code, gotShop, e.active, rec.Body.String())
		}
	})
}

// TestPublishableKeyTenantResolution pins spec storefront-client-auth's
// resolution requirements at the middleware boundary: the key wins over Host,
// an unusable key never falls back, and unknown/malformed/revoked keys are
// indistinguishable.
func TestPublishableKeyTenantResolution(t *testing.T) {
	e := newTenancyEnv(t)
	ctx := context.Background()

	mint := func(shopID int) string {
		v, err := tenant.NewPublishableKey()
		if err != nil {
			t.Fatal(err)
		}
		e.client.PublishableKey.Create().SetShopID(shopID).SetKey(v).SaveX(ctx)
		return v
	}
	activeKey := mint(e.active)
	brandKey := mint(e.brandA)
	disabledKey := mint(e.disabled)
	reviewKey := mint(e.review)
	revokedKey := mint(e.active)
	e.client.PublishableKey.Update().
		Where(publishablekey.KeyEQ(revokedKey)).SetRevokedAt(time.Now()).ExecX(ctx)

	var gotShop int
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotShop, _ = tenant.ShopID(r.Context())
		w.WriteHeader(200)
	})
	mw := httpapi.NewTenantMiddleware(e.resolver)(inner)

	call := func(key, host string) (int, string, int) {
		gotShop = 0
		req := httptest.NewRequest("POST", "/api/v1/shop/auth/login", nil)
		req.Host = host
		if key != "" {
			req.Header.Set(httpapi.PublishableKeyHeader, key)
		}
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String(), gotShop
	}

	// Scenario: 有效金鑰解析出商家 — including a shop that has no domain of its
	// own on this site's default mapping (brandA is prefix-only).
	if code, body, shop := call(activeKey, "irrelevant.example"); code != 200 || shop != e.active {
		t.Fatalf("active key: code=%d shop=%d (%s)", code, shop, body)
	}
	if code, body, shop := call(brandKey, "irrelevant.example"); code != 200 || shop != e.brandA {
		t.Fatalf("prefix-shop key: code=%d shop=%d (%s)", code, shop, body)
	}

	// Scenario: 金鑰優先於 Host — the Host would resolve to a different shop.
	if code, body, shop := call(activeKey, "review.test"); code != 200 || shop != e.active {
		t.Fatalf("key must win over Host: code=%d shop=%d (%s)", code, shop, body)
	}

	// Scenario: 已撤銷金鑰被拒且不回退 — the Host alone would have been a 200.
	revokedCode, revokedBody, shop := call(revokedKey, "shop1.test")
	if revokedCode != 404 || shop != 0 {
		t.Fatalf("revoked key must 404 without falling back: code=%d shop=%d (%s)", revokedCode, shop, revokedBody)
	}

	// Scenario: 未知金鑰與已撤銷金鑰的回應同構 (malformed too — every unusable
	// key looks the same from outside).
	unknownCode, unknownBody, _ := call("pk_zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", "shop1.test")
	malformedCode, malformedBody, _ := call("not-a-key", "shop1.test")
	if unknownCode != revokedCode || malformedCode != revokedCode {
		t.Fatalf("statuses must be identical: revoked=%d unknown=%d malformed=%d", revokedCode, unknownCode, malformedCode)
	}
	if unknownBody != revokedBody || malformedBody != revokedBody {
		t.Fatalf("bodies must be identical:\nrevoked=%s\nunknown=%s\nmalformed=%s", revokedBody, unknownBody, malformedBody)
	}
	var parsed map[string]any
	_ = json.Unmarshal([]byte(revokedBody), &parsed)
	if errObj, _ := parsed["error"].(map[string]any); errObj["code"] != "unknown_publishable_key" {
		t.Fatalf("want the dedicated error code, got %v", parsed)
	}

	// Scenario: 停用商家的金鑰 / 審核中商家的金鑰 — same gating as the domain path.
	if code, body, _ := call(disabledKey, "shop1.test"); code != 503 {
		t.Fatalf("disabled shop key: want 503, got %d (%s)", code, body)
	}
	if code, body, _ := call(reviewKey, "shop1.test"); code != 404 {
		t.Fatalf("under-review shop key: want 404, got %d (%s)", code, body)
	}

	// Scenario: 撤銷即時生效 — prime the cache with a live key, revoke it, then
	// invalidate through the same call the domain-event subscriber makes.
	rotating := mint(e.active)
	if code, _, _ := call(rotating, "irrelevant.example"); code != 200 {
		t.Fatalf("priming call: %d", code)
	}
	e.client.PublishableKey.Update().
		Where(publishablekey.KeyEQ(rotating)).SetRevokedAt(time.Now()).ExecX(ctx)
	e.resolver.InvalidateKeys(ctx, rotating)
	if code, body, _ := call(rotating, "irrelevant.example"); code != 404 {
		t.Fatalf("revocation must take effect without waiting for the TTL: %d (%s)", code, body)
	}

	// Scenario: 金鑰不接受查詢參數.
	req := httptest.NewRequest("POST", "/api/v1/shop/auth/login?publishable_key="+activeKey, nil)
	req.Host = "nope.test"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("query-parameter key must not resolve a tenant: %d (%s)", rec.Code, rec.Body.String())
	}
}

// ── change member-identity-per-shop: members is tenant-owned data ──────

// TestMemberIdentityIsShopScoped pins spec member-identity at the data layer:
// identity is per-shop, NULL email/phone stay distinct within a shop (design
// D3), the tenant interceptor scopes members automatically, and a shop's
// customer set is fully delimited by shop_id.
func TestMemberIdentityIsShopScoped(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()

	shopA := client.Shop.Create().SetName("Identity A").SetStatus(1).SaveX(ctx).ID
	shopB := client.Shop.Create().SetName("Identity B").SetStatus(1).SaveX(ctx).ID

	// Scenario: 同一 email 於兩家商家為獨立身分.
	a := client.Member.Create().SetShopID(shopA).SetEmail("same@t.dev").SetPasswordHash("hash-a").SetStatus(1).SaveX(ctx)
	b := client.Member.Create().SetShopID(shopB).SetEmail("same@t.dev").SetPasswordHash("hash-b").SetStatus(1).SaveX(ctx)
	if a.ID == b.ID || *a.PasswordHash == *b.PasswordHash {
		t.Fatalf("expected two independent identities, got %d/%d", a.ID, b.ID)
	}

	// Scenario: 同商家內 email 重複被拒.
	if _, err := client.Member.Create().SetShopID(shopA).SetEmail("same@t.dev").SetStatus(1).Save(ctx); err == nil {
		t.Fatal("expected UNIQUE(shop_id, email) to reject a duplicate within one shop")
	}

	// Scenario: 同商家內多筆 email 為 NULL 被允許 — Postgres's default NULLS
	// DISTINCT semantics, which design D3 requires this index to keep (a
	// NULLS NOT DISTINCT index would reject the second row here).
	client.Member.Create().SetShopID(shopA).SetPhone("0900000001").SetStatus(1).SaveX(ctx)
	client.Member.Create().SetShopID(shopA).SetPhone("0900000002").SetStatus(1).SaveX(ctx)
	// …and the same for phone: two rows with both email and phone NULL.
	client.Member.Create().SetShopID(shopA).SetStatus(1).SaveX(ctx)
	client.Member.Create().SetShopID(shopA).SetStatus(1).SaveX(ctx)
	if n := client.Member.Query().Where(member.ShopIDEQ(shopA), member.EmailIsNil()).CountX(ctx); n != 4 {
		t.Fatalf("expected 4 email-NULL identities in one shop, got %d", n)
	}

	// Scenario: 租戶上下文自動範圍化顧客查詢 — no explicit shop predicate here.
	tenantA := tenant.WithShopID(ctx, shopA)
	rows := client.Member.Query().AllX(tenantA)
	if len(rows) != 5 {
		t.Fatalf("tenant-scoped query returned %d rows, want shop A's 5", len(rows))
	}
	for _, m := range rows {
		if m.ShopID != shopA {
			t.Fatalf("tenant-scoped query leaked a shop %d row", m.ShopID)
		}
	}

	// Scenario: 刪除單一商家顧客資料不影響其他商家 (資料集自我完備). The delete
	// carries no predicate at all — the tenant hook confines it to shop A.
	if n := client.Member.Delete().ExecX(tenantA); n != 5 {
		t.Fatalf("tenant-scoped delete removed %d rows, want shop A's 5", n)
	}
	if n := client.Member.Query().Where(member.ShopIDEQ(shopA)).CountX(ctx); n != 0 {
		t.Fatalf("shop A still has %d customers", n)
	}
	survivor, err := client.Member.Query().Where(member.ShopIDEQ(shopB)).Only(ctx)
	if err != nil {
		t.Fatalf("shop B's customer must be untouched: %v", err)
	}
	if survivor.ID != b.ID || survivor.Email == nil || *survivor.Email != "same@t.dev" {
		t.Fatalf("shop B's customer changed: %+v", survivor)
	}
}
