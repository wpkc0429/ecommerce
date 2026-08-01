package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cart"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	entorderitem "ksdevworks/ecommerce/api/internal/ent/orderitem"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// orderEnv exercises change order-management: checkout, member self-service
// order queries/cancellation, and merchant back-office order queries/
// cancellation. Combines cartEnv's member/domain-based auth pattern with
// catalogEnv's RBAC admin pattern, since order-management spans both.
type orderEnv struct {
	client   *ent.Client
	rdb      *redis.Client
	router   http.Handler
	issuer   *auth.TokenIssuer
	resolver *tenant.Resolver
	cartSvc  *cart.Service
	orderSvc *order.Service

	shopA, shopB             int
	shopADomain, shopBDomain string

	memberA1, memberA2 int // shop A members: cross-member isolation
	memberB1           int // shop B member: cross-shop isolation

	ownerA  int // merchant_owner of shop A: order.view + order.cancel
	viewerA int // shop A role with order.view only (no order.cancel)
	ownerB  int // merchant_owner of shop B: cross-shop isolation checks

	seq int // fixture uniqueness counter
}

func newOrderEnv(t *testing.T) *orderEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &orderEnv{client: client, rdb: rdb, shopADomain: "order-a.test", shopBDomain: "order-b.test"}

	perms := []string{"order.view", "order.cancel"}
	permIDs := map[string]int{}
	for _, p := range perms {
		permIDs[p] = client.Permission.Create().SetName(p).SaveX(ctx).ID
	}
	ownerRole := client.Role.Create().SetName("merchant_owner").SetScope("merchant").SaveX(ctx)
	for _, p := range perms {
		client.RolePermission.Create().SetRoleID(ownerRole.ID).SetPermissionID(permIDs[p]).SaveX(ctx)
	}
	viewerRole := client.Role.Create().SetName("editor").SetScope("merchant").SaveX(ctx)
	client.RolePermission.Create().SetRoleID(viewerRole.ID).SetPermissionID(permIDs["order.view"]).SaveX(ctx)

	e.shopA = client.Shop.Create().SetName("Shop A").SetStatus(1).SaveX(ctx).ID
	e.shopB = client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(ctx).ID

	siteA := client.Site.Create().SetDomain(e.shopADomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(siteA.ID).SetShopID(e.shopA).SetIsPrimary(true).SaveX(ctx)
	siteB := client.Site.Create().SetDomain(e.shopBDomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(siteB.ID).SetShopID(e.shopB).SetIsPrimary(true).SaveX(ctx)

	e.memberA1 = client.Member.Create().SetShopID(e.shopA).SetEmail("member-a1@t.dev").SetStatus(1).SaveX(ctx).ID
	e.memberA2 = client.Member.Create().SetShopID(e.shopA).SetEmail("member-a2@t.dev").SetStatus(1).SaveX(ctx).ID
	e.memberB1 = client.Member.Create().SetShopID(e.shopB).SetEmail("member-b1@t.dev").SetStatus(1).SaveX(ctx).ID

	hash, _ := auth.HashPassword("password-123")
	e.ownerA = client.User.Create().SetEmail("owner-a@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.ownerA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.ownerA).SetRoleID(ownerRole.ID).SetShopID(e.shopA).SaveX(ctx)

	e.viewerA = client.User.Create().SetEmail("viewer-a@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.viewerA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.viewerA).SetRoleID(viewerRole.ID).SetShopID(e.shopA).SaveX(ctx)

	e.ownerB = client.User.Create().SetEmail("owner-b@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopB).SetUserID(e.ownerB).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.ownerB).SetRoleID(ownerRole.ID).SetShopID(e.shopB).SaveX(ctx)

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	e.resolver = tenant.NewResolver(client, rdb, log)
	engine := rbac.NewEngine(client, rdb, log)
	authz := &httpapi.AuthzMW{Engine: engine}

	e.cartSvc = &cart.Service{Client: client}
	e.orderSvc = &order.Service{Client: client}

	e.router = httpapi.New(httpapi.Deps{
		Cfg:      &config.Config{},
		Log:      log,
		AdminMW:  httpapi.NewAdminMiddleware(issuer),
		TenantMW: httpapi.NewTenantMiddleware(e.resolver),
		MemberMW: httpapi.NewMemberMiddleware(issuer),
		Orders:   &httpapi.OrderHandler{Client: client, Service: e.orderSvc, Authz: authz, Log: log},
	})
	return e
}

// newSKU creates a product+SKU pair scoped to shopID for test fixtures
// (mirrors cartEnv.newSKU) and returns (productID, skuID).
func (e *orderEnv) newSKU(t *testing.T, shopID int, price int64, currency string, stock int32, active, published bool) (int, int) {
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

// addToCart seeds memberID's active cart via the real cart.Service (so price
// snapshots etc. are produced by the actual business logic, not
// reimplemented in the test).
func (e *orderEnv) addToCart(t *testing.T, shopID, memberID, skuID int, qty int32) {
	t.Helper()
	if _, err := e.cartSvc.AddItem(context.Background(), shopID, memberID, skuID, qty, ""); err != nil {
		t.Fatalf("seed cart: %v", err)
	}
}

func validShippingAddress() map[string]any {
	return map[string]any{
		"recipient_name": "王小明",
		"phone":          "0912345678",
		"line1":          "民生東路一段1號",
		"city":           "台北市",
		"postal_code":    "104",
		"country":        "TW",
	}
}

// memberRequest issues a raw request as memberID (aud=shop:{shopID}) against
// domain, returning any issuer error instead of calling t.Fatal — safe to
// call from goroutines (mirrors render.cache_integration_test's channel-based
// error collection convention).
func (e *orderEnv) memberRequest(memberID, shopID int, domain, method, path string, body any) (int, string, error) {
	tok, err := e.issuer.IssueMember(memberID, shopID)
	if err != nil {
		return 0, "", err
	}
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return 0, "", err
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Authorization", "Bearer "+tok)
	if domain != "" {
		req.Host = domain // change storefront-client-auth: Host, not X-Site-Domain
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), nil
}

func (e *orderEnv) memberCall(t *testing.T, memberID, shopID int, domain, method, path string, body any) (int, string) {
	t.Helper()
	code, raw, err := e.memberRequest(memberID, shopID, domain, method, path, body)
	if err != nil {
		t.Fatal(err)
	}
	return code, raw
}

func (e *orderEnv) memberCallJSON(t *testing.T, memberID, shopID int, domain, method, path string, body any) (int, map[string]any) {
	t.Helper()
	code, raw := e.memberCall(t, memberID, shopID, domain, method, path, body)
	var parsed map[string]any
	_ = json.Unmarshal([]byte(raw), &parsed)
	return code, parsed
}

func (e *orderEnv) adminCall(t *testing.T, userID int, method, path string, body any) (int, string) {
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

func (e *orderEnv) adminCallJSON(t *testing.T, userID int, method, path string, body any) (int, map[string]any) {
	t.Helper()
	code, raw := e.adminCall(t, userID, method, path, body)
	var parsed map[string]any
	_ = json.Unmarshal([]byte(raw), &parsed)
	return code, parsed
}

func (e *orderEnv) shopPath(shopID int, parts string) string {
	return fmt.Sprintf("/api/v1/admin/shops/%d%s", shopID, parts)
}

// ── checkout ──────────────────────────────────────────────────────────

// Scenario: 結帳成功建立訂單並轉換購物車 (spec Checkout converts an active
// cart into an order); denormalized snapshots (spec Order line items are
// denormalized snapshots); three-axis initial statuses (spec Order
// three-axis status model).
func TestCheckoutHappyPath(t *testing.T) {
	e := newOrderEnv(t)
	ctx := context.Background()

	_, skuA := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
	_, skuB := e.newSKU(t, e.shopA, 2500, "TWD", 10, true, true)
	e.addToCart(t, e.shopA, e.memberA1, skuA, 2)
	e.addToCart(t, e.shopA, e.memberA1, skuB, 1)

	code, body := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %+v", code, body)
	}
	if body["status"].(float64) != float64(order.StatusCreated) {
		t.Fatalf("new order status: want %d, got %v", order.StatusCreated, body["status"])
	}
	if body["payment_status"].(float64) != float64(order.PaymentStatusUnpaid) {
		t.Fatalf("new order payment_status: want %d, got %v", order.PaymentStatusUnpaid, body["payment_status"])
	}
	if body["fulfillment_status"].(float64) != float64(order.FulfillmentStatusUnfulfilled) {
		t.Fatalf("new order fulfillment_status: want %d, got %v", order.FulfillmentStatusUnfulfilled, body["fulfillment_status"])
	}
	wantTotal := float64(1000*2 + 2500*1)
	if body["total_amount"].(float64) != wantTotal {
		t.Fatalf("total_amount: want %v, got %v", wantTotal, body["total_amount"])
	}
	if body["currency"] != "TWD" {
		t.Fatalf("currency: want TWD, got %v", body["currency"])
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("want 2 order items, got %d: %+v", len(items), items)
	}
	for _, raw := range items {
		it := raw.(map[string]any)
		if it["product_title"] == "" || it["sku_code"] == "" {
			t.Fatalf("order item missing denormalized snapshot: %+v", it)
		}
	}
	addr, _ := body["shipping_address"].(map[string]any)
	if addr["recipient_name"] != "王小明" {
		t.Fatalf("shipping_address not round-tripped: %+v", body["shipping_address"])
	}

	// Source cart converted: GetCartView must report no active cart.
	view, err := e.cartSvc.GetCartView(ctx, e.shopA, e.memberA1)
	if err != nil {
		t.Fatalf("get cart view: %v", err)
	}
	if view.ID != nil {
		t.Fatalf("cart must be converted (no active cart left), got id=%v", *view.ID)
	}
}

// Scenario: 空購物車結帳被拒 (spec Checkout converts an active cart into an
// order).
func TestCheckoutEmptyCartRejected(t *testing.T) {
	e := newOrderEnv(t)
	ctx := context.Background()

	code, body := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("empty cart checkout: want 422, got %d %+v", code, body)
	}
	if n := e.client.Order.Query().CountX(ctx); n != 0 {
		t.Fatalf("no order should be created: %d exist", n)
	}
}

// Scenario: 缺少必要收件欄位被拒 (spec Structured shipping address captured
// at checkout).
func TestCheckoutMissingShippingFieldRejected(t *testing.T) {
	e := newOrderEnv(t)
	_, sku := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
	e.addToCart(t, e.shopA, e.memberA1, sku, 1)

	addr := validShippingAddress()
	delete(addr, "phone")
	code, body := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", addr)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("missing phone: want 422, got %d %+v", code, body)
	}
}

// Scenario: 商品下架後結帳被拒 / SKU 已下架結帳被拒 / SKU 已被刪除結帳被拒
// (spec Checkout converts an active cart into an order).
func TestCheckoutUnpurchasableItemRejected(t *testing.T) {
	ctx := context.Background()

	t.Run("product unpublished", func(t *testing.T) {
		e := newOrderEnv(t)
		productID, sku := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
		e.addToCart(t, e.shopA, e.memberA1, sku, 1)
		e.client.Product.UpdateOneID(productID).SetStatus(0).SaveX(ctx)

		code, body := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("want 422, got %d %+v", code, body)
		}
		assertNoOrderAndStockUnchanged(t, e, sku, 10)
	})

	t.Run("sku inactive", func(t *testing.T) {
		e := newOrderEnv(t)
		_, sku := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
		e.addToCart(t, e.shopA, e.memberA1, sku, 1)
		e.client.ProductSKU.UpdateOneID(sku).SetIsActive(false).SaveX(ctx)

		code, body := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("want 422, got %d %+v", code, body)
		}
		assertNoOrderAndStockUnchanged(t, e, sku, 10)
	})

	t.Run("sku deleted", func(t *testing.T) {
		e := newOrderEnv(t)
		_, sku := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
		e.addToCart(t, e.shopA, e.memberA1, sku, 1)
		e.client.ProductSKU.DeleteOneID(sku).ExecX(ctx)

		code, body := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("want 422, got %d %+v", code, body)
		}
		if n := e.client.Order.Query().CountX(ctx); n != 0 {
			t.Fatalf("no order should be created: %d exist", n)
		}
	})
}

