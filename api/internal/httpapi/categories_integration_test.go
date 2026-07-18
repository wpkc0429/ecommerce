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
	"ksdevworks/ecommerce/api/internal/catalog"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// catalogEnv exercises change product-catalog: merchant-scoped category and
// product management, shared by categories_integration_test.go and
// products_integration_test.go. Wires the same permission/role shape as
// pages (merchant_owner: full CRUD; editor: no delete).
type catalogEnv struct {
	client   *ent.Client
	rdb      *redis.Client
	router   http.Handler
	issuer   *auth.TokenIssuer
	resolver *tenant.Resolver

	shopA, shopB int
	ownerA       int // merchant_owner of shop A: full category/product CRUD
	editorA      int // editor of shop A: no delete
	ownerB       int // merchant_owner of shop B (cross-shop isolation checks)
	shopADomain  string
}

func newCatalogEnv(t *testing.T) *catalogEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &catalogEnv{client: client, rdb: rdb, shopADomain: "catalog-a.test"}

	perms := []string{
		"category.view", "category.create", "category.edit", "category.delete",
		"product.view", "product.create", "product.edit", "product.delete",
	}
	permIDs := map[string]int{}
	for _, p := range perms {
		permIDs[p] = client.Permission.Create().SetName(p).SaveX(ctx).ID
	}

	ownerRole := client.Role.Create().SetName("merchant_owner").SetScope("merchant").SaveX(ctx)
	for _, p := range perms {
		client.RolePermission.Create().SetRoleID(ownerRole.ID).SetPermissionID(permIDs[p]).SaveX(ctx)
	}
	editorRole := client.Role.Create().SetName("editor").SetScope("merchant").SaveX(ctx)
	for _, p := range perms {
		if p == "category.delete" || p == "product.delete" {
			continue
		}
		client.RolePermission.Create().SetRoleID(editorRole.ID).SetPermissionID(permIDs[p]).SaveX(ctx)
	}

	e.shopA = client.Shop.Create().SetName("Shop A").SetStatus(1).SaveX(ctx).ID
	e.shopB = client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(ctx).ID

	site := client.Site.Create().SetDomain(e.shopADomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(site.ID).SetShopID(e.shopA).SetIsPrimary(true).SaveX(ctx)

	hash, _ := auth.HashPassword("password-123")
	e.ownerA = client.User.Create().SetEmail("owner-a@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.ownerA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.ownerA).SetRoleID(ownerRole.ID).SetShopID(e.shopA).SaveX(ctx)

	e.editorA = client.User.Create().SetEmail("editor-a@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.editorA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.editorA).SetRoleID(editorRole.ID).SetShopID(e.shopA).SaveX(ctx)

	e.ownerB = client.User.Create().SetEmail("owner-b@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopB).SetUserID(e.ownerB).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.ownerB).SetRoleID(ownerRole.ID).SetShopID(e.shopB).SaveX(ctx)

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	engine := rbac.NewEngine(client, rdb, log)
	authz := &httpapi.AuthzMW{Engine: engine}
	e.resolver = tenant.NewResolver(client, rdb, log)

	catalogService := &catalog.Service{Client: client}
	e.router = httpapi.New(httpapi.Deps{
		Cfg:        &config.Config{},
		Log:        log,
		AdminMW:    httpapi.NewAdminMiddleware(issuer),
		TenantMW:   httpapi.NewTenantMiddleware(e.resolver),
		Categories: &httpapi.CategoriesHandler{Client: client, Service: catalogService, Authz: authz, Log: log},
		Products:   &httpapi.ProductsHandler{Client: client, Service: catalogService, Authz: authz, Log: log},
	})
	return e
}

func (e *catalogEnv) call(t *testing.T, userID int, method, path string, body any) (int, string) {
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

func (e *catalogEnv) callJSON(t *testing.T, userID int, method, path string, body any) (int, map[string]any) {
	t.Helper()
	code, raw := e.call(t, userID, method, path, body)
	var parsed map[string]any
	_ = json.Unmarshal([]byte(raw), &parsed)
	return code, parsed
}

// public issues an unauthenticated request through the tenant-resolved
// storefront group (X-Site-Domain channel, design D5).
func (e *catalogEnv) public(t *testing.T, method, path, domain string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Site-Domain", domain)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (e *catalogEnv) shopPath(shopID int, parts string) string {
	return fmt.Sprintf("/api/v1/admin/shops/%d%s", shopID, parts)
}

// ── categories: tree, uniqueness, cycle prevention, deletion guard ──

// Scenario: 建立根分類與子分類，樹狀查詢正確性 (spec Shop-scoped category tree).
func TestCategoryTreeQuery(t *testing.T) {
	e := newCatalogEnv(t)

	_, root := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/categories"),
		map[string]any{"name": "男鞋", "slug": "mens-shoes"})
	rootID := int(root["id"].(float64))

	_, child := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/categories"),
		map[string]any{"name": "跑鞋", "slug": "running-shoes", "parent_id": rootID})
	if child["parent_id"] == nil {
		t.Fatalf("child category missing parent_id: %+v", child)
	}

	code, body := e.callJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/categories"), nil)
	if code != http.StatusOK {
		t.Fatalf("list tree: %d %+v", code, body)
	}
	cats, _ := body["categories"].([]any)
	if len(cats) != 1 {
		t.Fatalf("want 1 root category, got %d: %+v", len(cats), cats)
	}
	rootNode := cats[0].(map[string]any)
	if rootNode["name"] != "男鞋" {
		t.Fatalf("unexpected root: %+v", rootNode)
	}
	children, _ := rootNode["children"].([]any)
	if len(children) != 1 || children[0].(map[string]any)["name"] != "跑鞋" {
		t.Fatalf("child not nested under root: %+v", rootNode)
	}
}

// Scenario: 同店名稱/slug 重複被拒 (spec Shop-scoped category tree).
func TestCategoryUniqueness(t *testing.T) {
	e := newCatalogEnv(t)

	code, _ := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/categories"),
		map[string]any{"name": "男鞋", "slug": "mens-shoes"})
	if code != http.StatusCreated {
		t.Fatalf("first create: %d", code)
	}
	code, _ = e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/categories"),
		map[string]any{"name": "男鞋", "slug": "different-slug"})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("dup name: want 422, got %d", code)
	}
	code, _ = e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/categories"),
		map[string]any{"name": "different name", "slug": "mens-shoes"})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("dup slug: want 422, got %d", code)
	}
	// Different shop, same name/slug: allowed.
	code, _ = e.callJSON(t, e.ownerB, "POST", e.shopPath(e.shopB, "/categories"),
		map[string]any{"name": "男鞋", "slug": "mens-shoes"})
	if code != http.StatusCreated {
		t.Fatalf("cross-shop same name/slug: want 201, got %d", code)
	}
}

