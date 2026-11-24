package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cart"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	entrole "ksdevworks/ecommerce/api/internal/ent/role"
	entshipment "ksdevworks/ecommerce/api/internal/ent/shipment"
	entshipmentitem "ksdevworks/ecommerce/api/internal/ent/shipmentitem"
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
	staffA  int // shipment.view/create/update but NOT shipment.return (change merchant-staff-role)

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
		"shipment.view", "shipment.create", "shipment.update", "shipment.return",
	}
	editorNodes := []string{
		"shipping_method.view", "shipping_method.create", "shipping_method.edit",
		"shipment.view", "shipment.create", "shipment.update", "shipment.return",
	}
	staffNodes := []string{"shipment.view", "shipment.create", "shipment.update"} // no shipment.return
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
	staffRole := client.Role.Create().SetName("staff").SetScope("merchant").SaveX(ctx)
	for _, p := range staffNodes {
		client.RolePermission.Create().SetRoleID(staffRole.ID).SetPermissionID(permIDs[p]).SaveX(ctx)
	}

	e.shopA = client.Shop.Create().SetName("Shop A").SetStatus(1).SaveX(ctx).ID
	e.shopB = client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(ctx).ID

	siteA := client.Site.Create().SetDomain(e.shopADomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(siteA.ID).SetShopID(e.shopA).SetIsPrimary(true).SaveX(ctx)
	siteB := client.Site.Create().SetDomain(e.shopBDomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(siteB.ID).SetShopID(e.shopB).SetIsPrimary(true).SaveX(ctx)

	e.memberA1 = client.Member.Create().SetShopID(e.shopA).SetEmail("shipping-member-a1@t.dev").SetStatus(1).SaveX(ctx).ID
	e.memberA2 = client.Member.Create().SetShopID(e.shopA).SetEmail("shipping-member-a2@t.dev").SetStatus(1).SaveX(ctx).ID
	e.memberB1 = client.Member.Create().SetShopID(e.shopB).SetEmail("shipping-member-b1@t.dev").SetStatus(1).SaveX(ctx).ID

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

	e.staffA = client.User.Create().SetEmail("shipping-staff-a@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.staffA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.staffA).SetRoleID(staffRole.ID).SetShopID(e.shopA).SaveX(ctx)

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
//
// Since change order-and-invitation-polish shipments require a paid order, so
// this helper marks the order paid; checkoutUnpaidOrder is the raw checkout.
func (e *shippingEnv) checkoutOrder(t *testing.T, shopID, memberID int, domain string, price int64) int {
	t.Helper()
	id := e.checkoutUnpaidOrder(t, shopID, memberID, domain, price)
	e.markPaid(t, id)
	return id
}

// markPaid sets payment_status to paid directly (the payment flow has its own
// tests; this package only needs a payable-to-ship order).
func (e *shippingEnv) markPaid(t *testing.T, orderID int) {
	t.Helper()
	if _, err := e.client.Order.UpdateOneID(orderID).SetPaymentStatus(1).Save(context.Background()); err != nil {
		t.Fatalf("mark order paid: %v", err)
	}
}

func (e *shippingEnv) checkoutUnpaidOrder(t *testing.T, shopID, memberID int, domain string, price int64) int {
	t.Helper()
	skuID := e.newSKU(t, shopID, price, "TWD", 10)
	if _, err := e.cartSvc.AddItem(context.Background(), shopID, memberID, skuID, 1, ""); err != nil {
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
		req.Host = domain // change storefront-client-auth: Host, not X-Site-Domain
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

// adminRequest is adminCall without the t.Fatal — safe to call from a
// goroutine, which adminCall is not (mirrors memberRequest/memberCall's split,
// and exists for the same reason: the concurrency test below).
func (e *shippingEnv) adminRequest(userID int, method, path string, body any) (int, string, error) {
	tok, err := e.issuer.IssueAdmin(userID, nil)
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
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), nil
}

func (e *shippingEnv) adminCall(t *testing.T, userID int, method, path string, body any) (int, string) {
	t.Helper()
	code, raw, err := e.adminRequest(userID, method, path, body)
	if err != nil {
		t.Fatal(err)
	}
	return code, raw
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

// Change merchant-staff-role (shipping-logistics spec): returning a shipment
// needs shipment.return on top of shipment.update. A refusal leaves the
// shipment, the order and the points untouched; delivering is unaffected.
func TestStaffCanDeliverButNotReturn(t *testing.T) {
	e := newShippingEnv(t)
	ctx := context.Background()
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)

	code, created := e.adminCallJSON(t, e.staffA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)), map[string]any{
		"carrier": "X", "tracking_number": "T-STAFF",
	})
	if code != http.StatusCreated {
		t.Fatalf("staff create shipment: want 201, got %d: %+v", code, created)
	}
	shipmentID := int(created["id"].(float64))
	path := e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments/%d", orderID, shipmentID))

	// Return is refused before anything changes.
	code, _ = e.adminCallJSON(t, e.staffA, "PUT", path, map[string]any{"status": "returned"})
	if code != http.StatusForbidden {
		t.Fatalf("staff return: want 403, got %d", code)
	}
	o, err := e.orderSvc.GetOrderAdmin(ctx, e.shopA, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if o.FulfillmentStatus != shipping.ShippedStatus {
		t.Fatalf("a refused return changed fulfillment_status to %d", o.FulfillmentStatus)
	}

	// The editor-shaped role keeps the ability (same request, same shipment).
	code, _ = e.adminCallJSON(t, e.editorA, "PUT", path, map[string]any{"status": "returned"})
	if code != http.StatusOK {
		t.Fatalf("editor return: want 200, got %d", code)
	}

	// Delivered works for staff on another order, and a return stays refused
	// (403 — the permission check precedes the state-machine's 409).
	order2 := e.checkoutOrder(t, e.shopA, e.memberA2, e.shopADomain, 1000)
	code, created = e.adminCallJSON(t, e.staffA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", order2)), map[string]any{
		"carrier": "X", "tracking_number": "T-STAFF-2",
	})
	if code != http.StatusCreated {
		t.Fatalf("staff create shipment 2: want 201, got %d", code)
	}
	path2 := e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments/%d", order2, int(created["id"].(float64))))
	if code, _ = e.adminCallJSON(t, e.staffA, "PUT", path2, map[string]any{"status": "delivered"}); code != http.StatusOK {
		t.Fatalf("staff deliver: want 200, got %d", code)
	}
	if code, _ = e.adminCallJSON(t, e.staffA, "PUT", path2, map[string]any{"status": "returned"}); code != http.StatusForbidden {
		t.Fatalf("staff return after delivery: want 403, got %d", code)
	}
}

// ── 6.4: illegal transitions ─────────────────────────────────────────────

func TestShipmentCreateRejectedForUnpaidOrder(t *testing.T) {
	e := newShippingEnv(t)
	orderID := e.checkoutUnpaidOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)
	path := e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID))

	code, resp := e.adminCallJSON(t, e.ownerA, "POST", path, map[string]any{"carrier": "黑貓宅急便"})
	if code != http.StatusConflict {
		t.Fatalf("create shipment on unpaid order: want 409, got %d: %+v", code, resp)
	}
	if n := e.client.Shipment.Query().Where(entshipment.OrderIDEQ(orderID)).CountX(context.Background()); n != 0 {
		t.Fatalf("%d shipment rows written for an unpaid order", n)
	}
	if fs := e.client.Order.GetX(context.Background(), orderID).FulfillmentStatus; fs != 0 {
		t.Fatalf("fulfillment_status = %d, want 0", fs)
	}

	// Once paid it goes through.
	e.markPaid(t, orderID)
	code, resp = e.adminCallJSON(t, e.ownerA, "POST", path, map[string]any{"carrier": "黑貓宅急便"})
	if code != http.StatusCreated {
		t.Fatalf("create shipment on paid order: want 201, got %d: %+v", code, resp)
	}
}

