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
	entmember "ksdevworks/ecommerce/api/internal/ent/member"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/guest"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/idempotency"
	"ksdevworks/ecommerce/api/internal/notify"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/payment"
	"ksdevworks/ecommerce/api/internal/points"
	"ksdevworks/ecommerce/api/internal/shipping"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// guestEnv exercises change guest-checkout end to end: unauthenticated
// checkout, the order access token's exact reach, the passwordless identity it
// creates, and the registration that later claims it.
//
// It wires more of the stack than the other envs on purpose. The whole premise
// of this change is "a guest order is an ordinary order" — proving that needs
// the real payment webhook (points award), the real shipping service, and the
// real member self-service routes side by side with the guest ones.
type guestEnv struct {
	client      *ent.Client
	rdb         *redis.Client
	router      http.Handler
	issuer      *auth.TokenIssuer
	resolver    *tenant.Resolver
	cartSvc     *cart.Service
	orderSvc    *order.Service
	shippingSvc *shipping.Service
	idemSvc     *idempotency.Service

	// change order-notification-email: the notification chain is wired into this
	// env so the HTTP-level tests can assert what a customer actually receives.
	// Every pre-existing test in this file is unaffected — a RecordingSender
	// touches no network — which is the same evidence-by-silence the
	// Idempotency-Key wiring left above.
	notifySender *notify.RecordingSender
	notifySvc    *notify.Service

	webhookSecret string

	shopA, shopB             int
	shopADomain, shopBDomain string

	memberA1 int // an ordinary password-holding shop A member

	seq int
}

