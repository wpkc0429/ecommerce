package httpapi_test

// Integration coverage for change order-notification-email at the HTTP level.
//
// The unit and service-level assertions (what the message says, what the dedupe
// table arbitrates) live in internal/notify. What can only be observed from
// here is the *chain*: a real checkout request → events.OrderPlaced published
// from inside order.Service.Checkout → exactly one email; and the paths that
// create no order producing no email — including the Idempotency-Key replay,
// which is the whole reason the publish point is inside Checkout rather than in
// these two handlers (design D6/D15).
//
// It reuses guestEnv, which now wires a notify.Service with a RecordingSender.
// **No test here opens a network connection.**

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ksdevworks/ecommerce/api/internal/notify"
	"ksdevworks/ecommerce/api/internal/payment"
)

func (e *guestEnv) emails(t *testing.T) []notify.Message {
	t.Helper()
	return e.notifySender.Messages()
}

// TestGuestCheckoutSendsExactlyOneConfirmation — the case this change exists
// for. A guest has no account and no order list; if this email does not arrive,
// a lost HTTP response leaves the customer with nothing at all while the order
// and the stock deduction are real.
func TestGuestCheckoutSendsExactlyOneConfirmation(t *testing.T) {
	e := newGuestEnv(t)
	sku := e.newSKU(t, e.shopA, 3400, "TWD", 10)

	code, body := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		guestBody("notify-guest@t.dev", guestItem(sku, 2)))
	if code != 201 {
		t.Fatalf("checkout status = %d: %+v", code, body)
	}

	msgs := e.emails(t)
	if len(msgs) != 1 {
		t.Fatalf("sent %d emails, want 1", len(msgs))
	}
	if msgs[0].To != "notify-guest@t.dev" {
		t.Errorf("recipient = %q, want the buyer's address", msgs[0].To)
	}
	if !strings.Contains(msgs[0].Subject, "訂單成立") {
		t.Errorf("subject = %q", msgs[0].Subject)
	}
	// Amounts, unscaled and composed, straight off the row checkout wrote.
	for _, want := range []string{"商品小計：NT$6,800", "運費：NT$0", "應付總額：NT$6,800"} {
		if !strings.Contains(msgs[0].TextBody, want) {
			t.Errorf("body is missing %q:\n%s", want, msgs[0].TextBody)
		}
	}
}

// TestMemberCheckoutSendsExactlyOneConfirmation — the member path publishes the
// same event from the same method, so it gets the same email. Two entry points,
// one implementation.
func TestMemberCheckoutSendsExactlyOneConfirmation(t *testing.T) {
	e := newGuestEnv(t)
	sku := e.newSKU(t, e.shopA, 1200, "TWD", 5)

	code, body := e.memberTokenCall(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": sku, "quantity": 1})
	if code != 201 && code != 200 {
		t.Fatalf("add to cart status = %d: %+v", code, body)
	}
	code, body = e.memberTokenCall(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout",
		map[string]any{"recipient_name": "王小明", "phone": "0912345678", "line1": "台北市"})
	if code != 201 {
		t.Fatalf("checkout status = %d: %+v", code, body)
	}

	msgs := e.emails(t)
	if len(msgs) != 1 {
		t.Fatalf("sent %d emails, want 1", len(msgs))
	}
	if msgs[0].To != "guest-member-a1@t.dev" {
		t.Errorf("recipient = %q", msgs[0].To)
	}
}

// TestFailedCheckoutSendsNothing — every path that does not create an order must
// not reach the publish, because a "your order is placed" email for an order
// that was never placed is worse than any missing email.
func TestFailedCheckoutSendsNothing(t *testing.T) {
	e := newGuestEnv(t)
	sku := e.newSKU(t, e.shopA, 3400, "TWD", 1)

	// Insufficient stock: the transaction rolls back after the cart flip.
	code, _ := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		guestBody("notify-oos@t.dev", guestItem(sku, 5)))
	if code != 422 {
		t.Fatalf("insufficient stock status = %d, want 422", code)
	}
	// A malformed address: rejected before anything is written.
	bad := guestBody("notify-bad@t.dev", guestItem(sku, 1))
	bad["shipping_address"] = map[string]any{"recipient_name": ""}
	if code, _ := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout", bad); code != 422 {
		t.Fatalf("invalid address status = %d, want 422", code)
	}

	if got := len(e.emails(t)); got != 0 {
		t.Fatalf("failed checkouts sent %d emails, want 0", got)
	}
}

// TestIdempotentReplayDoesNotEmailTwice — **the assertion that pins design D15.**
//
// A replay returns 201 with a body indistinguishable from the first response, so
// nothing on the wire says "this was not a new order". The only thing that keeps
// the customer from receiving a second 訂單成立 email is that the replay path
// never enters order.Service.Checkout. Publishing from the handlers instead
// would have made this an `if` statement someone has to keep correct forever.
func TestIdempotentReplayDoesNotEmailTwice(t *testing.T) {
	e := newGuestEnv(t)
	sku := e.newSKU(t, e.shopA, 3400, "TWD", 10)
	body := guestBody("notify-replay@t.dev", guestItem(sku, 1))
	key := "notify-replay-key-0001"

	code, first, _ := e.guestIdem(t, key, body)
	if code != 201 {
		t.Fatalf("first checkout status = %d: %+v", code, first)
	}
	code, second, hdr := e.guestIdem(t, key, body)
	if code != 201 || hdr.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay status = %d replayed=%q", code, hdr.Get("Idempotency-Replayed"))
	}
	if orderIDOf(t, first) != orderIDOf(t, second) {
		t.Fatal("replay returned a different order")
	}

	if got := len(e.emails(t)); got != 1 {
		t.Fatalf("a replayed checkout sent %d emails, want 1", got)
	}
}