func TestShipmentCreateRejectedForCancelledOrder(t *testing.T) {
	e := newShippingEnv(t)
	orderID := e.checkoutUnpaidOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)

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

// ── change partial-shipment (HTTP level) ──────────────────────────────

// checkoutTwoLineOrder places an order with two distinct lines (2 + 1 units),
// which is the smallest shape in which "partly shipped" is even expressible.
func (e *shippingEnv) checkoutTwoLineOrder(t *testing.T, shopID, memberID int, domain string) (int, []map[string]any) {
	t.Helper()
	ctx := context.Background()
	for _, spec := range []struct {
		qty  int32
		gift string
	}{{2, "給媽媽"}, {1, "給岳母"}} {
		skuID := e.newSKU(t, shopID, 1000, "TWD", 10)
		if _, err := e.cartSvc.AddItem(ctx, shopID, memberID, skuID, spec.qty, spec.gift); err != nil {
			t.Fatalf("seed cart: %v", err)
		}
	}
	code, created := e.memberCallJSON(t, memberID, shopID, domain, "POST", "/api/v1/shop/checkout", shippingValidAddr())
	if code != http.StatusCreated {
		t.Fatalf("checkout: want 201, got %d: %+v", code, created)
	}
	e.markPaid(t, int(created["id"].(float64)))
	raw := created["items"].([]any)
	items := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		items = append(items, it.(map[string]any))
	}
	return int(created["id"].(float64)), items
}