func newGuestEnv(t *testing.T) *guestEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &guestEnv{
		client: client, rdb: rdb,
		shopADomain: "guest-a.test", shopBDomain: "guest-b.test",
		webhookSecret: "guest-env-test-secret",
	}

	e.shopA = client.Shop.Create().SetName("Shop A").SetStatus(1).SaveX(ctx).ID
	e.shopB = client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(ctx).ID

	siteA := client.Site.Create().SetDomain(e.shopADomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(siteA.ID).SetShopID(e.shopA).SetIsPrimary(true).SaveX(ctx)
	siteB := client.Site.Create().SetDomain(e.shopBDomain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(siteB.ID).SetShopID(e.shopB).SetIsPrimary(true).SaveX(ctx)

	hash, _ := auth.HashPassword("password-123")
	e.memberA1 = client.Member.Create().
		SetShopID(e.shopA).SetEmail("guest-member-a1@t.dev").SetPasswordHash(hash).SetStatus(1).
		SaveX(ctx).ID

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	e.resolver = tenant.NewResolver(client, rdb, log)
	refresh := auth.NewRefreshService(client, 30*24*time.Hour)

	dispatcher := events.NewDispatcher(log)
	pointsSvc := &points.Service{Client: client, Log: log}
	dispatcher.Subscribe(pointsSvc.Handle)

	// change order-notification-email: subscribed synchronously on purpose.
	// Production hands each job to a worker pool so an SMTP round trip never
	// lands on the checkout response (design D5); a test asserting "was an email
	// produced for this request" must not race that pool. What is under test
	// here is the chain — event published, notification produced, exactly once —
	// not the hop.
	e.notifySender = &notify.RecordingSender{}
	e.notifySvc = &notify.Service{Client: client, Sender: e.notifySender, Log: log}
	dispatcher.Subscribe(func(ctx context.Context, ev events.Event) {
		_ = e.notifySvc.DeliverEvent(ctx, ev)
	})

	e.cartSvc = &cart.Service{Client: client}
	// The dispatcher makes Checkout publish events.OrderPlaced after it commits
	// (change order-notification-email, design D15).
	e.orderSvc = &order.Service{Client: client, Dispatcher: dispatcher}
	// change checkout-idempotency: wired on both checkout handlers below. Every
	// pre-existing test in this file sends no Idempotency-Key header and is
	// therefore completely unaffected — that is the point of the header being
	// optional, and their unmodified passing is the evidence for it.
	e.idemSvc = &idempotency.Service{Client: client, TTL: time.Hour}
	mock := payment.NewMockProvider(e.webhookSecret)
	paymentSvc := &payment.Service{
		Client:          client,
		Orders:          e.orderSvc,
		Dispatcher:      dispatcher,
		Providers:       map[string]payment.Provider{mock.Name(): mock},
		DefaultProvider: mock.Name(),
	}
	e.shippingSvc = &shipping.Service{Client: client, Orders: e.orderSvc, Dispatcher: dispatcher}
	guestSvc := &guest.Service{Client: client, Cart: e.cartSvc, Orders: e.orderSvc}

	e.router = httpapi.New(httpapi.Deps{
		Cfg:           &config.Config{OrderAccessTokenTTL: time.Hour},
		Log:           log,
		AdminMW:       httpapi.NewAdminMiddleware(issuer),
		TenantMW:      httpapi.NewTenantMiddleware(e.resolver),
		MemberMW:      httpapi.NewMemberMiddleware(issuer),
		OrderAccessMW: httpapi.NewOrderAccessMiddleware(issuer),
		MemberAuth:    &httpapi.MemberAuthHandler{Client: client, Issuer: issuer, Refresh: refresh, Log: log},
		Cart:          &httpapi.CartHandler{Client: client, Service: e.cartSvc, Log: log},
		Orders:        &httpapi.OrderHandler{Client: client, Service: e.orderSvc, Log: log, Idempotency: e.idemSvc},
		Payments:      &httpapi.PaymentHandler{Client: client, Service: paymentSvc, Log: log},
		Shipping:      &httpapi.ShippingHandler{Client: client, Service: e.shippingSvc, Log: log},
		Points:        &httpapi.PointsHandler{Client: client, Service: pointsSvc, Log: log},
		Guest: &httpapi.GuestHandler{
			Client: client, Service: guestSvc, Orders: e.orderSvc,
			Payments: paymentSvc, Shipping: e.shippingSvc,
			Issuer: issuer, Cfg: &config.Config{OrderAccessTokenTTL: time.Hour}, Log: log,
			Idempotency: e.idemSvc,
		},
	})
	return e
}

// ── fixtures & request helpers ─────────────────────────────────────────

func (e *guestEnv) newSKU(t *testing.T, shopID int, price int64, currency string, stock int32) int {
	t.Helper()
	ctx := context.Background()
	e.seq++
	p := e.client.Product.Create().
		SetShopID(shopID).SetTitle(fmt.Sprintf("Product %d", e.seq)).SetSlug(fmt.Sprintf("guest-product-%d", e.seq)).
		SetStatus(1).SaveX(ctx)
	sku := e.client.ProductSKU.Create().
		SetShopID(shopID).SetProductID(p.ID).SetSkuCode(fmt.Sprintf("GUEST-SKU-%d", e.seq)).
		SetPriceAmount(price).SetCurrency(currency).SetStockQty(stock).SetIsActive(true).
		SaveX(ctx)
	return sku.ID
}

// guestBody builds a complete guest checkout payload. The shipping address
// deliberately carries only the three required fields — that is the exact shape
// ONDO's real form produces (change guest-checkout, design D8).
func guestBody(email string, items ...map[string]any) map[string]any {
	return map[string]any{
		"email": email,
		"items": items,
		"shipping_address": map[string]any{
			"recipient_name": "王小明",
			"phone":          "0912345678",
			"line1":          "台北市松山區民生東路一段1號",
		},
	}
}

func guestItem(skuID int, qty int) map[string]any {
	return map[string]any{"sku_id": skuID, "quantity": qty}
}

// guestRequest posts an unauthenticated guest request. Returns the raw error
// rather than calling t.Fatal so it is safe from goroutines (mirrors
// orderEnv.memberRequest).
func (e *guestEnv) guestRequest(domain, method, path string, body any) (int, string, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return 0, "", err
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if domain != "" {
		req.Host = domain
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), nil
}

func (e *guestEnv) guestCall(t *testing.T, domain, method, path string, body any) (int, map[string]any) {
	t.Helper()
	code, raw, err := e.guestRequest(domain, method, path, body)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	_ = json.Unmarshal([]byte(raw), &parsed)
	return code, parsed
}

// tokenCall issues a request bearing an arbitrary token value (used for both
// order access tokens and — in the isolation tests — the wrong kind of token).
func (e *guestEnv) tokenCall(t *testing.T, token, domain, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if domain != "" {
		req.Host = domain
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var parsed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, parsed
}

func (e *guestEnv) memberTokenCall(t *testing.T, memberID, shopID int, domain, method, path string, body any) (int, map[string]any) {
	t.Helper()
	tok, err := e.issuer.IssueMember(memberID, shopID)
	if err != nil {
		t.Fatal(err)
	}
	return e.tokenCall(t, tok, domain, method, path, body)
}

// checkoutAsGuest runs one successful guest checkout and returns
// (orderID, orderAccessToken).
func (e *guestEnv) checkoutAsGuest(t *testing.T, domain, email string, skuID int, qty int) (int, string) {
	t.Helper()
	code, body := e.guestCall(t, domain, "POST", "/api/v1/shop/guest/checkout", guestBody(email, guestItem(skuID, qty)))
	if code != http.StatusCreated {
		t.Fatalf("guest checkout: want 201, got %d: %+v", code, body)
	}
	o, ok := body["order"].(map[string]any)
	if !ok {
		t.Fatalf("guest checkout response has no order: %+v", body)
	}
	tok, _ := body["order_access_token"].(string)
	if tok == "" {
		t.Fatalf("guest checkout response has no order_access_token: %+v", body)
	}
	return int(o["id"].(float64)), tok
}

func (e *guestEnv) identity(t *testing.T, shopID int, email string) *ent.Member {
	t.Helper()
	m, err := e.client.Member.Query().
		Where(entmember.ShopIDEQ(shopID), entmember.EmailEQ(email)).
		Only(context.Background())
	if err != nil {
		t.Fatalf("identity %q not found in shop %d: %v", email, shopID, err)
	}
	return m
}

// ── Scenario: 全新 email 的訪客結帳 ──────────────────────────────────────

func TestGuestCheckoutCreatesPasswordlessIdentityAndOrder(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 1500, "TWD", 10)

	orderID, token := e.checkoutAsGuest(t, e.shopADomain, "first-time@t.dev", skuID, 2)

	m := e.identity(t, e.shopA, "first-time@t.dev")
	if m.PasswordHash != nil {
		t.Fatal("guest identity must be created without a password")
	}
	if m.Status != 1 {
		t.Fatalf("guest identity must be active (design D10), got status=%d", m.Status)
	}

	o := e.client.Order.GetX(ctx, orderID)
	if o.MemberID != m.ID {
		t.Fatalf("order must be bound to the guest identity: order.member_id=%d, member=%d", o.MemberID, m.ID)
	}
	if o.TotalAmount != 3000 {
		t.Fatalf("want total 3000, got %d", o.TotalAmount)
	}
	if got := e.client.ProductSKU.GetX(ctx, skuID).StockQty; got != 8 {
		t.Fatalf("stock must be decremented by the ordinary checkout path: want 8, got %d", got)
	}

	// The token works and names this order.
	code, body := e.tokenCall(t, token, e.shopADomain, "GET", "/api/v1/shop/guest/order", nil)
	if code != http.StatusOK {
		t.Fatalf("read own order: want 200, got %d: %+v", code, body)
	}
	if int(body["id"].(float64)) != orderID {
		t.Fatalf("token returned the wrong order: %+v", body)
	}
}

// ── Scenario: 回頭訪客沿用同一身分 ──────────────────────────────────────

func TestGuestCheckoutReturningCustomerReusesIdentity(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	sku1 := e.newSKU(t, e.shopA, 1000, "TWD", 10)
	sku2 := e.newSKU(t, e.shopA, 2000, "TWD", 10)

	order1, _ := e.checkoutAsGuest(t, e.shopADomain, "repeat@t.dev", sku1, 1)
	order2, _ := e.checkoutAsGuest(t, e.shopADomain, "repeat@t.dev", sku2, 1)

	n := e.client.Member.Query().Where(entmember.ShopIDEQ(e.shopA), entmember.EmailEQ("repeat@t.dev")).CountX(ctx)
	if n != 1 {
		t.Fatalf("a returning guest must reuse one identity, found %d", n)
	}
	m := e.identity(t, e.shopA, "repeat@t.dev")
	for _, id := range []int{order1, order2} {
		if got := e.client.Order.GetX(ctx, id).MemberID; got != m.ID {
			t.Fatalf("order %d bound to member %d, want %d", id, got, m.ID)
		}
	}
}

// ── Scenario: email 已是本店有密碼會員 ──────────────────────────────────
//
// The order attaches to that member, and the response must be indistinguishable
// from a brand-new address (design D6 problem one) — in particular it must not
// hand back anything that reaches the member's other orders.

func TestGuestCheckoutWithExistingPasswordMemberIsIsomorphic(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 1000, "TWD", 10)

	// The member already has an order of their own.
	priorSKU := e.newSKU(t, e.shopA, 500, "TWD", 5)
	if _, err := e.cartSvc.AddItem(ctx, e.shopA, e.memberA1, priorSKU, 1, ""); err != nil {
		t.Fatal(err)
	}
	priorOrder, err := e.orderSvc.Checkout(ctx, e.shopA, e.memberA1, order.CheckoutInput{
		ShippingAddress: order.ShippingAddress{
			RecipientName: "王小明", Phone: "0900000000", Line1: "既有訂單地址",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	freshCode, freshBody := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		guestBody("brand-new@t.dev", guestItem(skuID, 1)))
	takenCode, takenBody := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		guestBody("guest-member-a1@t.dev", guestItem(skuID, 1)))

	if freshCode != takenCode {
		t.Fatalf("status codes must be isomorphic: fresh=%d taken=%d", freshCode, takenCode)
	}
	if keysOfMap(freshBody) != keysOfMap(takenBody) {
		t.Fatalf("response shapes must be isomorphic: fresh=%s taken=%s", keysOfMap(freshBody), keysOfMap(takenBody))
	}

	takenOrder := takenBody["order"].(map[string]any)
	if int(takenOrder["member_id"].(float64)) != e.memberA1 {
		t.Fatalf("order must attach to the existing member, got member_id=%v", takenOrder["member_id"])
	}

	// The token names only the new order — the member's prior order stays out
	// of reach, and there is no way to list history with it.
	token := takenBody["order_access_token"].(string)
	code, body := e.tokenCall(t, token, e.shopADomain, "GET", "/api/v1/shop/guest/order", nil)
	if code != http.StatusOK || int(body["id"].(float64)) == priorOrder.ID {
		t.Fatalf("order access token must name only the new order, got %d: %+v", code, body)
	}
	if code, body := e.tokenCall(t, token, e.shopADomain, "GET", "/api/v1/shop/orders", nil); code != http.StatusUnauthorized {
		t.Fatalf("order access token must not list order history: got %d: %+v", code, body)
	}
}

func keysOfMap(m map[string]any) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	// Small maps; a simple insertion sort keeps the helper dependency-free.
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && ks[j] < ks[j-1]; j-- {
			ks[j], ks[j-1] = ks[j-1], ks[j]
		}
	}
	out := ""
	for _, k := range ks {
		out += k + ","
	}
	return out
}

