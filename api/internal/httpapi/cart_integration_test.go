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
	"ksdevworks/ecommerce/api/internal/cart"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// cartEnv exercises change shopping-cart: the member self-service cart API.
// No RBAC/Authz is wired here on purpose — access control is solely "the
// JWT's member_id owns this cart" (spec Member-owned cart access control),
// and this environment is the proof that Cart mounts and works without
// MemberAuth (router.go task 5.5's loosened mount condition).
type cartEnv struct {
	client   *ent.Client
	rdb      *redis.Client
	router   http.Handler
	issuer   *auth.TokenIssuer
	resolver *tenant.Resolver

	shopA, shopB              int
	memberA1, memberA2        int // two members of shop A: cross-member isolation
	memberB1                  int // a member of shop B: cross-shop isolation
	shopADomain, shopBDomain  string

	seq int // fixture uniqueness counter
}

func newCartEnv(t *testing.T) *cartEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &cartEnv{client: client, rdb: rdb, shopADomain: "cart-a.test", shopBDomain: "cart-b.test"}

	e.shopA = client.Shop.Create().SetName("Shop A").SetStatus(1).SaveX(ctx).ID
	e.shopB = client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(ctx).ID

	siteA := client.Site.Create().SetDomain(e.shopADomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(siteA.ID).SetShopID(e.shopA).SetIsPrimary(true).SaveX(ctx)
	siteB := client.Site.Create().SetDomain(e.shopBDomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(siteB.ID).SetShopID(e.shopB).SetIsPrimary(true).SaveX(ctx)

	e.memberA1 = client.Member.Create().SetEmail("member-a1@t.dev").SetStatus(1).SaveX(ctx).ID
	e.memberA2 = client.Member.Create().SetEmail("member-a2@t.dev").SetStatus(1).SaveX(ctx).ID
	e.memberB1 = client.Member.Create().SetEmail("member-b1@t.dev").SetStatus(1).SaveX(ctx).ID

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	e.resolver = tenant.NewResolver(client, rdb, log)

	cartService := &cart.Service{Client: client}
	e.router = httpapi.New(httpapi.Deps{
		Cfg:      &config.Config{},
		Log:      log,
		TenantMW: httpapi.NewTenantMiddleware(e.resolver),
		MemberMW: httpapi.NewMemberMiddleware(issuer),
		Cart:     &httpapi.CartHandler{Client: client, Service: cartService, Log: log},
	})
	return e
}

// newSKU creates a product+SKU pair scoped to shopID for test fixtures and
// returns (productID, skuID).
func (e *cartEnv) newSKU(t *testing.T, shopID int, price int64, currency string, stock int32, active, published bool) (int, int) {
	t.Helper()
	ctx := context.Background()
	e.seq++
	status := int16(0)
	if published {
		status = 1
	}
	p := e.client.Product.Create().
		SetShopID(shopID).SetTitle(fmt.Sprintf("Product %d", e.seq)).SetSlug(fmt.Sprintf("product-%d", e.seq)).
		SetStatus(status).SaveX(ctx)
	sku := e.client.ProductSKU.Create().
		SetShopID(shopID).SetProductID(p.ID).SetSkuCode(fmt.Sprintf("SKU-%d", e.seq)).
		SetPriceAmount(price).SetCurrency(currency).SetStockQty(stock).SetIsActive(active).
		SaveX(ctx)
	return p.ID, sku.ID
}

// call issues a request as memberID (aud=shop:{shopID}) against domain;
// memberID <= 0 sends no Authorization header at all.
func (e *cartEnv) call(t *testing.T, memberID, shopID int, domain, method, path string, body any) (int, string) {
	t.Helper()
	var authHeader string
	if memberID > 0 {
		tok, err := e.issuer.IssueMember(memberID, shopID)
		if err != nil {
			t.Fatal(err)
		}
		authHeader = "Bearer " + tok
	}
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if domain != "" {
		req.Header.Set("X-Site-Domain", domain)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (e *cartEnv) callJSON(t *testing.T, memberID, shopID int, domain, method, path string, body any) (int, map[string]any) {
	t.Helper()
	code, raw := e.call(t, memberID, shopID, domain, method, path, body)
	var parsed map[string]any
	_ = json.Unmarshal([]byte(raw), &parsed)
	return code, parsed
}

// Scenario: 尚未加入任何品項時查詢購物車 (spec Cart lookup without forced creation).
func TestCartEmptyByDefault(t *testing.T) {
	e := newCartEnv(t)
	ctx := context.Background()

	code, body := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/cart", nil)
	if code != http.StatusOK {
		t.Fatalf("get empty cart: %d %+v", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("want empty items, got %+v", items)
	}
	if body["subtotal"].(float64) != 0 || body["total"].(float64) != 0 {
		t.Fatalf("want zero totals: %+v", body)
	}
	if body["id"] != nil {
		t.Fatalf("want nil id for ephemeral empty cart: %+v", body)
	}
	if n := e.client.Cart.Query().CountX(ctx); n != 0 {
		t.Fatalf("GET /cart must not create a row: %d carts exist", n)
	}
}

// Scenario: 加入不存在/跨店/已下架/未發佈商品的 SKU 皆 422；超過庫存 422；合法加入
// 201 且購物車列被建立 (spec Add item validation).
func TestCartAddItemValidation(t *testing.T) {
	e := newCartEnv(t)
	ctx := context.Background()

	_, activeSKU := e.newSKU(t, e.shopA, 1000, "TWD", 5, true, true)
	_, inactiveSKU := e.newSKU(t, e.shopA, 1000, "TWD", 5, false, true)
	_, draftSKU := e.newSKU(t, e.shopA, 1000, "TWD", 5, true, false)
	_, otherShopSKU := e.newSKU(t, e.shopB, 1000, "TWD", 5, true, true)

	cases := []struct {
		name  string
		skuID int
		qty   int
	}{
		{"unknown sku", 9_999_999, 1},
		{"cross-shop sku", otherShopSKU, 1},
		{"inactive sku", inactiveSKU, 1},
		{"draft product sku", draftSKU, 1},
		{"exceeds stock", activeSKU, 6},
	}
	for _, tc := range cases {
		code, body := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
			map[string]any{"sku_id": tc.skuID, "quantity": tc.qty})
		if code != http.StatusUnprocessableEntity {
			t.Errorf("%s: want 422, got %d %+v", tc.name, code, body)
		}
	}
	if n := e.client.Cart.Query().CountX(ctx); n != 0 {
		t.Fatalf("rejected adds must not create a cart row: %d exist", n)
	}

	code, body := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": activeSKU, "quantity": 5})
	if code != http.StatusCreated {
		t.Fatalf("valid add: want 201, got %d %+v", code, body)
	}
	if n := e.client.Cart.Query().CountX(ctx); n != 1 {
		t.Fatalf("want 1 cart row after a valid add, got %d", n)
	}
}

// Scenario: 重複加入同一 SKU 累加數量；累加後超過庫存被拒且既有數量不變
// (spec Add-item accumulation semantics).
func TestCartAddItemAccumulates(t *testing.T) {
	e := newCartEnv(t)
	_, sku := e.newSKU(t, e.shopA, 100, "TWD", 5, true, true)

	code, body := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": sku, "quantity": 2})
	if code != http.StatusCreated {
		t.Fatalf("first add: %d %+v", code, body)
	}
	code, body = e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": sku, "quantity": 3})
	if code != http.StatusCreated {
		t.Fatalf("second add: %d %+v", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("want 1 line item (accumulated), got %d: %+v", len(items), items)
	}
	if items[0].(map[string]any)["quantity"].(float64) != 5 {
		t.Fatalf("want accumulated quantity 5: %+v", items[0])
	}

	// Existing qty 5 + 1 more = 6 > stock 5 → 422, existing quantity unchanged.
	code, _ = e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": sku, "quantity": 1})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("accumulate past stock: want 422, got %d", code)
	}
	_, after := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/cart", nil)
	items, _ = after["items"].([]any)
	if items[0].(map[string]any)["quantity"].(float64) != 5 {
		t.Fatalf("quantity should remain 5 after rejected accumulation: %+v", items[0])
	}
}

// Scenario: PUT 絕對設定值語意、超過庫存被拒、<=0 被拒 (spec Update item
// quantity is an absolute set).
func TestCartUpdateItemQuantity(t *testing.T) {
	e := newCartEnv(t)
	_, sku := e.newSKU(t, e.shopA, 100, "TWD", 3, true, true)

	_, added := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": sku, "quantity": 1})
	items, _ := added["items"].([]any)
	itemID := int(items[0].(map[string]any)["id"].(float64))

	code, body := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "PUT", fmt.Sprintf("/api/v1/shop/cart/items/%d", itemID),
		map[string]any{"quantity": 2})
	if code != http.StatusOK {
		t.Fatalf("update ok: %d %+v", code, body)
	}
	items, _ = body["items"].([]any)
	if items[0].(map[string]any)["quantity"].(float64) != 2 {
		t.Fatalf("quantity not updated: %+v", items[0])
	}

	code, _ = e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "PUT", fmt.Sprintf("/api/v1/shop/cart/items/%d", itemID),
		map[string]any{"quantity": 4})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("exceeds stock: want 422, got %d", code)
	}
	code, _ = e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "PUT", fmt.Sprintf("/api/v1/shop/cart/items/%d", itemID),
		map[string]any{"quantity": 0})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("zero quantity: want 422, got %d", code)
	}
}

