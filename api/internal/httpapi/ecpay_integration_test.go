package httpapi_test

// change ecpay-payment-provider — the HTTP layer, end to end.
//
// The service-level state machine is covered in internal/payment; what is only
// observable HERE is the wire: the field set a consumer receives, the exact
// bytes 綠界 receives back, and the reconciliation lookup a merchant uses.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/payment"
	"ksdevworks/ecommerce/api/internal/payment/ecpay"
)

// 綠界's published AIO test store (文件 /2856/). Not secrets.
const (
	ecpayTestMerchantID = "3002607"
	ecpayTestHashKey    = "pwFHCqoQZGmho4w6"
	ecpayTestHashIV     = "EkRm7iFT261dpevs"
)

// initiateECPay starts a payment for orderID with the ecpay provider and
// returns the parsed 201 body.
func (e *paymentEnv) initiateECPay(t *testing.T, memberID, shopID int, domain string, orderID int) map[string]any {
	t.Helper()
	code, body := e.memberCallJSON(t, memberID, shopID, domain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/payments", orderID),
		map[string]any{"provider": "ecpay"})
	if code != http.StatusCreated {
		t.Fatalf("initiate with ecpay: want 201, got %d: %+v", code, body)
	}
	return body
}

// deliverECPayCallback signs and posts a 綠界-shaped payment result
// notification, returning the status and the RAW response body.
//
// The raw body matters: this is the one place in the codebase where the exact
// bytes of a response are the contract.
func (e *paymentEnv) deliverECPayCallback(fields map[string]string) (int, string, string) {
	body := ecpay.SignCallback(ecpayTestHashKey, ecpayTestHashIV, fields)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/payments/ecpay", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec.Code, strings.TrimSpace(rec.Body.String()), rec.Header().Get("Content-Type")
}

func ecpayCallbackFields(merchantTradeNo string, amount int64, rtnCode, simulatePaid string) map[string]string {
	now := time.Now()
	return map[string]string{
		"MerchantID":           ecpayTestMerchantID,
		"MerchantTradeNo":      merchantTradeNo,
		"StoreID":              "",
		"RtnCode":              rtnCode,
		"RtnMsg":               "交易成功",
		"TradeNo":              "2412311225437371",
		"TradeAmt":             fmt.Sprintf("%d", amount),
		"PaymentDate":          now.Format("2006/01/02 15:04:05"),
		"PaymentType":          "Credit_CreditCard",
		"PaymentTypeChargeFee": "10",
		"TradeDate":            now.Format("2006/01/02 15:04:05"),
		"SimulatePaid":         simulatePaid,
	}
}

// ── the initiate response ──────────────────────────────────────────────

// TestECPayInitiateReturnsAFormNotAURL is design D2 observed from the outside:
// a consumer receives a field set to POST, not a URL to redirect to.
func TestECPayInitiateReturnsAFormNotAURL(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 3400)
	body := e.initiateECPay(t, e.memberA1, e.shopA, e.shopADomain, orderID)

	form, ok := body["redirect_form"].(map[string]any)
	if !ok {
		t.Fatalf("redirect_form is %T, want an object — 綠界's cashier takes a form POST", body["redirect_form"])
	}
	if form["method"] != "POST" {
		t.Errorf("method = %v, want POST", form["method"])
	}
	if action, _ := form["action"].(string); !strings.HasSuffix(action, "/Cashier/AioCheckOut/V5") {
		t.Errorf("action = %q", action)
	}
	// redirect_url survives as a v1 promise but has no meaning here.
	if body["redirect_url"] != "" {
		t.Errorf("redirect_url = %v, want \"\" — a GET cannot reach 綠界's cashier", body["redirect_url"])
	}

	fields, ok := form["fields"].(map[string]any)
	if !ok {
		t.Fatalf("redirect_form.fields is %T, want an object", form["fields"])
	}
	for _, name := range []string{
		"MerchantID", "MerchantTradeNo", "MerchantTradeDate", "PaymentType",
		"TotalAmount", "TradeDesc", "ItemName", "ReturnURL", "ChoosePayment",
		"EncryptType", "CheckMacValue",
	} {
		if v, _ := fields[name].(string); v == "" {
			t.Errorf("required 綠界 field %s missing from redirect_form.fields", name)
		}
	}
	// design D7 / design D17: this assertion is the mechanical guard the OpenAPI
	// money check cannot give us, because fields has no per-key schema.
	if fields["TotalAmount"] != "3400" {
		t.Errorf("TotalAmount = %v, want \"3400\" — no scaling, TWD's minor unit is the dollar",
			fields["TotalAmount"])
	}
	// provider_reference is the MerchantTradeNo we sent (design D4): it has to
	// exist at this moment, which is why it cannot be 綠界's TradeNo.
	if body["provider_reference"] != fields["MerchantTradeNo"] {
		t.Errorf("provider_reference %v != MerchantTradeNo %v", body["provider_reference"], fields["MerchantTradeNo"])
	}
	// The provider's own identifier is not knowable yet.
	if body["provider_transaction_id"] != nil || body["paid_at"] != nil {
		t.Errorf("provider_transaction_id/paid_at must be null before any callback, got %v/%v",
			body["provider_transaction_id"], body["paid_at"])
	}
}

