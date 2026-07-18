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
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/payment"
	"ksdevworks/ecommerce/api/internal/points"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/seed"
	"ksdevworks/ecommerce/api/internal/shipping"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// pointsEnv exercises change member-tiers-and-points end-to-end: merchant
// back-office member-tier CRUD + a shop member's points balance/ledger/
// manual-adjustment, member self-service points queries, and the full
// event-driven award/clawback flow through real checkout + payment webhook +
// shipment endpoints. Mirrors paymentEnv's/shippingEnv's fixture shape, with
// the dispatcher wired exactly like api/internal/app/wire.go does (design
// D1): payment/shipping publish, points.Service subscribes.
type pointsEnv struct {
	client     *ent.Client
	rdb        *redis.Client
	router     http.Handler
	issuer     *auth.TokenIssuer
	resolver   *tenant.Resolver
	cartSvc    *cart.Service
	orderSvc   *order.Service
	paymentSvc *payment.Service
	shipSvc    *shipping.Service
	pointsSvc  *points.Service

	webhookSecret string

	shopA, shopB             int
	shopADomain, shopBDomain string

	memberA1, memberA2 int // shop A members: cross-member isolation
	memberB1           int // shop B member: cross-shop isolation

	shopMemberA1, shopMemberA2, shopMemberB1 int // ShopMember row ids (admin routes address these, not member_id)

	ownerA  int // merchant_owner of shop A: every member_tier.*/point.* node
	viewerA int // shop A role WITHOUT any member_tier.*/point.* permission
	ownerB  int // merchant_owner of shop B: cross-shop isolation checks

	seq int // fixture uniqueness counter
}