// Scenario: 移除單一品項不影響其他品項；清空購物車；清空空購物車是幂等操作
// (spec Remove item and clear cart).
func TestCartRemoveAndClear(t *testing.T) {
	e := newCartEnv(t)
	_, sku1 := e.newSKU(t, e.shopA, 100, "TWD", 5, true, true)
	_, sku2 := e.newSKU(t, e.shopA, 200, "TWD", 5, true, true)

	e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": sku1, "quantity": 1})
	_, added2 := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": sku2, "quantity": 1})
	items, _ := added2["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d: %+v", len(items), items)
	}
	var item1ID int
	for _, it := range items {
		m := it.(map[string]any)
		if int(m["sku_id"].(float64)) == sku1 {
			item1ID = int(m["id"].(float64))
		}
	}

	code, body := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "DELETE", fmt.Sprintf("/api/v1/shop/cart/items/%d", item1ID), nil)
	if code != http.StatusNoContent {
		t.Fatalf("remove item: want 204, got %d %+v", code, body)
	}
	_, afterRemove := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/cart", nil)
	items, _ = afterRemove["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("want 1 item after removal, got %d: %+v", len(items), items)
	}

	code, _ = e.call(t, e.memberA1, e.shopA, e.shopADomain, "DELETE", "/api/v1/shop/cart/items", nil)
	if code != http.StatusNoContent {
		t.Fatalf("clear cart: want 204, got %d", code)
	}
	_, afterClear := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/cart", nil)
	items, _ = afterClear["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("want 0 items after clear, got %d: %+v", len(items), items)
	}

	// Clearing an already-empty cart (member A2 never added anything) is idempotent.
	code, _ = e.call(t, e.memberA2, e.shopA, e.shopADomain, "DELETE", "/api/v1/shop/cart/items", nil)
	if code != http.StatusNoContent {
		t.Fatalf("clear empty cart: want 204 (idempotent), got %d", code)
	}
}