// TestECPayInitiateFormSurvivesFormEncoding proves the field set we hand a
// browser still verifies after a real form round trip — the checksum covers
// values that are about to be percent-encoded, joined and re-parsed.
func TestECPayInitiateFormSurvivesFormEncoding(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 3400)
	body := e.initiateECPay(t, e.memberA1, e.shopA, e.shopADomain, orderID)
	form := body["redirect_form"].(map[string]any)

	// Exactly what a browser does with <form method="post">.
	values := url.Values{}
	for k, v := range form["fields"].(map[string]any) {
		values.Set(k, v.(string))
	}
	encoded := values.Encode()

	parsed, err := url.ParseQuery(encoded)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	round := map[string]string{}
	for k := range parsed {
		round[k] = parsed.Get(k)
	}
	want := ecpay.CheckMacValue(ecpayTestHashKey, ecpayTestHashIV, round)
	if round["CheckMacValue"] != want {
		t.Fatalf("the checksum does not survive form encoding: %s vs %s", round["CheckMacValue"], want)
	}
}

// ── the callback ───────────────────────────────────────────────────────

// TestECPayCallbackAnswersWithExactlyOneOK is design D3 as bytes.
//
// 綠界 accepts the literal `1|OK` and nothing else; its documentation lists the
// near misses it sees in the wild ("1|OK" with quotes, 1|ok, 1OK, OK) and every
// one of them causes redelivery every 5–15 minutes, 4 times a day. A JSON
// envelope is simply one more of them — which is why the response shape had to
// become part of the Provider contract rather than a special case in the
// handler.
func TestECPayCallbackAnswersWithExactlyOneOK(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 3400)
	body := e.initiateECPay(t, e.memberA1, e.shopA, e.shopADomain, orderID)
	ref := body["provider_reference"].(string)

	code, raw, ctype := e.deliverECPayCallback(ecpayCallbackFields(ref, 3400, "1", "0"))
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if raw != "1|OK" {
		t.Fatalf("response body = %q, want exactly %q — anything else makes 綠界 redeliver", raw, "1|OK")
	}
	if !strings.HasPrefix(ctype, "text/plain") {
		t.Errorf("content-type = %q, want text/plain", ctype)
	}

	// And the order really did advance.
	code, ord := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET",
		fmt.Sprintf("/api/v1/shop/orders/%d", orderID), nil)
	if code != http.StatusOK {
		t.Fatalf("get order: %d", code)
	}
	if ord["payment_status"] != float64(1) {
		t.Fatalf("payment_status = %v, want 1", ord["payment_status"])
	}
}

// TestECPayCallbackRecordsBothIdentifiers is design D4 observed from the
// merchant's side: after the callback, the row carries 綠界's OWN number too.
func TestECPayCallbackRecordsBothIdentifiers(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 3400)
	body := e.initiateECPay(t, e.memberA1, e.shopA, e.shopADomain, orderID)
	ref := body["provider_reference"].(string)

	if _, raw, _ := e.deliverECPayCallback(ecpayCallbackFields(ref, 3400, "1", "0")); raw != "1|OK" {
		t.Fatalf("callback rejected: %q", raw)
	}

	code, listed := e.adminCallJSON(t, e.ownerA, "GET",
		e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/payments", orderID)), nil)
	if code != http.StatusOK {
		t.Fatalf("list payments: %d", code)
	}
	rows := listed["payments"].([]any)
	if len(rows) != 1 {
		t.Fatalf("payments = %d, want 1", len(rows))
	}
	row := rows[0].(map[string]any)
	if row["provider_reference"] != ref {
		t.Errorf("provider_reference = %v, want %q", row["provider_reference"], ref)
	}
	if row["provider_transaction_id"] != "2412311225437371" {
		t.Errorf("provider_transaction_id = %v, want 綠界's TradeNo", row["provider_transaction_id"])
	}
	if row["paid_at"] == nil {
		t.Error("paid_at is null; 綠界 reported a PaymentDate and it must be recorded")
	}
}