// ── Scenario: 收件電話不寫入身分 ────────────────────────────────────────

func TestGuestCheckoutDoesNotWritePhoneToIdentity(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 1000, "TWD", 10)

	// Another customer of this shop already owns that phone number. If guest
	// checkout wrote members.phone, UNIQUE(shop_id, phone) would reject this
	// perfectly reasonable order (design D2).
	e.client.Member.Create().SetShopID(e.shopA).SetEmail("phone-owner@t.dev").SetPhone("0912345678").SetStatus(1).SaveX(ctx)

	orderID, _ := e.checkoutAsGuest(t, e.shopADomain, "shares-phone@t.dev", skuID, 1)

	m := e.identity(t, e.shopA, "shares-phone@t.dev")
	if m.Phone != nil {
		t.Fatalf("guest identity must not carry a phone, got %q", *m.Phone)
	}
	var addr map[string]any
	if err := json.Unmarshal(e.client.Order.GetX(ctx, orderID).ShippingAddress, &addr); err != nil {
		t.Fatal(err)
	}
	if addr["phone"] != "0912345678" {
		t.Fatalf("the phone belongs on the order's shipping address, got %+v", addr)
	}
}

// ── Scenario: 缺少 email 被拒 / 品項為空 ────────────────────────────────

func TestGuestCheckoutInputValidation(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 1000, "TWD", 10)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing email", map[string]any{
			"items":            []map[string]any{guestItem(skuID, 1)},
			"shipping_address": map[string]any{"recipient_name": "王", "phone": "09", "line1": "地址"},
		}},
		{"malformed email", guestBody("not-an-email", guestItem(skuID, 1))},
		{"no items", guestBody("no-items@t.dev")},
		{"missing recipient phone", map[string]any{
			"email":            "no-phone@t.dev",
			"items":            []map[string]any{guestItem(skuID, 1)},
			"shipping_address": map[string]any{"recipient_name": "王", "line1": "地址"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout", tc.body)
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("want 422, got %d: %+v", code, body)
			}
		})
	}
	if n := e.client.Order.Query().CountX(ctx); n != 0 {
		t.Fatalf("no order may be created by invalid input, got %d", n)
	}
	// Input rejected before any identity is created.
	if n := e.client.Member.Query().Where(entmember.EmailEQ("no-items@t.dev")).CountX(ctx); n != 0 {
		t.Fatalf("an empty item list must not leave an identity behind, got %d", n)
	}
	if got := e.client.ProductSKU.GetX(ctx, skuID).StockQty; got != 10 {
		t.Fatalf("stock must be untouched, got %d", got)
	}
}

