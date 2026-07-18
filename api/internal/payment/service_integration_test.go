package payment_test

import (
	"context"
	"encoding/json"
	"testing"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/payment"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// paymentFixtures exercises payment.Service directly (no HTTP/JWT/RBAC
// layer) to pin down the CAS/idempotency state machine in HandleWebhook
// (design D6) — the httpapi-level tests (payment_integration_test.go) cover
// the same guarantees end-to-end through signature verification and the
// wire format; this file is where the state-machine branches themselves are
// exhaustively exercised.
type paymentFixtures struct {
	client *ent.Client
	orders *order.Service
	shopID int
}

func setupPaymentFixtures(t *testing.T) *paymentFixtures {
	t.Helper()
	client := testutil.OpenDB(t)
	ctx := context.Background()

	shop := client.Shop.Create().SetName("Payment Test Shop").SetStatus(1).SaveX(ctx)

	return &paymentFixtures{
		client: client,
		orders: &order.Service{Client: client},
		shopID: shop.ID,
	}
}

// newOrder creates a fresh unpaid, uncancelled order directly via ent
// (bypassing checkout — this package only needs an order to exist, not a
// cart/checkout flow).
func (f *paymentFixtures) newOrder(t *testing.T, memberID int, totalAmount int64) *ent.Order {
	t.Helper()
	ctx := context.Background()
	return f.client.Order.Create().
		SetShopID(f.shopID).SetMemberID(memberID).
		SetStatus(order.StatusCreated).
		SetPaymentStatus(order.PaymentStatusUnpaid).
		SetFulfillmentStatus(0).
		SetCurrency("TWD").
		SetTotalAmount(totalAmount).
		SetShippingAddress(json.RawMessage(`{}`)).
		SaveX(ctx)
}

func newMember(t *testing.T, client *ent.Client, email string) int {
	t.Helper()
	return client.Member.Create().SetEmail(email).SetStatus(1).SaveX(context.Background()).ID
}

const testWebhookSecret = "svc-test-webhook-secret"

func newTestService(f *paymentFixtures) *payment.Service {
	mock := payment.NewMockProvider(testWebhookSecret)
	return &payment.Service{
		Client:          f.client,
		Orders:          f.orders,
		Providers:       map[string]payment.Provider{mock.Name(): mock},
		DefaultProvider: mock.Name(),
	}
}

func TestServiceInitiatePaymentAndWebhookSuccessFlow(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := newMember(t, f.client, "flow@t.dev")
	ord := f.newOrder(t, memberID, 5000)
	svc := newTestService(f)
	ctx := context.Background()

	p, res, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}
	if p.Status != payment.StatusPending {
		t.Fatalf("expected pending status, got %d", p.Status)
	}
	if p.Amount != 5000 || p.Currency != "TWD" {
		t.Fatalf("expected payment to snapshot order amount/currency, got %d %s", p.Amount, p.Currency)
	}
	if res.ProviderReference == "" {
		t.Fatal("expected a provider reference")
	}

	updated, err := svc.HandleWebhook(ctx, "mock", &payment.WebhookResult{
		ProviderReference: res.ProviderReference, Outcome: payment.OutcomeSucceeded,
	})
	if err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if updated.Status != payment.StatusSucceeded {
		t.Fatalf("expected succeeded status, got %d", updated.Status)
	}

	gotOrder, err := f.orders.GetOrderAdmin(ctx, f.shopID, ord.ID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.PaymentStatus != payment.OrderPaymentStatusPaid {
		t.Fatalf("expected order payment_status=paid, got %d", gotOrder.PaymentStatus)
	}
}

func TestServiceHandleWebhookFailedDoesNotTouchOrder(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := newMember(t, f.client, "failed@t.dev")
	ord := f.newOrder(t, memberID, 2000)
	svc := newTestService(f)
	ctx := context.Background()

	_, res, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}

	updated, err := svc.HandleWebhook(ctx, "mock", &payment.WebhookResult{
		ProviderReference: res.ProviderReference, Outcome: payment.OutcomeFailed,
	})
	if err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if updated.Status != payment.StatusFailed {
		t.Fatalf("expected failed status, got %d", updated.Status)
	}

	gotOrder, err := f.orders.GetOrderAdmin(ctx, f.shopID, ord.ID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.PaymentStatus != order.PaymentStatusUnpaid {
		t.Fatalf("expected order to remain unpaid after a failed payment, got %d", gotOrder.PaymentStatus)
	}

	// Retry: member can initiate a second payment attempt on the same order.
	p2, _, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("retry InitiatePayment: %v", err)
	}
	if p2.OrderID != ord.ID {
		t.Fatalf("expected retry payment to reference the same order")
	}
}

func TestServiceHandleWebhookDuplicateSucceededIsSafeNoOp(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := newMember(t, f.client, "dup@t.dev")
	ord := f.newOrder(t, memberID, 3000)
	svc := newTestService(f)
	ctx := context.Background()

	_, res, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}
	wh := &payment.WebhookResult{ProviderReference: res.ProviderReference, Outcome: payment.OutcomeSucceeded}

	if _, err := svc.HandleWebhook(ctx, "mock", wh); err != nil {
		t.Fatalf("first HandleWebhook: %v", err)
	}
	// Deliver the exact same successful webhook a second time.
	second, err := svc.HandleWebhook(ctx, "mock", wh)
	if err != nil {
		t.Fatalf("second HandleWebhook (must be a safe no-op, not an error): %v", err)
	}
	if second.Status != payment.StatusSucceeded {
		t.Fatalf("expected status to remain succeeded, got %d", second.Status)
	}

	gotOrder, err := f.orders.GetOrderAdmin(ctx, f.shopID, ord.ID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.PaymentStatus != payment.OrderPaymentStatusPaid {
		t.Fatalf("expected order payment_status=paid after duplicate delivery, got %d", gotOrder.PaymentStatus)
	}
}