// TestECPayCallbackKeepsProvidersPaymentDate — when 綠界 reports PaymentDate it
// is authoritative and the "now" fallback (change order-and-invitation-polish,
// design D3) must not overwrite it.
func TestECPayCallbackKeepsProvidersPaymentDate(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 3400)
	ref := e.initiateECPay(t, e.memberA1, e.shopA, e.shopADomain, orderID)["provider_reference"].(string)

	fields := ecpayCallbackFields(ref, 3400, "1", "0")
	fields["PaymentDate"] = "2026/01/02 03:04:05" // Taipei-local, UTC+8
	if _, raw, _ := e.deliverECPayCallback(fields); raw != "1|OK" {
		t.Fatalf("callback rejected: %q", raw)
	}
	row := e.firstPayment(t, orderID)
	got, err := time.Parse(time.RFC3339, fmt.Sprint(row["paid_at"]))
	if err != nil || !got.Equal(time.Date(2026, 1, 1, 19, 4, 5, 0, time.UTC)) {
		t.Errorf("paid_at = %v, want the provider's 2026-01-01T19:04:05Z (err %v)", row["paid_at"], err)
	}
}

// TestECPayCallbackWithoutPaymentDateStillSetsPaidAt — 綠界 can omit
// PaymentDate; a succeeded payment still gets a paid_at (the processing time).
func TestECPayCallbackWithoutPaymentDateStillSetsPaidAt(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 3400)
	ref := e.initiateECPay(t, e.memberA1, e.shopA, e.shopADomain, orderID)["provider_reference"].(string)

	fields := ecpayCallbackFields(ref, 3400, "1", "0")
	delete(fields, "PaymentDate")
	if _, raw, _ := e.deliverECPayCallback(fields); raw != "1|OK" {
		t.Fatalf("callback rejected: %q", raw)
	}
	row := e.firstPayment(t, orderID)
	paidAt, _ := row["paid_at"].(string)
	ts, err := time.Parse(time.RFC3339, paidAt)
	if err != nil {
		t.Fatalf("paid_at = %v, want a timestamp (%v)", row["paid_at"], err)
	}
	if d := time.Since(ts); d < -time.Minute || d > time.Minute {
		t.Errorf("paid_at = %s, want roughly now", paidAt)
	}
}

// firstPayment returns the merchant-visible payment row of an order.
func (e *paymentEnv) firstPayment(t *testing.T, orderID int) map[string]any {
	t.Helper()
	code, listed := e.adminCallJSON(t, e.ownerA, "GET",
		e.shopPath(e.shopA, fmt.Sprintf("/orders/%d/payments", orderID)), nil)
	if code != http.StatusOK {
		t.Fatalf("list payments: %d", code)
	}
	rows := listed["payments"].([]any)
	if len(rows) != 1 {
		t.Fatalf("payments = %d, want 1", len(rows))
	}
	return rows[0].(map[string]any)
}

// TestECPayCallbackRejectsABadChecksum — 綠界's own instruction is to recompute
// the checksum and only then answer 1|OK.
func TestECPayCallbackRejectsABadChecksum(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 3400)
	body := e.initiateECPay(t, e.memberA1, e.shopA, e.shopADomain, orderID)
	ref := body["provider_reference"].(string)

	// Tamper after signing.
	signed := ecpay.SignCallback(ecpayTestHashKey, ecpayTestHashIV, ecpayCallbackFields(ref, 3400, "1", "0"))
	tampered := strings.Replace(signed, "TradeAmt=3400", "TradeAmt=1", 1)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/payments/ecpay", strings.NewReader(tampered))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	if got := strings.TrimSpace(rec.Body.String()); got == "1|OK" {
		t.Fatal("a tampered notification was acknowledged as processed")
	}
	code, ord := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET",
		fmt.Sprintf("/api/v1/shop/orders/%d", orderID), nil)
	if code != http.StatusOK || ord["payment_status"] != float64(0) {
		t.Fatalf("order must stay unpaid, payment_status = %v", ord["payment_status"])
	}
}