// ── Scenario: 品項驗證失敗不留下半套購物車 ──────────────────────────────

func TestGuestCheckoutInvalidItemLeavesCartUnchanged(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	goodSKU := e.newSKU(t, e.shopA, 1000, "TWD", 10)
	foreignSKU := e.newSKU(t, e.shopB, 1000, "TWD", 10)

	// Establish an identity with a cart holding one line, so the failure has
	// something to damage if ReplaceItems were not atomic.
	e.checkoutAsGuest(t, e.shopADomain, "atomic@t.dev", goodSKU, 1)
	m := e.identity(t, e.shopA, "atomic@t.dev")
	if _, err := e.cartSvc.AddItem(ctx, e.shopA, m.ID, goodSKU, 3, ""); err != nil {
		t.Fatal(err)
	}

	code, body := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		guestBody("atomic@t.dev", guestItem(goodSKU, 1), guestItem(foreignSKU, 1)))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 for a foreign SKU, got %d: %+v", code, body)
	}

	view, err := e.cartSvc.GetCartView(ctx, e.shopA, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Items) != 1 || view.Items[0].Quantity != 3 {
		t.Fatalf("cart must be exactly as it was before the failed request, got %+v", view.Items)
	}
	if n := e.client.Order.Query().CountX(ctx); n != 1 {
		t.Fatalf("no second order may exist, got %d", n)
	}
}

// ── Scenario: 訪客結帳取代既有購物車內容 ────────────────────────────────

func TestGuestCheckoutReplacesExistingCartContents(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	oldSKU := e.newSKU(t, e.shopA, 9999, "TWD", 10)
	newSKU := e.newSKU(t, e.shopA, 1000, "TWD", 10)

	// A claimed identity shopping on another device.
	m := e.client.Member.Create().SetShopID(e.shopA).SetEmail("replaces@t.dev").SetStatus(1).SaveX(ctx)
	if _, err := e.cartSvc.AddItem(ctx, e.shopA, m.ID, oldSKU, 2, ""); err != nil {
		t.Fatal(err)
	}

	orderID, _ := e.checkoutAsGuest(t, e.shopADomain, "replaces@t.dev", newSKU, 1)

	o, err := e.orderSvc.GetOrderAdmin(ctx, e.shopA, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Edges.Items) != 1 || *o.Edges.Items[0].SkuID != newSKU {
		t.Fatalf("the order must contain only the submitted line, got %d items", len(o.Edges.Items))
	}
	if o.TotalAmount != 1000 {
		t.Fatalf("the pre-existing cart line must not be billed, total=%d", o.TotalAmount)
	}
	if got := e.client.ProductSKU.GetX(ctx, oldSKU).StockQty; got != 10 {
		t.Fatalf("the replaced line's stock must be untouched, got %d", got)
	}
}

// ── Scenario: 訂單存取 token 的作用範圍 ────────────────────────────────