func assertNoOrderAndStockUnchanged(t *testing.T, e *orderEnv, skuID int, wantStock int32) {
	t.Helper()
	ctx := context.Background()
	if n := e.client.Order.Query().CountX(ctx); n != 0 {
		t.Fatalf("no order should be created: %d exist", n)
	}
	sku := e.client.ProductSKU.GetX(ctx, skuID)
	if sku.StockQty != wantStock {
		t.Fatalf("stock must be unchanged: want %d, got %d", wantStock, sku.StockQty)
	}
}

// Scenario: 結帳品項之一庫存不足導致整單失敗、其餘品項庫存不變 (spec
// Checkout stock deduction is concurrency-safe and never oversells).
func TestCheckoutPartialStockFailureRollsBackEverything(t *testing.T) {
	e := newOrderEnv(t)
	ctx := context.Background()

	_, skuOK := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
	_, skuShort := e.newSKU(t, e.shopA, 500, "TWD", 5, true, true)
	e.addToCart(t, e.shopA, e.memberA1, skuOK, 3)
	e.addToCart(t, e.shopA, e.memberA1, skuShort, 5) // valid at add-time (stock=5)

	// Simulate stock being depleted by another sale after the item was added
	// to the cart (shopping-cart's add-time stock check is only advisory —
	// spec Add item validation) — checkout's own atomic check must catch it.
	e.client.ProductSKU.UpdateOneID(skuShort).SetStockQty(1).SaveX(ctx)

	code, body := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d %+v", code, body)
	}
	if n := e.client.Order.Query().CountX(ctx); n != 0 {
		t.Fatalf("no order should be created: %d exist", n)
	}
	if got := e.client.ProductSKU.GetX(ctx, skuOK).StockQty; got != 10 {
		t.Fatalf("unaffected line's stock must be rolled back: want 10, got %d", got)
	}
	if got := e.client.ProductSKU.GetX(ctx, skuShort).StockQty; got != 1 {
		t.Fatalf("short line's stock must be unchanged: want 1, got %d", got)
	}
}

