package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/ent/permission"
)

// GET /api/v1/admin/shops/{shopID}/me — the caller's own effective permission
// set (change admin-commerce-catalog, design D5; spec rbac/Caller's effective
// permission set in a shop is self-queryable).
//
// Why this endpoint has its own test file rather than living in
// roles_integration_test.go: it is the only admin route guarded by
// RequireShopAccess (no permission node at all), and the scenarios that matter
// are RBAC decision scenarios (override wins over role) rather than role
// management ones.

func meIn(t *testing.T, e *catalogEnv, userID, shopID int) (int, []string) {
	t.Helper()
	code, body := e.callJSON(t, userID, "GET", e.shopPath(shopID, "/me"), nil)
	raw, _ := body["permissions"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(string))
	}
	return code, out
}

func has(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// Scenario: 商家管理者取得自己的權限集合 / editor 缺 delete。
func TestShopMeReflectsRolePermissions(t *testing.T) {
	e := newCatalogEnv(t)

	code, owner := meIn(t, e, e.ownerA, e.shopA)
	if code != http.StatusOK {
		t.Fatalf("owner /me: %d", code)
	}
	for _, want := range []string{
		"category.view", "category.create", "category.edit", "category.delete",
		"product.view", "product.create", "product.edit", "product.delete",
	} {
		if !has(owner, want) {
			t.Fatalf("owner should hold %q: %v", want, owner)
		}
	}
	if !sort.StringsAreSorted(owner) {
		t.Fatalf("permissions must be sorted by name: %v", owner)
	}

	code, editor := meIn(t, e, e.editorA, e.shopA)
	if code != http.StatusOK {
		t.Fatalf("editor /me: %d", code)
	}
	if has(editor, "product.delete") || has(editor, "category.delete") {
		t.Fatalf("editor must not hold delete nodes: %v", editor)
	}
	if !has(editor, "product.edit") {
		t.Fatalf("editor should hold product.edit: %v", editor)
	}
}

// Scenario: 個人覆蓋剝奪 / 授予反映在結果中 — the endpoint must report the
// DECIDED set, not the raw union of role permissions.
func TestShopMeRespectsUserOverrides(t *testing.T) {
	e := newCatalogEnv(t)
	ctx := context.Background()

	deleteID := e.client.Permission.Query().Where(permission.NameEQ("product.delete")).OnlyX(ctx).ID

	// Deny a node the owner's role grants.
	e.client.UserPermission.Create().
		SetUserID(e.ownerA).SetPermissionID(deleteID).SetShopID(e.shopA).SetIsGranted(false).SaveX(ctx)
	// Grant a node the editor's role does not.
	e.client.UserPermission.Create().
		SetUserID(e.editorA).SetPermissionID(deleteID).SetShopID(e.shopA).SetIsGranted(true).SaveX(ctx)

	_, owner := meIn(t, e, e.ownerA, e.shopA)
	if has(owner, "product.delete") {
		t.Fatalf("denial override must remove the node: %v", owner)
	}
	if !has(owner, "product.edit") {
		t.Fatalf("the override must not affect other nodes: %v", owner)
	}

	_, editor := meIn(t, e, e.editorA, e.shopA)
	if !has(editor, "product.delete") {
		t.Fatalf("grant override must add the node: %v", editor)
	}
}

// Scenario: 不具任何權限節點但屬於該商家者仍可查詢 — the whole reason the
// guard is RequireShopAccess and not some existing node.
func TestShopMeWithoutAnyPermissionReturns200AndEmptySet(t *testing.T) {
	e := newCatalogEnv(t)
	ctx := context.Background()

	hash, _ := auth.HashPassword("password-123")
	bare := e.client.User.Create().SetEmail("bare-a@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	e.client.ShopUser.Create().SetShopID(e.shopA).SetUserID(bare).SaveX(ctx)

	code, perms := meIn(t, e, bare, e.shopA)
	if code != http.StatusOK {
		t.Fatalf("a shop member with no permissions must still get 200, got %d", code)
	}
	if len(perms) != 0 {
		t.Fatalf("want empty permission set, got %v", perms)
	}
}

// Scenario: 非該商家成員被拒 / 未認證被拒。
func TestShopMeCrossShopAndAnonymousDenied(t *testing.T) {
	e := newCatalogEnv(t)

	if code, _ := meIn(t, e, e.ownerB, e.shopA); code != http.StatusForbidden {
		t.Fatalf("shop B owner querying shop A: want 403, got %d", code)
	}

	req := httptest.NewRequest("GET", e.shopPath(e.shopA, "/me"), nil)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous /me: want 401, got %d", rec.Code)
	}
}

// Scenario: 平台層管理者跨商家可查詢 — platform admins operate on every shop
// without a shop_user row.
func TestShopMePlatformAdminCrossShop(t *testing.T) {
	e := newCatalogEnv(t)
	ctx := context.Background()

	platformRole := e.client.Role.Create().SetName("super_admin_test").SetScope("platform").SaveX(ctx)
	viewID := e.client.Permission.Query().Where(permission.NameEQ("product.view")).OnlyX(ctx).ID
	e.client.RolePermission.Create().SetRoleID(platformRole.ID).SetPermissionID(viewID).SaveX(ctx)

	hash, _ := auth.HashPassword("password-123")
	admin := e.client.User.Create().SetEmail("platform@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	e.client.RoleUser.Create().SetUserID(admin).SetRoleID(platformRole.ID).SaveX(ctx)

	code, perms := meIn(t, e, admin, e.shopB)
	if code != http.StatusOK {
		t.Fatalf("platform admin querying an arbitrary shop: want 200, got %d", code)
	}
	if !has(perms, "product.view") {
		t.Fatalf("platform role permission missing: %v", perms)
	}
}