// TestECPaySimulatedPaymentDoesNotMarkTheOrderPaid is design D6's headline case,
// through the full stack.
//
// 綠界's back-office "模擬付款" button sends RtnCode=1, RtnMsg=交易成功 and
// SimulatePaid=1 — and settles no money. A system reading only RtnCode marks the
// order paid, awards loyalty points and queues it for shipping, with nothing in
// the bank, triggered by anyone who can log into the merchant's 綠界 account.
func TestECPaySimulatedPaymentDoesNotMarkTheOrderPaid(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 3400)
	body := e.initiateECPay(t, e.memberA1, e.shopA, e.shopADomain, orderID)
	ref := body["provider_reference"].(string)

	code, raw, _ := e.deliverECPayCallback(ecpayCallbackFields(ref, 3400, "1", "1"))
	if code != http.StatusOK || raw != "1|OK" {
		t.Fatalf("a simulated-payment notification must still be acknowledged: %d %q", code, raw)
	}

	_, ord := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET",
		fmt.Sprintf("/api/v1/shop/orders/%d", orderID), nil)
	if ord["payment_status"] != float64(0) {
		t.Fatalf("payment_status = %v; RtnCode=1 with SimulatePaid=1 settles NO money and "+
			"must not mark the order paid", ord["payment_status"])
	}

	// And the row is still claimable: the real notification still works.
	if _, raw, _ := e.deliverECPayCallback(ecpayCallbackFields(ref, 3400, "1", "0")); raw != "1|OK" {
		t.Fatalf("the real notification was rejected: %q", raw)
	}
	_, ord = e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET",
		fmt.Sprintf("/api/v1/shop/orders/%d", orderID), nil)
	if ord["payment_status"] != float64(1) {
		t.Fatalf("payment_status = %v after the real notification, want 1", ord["payment_status"])
	}
}

// TestECPayAmountMismatchIsNotAcknowledged — design D7. 綠界 retries a
// non-`1|OK` answer and then records the failure on its side, which is exactly
// the outcome a money disagreement deserves.
func TestECPayAmountMismatchIsNotAcknowledged(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 3400)
	body := e.initiateECPay(t, e.memberA1, e.shopA, e.shopADomain, orderID)
	ref := body["provider_reference"].(string)

	code, raw, _ := e.deliverECPayCallback(ecpayCallbackFields(ref, 34, "1", "0"))
	if raw == "1|OK" {
		t.Fatal("an amount mismatch was acknowledged as processed")
	}
	if code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 so 綠界 retries and eventually logs a delivery failure", code)
	}
	_, ord := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET",
		fmt.Sprintf("/api/v1/shop/orders/%d", orderID), nil)
	if ord["payment_status"] != float64(0) {
		t.Errorf("order advanced despite the amount mismatch")
	}
}

// TestECPayUnknownReferenceIsAcknowledged — retrying a MerchantTradeNo we do not
// recognize cannot make it recognizable, so it is acknowledged, not retried.
func TestECPayUnknownReferenceIsAcknowledged(t *testing.T) {
	e := newPaymentEnv(t)
	code, raw, _ := e.deliverECPayCallback(ecpayCallbackFields("O999999TZZZZZZZZ", 100, "1", "0"))
	if code != http.StatusOK || raw != "1|OK" {
		t.Fatalf("got %d %q, want 200 %q", code, raw, "1|OK")
	}
}