// Scenario: 併發結帳搶購同一 SKU 不超賣 (spec Checkout stock deduction is
// concurrency-safe and never oversells) — MANDATORY correctness test.
func TestCheckoutConcurrentOversellingProtection(t *testing.T) {
	e := newOrderEnv(t)
	ctx := context.Background()

	const stock = 5
	const n = 10
	_, skuID := e.newSKU(t, e.shopA, 500, "TWD", stock, true, true)

	memberIDs := make([]int, n)
	for i := 0; i < n; i++ {
		memberIDs[i] = e.client.Member.Create().SetShopID(e.shopA).SetEmail(fmt.Sprintf("concurrent-%d@t.dev", i)).SetStatus(1).SaveX(ctx).ID
		e.addToCart(t, e.shopA, memberIDs[i], skuID, 1)
	}

	codes := make([]int, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, _, err := e.memberRequest(memberIDs[i], e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
			if err != nil {
				errs <- err
				return
			}
			codes[i] = code
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	var succeeded, failed int
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			succeeded++
		case http.StatusUnprocessableEntity:
			failed++
		default:
			t.Errorf("unexpected status code: %d", c)
		}
	}
	if succeeded != stock {
		t.Errorf("want %d successful checkouts, got %d (codes=%v)", stock, succeeded, codes)
	}
	if failed != n-stock {
		t.Errorf("want %d failed checkouts, got %d (codes=%v)", n-stock, failed, codes)
	}

	sku := e.client.ProductSKU.GetX(ctx, skuID)
	if sku.StockQty != 0 {
		t.Errorf("final stock_qty: want 0, got %d (must never be negative or leftover)", sku.StockQty)
	}
	if sku.StockQty < 0 {
		t.Fatalf("stock_qty went negative: %d — OVERSOLD", sku.StockQty)
	}

	orderItemCount := e.client.OrderItem.Query().Where(entorderitem.SkuIDEQ(skuID)).CountX(ctx)
	if orderItemCount != stock {
		t.Errorf("want %d order_items referencing the sku, got %d", stock, orderItemCount)
	}
}