// Scenario: 直接與間接環狀被拒 (spec Category hierarchy cycle prevention).
func TestCategoryCyclePrevention(t *testing.T) {
	e := newCatalogEnv(t)

	_, a := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/categories"), map[string]any{"name": "A", "slug": "a"})
	aID := int(a["id"].(float64))
	_, b := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/categories"),
		map[string]any{"name": "B", "slug": "b", "parent_id": aID})
	bID := int(b["id"].(float64))
	_, c := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/categories"),
		map[string]any{"name": "C", "slug": "c", "parent_id": bID})
	cID := int(c["id"].(float64))

	// Direct self-reference.
	code, _ := e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/categories/%d", aID)),
		map[string]any{"parent_id": aID})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("direct self-reference: want 422, got %d", code)
	}

	// Indirect: A → C would close the loop A→C→B→A.
	code, _ = e.callJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/categories/%d", aID)),
		map[string]any{"parent_id": cID})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("indirect cycle: want 422, got %d", code)
	}
}

// Scenario: 刪除有子分類/有商品掛載的分類被拒 (409)；空分類刪除成功
// (spec Category deletion guard).
func TestCategoryDeletionGuard(t *testing.T) {
	e := newCatalogEnv(t)

	_, parent := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/categories"), map[string]any{"name": "Parent", "slug": "parent"})
	parentID := int(parent["id"].(float64))
	_, child := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/categories"),
		map[string]any{"name": "Child", "slug": "child", "parent_id": parentID})
	childID := int(child["id"].(float64))

	code, _ := e.call(t, e.ownerA, "DELETE", e.shopPath(e.shopA, fmt.Sprintf("/categories/%d", parentID)), nil)
	if code != http.StatusConflict {
		t.Fatalf("delete category with children: want 409, got %d", code)
	}

	// Delete the (empty) child first, then the now-empty parent succeeds.
	code, _ = e.call(t, e.ownerA, "DELETE", e.shopPath(e.shopA, fmt.Sprintf("/categories/%d", childID)), nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete empty child: want 204, got %d", code)
	}
	code, _ = e.call(t, e.ownerA, "DELETE", e.shopPath(e.shopA, fmt.Sprintf("/categories/%d", parentID)), nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete now-empty parent: want 204, got %d", code)
	}

	// A category with a product assigned cannot be deleted either.
	_, cat := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/categories"), map[string]any{"name": "Has Product", "slug": "has-product"})
	catID := int(cat["id"].(float64))
	e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/products"), map[string]any{
		"title": "T", "slug": "t", "category_ids": []int{catID},
	})
	code, _ = e.call(t, e.ownerA, "DELETE", e.shopPath(e.shopA, fmt.Sprintf("/categories/%d", catID)), nil)
	if code != http.StatusConflict {
		t.Fatalf("delete category with product: want 409, got %d", code)
	}
}

