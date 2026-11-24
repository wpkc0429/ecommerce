// Package rbac implements the shop-scoped three-tier permission decision
// (design D4): per-user override (including forced denial) → role union →
// default deny, with a Redis snapshot cache invalidated on every write that
// affects a user's permissions.
package rbac

// Snapshot is the resolved authorization state of one (user, shop) pair.
// It is JSON-serialized into Redis (authz:user:{id}:shop:{shop_id}).
type Snapshot struct {
	// ShopOverrides: permission → is_granted, from user_permission rows with
	// shop_id = shop ctx. Most specific tier — wins over everything.
	ShopOverrides map[string]bool `json:"shop_overrides"`
	// PlatformOverrides: permission → is_granted, from rows with shop_id NULL.
	PlatformOverrides map[string]bool `json:"platform_overrides"`
	// RolePerms is the union of permissions granted by the user's roles in
	// this shop ctx and at platform level.
	RolePerms map[string]bool `json:"role_perms"`
	// HasPlatformRole: user holds a scope='platform' role (role_user.shop_id
	// IS NULL) — platform admins operate on every shop.
	HasPlatformRole bool `json:"has_platform_role"`
	// IsShopMember: user has a shop_user row for this shop.
	IsShopMember bool `json:"is_shop_member"`
	// Disabled: the account's users.status is not 1 (or the user no longer
	// exists). A disabled user holds no permission at all — including
	// platform roles and personal overrides — and is a non-member of every
	// shop (change platform-account-provisioning, design D5). This is what
	// makes disabling an account take effect on its already-issued access
	// tokens at the next request.
	Disabled bool `json:"disabled,omitempty"`
}

// Allows runs the three-tier decision for one permission node.
func (s *Snapshot) Allows(perm string) bool {
	if s.Disabled {
		return false
	}
	if granted, ok := s.ShopOverrides[perm]; ok {
		return granted
	}
	if granted, ok := s.PlatformOverrides[perm]; ok {
		return granted
	}
	return s.RolePerms[perm]
}

// CanAccessShop is the cross-shop guard (design D4): before any permission
// decision, a non-platform user must be a member of the target shop.
func (s *Snapshot) CanAccessShop() bool {
	if s.Disabled {
		return false
	}
	return s.HasPlatformRole || s.IsShopMember
}

// IsPlatformAdmin reports whether the user holds a platform-scope role and is
// not disabled. Prefer it over reading HasPlatformRole directly.
func (s *Snapshot) IsPlatformAdmin() bool {
	return !s.Disabled && s.HasPlatformRole
}
