package testutil

import (
	"crypto/sha256"
	"encoding/base32"
	"net/http"
	"strings"
	"testing"

	"ksdevworks/ecommerce/api/internal/ent"
)

// Test tenants (change retire-hosted-storefront-and-themes, stage 3).
//
// Storefront-facing tests used to create a Site + SiteShop row and set
// req.Host to make the API resolve a shop. The supported way for a client to
// name its shop is the publishable key, so tests now bind a shop to a label
// and send the label's key:
//
//	testutil.BindTenant(t, client, shopID, "shop-a.test")
//	...
//	testutil.SetTenant(req, "shop-a.test")
//
// The label is only a handle inside the test; the key derived from it is
// deterministic, so BindTenant and SetTenant need no shared state. The
// integration database is truncated between tests, so labels can be reused.

// TenantKey returns the publishable key that BindTenant registers for label.
func TenantKey(label string) string {
	sum := sha256.Sum256([]byte("test-tenant:" + label))
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	return "pk_" + strings.ToLower(enc[:32])
}

// BindTenant creates the publishable key of shopID for label.
func BindTenant(t *testing.T, client *ent.Client, shopID int, label string) {
	t.Helper()
	if _, err := client.PublishableKey.Create().
		SetShopID(shopID).
		SetKey(TenantKey(label)).
		SetLabel(label).
		Save(t.Context()); err != nil {
		t.Fatalf("bind tenant %q to shop %d: %v", label, shopID, err)
	}
}

// SetTenant makes req carry the publishable key bound to label.
func SetTenant(req *http.Request, label string) {
	req.Header.Set("X-Publishable-Key", TenantKey(label))
}
