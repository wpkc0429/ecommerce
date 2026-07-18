package rbac_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/testutil"
)

type fixtures struct {
	client                *ent.Client
	shopA, shopB          int
	editor, owner, super  int // role ids
	permEdit, permUpdate  int
	editorUser, superUser int
}

func setup(t *testing.T) *fixtures {
	t.Helper()
	client := testutil.OpenDB(t)
	ctx := context.Background()
	f := &fixtures{client: client}

	f.shopA = client.Shop.Create().SetName("A").SaveX(ctx).ID
	f.shopB = client.Shop.Create().SetName("B").SaveX(ctx).ID

	pe := client.Permission.Create().SetName("page.edit").SaveX(ctx)
	pu := client.Permission.Create().SetName("shop.update").SaveX(ctx)
	f.permEdit, f.permUpdate = pe.ID, pu.ID

	editor := client.Role.Create().SetName("editor").SetScope("merchant").SaveX(ctx)
	owner := client.Role.Create().SetName("merchant_owner").SetScope("merchant").SaveX(ctx)
	super := client.Role.Create().SetName("super_admin").SetScope("platform").SaveX(ctx)
	f.editor, f.owner, f.super = editor.ID, owner.ID, super.ID
	client.RolePermission.Create().SetRoleID(editor.ID).SetPermissionID(pe.ID).SaveX(ctx)
	client.RolePermission.Create().SetRoleID(owner.ID).SetPermissionID(pe.ID).SaveX(ctx)
	client.RolePermission.Create().SetRoleID(owner.ID).SetPermissionID(pu.ID).SaveX(ctx)
	client.RolePermission.Create().SetRoleID(super.ID).SetPermissionID(pu.ID).SaveX(ctx)

	hash, _ := auth.HashPassword("password-123")
	eu := client.User.Create().SetEmail("editor@t.dev").SetPasswordHash(hash).SaveX(ctx)
	su := client.User.Create().SetEmail("super@t.dev").SetPasswordHash(hash).SaveX(ctx)
	f.editorUser, f.superUser = eu.ID, su.ID

	// editorUser: member of A only, editor role in A only.
	client.ShopUser.Create().SetShopID(f.shopA).SetUserID(eu.ID).SaveX(ctx)
	client.RoleUser.Create().SetUserID(eu.ID).SetRoleID(editor.ID).SetShopID(f.shopA).SaveX(ctx)
	// superUser: platform role, no memberships at all.
	client.RoleUser.Create().SetUserID(su.ID).SetRoleID(super.ID).SaveX(ctx)
	return f
}

func TestEngineSnapshotFromDB(t *testing.T) {
	f := setup(t)
	e := rbac.NewEngine(f.client, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	ctx := context.Background()

	// Role grant in shop A.
	if ok, _ := e.Allows(ctx, f.editorUser, f.shopA, "page.edit"); !ok {
		t.Fatal("editor must edit in shop A")
	}
	// Scenario: 角色不跨店外溢 — same user, shop B ctx.
	if ok, _ := e.Allows(ctx, f.editorUser, f.shopB, "page.edit"); ok {
		t.Fatal("shop A role leaked into shop B")
	}
	snapB, _ := e.Snapshot(ctx, f.editorUser, f.shopB)
	if snapB.CanAccessShop() {
		t.Fatal("editor is not a member of shop B")
	}

	// Scenario: 超級管理員跨店操作 — platform role works on any shop without
	// membership.
	snapSuper, _ := e.Snapshot(ctx, f.superUser, f.shopB)
	if !snapSuper.HasPlatformRole || !snapSuper.CanAccessShop() {
		t.Fatal("platform admin must pass the shop guard everywhere")
	}
	if ok, _ := e.Allows(ctx, f.superUser, f.shopB, "shop.update"); !ok {
		t.Fatal("platform role permission must apply to any shop ctx")
	}

	// Scenario: 個人覆蓋剝奪優先於角色 (loaded from DB).
	f.client.UserPermission.Create().
		SetUserID(f.editorUser).SetPermissionID(f.permEdit).SetShopID(f.shopA).SetIsGranted(false).
		SaveX(ctx)
	if ok, _ := e.Allows(ctx, f.editorUser, f.shopA, "page.edit"); ok {
		t.Fatal("deny override must beat role grant")
	}

	// Platform-level override grant for a permission no role gives.
	f.client.UserPermission.Create().
		SetUserID(f.editorUser).SetPermissionID(f.permUpdate).SetIsGranted(true).
		SaveX(ctx)
	if ok, _ := e.Allows(ctx, f.editorUser, f.shopA, "shop.update"); !ok {
		t.Fatal("platform-level override grant must apply in shop ctx")
	}
}

func TestEngineCacheAndInvalidation(t *testing.T) {
	f := setup(t)
	rdb := testutil.OpenRedis(t)
	e := rbac.NewEngine(f.client, rdb, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	ctx := context.Background()

	// Prime the cache.
	if ok, _ := e.Allows(ctx, f.editorUser, f.shopA, "page.edit"); !ok {
		t.Fatal("precondition: editor edits in A")
	}
	key := fmt.Sprintf("authz:user:%d:shop:%d", f.editorUser, f.shopA)
	if n, err := rdb.Exists(ctx, key).Result(); err != nil || n != 1 {
		t.Fatalf("snapshot not cached (n=%d err=%v)", n, err)
	}

	// Mutate DB behind the cache: revoke the role directly.
	f.client.RoleUser.Delete().ExecX(ctx)

	// Cached snapshot still answers (TTL 10 min) — stale by design…
	if ok, _ := e.Allows(ctx, f.editorUser, f.shopA, "page.edit"); !ok {
		t.Fatal("expected stale cached grant before invalidation")
	}
	// …until the write path invalidates (spec: 撤權立即生效).
	e.InvalidateUsers(ctx, f.editorUser)
	if ok, _ := e.Allows(ctx, f.editorUser, f.shopA, "page.edit"); ok {
		t.Fatal("invalidation must expose the revocation immediately")
	}
}
