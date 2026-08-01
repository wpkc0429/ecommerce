package ecpay_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/payment"
	"ksdevworks/ecommerce/api/internal/payment/ecpay"
)

const (
	testMerchantID  = "3002607"
	testHashKey     = "pwFHCqoQZGmho4w6"
	testHashIV      = "EkRm7iFT261dpevs"
	testCheckoutURL = "http://localhost:8090/Cashier/AioCheckOut/V5"
	testReturnURL   = "http://localhost:8080/api/v1/webhooks/payments/ecpay"
)

func testConfig() ecpay.Config {
	return ecpay.Config{
		MerchantID:    testMerchantID,
		HashKey:       testHashKey,
		HashIV:        testHashIV,
		CheckoutURL:   testCheckoutURL,
		ReturnURL:     testReturnURL,
		ClientBackURL: "http://localhost:3000/thanks",
	}
}

func newProvider() *ecpay.Provider {
	fixed := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	return ecpay.New(testConfig(), func() time.Time { return fixed })
}

// compile-time proof that the provider satisfies the abstraction.
var _ payment.Provider = (*ecpay.Provider)(nil)

func TestProviderName(t *testing.T) {
	if got := newProvider().Name(); got != "ecpay" {
		t.Fatalf("Name() = %q", got)
	}
}

// ── InitiatePayment ────────────────────────────────────────────────────

func initiate(t *testing.T, req payment.InitiateRequest) *payment.InitiateResult {
	t.Helper()
	res, err := newProvider().InitiatePayment(context.Background(), req)
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}
	return res
}

func TestInitiateProducesEveryRequiredField(t *testing.T) {
	res := initiate(t, payment.InitiateRequest{
		ShopID: 1, OrderID: 1234, Amount: 3400, Currency: "TWD",
		Items: []payment.InitiateItem{{Name: "季節花束", Quantity: 1, UnitAmount: 3400}},
	})

	if res.RedirectForm == nil {
		t.Fatal("綠界 needs a form POST; RedirectForm must not be nil")
	}
	if res.RedirectForm.Method != http.MethodPost {
		t.Errorf("method = %q, 綠界's cashier is POST-only", res.RedirectForm.Method)
	}
	if res.RedirectForm.Action != testCheckoutURL {
		t.Errorf("action = %q, want the configured cashier URL", res.RedirectForm.Action)
	}
	// design D2: a plain redirect cannot carry the field set, so this stays
	// empty and consumers are told to look at RedirectForm first.
	if res.RedirectURL != "" {
		t.Errorf("redirect_url must be empty for a form-POST provider, got %q", res.RedirectURL)
	}

	f := res.RedirectForm.Fields
	for _, name := range []string{
		"MerchantID", "MerchantTradeNo", "MerchantTradeDate", "PaymentType",
		"TotalAmount", "TradeDesc", "ItemName", "ReturnURL", "ChoosePayment",
		"EncryptType", "CheckMacValue",
	} {
		if f[name] == "" {
			t.Errorf("required 綠界 field %s is missing or empty", name)
		}
	}
	if f["PaymentType"] != "aio" {
		t.Errorf("PaymentType = %q, AioCheckOut V5 fixes it at aio", f["PaymentType"])
	}
	if f["EncryptType"] != "1" {
		t.Errorf("EncryptType = %q, must be 1 (SHA256)", f["EncryptType"])
	}
	if f["ChoosePayment"] != "Credit" {
		t.Errorf("ChoosePayment default = %q, want Credit", f["ChoosePayment"])
	}
	if f["ReturnURL"] != testReturnURL {
		t.Errorf("ReturnURL = %q", f["ReturnURL"])
	}
	if res.ProviderReference != f["MerchantTradeNo"] {
		t.Errorf("provider_reference (%q) must be the MerchantTradeNo we sent (%q)",
			res.ProviderReference, f["MerchantTradeNo"])
	}
}

// TestInitiateAmountIsSentVerbatim is the mechanical guard the OpenAPI money
// check cannot provide (design D17): redirect_form.fields has no per-field
// schema, so nothing in the contract can assert that TotalAmount is the order
// amount with no scaling. This is where that lives instead.
func TestInitiateAmountIsSentVerbatim(t *testing.T) {
	res := initiate(t, payment.InitiateRequest{ShopID: 1, OrderID: 7, Amount: 3400, Currency: "TWD"})
	if got := res.RedirectForm.Fields["TotalAmount"]; got != "3400" {
		t.Fatalf("TotalAmount = %q, want \"3400\" — TWD's minor unit IS the dollar, "+
			"so there must be no /100 or *100 anywhere on this path", got)
	}
}