// The request body that predates line items must keep working, unchanged, and
// keep meaning what it meant. This is design D5 asserted at the wire level
// rather than at the service level.
func TestShipmentCreateWithoutItemsStillShipsEverything(t *testing.T) {
	e := newShippingEnv(t)
	orderID, orderItems := e.checkoutTwoLineOrder(t, e.shopA, e.memberA1, e.shopADomain)

	code, created := e.adminCallJSON(t, e.ownerA, "POST",
		e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)),
		map[string]any{"carrier": "黑貓宅急便", "tracking_number": "TRACK-ALL"})
	if code != http.StatusCreated {
		t.Fatalf("create shipment without items: want 201, got %d: %+v", code, created)
	}
	if got := len(created["items"].([]any)); got != len(orderItems) {
		t.Fatalf("expected the parcel to carry every line (%d), got %d", len(orderItems), got)
	}

	gotOrder, err := e.orderSvc.GetOrderAdmin(context.Background(), e.shopA, orderID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.FulfillmentStatus != shipping.ShippedStatus {
		t.Fatalf("expected fulfillment_status=1 (shipped), got %d", gotOrder.FulfillmentStatus)
	}
}

func TestShipmentPartialFlowOverHTTP(t *testing.T) {
	e := newShippingEnv(t)
	orderID, orderItems := e.checkoutTwoLineOrder(t, e.shopA, e.memberA1, e.shopADomain)
	firstLine := int(orderItems[0]["id"].(float64))

	code, created := e.adminCallJSON(t, e.ownerA, "POST",
		e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)),
		map[string]any{"carrier": "黑貓宅急便", "items": []any{
			map[string]any{"order_item_id": firstLine, "quantity": 1},
		}})
	if code != http.StatusCreated {
		t.Fatalf("partial create: want 201, got %d: %+v", code, created)
	}
	firstShipment := int(created["id"].(float64))

	// The order reports the new value, and the line reports how much of it left.
	// Read through the *customer's* endpoint on purpose: this is the payload
	// ONDO consumes, and the new enum value plus shipped_quantity have to be
	// visible exactly there.
	code, ord := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET",
		fmt.Sprintf("/api/v1/shop/orders/%d", orderID), nil)
	if code != http.StatusOK {
		t.Fatalf("get order: want 200, got %d", code)
	}
	if int16(ord["fulfillment_status"].(float64)) != shipping.PartiallyShippedStatus {
		t.Fatalf("expected fulfillment_status=4 (partially shipped), got %+v", ord["fulfillment_status"])
	}
	// The lines must still come back in the order they were created, and this is
	// not a stylistic assertion. shipped_quantity is the first column anything
	// ever UPDATEs on order_items, and Postgres rewrites an updated tuple at the
	// end of the heap — so an unordered eager-load returns the line that just
	// shipped last, and the customer's order detail reshuffles itself every time
	// a parcel goes out. order.orderItemsByID is what stops that; this pins it.
	gotIDs := make([]int, 0, len(ord["items"].([]any)))
	for _, it := range ord["items"].([]any) {
		gotIDs = append(gotIDs, int(it.(map[string]any)["id"].(float64)))
	}
	wantIDs := make([]int, 0, len(orderItems))
	for _, it := range orderItems {
		wantIDs = append(wantIDs, int(it["id"].(float64)))
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("order lines reordered after a partial shipment: got %v, want %v", gotIDs, wantIDs)
	}

	line := ord["items"].([]any)[0].(map[string]any)
	if line["shipped_quantity"].(float64) != 1 {
		t.Fatalf("expected shipped_quantity=1 on the first line, got %+v", line["shipped_quantity"])
	}

	// Over-shipping that same line is a 422 on the offending index.
	code, rejected := e.adminCallJSON(t, e.ownerA, "POST",
		e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)),
		map[string]any{"carrier": "黑貓宅急便", "items": []any{
			map[string]any{"order_item_id": firstLine, "quantity": 2},
		}})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("over-shipment: want 422, got %d: %+v", code, rejected)
	}

	// The customer sees both parcels through the plural endpoint, and the
	// singular one keeps answering (with the newest).
	code, all := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET",
		fmt.Sprintf("/api/v1/shop/orders/%d/shipments", orderID), nil)
	if code != http.StatusOK {
		t.Fatalf("member list shipments: want 200, got %d: %+v", code, all)
	}
	if len(all["shipments"].([]any)) != 1 {
		t.Fatalf("expected one parcel so far, got %+v", all)
	}

	// Ship the remainder by omitting items, then everything is shipped.
	code, second := e.adminCallJSON(t, e.ownerA, "POST",
		e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)),
		map[string]any{"carrier": "7-11 超商取貨"})
	if code != http.StatusCreated {
		t.Fatalf("remainder create: want 201, got %d: %+v", code, second)
	}
	secondShipment := int(second["id"].(float64))

	code, all = e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET",
		fmt.Sprintf("/api/v1/shop/orders/%d/shipments", orderID), nil)
	if code != http.StatusOK {
		t.Fatalf("member list shipments (2): want 200, got %d", code)
	}
	list := all["shipments"].([]any)
	if len(list) != 2 {
		t.Fatalf("expected two parcels, got %d", len(list))
	}
	if int(list[0].(map[string]any)["id"].(float64)) != secondShipment {
		t.Fatalf("expected newest-first ordering, got %+v", list)
	}

	// This is the regression the singular endpoint used to have: with two rows
	// its `.Only()` errored and the customer was told their shipment did not
	// exist.
	code, single := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET",
		fmt.Sprintf("/api/v1/shop/orders/%d/shipment", orderID), nil)
	if code != http.StatusOK {
		t.Fatalf("singular endpoint with two shipments: want 200, got %d: %+v", code, single)
	}
	if int(single["id"].(float64)) != secondShipment {
		t.Fatalf("expected the most recent shipment %d, got %+v", secondShipment, single["id"])
	}

	// One parcel delivered does not deliver the order.
	code, _ = e.adminCallJSON(t, e.ownerA, "PUT",
		e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments/%d", orderID, firstShipment)),
		map[string]any{"status": "delivered"})
	if code != http.StatusOK {
		t.Fatalf("advance first: want 200, got %d", code)
	}
	gotOrder, err := e.orderSvc.GetOrderAdmin(context.Background(), e.shopA, orderID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.FulfillmentStatus != shipping.ShippedStatus {
		t.Fatalf("one delivered parcel must leave the order at shipped, got %d", gotOrder.FulfillmentStatus)
	}
}

