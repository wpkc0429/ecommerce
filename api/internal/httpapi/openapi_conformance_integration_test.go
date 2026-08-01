package httpapi

// 契約防腐機制 3（change openapi-contract, design D2/D10）：**對真實 router 發真實請求，
// 用契約自己宣告的 schema 驗證回應主體。**
//
// 這是四道檢查裡唯一能抓到「形狀漂移」的一道——路由還在、名字沒變，但回應多了一個鍵、
// 少了一個鍵，或某個整數變成了字串。它的力量來自回應 schema 上的 `additionalProperties: false`
// （design D14）：拿掉它，新增回應鍵的漂移就會完全靜默。
//
// ── 取樣原則（design D10），刻意不做全部 111 條 ─────────────────────────
//
// 全覆蓋需要為每個後台端點建出整套前置資料（角色、權限、主題、頁型…），寫出來的會是一組
// 脆弱、且與既有整合測試高度重複的測試。取而代之的三條規則：
//
//  1. **自帶前端（ONDO）會走的每一條路徑都必須有** —— 列商品 → 取運送方式 → 訪客結帳 →
//     付款 → 回呼 → 查單 → 查出貨。這是本檢查的第一責任。
//  2. **每一個共用 schema 至少被一條被驗證過的回應涵蓋一次** —— 共用 schema 是 111 條路由的
//     實際交集（`Order` 就被 6 條路由回傳），驗它們的槓桿遠高於逐條驗端點。
//     結尾的 requiredSchemas 斷言守著這一點，取樣清單日後被砍短會轉紅。
//  3. **錯誤 envelope 的 401／404／422 各至少一次**，422 連 `details[].pointer` 一起驗。
//
// **已知缺口，不假裝覆蓋：** 未取樣到的後台端點若改了回應形狀，本檢查不會發現
// （檢查 1 仍守著它的路徑存在、檢查 4 仍守著它的公開鍵不消失）。
//
// **requiredSchemas 的精確語意：** 它斷言的是「這些 schema 出現在某條**被實際驗證過的**
// 回應的 schema 圖中」，不是「它的每一個分支都被實際資料走過」——例如 `MemberTier` 可能只以
// `level: null` 的形式出現。這個區別寫在這裡，免得日後有人把它當成更強的保證。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cart"
	"ksdevworks/ecommerce/api/internal/catalog"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/guest"
	"ksdevworks/ecommerce/api/internal/idempotency"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/payment"
	"ksdevworks/ecommerce/api/internal/payment/ecpay"
	"ksdevworks/ecommerce/api/internal/points"
	"ksdevworks/ecommerce/api/internal/shipping"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// requiredSchemas is rule 2 above, written down. Removing an endpoint from the
// sample list without replacing its coverage turns this test red.
var requiredSchemas = []string{
	"Cart", "CartItem",
	"Error", "ValidationDetail",
	"GuestCheckoutResult",
	"MemberPoints", "MemberTier",
	"MinorUnitAmount",
	"Order", "OrderItem", "OrderPage", "ShippingAddress",
	"PaymentInitiateResult", "PaymentRedirectForm",
	"PointTransaction", "PointTransactionPage",
	"Product", "ProductPage", "ProductSKU",
	"PublicShippingMethod",
	"Shipment",
	"TokenResponse",
}

// ── 契約驅動的回應驗證 ────────────────────────────────────────────────

type contractValidator struct {
	doc      map[string]any
	compiler *jsonschema.Compiler
	covered  map[string]bool // component schema names reached by a validated response
}

func newContractValidator(t *testing.T) *contractValidator {
	t.Helper()
	doc := loadOpenAPI(t)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("re-encode contract: %v", err)
	}
	normalized, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("normalize contract: %v", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("openapi.json", normalized); err != nil {
		t.Fatalf("add contract as schema resource: %v", err)
	}
	return &contractValidator{doc: doc, compiler: c, covered: map[string]bool{}}
}