// Scenario: editor 不可刪除分類；merchant_owner 可以 (spec Merchant-scoped
// catalog management authorization).
func TestCategoryRBACDeleteRestriction(t *testing.T) {
	e := newCatalogEnv(t)

	_, cat := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/categories"), map[string]any{"name": "X", "slug": "x"})
	catID := int(cat["id"].(float64))

	code, _ := e.call(t, e.editorA, "DELETE", e.shopPath(e.shopA, fmt.Sprintf("/categories/%d", catID)), nil)
	if code != http.StatusForbidden {
		t.Fatalf("editor delete: want 403, got %d", code)
	}
	// editor can still create/view/edit.
	code, _ = e.callJSON(t, e.editorA, "POST", e.shopPath(e.shopA, "/categories"), map[string]any{"name": "Y", "slug": "y"})
	if code != http.StatusCreated {
		t.Fatalf("editor create: want 201, got %d", code)
	}
	code, _ = e.call(t, e.ownerA, "DELETE", e.shopPath(e.shopA, fmt.Sprintf("/categories/%d", catID)), nil)
	if code != http.StatusNoContent {
		t.Fatalf("owner delete: want 204, got %d", code)
	}
}

// Scenario: 跨店操作被拒 (spec Tenant data isolation for catalog tables /
// Cross-shop access guard).
func TestCategoryCrossShopIsolation(t *testing.T) {
	e := newCatalogEnv(t)

	_, catA := e.callJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/categories"), map[string]any{"name": "A only", "slug": "a-only"})
	catAID := int(catA["id"].(float64))

	// shop B owner cannot see/act on shop A's category via shop B's own admin path...
	code, _ := e.call(t, e.ownerB, "GET", e.shopPath(e.shopB, fmt.Sprintf("/categories/%d", catAID)), nil)
	if code != http.StatusNotFound {
		t.Fatalf("shop B reading shop A category via own scope: want 404, got %d", code)
	}
	// ...and cannot call shop A's endpoints at all (not a member of shop A).
	code, _ = e.call(t, e.ownerB, "GET", e.shopPath(e.shopA, "/categories"), nil)
	if code != http.StatusForbidden {
		t.Fatalf("shop B user calling shop A endpoint: want 403, got %d", code)
	}
}