// Scenario: 第一個品項決定購物車幣別；混幣別加入被拒 (spec Cart currency lock).
func TestCartMixedCurrencyRejected(t *testing.T) {
	e := newCartEnv(t)
	_, twdSKU := e.newSKU(t, e.shopA, 100, "TWD", 5, true, true)
	_, usdSKU := e.newSKU(t, e.shopA, 10, "USD", 5, true, true)

	code, body := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": twdSKU, "quantity": 1})
	if code != http.StatusCreated {
		t.Fatalf("first add: %d %+v", code, body)
	}
	if body["currency"] != "TWD" {
		t.Fatalf("cart currency should lock to TWD: %+v", body)
	}
	code, _ = e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": usdSKU, "quantity": 1})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("mixed currency: want 422, got %d", code)
	}
	_, after := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/cart", nil)
	items, _ := after["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("rejected currency item must not be added: %+v", items)
	}
}

// Scenario: SKU 現價變動不影響既有購物車品項金額 (spec Price snapshot on add /
// Cart totals computed from snapshot).
func TestCartPriceSnapshotStable(t *testing.T) {
	e := newCartEnv(t)
	ctx := context.Background()
	_, sku := e.newSKU(t, e.shopA, 1000, "TWD", 5, true, true)

	code, body := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": sku, "quantity": 2})
	if code != http.StatusCreated {
		t.Fatalf("add: %d %+v", code, body)
	}
	if body["subtotal"].(float64) != 2000 {
		t.Fatalf("initial subtotal: want 2000, got %v", body["subtotal"])
	}

	// Merchant raises the price after the item was added.
	e.client.ProductSKU.UpdateOneID(sku).SetPriceAmount(1500).SaveX(ctx)

	_, after := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/cart", nil)
	items, _ := after["items"].([]any)
	if items[0].(map[string]any)["price_amount"].(float64) != 1000 {
		t.Fatalf("item price must stay snapshotted at 1000: %+v", items[0])
	}
	if after["subtotal"].(float64) != 2000 || after["total"].(float64) != 2000 {
		t.Fatalf("cart totals must stay snapshotted at 2000: %+v", after)
	}
}