// lookupPointer resolves a JSON pointer (without the leading '#') inside doc.
func lookupPointer(doc map[string]any, pointer string) any {
	var node any = doc
	for _, seg := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		if seg == "" {
			continue
		}
		m, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node, ok = m[strings.NewReplacer("~1", "/", "~0", "~").Replace(seg)]
		if !ok {
			return nil
		}
	}
	return node
}

// schemaPointer walks segments from the document root, transparently following
// any internal $ref it meets, and returns the compilable URI of the node it
// lands on. This is what lets the test address a response schema the way the
// contract actually writes it — `responses.404` is a $ref to a shared response
// component, whose content is in turn a $ref to the shared Error schema.
func (v *contractValidator) schemaPointer(segs ...string) (string, error) {
	base := ""
	var node any = v.doc
	for _, seg := range segs {
		for {
			m, ok := node.(map[string]any)
			if !ok {
				break
			}
			ref, ok := m["$ref"].(string)
			if !ok || !strings.HasPrefix(ref, "#/") {
				break
			}
			base = strings.TrimPrefix(ref, "#")
			node = lookupPointer(v.doc, base)
			if node == nil {
				return "", fmt.Errorf("contract has a dangling $ref %q", ref)
			}
		}
		m, ok := node.(map[string]any)
		if !ok {
			return "", fmt.Errorf("contract node %q is not an object (looking for %q)", base, seg)
		}
		next, ok := m[seg]
		if !ok {
			return "", fmt.Errorf("contract has no %q under %q", seg, base)
		}
		base += "/" + escapePointer(seg)
		node = next
	}
	return "openapi.json#" + base, nil
}

// markCovered records every component schema reachable from pointer.
func (v *contractValidator) markCovered(pointer string) {
	start := lookupPointer(v.doc, strings.TrimPrefix(pointer, "openapi.json#"))
	seen := map[string]bool{}
	var walk func(any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			if ref, ok := n["$ref"].(string); ok {
				if name, found := strings.CutPrefix(ref, "#/components/schemas/"); found {
					if !seen[name] {
						seen[name] = true
						v.covered[name] = true
						walk(lookupPointer(v.doc, "/components/schemas/"+escapePointer(name)))
					}
					return
				}
				walk(lookupPointer(v.doc, strings.TrimPrefix(ref, "#")))
				return
			}
			for _, sub := range n {
				walk(sub)
			}
		case []any:
			for _, sub := range n {
				walk(sub)
			}
		}
	}
	walk(start)
}