func TestInitiateRejectsNonTWD(t *testing.T) {
	for _, cur := range []string{"USD", "JPY", "", "twd"} {
		_, err := newProvider().InitiatePayment(context.Background(), payment.InitiateRequest{
			ShopID: 1, OrderID: 1, Amount: 100, Currency: cur,
		})
		if err == nil {
			t.Errorf("currency %q was accepted; 綠界 only settles TWD and the amount "+
				"identity holds only there", cur)
		}
	}
}

func TestInitiateSignatureVerifies(t *testing.T) {
	res := initiate(t, payment.InitiateRequest{ShopID: 1, OrderID: 42, Amount: 500, Currency: "TWD"})
	f := res.RedirectForm.Fields
	want := ecpay.CheckMacValue(testHashKey, testHashIV, f)
	if f["CheckMacValue"] != want {
		t.Fatalf("the form carries a checksum that does not match its own fields")
	}
}

func TestInitiateItemNameRendersLines(t *testing.T) {
	res := initiate(t, payment.InitiateRequest{
		ShopID: 1, OrderID: 9, Amount: 7800, Currency: "TWD",
		Items: []payment.InitiateItem{
			{Name: "玫瑰花束", Quantity: 1, UnitAmount: 4400},
			{Name: "康乃馨", Quantity: 2, UnitAmount: 1700},
		},
	})
	got := res.RedirectForm.Fields["ItemName"]
	if !strings.Contains(got, "玫瑰花束") || !strings.Contains(got, "康乃馨") || !strings.Contains(got, "#") {
		t.Fatalf("ItemName = %q, want both lines joined by #", got)
	}
}

// TestInitiateItemNameTruncatesOnRuneBoundary — 綠界 truncates ItemName past 400
// characters itself, and its cut produces a mangled trailing byte that breaks
// the checksum and loses the order. Ours has to happen first, and on a rune
// boundary.
func TestInitiateItemNameTruncatesOnRuneBoundary(t *testing.T) {
	items := make([]payment.InitiateItem, 0, 40)
	for i := 0; i < 40; i++ {
		items = append(items, payment.InitiateItem{Name: strings.Repeat("繡球花", 10), Quantity: 1, UnitAmount: 100})
	}
	res := initiate(t, payment.InitiateRequest{ShopID: 1, OrderID: 9, Amount: 4000, Currency: "TWD", Items: items})
	got := res.RedirectForm.Fields["ItemName"]
	if n := len([]rune(got)); n > 400 {
		t.Fatalf("ItemName is %d runes, 綠界 allows 400", n)
	}
	if !strings.ContainsRune(got, '繡') || strings.ContainsRune(got, '�') {
		t.Fatalf("ItemName was cut mid-character: %q", got[len(got)-12:])
	}
}

// ── MerchantTradeNo ────────────────────────────────────────────────────