// ── member self-service reads/cancel ─────────────────────────────────

// Scenario: 跨會員查詢/取消訂單被拒 (spec Member self-service order access is
// scoped by member identity).
func TestOrderMemberCrossMemberIsolation(t *testing.T) {
	e := newOrderEnv(t)
	_, sku := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
	e.addToCart(t, e.shopA, e.memberA1, sku, 1)
	_, created := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
	orderID := int(created["id"].(float64))

	code, _ := e.memberCall(t, e.memberA2, e.shopA, e.shopADomain, "GET", fmt.Sprintf("/api/v1/shop/orders/%d", orderID), nil)
	if code != http.StatusNotFound {
		t.Fatalf("cross-member get: want 404, got %d", code)
	}
	code, _ = e.memberCall(t, e.memberA2, e.shopA, e.shopADomain, "POST", fmt.Sprintf("/api/v1/shop/orders/%d/cancel", orderID), nil)
	if code != http.StatusNotFound {
		t.Fatalf("cross-member cancel: want 404, got %d", code)
	}
}

// Scenario: 跨店重用會員 token 存取訂單被拒 (spec Member self-service order
// access is scoped by member identity).
func TestOrderMemberCrossShopIsolation(t *testing.T) {
	e := newOrderEnv(t)
	_, skuA := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
	e.addToCart(t, e.shopA, e.memberA1, skuA, 1)
	_, created := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
	orderID := int(created["id"].(float64))

	// shop B member token cannot see shop A's order, even hitting shop B's
	// own domain (shop_id mismatch on the order lookup).
	code, _ := e.memberCall(t, e.memberB1, e.shopB, e.shopBDomain, "GET", fmt.Sprintf("/api/v1/shop/orders/%d", orderID), nil)
	if code != http.StatusNotFound {
		t.Fatalf("cross-shop get: want 404, got %d", code)
	}

	// A shop-A-issued token against shop B's domain fails the audience check
	// (401), mirroring shopping-cart's equivalent scenario.
	tok, err := e.issuer.IssueMember(e.memberA1, e.shopA)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/shop/orders/%d", orderID), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Host = e.shopBDomain
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cross-shop token reuse: want 401, got %d", rec.Code)
	}
}

// Scenario: 會員查詢自己的訂單列表 (spec Member self-service order access is
// scoped by member identity).
func TestOrderMemberListIsOwnOnly(t *testing.T) {
	e := newOrderEnv(t)
	_, sku := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)

	e.addToCart(t, e.shopA, e.memberA1, sku, 1)
	e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())

	e.addToCart(t, e.shopA, e.memberA2, sku, 1)
	e.memberCallJSON(t, e.memberA2, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())

	code, body := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/orders", nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d %+v", code, body)
	}
	if body["total"].(float64) != 1 {
		t.Fatalf("want only own order, total=1, got %v", body["total"])
	}
}

// Scenario: 取消未付款未出貨的訂單成功並歸還庫存 (spec Order cancellation
// requires all three axes at their initial value, and restores stock).
func TestOrderCancelRestoresStock(t *testing.T) {
	e := newOrderEnv(t)
	ctx := context.Background()

	_, sku := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
	e.addToCart(t, e.shopA, e.memberA1, sku, 3)
	_, created := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
	orderID := int(created["id"].(float64))

	if got := e.client.ProductSKU.GetX(ctx, sku).StockQty; got != 7 {
		t.Fatalf("stock after checkout: want 7, got %d", got)
	}

	code, body := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", fmt.Sprintf("/api/v1/shop/orders/%d/cancel", orderID), nil)
	if code != http.StatusOK {
		t.Fatalf("cancel: %d %+v", code, body)
	}
	if body["status"].(float64) != float64(order.StatusCancelled) {
		t.Fatalf("status after cancel: want %d, got %v", order.StatusCancelled, body["status"])
	}
	if got := e.client.ProductSKU.GetX(ctx, sku).StockQty; got != 10 {
		t.Fatalf("stock after cancel: want restored to 10, got %d", got)
	}
}

