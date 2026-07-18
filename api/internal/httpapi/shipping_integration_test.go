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
	entrole "ksdevworks/ecommerce/api/internal/ent/role"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/seed"
	"ksdevworks/ecommerce/api/internal/shipping"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// shippingEnv exercises change shipping-logistics end-to-end: merchant
// back-office shipping-method CRUD, shipment creation/state-transition, and
// member self-service shipment queries. Mirrors paymentEnv's/orderEnv's
// fixture shape (shopping-cart+order-management member/RBAC setup).
type shippingEnv struct {
	client   *ent.Client
	rdb      *redis.Client
	router   http.Handler
	issuer   *auth.TokenIssuer
	resolver *tenant.Resolver
	cartSvc  *cart.Service
	orderSvc *order.Service
	shipSvc  *shipping.Service

	shopA, shopB             int
	shopADomain, shopBDomain string

	memberA1, memberA2 int // shop A members: cross-member isolation
	memberB1           int // shop B member: cross-shop isolation

	ownerA  int // merchant_owner of shop A: every shipping_method.*/shipment.* node
	editorA int // shop A role: shipping_method.view/create/edit + shipment.view/create/update (mirrors seed "editor" breadth — no shipping_method.delete)
	viewerA int // shop A role WITHOUT any shipping_method.*/shipment.* permission
	ownerB  int // merchant_owner of shop B: cross-shop isolation checks

	seq int // fixture uniqueness counter
}