// A partially shipped order must not be cancellable — and order.Service.CancelOrder
// was not touched by this change, so this asserts an emergent property of
// modelling partial shipment as a distinct value rather than as "still 0".
func TestPartiallyShippedOrderCannotBeCancelled(t *testing.T) {
	e := newShippingEnv(t)
	orderID, orderItems := e.checkoutTwoLineOrder(t, e.shopA, e.memberA1, e.shopADomain)
	firstLine := int(orderItems[0]["id"].(float64))

	code, _ := e.adminCallJSON(t, e.ownerA, "POST",
		e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)),
		map[string]any{"carrier": "黑貓宅急便", "items": []any{
			map[string]any{"order_item_id": firstLine, "quantity": 1},
		}})
	if code != http.StatusCreated {
		t.Fatalf("partial create: want 201, got %d", code)
	}

	code, raw := e.memberCall(t, e.memberA1, e.shopA, e.shopADomain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/cancel", orderID), nil)
	if code != http.StatusConflict {
		t.Fatalf("cancel a partially shipped order: want 409, got %d: %s", code, raw)
	}

	// Stock was NOT returned: that is the whole point — the restock loop gives
	// back ordered quantities, and part of this order has already left.
	gotOrder, err := e.orderSvc.GetOrderAdmin(context.Background(), e.shopA, orderID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.Status != order.StatusCreated {
		t.Fatalf("expected the order to stay uncancelled, got status=%d", gotOrder.Status)
	}
}

// checkoutSingleLineOrder produces an order with exactly one line of the given
// quantity — the shape the over-shipment guard has to defend.
func (e *shippingEnv) checkoutSingleLineOrder(t *testing.T, shopID, memberID int, domain string, qty int32) (int, int) {
	t.Helper()
	skuID := e.newSKU(t, shopID, 1000, "TWD", qty+5)
	if _, err := e.cartSvc.AddItem(context.Background(), shopID, memberID, skuID, qty, ""); err != nil {
		t.Fatalf("seed cart: %v", err)
	}
	code, created := e.memberCallJSON(t, memberID, shopID, domain, "POST", "/api/v1/shop/checkout", shippingValidAddr())
	if code != http.StatusCreated {
		t.Fatalf("checkout: want 201, got %d: %+v", code, created)
	}
	e.markPaid(t, int(created["id"].(float64)))
	items := created["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected a single-line order, got %d lines", len(items))
	}
	return int(created["id"].(float64)), int(items[0].(map[string]any)["id"].(float64))
}

