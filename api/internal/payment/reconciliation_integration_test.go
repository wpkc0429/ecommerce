package payment_test

// change ecpay-payment-provider: the behaviours the first REAL payment provider
// forced into payment.Service, plus the one pre-existing hole finding them
// uncovered.
//
// Every test here uses the mock provider deliberately: none of this is
// 綠界-specific. The provider-neutral layer either has these properties or it
// does not, and the next provider inherits whichever answer it finds.

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/payment"
)

// captureEvents returns a Service whose dispatcher records every
// OrderPaymentSucceeded, plus the slice it records into.
func captureEvents(f *paymentFixtures) (*payment.Service, *[]events.OrderPaymentSucceeded) {
	var captured []events.OrderPaymentSucceeded
	d := events.NewDispatcher(slog.New(slog.DiscardHandler))
	d.Subscribe(func(_ context.Context, e events.Event) {
		if ev, ok := e.(events.OrderPaymentSucceeded); ok {
			captured = append(captured, ev)
		}
	})
	svc := newTestService(f)
	svc.Dispatcher = d
	svc.Log = slog.New(slog.DiscardHandler)
	return svc, &captured
}

// ── design D6: the third Outcome ───────────────────────────────────────

// TestHandleWebhookPendingOutcomeLeavesTheRowClaimable is the whole reason
// payment.Outcome grew a third value.
//
// 綠界 has two notifications that look like a result but are not one: its
// back-office "模擬付款" button (RtnCode=1, RtnMsg=交易成功, SimulatePaid=1, and no
// money settled) and RtnCode=10300066 (交易付款結果待確認中，請勿出貨). With only
// Succeeded/Failed available, both had to be mapped to one of them:
//
//   - as Succeeded → an order is marked paid, points are awarded, and it enters
//     the shipping queue, with nothing in the bank;
//   - as Failed → the payment row goes terminal, and the REAL success
//     notification that follows loses the CAS and can never advance the order.
//     The money arrives, the order stays unpaid, and nothing errors.
//
// This test walks the second sequence and asserts the escape hatch works.
func TestHandleWebhookPendingOutcomeLeavesTheRowClaimable(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := f.newMember(t, "pending@t.dev")
	ord := f.newOrder(t, memberID, 3400)
	svc, captured := captureEvents(f)
	ctx := context.Background()

	_, res, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}

	// Delivery 1: non-terminal.
	p, err := svc.HandleWebhook(ctx, "mock", 0, &payment.WebhookResult{
		ProviderReference: res.ProviderReference, Outcome: payment.OutcomePending,
		ProviderTransactionID: "2412311225437371",
	})
	if err != nil {
		t.Fatalf("a non-terminal delivery must be accepted, not rejected: %v", err)
	}
	if p.Status != payment.StatusPending {
		t.Fatalf("payment status = %d after a non-terminal delivery, must still be pending", p.Status)
	}
	got, err := f.orders.GetOrderAdmin(ctx, f.shopID, ord.ID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if got.PaymentStatus != order.PaymentStatusUnpaid {
		t.Fatalf("order payment_status = %d; a notification that settled no money must not mark it paid",
			got.PaymentStatus)
	}
	if len(*captured) != 0 {
		t.Fatalf("a non-terminal delivery published %d loyalty events; it must publish none", len(*captured))
	}
	// The provider's own identifier IS recorded, even on a non-terminal
	// delivery — that number is exactly what the merchant reconciles with.
	if derefStr(p.ProviderTransactionID) != "2412311225437371" {
		t.Errorf("provider_transaction_id = %q, want it recorded from the first delivery that carried one",
			derefStr(p.ProviderTransactionID))
	}

	// Delivery 2: the real thing. It can only succeed because delivery 1 left
	// the row pending.
	if _, err := svc.HandleWebhook(ctx, "mock", 0, &payment.WebhookResult{
		ProviderReference: res.ProviderReference, Outcome: payment.OutcomeSucceeded,
	}); err != nil {
		t.Fatalf("second HandleWebhook: %v", err)
	}
	got, err = f.orders.GetOrderAdmin(ctx, f.shopID, ord.ID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if got.PaymentStatus != payment.OrderPaymentStatusPaid {
		t.Fatalf("order payment_status = %d after the real success notification, want paid", got.PaymentStatus)
	}
	if len(*captured) != 1 {
		t.Fatalf("expected exactly one loyalty event, got %d", len(*captured))
	}
}

// ── design D7: the two systems must agree on the amount ────────────────

func TestHandleWebhookRejectsAnAmountThatDisagrees(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := f.newMember(t, "amount@t.dev")
	ord := f.newOrder(t, memberID, 3400)
	svc, captured := captureEvents(f)
	ctx := context.Background()

	_, res, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}

	_, err = svc.HandleWebhook(ctx, "mock", 0, &payment.WebhookResult{
		ProviderReference: res.ProviderReference, Outcome: payment.OutcomeSucceeded,
		Amount: 34, // the classic off-by-100
	})
	if !errors.Is(err, payment.ErrAmountMismatch) {
		t.Fatalf("err = %v, want ErrAmountMismatch — a disagreement about money must ring, not resolve silently", err)
	}
	got, err := f.orders.GetOrderAdmin(ctx, f.shopID, ord.ID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if got.PaymentStatus != order.PaymentStatusUnpaid {
		t.Errorf("order was advanced despite the amount mismatch")
	}
	if len(*captured) != 0 {
		t.Errorf("loyalty events were published despite the amount mismatch")
	}
}

func TestHandleWebhookAcceptsAMatchingAmount(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := f.newMember(t, "amount-ok@t.dev")
	ord := f.newOrder(t, memberID, 3400)
	svc := newTestService(f)
	ctx := context.Background()

	_, res, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}
	p, err := svc.HandleWebhook(ctx, "mock", 0, &payment.WebhookResult{
		ProviderReference: res.ProviderReference, Outcome: payment.OutcomeSucceeded, Amount: 3400,
	})
	if err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if p.Status != payment.StatusSucceeded {
		t.Fatalf("payment status = %d, want succeeded", p.Status)
	}
}