func newShippingEnv(t *testing.T) *shippingEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &shippingEnv{
		client: client, rdb: rdb,
		shopADomain: "shipping-a.test", shopBDomain: "shipping-b.test",
	}

	allNodes := []string{
		"shipping_method.view", "shipping_method.create", "shipping_method.edit", "shipping_method.delete",
		"shipment.view", "shipment.create", "shipment.update",
	}
	editorNodes := []string{
		"shipping_method.view", "shipping_method.create", "shipping_method.edit",
		"shipment.view", "shipment.create", "shipment.update",
	}
	permIDs := map[string]int{}
	for _, p := range allNodes {
		permIDs[p] = client.Permission.Create().SetName(p).SaveX(ctx).ID
	}
	ownerRole := client.Role.Create().SetName("merchant_owner").SetScope("merchant").SaveX(ctx)
	for _, p := range allNodes {
		client.RolePermission.Create().SetRoleID(ownerRole.ID).SetPermissionID(permIDs[p]).SaveX(ctx)
	}
	editorRole := client.Role.Create().SetName("editor").SetScope("merchant").SaveX(ctx)
	for _, p := range editorNodes {
		client.RolePermission.Create().SetRoleID(editorRole.ID).SetPermissionID(permIDs[p]).SaveX(ctx)
	}
	viewerRole := client.Role.Create().SetName("viewer_no_shipping_perms").SetScope("merchant").SaveX(ctx) // no permissions granted

	e.shopA = client.Shop.Create().SetName("Shop A").SetStatus(1).SaveX(ctx).ID
	e.shopB = client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(ctx).ID

	siteA := client.Site.Create().SetDomain(e.shopADomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(siteA.ID).SetShopID(e.shopA).SetIsPrimary(true).SaveX(ctx)
	siteB := client.Site.Create().SetDomain(e.shopBDomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(siteB.ID).SetShopID(e.shopB).SetIsPrimary(true).SaveX(ctx)

	e.memberA1 = client.Member.Create().SetEmail("shipping-member-a1@t.dev").SetStatus(1).SaveX(ctx).ID
	e.memberA2 = client.Member.Create().SetEmail("shipping-member-a2@t.dev").SetStatus(1).SaveX(ctx).ID
	e.memberB1 = client.Member.Create().SetEmail("shipping-member-b1@t.dev").SetStatus(1).SaveX(ctx).ID

	hash, _ := auth.HashPassword("password-123")
	e.ownerA = client.User.Create().SetEmail("shipping-owner-a@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.ownerA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.ownerA).SetRoleID(ownerRole.ID).SetShopID(e.shopA).SaveX(ctx)

	e.editorA = client.User.Create().SetEmail("shipping-editor-a@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.editorA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.editorA).SetRoleID(editorRole.ID).SetShopID(e.shopA).SaveX(ctx)

	e.viewerA = client.User.Create().SetEmail("shipping-viewer-a@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.viewerA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.viewerA).SetRoleID(viewerRole.ID).SetShopID(e.shopA).SaveX(ctx)

	e.ownerB = client.User.Create().SetEmail("shipping-owner-b@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
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
	e.shipSvc = &shipping.Service{Client: client, Orders: e.orderSvc}

	e.router = httpapi.New(httpapi.Deps{
		Cfg:      &config.Config{},
		Log:      log,
		AdminMW:  httpapi.NewAdminMiddleware(issuer),
		TenantMW: httpapi.NewTenantMiddleware(e.resolver),
		MemberMW: httpapi.NewMemberMiddleware(issuer),
		Orders:   &httpapi.OrderHandler{Client: client, Service: e.orderSvc, Authz: authz, Log: log},
		Shipping: &httpapi.ShippingHandler{Client: client, Service: e.shipSvc, Authz: authz, Log: log},
	})
	return e
}

func (e *shippingEnv) newSKU(t *testing.T, shopID int, price int64, currency string, stock int32) int {
	t.Helper()
	ctx := context.Background()
	e.seq++
	p := e.client.Product.Create().
		SetShopID(shopID).SetTitle(fmt.Sprintf("Product %d", e.seq)).SetSlug(fmt.Sprintf("shipping-product-%d", e.seq)).
		SetStatus(1).SaveX(ctx)
	sku := e.client.ProductSKU.Create().
		SetShopID(shopID).SetProductID(p.ID).SetSkuCode(fmt.Sprintf("SHIP-SKU-%d", e.seq)).
		SetPriceAmount(price).SetCurrency(currency).SetStockQty(stock).SetIsActive(true).
		SaveX(ctx)
	return sku.ID
}

func shippingValidAddr() map[string]any {
	return map[string]any{
		"recipient_name": "王小明", "phone": "0912345678", "line1": "民生東路一段1號",
		"city": "台北市", "postal_code": "104", "country": "TW",
	}
}

// checkoutOrder mirrors paymentEnv's helper of the same shape: seeds a SKU,
// adds it to memberID's cart via the real cart.Service, then checks out via
// the real HTTP endpoint.
func (e *shippingEnv) checkoutOrder(t *testing.T, shopID, memberID int, domain string, price int64) int {
	t.Helper()
	skuID := e.newSKU(t, shopID, price, "TWD", 10)
	if _, err := e.cartSvc.AddItem(context.Background(), shopID, memberID, skuID, 1); err != nil {
		t.Fatalf("seed cart: %v", err)
	}
	code, created := e.memberCallJSON(t, memberID, shopID, domain, "POST", "/api/v1/shop/checkout", shippingValidAddr())
	if code != http.StatusCreated {
		t.Fatalf("checkout: want 201, got %d: %+v", code, created)
	}
	return int(created["id"].(float64))
}

func (e *shippingEnv) memberRequest(memberID, shopID int, domain, method, path string, body any) (int, string, error) {
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
		req.Header.Set("X-Site-Domain", domain)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), nil
}

func (e *shippingEnv) memberCall(t *testing.T, memberID, shopID int, domain, method, path string, body any) (int, string) {
	t.Helper()
	code, raw, err := e.memberRequest(memberID, shopID, domain, method, path, body)
	if err != nil {
		t.Fatal(err)
	}
	return code, raw
}

func (e *shippingEnv) memberCallJSON(t *testing.T, memberID, shopID int, domain, method, path string, body any) (int, map[string]any) {
	t.Helper()
	code, raw := e.memberCall(t, memberID, shopID, domain, method, path, body)
	var parsed map[string]any
	_ = json.Unmarshal([]byte(raw), &parsed)
	return code, parsed
}

func (e *shippingEnv) adminCall(t *testing.T, userID int, method, path string, body any) (int, string) {
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

func (e *shippingEnv) adminCallJSON(t *testing.T, userID int, method, path string, body any) (int, map[string]any) {
	t.Helper()
	code, raw := e.adminCall(t, userID, method, path, body)
	var parsed map[string]any
	_ = json.Unmarshal([]byte(raw), &parsed)
	return code, parsed
}

func (e *shippingEnv) shopPath(shopID int, parts string) string {
	return fmt.Sprintf("/api/v1/admin/shops/%d%s", shopID, parts)
}

// ── 6.2: shipping_methods CRUD + validation ─────────────────────────────

func TestShippingMethodCRUDHappyPath(t *testing.T) {
	e := newShippingEnv(t)

	code, created := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/shipping-methods"), map[string]any{
		"name": "常溫宅配", "carrier": "黑貓宅急便", "flat_rate": 100,
	})
	if code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d: %+v", code, created)
	}
	if !created["is_active"].(bool) {
		t.Fatalf("expected default is_active=true, got %+v", created)
	}
	id := int(created["id"].(float64))

	code, got := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/shipping-methods/%d", id)), nil)
	if code != http.StatusOK || got["name"] != "常溫宅配" {
		t.Fatalf("get: want 200 with name, got %d: %+v", code, got)
	}

	code, updated := e.adminCallJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/shipping-methods/%d", id)), map[string]any{
		"name": "冷凍宅配",
	})
	if code != http.StatusOK || updated["name"] != "冷凍宅配" {
		t.Fatalf("update: want 200 with updated name, got %d: %+v", code, updated)
	}

	code, list := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/shipping-methods"), nil)
	if code != http.StatusOK {
		t.Fatalf("list: want 200, got %d", code)
	}
	if len(list["shipping_methods"].([]any)) != 1 {
		t.Fatalf("expected 1 shipping method, got %+v", list)
	}

	code, _ = e.adminCall(t, e.ownerA, "DELETE", e.shopPath(e.shopA, fmt.Sprintf("/shipping-methods/%d", id)), nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d", code)
	}
	code, _ = e.adminCall(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/shipping-methods/%d", id)), nil)
	if code != http.StatusNotFound {
		t.Fatalf("get after delete: want 404, got %d", code)
	}
}

func TestShippingMethodCreateValidation(t *testing.T) {
	e := newShippingEnv(t)

	cases := []map[string]any{
		{"name": "", "carrier": "黑貓宅急便", "flat_rate": 100},
		{"name": "常溫宅配", "carrier": "", "flat_rate": 100},
		{"name": "常溫宅配", "carrier": "黑貓宅急便", "flat_rate": -1},
	}
	for i, body := range cases {
		code, resp := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/shipping-methods"), body)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("case %d: want 422, got %d: %+v", i, code, resp)
		}
	}
}

