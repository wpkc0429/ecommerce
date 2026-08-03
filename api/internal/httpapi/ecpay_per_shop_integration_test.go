package httpapi_test

// change ecpay-per-shop-credentials — the new webhook route
// (POST /api/v1/webhooks/payments/ecpay/{shopID}) proven end to end against a
// real ecpay.ShopResolver: two shops, two independently-configured
// credentials, and the wrong-shop-id case that design D2's safety argument
// rests on (using the wrong URL just fails signature verification, it never
// lets anything through).
//
// Deliberately a separate, minimal fixture rather than paymentEnv
// (payment_integration_test.go): that fixture's "ecpay" registry entry is a
// FIXED, non-shop-scoped *ecpay.Provider on purpose, so every pre-existing
// ecpay_integration_test.go test keeps exercising the platform-wide-credential
// path it was written for. This file is the only place a ShopResolver is
// wired into a live router.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/cryptbox"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/payment"
	"ksdevworks/ecommerce/api/internal/payment/ecpay"
	"ksdevworks/ecommerce/api/internal/testutil"
)

type perShopECPayEnv struct {
	client *ent.Client
	router http.Handler
	orders *order.Service
}

func newPerShopECPayEnv(t *testing.T) *perShopECPayEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	log := slog.New(slog.NewTextHandler(&strings.Builder{}, nil))

	box, err := cryptbox.ParseKey("c581a49c93f388267ccc5320b292e808f0dc325fc881fe8f2bbcf0a8cd0174d3")
	if err != nil {
		t.Fatal(err)
	}
	orderSvc := &order.Service{Client: client}
	paymentSvc := &payment.Service{
		Client: client, Orders: orderSvc, Log: log,
		Providers: map[string]any{
			"ecpay": &ecpay.ShopResolver{
				Client: client, Box: box,
				CheckoutURL:   "http://localhost:8090/Cashier/AioCheckOut/V5",
				ReturnURLBase: "http://localhost:8080/api/v1/webhooks/payments/ecpay",
			},
		},
		DefaultProvider: "ecpay",
	}
	return &perShopECPayEnv{
		client: client, orders: orderSvc,
		router: httpapi.New(httpapi.Deps{
			Log:      log,
			Payments: &httpapi.PaymentHandler{Client: client, Service: paymentSvc, Log: log},
		}),
	}
}

func (e *perShopECPayEnv) newShopWithCredentials(t *testing.T, merchantID, hashKey, hashIV string) *ent.Shop {
	t.Helper()
	ctx := context.Background()
	shop := e.client.Shop.Create().SetName("per-shop-ecpay-" + merchantID).SetStatus(1).SaveX(ctx)
	box, err := cryptbox.ParseKey("c581a49c93f388267ccc5320b292e808f0dc325fc881fe8f2bbcf0a8cd0174d3")
	if err != nil {
		t.Fatal(err)
	}
	hashKeyCT, err := box.Encrypt(hashKey)
	if err != nil {
		t.Fatal(err)
	}
	hashIVCT, err := box.Encrypt(hashIV)
	if err != nil {
		t.Fatal(err)
	}
	e.client.ShopPaymentProviderCredential.Create().
		SetShopID(shop.ID).SetProvider("ecpay").SetMerchantID(merchantID).
		SetHashKeyCiphertext(hashKeyCT).SetHashIvCiphertext(hashIVCT).
		SaveX(ctx)
	return shop
}

func (e *perShopECPayEnv) newOrder(t *testing.T, shopID int, amount int64) *ent.Order {
	t.Helper()
	ctx := context.Background()
	member := e.client.Member.Create().SetShopID(shopID).SetEmail("per-shop-ecpay@t.dev").SetStatus(1).SaveX(ctx)
	return e.client.Order.Create().
		SetShopID(shopID).SetMemberID(member.ID).
		SetStatus(order.StatusCreated).SetPaymentStatus(order.PaymentStatusUnpaid).SetFulfillmentStatus(0).
		SetCurrency("TWD").SetSubtotalAmount(amount).SetTotalAmount(amount).
		SetShippingAddress(json.RawMessage(`{}`)).
		SaveX(ctx)
}