// Scenario: 已取消訂單不可重複取消 (spec Order cancellation requires all
// three axes at their initial value, and restores stock).
func TestOrderCancelAlreadyCancelledRejected(t *testing.T) {
	e := newOrderEnv(t)
	ctx := context.Background()

	_, sku := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
	e.addToCart(t, e.shopA, e.memberA1, sku, 1)
	_, created := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
	orderID := int(created["id"].(float64))

	code, _ := e.memberCall(t, e.memberA1, e.shopA, e.shopADomain, "POST", fmt.Sprintf("/api/v1/shop/orders/%d/cancel", orderID), nil)
	if code != http.StatusOK {
		t.Fatalf("first cancel: want 200, got %d", code)
	}
	code, _ = e.memberCall(t, e.memberA1, e.shopA, e.shopADomain, "POST", fmt.Sprintf("/api/v1/shop/orders/%d/cancel", orderID), nil)
	if code != http.StatusConflict {
		t.Fatalf("second cancel: want 409, got %d", code)
	}
	if got := e.client.ProductSKU.GetX(ctx, sku).StockQty; got != 10 {
		t.Fatalf("double-cancel must not double-restore stock: want 10, got %d", got)
	}
}

// Scenario: cancel rejected once payment/fulfillment has moved off its
// initial value (spec Order cancellation requires all three axes at their
// initial value, and restores stock).
func TestOrderCancelRejectedAfterPaymentAdvances(t *testing.T) {
	e := newOrderEnv(t)
	ctx := context.Background()

	_, sku := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
	e.addToCart(t, e.shopA, e.memberA1, sku, 1)
	_, created := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
	orderID := int(created["id"].(float64))

	// Simulate payment-integration having advanced payment_status via the
	// controlled service method (design D6) — not by writing ent directly.
	if _, err := e.orderSvc.UpdatePaymentStatus(ctx, e.shopA, orderID, 1); err != nil {
		t.Fatalf("advance payment status: %v", err)
	}

	code, _ := e.memberCall(t, e.memberA1, e.shopA, e.shopADomain, "POST", fmt.Sprintf("/api/v1/shop/orders/%d/cancel", orderID), nil)
	if code != http.StatusConflict {
		t.Fatalf("cancel after payment advanced: want 409, got %d", code)
	}
	if got := e.client.ProductSKU.GetX(ctx, sku).StockQty; got != 9 {
		t.Fatalf("rejected cancel must not restore stock: want 9, got %d", got)
	}
}

// ── merchant back office ─────────────────────────────────────────────

