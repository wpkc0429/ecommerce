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
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/payment"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// paymentEnv exercises change payment-integration end-to-end: member-
// initiated payment, provider webhook confirmation (signature verification
// + idempotency), and merchant back-office payment record queries.
// Mirrors orderEnv's fixture shape (shopping-cart+order-management member/
// RBAC setup) plus a wired payment.Service backed by a MockProvider keyed
// on a known test secret so tests can construct validly-signed webhook
// requests via payment.SignMockWebhook.
type paymentEnv struct {
	client     *ent.Client
	rdb        *redis.Client
	router     http.Handler
	issuer     *auth.TokenIssuer
	resolver   *tenant.Resolver
	cartSvc    *cart.Service
	orderSvc   *order.Service
	paymentSvc *payment.Service

	webhookSecret string

	shopA, shopB             int
	shopADomain, shopBDomain string

	memberA1, memberA2 int // shop A members: cross-member isolation
	memberB1           int // shop B member: cross-shop isolation

	ownerA  int // merchant_owner of shop A: payment.view
	viewerA int // shop A role WITHOUT payment.view
	ownerB  int // merchant_owner of shop B: cross-shop isolation checks

	seq int // fixture uniqueness counter
}

func newPaymentEnv(t *testing.T) *paymentEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &paymentEnv{
		client: client, rdb: rdb,
		shopADomain: "payment-a.test", shopBDomain: "payment-b.test",
		webhookSecret: "payment-env-test-secret",
	}

	permIDs := map[string]int{}
	for _, p := range []string{"payment.view"} {
		permIDs[p] = client.Permission.Create().SetName(p).SaveX(ctx).ID
	}
	ownerRole := client.Role.Create().SetName("merchant_owner").SetScope("merchant").SaveX(ctx)
	client.RolePermission.Create().SetRoleID(ownerRole.ID).SetPermissionID(permIDs["payment.view"]).SaveX(ctx)
	viewerRole := client.Role.Create().SetName("editor").SetScope("merchant").SaveX(ctx) // no permissions granted

	e.shopA = client.Shop.Create().SetName("Shop A").SetStatus(1).SaveX(ctx).ID
	e.shopB = client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(ctx).ID

	siteA := client.Site.Create().SetDomain(e.shopADomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(siteA.ID).SetShopID(e.shopA).SetIsPrimary(true).SaveX(ctx)
	siteB := client.Site.Create().SetDomain(e.shopBDomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(siteB.ID).SetShopID(e.shopB).SetIsPrimary(true).SaveX(ctx)

	e.memberA1 = client.Member.Create().SetEmail("payment-member-a1@t.dev").SetStatus(1).SaveX(ctx).ID
	e.memberA2 = client.Member.Create().SetEmail("payment-member-a2@t.dev").SetStatus(1).SaveX(ctx).ID
	e.memberB1 = client.Member.Create().SetEmail("payment-member-b1@t.dev").SetStatus(1).SaveX(ctx).ID

	hash, _ := auth.HashPassword("password-123")
	e.ownerA = client.User.Create().SetEmail("payment-owner-a@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.ownerA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.ownerA).SetRoleID(ownerRole.ID).SetShopID(e.shopA).SaveX(ctx)

	e.viewerA = client.User.Create().SetEmail("payment-viewer-a@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.viewerA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.viewerA).SetRoleID(viewerRole.ID).SetShopID(e.shopA).SaveX(ctx)

	e.ownerB = client.User.Create().SetEmail("payment-owner-b@t.dev").SetPasswordHash(hash).SaveX(ctx).ID
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
	mock := payment.NewMockProvider(e.webhookSecret)
	e.paymentSvc = &payment.Service{
		Client:          client,
		Orders:          e.orderSvc,
		Providers:       map[string]payment.Provider{mock.Name(): mock},
		DefaultProvider: mock.Name(),
	}

	e.router = httpapi.New(httpapi.Deps{
		Cfg:      &config.Config{},
		Log:      log,
		AdminMW:  httpapi.NewAdminMiddleware(issuer),
		TenantMW: httpapi.NewTenantMiddleware(e.resolver),
		MemberMW: httpapi.NewMemberMiddleware(issuer),
		Orders:   &httpapi.OrderHandler{Client: client, Service: e.orderSvc, Authz: authz, Log: log},
		Payments: &httpapi.PaymentHandler{Client: client, Service: e.paymentSvc, Authz: authz, Log: log},
	})
	return e
}

func (e *paymentEnv) newSKU(t *testing.T, shopID int, price int64, currency string, stock int32) int {
	t.Helper()
	ctx := context.Background()
	e.seq++
	p := e.client.Product.Create().
		SetShopID(shopID).SetTitle(fmt.Sprintf("Product %d", e.seq)).SetSlug(fmt.Sprintf("payment-product-%d", e.seq)).
		SetStatus(1).SaveX(ctx)
	sku := e.client.ProductSKU.Create().
		SetShopID(shopID).SetProductID(p.ID).SetSkuCode(fmt.Sprintf("PAY-SKU-%d", e.seq)).
		SetPriceAmount(price).SetCurrency(currency).SetStockQty(stock).SetIsActive(true).
		SaveX(ctx)
	return sku.ID
}

func validShippingAddr() map[string]any {
	return map[string]any{
		"recipient_name": "王小明", "phone": "0912345678", "line1": "民生東路一段1號",
		"city": "台北市", "postal_code": "104", "country": "TW",
	}
}

// checkoutOrder is a compact "get me a fresh order" helper: seeds a SKU,
// adds it to memberID's cart via the real cart.Service, then checks out via
// the real HTTP endpoint (so denormalized snapshots etc. come from actual
// business logic — mirrors orderEnv's per-test setup shape).
func (e *paymentEnv) checkoutOrder(t *testing.T, shopID, memberID int, domain string, price int64) int {
	t.Helper()
	skuID := e.newSKU(t, shopID, price, "TWD", 10)
	if _, err := e.cartSvc.AddItem(context.Background(), shopID, memberID, skuID, 1); err != nil {
		t.Fatalf("seed cart: %v", err)
	}
	code, created := e.memberCallJSON(t, memberID, shopID, domain, "POST", "/api/v1/shop/checkout", validShippingAddr())
	if code != http.StatusCreated {
		t.Fatalf("checkout: want 201, got %d: %+v", code, created)
	}
	return int(created["id"].(float64))
}

func (e *paymentEnv) memberRequest(memberID, shopID int, domain, method, path string, body any) (int, string, error) {
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

func (e *paymentEnv) memberCall(t *testing.T, memberID, shopID int, domain, method, path string, body any) (int, string) {
	t.Helper()
	code, raw, err := e.memberRequest(memberID, shopID, domain, method, path, body)
	if err != nil {
		t.Fatal(err)
	}
	return code, raw
}

func (e *paymentEnv) memberCallJSON(t *testing.T, memberID, shopID int, domain, method, path string, body any) (int, map[string]any) {
	t.Helper()
	code, raw := e.memberCall(t, memberID, shopID, domain, method, path, body)
	var parsed map[string]any
	_ = json.Unmarshal([]byte(raw), &parsed)
	return code, parsed
}

func (e *paymentEnv) adminCall(t *testing.T, userID int, method, path string, body any) (int, string) {
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

func (e *paymentEnv) adminCallJSON(t *testing.T, userID int, method, path string, body any) (int, map[string]any) {
	t.Helper()
	code, raw := e.adminCall(t, userID, method, path, body)
	var parsed map[string]any
	_ = json.Unmarshal([]byte(raw), &parsed)
	return code, parsed
}

func (e *paymentEnv) shopPath(shopID int, parts string) string {
	return fmt.Sprintf("/api/v1/admin/shops/%d%s", shopID, parts)
}

// webhookBody builds the mock provider's wire payload.
func webhookBody(t *testing.T, providerReference, status string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]string{"provider_reference": providerReference, "status": status})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sendWebhook posts body to the mock provider's webhook route. sig == ""
// omits the signature header entirely (missing-signature case); pass an
// explicit (possibly wrong) signature otherwise.
func (e *paymentEnv) sendWebhook(body []byte, sig string) (int, map[string]any) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/payments/mock", bytes.NewReader(body))
	if sig != "" {
		req.Header.Set("X-Payment-Signature", sig)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var parsed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, parsed
}

func (e *paymentEnv) sendSignedWebhook(body []byte) (int, map[string]any) {
	return e.sendWebhook(body, payment.SignMockWebhook(e.webhookSecret, body))
}

// ── 7.2: end-to-end success flow ─────────────────────────────────────

func TestPaymentSuccessEndToEnd(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)

	code, initiated := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/payments", orderID), nil)
	if code != http.StatusCreated {
		t.Fatalf("initiate payment: want 201, got %d: %+v", code, initiated)
	}
	if initiated["status"].(float64) != float64(payment.StatusPending) {
		t.Fatalf("expected pending status, got %+v", initiated)
	}
	ref, _ := initiated["provider_reference"].(string)
	if ref == "" {
		t.Fatalf("expected a provider_reference: %+v", initiated)
	}
	if initiated["redirect_url"].(string) == "" {
		t.Fatalf("expected a redirect_url: %+v", initiated)
	}

	body := webhookBody(t, ref, "succeeded")
	code, resp := e.sendSignedWebhook(body)
	if code != http.StatusOK {
		t.Fatalf("webhook: want 200, got %d: %+v", code, resp)
	}

	gotOrder, err := e.orderSvc.GetOrderAdmin(context.Background(), e.shopA, orderID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.PaymentStatus != payment.OrderPaymentStatusPaid {
		t.Fatalf("expected order payment_status=paid, got %d", gotOrder.PaymentStatus)
	}

	// Merchant back office reflects the succeeded payment record.
	code, list := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/payments", orderID)), nil)
	if code != http.StatusOK {
		t.Fatalf("admin list payments: want 200, got %d", code)
	}
	payments := list["payments"].([]any)
	if len(payments) != 1 {
		t.Fatalf("expected exactly one payment record, got %d", len(payments))
	}
	first := payments[0].(map[string]any)
	if int16(first["status"].(float64)) != payment.StatusSucceeded {
		t.Fatalf("expected succeeded status in admin view, got %+v", first)
	}
}

// ── 7.3: signature verification failure ──────────────────────────────

func TestPaymentWebhookSignatureFailureRejected(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)
	_, initiated := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/payments", orderID), nil)
	ref := initiated["provider_reference"].(string)
	body := webhookBody(t, ref, "succeeded")

	// Missing signature.
	if code, resp := e.sendWebhook(body, ""); code != http.StatusUnauthorized {
		t.Fatalf("missing signature: want 401, got %d: %+v", code, resp)
	}
	// Wrong signature (signed with a different secret).
	wrongSig := payment.SignMockWebhook("not-the-real-secret", body)
	if code, resp := e.sendWebhook(body, wrongSig); code != http.StatusUnauthorized {
		t.Fatalf("wrong signature: want 401, got %d: %+v", code, resp)
	}

	// Neither attempt should have touched the order or the payment record.
	gotOrder, err := e.orderSvc.GetOrderAdmin(context.Background(), e.shopA, orderID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.PaymentStatus != order.PaymentStatusUnpaid {
		t.Fatalf("order must remain unpaid after rejected webhooks, got %d", gotOrder.PaymentStatus)
	}
	_, list := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/payments", orderID)), nil)
	first := list["payments"].([]any)[0].(map[string]any)
	if int16(first["status"].(float64)) != payment.StatusPending {
		t.Fatalf("payment record must remain pending after rejected webhooks, got %+v", first)
	}
}

// ── 7.4: webhook idempotency ──────────────────────────────────────────

func TestPaymentWebhookDuplicateDeliveryIsIdempotent(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)
	_, initiated := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/payments", orderID), nil)
	ref := initiated["provider_reference"].(string)
	body := webhookBody(t, ref, "succeeded")

	code1, resp1 := e.sendSignedWebhook(body)
	if code1 != http.StatusOK {
		t.Fatalf("first delivery: want 200, got %d: %+v", code1, resp1)
	}
	code2, resp2 := e.sendSignedWebhook(body)
	if code2 != http.StatusOK {
		t.Fatalf("duplicate delivery must be a safe no-op, not an error: got %d: %+v", code2, resp2)
	}

	gotOrder, err := e.orderSvc.GetOrderAdmin(context.Background(), e.shopA, orderID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.PaymentStatus != payment.OrderPaymentStatusPaid {
		t.Fatalf("expected order payment_status=paid exactly once, got %d", gotOrder.PaymentStatus)
	}

	_, list := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/payments", orderID)), nil)
	payments := list["payments"].([]any)
	if len(payments) != 1 {
		t.Fatalf("duplicate webhook delivery must not create a second payment record, got %d", len(payments))
	}
}

// ── 7.5: failed payment does not affect order, member can retry ───────

func TestPaymentFailedWebhookDoesNotAffectOrderAndAllowsRetry(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)

	_, first := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/payments", orderID), nil)
	ref1 := first["provider_reference"].(string)
	code, resp := e.sendSignedWebhook(webhookBody(t, ref1, "failed"))
	if code != http.StatusOK {
		t.Fatalf("failed webhook: want 200, got %d: %+v", code, resp)
	}

	gotOrder, err := e.orderSvc.GetOrderAdmin(context.Background(), e.shopA, orderID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.PaymentStatus != order.PaymentStatusUnpaid {
		t.Fatalf("failed payment must not change order payment_status, got %d", gotOrder.PaymentStatus)
	}

	// Retry: a second payment attempt on the same order succeeds and
	// produces a second payments row.
	code2, second := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/payments", orderID), nil)
	if code2 != http.StatusCreated {
		t.Fatalf("retry initiate: want 201, got %d: %+v", code2, second)
	}
	ref2 := second["provider_reference"].(string)
	if ref2 == ref1 {
		t.Fatal("retry must produce a distinct provider_reference")
	}

	_, list := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/payments", orderID)), nil)
	payments := list["payments"].([]any)
	if len(payments) != 2 {
		t.Fatalf("expected two payment records (failed + retry), got %d", len(payments))
	}
}

// ── 7.6: cross-member isolation ────────────────────────────────────────

func TestPaymentInitiateCrossMemberIsolation(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)

	code, resp := e.memberCallJSON(t, e.memberA2, e.shopA, e.shopADomain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/payments", orderID), nil)
	if code != http.StatusNotFound {
		t.Fatalf("cross-member initiate: want 404, got %d: %+v", code, resp)
	}
}

// ── 7.7: RBAC + cross-shop isolation on the admin payments endpoint ────

func TestPaymentAdminRBACAndCrossShopIsolation(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)
	e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/payments", orderID), nil)

	// Owner (payment.view) can list shop A's payments.
	code, list := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/payments", orderID)), nil)
	if code != http.StatusOK {
		t.Fatalf("owner list: want 200, got %d: %+v", code, list)
	}
	if len(list["payments"].([]any)) != 1 {
		t.Fatalf("expected 1 payment record, got %+v", list)
	}

	// Viewer (no payment.view) is forbidden.
	code, _ = e.adminCall(t, e.viewerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/payments", orderID)), nil)
	if code != http.StatusForbidden {
		t.Fatalf("viewer without payment.view: want 403, got %d", code)
	}

	// Shop B's owner cannot see shop A's order payments at all.
	code, _ = e.adminCall(t, e.ownerB, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/payments", orderID)), nil)
	if code != http.StatusForbidden {
		t.Fatalf("cross-shop: want 403, got %d", code)
	}
}

// ── 7.8: cancelled/already-paid orders reject new payment attempts ─────

func TestPaymentInitiateRejectedForCancelledOrder(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)
	code, _ := e.memberCall(t, e.memberA1, e.shopA, e.shopADomain, "POST", fmt.Sprintf("/api/v1/shop/orders/%d/cancel", orderID), nil)
	if code != http.StatusOK {
		t.Fatalf("cancel: want 200, got %d", code)
	}

	code, resp := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/payments", orderID), nil)
	if code != http.StatusConflict {
		t.Fatalf("initiate on cancelled order: want 409, got %d: %+v", code, resp)
	}
}

func TestPaymentInitiateRejectedForAlreadyPaidOrder(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)
	if _, err := e.orderSvc.UpdatePaymentStatus(context.Background(), e.shopA, orderID, payment.OrderPaymentStatusPaid); err != nil {
		t.Fatalf("advance payment status: %v", err)
	}

	code, resp := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/payments", orderID), nil)
	if code != http.StatusConflict {
		t.Fatalf("initiate on already-paid order: want 409, got %d: %+v", code, resp)
	}
}

// ── 7.9: unknown provider path segment ─────────────────────────────────

func TestPaymentWebhookUnknownProviderNotFound(t *testing.T) {
	e := newPaymentEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/payments/does-not-exist", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider: want 404, got %d", rec.Code)
	}
}