func newPointsEnv(t *testing.T) *pointsEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &pointsEnv{
		client: client, rdb: rdb,
		shopADomain: "points-a.test", shopBDomain: "points-b.test",
		webhookSecret: "points-env-test-secret",
	}

	allNodes := []string{
		"member_tier.view", "member_tier.create", "member_tier.edit", "member_tier.delete",
		"point.view", "point.adjust",
		// shipment.create/update: only needed so ownerA can drive
		// shipAndReturn (the clawback end-to-end test) through the real
		// admin shipment endpoints — not part of this change's own
		// permission catalog additions.
		"shipment.create", "shipment.update",
	}
	permIDs := map[string]int{}
	for _, p := range allNodes {
		permIDs[p] = client.Permission.Create().SetName(p).SaveX(ctx).ID
	}
	ownerRole := client.Role.Create().SetName("merchant_owner").SetScope("merchant").SaveX(ctx)
	for _, p := range allNodes {
		client.RolePermission.Create().SetRoleID(ownerRole.ID).SetPermissionID(permIDs[p]).SaveX(ctx)
	}
	viewerRole := client.Role.Create().SetName("viewer_no_points_perms").SetScope("merchant").SaveX(ctx) // no permissions granted

	e.shopA = client.Shop.Create().SetName("Shop A").SetStatus(1).SaveX(ctx).ID
	e.shopB = client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(ctx).ID

	siteA := client.Site.Create().SetDomain(e.shopADomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(siteA.ID).SetShopID(e.shopA).SetIsPrimary(true).SaveX(ctx)
	siteB := client.Site.Create().SetDomain(e.shopBDomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(siteB.ID).SetShopID(e.shopB).SetIsPrimary(true).SaveX(ctx)

	e.memberA1 = client.Member.Create().SetEmail("points-member-a1@t.dev").SetStatus(1).SaveX(ctx).ID
	e.memberA2 = client.Member.Create().SetEmail("points-member-a2@t.dev").SetStatus(1).SaveX(ctx).ID
	e.memberB1 = client.Member.Create().SetEmail("points-member-b1@t.dev").SetStatus(1).SaveX(ctx).ID

	// A real ShopMember row per (shop, member) — mirrors
	// httpapi.MemberAuthHandler.ensureMembership's effect at login/register
	// time (design Context); tests here mint JWTs directly, bypassing that
	// handler, so the row is created explicitly instead.
	e.shopMemberA1 = client.ShopMember.Create().SetShopID(e.shopA).SetMemberID(e.memberA1).SaveX(ctx).ID
	e.shopMemberA2 = client.ShopMember.Create().SetShopID(e.shopA).SetMemberID(e.memberA2).SaveX(ctx).ID
	e.shopMemberB1 = client.ShopMember.Create().SetShopID(e.shopB).SetMemberID(e.memberB1).SaveX(ctx).ID

	hash, _ := auth.HashPassword("password-123")
	e.ownerA = client.User.Create().SetEmail("points-owner-a@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.ownerA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.ownerA).SetRoleID(ownerRole.ID).SetShopID(e.shopA).SaveX(ctx)

	e.viewerA = client.User.Create().SetEmail("points-viewer-a@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.viewerA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.viewerA).SetRoleID(viewerRole.ID).SetShopID(e.shopA).SaveX(ctx)

	e.ownerB = client.User.Create().SetEmail("points-owner-b@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopB).SetUserID(e.ownerB).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.ownerB).SetRoleID(ownerRole.ID).SetShopID(e.shopB).SaveX(ctx)

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	log := slog.New(slog.DiscardHandler)
	e.resolver = tenant.NewResolver(client, rdb, log)
	engine := rbac.NewEngine(client, rdb, log)
	authz := &httpapi.AuthzMW{Engine: engine}

	// Dispatcher wiring mirrors api/internal/app/wire.go exactly (design D1):
	// points.Service subscribes, payment/shipping publish.
	dispatcher := events.NewDispatcher(log)
	e.pointsSvc = &points.Service{Client: client, Log: log}
	dispatcher.Subscribe(e.pointsSvc.Handle)

	e.cartSvc = &cart.Service{Client: client}
	e.orderSvc = &order.Service{Client: client}
	mock := payment.NewMockProvider(e.webhookSecret)
	e.paymentSvc = &payment.Service{
		Client:          client,
		Orders:          e.orderSvc,
		Dispatcher:      dispatcher,
		Providers:       map[string]payment.Provider{mock.Name(): mock},
		DefaultProvider: mock.Name(),
	}
	e.shipSvc = &shipping.Service{Client: client, Orders: e.orderSvc, Dispatcher: dispatcher}

	e.router = httpapi.New(httpapi.Deps{
		Cfg:      &config.Config{},
		Log:      log,
		AdminMW:  httpapi.NewAdminMiddleware(issuer),
		TenantMW: httpapi.NewTenantMiddleware(e.resolver),
		MemberMW: httpapi.NewMemberMiddleware(issuer),
		Orders:   &httpapi.OrderHandler{Client: client, Service: e.orderSvc, Authz: authz, Log: log},
		Payments: &httpapi.PaymentHandler{Client: client, Service: e.paymentSvc, Authz: authz, Log: log},
		Shipping: &httpapi.ShippingHandler{Client: client, Service: e.shipSvc, Authz: authz, Log: log},
		Points:   &httpapi.PointsHandler{Client: client, Service: e.pointsSvc, Authz: authz, Log: log},
	})
	return e
}

func (e *pointsEnv) newSKU(t *testing.T, shopID int, price int64, currency string, stock int32) int {
	t.Helper()
	ctx := context.Background()
	e.seq++
	p := e.client.Product.Create().
		SetShopID(shopID).SetTitle(fmt.Sprintf("Product %d", e.seq)).SetSlug(fmt.Sprintf("points-product-%d", e.seq)).
		SetStatus(1).SaveX(ctx)
	sku := e.client.ProductSKU.Create().
		SetShopID(shopID).SetProductID(p.ID).SetSkuCode(fmt.Sprintf("PTS-SKU-%d", e.seq)).
		SetPriceAmount(price).SetCurrency(currency).SetStockQty(stock).SetIsActive(true).
		SaveX(ctx)
	return sku.ID
}

func pointsValidAddr() map[string]any {
	return map[string]any{
		"recipient_name": "王小明", "phone": "0912345678", "line1": "民生東路一段1號",
		"city": "台北市", "postal_code": "104", "country": "TW",
	}
}

// checkoutOrder mirrors paymentEnv's/shippingEnv's helper of the same shape.
func (e *pointsEnv) checkoutOrder(t *testing.T, shopID, memberID int, domain string, price int64) int {
	t.Helper()
	skuID := e.newSKU(t, shopID, price, "TWD", 10)
	if _, err := e.cartSvc.AddItem(context.Background(), shopID, memberID, skuID, 1); err != nil {
		t.Fatalf("seed cart: %v", err)
	}
	code, created := e.memberCallJSON(t, memberID, shopID, domain, "POST", "/api/v1/shop/checkout", pointsValidAddr())
	if code != http.StatusCreated {
		t.Fatalf("checkout: want 201, got %d: %+v", code, created)
	}
	return int(created["id"].(float64))
}

// payOrder drives a full member-initiate-payment + signed-webhook-success
// flow, mirroring paymentEnv's TestPaymentSuccessEndToEnd.
func (e *pointsEnv) payOrder(t *testing.T, shopID, memberID int, domain string, orderID int) {
	t.Helper()
	code, initiated := e.memberCallJSON(t, memberID, shopID, domain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/payments", orderID), nil)
	if code != http.StatusCreated {
		t.Fatalf("initiate payment: want 201, got %d: %+v", code, initiated)
	}
	ref := initiated["provider_reference"].(string)
	body, err := json.Marshal(map[string]string{"provider_reference": ref, "status": "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	sig := payment.SignMockWebhook(e.webhookSecret, body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/payments/mock", bytes.NewReader(body))
	req.Header.Set("X-Payment-Signature", sig)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// shipAndReturn drives a full create-shipment + advance-to-returned flow via
// the admin HTTP API, mirroring shippingEnv's TestShipmentEndToEndFlow.
func (e *pointsEnv) shipAndReturn(t *testing.T, ownerUserID, shopID, orderID int) {
	t.Helper()
	code, created := e.adminCallJSON(t, ownerUserID, "POST", e.shopPath(shopID, fmt.Sprintf("/orders/%d/shipments", orderID)), map[string]any{"carrier": "黑貓宅急便"})
	if code != http.StatusCreated {
		t.Fatalf("create shipment: want 201, got %d: %+v", code, created)
	}
	shipmentID := int(created["id"].(float64))
	code, advanced := e.adminCallJSON(t, ownerUserID, "PUT", e.shopPath(shopID, fmt.Sprintf("/orders/%d/shipments/%d", orderID, shipmentID)), map[string]any{"status": "returned"})
	if code != http.StatusOK {
		t.Fatalf("advance to returned: want 200, got %d: %+v", code, advanced)
	}
}

func (e *pointsEnv) memberRequest(memberID, shopID int, domain, method, path string, body any) (int, string, error) {
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

func (e *pointsEnv) memberCall(t *testing.T, memberID, shopID int, domain, method, path string, body any) (int, string) {
	t.Helper()
	code, raw, err := e.memberRequest(memberID, shopID, domain, method, path, body)
	if err != nil {
		t.Fatal(err)
	}
	return code, raw
}

func (e *pointsEnv) memberCallJSON(t *testing.T, memberID, shopID int, domain, method, path string, body any) (int, map[string]any) {
	t.Helper()
	code, raw := e.memberCall(t, memberID, shopID, domain, method, path, body)
	var parsed map[string]any
	_ = json.Unmarshal([]byte(raw), &parsed)
	return code, parsed
}

func (e *pointsEnv) adminCall(t *testing.T, userID int, method, path string, body any) (int, string) {
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

func (e *pointsEnv) adminCallJSON(t *testing.T, userID int, method, path string, body any) (int, map[string]any) {
	t.Helper()
	code, raw := e.adminCall(t, userID, method, path, body)
	var parsed map[string]any
	_ = json.Unmarshal([]byte(raw), &parsed)
	return code, parsed
}

func (e *pointsEnv) shopPath(shopID int, parts string) string {
	return fmt.Sprintf("/api/v1/admin/shops/%d%s", shopID, parts)
}

// ── member tier CRUD + validation ───────────────────────────────────────

func TestMemberTierCRUDHappyPath(t *testing.T) {
	e := newPointsEnv(t)

	code, created := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/member-tiers"), map[string]any{
		"name": "銀卡", "min_points": 100, "discount_percent": 5,
	})
	if code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d: %+v", code, created)
	}
	id := int(created["id"].(float64))

	code, got := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/member-tiers/%d", id)), nil)
	if code != http.StatusOK || got["name"] != "銀卡" {
		t.Fatalf("get: want 200 with name, got %d: %+v", code, got)
	}

	code, updated := e.adminCallJSON(t, e.ownerA, "PUT", e.shopPath(e.shopA, fmt.Sprintf("/member-tiers/%d", id)), map[string]any{"name": "金卡"})
	if code != http.StatusOK || updated["name"] != "金卡" {
		t.Fatalf("update: want 200 with updated name, got %d: %+v", code, updated)
	}

	code, list := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/member-tiers"), nil)
	if code != http.StatusOK || len(list["member_tiers"].([]any)) != 1 {
		t.Fatalf("list: want 200 with 1 tier, got %d: %+v", code, list)
	}

	code, _ = e.adminCall(t, e.ownerA, "DELETE", e.shopPath(e.shopA, fmt.Sprintf("/member-tiers/%d", id)), nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d", code)
	}
	code, _ = e.adminCall(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/member-tiers/%d", id)), nil)
	if code != http.StatusNotFound {
		t.Fatalf("get after delete: want 404, got %d", code)
	}
}

func TestMemberTierCreateValidation(t *testing.T) {
	e := newPointsEnv(t)

	cases := []map[string]any{
		{"name": "", "min_points": 0},
		{"name": "銀卡", "min_points": -1},
		{"name": "銀卡", "min_points": 0, "discount_percent": 101},
	}
	for i, body := range cases {
		code, resp := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/member-tiers"), body)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("case %d: want 422, got %d: %+v", i, code, resp)
		}
	}
}

func TestMemberTierRBACAndCrossShopIsolation(t *testing.T) {
	e := newPointsEnv(t)

	code, _ := e.adminCall(t, e.viewerA, "GET", e.shopPath(e.shopA, "/member-tiers"), nil)
	if code != http.StatusForbidden {
		t.Fatalf("viewer list: want 403, got %d", code)
	}
	code, _ = e.adminCall(t, e.viewerA, "POST", e.shopPath(e.shopA, "/member-tiers"), map[string]any{"name": "x", "min_points": 0})
	if code != http.StatusForbidden {
		t.Fatalf("viewer create: want 403, got %d", code)
	}
	code, _ = e.adminCall(t, e.ownerB, "GET", e.shopPath(e.shopA, "/member-tiers"), nil)
	if code != http.StatusForbidden {
		t.Fatalf("cross-shop list: want 403, got %d", code)
	}
}

func TestMemberTierDeleteClearsMemberLevel(t *testing.T) {
	e := newPointsEnv(t)
	_, created := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/member-tiers"), map[string]any{"name": "銀卡", "min_points": 5})
	tierID := int(created["id"].(float64))

	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)
	e.payOrder(t, e.shopA, e.memberA1, e.shopADomain, orderID) // 10 points awarded, crosses the 5-point tier

	code, before := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/members/%d/points", e.shopMemberA1)), nil)
	if code != http.StatusOK || before["level_id"] == nil {
		t.Fatalf("expected member to hold a level before tier deletion, got %d: %+v", code, before)
	}

	code, _ = e.adminCall(t, e.ownerA, "DELETE", e.shopPath(e.shopA, fmt.Sprintf("/member-tiers/%d", tierID)), nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete tier: want 204, got %d", code)
	}

	code, after := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/members/%d/points", e.shopMemberA1)), nil)
	if code != http.StatusOK {
		t.Fatalf("get points after tier delete: want 200, got %d", code)
	}
	if after["level_id"] != nil {
		t.Fatalf("expected level_id cleared to nil after tier deletion, got %+v", after["level_id"])
	}
	if after["points"].(float64) != 10 {
		t.Fatalf("expected points balance unaffected by tier deletion, got %+v", after["points"])
	}
}

// ── award on payment success (design D3/D4) ─────────────────────────────

func TestPointsAwardedOnPaymentSuccessEndToEnd(t *testing.T) {
	e := newPointsEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1290)
	e.payOrder(t, e.shopA, e.memberA1, e.shopADomain, orderID)

	// Member self-service view.
	code, mine := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/points", nil)
	if code != http.StatusOK {
		t.Fatalf("member get points: want 200, got %d: %+v", code, mine)
	}
	if mine["points"].(float64) != 12 { // 1290/100 = 12
		t.Fatalf("expected 12 points, got %+v", mine)
	}

	// Merchant back-office view + ledger.
	code, admin := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/members/%d/points", e.shopMemberA1)), nil)
	if code != http.StatusOK || admin["points"].(float64) != 12 {
		t.Fatalf("admin get points: want 200 with 12 points, got %d: %+v", code, admin)
	}
	code, ledger := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/members/%d/points/transactions", e.shopMemberA1)), nil)
	if code != http.StatusOK {
		t.Fatalf("admin list transactions: want 200, got %d", code)
	}
	txs := ledger["transactions"].([]any)
	if len(txs) != 1 {
		t.Fatalf("expected exactly one ledger row, got %d: %+v", len(txs), ledger)
	}
	first := txs[0].(map[string]any)
	if int16(first["kind"].(float64)) != points.KindOrderAward || first["points_delta"].(float64) != 12 {
		t.Fatalf("expected an award row with delta 12, got %+v", first)
	}
}

func TestPointsAwardIdempotentOnDuplicateWebhookDelivery(t *testing.T) {
	e := newPointsEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)

	code, initiated := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/payments", orderID), nil)
	if code != http.StatusCreated {
		t.Fatalf("initiate payment: want 201, got %d: %+v", code, initiated)
	}
	ref := initiated["provider_reference"].(string)
	body, _ := json.Marshal(map[string]string{"provider_reference": ref, "status": "succeeded"})
	sig := payment.SignMockWebhook(e.webhookSecret, body)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/payments/mock", bytes.NewReader(body))
		req.Header.Set("X-Payment-Signature", sig)
		rec := httptest.NewRecorder()
		e.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("webhook delivery %d: want 200, got %d: %s", i, rec.Code, rec.Body.String())
		}
	}

	code, mine := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/points", nil)
	if code != http.StatusOK || mine["points"].(float64) != 10 {
		t.Fatalf("expected points awarded exactly once (10) despite duplicate webhook delivery, got %d: %+v", code, mine)
	}
	_, ledger := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/members/%d/points/transactions", e.shopMemberA1)), nil)
	if len(ledger["transactions"].([]any)) != 1 {
		t.Fatalf("expected exactly one ledger row after duplicate delivery, got %+v", ledger)
	}
}

func TestPointsCrossesTierThresholdUpgradesLevel(t *testing.T) {
	e := newPointsEnv(t)
	_, silver := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, "/member-tiers"), map[string]any{"name": "銀卡", "min_points": 10})
	silverID := int(silver["id"].(float64))

	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1290) // -> 12 points
	e.payOrder(t, e.shopA, e.memberA1, e.shopADomain, orderID)

	code, mine := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/points", nil)
	if code != http.StatusOK {
		t.Fatalf("member get points: want 200, got %d", code)
	}
	if int(mine["level_id"].(float64)) != silverID {
		t.Fatalf("expected member to be upgraded to the silver tier, got %+v", mine)
	}
	level := mine["level"].(map[string]any)
	if level["name"] != "銀卡" {
		t.Fatalf("expected nested level detail to show the tier name, got %+v", level)
	}
}