// ── 6.5: RBAC + cross-shop isolation on shipping_methods ────────────────

func TestShippingMethodRBACAndCrossShopIsolation(t *testing.T) {
	e := newShippingEnv(t)

	code, _ := e.adminCall(t, e.viewerA, "GET", e.shopPath(e.shopA, "/shipping-methods"), nil)
	if code != http.StatusForbidden {
		t.Fatalf("viewer list: want 403, got %d", code)
	}
	code, _ = e.adminCall(t, e.viewerA, "POST", e.shopPath(e.shopA, "/shipping-methods"), map[string]any{"name": "x", "carrier": "y", "flat_rate": 0})
	if code != http.StatusForbidden {
		t.Fatalf("viewer create: want 403, got %d", code)
	}

	code, created := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/shipping-methods"), map[string]any{"name": "x", "carrier": "y", "flat_rate": 0})
	if code != http.StatusCreated {
		t.Fatalf("owner create: want 201, got %d", code)
	}
	id := int(created["id"].(float64))

	// editor has shipping_method.edit but not .delete.
	code, _ = e.adminCall(t, e.editorA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/shipping-methods/%d", id)), map[string]any{"name": "z"})
	if code != http.StatusOK {
		t.Fatalf("editor update: want 200, got %d", code)
	}
	code, _ = e.adminCall(t, e.editorA, "DELETE", e.shopPath(e.shopA, fmt.Sprintf("/shipping-methods/%d", id)), nil)
	if code != http.StatusForbidden {
		t.Fatalf("editor delete: want 403, got %d", code)
	}

	// Shop B's owner cannot see or touch shop A's shipping methods.
	code, _ = e.adminCall(t, e.ownerB, "GET", e.shopPath(e.shopA, "/shipping-methods"), nil)
	if code != http.StatusForbidden {
		t.Fatalf("cross-shop list: want 403, got %d", code)
	}
}

// ── 6.3: end-to-end shipment flow ───────────────────────────────────────

func TestShipmentEndToEndFlow(t *testing.T) {
	e := newShippingEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)

	code, created := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)), map[string]any{
		"carrier": "黑貓宅急便", "tracking_number": "TRACK-123",
	})
	if code != http.StatusCreated {
		t.Fatalf("create shipment: want 201, got %d: %+v", code, created)
	}
	if int16(created["status"].(float64)) != shipping.ShippedStatus {
		t.Fatalf("expected shipped status, got %+v", created)
	}
	shipmentID := int(created["id"].(float64))

	gotOrder, err := e.orderSvc.GetOrderAdmin(context.Background(), e.shopA, orderID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.FulfillmentStatus != shipping.ShippedStatus {
		t.Fatalf("expected order fulfillment_status=shipped, got %d", gotOrder.FulfillmentStatus)
	}

	code, advanced := e.adminCallJSON(t, e.ownerA, "PUT",
		e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments/%d", orderID, shipmentID)),
		map[string]any{"status": "delivered"})
	if code != http.StatusOK {
		t.Fatalf("advance to delivered: want 200, got %d: %+v", code, advanced)
	}
	if int16(advanced["status"].(float64)) != shipping.DeliveredStatus {
		t.Fatalf("expected delivered status, got %+v", advanced)
	}

	gotOrder, err = e.orderSvc.GetOrderAdmin(context.Background(), e.shopA, orderID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.FulfillmentStatus != shipping.DeliveredStatus {
		t.Fatalf("expected order fulfillment_status=delivered, got %d", gotOrder.FulfillmentStatus)
	}

	// Merchant list reflects the shipment.
	code, list := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)), nil)
	if code != http.StatusOK {
		t.Fatalf("admin list shipments: want 200, got %d", code)
	}
	if len(list["shipments"].([]any)) != 1 {
		t.Fatalf("expected exactly one shipment record, got %+v", list)
	}

	// Member self-service reflects the final state.
	code, mine := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", fmt.Sprintf("/api/v1/shop/orders/%d/shipment", orderID), nil)
	if code != http.StatusOK {
		t.Fatalf("member get shipment: want 200, got %d: %+v", code, mine)
	}
	if int16(mine["status"].(float64)) != shipping.DeliveredStatus {
		t.Fatalf("expected member view to reflect delivered status, got %+v", mine)
	}
	if mine["tracking_number"] != "TRACK-123" {
		t.Fatalf("expected tracking number to round-trip, got %+v", mine)
	}
}

