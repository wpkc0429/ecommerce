package app

// Check 8 (change retire-hosted-storefront-and-themes, stage 2): disabling a
// shop must make its publishable keys stop serving immediately.
//
// The cached publishable-key entry embeds the shop status; the only thing that
// drops it when a shop's status changes is a dispatcher subscriber reacting to
// the shop-status event. Before this test nothing watched that: removing the
// InvalidateShopKeys call left every test green while a disabled shop's keys
// kept working until the cache TTL ran out.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/httpapi"
)

func TestShopStatusChangeInvalidatesPublishableKeyCache(t *testing.T) {
	_, deps := bootWire(t)
	ctx := context.Background()
	client := deps.Shops.Client

	sh := client.Shop.Create().SetName("Key Cache Shop").SetStatus(1).SaveX(ctx)
	const key = "pk_keycachetestkeyaaaaaaaaaaaaaaa"
	client.PublishableKey.Create().SetShopID(sh.ID).SetKey(key).SetLabel("test").SaveX(ctx)
	router := httpapi.New(deps)

	get := func() int {
		req := httptest.NewRequest("GET", "/api/v1/shop/products", nil)
		req.Header.Set(httpapi.PublishableKeyHeader, key)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := get(); code != http.StatusOK {
		t.Fatalf("active shop: want 200, got %d", code)
	}
	// Second call is served from the pkey: cache entry (status 1).
	if code := get(); code != http.StatusOK {
		t.Fatalf("active shop (cached): want 200, got %d", code)
	}

	disabled := int16(0)
	if _, err := deps.Shops.Service.UpdateShop(ctx, sh.ID, cms.ShopUpdate{Status: &disabled}); err != nil {
		t.Fatalf("disable shop: %v", err)
	}
	if code := get(); code != http.StatusServiceUnavailable {
		t.Fatalf("disabled shop: the cached key entry must be invalidated; want 503 shop_disabled, got %d", code)
	}

	enabled := int16(1)
	if _, err := deps.Shops.Service.UpdateShop(ctx, sh.ID, cms.ShopUpdate{Status: &enabled}); err != nil {
		t.Fatalf("re-enable shop: %v", err)
	}
	if code := get(); code != http.StatusOK {
		t.Fatalf("re-enabled shop: want 200, got %d", code)
	}
}