// ── clawback on return (design D5) ──────────────────────────────────────

func TestPointsClawbackOnReturnEndToEnd(t *testing.T) {
	e := newPointsEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)
	e.payOrder(t, e.shopA, e.memberA1, e.shopADomain, orderID) // 10 points

	code, mine := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/points", nil)
	if code != http.StatusOK || mine["points"].(float64) != 10 {
		t.Fatalf("expected 10 points before return, got %d: %+v", code, mine)
	}

	e.shipAndReturn(t, e.ownerA, e.shopA, orderID)

	code, mine = e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/points", nil)
	if code != http.StatusOK || mine["points"].(float64) != 0 {
		t.Fatalf("expected points clawed back to 0 after return, got %d: %+v", code, mine)
	}

	_, ledger := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/members/%d/points/transactions", e.shopMemberA1)), nil)
	txs := ledger["transactions"].([]any)
	if len(txs) != 2 {
		t.Fatalf("expected 2 ledger rows (award + clawback), got %d: %+v", len(txs), ledger)
	}
}

// ── manual adjustment (design D10) ──────────────────────────────────────

func TestPointsAdjustRBACAndValidation(t *testing.T) {
	e := newPointsEnv(t)

	code, _ := e.adminCall(t, e.viewerA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/members/%d/points/adjust", e.shopMemberA1)),
		map[string]any{"points_delta": 10, "reason": "test"})
	if code != http.StatusForbidden {
		t.Fatalf("viewer adjust: want 403, got %d", code)
	}
	code, _ = e.adminCall(t, e.ownerB, "POST", e.shopPath(e.shopA, fmt.Sprintf("/members/%d/points/adjust", e.shopMemberA1)),
		map[string]any{"points_delta": 10, "reason": "test"})
	if code != http.StatusForbidden {
		t.Fatalf("cross-shop adjust: want 403, got %d", code)
	}

	code, adjusted := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/members/%d/points/adjust", e.shopMemberA1)),
		map[string]any{"points_delta": 50, "reason": "客服補償"})
	if code != http.StatusOK || adjusted["points"].(float64) != 50 {
		t.Fatalf("owner adjust: want 200 with 50 points, got %d: %+v", code, adjusted)
	}

	code, resp := e.adminCallJSON(t, e.ownerA, "POST", e.shopPath(e.shopA, fmt.Sprintf("/members/%d/points/adjust", e.shopMemberA1)),
		map[string]any{"points_delta": -1000, "reason": "太多了"})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("adjust below zero: want 422, got %d: %+v", code, resp)
	}
}