// ── 6.4: illegal transitions ─────────────────────────────────────────────

func TestShipmentCreateRejectedForCancelledOrder(t *testing.T) {
	e := newShippingEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)

	code, _ := e.memberCall(t, e.memberA1, e.shopA, e.shopADomain, "POST", fmt.Sprintf("/api/v1/shop/orders/%d/cancel", orderID), nil)
	if code != http.StatusOK {
		t.Fatalf("cancel: want 200, got %d", code)
	}

	code, resp := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)), map[string]any{"carrier": "黑貓宅急便"})
	if code != http.StatusConflict {
		t.Fatalf("create shipment on cancelled order: want 409, got %d: %+v", code, resp)
	}
}

func TestShipmentCreateRejectedWhenAlreadyShipped(t *testing.T) {
	e := newShippingEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)

	code, _ := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)), map[string]any{"carrier": "黑貓宅急便"})
	if code != http.StatusCreated {
		t.Fatalf("first create: want 201, got %d", code)
	}

	code, resp := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)), map[string]any{"carrier": "7-11 超商取貨"})
	if code != http.StatusConflict {
		t.Fatalf("second create: want 409, got %d: %+v", code, resp)
	}
}

func TestShipmentAdvanceRejectedWhenAlreadyDelivered(t *testing.T) {
	e := newShippingEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)
	_, created := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)), map[string]any{"carrier": "黑貓宅急便"})
	shipmentID := int(created["id"].(float64))

	code, _ := e.adminCallJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments/%d", orderID, shipmentID)), map[string]any{"status": "delivered"})
	if code != http.StatusOK {
		t.Fatalf("first advance: want 200, got %d", code)
	}

	code, resp := e.adminCallJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments/%d", orderID, shipmentID)), map[string]any{"status": "delivered"})
	if code != http.StatusConflict {
		t.Fatalf("repeated advance to delivered: want 409, got %d: %+v", code, resp)
	}
}

func TestShipmentAdvanceRejectedReturnedToDelivered(t *testing.T) {
	e := newShippingEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)
	_, created := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)), map[string]any{"carrier": "黑貓宅急便"})
	shipmentID := int(created["id"].(float64))

	code, _ := e.adminCallJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments/%d", orderID, shipmentID)), map[string]any{"status": "returned"})
	if code != http.StatusOK {
		t.Fatalf("advance to returned: want 200, got %d", code)
	}

	code, resp := e.adminCallJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments/%d", orderID, shipmentID)), map[string]any{"status": "delivered"})
	if code != http.StatusConflict {
		t.Fatalf("returned -> delivered: want 409, got %d: %+v", code, resp)
	}
}

