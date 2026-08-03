package ecpay_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"ksdevworks/ecommerce/api/internal/cryptbox"
	"ksdevworks/ecommerce/api/internal/payment"
	"ksdevworks/ecommerce/api/internal/payment/ecpay"
	"ksdevworks/ecommerce/api/internal/testutil"
)

const resolverTestEncryptionKeyHex = "075d367f7e5f40a3856e621a743c81e156e10ac4f07259b4d9c8d0f942acbee0"

func newTestBox(t *testing.T) *cryptbox.Box {
	t.Helper()
	box, err := cryptbox.ParseKey(resolverTestEncryptionKeyHex)
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	return box
}

// Scenario: 商家設定憑證後即可用該憑證發起付款 (spec
// shop-payment-credentials: ResolveForShop resolves the shop's own row).
func TestShopResolverResolvesConfiguredShop(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()
	box := newTestBox(t)

	shop := client.Shop.Create().SetName("resolver-shop-a").SetStatus(1).SaveX(ctx)

	hashKeyCT, err := box.Encrypt("hash-key-a")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	hashIVCT, err := box.Encrypt("hash-iv-a")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	client.ShopPaymentProviderCredential.Create().
		SetShopID(shop.ID).SetProvider(ecpay.ProviderName).
		SetMerchantID("merchant-a").
		SetHashKeyCiphertext(hashKeyCT).SetHashIvCiphertext(hashIVCT).
		SaveX(ctx)

	resolver := &ecpay.ShopResolver{
		Client: client, Box: box,
		CheckoutURL:   "http://localhost:8090/Cashier/AioCheckOut/V5",
		ReturnURLBase: "http://localhost:8080/api/v1/webhooks/payments/ecpay",
	}
	prov, err := resolver.ResolveForShop(ctx, shop.ID)
	if err != nil {
		t.Fatalf("ResolveForShop: %v", err)
	}

	res, err := prov.InitiatePayment(ctx, payment.InitiateRequest{
		ShopID: shop.ID, OrderID: 1, Amount: 100, Currency: "TWD",
	})
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}
	if got := res.RedirectForm.Fields["MerchantID"]; got != "merchant-a" {
		t.Errorf("MerchantID = %q, want merchant-a", got)
	}
	wantReturnURL := fmt.Sprintf("http://localhost:8080/api/v1/webhooks/payments/ecpay/%d", shop.ID)
	if got := res.RedirectForm.Fields["ReturnURL"]; got != wantReturnURL {
		t.Errorf("ReturnURL = %q, want %q (design D2: shop id travels in a URL we control)", got, wantReturnURL)
	}
}

// Scenario: 未設定憑證的商家無法以 ecpay 發起付款 (spec
// ecpay-payment-provider: ECPay credentials are resolved per shop).
func TestShopResolverReturnsErrProviderNotConfigured(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()
	box := newTestBox(t)

	shop := client.Shop.Create().SetName("resolver-shop-unconfigured").SetStatus(1).SaveX(ctx)

	resolver := &ecpay.ShopResolver{Client: client, Box: box}
	_, err := resolver.ResolveForShop(ctx, shop.ID)
	if !errors.Is(err, payment.ErrProviderNotConfigured) {
		t.Fatalf("ResolveForShop() error = %v, want payment.ErrProviderNotConfigured", err)
	}
}

// Scenario: 商家各自的憑證各自生效 (spec ecpay-payment-provider): two shops'
// resolved providers never cross-contaminate — same resolver instance, two
// different shop ids, two different MerchantIDs.
func TestShopResolverIsolatesShops(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()
	box := newTestBox(t)

	shopA := client.Shop.Create().SetName("resolver-shop-b1").SetStatus(1).SaveX(ctx)
	shopB := client.Shop.Create().SetName("resolver-shop-b2").SetStatus(1).SaveX(ctx)

	for shopID, merchantID := range map[int]string{shopA.ID: "merchant-b1", shopB.ID: "merchant-b2"} {
		hashKeyCT, _ := box.Encrypt("hash-key-" + merchantID)
		hashIVCT, _ := box.Encrypt("hash-iv-" + merchantID)
		client.ShopPaymentProviderCredential.Create().
			SetShopID(shopID).SetProvider(ecpay.ProviderName).SetMerchantID(merchantID).
			SetHashKeyCiphertext(hashKeyCT).SetHashIvCiphertext(hashIVCT).
			SaveX(ctx)
	}

	resolver := &ecpay.ShopResolver{Client: client, Box: box, ReturnURLBase: "http://localhost:8080/api/v1/webhooks/payments/ecpay"}

	provA, err := resolver.ResolveForShop(ctx, shopA.ID)
	if err != nil {
		t.Fatalf("resolve shop A: %v", err)
	}
	resA, err := provA.InitiatePayment(ctx, payment.InitiateRequest{ShopID: shopA.ID, OrderID: 1, Amount: 100, Currency: "TWD"})
	if err != nil {
		t.Fatalf("initiate shop A: %v", err)
	}

	provB, err := resolver.ResolveForShop(ctx, shopB.ID)
	if err != nil {
		t.Fatalf("resolve shop B: %v", err)
	}
	resB, err := provB.InitiatePayment(ctx, payment.InitiateRequest{ShopID: shopB.ID, OrderID: 2, Amount: 100, Currency: "TWD"})
	if err != nil {
		t.Fatalf("initiate shop B: %v", err)
	}

	if resA.RedirectForm.Fields["MerchantID"] != "merchant-b1" {
		t.Errorf("shop A MerchantID = %q, want merchant-b1", resA.RedirectForm.Fields["MerchantID"])
	}
	if resB.RedirectForm.Fields["MerchantID"] != "merchant-b2" {
		t.Errorf("shop B MerchantID = %q, want merchant-b2", resB.RedirectForm.Fields["MerchantID"])
	}

	// The two CheckMacValue-signed field sets must not be interchangeable:
	// shop A's signature was computed with shop A's own HashKey/HashIV.
	if resA.RedirectForm.Fields["CheckMacValue"] == resB.RedirectForm.Fields["CheckMacValue"] {
		t.Error("shop A and shop B produced the same CheckMacValue — their credentials are not actually isolated")
	}
}