// Scenario: 具權限商家檢視自己商家的訂單；跨店操作被拒；無權限操作被拒
// (spec Merchant back-office order access is scoped by shop and RBAC).
func TestOrderAdminRBACAndCrossShopIsolation(t *testing.T) {
	e := newOrderEnv(t)
	_, skuA := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
	e.addToCart(t, e.shopA, e.memberA1, skuA, 1)
	_, created := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
	orderID := int(created["id"].(float64))

	// Owner (order.view + order.cancel) can list/get/cancel shop A's orders.
	code, list := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/orders"), nil)
	if code != http.StatusOK || list["total"].(float64) != 1 {
		t.Fatalf("owner list: %d %+v", code, list)
	}
	code, _ = e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d", orderID)), nil)
	if code != http.StatusOK {
		t.Fatalf("owner get: want 200, got %d", code)
	}

	// Viewer (order.view only) can list/get but not cancel.
	code, _ = e.adminCallJSON(t, e.viewerA, "GET", e.shopPath(e.shopA, "/orders"), nil)
	if code != http.StatusOK {
		t.Fatalf("viewer list: want 200, got %d", code)
	}
	code, _ = e.adminCall(t, e.viewerA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/cancel", orderID)), nil)
	if code != http.StatusForbidden {
		t.Fatalf("viewer cancel: want 403, got %d", code)
	}

	// Shop B's owner cannot touch shop A's orders at all.
	code, _ = e.adminCall(t, e.ownerB, "GET", e.shopPath(e.shopA, "/orders"), nil)
	if code != http.StatusForbidden {
		t.Fatalf("cross-shop list: want 403, got %d", code)
	}
	code, _ = e.adminCall(t, e.ownerB, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d", orderID)), nil)
	if code != http.StatusForbidden {
		t.Fatalf("cross-shop get: want 403, got %d", code)
	}
	code, _ = e.adminCall(t, e.ownerB, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/cancel", orderID)), nil)
	if code != http.StatusForbidden {
		t.Fatalf("cross-shop cancel: want 403, got %d", code)
	}

	// Owner can cancel their own shop's order.
	code, _ = e.adminCall(t, e.ownerA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/cancel", orderID)), nil)
	if code != http.StatusOK {
		t.Fatalf("owner cancel: want 200, got %d", code)
	}
}

// Scenario: 分頁列表；依訂單 status 篩選 (spec Merchant back-office order
// access is scoped by shop and RBAC).
func TestOrderAdminListPaginationAndStatusFilter(t *testing.T) {
	e := newOrderEnv(t)
	_, sku := e.newSKU(t, e.shopA, 1000, "TWD", 100, true, true)

	var lastOrderID int
	for i := 0; i < 3; i++ {
		e.addToCart(t, e.shopA, e.memberA1, sku, 1)
		_, created := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
		lastOrderID = int(created["id"].(float64))
	}
	// Cancel one so status filtering has something to distinguish.
	code, _ := e.adminCall(t, e.ownerA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/cancel", lastOrderID)), nil)
	if code != http.StatusOK {
		t.Fatalf("cancel one order: want 200, got %d", code)
	}

	code, page1 := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/orders?page=1&page_size=2"), nil)
	if code != http.StatusOK || page1["total"].(float64) != 3 {
		t.Fatalf("paginated list: %d %+v", code, page1)
	}
	if len(page1["orders"].([]any)) != 2 {
		t.Fatalf("page size not honored: %+v", page1)
	}

	code, cancelled := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders?status=%d", order.StatusCancelled)), nil)
	if code != http.StatusOK || cancelled["total"].(float64) != 1 {
		t.Fatalf("status filter: %d %+v", code, cancelled)
	}
}

// ── controlled status update entry points (spec Order three-axis status
// model) — service-level, exercising design D6's contract directly. ─────

func TestUpdatePaymentAndFulfillmentStatusScopingAndValidation(t *testing.T) {
	e := newOrderEnv(t)
	ctx := context.Background()

	_, sku := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
	e.addToCart(t, e.shopA, e.memberA1, sku, 1)
	_, created := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
	orderID := int(created["id"].(float64))

	updated, err := e.orderSvc.UpdatePaymentStatus(ctx, e.shopA, orderID, 1)
	if err != nil {
		t.Fatalf("update payment status: %v", err)
	}
	if updated.PaymentStatus != 1 {
		t.Fatalf("payment_status not persisted: %+v", updated)
	}

	updated, err = e.orderSvc.UpdateFulfillmentStatus(ctx, e.shopA, orderID, 1)
	if err != nil {
		t.Fatalf("update fulfillment status: %v", err)
	}
	if updated.FulfillmentStatus != 1 {
		t.Fatalf("fulfillment_status not persisted: %+v", updated)
	}

	// Wrong shop: ErrNotFound.
	if _, err := e.orderSvc.UpdatePaymentStatus(ctx, e.shopB, orderID, 1); err != order.ErrNotFound {
		t.Fatalf("cross-shop update: want ErrNotFound, got %v", err)
	}
	if _, err := e.orderSvc.UpdateFulfillmentStatus(ctx, e.shopB, orderID, 1); err != order.ErrNotFound {
		t.Fatalf("cross-shop update: want ErrNotFound, got %v", err)
	}

	// Negative values rejected.
	if _, err := e.orderSvc.UpdatePaymentStatus(ctx, e.shopA, orderID, -1); err == nil {
		t.Fatal("negative payment_status: want error")
	} else if _, ok := err.(*order.ValidationError); !ok {
		t.Fatalf("negative payment_status: want *ValidationError, got %T", err)
	}
	if _, err := e.orderSvc.UpdateFulfillmentStatus(ctx, e.shopA, orderID, -1); err == nil {
		t.Fatal("negative fulfillment_status: want error")
	} else if _, ok := err.(*order.ValidationError); !ok {
		t.Fatalf("negative fulfillment_status: want *ValidationError, got %T", err)
	}
}

// ── change admin-commerce-operations (design D3/D4): the back-office order
// list's optional filters. Every case below also has to leave the no-filter
// behavior untouched, which is why the first assertion in each block is the
// unfiltered count. ────────────────────────────────────────────────────────

// checkoutAs places one order for memberA1 in shop A with a chosen recipient
// name/phone and an optional requested delivery date (YYYY-MM-DD, "" to omit).
func (e *orderEnv) checkoutAs(t *testing.T, name, phone, deliveryDate string) int {
	t.Helper()
	_, sku := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
	e.addToCart(t, e.shopA, e.memberA1, sku, 1)
	body := map[string]any{"recipient_name": name, "phone": phone, "line1": "民生東路一段1號"}
	if deliveryDate != "" {
		body["requested_delivery_date"] = deliveryDate
	}
	code, created := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", body)
	if code != http.StatusCreated {
		t.Fatalf("checkout %q: want 201, got %d (%+v)", name, code, created)
	}
	return int(created["id"].(float64))
}

// listIDs runs the admin order list with a raw query string and returns the
// returned order ids in response order.
func (e *orderEnv) listIDs(t *testing.T, userID, shopID int, query string) []int {
	t.Helper()
	code, page := e.adminCallJSON(t, userID, "GET", e.shopPath(shopID, "/orders"+query), nil)
	if code != http.StatusOK {
		t.Fatalf("list %q: want 200, got %d (%+v)", query, code, page)
	}
	rows, _ := page["orders"].([]any)
	out := make([]int, 0, len(rows))
	for _, r := range rows {
		out = append(out, int(r.(map[string]any)["id"].(float64)))
	}
	return out
}

func sameIDs(got []int, want ...int) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[int]bool{}
	for _, id := range got {
		seen[id] = true
	}
	for _, id := range want {
		if !seen[id] {
			return false
		}
	}
	return true
}

// Scenario: 依付款與出貨狀態篩選出待出貨清單 (spec Merchant back-office order
// access is scoped by shop and RBAC).
func TestOrderAdminListPaymentAndFulfillmentFilters(t *testing.T) {
	e := newOrderEnv(t)
	ctx := context.Background()
	paid := e.checkoutAs(t, "已付款", "0911000001", "")
	shipped := e.checkoutAs(t, "已出貨", "0911000002", "")
	fresh := e.checkoutAs(t, "剛下單", "0911000003", "")

	if _, err := e.orderSvc.UpdatePaymentStatus(ctx, e.shopA, paid, 1); err != nil {
		t.Fatalf("advance payment: %v", err)
	}
	if _, err := e.orderSvc.UpdatePaymentStatus(ctx, e.shopA, shipped, 1); err != nil {
		t.Fatalf("advance payment: %v", err)
	}
	if _, err := e.orderSvc.UpdateFulfillmentStatus(ctx, e.shopA, shipped, 1); err != nil {
		t.Fatalf("advance fulfillment: %v", err)
	}

	if got := e.listIDs(t, e.ownerA, e.shopA, ""); !sameIDs(got, paid, shipped, fresh) {
		t.Fatalf("unfiltered list changed: %v", got)
	}
	if got := e.listIDs(t, e.ownerA, e.shopA, "?payment_status=1"); !sameIDs(got, paid, shipped) {
		t.Fatalf("payment_status=1: %v", got)
	}
	if got := e.listIDs(t, e.ownerA, e.shopA, "?fulfillment_status=0"); !sameIDs(got, paid, fresh) {
		t.Fatalf("fulfillment_status=0: %v", got)
	}
	// The picking list: paid but not yet shipped.
	if got := e.listIDs(t, e.ownerA, e.shopA, "?payment_status=1&fulfillment_status=0"); !sameIDs(got, paid) {
		t.Fatalf("paid+unfulfilled: %v", got)
	}
	// An unrecognized value matches nothing rather than erroring — the
	// enumerations belong to payment/shipping, not to this endpoint.
	if got := e.listIDs(t, e.ownerA, e.shopA, "?payment_status=99"); len(got) != 0 {
		t.Fatalf("unknown payment_status should match nothing: %v", got)
	}
}

// Scenario: 依指定配送日區間篩選；未指定配送日的訂單不被回傳 (spec Merchant
// back-office order access is scoped by shop and RBAC).
func TestOrderAdminListDeliveryDateRangeFilter(t *testing.T) {
	e := newOrderEnv(t)
	day := func(n int) string { return time.Now().AddDate(0, 0, n).Format("2006-01-02") }
	tomorrow := e.checkoutAs(t, "明天送", "0922000001", day(1))
	later := e.checkoutAs(t, "後天送", "0922000002", day(3))
	whenever := e.checkoutAs(t, "沒指定", "0922000003", "")

	if got := e.listIDs(t, e.ownerA, e.shopA, ""); !sameIDs(got, tomorrow, later, whenever) {
		t.Fatalf("unfiltered list changed: %v", got)
	}
	// A single day: from == to, inclusive on both ends.
	q := fmt.Sprintf("?delivery_from=%s&delivery_to=%s", day(1), day(1))
	if got := e.listIDs(t, e.ownerA, e.shopA, q); !sameIDs(got, tomorrow) {
		t.Fatalf("single-day range: %v", got)
	}
	// One-sided bounds.
	if got := e.listIDs(t, e.ownerA, e.shopA, "?delivery_from="+day(2)); !sameIDs(got, later) {
		t.Fatalf("from-only: %v", got)
	}
	if got := e.listIDs(t, e.ownerA, e.shopA, "?delivery_to="+day(2)); !sameIDs(got, tomorrow) {
		t.Fatalf("to-only: %v", got)
	}
	// The documented exclusion: NULL compares false, so an order with no
	// requested delivery date is absent from every dated query. The UI has to
	// say so (spec admin-operations-ui), and this is the fact it must say.
	wide := fmt.Sprintf("?delivery_from=%s&delivery_to=%s", day(-30), day(365))
	if got := e.listIDs(t, e.ownerA, e.shopA, wide); !sameIDs(got, tomorrow, later) {
		t.Fatalf("wide range should still exclude the undated order: %v", got)
	}
	// An unparseable date is "filter absent", not an error (tolerant input).
	if got := e.listIDs(t, e.ownerA, e.shopA, "?delivery_from=not-a-date"); !sameIDs(got, tomorrow, later, whenever) {
		t.Fatalf("unparseable date should be ignored: %v", got)
	}
}

// Scenario: 關鍵字比對收件人姓名或電話；數字時亦比對訂單編號 (spec Merchant
// back-office order access is scoped by shop and RBAC).
func TestOrderAdminListKeywordFilter(t *testing.T) {
	e := newOrderEnv(t)
	wang := e.checkoutAs(t, "王小明", "0912345678", "")
	chen := e.checkoutAs(t, "陳大文", "0987654321", "")
	alice := e.checkoutAs(t, "Alice Wang", "0900111222", "")

	if got := e.listIDs(t, e.ownerA, e.shopA, ""); !sameIDs(got, wang, chen, alice) {
		t.Fatalf("unfiltered list changed: %v", got)
	}
	if got := e.listIDs(t, e.ownerA, e.shopA, "?q=%E7%8E%8B"); !sameIDs(got, wang) { // 王
		t.Fatalf("name substring: %v", got)
	}
	if got := e.listIDs(t, e.ownerA, e.shopA, "?q=0987"); !sameIDs(got, chen) {
		t.Fatalf("phone substring: %v", got)
	}
	// Case-insensitive, and it is the *name* being matched, not the id.
	if got := e.listIDs(t, e.ownerA, e.shopA, "?q=alice"); !sameIDs(got, alice) {
		t.Fatalf("case-insensitive name: %v", got)
	}
	// "他說訂單編號是 …" — a numeric keyword also matches the order id.
	if got := e.listIDs(t, e.ownerA, e.shopA, fmt.Sprintf("?q=%d", chen)); len(got) == 0 || !contains(got, chen) {
		t.Fatalf("order id keyword: %v (want %d)", got, chen)
	}
	// A keyword that matches nothing is an empty page, not an error.
	if got := e.listIDs(t, e.ownerA, e.shopA, "?q=nobody-here"); len(got) != 0 {
		t.Fatalf("no-match keyword: %v", got)
	}
	// A quote in the keyword is a keyword, not SQL (parameter binding).
	if got := e.listIDs(t, e.ownerA, e.shopA, "?q=%27%20OR%201%3D1%20--"); len(got) != 0 {
		t.Fatalf("quoted keyword must not become SQL: %v", got)
	}
	// Filters compose with the pre-existing status filter and stay shop-scoped.
	q := fmt.Sprintf("?q=%%E7%%8E%%8B&status=%d", order.StatusCreated)
	if got := e.listIDs(t, e.ownerA, e.shopA, q); !sameIDs(got, wang) {
		t.Fatalf("keyword + status: %v", got)
	}
	if got := e.listIDs(t, e.ownerB, e.shopB, "?q=%E7%8E%8B"); len(got) != 0 {
		t.Fatalf("keyword must not cross shops: %v", got)
	}
}

func contains(ids []int, want int) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// Scenario: 依顧客識別碼篩出該顧客的全部訂單 / 顧客識別碼篩選受商家範圍約束 /
// 不帶新參數時行為不變 (spec Merchant back-office order access is scoped by
// shop and RBAC; change member-directory design D2).
//
// This is the parameter the customer roster's detail view uses instead of
// growing its own copy of the order payload.
func TestOrderAdminListMemberFilter(t *testing.T) {
	e := newOrderEnv(t)

	// Two orders for memberA1 (checkoutAs always uses it) and one for memberA2.
	firstA1 := e.checkoutAs(t, "顧客甲", "0922000001", "")
	secondA1 := e.checkoutAs(t, "顧客甲", "0922000002", "")

	_, skuForA2 := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
	e.addToCart(t, e.shopA, e.memberA2, skuForA2, 1)
	code, created := e.memberCallJSON(t, e.memberA2, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout",
		map[string]any{"recipient_name": "顧客乙", "phone": "0922000003", "line1": "民生東路一段2號"})
	if code != http.StatusCreated {
		t.Fatalf("memberA2 checkout: %d %+v", code, created)
	}
	orderA2 := int(created["id"].(float64))

	// Baseline: without the parameter the list still returns everything, which
	// is the "不帶新參數時行為不變" clause made mechanical.
	unfiltered := e.listIDs(t, e.ownerA, e.shopA, "")
	if !sameIDs(unfiltered, firstA1, secondA1, orderA2) {
		t.Fatalf("unfiltered list must be unchanged by the new parameter: %v", unfiltered)
	}

	if got := e.listIDs(t, e.ownerA, e.shopA, fmt.Sprintf("?member_id=%d", e.memberA1)); !sameIDs(got, firstA1, secondA1) {
		t.Fatalf("member_id must return exactly that customer's orders: %v", got)
	}
	if got := e.listIDs(t, e.ownerA, e.shopA, fmt.Sprintf("?member_id=%d", e.memberA2)); !sameIDs(got, orderA2) {
		t.Fatalf("member_id for the other customer: %v", got)
	}

	// The shop predicate still applies, so another shop's member id matches
	// nothing rather than reaching across the tenant boundary.
	if got := e.listIDs(t, e.ownerA, e.shopA, fmt.Sprintf("?member_id=%d", e.memberB1)); len(got) != 0 {
		t.Fatalf("a foreign member id must match nothing: %v", got)
	}

	// Composes with the pre-existing filters.
	q := fmt.Sprintf("?member_id=%d&status=%d", e.memberA1, order.StatusCreated)
	if got := e.listIDs(t, e.ownerA, e.shopA, q); !sameIDs(got, firstA1, secondA1) {
		t.Fatalf("member_id + status: %v", got)
	}
}