func TestOrderAccessTokenScope(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 1000, "TWD", 10)

	orderID, token := e.checkoutAsGuest(t, e.shopADomain, "scope@t.dev", skuID, 1)
	otherOrder, otherToken := e.checkoutAsGuest(t, e.shopADomain, "scope-other@t.dev", skuID, 1)

	t.Run("reads its own order", func(t *testing.T) {
		code, body := e.tokenCall(t, token, e.shopADomain, "GET", "/api/v1/shop/guest/order", nil)
		if code != http.StatusOK || int(body["id"].(float64)) != orderID {
			t.Fatalf("want 200 for order %d, got %d: %+v", orderID, code, body)
		}
	})

	t.Run("initiates payment for its own order", func(t *testing.T) {
		code, body := e.tokenCall(t, token, e.shopADomain, "POST", "/api/v1/shop/guest/order/payments", nil)
		if code != http.StatusCreated {
			t.Fatalf("want 201, got %d: %+v", code, body)
		}
		if body["redirect_url"] == nil || body["redirect_url"] == "" {
			t.Fatalf("payment initiation must return a redirect_url: %+v", body)
		}
	})

	t.Run("reads its own shipment", func(t *testing.T) {
		if _, err := e.shippingSvc.CreateShipment(ctx, e.shopA, orderID, "黑貓", nil, nil); err != nil {
			t.Fatal(err)
		}
		code, body := e.tokenCall(t, token, e.shopADomain, "GET", "/api/v1/shop/guest/order/shipment", nil)
		if code != http.StatusOK || int(body["order_id"].(float64)) != orderID {
			t.Fatalf("want 200 for order %d's shipment, got %d: %+v", orderID, code, body)
		}
		if len(body["items"].([]any)) == 0 {
			t.Fatalf("a shipment must say what is in it (change partial-shipment): %+v", body)
		}
	})

	// change partial-shipment, design D6: guests reach the plural form too —
	// ONDO has no login UI, so "members only" would mean nobody.
	t.Run("reads every shipment of its order", func(t *testing.T) {
		code, body := e.tokenCall(t, token, e.shopADomain, "GET", "/api/v1/shop/guest/order/shipments", nil)
		if code != http.StatusOK {
			t.Fatalf("want 200, got %d: %+v", code, body)
		}
		list := body["shipments"].([]any)
		if len(list) != 1 {
			t.Fatalf("expected exactly this order's one parcel, got %+v", body)
		}
		if int(list[0].(map[string]any)["order_id"].(float64)) != orderID {
			t.Fatalf("token reached the wrong order's parcels: %+v", body)
		}
	})

	// A collection's empty case is 200 with an empty array, unlike the singular
	// endpoint's 404. Asserted against the *other* guest's order, which has
	// nothing shipped.
	t.Run("lists no shipments as an empty collection", func(t *testing.T) {
		code, body := e.tokenCall(t, otherToken, e.shopADomain, "GET", "/api/v1/shop/guest/order/shipments", nil)
		if code != http.StatusOK {
			t.Fatalf("want 200 for an unshipped order, got %d: %+v", code, body)
		}
		if len(body["shipments"].([]any)) != 0 {
			t.Fatalf("expected an empty collection, got %+v", body)
		}
		code, _ = e.tokenCall(t, otherToken, e.shopADomain, "GET", "/api/v1/shop/guest/order/shipment", nil)
		if code != http.StatusNotFound {
			t.Fatalf("the singular endpoint must keep its 404, got %d", code)
		}
	})

	t.Run("cannot see another guest's order", func(t *testing.T) {
		code, body := e.tokenCall(t, token, e.shopADomain, "GET", "/api/v1/shop/guest/order", nil)
		if int(body["id"].(float64)) == otherOrder {
			t.Fatalf("token reached the wrong order: %d %+v", code, body)
		}
		// ...and the other guest's token cannot reach this one either.
		_, otherBody := e.tokenCall(t, otherToken, e.shopADomain, "GET", "/api/v1/shop/guest/order", nil)
		if int(otherBody["id"].(float64)) != otherOrder {
			t.Fatalf("the other token reached the wrong order: %+v", otherBody)
		}
	})

	t.Run("cannot list order history", func(t *testing.T) {
		if code, _ := e.tokenCall(t, token, e.shopADomain, "GET", "/api/v1/shop/orders", nil); code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", code)
		}
	})

	t.Run("cannot cancel", func(t *testing.T) {
		path := fmt.Sprintf("/api/v1/shop/orders/%d/cancel", orderID)
		if code, _ := e.tokenCall(t, token, e.shopADomain, "POST", path, nil); code != http.StatusUnauthorized {
			t.Fatalf("want 401 on the member cancel route, got %d", code)
		}
		// There is no cancel route under /guest at all.
		if code, _ := e.tokenCall(t, token, e.shopADomain, "POST", "/api/v1/shop/guest/order/cancel", nil); code != http.StatusNotFound {
			t.Fatalf("want 404 (no such route), got %d", code)
		}
		if e.client.Order.GetX(ctx, orderID).Status != order.StatusCreated {
			t.Fatal("order status must be unchanged")
		}
	})

	t.Run("cannot read points", func(t *testing.T) {
		if code, _ := e.tokenCall(t, token, e.shopADomain, "GET", "/api/v1/shop/points", nil); code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", code)
		}
	})

	t.Run("is not accepted as a query parameter", func(t *testing.T) {
		path := "/api/v1/shop/guest/order?order_access_token=" + token
		if code, _ := e.tokenCall(t, "", e.shopADomain, "GET", path, nil); code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", code)
		}
	})
}