// ── design D4/D12: the two facts reconciliation needs ──────────────────

func TestHandleWebhookRecordsProviderTransactionIDAndPaidAt(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := f.newMember(t, "facts@t.dev")
	ord := f.newOrder(t, memberID, 3400)
	svc := newTestService(f)
	ctx := context.Background()

	_, res, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}
	paidAt := time.Date(2026, 8, 1, 12, 26, 9, 0, time.UTC)
	p, err := svc.HandleWebhook(ctx, "mock", 0, &payment.WebhookResult{
		ProviderReference: res.ProviderReference, Outcome: payment.OutcomeSucceeded,
		ProviderTransactionID: "2412311225437371", PaidAt: &paidAt,
	})
	if err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if derefStr(p.ProviderTransactionID) != "2412311225437371" {
		t.Errorf("provider_transaction_id = %q", derefStr(p.ProviderTransactionID))
	}
	if p.PaidAt == nil || !p.PaidAt.Equal(paidAt) {
		t.Errorf("paid_at = %v, want the provider's own stamp %v", p.PaidAt, paidAt)
	}

	// A later redelivery carrying nothing must not erase what the first one
	// established.
	p, err = svc.HandleWebhook(ctx, "mock", 0, &payment.WebhookResult{
		ProviderReference: res.ProviderReference, Outcome: payment.OutcomeSucceeded,
	})
	if err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if derefStr(p.ProviderTransactionID) != "2412311225437371" || p.PaidAt == nil {
		t.Error("a redelivery without these fields erased them; the first delivery that carried a value is authoritative")
	}
}

// TestHandleWebhookLeavesProviderFactsNullWhenNotReported — NULL is the honest
// answer for a provider that has only one identifier and reports no time, and
// for every row that predates this change.
func TestHandleWebhookLeavesProviderFactsNullWhenNotReported(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := f.newMember(t, "no-facts@t.dev")
	ord := f.newOrder(t, memberID, 3400)
	svc := newTestService(f)
	ctx := context.Background()

	_, res, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}
	p, err := svc.HandleWebhook(ctx, "mock", 0, &payment.WebhookResult{
		ProviderReference: res.ProviderReference, Outcome: payment.OutcomeSucceeded,
	})
	if err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if p.ProviderTransactionID != nil || p.PaidAt != nil {
		t.Fatalf("want both NULL for a provider that reports neither, got %v / %v",
			p.ProviderTransactionID, p.PaidAt)
	}
}

// ── design D9: the hole a cancelled order used to open ─────────────────