// TestServiceHandleWebhookConflictingSucceededAfterFailedIsIgnored proves the
// defensive branch in design D6: once a payment has terminally settled to
// "failed", a later webhook claiming "succeeded" for the same reference
// (a conflicting/confused redelivery) MUST NOT resurrect it into paid.
func TestServiceHandleWebhookConflictingSucceededAfterFailedIsIgnored(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := newMember(t, f.client, "conflict@t.dev")
	ord := f.newOrder(t, memberID, 4000)
	svc := newTestService(f)
	ctx := context.Background()

	_, res, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}

	if _, err := svc.HandleWebhook(ctx, "mock", &payment.WebhookResult{
		ProviderReference: res.ProviderReference, Outcome: payment.OutcomeFailed,
	}); err != nil {
		t.Fatalf("HandleWebhook (failed): %v", err)
	}

	// A later, conflicting "succeeded" redelivery for the same reference.
	conflicting, err := svc.HandleWebhook(ctx, "mock", &payment.WebhookResult{
		ProviderReference: res.ProviderReference, Outcome: payment.OutcomeSucceeded,
	})
	if err != nil {
		t.Fatalf("conflicting HandleWebhook must not error: %v", err)
	}
	if conflicting.Status != payment.StatusFailed {
		t.Fatalf("expected the payment to remain failed (CAS only allows pending->target), got %d", conflicting.Status)
	}

	gotOrder, err := f.orders.GetOrderAdmin(ctx, f.shopID, ord.ID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if gotOrder.PaymentStatus != order.PaymentStatusUnpaid {
		t.Fatalf("expected order to remain unpaid — a conflicting redelivery must never mark it paid, got %d", gotOrder.PaymentStatus)
	}
}

func TestServiceHandleWebhookUnknownReferenceIsErrNotFound(t *testing.T) {
	f := setupPaymentFixtures(t)
	svc := newTestService(f)
	_, err := svc.HandleWebhook(context.Background(), "mock", &payment.WebhookResult{
		ProviderReference: "mock_does-not-exist", Outcome: payment.OutcomeSucceeded,
	})
	if err != payment.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestServiceInitiatePaymentRejectsCancelledOrder(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := newMember(t, f.client, "cancelled@t.dev")
	ord := f.newOrder(t, memberID, 1000)
	svc := newTestService(f)
	ctx := context.Background()

	f.client.Order.UpdateOneID(ord.ID).SetStatus(order.StatusCancelled).SaveX(ctx)

	_, _, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	var ce *payment.ConflictError
	if err == nil {
		t.Fatal("expected an error")
	}
	if !asConflict(err, &ce) {
		t.Fatalf("expected *payment.ConflictError, got %T: %v", err, err)
	}
}

func TestServiceInitiatePaymentRejectsAlreadyPaidOrder(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := newMember(t, f.client, "paid@t.dev")
	ord := f.newOrder(t, memberID, 1000)
	svc := newTestService(f)
	ctx := context.Background()

	f.client.Order.UpdateOneID(ord.ID).SetPaymentStatus(payment.OrderPaymentStatusPaid).SaveX(ctx)

	_, _, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	var ce *payment.ConflictError
	if err == nil {
		t.Fatal("expected an error")
	}
	if !asConflict(err, &ce) {
		t.Fatalf("expected *payment.ConflictError, got %T: %v", err, err)
	}
}

func TestServiceInitiatePaymentCrossMemberIsNotFound(t *testing.T) {
	f := setupPaymentFixtures(t)
	ownerID := newMember(t, f.client, "owner@t.dev")
	otherID := newMember(t, f.client, "other@t.dev")
	ord := f.newOrder(t, ownerID, 1000)
	svc := newTestService(f)

	_, _, err := svc.InitiatePayment(context.Background(), f.shopID, otherID, ord.ID, "")
	if err != payment.ErrNotFound {
		t.Fatalf("expected ErrNotFound for cross-member access, got %v", err)
	}
}

func TestServiceInitiatePaymentRejectsUnknownProvider(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := newMember(t, f.client, "unknown-provider@t.dev")
	ord := f.newOrder(t, memberID, 1000)
	svc := newTestService(f)

	_, _, err := svc.InitiatePayment(context.Background(), f.shopID, memberID, ord.ID, "does-not-exist")
	var ve *payment.ValidationError
	if err == nil {
		t.Fatal("expected an error")
	}
	if !asValidation(err, &ve) {
		t.Fatalf("expected *payment.ValidationError, got %T: %v", err, err)
	}
}

func asConflict(err error, target **payment.ConflictError) bool {
	ce, ok := err.(*payment.ConflictError)
	if ok {
		*target = ce
	}
	return ok
}

func asValidation(err error, target **payment.ValidationError) bool {
	ve, ok := err.(*payment.ValidationError)
	if ok {
		*target = ve
	}
	return ok
}
