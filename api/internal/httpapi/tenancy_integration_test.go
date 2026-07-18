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
		if m, ok := ev.(events.SiteMappingChanged); ok {
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
	// Bind the brand shop as default via the API (publishes SiteMappingChanged).
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

// The tenant middleware end-to-end: header channel + status gating.
func TestTenantMiddlewareHTTP(t *testing.T) {
	e := newTenancyEnv(t)
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	var gotShop int
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotShop, _ = tenant.ShopID(r.Context())
		w.WriteHeader(200)
	})
	mw := httpapi.NewTenantMiddleware(e.resolver)(inner)
	_ = log

	cases := []struct {
		name     string
		domain   string
		path     string
		want     int
		wantShop int
	}{
		{"X-Site-Domain wins over Host", "shop1.test", "/", 200, e.active},
		{"prefix via X-Site-Path", "shop1.test", "/brand-a/cart", 200, e.brandA},
		{"unknown domain 404", "nope.test", "/", 404, 0},
		{"disabled shop 503", "disabled.test", "/", 503, 0},
		{"review shop 404", "review.test", "/", 404, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotShop = 0
			req := httptest.NewRequest("POST", "/api/v1/shop/auth/login", nil)
			req.Host = "irrelevant.example" // X-Site-Domain must take precedence
			req.Header.Set("X-Site-Domain", tc.domain)
			req.Header.Set("X-Site-Path", tc.path)
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
}