func TestMerchantTradeNoWithinECPayLimits(t *testing.T) {
	now := time.Now()
	for _, orderID := range []int{1, 42, 999999, 999999999} {
		no, err := ecpay.NewMerchantTradeNo(orderID, now)
		if err != nil {
			t.Fatalf("order %d: %v", orderID, err)
		}
		if len(no) > ecpay.MaxMerchantTradeNoLen {
			t.Errorf("order %d produced %q (%d chars), 綠界 allows %d",
				orderID, no, len(no), ecpay.MaxMerchantTradeNoLen)
		}
		for _, r := range no {
			isAlnum := (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
			if !isAlnum {
				t.Errorf("order %d produced %q, which contains the non-alphanumeric %q", orderID, no, r)
			}
		}
	}
}

// TestMerchantTradeNoFailsLoudlyWhenTooLong — better a 500 in our own handler
// with a readable message than a Chinese error code on 綠界's cashier page.
func TestMerchantTradeNoFailsLoudlyWhenTooLong(t *testing.T) {
	// 20 digits of order id cannot fit alongside the prefix and timestamp.
	if _, err := ecpay.NewMerchantTradeNo(99999999999999999, time.Now()); err == nil {
		t.Fatal("an over-long MerchantTradeNo must be an error, not something we send")
	}
}

// TestMerchantTradeNoDiffersBetweenAttempts — 綠界 burns a MerchantTradeNo even
// when its transaction failed, so a retry has to bring a new one.
func TestMerchantTradeNoDiffersBetweenAttempts(t *testing.T) {
	base := time.Now()
	a, _ := ecpay.NewMerchantTradeNo(1234, base)
	b, _ := ecpay.NewMerchantTradeNo(1234, base.Add(time.Millisecond))
	if a == b {
		t.Fatalf("two attempts on one order produced the same identifier: %q", a)
	}
}

// ── VerifyWebhook ──────────────────────────────────────────────────────

func callback(t *testing.T, extra map[string]string) payment.WebhookRequest {
	t.Helper()
	fields := map[string]string{
		"MerchantID":           testMerchantID,
		"MerchantTradeNo":      "O1234TMZQ8K3PA",
		"StoreID":              "",
		"RtnCode":              "1",
		"RtnMsg":               "交易成功",
		"TradeNo":              "2412311225437371",
		"TradeAmt":             "3400",
		"PaymentDate":          "2026/08/01 20:26:09",
		"PaymentType":          "Credit_CreditCard",
		"PaymentTypeChargeFee": "10",
		"TradeDate":            "2026/08/01 20:25:43",
		"SimulatePaid":         "0",
	}
	for k, v := range extra {
		if v == "\x00" {
			delete(fields, k)
			continue
		}
		fields[k] = v
	}
	return payment.WebhookRequest{
		Headers: http.Header{},
		Body:    []byte(ecpay.SignCallback(testHashKey, testHashIV, fields)),
	}
}

func verify(t *testing.T, req payment.WebhookRequest) (*payment.WebhookResult, error) {
	t.Helper()
	return newProvider().VerifyWebhook(context.Background(), req)
}

func TestVerifyWebhookSuccess(t *testing.T) {
	res, err := verify(t, callback(t, nil))
	if err != nil {
		t.Fatalf("VerifyWebhook: %v", err)
	}
	if res.Outcome != payment.OutcomeSucceeded {
		t.Errorf("outcome = %d, want succeeded", res.Outcome)
	}
	if res.ProviderReference != "O1234TMZQ8K3PA" {
		t.Errorf("provider_reference = %q", res.ProviderReference)
	}
	if res.ProviderTransactionID != "2412311225437371" {
		t.Errorf("綠界's own TradeNo must be captured, got %q", res.ProviderTransactionID)
	}
	if res.Amount != 3400 {
		t.Errorf("TradeAmt = %d, want 3400", res.Amount)
	}
	if res.PaidAt == nil {
		t.Fatal("PaymentDate must be parsed into PaidAt")
	}
	// 2026/08/01 20:26:09 UTC+8 == 12:26:09 UTC.
	if got := res.PaidAt.UTC().Format(time.RFC3339); got != "2026-08-01T12:26:09Z" {
		t.Errorf("PaidAt = %s, want 綠界's Taipei-local stamp read as UTC+8", got)
	}
}

func TestVerifyWebhookRejectsMissingChecksum(t *testing.T) {
	req := payment.WebhookRequest{Headers: http.Header{}, Body: []byte("MerchantTradeNo=O1T2&RtnCode=1")}
	if _, err := verify(t, req); !errors.Is(err, payment.ErrInvalidSignature) {
		t.Fatalf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestVerifyWebhookRejectsTamperedBody(t *testing.T) {
	req := callback(t, nil)
	// Flip the amount without recomputing the checksum — exactly what a forged
	// notification looks like.
	req.Body = []byte(strings.Replace(string(req.Body), "TradeAmt=3400", "TradeAmt=1", 1))
	if _, err := verify(t, req); !errors.Is(err, payment.ErrInvalidSignature) {
		t.Fatalf("err = %v, want ErrInvalidSignature", err)
	}
}

// TestVerifyWebhookCountsUnknownFieldsInTheChecksum is the property that makes
// payment.WebhookRequest's raw-bytes design load-bearing (design D14): 綠界 adds
// fields per payment method (NeedExtraPaidInfo brings six lowercase ones), and
// they all participate in the checksum. An implementation that decoded into a
// fixed struct would drop them and compute a wrong value.
func TestVerifyWebhookCountsUnknownFieldsInTheChecksum(t *testing.T) {
	res, err := verify(t, callback(t, map[string]string{
		"gwsr": "12345678", "auth_code": "777777", "process_date": "2026/08/01 20:26:09",
		"SomeFieldECPayAddsNextYear": "whatever",
	}))
	if err != nil {
		t.Fatalf("a notification carrying fields we do not know must still verify: %v", err)
	}
	if res.Outcome != payment.OutcomeSucceeded {
		t.Errorf("outcome = %d", res.Outcome)
	}
}

func TestVerifyWebhookRequiresMerchantTradeNo(t *testing.T) {
	if _, err := verify(t, callback(t, map[string]string{"MerchantTradeNo": "\x00"})); err == nil {
		t.Fatal("a notification without MerchantTradeNo cannot be matched to anything")
	}
}

// TestVerifyWebhookOutcomeMapping is design D6 written as a table. The two
// Pending rows are the reason payment.Outcome has a third value at all.
func TestVerifyWebhookOutcomeMapping(t *testing.T) {
	cases := []struct {
		name         string
		rtnCode      string
		simulatePaid string
		want         payment.Outcome
		why          string
	}{
		{"real success", "1", "0", payment.OutcomeSucceeded, ""},
		{
			"simulated payment", "1", "1", payment.OutcomePending,
			"綠界's back-office 模擬付款 button sends RtnCode=1 and settles nothing — " +
				"treating it as success marks an order paid with no money behind it",
		},
		{
			"awaiting confirmation", "10300066", "0", payment.OutcomePending,
			"10300066 is 待確認中請勿出貨; treating it as failure makes the row terminal " +
				"and the later real success can never advance the order",
		},
		{"insufficient funds", "10100252", "0", payment.OutcomeFailed, ""},
		{"declined", "10100248", "0", payment.OutcomeFailed, ""},
		{"unknown code", "999999", "0", payment.OutcomeFailed, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := verify(t, callback(t, map[string]string{
				"RtnCode": tc.rtnCode, "SimulatePaid": tc.simulatePaid,
			}))
			if err != nil {
				t.Fatalf("VerifyWebhook: %v", err)
			}
			if res.Outcome != tc.want {
				t.Errorf("RtnCode=%s SimulatePaid=%s → outcome %d, want %d. %s",
					tc.rtnCode, tc.simulatePaid, res.Outcome, tc.want, tc.why)
			}
		})
	}
}

// ── WebhookAck ─────────────────────────────────────────────────────────

// TestWebhookAckBytes pins the exact strings 綠界 accepts. Its documentation
// lists the near-misses it sees in the wild ("1|OK" with quotes, 1|ok, 1OK, OK)
// — every one of them causes redelivery every 5–15 minutes, 4 times a day.
func TestWebhookAckBytes(t *testing.T) {
	p := newProvider()
	cases := []struct {
		ack    payment.Ack
		status int
		body   string
	}{
		{payment.AckProcessed, http.StatusOK, "1|OK"},
		// Not recognizing a transaction is not something a retry can fix.
		{payment.AckIgnored, http.StatusOK, "1|OK"},
		{payment.AckInvalidSignature, http.StatusOK, "0|CheckMacValue Error"},
		{payment.AckMalformed, http.StatusOK, "0|Bad Request"},
		// The only case where a retry helps, so the only non-2xx.
		{payment.AckFailed, http.StatusInternalServerError, "0|Internal Error"},
	}
	for _, tc := range cases {
		got := p.WebhookAck(tc.ack)
		if got.IsZero() {
			t.Errorf("ack %d returned the zero response; 綠界 requires plain text", tc.ack)
			continue
		}
		if string(got.Body) != tc.body {
			t.Errorf("ack %d body = %q, want %q", tc.ack, got.Body, tc.body)
		}
		if got.Status != tc.status {
			t.Errorf("ack %d status = %d, want %d", tc.ack, got.Status, tc.status)
		}
		if !strings.HasPrefix(got.ContentType, "text/plain") {
			t.Errorf("ack %d content type = %q, want text/plain", tc.ack, got.ContentType)
		}
	}
}