func TestOrderAccessTokenExpiryRejected(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 1000, "TWD", 10)
	orderID, _ := e.checkoutAsGuest(t, e.shopADomain, "expiry@t.dev", skuID, 1)

	expired, err := e.issuer.IssueOrderAccess(orderID, e.shopA, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if code, body := e.tokenCall(t, expired, e.shopADomain, "GET", "/api/v1/shop/guest/order", nil); code != http.StatusUnauthorized {
		t.Fatalf("want 401 for an expired token, got %d: %+v", code, body)
	}
}

// ── Scenario: 訂單存取 token 與會員／後台 token 完全隔離 ────────────────

func TestGuestTokenIsolation(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 1000, "TWD", 10)
	orderID, token := e.checkoutAsGuest(t, e.shopADomain, "isolation@t.dev", skuID, 1)

	t.Run("member token cannot reach guest routes", func(t *testing.T) {
		code, _ := e.memberTokenCall(t, e.memberA1, e.shopA, e.shopADomain, "GET", "/api/v1/shop/guest/order", nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", code)
		}
	})

	t.Run("admin token cannot reach guest routes", func(t *testing.T) {
		adminTok, err := e.issuer.IssueAdmin(1, nil)
		if err != nil {
			t.Fatal(err)
		}
		if code, _ := e.tokenCall(t, adminTok, e.shopADomain, "GET", "/api/v1/shop/guest/order", nil); code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", code)
		}
	})

	t.Run("order access token is inert at another shop", func(t *testing.T) {
		if code, _ := e.tokenCall(t, token, e.shopBDomain, "GET", "/api/v1/shop/guest/order", nil); code != http.StatusUnauthorized {
			t.Fatalf("want 401 in shop B context, got %d", code)
		}
	})

	t.Run("a shop B token cannot reach a shop A order", func(t *testing.T) {
		crossTok, err := e.issuer.IssueOrderAccess(orderID, e.shopB, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if code, _ := e.tokenCall(t, crossTok, e.shopADomain, "GET", "/api/v1/shop/guest/order", nil); code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", code)
		}
	})

	t.Run("no token at all", func(t *testing.T) {
		if code, _ := e.tokenCall(t, "", e.shopADomain, "GET", "/api/v1/shop/guest/order", nil); code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", code)
		}
	})
}

// ── Scenario: 併發訪客結帳搶購同一 SKU 不超賣 ───────────────────────────
//
// MANDATORY correctness test. Guest checkout must inherit order.Service.
// Checkout's guarantee unchanged, because it calls it rather than reproducing
// it (design D1).

func TestGuestCheckoutConcurrentOversellingProtection(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()

	const stock = 5
	const n = 10
	skuID := e.newSKU(t, e.shopA, 500, "TWD", stock)

	codes := make([]int, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := guestBody(fmt.Sprintf("rush-%d@t.dev", i), guestItem(skuID, 1))
			code, _, err := e.guestRequest(e.shopADomain, "POST", "/api/v1/shop/guest/checkout", body)
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
		t.Errorf("want %d successful guest checkouts, got %d (codes=%v)", stock, succeeded, codes)
	}
	if failed != n-stock {
		t.Errorf("want %d failed guest checkouts, got %d (codes=%v)", n-stock, failed, codes)
	}
	if got := e.client.ProductSKU.GetX(ctx, skuID).StockQty; got != 0 {
		t.Errorf("stock must land exactly at 0 (never negative), got %d", got)
	}
	if got := e.client.Order.Query().CountX(ctx); got != stock {
		t.Errorf("want exactly %d orders, got %d", stock, got)
	}
}

// ── Scenario: 訪客與會員併發搶購同一 SKU 共用同一份庫存 ──────────────────