// TestMockWebhookResponseIsUnchanged is the guard on the zero-value default
// (design D3): extending the Provider interface must not have moved a single
// byte for a provider that has no protocol of its own.
func TestMockWebhookResponseIsUnchanged(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)
	code, created := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/payments", orderID), nil)
	if code != http.StatusCreated {
		t.Fatalf("initiate: %d", code)
	}
	ref := created["provider_reference"].(string)

	body := webhookBody(t, ref, "succeeded")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/payments/mock", bytes.NewReader(body))
	req.Header.Set("X-Payment-Signature", payment.SignMockWebhook(e.webhookSecret, body))
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q, want application/json — the mock provider has no protocol "+
			"of its own and must keep answering in the API's own envelope", ct)
	}
	var parsed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("mock webhook response is no longer JSON: %v", err)
	}
	if parsed["status"] != "ok" {
		t.Errorf("body = %v, want {\"status\":\"ok\"}", parsed)
	}
	// The mock's redirect stays a plain URL, with no form.
	if created["redirect_url"] == "" {
		t.Error("the mock provider's redirect_url must still be a URL")
	}
	if created["redirect_form"] != nil {
		t.Errorf("redirect_form = %v, want null for a plain-redirect provider", created["redirect_form"])
	}
}

// ── the reconciliation lookup (design D10) ─────────────────────────────

func TestShopPaymentsLookupByEitherIdentifier(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 3400)
	body := e.initiateECPay(t, e.memberA1, e.shopA, e.shopADomain, orderID)
	ref := body["provider_reference"].(string)
	if _, raw, _ := e.deliverECPayCallback(ecpayCallbackFields(ref, 3400, "1", "0")); raw != "1|OK" {
		t.Fatalf("callback rejected: %q", raw)
	}

	for _, q := range []string{ref, "2412311225437371", fmt.Sprintf("%d", orderID)} {
		code, listed := e.adminCallJSON(t, e.ownerA, "GET",
			e.shopPath(e.shopA, "/payments?q="+url.QueryEscape(q)), nil)
		if code != http.StatusOK {
			t.Fatalf("q=%q: status %d", q, code)
		}
		rows := listed["payments"].([]any)
		if len(rows) != 1 {
			t.Errorf("q=%q matched %d rows, want 1", q, len(rows))
			continue
		}
		row := rows[0].(map[string]any)
		if int(row["order_id"].(float64)) != orderID {
			t.Errorf("q=%q led to order %v, want %d", q, row["order_id"], orderID)
		}
		// 付款單號、金額、時間 — the three things manual reconciliation needs.
		if row["provider_transaction_id"] != "2412311225437371" || row["amount"] != float64(3400) || row["paid_at"] == nil {
			t.Errorf("q=%q: the row is missing something reconciliation needs: %+v", q, row)
		}
	}
}

func TestShopPaymentsLookupWithoutQIsAPaginatedLedger(t *testing.T) {
	e := newPaymentEnv(t)
	for i := 0; i < 2; i++ {
		orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 1000)
		e.initiateECPay(t, e.memberA1, e.shopA, e.shopADomain, orderID)
	}
	code, listed := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, "/payments"), nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if listed["total"] != float64(2) || listed["page"] != float64(1) || listed["page_size"] != float64(20) {
		t.Fatalf("pagination metadata = %+v", listed)
	}
}

func TestShopPaymentsLookupRequiresPaymentView(t *testing.T) {
	e := newPaymentEnv(t)
	if code, _ := e.adminCall(t, e.viewerA, "GET", e.shopPath(e.shopA, "/payments"), nil); code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 without payment.view", code)
	}
}

func TestShopPaymentsLookupCrossShopIsRefused(t *testing.T) {
	e := newPaymentEnv(t)
	// shop B's owner has payment.view in shop B only.
	if code, _ := e.adminCall(t, e.ownerB, "GET", e.shopPath(e.shopA, "/payments"), nil); code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 from the cross-shop guard", code)
	}
}

// TestShopPaymentsLookupCannotSeeAnotherShopsReference — having access to shop B
// plus a valid shop A reference still yields nothing. Saying "no rows" leaks
// less than saying "it exists but is not yours", and a list endpoint answering
// "no rows" is not an anomaly.
func TestShopPaymentsLookupCannotSeeAnotherShopsReference(t *testing.T) {
	e := newPaymentEnv(t)
	orderID := e.checkoutOrder(t, e.shopA, e.memberA1, e.shopADomain, 3400)
	body := e.initiateECPay(t, e.memberA1, e.shopA, e.shopADomain, orderID)
	ref := body["provider_reference"].(string)

	code, listed := e.adminCallJSON(t, e.ownerB, "GET",
		e.shopPath(e.shopB, "/payments?q="+url.QueryEscape(ref)), nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if rows := listed["payments"].([]any); len(rows) != 0 {
		t.Fatalf("shop B saw %d of shop A's payments", len(rows))
	}
}