// Scenario: SKU 下架/商品下架後品項保留但標記不可購買 (spec Deactivated or
// unpublished SKU stays in cart with a purchasability flag).
func TestCartDeactivatedSKUFlagged(t *testing.T) {
	e := newCartEnv(t)
	ctx := context.Background()
	prodID, sku := e.newSKU(t, e.shopA, 500, "TWD", 5, true, true)

	code, body := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": sku, "quantity": 2})
	if code != http.StatusCreated {
		t.Fatalf("add: %d %+v", code, body)
	}

	e.client.ProductSKU.UpdateOneID(sku).SetIsActive(false).SaveX(ctx)
	_, after := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/cart", nil)
	items, _ := after["items"].([]any)
	item := items[0].(map[string]any)
	if item["purchasable"].(bool) {
		t.Fatalf("deactivated sku must be flagged unpurchasable: %+v", item)
	}
	if item["unavailable_reason"] != "sku_inactive" {
		t.Fatalf("want sku_inactive reason, got %+v", item)
	}
	if item["quantity"].(float64) != 2 || item["price_amount"].(float64) != 500 {
		t.Fatalf("quantity/price snapshot must survive deactivation: %+v", item)
	}

	// Re-activate the SKU but unpublish its product instead.
	e.client.ProductSKU.UpdateOneID(sku).SetIsActive(true).SaveX(ctx)
	e.client.Product.UpdateOneID(prodID).SetStatus(0).SaveX(ctx)
	_, after2 := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/cart", nil)
	items2, _ := after2["items"].([]any)
	item2 := items2[0].(map[string]any)
	if item2["purchasable"].(bool) || item2["unavailable_reason"] != "product_unpublished" {
		t.Fatalf("unpublished product must flag item: %+v", item2)
	}
}

// Scenario: 刪除商品後其 SKU 連帶被刪除，購物車品項保留 (spec SKU deletion
// preserves the cart item with its snapshot).
func TestCartSKUDeletionSetsNull(t *testing.T) {
	e := newCartEnv(t)
	ctx := context.Background()
	prodID, sku := e.newSKU(t, e.shopA, 700, "TWD", 5, true, true)

	code, body := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": sku, "quantity": 1})
	if code != http.StatusCreated {
		t.Fatalf("add: %d %+v", code, body)
	}

	// Delete the product — cascades to its SKU (product-catalog design D2/D7).
	e.client.Product.DeleteOneID(prodID).ExecX(ctx)

	_, after := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/cart", nil)
	items, _ := after["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("item must survive sku deletion: %+v", items)
	}
	item := items[0].(map[string]any)
	if item["sku_id"] != nil {
		t.Fatalf("sku_id must be null after sku deletion: %+v", item)
	}
	if item["purchasable"].(bool) || item["unavailable_reason"] != "sku_deleted" {
		t.Fatalf("want sku_deleted unpurchasable flag: %+v", item)
	}
	if item["price_amount"].(float64) != 700 || item["quantity"].(float64) != 1 {
		t.Fatalf("snapshot must survive sku deletion: %+v", item)
	}
}

// Scenario: 跨會員存取品項被拒；未登入/跨店重用會員 token 被拒 (spec
// Member-owned cart access control).
func TestCartCrossMemberAndCrossShopIsolation(t *testing.T) {
	e := newCartEnv(t)
	_, sku := e.newSKU(t, e.shopA, 100, "TWD", 5, true, true)

	_, added := e.callJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": sku, "quantity": 1})
	items, _ := added["items"].([]any)
	itemID := int(items[0].(map[string]any)["id"].(float64))

	// member A2 cannot touch member A1's item — 404, not 403 (no existence leak).
	code, _ := e.call(t, e.memberA2, e.shopA, e.shopADomain, "PUT", fmt.Sprintf("/api/v1/shop/cart/items/%d", itemID),
		map[string]any{"quantity": 2})
	if code != http.StatusNotFound {
		t.Fatalf("cross-member update: want 404, got %d", code)
	}
	code, _ = e.call(t, e.memberA2, e.shopA, e.shopADomain, "DELETE", fmt.Sprintf("/api/v1/shop/cart/items/%d", itemID), nil)
	if code != http.StatusNotFound {
		t.Fatalf("cross-member delete: want 404, got %d", code)
	}
	// member A2's own cart is unaffected.
	_, a2Cart := e.callJSON(t, e.memberA2, e.shopA, e.shopADomain, "GET", "/api/v1/shop/cart", nil)
	if items2, _ := a2Cart["items"].([]any); len(items2) != 0 {
		t.Fatalf("member A2 must not see member A1's items: %+v", items2)
	}

	// No token at all.
	code, _ = e.call(t, 0, e.shopA, e.shopADomain, "GET", "/api/v1/shop/cart", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("no token: want 401, got %d", code)
	}

	// Shop A member token reused against shop B's domain.
	code, _ = e.call(t, e.memberA1, e.shopA, e.shopBDomain, "GET", "/api/v1/shop/cart", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("cross-shop token reuse: want 401, got %d", code)
	}

	// Shop B's own member, on shop B's domain, sees an empty cart (no leakage
	// from shop A) — spec Tenant data isolation for cart tables.
	_, b1Cart := e.callJSON(t, e.memberB1, e.shopB, e.shopBDomain, "GET", "/api/v1/shop/cart", nil)
	if itemsB, _ := b1Cart["items"].([]any); len(itemsB) != 0 {
		t.Fatalf("shop B member must not see shop A cart data: %+v", itemsB)
	}
}