// TestConcurrentShipmentsNeverExceedTheOrderedQuantity is the MANDATORY
// correctness test for change partial-shipment, and it is the exact counterpart
// of TestCheckoutConcurrentOversellingProtection: the same idiom (conditional
// UPDATE plus affected-rows), asserted the same way (fire N at once, count the
// winners, then look at the number in the database).
//
// It proves two separate things, and they have two separate guards (design D3/D4):
//
//   - **Nothing over-ships.** 12 concurrent one-unit parcels against a 5-unit
//     line must produce exactly 5 parcels. This is the conditional UPDATE's job
//     and it would hold with no lock at all.
//   - **The derived order status is right afterwards.** Once everything has
//     shipped the order must read 1 (shipped), never 4 (partially shipped).
//     This is the order row lock's job: without it two transactions shipping
//     concurrently cannot see each other's uncommitted shipped_quantity under
//     READ COMMITTED, both derive "partially shipped", and the order sticks at 4
//     with nothing left to ship it out of.
//
// Run under -race as well as plain; both are green.
func TestConcurrentShipmentsNeverExceedTheOrderedQuantity(t *testing.T) {
	e := newShippingEnv(t)
	ctx := context.Background()

	const ordered int32 = 5
	const attempts = 12
	orderID, lineID := e.checkoutSingleLineOrder(t, e.shopA, e.memberA1, e.shopADomain, ordered)

	codes := make([]int, attempts)
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, _, err := e.adminRequest(e.ownerA, "POST",
				e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)),
				map[string]any{"carrier": "黑貓宅急便", "items": []any{
					map[string]any{"order_item_id": lineID, "quantity": 1},
				}})
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

	var created, rejected int
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusUnprocessableEntity, http.StatusConflict:
			// 422: this line has nothing left. 409: the whole order is fully
			// shipped (the racer that lost after the last unit went out).
			rejected++
		default:
			t.Errorf("unexpected status code %d (codes=%v)", c, codes)
		}
	}
	if created != int(ordered) {
		t.Errorf("want exactly %d parcels, got %d (codes=%v)", ordered, created, codes)
	}
	if rejected != attempts-int(ordered) {
		t.Errorf("want %d rejections, got %d (codes=%v)", attempts-int(ordered), rejected, codes)
	}

	// The number in the database is the one that matters.
	line := e.client.OrderItem.GetX(ctx, lineID)
	if line.ShippedQuantity > ordered {
		t.Fatalf("shipped_quantity=%d exceeds the ordered %d — OVER-SHIPPED", line.ShippedQuantity, ordered)
	}
	if line.ShippedQuantity != ordered {
		t.Fatalf("shipped_quantity=%d, want %d", line.ShippedQuantity, ordered)
	}

	// The derived field and its source of truth agree across every parcel.
	var sum int32
	for _, si := range e.client.ShipmentItem.Query().Where(entshipmentitem.OrderItemIDEQ(lineID)).AllX(ctx) {
		sum += si.Quantity
	}
	if sum != ordered {
		t.Fatalf("Σ shipment_items.quantity = %d, but order_items.shipped_quantity = %d — the derived column drifted", sum, line.ShippedQuantity)
	}
	if n := e.client.Shipment.Query().Where(entshipment.OrderIDEQ(orderID)).CountX(ctx); n != int(ordered) {
		t.Fatalf("want %d shipment rows, got %d", ordered, n)
	}

	// The row lock's assertion: fully shipped means 1, not 4.
	gotOrder, err := e.orderSvc.GetOrderAdmin(ctx, e.shopA, orderID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.FulfillmentStatus != shipping.ShippedStatus {
		t.Fatalf("everything shipped, but fulfillment_status=%d (want %d). "+
			"A 4 here means the order row lock stopped serializing the derivation",
			gotOrder.FulfillmentStatus, shipping.ShippedStatus)
	}
}