func TestGuestAndMemberCheckoutShareOneStockGuard(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()

	const stock = 4
	const perSide = 4
	skuID := e.newSKU(t, e.shopA, 500, "TWD", stock)

	// Member side: four members with a seeded cart each.
	memberIDs := make([]int, perSide)
	for i := 0; i < perSide; i++ {
		memberIDs[i] = e.client.Member.Create().
			SetShopID(e.shopA).SetEmail(fmt.Sprintf("mixed-member-%d@t.dev", i)).SetStatus(1).
			SaveX(ctx).ID
		if _, err := e.cartSvc.AddItem(ctx, e.shopA, memberIDs[i], skuID, 1, ""); err != nil {
			t.Fatal(err)
		}
	}

	codes := make([]int, perSide*2)
	errs := make(chan error, perSide*2)
	var wg sync.WaitGroup
	for i := 0; i < perSide; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			tok, err := e.issuer.IssueMember(memberIDs[i], e.shopA)
			if err != nil {
				errs <- err
				return
			}
			var buf bytes.Buffer
			_ = json.NewEncoder(&buf).Encode(map[string]any{
				"recipient_name": "王小明", "phone": "0912345678", "line1": "地址",
			})
			req := httptest.NewRequest("POST", "/api/v1/shop/checkout", &buf)
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Host = e.shopADomain
			rec := httptest.NewRecorder()
			e.router.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
		go func(i int) {
			defer wg.Done()
			body := guestBody(fmt.Sprintf("mixed-guest-%d@t.dev", i), guestItem(skuID, 1))
			code, _, err := e.guestRequest(e.shopADomain, "POST", "/api/v1/shop/guest/checkout", body)
			if err != nil {
				errs <- err
				return
			}
			codes[perSide+i] = code
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	var succeeded int
	for _, c := range codes {
		if c == http.StatusCreated {
			succeeded++
		}
	}
	if succeeded != stock {
		t.Errorf("both entry points must draw on the same %d units, got %d successes (codes=%v)", stock, succeeded, codes)
	}
	if got := e.client.ProductSKU.GetX(ctx, skuID).StockQty; got != 0 {
		t.Errorf("stock must land exactly at 0, got %d", got)
	}
}

// ── Scenario: 認領（註冊）後訪客訂單與點數自動浮現 ──────────────────────

func TestGuestIdentityClaimedByRegistration(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 100000, "TWD", 10) // 100000/100 = 1000 points

	orderID, token := e.checkoutAsGuest(t, e.shopADomain, "claimer@t.dev", skuID, 1)
	before := e.identity(t, e.shopA, "claimer@t.dev")

	// Pay for it so points are awarded to the passwordless identity.
	code, initiated := e.tokenCall(t, token, e.shopADomain, "POST", "/api/v1/shop/guest/order/payments", nil)
	if code != http.StatusCreated {
		t.Fatalf("initiate payment: want 201, got %d: %+v", code, initiated)
	}
	ref := initiated["provider_reference"].(string)
	webhook, _ := json.Marshal(map[string]string{"provider_reference": ref, "status": "succeeded"})
	req := httptest.NewRequest("POST", "/api/v1/webhooks/payments/mock", bytes.NewReader(webhook))
	req.Header.Set("X-Payment-Signature", payment.SignMockWebhook(e.webhookSecret, webhook))
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Now the customer decides to make an account, with the same address.
	regCode, reg := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/auth/register",
		map[string]any{"email": "claimer@t.dev", "password": "claim-password-1"})
	if regCode != http.StatusOK {
		t.Fatalf("claim registration: want 200, got %d: %+v", regCode, reg)
	}
	access, _ := reg["access_token"].(string)
	if access == "" {
		t.Fatalf("claim must issue tokens like any registration: %+v", reg)
	}

	after := e.identity(t, e.shopA, "claimer@t.dev")
	if after.ID != before.ID {
		t.Fatalf("claim must not change member_id: %d → %d", before.ID, after.ID)
	}
	if after.PasswordHash == nil {
		t.Fatal("claim must set a password")
	}
	if e.client.Order.GetX(ctx, orderID).MemberID != before.ID {
		t.Fatal("the order's member_id must not move")
	}

	// The prior guest order and its points are simply there — no migration.
	histCode, hist := e.tokenCall(t, access, e.shopADomain, "GET", "/api/v1/shop/orders", nil)
	if histCode != http.StatusOK {
		t.Fatalf("order history: want 200, got %d: %+v", histCode, hist)
	}
	orders := hist["orders"].([]any)
	if len(orders) != 1 || int(orders[0].(map[string]any)["id"].(float64)) != orderID {
		t.Fatalf("the guest order must appear in history: %+v", hist)
	}

	ptsCode, pts := e.tokenCall(t, access, e.shopADomain, "GET", "/api/v1/shop/points", nil)
	if ptsCode != http.StatusOK {
		t.Fatalf("points: want 200, got %d: %+v", ptsCode, pts)
	}
	if int(pts["points"].(float64)) != 1000 {
		t.Fatalf("points awarded to the guest identity must survive the claim, got %+v", pts)
	}
}

func TestClaimedIdentityCanCancelItsGuestOrder(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 1000, "TWD", 10)
	orderID, _ := e.checkoutAsGuest(t, e.shopADomain, "canceller@t.dev", skuID, 2)

	_, reg := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/auth/register",
		map[string]any{"email": "canceller@t.dev", "password": "claim-password-1"})
	access := reg["access_token"].(string)

	path := fmt.Sprintf("/api/v1/shop/orders/%d/cancel", orderID)
	if code, body := e.tokenCall(t, access, e.shopADomain, "POST", path, nil); code != http.StatusOK {
		t.Fatalf("cancel after claim: want 200, got %d: %+v", code, body)
	}
	if e.client.Order.GetX(ctx, orderID).Status != order.StatusCancelled {
		t.Fatal("order must be cancelled")
	}
	if got := e.client.ProductSKU.GetX(ctx, skuID).StockQty; got != 10 {
		t.Fatalf("stock must be restored, got %d", got)
	}
}

// ── Scenario: 並發認領僅一次成功 ────────────────────────────────────────