// assertResponse validates a real response body against the schema the contract
// declares for exactly this (path template, method, status).
func (v *contractValidator) assertResponse(t *testing.T, pathTemplate, method string, status int, body []byte) {
	t.Helper()
	pointer, err := v.schemaPointer("paths", pathTemplate, strings.ToLower(method),
		"responses", fmt.Sprintf("%d", status), "content", "application/json", "schema")
	if err != nil {
		t.Fatalf("%s %s -> %d: %v", method, pathTemplate, status, err)
	}
	sch, err := v.compiler.Compile(pointer)
	if err != nil {
		t.Fatalf("%s %s -> %d: compile %s: %v", method, pathTemplate, status, pointer, err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("%s %s -> %d: response is not JSON: %v", method, pathTemplate, status, err)
	}
	if err := sch.Validate(inst); err != nil {
		t.Errorf("%s %s -> %d 的回應不符合契約（api/openapi/openapi.yaml）：\n%v\n實際回應：%s",
			method, pathTemplate, status, err, truncate(body))
		return
	}
	v.markCovered(pointer)
}

func truncate(b []byte) string {
	const max = 1200
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + " …(truncated)"
}

// ── 測試環境 ──────────────────────────────────────────────────────────

type conformanceEnv struct {
	client   *ent.Client
	router   http.Handler
	shopID   int
	domain   string
	pubKey   string
	skuID    int
	slug     string
	shipID   int
	memberID int

	shippingSvc   *shipping.Service
	idemSvc       *idempotency.Service
	webhookSecret string
}

func newConformanceEnv(t *testing.T) *conformanceEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()

	e := &conformanceEnv{
		client: client, domain: "oa-conformance.test",
		pubKey: "pk_oaconformancekey", slug: "conformance-bouquet",
		webhookSecret: "oa-conformance-secret",
	}
	e.shopID = client.Shop.Create().SetName("Conformance Shop").SetStatus(1).SaveX(ctx).ID
	site := client.Site.Create().SetDomain(e.domain).SaveX(ctx)
	client.SiteShop.Create().SetSiteID(site.ID).SetShopID(e.shopID).SetIsPrimary(true).SaveX(ctx)
	client.PublishableKey.Create().SetShopID(e.shopID).SetKey(e.pubKey).SetLabel("conformance").SaveX(ctx)

	// A published product with a florist-shaped price: 3400 minor units is
	// NT$3,400, not NT$34 — the exact confusion the contract's MinorUnitAmount
	// exists to prevent (design D9).
	product := client.Product.Create().
		SetShopID(e.shopID).SetTitle("溫室玫瑰花束").SetSlug(e.slug).
		SetDescription("conformance fixture").SetStatus(1).
		SaveX(ctx)
	e.skuID = client.ProductSKU.Create().
		SetShopID(e.shopID).SetProductID(product.ID).SetSkuCode("CONF-M").
		SetPriceAmount(3400).SetCurrency("TWD").SetStockQty(50).SetIsActive(true).
		SaveX(ctx).ID

	e.shipID = client.ShippingMethod.Create().
		SetShopID(e.shopID).SetName("台北市當日配送").SetCarrier("自有車隊").
		SetFlatRate(200).SetIsActive(true).SaveX(ctx).ID
	client.ShippingMethod.Create().
		SetShopID(e.shopID).SetName("已停用").SetCarrier("無").
		SetFlatRate(999).SetIsActive(false).SaveX(ctx)

	hash, err := auth.HashPassword("conformance-pass-1")
	if err != nil {
		t.Fatal(err)
	}
	e.memberID = client.Member.Create().
		SetShopID(e.shopID).SetEmail("conformance-member@t.dev").SetPasswordHash(hash).SetStatus(1).
		SaveX(ctx).ID

	issuer, err := auth.NewTokenIssuer("conf-admin-secret", "conf-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolver := tenant.NewResolver(client, rdb, log)
	refresh := auth.NewRefreshService(client, 30*24*time.Hour)

	dispatcher := events.NewDispatcher(log)
	pointsSvc := &points.Service{Client: client, Log: log}
	dispatcher.Subscribe(pointsSvc.Handle) // mirrors internal/app/wire.go

	cartSvc := &cart.Service{Client: client}
	orderSvc := &order.Service{Client: client}
	mock := payment.NewMockProvider(e.webhookSecret)
	// change ecpay-payment-provider: ONDO's real provider, registered exactly as
	// wire.go does. Sampling rule 1 (ONDO's path is covered 100%) is why the
	// form-POST response shape is asserted against the contract below.
	ecpayProv := ecpay.New(ecpay.Config{
		MerchantID: "3002607", HashKey: "pwFHCqoQZGmho4w6", HashIV: "EkRm7iFT261dpevs",
		CheckoutURL: "http://localhost:8090/Cashier/AioCheckOut/V5",
		ReturnURL:   "http://localhost:8080/api/v1/webhooks/payments/ecpay",
	}, nil)
	paymentSvc := &payment.Service{
		Client: client, Orders: orderSvc, Dispatcher: dispatcher, Log: log,
		Providers: map[string]payment.Provider{
			mock.Name(): mock, ecpayProv.Name(): ecpayProv,
		},
		DefaultProvider: mock.Name(),
	}
	e.shippingSvc = &shipping.Service{Client: client, Orders: orderSvc, Dispatcher: dispatcher}
	guestSvc := &guest.Service{Client: client, Cart: cartSvc, Orders: orderSvc}
	e.idemSvc = &idempotency.Service{Client: client, TTL: time.Hour}
	cfg := &config.Config{OrderAccessTokenTTL: time.Hour}

	e.router = New(Deps{
		Cfg: cfg, Log: log,
		TenantMW:      NewTenantMiddleware(resolver),
		MemberMW:      NewMemberMiddleware(issuer),
		OrderAccessMW: NewOrderAccessMiddleware(issuer),
		MemberAuth:    &MemberAuthHandler{Client: client, Issuer: issuer, Refresh: refresh, Log: log},
		Products:      &ProductsHandler{Client: client, Service: &catalog.Service{Client: client}, Log: log},
		Cart:          &CartHandler{Client: client, Service: cartSvc, Log: log},
		Orders:        &OrderHandler{Client: client, Service: orderSvc, Log: log},
		Payments:      &PaymentHandler{Client: client, Service: paymentSvc, Log: log},
		Shipping:      &ShippingHandler{Client: client, Service: e.shippingSvc, Log: log},
		Points:        &PointsHandler{Client: client, Service: pointsSvc, Log: log},
		Guest: &GuestHandler{
			Client: client, Service: guestSvc, Orders: orderSvc,
			Payments: paymentSvc, Shipping: e.shippingSvc,
			Issuer: issuer, Cfg: cfg, Log: log,
			Idempotency: e.idemSvc,
		},
	})
	return e
}

// do issues a storefront request with the publishable key set, mirroring how a
// self-hosted front end actually calls this API.
func (e *conformanceEnv) do(t *testing.T, method, path, bearer string, body any) (int, []byte) {
	t.Helper()
	code, raw, _ := e.doWithKey(t, method, path, bearer, "", body)
	return code, raw
}

// doWithKey is do plus an optional Idempotency-Key, returning headers too
// (change checkout-idempotency: the replay marker is a header, not a body key).
func (e *conformanceEnv) doWithKey(t *testing.T, method, path, bearer, idemKey string, body any) (int, []byte, http.Header) {
	t.Helper()
	var reader io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Host = e.domain
	req.Header.Set(PublishableKeyHeader, e.pubKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if idemKey != "" {
		req.Header.Set(IdempotencyKeyHeader, idemKey)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), rec.Result().Header
}

func decodeInto(t *testing.T, body []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(body, dst); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, truncate(body))
	}
}

