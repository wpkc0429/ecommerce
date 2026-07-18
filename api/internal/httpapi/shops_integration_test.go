package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	entpage "ksdevworks/ecommerce/api/internal/ent/page"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// shopsEnv exercises change platform-shop-crud: platform-only shop
// CRUD, gated by the existing three-tier RBAC decision (design D1).
type shopsEnv struct {
	client   *ent.Client
	rdb      *redis.Client
	router   http.Handler
	issuer   *auth.TokenIssuer
	resolver *tenant.Resolver

	platform int // holds shop.create/shop.list/shop.view/shop.update at platform scope
	shopA    int
	ownerA   int // merchant_owner of shop A (shop-scoped only, not a platform role)
}

func newShopsEnv(t *testing.T) *shopsEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &shopsEnv{client: client, rdb: rdb}

	permCreate := client.Permission.Create().SetName("shop.create").SaveX(ctx)
	permList := client.Permission.Create().SetName("shop.list").SaveX(ctx)
	permView := client.Permission.Create().SetName("shop.view").SaveX(ctx)
	permUpdate := client.Permission.Create().SetName("shop.update").SaveX(ctx)

	superRole := client.Role.Create().SetName("super_admin").SetScope("platform").SaveX(ctx)
	for _, p := range []*ent.Permission{permCreate, permList, permView, permUpdate} {
		client.RolePermission.Create().SetRoleID(superRole.ID).SetPermissionID(p.ID).SaveX(ctx)
	}
	// Merchant-scope role holding the *same-named* view/update permissions —
	// proves the platform routes don't leak via shop-scoped role grants
	// (spec multi-tenancy/Platform-only shop creation).
	ownerRole := client.Role.Create().SetName("merchant_owner").SetScope("merchant").SaveX(ctx)
	client.RolePermission.Create().SetRoleID(ownerRole.ID).SetPermissionID(permView.ID).SaveX(ctx)
	client.RolePermission.Create().SetRoleID(ownerRole.ID).SetPermissionID(permUpdate.ID).SaveX(ctx)

	hash, _ := auth.HashPassword("password-123")
	e.platform = client.User.Create().SetEmail("platform@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.RoleUser.Create().SetUserID(e.platform).SetRoleID(superRole.ID).SaveX(ctx)

	e.shopA = client.Shop.Create().SetName("Shop A").SetStatus(1).SaveX(ctx).ID
	e.ownerA = client.User.Create().SetEmail("owner-a@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.ownerA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.ownerA).SetRoleID(ownerRole.ID).SetShopID(e.shopA).SaveX(ctx)

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	engine := rbac.NewEngine(client, rdb, log)
	authz := &httpapi.AuthzMW{Engine: engine}
	e.resolver = tenant.NewResolver(client, rdb, log)
	dispatcher := events.NewDispatcher(log)
	dispatcher.Subscribe(func(ctx context.Context, ev events.Event) {
		if m, ok := ev.(events.SiteMappingChanged); ok {
			e.resolver.InvalidateHosts(ctx, m.Hosts...)
		}
	})
	cmsService := &cms.Service{Client: client, Dispatcher: dispatcher}
	e.router = httpapi.New(httpapi.Deps{
		Cfg:     &config.Config{},
		Log:     log,
		AdminMW: httpapi.NewAdminMiddleware(issuer),
		Shops:   &httpapi.ShopsHandler{Client: client, Service: cmsService, Authz: authz, Log: log},
	})
	return e
}

func (e *shopsEnv) call(t *testing.T, userID int, method, path string, body any) (int, string) {
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
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// Scenario: 平台角色建店成功並自動建首頁 (spec multi-tenancy/Platform-only
// shop creation).
func TestCreateShopAutoCreatesHomePage(t *testing.T) {
	e := newShopsEnv(t)
	ctx := context.Background()

	code, body := e.call(t, e.platform, "POST", "/api/v1/admin/shops", map[string]any{"name": "New Shop"})
	if code != http.StatusCreated {
		t.Fatalf("create shop: %d %s", code, body)
	}
	var created struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if created.Name != "New Shop" {
		t.Fatalf("unexpected name: %q", created.Name)
	}

	exists, err := e.client.Page.Query().
		Where(entpage.ShopIDEQ(created.ID), entpage.SlugEQ("home"), entpage.StatusEQ(0)).
		Exist(ctx)
	if err != nil {
		t.Fatalf("query home page: %v", err)
	}
	if !exists {
		t.Fatal("home page was not auto-created (spec page-management/Auto-created home page)")
	}
}

func TestCreateShopValidation(t *testing.T) {
	e := newShopsEnv(t)

	if code, _ := e.call(t, e.platform, "POST", "/api/v1/admin/shops", map[string]any{"name": ""}); code != http.StatusUnprocessableEntity {
		t.Fatalf("empty name: want 422, got %d", code)
	}
	if code, _ := e.call(t, e.platform, "POST", "/api/v1/admin/shops",
		map[string]any{"name": "X", "theme_id": 999999}); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown theme: want 422, got %d", code)
	}
}

// Scenario: 商家自身角色呼叫被拒 — the target shop's own merchant_owner (a
// shop-scoped role, not a platform role) must not reach any platform route,
// even though the role happens to grant identically-named permissions.
func TestShopPlatformRoutesRejectMerchantRole(t *testing.T) {
	e := newShopsEnv(t)

	if code, _ := e.call(t, e.ownerA, "POST", "/api/v1/admin/shops", map[string]any{"name": "x"}); code != http.StatusForbidden {
		t.Fatalf("create: want 403, got %d", code)
	}
	if code, _ := e.call(t, e.ownerA, "GET", "/api/v1/admin/shops", nil); code != http.StatusForbidden {
		t.Fatalf("list: want 403, got %d", code)
	}
	if code, _ := e.call(t, e.ownerA, "GET", fmt.Sprintf("/api/v1/admin/shops/%d", e.shopA), nil); code != http.StatusForbidden {
		t.Fatalf("get: want 403, got %d", code)
	}
	if code, _ := e.call(t, e.ownerA, "PUT", fmt.Sprintf("/api/v1/admin/shops/%d", e.shopA), map[string]any{"name": "y"}); code != http.StatusForbidden {
		t.Fatalf("update: want 403, got %d", code)
	}
}

// Scenario: 合法 status 轉換 / 非法 status 值被拒.
func TestUpdateShopStatusTransition(t *testing.T) {
	e := newShopsEnv(t)
	ctx := context.Background()

	code, body := e.call(t, e.platform, "PUT", fmt.Sprintf("/api/v1/admin/shops/%d", e.shopA), map[string]any{"status": 0})
	if code != http.StatusOK {
		t.Fatalf("valid transition: %d %s", code, body)
	}
	s, err := e.client.Shop.Get(ctx, e.shopA)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != 0 {
		t.Fatalf("status not persisted: got %d", s.Status)
	}

	code, _ = e.call(t, e.platform, "PUT", fmt.Sprintf("/api/v1/admin/shops/%d", e.shopA), map[string]any{"status": 9})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid status: want 422, got %d", code)
	}
	s2, err := e.client.Shop.Get(ctx, e.shopA)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Status != 0 {
		t.Fatalf("status changed on rejected input: got %d", s2.Status)
	}
}

