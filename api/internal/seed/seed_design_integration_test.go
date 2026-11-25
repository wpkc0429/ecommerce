package seed_test

import (
	"context"
	"strings"
	"testing"

	entpermission "ksdevworks/ecommerce/api/internal/ent/permission"
	entrole "ksdevworks/ecommerce/api/internal/ent/role"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// Scenario: 設計歷史的權限節點 (change design-history, design D9). EQUALITY on
// the design.* subset: staff holds none, editor exactly design.view, owner the
// four merchant nodes. design_service.manage (it issues the AI edit service's
// credential) is held by super_admin ONLY: no merchant role, so no automation
// account created from one can mint its own credential.
func TestSeedDesignPermissionGrants(t *testing.T) {
	client := testutil.OpenDB(t)
	runDemoSeed(t, client)
	ctx := context.Background()

	designOf := func(nodes map[string]bool) map[string]bool {
		out := map[string]bool{}
		for n := range nodes {
			if strings.HasPrefix(n, "design") {
				out[n] = true
			}
		}
		return out
	}
	equal := func(name string, got map[string]bool, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s design nodes = %v, want exactly %v", name, got, want)
		}
		for _, w := range want {
			if !got[w] {
				t.Errorf("%s must hold %s (has %v)", name, w, got)
			}
		}
	}
	equal("staff", designOf(roleNodes(t, client, "staff")))
	equal("editor", designOf(roleNodes(t, client, "editor")), "design.view")
	equal("merchant_owner", designOf(roleNodes(t, client, "merchant_owner")),
		"design.view", "design.request", "design.approve", "design.revert")

	for _, node := range []string{"design.view", "design.request", "design.approve", "design.revert", "design_service.manage"} {
		p, err := client.Permission.Query().Where(entpermission.NameEQ(node)).Only(ctx)
		if err != nil {
			t.Fatalf("%s must be in the catalog: %v", node, err)
		}
		roles, err := client.Role.Query().Where(entrole.HasPermissionsWith(entpermission.IDEQ(p.ID))).All(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, r := range roles {
			got[r.Scope+"/"+r.Name] = true
		}
		want := map[string]bool{"platform/super_admin": true, "merchant/merchant_owner": true}
		switch node {
		case "design.view":
			want["merchant/editor"] = true
		case "design_service.manage":
			want = map[string]bool{"platform/super_admin": true}
		}
		if len(got) != len(want) {
			t.Errorf("%s held by %v, want %v", node, got, want)
			continue
		}
		for k := range want {
			if !got[k] {
				t.Errorf("%s must be held by %s (held by %v)", node, k, got)
			}
		}
	}
	// Seeding again changes nothing.
	before := client.RolePermission.Query().CountX(ctx)
	runDemoSeed(t, client)
	if after := client.RolePermission.Query().CountX(ctx); after != before {
		t.Errorf("re-seed changed role_permission rows: %d -> %d", before, after)
	}
}
