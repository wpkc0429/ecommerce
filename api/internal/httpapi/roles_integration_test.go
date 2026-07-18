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

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/testutil"
)

type rbacEnv struct {
	client *ent.Client
	router http.Handler
	issuer *auth.TokenIssuer

	shopA, shopB               int
	ownerRole, editorRole      int
	superRole                  int
	ownerA, ownerB, staff, sup int // user ids
}

func newRBACEnv(t *testing.T) *rbacEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &rbacEnv{client: client}

	e.shopA = client.Shop.Create().SetName("A").SaveX(ctx).ID
	e.shopB = client.Shop.Create().SetName("B").SaveX(ctx).ID

	permView := client.Permission.Create().SetName("user.view").SaveX(ctx)
	permManage := client.Permission.Create().SetName("user.manage_roles").SaveX(ctx)

	ownerRole := client.Role.Create().SetName("merchant_owner").SetScope("merchant").SaveX(ctx)
	editorRole := client.Role.Create().SetName("editor").SetScope("merchant").SaveX(ctx)
	superRole := client.Role.Create().SetName("super_admin").SetScope("platform").SaveX(ctx)
	e.ownerRole, e.editorRole, e.superRole = ownerRole.ID, editorRole.ID, superRole.ID

	client.RolePermission.Create().SetRoleID(ownerRole.ID).SetPermissionID(permView.ID).SaveX(ctx)
	client.RolePermission.Create().SetRoleID(ownerRole.ID).SetPermissionID(permManage.ID).SaveX(ctx)
	client.RolePermission.Create().SetRoleID(editorRole.ID).SetPermissionID(permView.ID).SaveX(ctx)
	client.RolePermission.Create().SetRoleID(superRole.ID).SetPermissionID(permManage.ID).SaveX(ctx)
	client.RolePermission.Create().SetRoleID(superRole.ID).SetPermissionID(permView.ID).SaveX(ctx)

	hash, _ := auth.HashPassword("password-123")
	mkUser := func(email string) int {
		return client.User.Create().SetEmail(email).SetPasswordHash(hash).SaveX(ctx).ID
	}
	e.ownerA = mkUser("owner-a@t.dev")
	e.ownerB = mkUser("owner-b@t.dev")
	e.staff = mkUser("staff@t.dev")
	e.sup = mkUser("super@t.dev")

	for _, m := range []struct{ shop, user int }{
		{e.shopA, e.ownerA}, {e.shopA, e.staff}, {e.shopB, e.ownerB},
	} {
		client.ShopUser.Create().SetShopID(m.shop).SetUserID(m.user).SaveX(ctx)
	}
	client.RoleUser.Create().SetUserID(e.ownerA).SetRoleID(ownerRole.ID).SetShopID(e.shopA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.ownerB).SetRoleID(ownerRole.ID).SetShopID(e.shopB).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.sup).SetRoleID(superRole.ID).SaveX(ctx)

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	engine := rbac.NewEngine(client, rdb, log)
	authz := &httpapi.AuthzMW{Engine: engine}
	e.router = httpapi.New(httpapi.Deps{
		Cfg:     &config.Config{},
		Log:     log,
		AdminMW: httpapi.NewAdminMiddleware(issuer),
		Roles:   &httpapi.RolesHandler{Client: client, Engine: engine, Authz: authz, Log: log},
	})
	return e
}