// ── cross-shop / cross-member isolation ─────────────────────────────────

func TestPointsAdminCrossShopIsolation(t *testing.T) {
	e := newPointsEnv(t)
	code, _ := e.adminCall(t, e.ownerB, "GET", e.shopPath(e.shopA, fmt.Sprintf("/members/%d/points", e.shopMemberA1)), nil)
	if code != http.StatusForbidden {
		t.Fatalf("cross-shop get points: want 403, got %d", code)
	}
}

func TestPointsAdminUnknownMemberIsNotFound(t *testing.T) {
	e := newPointsEnv(t)
	code, _ := e.adminCall(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/members/%d/points", e.shopMemberB1)), nil)
	if code != http.StatusNotFound {
		t.Fatalf("shop A owner querying a shop B member: want 404, got %d", code)
	}
}

func TestPointsMemberSelfServiceOnlySeesOwnData(t *testing.T) {
	e := newPointsEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)
	e.payOrder(t, e.shopA, e.memberA1, e.shopADomain, orderID) // member A1 earns 10 points

	code, a2 := e.memberCallJSON(t, e.memberA2, e.shopA, e.shopADomain, "GET", "/api/v1/shop/points", nil)
	if code != http.StatusOK || a2["points"].(float64) != 0 {
		t.Fatalf("expected member A2's own balance to be unaffected by member A1's award, got %d: %+v", code, a2)
	}

	code, ledger := e.memberCallJSON(t, e.memberA2, e.shopA, e.shopADomain, "GET", "/api/v1/shop/points/transactions", nil)
	if code != http.StatusOK {
		t.Fatalf("member A2 list transactions: want 200, got %d", code)
	}
	if len(ledger["transactions"].([]any)) != 0 {
		t.Fatalf("expected member A2's ledger to be empty, got %+v", ledger)
	}
}