// TestPaymentWebhookSendsOneEmailEvenWhenRedelivered — the other half of design
// D6. payment.Service republishes OrderPaymentSucceeded on every delivery that
// re-confirms an already-succeeded payment (its own doc comment says so), and
// 綠界 redelivers up to four times a day. Here that is exercised through the real
// webhook route rather than by calling the service.
func TestPaymentWebhookSendsOneEmailEvenWhenRedelivered(t *testing.T) {
	e := newGuestEnv(t)
	sku := e.newSKU(t, e.shopA, 3400, "TWD", 10)

	code, body := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
		guestBody("notify-paid@t.dev", guestItem(sku, 1)))
	if code != 201 {
		t.Fatalf("checkout status = %d: %+v", code, body)
	}
	token, _ := body["order_access_token"].(string)
	code, pay := e.tokenCall(t, token, e.shopADomain, "POST", "/api/v1/shop/guest/order/payments", nil)
	if code != 201 {
		t.Fatalf("initiate payment status = %d: %+v", code, pay)
	}
	ref, _ := pay["provider_reference"].(string)
	if ref == "" {
		t.Fatalf("no provider_reference: %+v", pay)
	}

	// Three identical deliveries, exactly as a retrying provider produces.
	for i := range 3 {
		if code := e.deliverMockWebhook(t, ref, "succeeded"); code != 200 {
			t.Fatalf("webhook delivery %d status = %d", i+1, code)
		}
	}

	var placed, paid int
	for _, m := range e.emails(t) {
		switch {
		case strings.Contains(m.Subject, "訂單成立"):
			placed++
		case strings.Contains(m.Subject, "付款成功"):
			paid++
		default:
			t.Fatalf("unexpected email subject %q", m.Subject)
		}
	}
	if placed != 1 || paid != 1 {
		t.Fatalf("emails: 訂單成立=%d 付款成功=%d, want 1 and 1", placed, paid)
	}
}

// TestCancelledOrderPaidAnywaySendsNoPaymentEmail — change
// ecpay-payment-provider (design D9) established that a customer can pay while
// still on the cashier page after the order was cancelled: the payment row is
// recorded as succeeded (the money really moved), but the ORDER is not advanced
// and no loyalty event is published.
//
// That branch is now also what keeps us from telling the customer "付款成功" for
// an order that will never ship — a message that would be simultaneously true
// (they were charged) and misleading (they get nothing), and which would make a
// manual refund harder to explain, not easier.
func TestCancelledOrderPaidAnywaySendsNoPaymentEmail(t *testing.T) {
	e := newGuestEnv(t)
	sku := e.newSKU(t, e.shopA, 3400, "TWD", 5)

	code, body := e.memberTokenCall(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": sku, "quantity": 1})
	if code != 201 && code != 200 {
		t.Fatalf("add to cart status = %d: %+v", code, body)
	}
	code, body = e.memberTokenCall(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout",
		map[string]any{"recipient_name": "王小明", "phone": "0912345678", "line1": "台北市"})
	if code != 201 {
		t.Fatalf("checkout status = %d: %+v", code, body)
	}
	orderID := int(body["id"].(float64))

	code, pay := e.memberTokenCall(t, e.memberA1, e.shopA, e.shopADomain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/payments", orderID), nil)
	if code != 201 {
		t.Fatalf("initiate payment status = %d: %+v", code, pay)
	}
	ref, _ := pay["provider_reference"].(string)

	// Cancelled while the customer is still on the cashier page.
	if code, cancelled := e.memberTokenCall(t, e.memberA1, e.shopA, e.shopADomain, "POST",
		fmt.Sprintf("/api/v1/shop/orders/%d/cancel", orderID), nil); code != 200 {
		t.Fatalf("cancel status = %d: %+v", code, cancelled)
	}
	if code := e.deliverMockWebhook(t, ref, "succeeded"); code != 200 {
		t.Fatalf("webhook status = %d", code)
	}

	for _, m := range e.emails(t) {
		if strings.Contains(m.Subject, "付款成功") {
			t.Fatalf("a cancelled order produced a payment confirmation email: %q", m.Subject)
		}
	}
	// The confirmation for the order itself was still correct when it was sent,
	// and is deliberately not retracted (design D2: no cancellation email).
	if got := len(e.emails(t)); got != 1 {
		t.Fatalf("sent %d emails, want just the 訂單成立 one", got)
	}
}

// deliverMockWebhook posts one signed mock-provider webhook, the same way the
// payment integration tests do.
func (e *guestEnv) deliverMockWebhook(t *testing.T, ref, status string) int {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"provider_reference": ref, "status": status})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/payments/mock", bytes.NewReader(payload))
	req.Header.Set("X-Payment-Signature", payment.SignMockWebhook(e.webhookSecret, payload))
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec.Code
}