// ── 主測試 ────────────────────────────────────────────────────────────

func TestOpenAPIResponsesMatchImplementation(t *testing.T) {
	testutil.RequireIntegration(t)
	e := newConformanceEnv(t)
	v := newContractValidator(t)

	// ── ONDO 路徑 ①：列商品（無認證） ──────────────────────────────────
	code, body := e.do(t, http.MethodGet, "/api/v1/shop/products", "", nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/shop/products", http.MethodGet, http.StatusOK, body)

	// ── ONDO 路徑 ②：單一商品 ──────────────────────────────────────────
	code, body = e.do(t, http.MethodGet, "/api/v1/shop/products/"+e.slug, "", nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/shop/products/{slug}", http.MethodGet, http.StatusOK, body)

	// ── ONDO 路徑 ③：運送方式（只回啟用中的） ─────────────────────────
	code, body = e.do(t, http.MethodGet, "/api/v1/shop/shipping-methods", "", nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/shop/shipping-methods", http.MethodGet, http.StatusOK, body)

	// ── ONDO 路徑 ④：訪客結帳 ─────────────────────────────────────────
	checkout := map[string]any{
		"email": "conformance-guest@t.dev",
		"items": []map[string]any{
			{"sku_id": e.skuID, "quantity": 1, "gift_message": "給媽媽"},
			{"sku_id": e.skuID, "quantity": 1, "gift_message": "給岳母"},
		},
		"shipping_address": map[string]any{
			"recipient_name": "王小明", "phone": "0912345678",
			"line1": "台北市大安區復興南路一段 100 號 5 樓",
		},
		"shipping_method_id":      e.shipID,
		"requested_delivery_date": time.Now().UTC().AddDate(0, 0, 7).Format("2006-01-02"),
	}
	code, body = e.do(t, http.MethodPost, "/api/v1/shop/guest/checkout", "", checkout)
	requireStatus(t, code, http.StatusCreated, body)
	v.assertResponse(t, "/api/v1/shop/guest/checkout", http.MethodPost, http.StatusCreated, body)

	var guestResult struct {
		Order struct {
			ID              int     `json:"id"`
			TotalAmount     int64   `json:"total_amount"`
			SubtotalAmount  int64   `json:"subtotal_amount"`
			DiscountAmount  int64   `json:"discount_amount"`
			ShippingFee     int64   `json:"shipping_fee"`
			DiscountPercent *int16  `json:"discount_percent"`
			MemberTierName  *string `json:"member_tier_name"`
		} `json:"order"`
		Token string `json:"order_access_token"`
	}
	decodeInto(t, body, &guestResult)

	// The contract's central money claim, asserted on real data rather than
	// merely described: total is the payable amount, in minor units.
	if got, want := guestResult.Order.SubtotalAmount, int64(6800); got != want {
		t.Errorf("subtotal_amount = %d, want %d (2 × 3400 最小貨幣單位)", got, want)
	}
	if got, want := guestResult.Order.ShippingFee, int64(200); got != want {
		t.Errorf("shipping_fee = %d, want %d", got, want)
	}
	// A brand-new guest email holds no tier, so the discount trio is the
	// "nothing applied" shape (change tier-discount-application).
	if guestResult.Order.DiscountAmount != 0 ||
		guestResult.Order.DiscountPercent != nil || guestResult.Order.MemberTierName != nil {
		t.Errorf("a tierless guest must get the null discount snapshot, got %d/%v/%v",
			guestResult.Order.DiscountAmount, guestResult.Order.DiscountPercent, guestResult.Order.MemberTierName)
	}
	if guestResult.Order.TotalAmount !=
		guestResult.Order.SubtotalAmount-guestResult.Order.DiscountAmount+guestResult.Order.ShippingFee {
		t.Errorf("total_amount %d != subtotal %d − discount %d + fee %d — 契約宣告這是恆等式",
			guestResult.Order.TotalAmount, guestResult.Order.SubtotalAmount,
			guestResult.Order.DiscountAmount, guestResult.Order.ShippingFee)
	}
	if guestResult.Token == "" {
		t.Fatal("guest checkout returned no order_access_token")
	}

	// ── ONDO 路徑 ⑤：以訂單存取權杖查訂單 ─────────────────────────────
	code, body = e.do(t, http.MethodGet, "/api/v1/shop/guest/order", guestResult.Token, nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/shop/guest/order", http.MethodGet, http.StatusOK, body)

	// ── ONDO 路徑 ⑥a：以綠界發起付款（表單導向的回應形狀）─────────────
	//
	// 這一條先跑，理由是它必須跑在訂單被標記已付款之前（已付款的訂單不可再發起
	// 付款）。同一張訂單允許多筆付款嘗試，這是 payment-integration 明訂的。
	code, body = e.do(t, http.MethodPost, "/api/v1/shop/guest/order/payments", guestResult.Token,
		map[string]string{"provider": "ecpay"})
	requireStatus(t, code, http.StatusCreated, body)
	v.assertResponse(t, "/api/v1/shop/guest/order/payments", http.MethodPost, http.StatusCreated, body)

	var ecpayInitiated struct {
		RedirectURL  string `json:"redirect_url"`
		RedirectForm *struct {
			Method string            `json:"method"`
			Action string            `json:"action"`
			Fields map[string]string `json:"fields"`
		} `json:"redirect_form"`
	}
	decodeInto(t, body, &ecpayInitiated)
	if ecpayInitiated.RedirectForm == nil {
		t.Fatal("綠界需要表單 POST，redirect_form 不得為 null")
	}
	if ecpayInitiated.RedirectURL != "" {
		t.Errorf("表單導向的 provider 其 redirect_url 必須為空字串，得到 %q", ecpayInitiated.RedirectURL)
	}
	// 金額欄位的單位在契約裡無法以 MinorUnitAmount 表達（表單值都是字串），
	// 所以這一行是它在契約層唯一的守衛。
	if got := ecpayInitiated.RedirectForm.Fields["TotalAmount"]; got != strconv.FormatInt(guestResult.Order.TotalAmount, 10) {
		t.Errorf("TotalAmount = %q，必須逐位元組等於訂單應付總額 %d（無任何換算）",
			got, guestResult.Order.TotalAmount)
	}

	// ── ONDO 路徑 ⑥：發起付款 ─────────────────────────────────────────
	code, body = e.do(t, http.MethodPost, "/api/v1/shop/guest/order/payments", guestResult.Token, nil)
	requireStatus(t, code, http.StatusCreated, body)
	v.assertResponse(t, "/api/v1/shop/guest/order/payments", http.MethodPost, http.StatusCreated, body)

	var initiated struct {
		ProviderReference string `json:"provider_reference"`
		Amount            int64  `json:"amount"`
	}
	decodeInto(t, body, &initiated)
	if initiated.Amount != guestResult.Order.TotalAmount {
		t.Errorf("請款金額 %d != 訂單應付總額 %d", initiated.Amount, guestResult.Order.TotalAmount)
	}

	// ── ONDO 路徑 ⑦：金流回呼（簽章認證，不帶 JWT、不帶租戶） ─────────
	hookBody, err := json.Marshal(map[string]string{
		"provider_reference": initiated.ProviderReference, "status": "succeeded",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/payments/mock", bytes.NewReader(hookBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Payment-Signature", payment.SignMockWebhook(e.webhookSecret, hookBody))
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	requireStatus(t, rec.Code, http.StatusOK, rec.Body.Bytes())
	v.assertResponse(t, "/api/v1/webhooks/payments/{provider}", http.MethodPost, http.StatusOK, rec.Body.Bytes())

	// ── ONDO 路徑 ⑧：查出貨 ───────────────────────────────────────────
	tracking := "CONF-TRACK-1"
	if _, err := e.shippingSvc.CreateShipment(context.Background(), e.shopID, guestResult.Order.ID, "黑貓宅急便", &tracking, nil); err != nil {
		t.Fatalf("create shipment: %v", err)
	}
	code, body = e.do(t, http.MethodGet, "/api/v1/shop/guest/order/shipment", guestResult.Token, nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/shop/guest/order/shipment", http.MethodGet, http.StatusOK, body)

	// ── 會員路徑：補齊其餘共用 schema ─────────────────────────────────
	code, body = e.do(t, http.MethodPost, "/api/v1/shop/auth/login", "", map[string]string{
		"email": "conformance-member@t.dev", "password": "conformance-pass-1",
	})
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/shop/auth/login", http.MethodPost, http.StatusOK, body)

	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	decodeInto(t, body, &tokens)

	code, body = e.do(t, http.MethodGet, "/api/v1/shop/cart", tokens.AccessToken, nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/shop/cart", http.MethodGet, http.StatusOK, body)

	code, body = e.do(t, http.MethodPost, "/api/v1/shop/cart/items", tokens.AccessToken, map[string]any{
		"sku_id": e.skuID, "quantity": 2, "gift_message": "生日快樂",
	})
	requireStatus(t, code, http.StatusCreated, body)
	v.assertResponse(t, "/api/v1/shop/cart/items", http.MethodPost, http.StatusCreated, body)

	code, body = e.do(t, http.MethodPost, "/api/v1/shop/checkout", tokens.AccessToken, map[string]any{
		"recipient_name": "王小明", "phone": "0912345678", "line1": "台北市信義區",
		"shipping_method_id": e.shipID,
	})
	requireStatus(t, code, http.StatusCreated, body)
	v.assertResponse(t, "/api/v1/shop/checkout", http.MethodPost, http.StatusCreated, body)

	code, body = e.do(t, http.MethodGet, "/api/v1/shop/orders", tokens.AccessToken, nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/shop/orders", http.MethodGet, http.StatusOK, body)

	code, body = e.do(t, http.MethodGet, "/api/v1/shop/points", tokens.AccessToken, nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/shop/points", http.MethodGet, http.StatusOK, body)

	code, body = e.do(t, http.MethodGet, "/api/v1/shop/points/transactions", tokens.AccessToken, nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/shop/points/transactions", http.MethodGet, http.StatusOK, body)

	// ── 錯誤 envelope：401 / 404 / 422 ────────────────────────────────
	code, body = e.do(t, http.MethodGet, "/api/v1/shop/cart", "", nil)
	requireStatus(t, code, http.StatusUnauthorized, body)
	v.assertResponse(t, "/api/v1/shop/cart", http.MethodGet, http.StatusUnauthorized, body)

	code, body = e.do(t, http.MethodGet, "/api/v1/shop/orders/999999", tokens.AccessToken, nil)
	requireStatus(t, code, http.StatusNotFound, body)
	v.assertResponse(t, "/api/v1/shop/orders/{orderID}", http.MethodGet, http.StatusNotFound, body)

	code, body = e.do(t, http.MethodPost, "/api/v1/shop/cart/items", tokens.AccessToken, map[string]any{
		"sku_id": 999999, "quantity": 1,
	})
	requireStatus(t, code, http.StatusUnprocessableEntity, body)
	v.assertResponse(t, "/api/v1/shop/cart/items", http.MethodPost, http.StatusUnprocessableEntity, body)

	var validationErr struct {
		Error struct {
			Code    string `json:"code"`
			Details []struct {
				Pointer string `json:"pointer"`
				Message string `json:"message"`
			} `json:"details"`
		} `json:"error"`
	}
	decodeInto(t, body, &validationErr)
	if validationErr.Error.Code != "validation_failed" {
		t.Errorf("error.code = %q, want validation_failed", validationErr.Error.Code)
	}
	if len(validationErr.Error.Details) == 0 || validationErr.Error.Details[0].Pointer == "" {
		t.Errorf("422 必須帶 details[].pointer（自帶前端唯一能定位錯誤欄位的資訊），實際：%s", truncate(body))
	}

	// ── 冪等鍵：回放與三個新錯誤碼（change checkout-idempotency）────────
	//
	// 回放的回應**刻意**與第一次同構，所以它也必須通過同一份 201 schema——
	// 若哪天回放改成回 200 或改了主體形狀，這裡會轉紅。
	idemBody := map[string]any{
		"email": "conformance-idem@t.dev",
		"items": []map[string]any{{"sku_id": e.skuID, "quantity": 1}},
		"shipping_address": map[string]any{
			"recipient_name": "王小明", "phone": "0912345678", "line1": "台北市信義區信義路五段7號",
		},
	}
	const idemKey = "k-conformance-0001"
	code, body, hdr := e.doWithKey(t, http.MethodPost, "/api/v1/shop/guest/checkout", "", idemKey, idemBody)
	requireStatus(t, code, http.StatusCreated, body)
	v.assertResponse(t, "/api/v1/shop/guest/checkout", http.MethodPost, http.StatusCreated, body)
	if got := hdr.Get(IdempotencyReplayedHeader); got != "" {
		t.Errorf("第一次請求不得帶 %s，實際 %q", IdempotencyReplayedHeader, got)
	}

	code, body, hdr = e.doWithKey(t, http.MethodPost, "/api/v1/shop/guest/checkout", "", idemKey, idemBody)
	requireStatus(t, code, http.StatusCreated, body)
	v.assertResponse(t, "/api/v1/shop/guest/checkout", http.MethodPost, http.StatusCreated, body)
	if got := hdr.Get(IdempotencyReplayedHeader); got != "true" {
		t.Errorf("回放必須帶 %s: true，實際 %q", IdempotencyReplayedHeader, got)
	}

	// 422 invalid_idempotency_key
	code, body, _ = e.doWithKey(t, http.MethodPost, "/api/v1/shop/guest/checkout", "", "short", idemBody)
	requireStatus(t, code, http.StatusUnprocessableEntity, body)
	v.assertResponse(t, "/api/v1/shop/guest/checkout", http.MethodPost, http.StatusUnprocessableEntity, body)
	assertErrorCode(t, body, "invalid_idempotency_key")

	// 422 idempotency_key_reused
	reusedBody := map[string]any{
		"email": "conformance-idem@t.dev",
		"items": []map[string]any{{"sku_id": e.skuID, "quantity": 2}},
		"shipping_address": map[string]any{
			"recipient_name": "王小明", "phone": "0912345678", "line1": "台北市信義區信義路五段7號",
		},
	}
	code, body, _ = e.doWithKey(t, http.MethodPost, "/api/v1/shop/guest/checkout", "", idemKey, reusedBody)
	requireStatus(t, code, http.StatusUnprocessableEntity, body)
	v.assertResponse(t, "/api/v1/shop/guest/checkout", http.MethodPost, http.StatusUnprocessableEntity, body)
	assertErrorCode(t, body, "idempotency_key_reused")

	// 409 idempotency_key_in_progress — 直接種一列 in_progress，而不是靠並發：
	// 契約驗證要的是「這個回應的形狀對不對」，用競態去產生它只會讓這支測試變得
	// 不穩定。真正的並發行為由 idempotency_integration_test.go 的 -race 測試證明。
	const stuckKey = "k-conformance-stuck"
	e.client.CheckoutIdempotencyKey.Create().
		SetShopID(e.shopID).SetIdempotencyKey(stuckKey).
		SetRequestHash(idempotency.Fingerprint(idempotency.ScopeGuestCheckout, mustJSON(t, idemBody))).
		SetStatus(idempotency.StatusInProgress).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SaveX(context.Background())
	code, body, _ = e.doWithKey(t, http.MethodPost, "/api/v1/shop/guest/checkout", "", stuckKey, idemBody)
	requireStatus(t, code, http.StatusConflict, body)
	v.assertResponse(t, "/api/v1/shop/guest/checkout", http.MethodPost, http.StatusConflict, body)
	assertErrorCode(t, body, "idempotency_key_in_progress")

	// ── 規則 2：共用 schema 覆蓋 ──────────────────────────────────────
	var uncovered []string
	for _, name := range requiredSchemas {
		if !v.covered[name] {
			uncovered = append(uncovered, name)
		}
	}
	sort.Strings(uncovered)
	if len(uncovered) > 0 {
		t.Errorf("%d 個共用 schema 沒有被任何一條被驗證過的回應涵蓋——取樣清單被砍短了（design D10 規則 2）：\n  %s",
			len(uncovered), strings.Join(uncovered, "\n  "))
	}
	names := make([]string, 0, len(v.covered))
	for n := range v.covered {
		names = append(names, n)
	}
	sort.Strings(names)
	t.Logf("以真實回應驗證了 %d 個共用 schema：%s", len(names), strings.Join(names, ", "))
}

// assertErrorCode checks the machine-readable code a consumer branches on.
// The envelope's *shape* is validated by assertResponse; this pins the value,
// because a code silently renamed would pass the schema and break every client.
func assertErrorCode(t *testing.T, body []byte, want string) {
	t.Helper()
	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decodeInto(t, body, &parsed)
	if parsed.Error.Code != want {
		t.Errorf("error.code = %q, want %q; body=%s", parsed.Error.Code, want, truncate(body))
	}
}

// mustJSON reproduces the exact bytes doWithKey sends, so a fingerprint computed
// here matches the one the handler will compute.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func requireStatus(t *testing.T, got, want int, body []byte) {
	t.Helper()
	if got != want {
		t.Fatalf("status = %d, want %d; body=%s", got, want, truncate(body))
	}
}