// TestCancelledOrderIsNeverAdvancedToPaid is the regression test for a bug that
// existed before this change and was reproduced with the mock provider.
//
// Measured behaviour before the fix (verbatim from the probe run):
//
//	order cancelled: status=1 payment_status=0
//	RESULT payment.status=1 order.status=1 order.payment_status=1 events=1
//	  event: order.payment_succeeded {ShopID:1 OrderID:1 MemberID:1 AwardBasisAmount:5000}
//
// i.e. an order that was BOTH cancelled and paid, whose stock had already been
// returned, and whose customer had been awarded loyalty points for it — with
// nothing anywhere reporting a problem.
func TestCancelledOrderIsNeverAdvancedToPaid(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := f.newMember(t, "cancelled@t.dev")
	ord := f.newOrder(t, memberID, 5000)
	svc, captured := captureEvents(f)
	ctx := context.Background()

	_, res, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}
	if _, err := f.orders.CancelOrder(ctx, f.shopID, ord.ID, nil); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}

	// The customer finishes paying on the provider's page anyway.
	p, err := svc.HandleWebhook(ctx, "mock", 0, &payment.WebhookResult{
		ProviderReference: res.ProviderReference, Outcome: payment.OutcomeSucceeded,
	})
	if err != nil {
		t.Fatalf("HandleWebhook must accept it — the money really moved: %v", err)
	}

	// The money is recorded truthfully. Marking it anything else would be lying
	// in the financial trail to keep a status table tidy.
	if p.Status != payment.StatusSucceeded {
		t.Errorf("payment status = %d, want succeeded — the customer really paid", p.Status)
	}
	got, err := f.orders.GetOrderAdmin(ctx, f.shopID, ord.ID)
	if err != nil {
		t.Fatalf("GetOrderAdmin: %v", err)
	}
	if got.Status != order.StatusCancelled {
		t.Errorf("order status = %d, want still cancelled", got.Status)
	}
	if got.PaymentStatus != order.PaymentStatusUnpaid {
		t.Errorf("order payment_status = %d — a cancelled order must never become paid; "+
			"its stock is already back on the shelf", got.PaymentStatus)
	}
	if len(*captured) != 0 {
		t.Errorf("%d loyalty events published for a cancelled order; an order that will never "+
			"ship must earn no points", len(*captured))
	}
}

// TestPaidOrderCannotBeCancelled records the other half of what the
// reconciliation question turned up (design D8, finding 1): the scenario the
// question assumed — "cancel an order that has been paid" — cannot happen.
// order.Service.CancelOrder's atomic conditional update requires all three
// status axes to still be at their initial value.
func TestPaidOrderCannotBeCancelled(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := f.newMember(t, "paid-cancel@t.dev")
	ord := f.newOrder(t, memberID, 7000)
	svc := newTestService(f)
	ctx := context.Background()

	_, res, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}
	if _, err := svc.HandleWebhook(ctx, "mock", 0, &payment.WebhookResult{
		ProviderReference: res.ProviderReference, Outcome: payment.OutcomeSucceeded,
	}); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}

	_, err = f.orders.CancelOrder(ctx, f.shopID, ord.ID, nil)
	var ce *order.ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v (%T), want a ConflictError — a paid order is not cancellable", err, err)
	}
}

// TestCancelledOrderRetainsPaymentTrail is the direct evidence for design D8:
// **cancelling an order takes nothing away from the financial record.**
//
// The reconciliation question was "is what survives a cancellation enough to
// reconcile by hand" — i.e. can the merchant still get the transaction number,
// the amount, and the time. This asserts each of them against the values
// captured before the cancellation.
func TestCancelledOrderRetainsPaymentTrail(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := f.newMember(t, "trail@t.dev")
	ord := f.newOrder(t, memberID, 4400)
	svc := newTestService(f)
	svc.Log = slog.New(slog.DiscardHandler)
	ctx := context.Background()

	before, res, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}
	if _, err := f.orders.CancelOrder(ctx, f.shopID, ord.ID, nil); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}

	rows, err := svc.ListForOrderAdmin(ctx, f.shopID, ord.ID)
	if err != nil {
		t.Fatalf("ListForOrderAdmin after cancellation: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("payment rows after cancellation = %d, want 1 — cancelling must not delete "+
			"the financial trail", len(rows))
	}
	after := rows[0]
	switch {
	case after.ProviderReference != before.ProviderReference || after.ProviderReference != res.ProviderReference:
		t.Errorf("provider_reference changed: %q → %q", before.ProviderReference, after.ProviderReference)
	case after.Amount != 4400:
		t.Errorf("amount = %d, want 4400", after.Amount)
	case after.Currency != "TWD":
		t.Errorf("currency = %q", after.Currency)
	// Postgres stores microseconds; the value Go held in memory before the
	// round trip has nanoseconds. Compare at the precision the column has.
	case !after.CreatedAt.Truncate(time.Microsecond).Equal(before.CreatedAt.Truncate(time.Microsecond)):
		t.Errorf("created_at changed: %v → %v", before.CreatedAt, after.CreatedAt)
	case after.Provider != before.Provider:
		t.Errorf("provider changed: %q → %q", before.Provider, after.Provider)
	}
}

// ── design D10: the lookup manual reconciliation actually starts from ──

