package rbac

import "testing"

// Full decision matrix for specs/rbac Three-tier permission decision:
// (覆蓋/角色/平台/跨店) × (允許/拒絕).
func TestDecisionMatrix(t *testing.T) {
	const perm = "page.publish"
	cases := []struct {
		name string
		snap Snapshot
		want bool
	}{
		{
			// Scenario: 個人覆蓋授予 — roles lack the permission, shop-level
			// override grants it.
			name: "shop override grants without role",
			snap: Snapshot{
				ShopOverrides: map[string]bool{perm: true},
				RolePerms:     map[string]bool{},
			},
			want: true,
		},
		{
			// Scenario: 個人覆蓋剝奪優先於角色 — forced denial beats role grant.
			name: "shop override denies despite role grant",
			snap: Snapshot{
				ShopOverrides: map[string]bool{perm: false},
				RolePerms:     map[string]bool{perm: true},
			},
			want: false,
		},
		{
			// Scenario: 角色授權.
			name: "role union grants",
			snap: Snapshot{RolePerms: map[string]bool{perm: true}},
			want: true,
		},
		{
			// Scenario: 預設拒絕.
			name: "default deny",
			snap: Snapshot{RolePerms: map[string]bool{"page.view": true}},
			want: false,
		},
		{
			name: "platform-level override grants",
			snap: Snapshot{
				PlatformOverrides: map[string]bool{perm: true},
				RolePerms:         map[string]bool{},
			},
			want: true,
		},
		{
			name: "platform-level override denies role grant",
			snap: Snapshot{
				PlatformOverrides: map[string]bool{perm: false},
				RolePerms:         map[string]bool{perm: true},
			},
			want: false,
		},
		{
			// Shop-specific override is more specific than platform override.
			name: "shop override beats platform override",
			snap: Snapshot{
				ShopOverrides:     map[string]bool{perm: true},
				PlatformOverrides: map[string]bool{perm: false},
			},
			want: true,
		},
		{
			// Scenario: 超級管理員跨店操作 — platform role permissions flow into
			// RolePerms for every shop ctx (built by the engine loader).
			name: "platform role grants in any shop ctx",
			snap: Snapshot{
				HasPlatformRole: true,
				RolePerms:       map[string]bool{perm: true},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.snap.Allows(perm); got != tc.want {
				t.Fatalf("Allows(%q) = %v, want %v", perm, got, tc.want)
			}
		})
	}
}

func TestCrossShopGuard(t *testing.T) {
	// Scenario: 操作非所屬商家 — non-member without platform role is blocked
	// before any permission decision.
	outsider := Snapshot{RolePerms: map[string]bool{"page.publish": true}}
	if outsider.CanAccessShop() {
		t.Fatal("non-member without platform role must not access the shop")
	}
	memberSnap := Snapshot{IsShopMember: true}
	if !memberSnap.CanAccessShop() {
		t.Fatal("shop member must pass the guard")
	}
	platform := Snapshot{HasPlatformRole: true}
	if !platform.CanAccessShop() {
		t.Fatal("platform admin must pass the guard for any shop")
	}
}