// Scenario: 分頁列表正確性.
func TestListShopsPagination(t *testing.T) {
	e := newShopsEnv(t)
	ctx := context.Background()

	// e.shopA already exists; create 29 more for a round total of 30.
	for i := 0; i < 29; i++ {
		e.client.Shop.Create().SetName(fmt.Sprintf("Shop %02d", i)).SetStatus(1).SaveX(ctx)
	}

	type listResp struct {
		Shops []struct {
			ID int `json:"id"`
		} `json:"shops"`
		Total    int `json:"total"`
		Page     int `json:"page"`
		PageSize int `json:"page_size"`
	}

	code, body := e.call(t, e.platform, "GET", "/api/v1/admin/shops?page=1&page_size=10", nil)
	if code != http.StatusOK {
		t.Fatalf("list page 1: %d %s", code, body)
	}
	var p1 listResp
	if err := json.Unmarshal([]byte(body), &p1); err != nil {
		t.Fatal(err)
	}
	if len(p1.Shops) != 10 || p1.Total != 30 || p1.Page != 1 || p1.PageSize != 10 {
		t.Fatalf("page 1 unexpected: %+v", p1)
	}

	_, body2 := e.call(t, e.platform, "GET", "/api/v1/admin/shops?page=2&page_size=10", nil)
	var p2 listResp
	if err := json.Unmarshal([]byte(body2), &p2); err != nil {
		t.Fatal(err)
	}
	if len(p2.Shops) != 10 || p2.Page != 2 {
		t.Fatalf("page 2 unexpected: %+v", p2)
	}

	seen := make(map[int]bool, len(p1.Shops))
	for _, s := range p1.Shops {
		seen[s.ID] = true
	}
	for _, s := range p2.Shops {
		if seen[s.ID] {
			t.Fatalf("shop %d present on both pages", s.ID)
		}
	}
}

// Scenario (design D5): status 轉換立即使 route resolution cache 失效.
func TestUpdateShopStatusInvalidatesRouteCache(t *testing.T) {
	e := newShopsEnv(t)
	ctx := context.Background()

	site := e.client.Site.Create().SetDomain("shopa.test").SaveX(ctx)
	e.client.SiteShop.Create().SetSiteID(site.ID).SetShopID(e.shopA).SetIsPrimary(true).SaveX(ctx)

	// Prime the route cache the way the resolver would on first request.
	if _, err := e.resolver.Resolve(ctx, "shopa.test", "/"); err != nil {
		t.Fatalf("prime cache: %v", err)
	}
	if n, err := e.rdb.Exists(ctx, "route:shopa.test").Result(); err != nil || n != 1 {
		t.Fatalf("route cache not primed: n=%d err=%v", n, err)
	}

	code, body := e.call(t, e.platform, "PUT", fmt.Sprintf("/api/v1/admin/shops/%d", e.shopA), map[string]any{"status": 0})
	if code != http.StatusOK {
		t.Fatalf("update status: %d %s", code, body)
	}

	if n, err := e.rdb.Exists(ctx, "route:shopa.test").Result(); err != nil || n != 0 {
		t.Fatalf("route cache key still present after status change: n=%d err=%v", n, err)
	}
	// Confirm it takes effect immediately, not just that the key is gone.
	if _, err := e.resolver.Resolve(ctx, "shopa.test", "/"); err != tenant.ErrShopDisabled {
		t.Fatalf("resolve after disable: %v", err)
	}
}