func TestListAdminFindsAPaymentByEitherIdentifier(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := f.newMember(t, "lookup@t.dev")
	ord := f.newOrder(t, memberID, 3400)
	svc := newTestService(f)
	ctx := context.Background()

	_, res, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}
	if _, err := svc.HandleWebhook(ctx, "mock", 0, &payment.WebhookResult{
		ProviderReference:     res.ProviderReference,
		Outcome:               payment.OutcomeSucceeded,
		ProviderTransactionID: "2412311225437371",
	}); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}

	// The merchant is holding one of these three numbers; all of them must lead
	// to the same order.
	for _, q := range []string{
		res.ProviderReference,  // ours (綠界's MerchantTradeNo)
		"2412311225437371",     // theirs (綠界's TradeNo)
		"  2412311225437371  ", // pasted with whitespace
		itoa(ord.ID),           // the order number, from a phone call
	} {
		page, err := svc.ListAdmin(ctx, f.shopID, payment.AdminListParams{Q: q})
		if err != nil {
			t.Fatalf("ListAdmin(%q): %v", q, err)
		}
		if len(page.Payments) != 1 {
			t.Errorf("q=%q matched %d payments, want 1", q, len(page.Payments))
			continue
		}
		if page.Payments[0].OrderID != ord.ID {
			t.Errorf("q=%q led to order %d, want %d", q, page.Payments[0].OrderID, ord.ID)
		}
		if page.Payments[0].Amount != 3400 {
			t.Errorf("q=%q: amount = %d, want 3400", q, page.Payments[0].Amount)
		}
	}
}

func TestListAdminIsScopedToTheShop(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := f.newMember(t, "scope@t.dev")
	ord := f.newOrder(t, memberID, 3400)
	svc := newTestService(f)
	ctx := context.Background()

	_, res, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}

	other := f.client.Shop.Create().SetName("Other Shop").SetStatus(1).SaveX(ctx)
	page, err := svc.ListAdmin(ctx, other.ID, payment.AdminListParams{Q: res.ProviderReference})
	if err != nil {
		t.Fatalf("ListAdmin: %v", err)
	}
	if len(page.Payments) != 0 {
		t.Fatalf("another shop's reference matched %d rows; the shop predicate is unconditional "+
			"so a guessed reference must reach nothing", len(page.Payments))
	}
}

func TestListAdminWithoutQIsAPaginatedLedger(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := f.newMember(t, "ledger@t.dev")
	svc := newTestService(f)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		ord := f.newOrder(t, memberID, 1000)
		if _, _, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, ""); err != nil {
			t.Fatalf("InitiatePayment: %v", err)
		}
	}

	page, err := svc.ListAdmin(ctx, f.shopID, payment.AdminListParams{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("ListAdmin: %v", err)
	}
	if page.Total != 3 || len(page.Payments) != 2 {
		t.Fatalf("total=%d len=%d, want 3 and 2", page.Total, len(page.Payments))
	}
	// Explicit order, never the database's choice.
	if page.Payments[0].ID < page.Payments[1].ID {
		t.Errorf("the ledger must be ordered by id descending, got %d then %d",
			page.Payments[0].ID, page.Payments[1].ID)
	}
}

// TestListAdminFindsThePaymentThatNeedsAManualRefund closes the loop between
// design D9 and D10: the state the fix deliberately leaves behind (money taken,
// order cancelled) is a state the merchant can always find.
func TestListAdminFindsThePaymentThatNeedsAManualRefund(t *testing.T) {
	f := setupPaymentFixtures(t)
	memberID := f.newMember(t, "orphan@t.dev")
	ord := f.newOrder(t, memberID, 5000)
	svc, _ := captureEvents(f)
	ctx := context.Background()

	_, res, err := svc.InitiatePayment(ctx, f.shopID, memberID, ord.ID, "")
	if err != nil {
		t.Fatalf("InitiatePayment: %v", err)
	}
	if _, err := f.orders.CancelOrder(ctx, f.shopID, ord.ID, nil); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if _, err := svc.HandleWebhook(ctx, "mock", 0, &payment.WebhookResult{
		ProviderReference: res.ProviderReference, Outcome: payment.OutcomeSucceeded,
		ProviderTransactionID: "2412311225437371",
	}); err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}

	page, err := svc.ListAdmin(ctx, f.shopID, payment.AdminListParams{Q: "2412311225437371"})
	if err != nil {
		t.Fatalf("ListAdmin: %v", err)
	}
	if len(page.Payments) != 1 {
		t.Fatalf("the merchant cannot find the transaction they have to refund by hand")
	}
	if page.Payments[0].Status != payment.StatusSucceeded {
		t.Errorf("status = %d, want succeeded — the money really moved", page.Payments[0].Status)
	}
	if page.Payments[0].OrderID != ord.ID {
		t.Errorf("order_id = %d, want %d — the merchant needs to reach the order from here",
			page.Payments[0].OrderID, ord.ID)
	}
}

// ── helpers ────────────────────────────────────────────────────────────

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func itoa(i int) string { return strconv.Itoa(i) }
