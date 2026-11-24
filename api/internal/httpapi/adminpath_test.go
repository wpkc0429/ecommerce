package httpapi

import "testing"

// classifyAdminPath: every row of the design D3 table plus malformed paths.
func TestClassifyAdminPath(t *testing.T) {
	cases := []struct {
		path string
		kind adminPathKind
		shop int
	}{
		{"/api/v1/admin/shops/7/orders", pathAdminShop, 7},
		{"/api/v1/admin/shops/7/orders/12", pathAdminShop, 7},
		{"/api/v1/admin/shops/7/me", pathAdminShop, 7},
		{"/api/v1/admin/shops/7/branding", pathAdminShop, 7},
		{"/api/v1/admin/shops/007/orders", pathAdminShop, 7},
		{"/api/v1/admin/shops/7", pathAdminPlatform, 0},  // platform-scoped GET/PUT /shops/{id}
		{"/api/v1/admin/shops/7/", pathAdminPlatform, 0}, // empty subpath
		{"/api/v1/admin/shops", pathAdminPlatform, 0},
		{"/api/v1/admin/shops/abc/orders", pathAdminPlatform, 0},
		{"/api/v1/admin/shops/0/orders", pathAdminPlatform, 0},
		{"/api/v1/admin/shops/-3/orders", pathAdminPlatform, 0},
		{"/api/v1/admin/shops//orders", pathAdminPlatform, 0},
		{"/api/v1/admin/shops/99999999999999999999/orders", pathAdminPlatform, 0},
		{"/api/v1/admin/auth/login", pathAdminShared, 0},
		{"/api/v1/admin/auth/password-setup", pathAdminShared, 0},
		{"/api/v1/admin/auth/", pathAdminPlatform, 0},
		{"/api/v1/admin/auth", pathAdminPlatform, 0},
		{"/api/v1/admin/me", pathAdminShared, 0},
		{"/api/v1/admin/me/x", pathAdminPlatform, 0},
		{"/api/v1/admin/branding", pathAdminShared, 0},
		{"/api/v1/admin/invitations/accept", pathAdminShared, 0},
		{"/api/v1/admin/invitations/other", pathAdminPlatform, 0},
		{"/api/v1/admin/media/config", pathAdminShared, 0},
		{"/api/v1/admin/media/uploads", pathAdminPlatform, 0},
		{"/api/v1/admin/accounts", pathAdminPlatform, 0},
		{"/api/v1/admin/shop-provisioning", pathAdminPlatform, 0},
		{"/api/v1/admin", pathAdminPlatform, 0},
		{"/api/v1/admin/", pathAdminPlatform, 0},
		{"/api/v1/administrator", pathNonAdmin, 0},
		{"/api/v1/adminx/shops/1/orders", pathNonAdmin, 0},
		{"/api/v1/shop/products", pathNonAdmin, 0},
		{"/healthz", pathNonAdmin, 0},
		{"", pathNonAdmin, 0},
		{"/api/v1/admin/../shops/1/orders", pathAdminPlatform, 0},
	}
	for _, c := range cases {
		kind, shop := classifyAdminPath(c.path)
		if kind != c.kind || shop != c.shop {
			t.Errorf("%q: got (%v, %d), want (%v, %d)", c.path, kind, shop, c.kind, c.shop)
		}
	}
}