// TestECPayPerShopWebhookRoundTrip is the route this whole change exists for:
// initiate with shop A's own credentials, deliver the callback to
// /webhooks/payments/ecpay/{shopA}, and see the order marked paid.
func TestECPayPerShopWebhookRoundTrip(t *testing.T) {
	e := newPerShopECPayEnv(t)
	ctx := context.Background()
	shopA := e.newShopWithCredentials(t, "merchant-a", "hash-key-a", "hash-iv-a")
	ord := e.newOrder(t, shopA.ID, 1500)

	// Initiate via the Service directly (this test is about the webhook
	// route, not the member-facing initiate HTTP path already covered
	// elsewhere) using the same resolver the router itself uses.
	resolver := &ecpay.ShopResolver{
		Client:        e.client,
		Box:           mustBox(t),
		CheckoutURL:   "http://localhost:8090/Cashier/AioCheckOut/V5",
		ReturnURLBase: "http://localhost:8080/api/v1/webhooks/payments/ecpay",
	}
	prov, err := resolver.ResolveForShop(ctx, shopA.ID)
	if err != nil {
		t.Fatalf("ResolveForShop: %v", err)
	}
	res, err := prov.InitiatePayment(ctx, payment.InitiateRequest{
		ShopID: shopA.ID, OrderID: ord.ID, Amount: ord.TotalAmount, Currency: ord.Currency,
	})
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}
	e.client.Payment.Create().
		SetShopID(shopA.ID).SetOrderID(ord.ID).
		SetProvider("ecpay").SetProviderReference(res.ProviderReference).
		SetAmount(ord.TotalAmount).SetCurrency(ord.Currency).
		SetStatus(payment.StatusPending).
		SaveX(ctx)

	callbackFields := map[string]string{
		"MerchantID": "merchant-a", "MerchantTradeNo": res.ProviderReference,
		"RtnCode": "1", "RtnMsg": "交易成功", "TradeNo": "2412311225437371",
		"TradeAmt": "1500", "PaymentDate": time.Now().Format("2006/01/02 15:04:05"),
		"PaymentType": "Credit_CreditCard", "SimulatePaid": "0",
	}
	body := ecpay.SignCallback("hash-key-a", "hash-iv-a", callbackFields)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/webhooks/payments/ecpay/"+strconv.Itoa(shopA.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	if got := strings.TrimSpace(rec.Body.String()); got != "1|OK" {
		t.Fatalf("webhook response = %q, want 1|OK (code %d)", got, rec.Code)
	}

	updated, err := e.client.Order.Get(ctx, ord.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.PaymentStatus != payment.OrderPaymentStatusPaid {
		t.Errorf("order.payment_status = %d, want paid", updated.PaymentStatus)
	}
}

// TestECPayPerShopWebhookWrongShopIDFailsSignature is design D2's safety
// argument, checked at the HTTP layer: a genuine shop-A-signed callback
// delivered to shop B's URL segment must fail verification outright — shop
// B's own (different) HashKey/HashIV cannot validate a MAC computed with
// shop A's.
func TestECPayPerShopWebhookWrongShopIDFailsSignature(t *testing.T) {
	e := newPerShopECPayEnv(t)
	shopA := e.newShopWithCredentials(t, "merchant-a", "hash-key-a", "hash-iv-a")
	shopB := e.newShopWithCredentials(t, "merchant-b", "hash-key-b", "hash-iv-b")
	ord := e.newOrder(t, shopA.ID, 1500)

	callbackFields := map[string]string{
		"MerchantID": "merchant-a", "MerchantTradeNo": "whatever-shop-a-reference",
		"RtnCode": "1", "RtnMsg": "交易成功", "TradeNo": "2412311225437371",
		"TradeAmt": "1500", "PaymentDate": time.Now().Format("2006/01/02 15:04:05"),
		"PaymentType": "Credit_CreditCard", "SimulatePaid": "0",
	}
	// Signed with shop A's own key.
	body := ecpay.SignCallback("hash-key-a", "hash-iv-a", callbackFields)

	// Delivered to shop B's URL.
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/webhooks/payments/ecpay/"+strconv.Itoa(shopB.ID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	if got := strings.TrimSpace(rec.Body.String()); got == "1|OK" {
		t.Fatalf("shop A's signed callback validated against shop B's credentials — the per-shop isolation is broken")
	}

	updated, err := e.client.Order.Get(context.Background(), ord.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.PaymentStatus == payment.OrderPaymentStatusPaid {
		t.Error("order must not be marked paid by a callback verified against the wrong shop's credentials")
	}
}

func mustBox(t *testing.T) *cryptbox.Box {
	t.Helper()
	box, err := cryptbox.ParseKey("c581a49c93f388267ccc5320b292e808f0dc325fc881fe8f2bbcf0a8cd0174d3")
	if err != nil {
		t.Fatal(err)
	}
	return box
}