// ── 6.5: RBAC + cross-shop isolation on shipment endpoints ──────────────

func TestShipmentAdminRBACAndCrossShopIsolation(t *testing.T) {
	e := newShippingEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)

	// Viewer (no shipment.create) is forbidden.
	code, _ := e.adminCall(t, e.viewerA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)), map[string]any{"carrier": "黑貓宅急便"})
	if code != http.StatusForbidden {
		t.Fatalf("viewer create: want 403, got %d", code)
	}

	// Shop B's owner cannot operate on shop A's order at all.
	code, _ = e.adminCall(t, e.ownerB, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)), map[string]any{"carrier": "黑貓宅急便"})
	if code != http.StatusForbidden {
		t.Fatalf("cross-shop create: want 403, got %d", code)
	}

	code, created := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)), map[string]any{"carrier": "黑貓宅急便"})
	if code != http.StatusCreated {
		t.Fatalf("owner create: want 201, got %d", code)
	}
	shipmentID := int(created["id"].(float64))

	code, _ = e.adminCall(t, e.viewerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments/%d", orderID, shipmentID)), map[string]any{"status": "delivered"})
	if code != http.StatusForbidden {
		t.Fatalf("viewer advance: want 403, got %d", code)
	}
	code, _ = e.adminCall(t, e.viewerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)), nil)
	if code != http.StatusForbidden {
		t.Fatalf("viewer list: want 403, got %d", code)
	}
	code, _ = e.adminCall(t, e.ownerB, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)), nil)
	if code != http.StatusForbidden {
		t.Fatalf("cross-shop list: want 403, got %d", code)
	}
}

// ── 6.6: cross-member isolation on the member self-service endpoint ─────

func TestShipmentMemberSelfServiceCrossMemberIsolation(t *testing.T) {
	e := newShippingEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)
	e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)), map[string]any{"carrier": "黑貓宅急便"})

	code, resp := e.memberCallJSON(t, e.memberA2, e.shopA, e.shopADomain, "GET", fmt.Sprintf("/api/v1/shop/orders/%d/shipment", orderID), nil)
	if code != http.StatusNotFound {
		t.Fatalf("cross-member get shipment: want 404, got %d: %+v", code, resp)
	}
}

func TestShipmentMemberSelfServiceNoShipmentYet(t *testing.T) {
	e := newShippingEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)

	code, resp := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", fmt.Sprintf("/api/v1/shop/orders/%d/shipment", orderID), nil)
	if code != http.StatusNotFound {
		t.Fatalf("no shipment yet: want 404, got %d: %+v", code, resp)
	}
}

// ── 6.7: RBAC role coverage — the real seed catalog grants the expected
// shipping_method.*/shipment.* nodes to merchant_owner/editor ────────────

func TestSeedGrantsShippingPermissionsToMerchantOwnerAndEditor(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()
	if err := seed.Run(ctx, client, seed.Options{AdminEmail: "seed-admin@t.dev", AdminPassword: "seed-admin-pw-123"}); err != nil {
		t.Fatalf("seed.Run: %v", err)
	}

	roleNames := func(roleName string) map[string]bool {
		r, err := client.Role.Query().Where(entrole.NameEQ(roleName), entrole.ScopeEQ("merchant")).Only(ctx)
		if err != nil {
			t.Fatalf("query role %s: %v", roleName, err)
		}
		perms, err := r.QueryPermissions().All(ctx)
		if err != nil {
			t.Fatalf("query permissions for role %s: %v", roleName, err)
		}
		out := make(map[string]bool, len(perms))
		for _, p := range perms {
			out[p.Name] = true
		}
		return out
	}

	owner := roleNames("merchant_owner")
	for _, node := range []string{
		"shipping_method.view", "shipping_method.create", "shipping_method.edit", "shipping_method.delete",
		"shipment.view", "shipment.create", "shipment.update",
	} {
		if !owner[node] {
			t.Errorf("expected merchant_owner to be granted %s", node)
		}
	}

	editor := roleNames("editor")
	for _, node := range []string{
		"shipping_method.view", "shipping_method.create", "shipping_method.edit",
		"shipment.view", "shipment.create", "shipment.update",
	} {
		if !editor[node] {
			t.Errorf("expected editor to be granted %s", node)
		}
	}
	if editor["shipping_method.delete"] {
		t.Error("expected editor NOT to be granted shipping_method.delete (mirrors category.* breadth split)")
	}
}