// TestConcurrentShipmentsOfDisjointLinesStillCompleteTheOrder is the order row
// lock's own test (design D4), and it is deliberately separate from the
// over-shipment one above because it fails for a completely different reason.
//
// Two goroutines ship two *different* lines of one order. They never contend on
// an order_items row, so the conditional UPDATE — the guard that makes
// over-shipping impossible — is not involved at all and both succeed. What can
// still go wrong is the value they each derive for the order: under READ
// COMMITTED neither transaction can see the other's uncommitted
// shipped_quantity, so both compute "some of this order is still outstanding"
// and the order is left at 4 (partially shipped) with nothing left to ship.
//
// Only lockOrder's SELECT ... FOR UPDATE prevents that, by making the two
// derivations run one after the other. Verified by removing ForUpdate() and
// watching this test go red while every other shipping test stayed green.
func TestConcurrentShipmentsOfDisjointLinesStillCompleteTheOrder(t *testing.T) {
	e := newShippingEnv(t)
	ctx := context.Background()

	orderID, orderItems := e.checkoutTwoLineOrder(t, e.shopA, e.memberA1, e.shopADomain)
	if len(orderItems) != 2 {
		t.Fatalf("expected a two-line order, got %d", len(orderItems))
	}

	codes := make([]int, len(orderItems))
	errs := make(chan error, len(orderItems))
	var wg sync.WaitGroup
	for i, it := range orderItems {
		wg.Add(1)
		go func(i int, lineID int, qty float64) {
			defer wg.Done()
			code, _, err := e.adminRequest(e.ownerA, "POST",
				e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/shipments", orderID)),
				map[string]any{"carrier": "黑貓宅急便", "items": []any{
					map[string]any{"order_item_id": lineID, "quantity": int(qty)},
				}})
			if err != nil {
				errs <- err
				return
			}
			codes[i] = code
		}(i, int(it["id"].(float64)), it["quantity"].(float64))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	for i, c := range codes {
		if c != http.StatusCreated {
			t.Fatalf("parcel %d: want 201 (disjoint lines never contend), got %d (codes=%v)", i, c, codes)
		}
	}

	gotOrder, err := e.orderSvc.GetOrderAdmin(ctx, e.shopA, orderID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.FulfillmentStatus != shipping.ShippedStatus {
		t.Fatalf("both lines shipped, but fulfillment_status=%d (want %d = shipped). "+
			"4 here is the exact lost-update the order row lock exists to prevent: "+
			"the order is fully shipped and there is nothing left to ship it out of that state",
			gotOrder.FulfillmentStatus, shipping.ShippedStatus)
	}
}