func (e *rbacEnv) call(t *testing.T, userID int, method, path string, body any) (int, string) {
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

func TestShopRoleAssignmentFlow(t *testing.T) {
	e := newRBACEnv(t)

	pathRoles := func(shop int) string { return fmt.Sprintf("/api/v1/admin/shops/%d/roles", shop) }
	assignPath := fmt.Sprintf("/api/v1/admin/shops/%d/users/%d/roles", e.shopA, e.staff)

	// Staff has no role yet → user.view denied (default deny).
	if code, _ := e.call(t, e.staff, "GET", pathRoles(e.shopA), nil); code != 403 {
		t.Fatalf("staff before role: want 403, got %d", code)
	}

	// Scenario: 商家管理員於自店指派 — owner A assigns editor to staff in A.
	if code, body := e.call(t, e.ownerA, "POST", assignPath, map[string]int{"role_id": e.editorRole}); code != 200 {
		t.Fatalf("owner assign: %d %s", code, body)
	}
	// Invalidation makes the grant effective immediately.
	if code, _ := e.call(t, e.staff, "GET", pathRoles(e.shopA), nil); code != 200 {
		t.Fatal("staff should view roles after editor assignment")
	}
	// …but only in shop A (cross-shop guard: staff not a member of B).
	if code, _ := e.call(t, e.staff, "GET", pathRoles(e.shopB), nil); code != 403 {
		t.Fatal("staff must not access shop B")
	}

	// Scenario: 商家管理員跨店指派被拒 — owner of B cannot assign in A.
	if code, _ := e.call(t, e.ownerB, "POST", assignPath, map[string]int{"role_id": e.editorRole}); code != 403 {
		t.Fatal("cross-shop assignment must be rejected before permission decision")
	}

	// Platform-scope role cannot be assigned through the shop endpoint.
	if code, _ := e.call(t, e.ownerA, "POST", assignPath, map[string]int{"role_id": e.superRole}); code != 422 {
		t.Fatal("platform role via shop endpoint must 422")
	}

	// Scenario: 撤權立即生效 — removal invalidates the cache.
	removePath := fmt.Sprintf("/api/v1/admin/shops/%d/users/%d/roles/%d", e.shopA, e.staff, e.editorRole)
	if code, _ := e.call(t, e.ownerA, "DELETE", removePath, nil); code != 204 {
		t.Fatal("owner remove failed")
	}
	if code, _ := e.call(t, e.staff, "GET", pathRoles(e.shopA), nil); code != 403 {
		t.Fatal("revocation must take effect on the next request")
	}

	// Platform admin passes the guard on any shop without membership.
	if code, _ := e.call(t, e.sup, "GET", pathRoles(e.shopB), nil); code != 200 {
		t.Fatal("platform admin must operate on any shop")
	}
}

func TestPlatformRoleManagement(t *testing.T) {
	e := newRBACEnv(t)

	// Merchant owner is not a platform admin.
	if code, _ := e.call(t, e.ownerA, "GET", "/api/v1/admin/permissions", nil); code != 403 {
		t.Fatal("merchant owner must not manage the platform catalog")
	}

	// Platform admin manages catalog + roles.
	code, body := e.call(t, e.sup, "POST", "/api/v1/admin/permissions",
		map[string]string{"name": "page.publish", "description": "發佈"})
	if code != 201 {
		t.Fatalf("create permission: %d %s", code, body)
	}
	var created struct{ ID int }
	_ = json.Unmarshal([]byte(body), &created)

	code, body = e.call(t, e.sup, "POST", "/api/v1/admin/roles",
		map[string]string{"name": "publisher", "scope": "merchant"})
	if code != 201 {
		t.Fatalf("create role: %d %s", code, body)
	}
	var role struct{ ID int }
	_ = json.Unmarshal([]byte(body), &role)

	code, body = e.call(t, e.sup, "PUT", fmt.Sprintf("/api/v1/admin/roles/%d/permissions", role.ID),
		map[string][]int{"permission_ids": {created.ID}})
	if code != 200 {
		t.Fatalf("set role permissions: %d %s", code, body)
	}

	// Platform role assignment via the platform endpoint (shop NULL).
	code, body = e.call(t, e.sup, "POST", fmt.Sprintf("/api/v1/admin/users/%d/roles", e.staff),
		map[string]int{"role_id": e.superRole})
	if code != 200 {
		t.Fatalf("platform assign: %d %s", code, body)
	}
	// Now staff can hit platform endpoints.
	if code, _ := e.call(t, e.staff, "GET", "/api/v1/admin/permissions", nil); code != 200 {
		t.Fatal("newly assigned platform role must grant catalog access")
	}
	// Merchant-scope role cannot be assigned at platform level.
	code, _ = e.call(t, e.sup, "POST", fmt.Sprintf("/api/v1/admin/users/%d/roles", e.staff),
		map[string]int{"role_id": e.editorRole})
	if code != 422 {
		t.Fatal("merchant role at platform level must 422")
	}
}