func TestPointsMemberCrossShopTokenRejected(t *testing.T) {
	e := newPointsEnv(t)
	// A shop A member token used against shop B's domain must fail tenant
	// resolution before even reaching the points handler (mirrors existing
	// order/shipment member cross-shop token tests).
	code, _ := e.memberCall(t, e.memberA1, e.shopA, e.shopBDomain, "GET", "/api/v1/shop/points", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("cross-shop token: want 401, got %d", code)
	}
}

// ── RBAC role coverage — the real seed catalog grants the expected
// member_tier.*/point.* nodes to merchant_owner/editor ─────────────────

func TestSeedGrantsPointsPermissionsToMerchantOwnerAndEditor(t *testing.T) {
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
		"member_tier.view", "member_tier.create", "member_tier.edit", "member_tier.delete",
		"point.view", "point.adjust",
	} {
		if !owner[node] {
			t.Errorf("expected merchant_owner to be granted %s", node)
		}
	}

	editor := roleNames("editor")
	for _, node := range []string{
		"member_tier.view", "member_tier.create", "member_tier.edit",
		"point.view", "point.adjust",
	} {
		if !editor[node] {
			t.Errorf("expected editor to be granted %s", node)
		}
	}
	if editor["member_tier.delete"] {
		t.Error("expected editor NOT to be granted member_tier.delete (mirrors shipping_method.* breadth split)")
	}
}