func TestConcurrentClaimOnlyOneWins(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 1000, "TWD", 10)
	e.checkoutAsGuest(t, e.shopADomain, "race-claim@t.dev", skuID, 1)

	const n = 4
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := map[string]any{"email": "race-claim@t.dev", "password": fmt.Sprintf("claim-password-%d", i)}
			code, _, err := e.guestRequest(e.shopADomain, "POST", "/api/v1/shop/auth/register", body)
			if err != nil {
				return
			}
			codes[i] = code
		}(i)
	}
	wg.Wait()

	var won int
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			won++
		case http.StatusUnprocessableEntity:
		default:
			t.Errorf("unexpected status: %d", c)
		}
	}
	if won != 1 {
		t.Fatalf("exactly one concurrent claim may win, got %d (codes=%v)", won, codes)
	}
	if n := e.client.Member.Query().
		Where(entmember.ShopIDEQ(e.shopA), entmember.EmailEQ("race-claim@t.dev")).CountX(ctx); n != 1 {
		t.Fatalf("still exactly one identity expected, got %d", n)
	}
}

// ── Scenario: 已設定密碼者不可被再次認領 / 無密碼身分無法登入 ────────────

func TestPasswordHolderIsNotReclaimableAndPasswordlessCannotLogin(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 1000, "TWD", 10)
	e.checkoutAsGuest(t, e.shopADomain, "locked@t.dev", skuID, 1)

	// A passwordless identity cannot log in, whatever password is offered.
	code, _ := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/auth/login",
		map[string]any{"email": "locked@t.dev", "password": "anything-at-all"})
	if code != http.StatusUnauthorized {
		t.Fatalf("passwordless identity must not log in, got %d", code)
	}

	// Claim it, then try to re-claim with a different password.
	if code, body := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/auth/register",
		map[string]any{"email": "locked@t.dev", "password": "the-real-password"}); code != http.StatusOK {
		t.Fatalf("claim: want 200, got %d: %+v", code, body)
	}
	hashAfterClaim := *e.identity(t, e.shopA, "locked@t.dev").PasswordHash

	if code, _ := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/auth/register",
		map[string]any{"email": "locked@t.dev", "password": "attacker-password"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("re-registering a password holder must be refused, got %d", code)
	}
	if got := *e.identity(t, e.shopA, "locked@t.dev").PasswordHash; got != hashAfterClaim {
		t.Fatal("a set password must never be overwritten by registration")
	}

	// The real password still works.
	if code, _ := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/auth/login",
		map[string]any{"email": "locked@t.dev", "password": "the-real-password"}); code != http.StatusOK {
		t.Fatalf("the claimed password must authenticate, got %d", code)
	}
	_ = ctx
}

// ── Scenario: 停用顧客無法以訪客結帳下單 ────────────────────────────────

func TestGuestCheckoutRefusedForDisabledIdentity(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 1000, "TWD", 10)
	e.client.Member.Create().SetShopID(e.shopA).SetEmail("banned@t.dev").SetStatus(0).SaveX(ctx)

	code, body := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		guestBody("banned@t.dev", guestItem(skuID, 1)))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %+v", code, body)
	}
	if errObj, ok := body["error"].(map[string]any); !ok || errObj["code"] != "checkout_unavailable" {
		t.Fatalf("want the checkout_unavailable code, got %+v", body)
	}
	if n := e.client.Order.Query().CountX(ctx); n != 0 {
		t.Fatalf("no order may be created for a disabled identity, got %d", n)
	}
	if got := e.client.ProductSKU.GetX(ctx, skuID).StockQty; got != 10 {
		t.Fatalf("stock must be untouched, got %d", got)
	}
}

// ── Scenario: 僅提供必填欄位的單欄地址結帳成功（design D8） ──────────────

func TestSingleLineShippingAddressAcceptedOnBothCheckoutPaths(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	guestSKU := e.newSKU(t, e.shopA, 1000, "TWD", 10)
	memberSKU := e.newSKU(t, e.shopA, 1000, "TWD", 10)

	// Guest path: guestBody already carries only the three required fields.
	orderID, _ := e.checkoutAsGuest(t, e.shopADomain, "single-line@t.dev", guestSKU, 1)
	var addr map[string]any
	if err := json.Unmarshal(e.client.Order.GetX(ctx, orderID).ShippingAddress, &addr); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"city", "postal_code", "country"} {
		v, present := addr[k]
		if !present {
			t.Fatalf("optional key %q must still be serialized (response shape is unchanged), got %+v", k, addr)
		}
		if v != "" {
			t.Fatalf("optional key %q must be stored empty, not guessed, got %q", k, v)
		}
	}

	// Member path: the same relaxation applies (design D8 rejects a
	// guest-only rule for the same column).
	if _, err := e.cartSvc.AddItem(ctx, e.shopA, e.memberA1, memberSKU, 1, ""); err != nil {
		t.Fatal(err)
	}
	code, body := e.memberTokenCall(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout",
		map[string]any{"recipient_name": "王小明", "phone": "0912345678", "line1": "台北市信義區信義路五段7號"})
	if code != http.StatusCreated {
		t.Fatalf("member checkout with a single-line address: want 201, got %d: %+v", code, body)
	}

	// ...but the required three are still required.
	if code, _ := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout", map[string]any{
		"email":            "no-line1@t.dev",
		"items":            []map[string]any{guestItem(guestSKU, 1)},
		"shipping_address": map[string]any{"recipient_name": "王小明", "phone": "0912345678"},
	}); code != http.StatusUnprocessableEntity {
		t.Fatalf("a missing line1 must still be 422, got %d", code)
	}
}
