package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/ent"
	entnotification "ksdevworks/ecommerce/api/internal/ent/ordernotification"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/notify"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// These tests drive notify.Service through DeliverEvent — synchronously, start
// to finish (dedupe → load → render → send → record). The asynchrony is a
// property of Handle and gets its own two tests at the bottom; baking a worker
// pool into every content assertion would only make them flaky.
//
// **Nothing here opens a connection to anything but the test database.**
// RecordingSender is what makes that true, which is the job payment.MockProvider
// does for the webhook path.

type fixtures struct {
	client   *ent.Client
	sender   *notify.RecordingSender
	svc      *notify.Service
	shopID   int
	memberID int
}

func setup(t *testing.T) *fixtures {
	t.Helper()
	client := testutil.OpenDB(t)
	ctx := context.Background()

	shop := client.Shop.Create().SetName("ONDO").SetStatus(1).SaveX(ctx)
	member := client.Member.Create().SetShopID(shop.ID).SetEmail("buyer@example.com").SetStatus(1).SaveX(ctx)

	sender := &notify.RecordingSender{}
	return &fixtures{
		client: client, sender: sender,
		svc:      &notify.Service{Client: client, Sender: sender},
		shopID:   shop.ID,
		memberID: member.ID,
	}
}

// newOrder writes a realistic order row directly via ent — the same shortcut the
// points and shipping fixtures take. This package needs a real order to read,
// not the checkout that produced it.
func (f *fixtures) newOrder(t *testing.T) *ent.Order {
	t.Helper()
	ctx := context.Background()
	addr, err := json.Marshal(order.ShippingAddress{
		RecipientName: "王小明", Phone: "0912345678", Line1: "信義路五段7號", City: "台北市",
	})
	if err != nil {
		t.Fatal(err)
	}
	o := f.client.Order.Create().
		SetShopID(f.shopID).SetMemberID(f.memberID).
		SetStatus(order.StatusCreated).
		SetPaymentStatus(order.PaymentStatusUnpaid).
		SetFulfillmentStatus(order.FulfillmentStatusUnfulfilled).
		SetCurrency("TWD").
		SetSubtotalAmount(6800).SetDiscountAmount(680).SetShippingFee(120).SetTotalAmount(6240).
		SetMemberTierName("VIP").SetDiscountPercent(10).
		SetShippingAddress(addr).
		SaveX(ctx)
	f.client.OrderItem.Create().
		SetShopID(f.shopID).SetOrderID(o.ID).
		SetProductTitle("週年紀念花束").SetSkuCode("BQ-ANNI-M").
		SetQuantity(2).SetPriceAmount(3400).SetCurrency("TWD").SetGiftMessage("生日快樂").
		SaveX(ctx)
	return o
}

func (f *fixtures) rows(t *testing.T, orderID int) []*ent.OrderNotification {
	t.Helper()
	return f.client.OrderNotification.Query().
		Where(entnotification.OrderIDEQ(orderID)).
		Order(ent.Asc(entnotification.FieldID)).
		AllX(context.Background())
}

func placed(shopID, orderID, memberID int) events.OrderPlaced {
	return events.OrderPlaced{ShopID: shopID, OrderID: orderID, MemberID: memberID}
}

func paid(shopID, orderID, memberID int) events.OrderPaymentSucceeded {
	return events.OrderPaymentSucceeded{
		ShopID: shopID, OrderID: orderID, MemberID: memberID,
		AwardBasisAmount: 6120, Currency: "TWD",
	}
}

// ── the happy paths ────────────────────────────────────────────────────

func TestOrderPlacedSendsOneEmailAndRecordsIt(t *testing.T) {
	f := setup(t)
	o := f.newOrder(t)
	ctx := context.Background()

	if err := f.svc.DeliverEvent(ctx, placed(f.shopID, o.ID, f.memberID)); err != nil {
		t.Fatalf("DeliverEvent: %v", err)
	}

	msgs := f.sender.Messages()
	if len(msgs) != 1 {
		t.Fatalf("sent %d messages, want 1", len(msgs))
	}
	if msgs[0].To != "buyer@example.com" {
		t.Errorf("recipient = %q", msgs[0].To)
	}
	if !strings.Contains(msgs[0].Subject, "訂單成立") {
		t.Errorf("subject = %q", msgs[0].Subject)
	}
	// The composition, on a real row read back out of Postgres — the amounts a
	// customer sees must survive the round trip unscaled.
	for _, want := range []string{
		"商品小計：NT$6,800", "會員折扣（VIP 10%）：-NT$680", "運費：NT$120", "應付總額：NT$6,240",
		"週年紀念花束（BQ-ANNI-M）", "卡片訊息：生日快樂", "收件人：王小明",
	} {
		if !strings.Contains(msgs[0].TextBody, want) {
			t.Errorf("body is missing %q:\n%s", want, msgs[0].TextBody)
		}
	}

	rows := f.rows(t, o.ID)
	if len(rows) != 1 {
		t.Fatalf("recorded %d rows, want 1", len(rows))
	}
	if rows[0].Kind != notify.KindOrderPlaced || rows[0].Status != notify.StatusSent {
		t.Errorf("row kind=%d status=%d, want kind=%d status=%d",
			rows[0].Kind, rows[0].Status, notify.KindOrderPlaced, notify.StatusSent)
	}
	if rows[0].SentAt == nil {
		t.Error("sent_at was not recorded")
	}
	if rows[0].RecipientEmail != "buyer@example.com" {
		t.Errorf("recipient snapshot = %q", rows[0].RecipientEmail)
	}
}

func TestBothNotificationsCoexistForOneOrder(t *testing.T) {
	f := setup(t)
	o := f.newOrder(t)
	ctx := context.Background()

	for _, e := range []events.Event{placed(f.shopID, o.ID, f.memberID), paid(f.shopID, o.ID, f.memberID)} {
		if err := f.svc.DeliverEvent(ctx, e); err != nil {
			t.Fatalf("DeliverEvent(%s): %v", e.EventName(), err)
		}
	}

	if got := len(f.sender.Messages()); got != 2 {
		t.Fatalf("sent %d messages, want 2", got)
	}
	if got := len(f.rows(t, o.ID)); got != 2 {
		t.Fatalf("recorded %d rows, want 2", got)
	}
	// UNIQUE(order_id, kind) — not UNIQUE(order_id) — so the two kinds do not
	// crowd each other out.
	if got := f.sender.Messages()[1].Subject; !strings.Contains(got, "付款成功") {
		t.Errorf("second subject = %q", got)
	}
}

// ── the one that matters most (design D6) ──────────────────────────────

// TestRepeatedPaymentDeliveryEmailsOnce is the assertion this whole change's
// dedupe table exists for.
//
// payment.Service.HandleWebhook publishes OrderPaymentSucceeded on EVERY
// delivery that re-confirms an already-succeeded payment — its own doc comment
// says so — and 綠界 redelivers up to 4 times a day when it does not see `1|OK`.
// Two "付款成功" emails is a failure the customer can see.
func TestRepeatedPaymentDeliveryEmailsOnce(t *testing.T) {
	f := setup(t)
	o := f.newOrder(t)
	ctx := context.Background()

	for range 3 {
		if err := f.svc.DeliverEvent(ctx, paid(f.shopID, o.ID, f.memberID)); err != nil {
			t.Fatalf("DeliverEvent: %v", err)
		}
	}

	if got := len(f.sender.Messages()); got != 1 {
		t.Fatalf("three deliveries sent %d messages, want 1", got)
	}
	n, err := f.svc.SentCount(ctx, f.shopID, o.ID, notify.KindPaymentSucceeded)
	if err != nil {
		t.Fatalf("SentCount: %v", err)
	}
	if n != 1 {
		t.Fatalf("recorded %d rows, want 1", n)
	}
}

// TestConcurrentDeliveriesEmailOnce — the unique index, not a read-then-write
// check, is what arbitrates. Two concurrent deliveries would both pass an
// application-level "has it been sent?" query.
func TestConcurrentDeliveriesEmailOnce(t *testing.T) {
	f := setup(t)
	o := f.newOrder(t)

	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = f.svc.DeliverEvent(context.Background(), paid(f.shopID, o.ID, f.memberID))
		}()
	}
	wg.Wait()

	if got := len(f.sender.Messages()); got != 1 {
		t.Fatalf("six concurrent deliveries sent %d messages, want 1", got)
	}
}

// ── boundaries ─────────────────────────────────────────────────────────

// TestCustomerWithoutEmailIsSkippedSilently — members.email is Optional/Nillable
// (the schema reserves phone-only identities). Nothing to send to is not an
// error, and it must leave no row behind claiming otherwise.
func TestCustomerWithoutEmailIsSkippedSilently(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	silent := f.client.Member.Create().SetShopID(f.shopID).SetStatus(1).SaveX(ctx)
	o := f.client.Order.Create().
		SetShopID(f.shopID).SetMemberID(silent.ID).
		SetStatus(order.StatusCreated).
		SetPaymentStatus(order.PaymentStatusUnpaid).
		SetFulfillmentStatus(order.FulfillmentStatusUnfulfilled).
		SetCurrency("TWD").SetSubtotalAmount(100).SetTotalAmount(100).
		SetShippingAddress(json.RawMessage(`{}`)).
		SaveX(ctx)

	if err := f.svc.DeliverEvent(ctx, placed(f.shopID, o.ID, silent.ID)); err != nil {
		t.Fatalf("a customer without an email must not be an error: %v", err)
	}
	if got := len(f.sender.Messages()); got != 0 {
		t.Errorf("sent %d messages, want 0", got)
	}
	if got := len(f.rows(t, o.ID)); got != 0 {
		t.Errorf("recorded %d rows, want 0", got)
	}
}

// TestForeignShopDeliversNothing — every query carries shop_id explicitly
// (design D7). Notifications are delivered outside tenant context, so the
// interceptor adds nothing; without the predicate, an event naming another
// shop's order would mail that shop's order contents to this shop's customer.
func TestForeignShopDeliversNothing(t *testing.T) {
	f := setup(t)
	o := f.newOrder(t)
	ctx := context.Background()

	// A second shop with a customer at the very same address — per-shop identity
	// means these are two unrelated people.
	other := f.client.Shop.Create().SetName("Other Shop").SetStatus(1).SaveX(ctx)
	otherMember := f.client.Member.Create().
		SetShopID(other.ID).SetEmail("buyer@example.com").SetStatus(1).SaveX(ctx)

	if err := f.svc.DeliverEvent(ctx, placed(other.ID, o.ID, otherMember.ID)); err == nil {
		t.Fatal("delivering shop A's order under shop B must fail, not succeed quietly")
	}
	if got := len(f.sender.Messages()); got != 0 {
		t.Errorf("sent %d messages across the tenant boundary, want 0", got)
	}
	if got := len(f.rows(t, o.ID)); got != 0 {
		t.Errorf("recorded %d rows, want 0", got)
	}
}

// TestSendFailureRecordsAFailedRow — there is no retry (design D5), so the row
// is the only durable evidence that a customer never got their confirmation. A
// log line alone would rotate away.
func TestSendFailureRecordsAFailedRow(t *testing.T) {
	f := setup(t)
	o := f.newOrder(t)
	ctx := context.Background()
	f.sender.Err = errors.New("notify: dial smtp: i/o timeout")

	if err := f.svc.DeliverEvent(ctx, placed(f.shopID, o.ID, f.memberID)); err == nil {
		t.Fatal("a failed send must be reported to the caller (which logs it)")
	}

	rows := f.rows(t, o.ID)
	if len(rows) != 1 {
		t.Fatalf("recorded %d rows, want 1", len(rows))
	}
	if rows[0].Status != notify.StatusFailed {
		t.Errorf("status = %d, want %d (failed)", rows[0].Status, notify.StatusFailed)
	}
	if rows[0].ErrorReason == nil || !strings.Contains(*rows[0].ErrorReason, "i/o timeout") {
		t.Errorf("error_reason = %v, want the transport error", rows[0].ErrorReason)
	}
	if rows[0].SentAt != nil {
		t.Error("sent_at must stay unset for a failed send")
	}

	// The order is untouched: a failed email must never look like a failed sale.
	reloaded := f.client.Order.GetX(ctx, o.ID)
	if reloaded.Status != order.StatusCreated || reloaded.PaymentStatus != order.PaymentStatusUnpaid {
		t.Errorf("a failed email changed the order: status=%d payment=%d",
			reloaded.Status, reloaded.PaymentStatus)
	}
}

// TestDisabledSenderDoesNothingAtAll — no credentials means the deployment
// behaves byte for byte as it did before this change (design D10): no mail, no
// error, and in particular no rows.
func TestDisabledSenderDoesNothingAtAll(t *testing.T) {
	f := setup(t)
	o := f.newOrder(t)
	ctx := context.Background()

	off := &notify.Service{Client: f.client, Sender: notify.DiscardSender{}}
	if err := off.DeliverEvent(ctx, placed(f.shopID, o.ID, f.memberID)); err != nil {
		t.Fatalf("disabled notifications must not error: %v", err)
	}
	if got := len(f.rows(t, o.ID)); got != 0 {
		t.Errorf("a disabled service recorded %d rows, want 0", got)
	}
}

// TestUnrelatedEventsAreIgnored — OrderReturned is points' business, not this
// package's (design D2: no cancellation or fulfilment mail).
func TestUnrelatedEventsAreIgnored(t *testing.T) {
	f := setup(t)
	o := f.newOrder(t)
	ctx := context.Background()

	ev := events.OrderReturned{ShopID: f.shopID, OrderID: o.ID, MemberID: f.memberID}
	if err := f.svc.DeliverEvent(ctx, ev); err != nil {
		t.Fatalf("an unrelated event must be a no-op: %v", err)
	}
	if got := len(f.sender.Messages()); got != 0 {
		t.Errorf("sent %d messages for an unrelated event, want 0", got)
	}
}

// ── the asynchronous half (design D5) ──────────────────────────────────

// TestHandleEnqueuesAndWorkersDeliver — Handle does no I/O; the workers do. Close
// drains what was already accepted.
func TestHandleEnqueuesAndWorkersDeliver(t *testing.T) {
	f := setup(t)
	o := f.newOrder(t)

	f.svc.Workers, f.svc.QueueSize = 2, 8
	f.svc.Start()
	f.svc.Handle(context.Background(), placed(f.shopID, o.ID, f.memberID))
	if err := f.svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := len(f.sender.Messages()); got != 1 {
		t.Fatalf("workers delivered %d messages, want 1", got)
	}
}

// gatedSender blocks in Send until released, so the queue can be filled
// deterministically.
type gatedSender struct {
	release chan struct{}
	mu      sync.Mutex
	n       int
}

func (g *gatedSender) Send(context.Context, notify.Message) error {
	<-g.release
	g.mu.Lock()
	g.n++
	g.mu.Unlock()
	return nil
}

func (g *gatedSender) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n
}

// TestHandleDropsRatherThanBlocks — the whole point of the bounded queue. Back
// pressure applied here would land on the checkout request, which is exactly the
// coupling the async hop exists to prevent; so a full queue drops and logs
// instead.
func TestHandleDropsRatherThanBlocks(t *testing.T) {
	f := setup(t)
	o := f.newOrder(t)

	gate := &gatedSender{release: make(chan struct{})}
	svc := &notify.Service{Client: f.client, Sender: gate, Workers: 1, QueueSize: 1}
	svc.Start()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 20 {
			svc.Handle(context.Background(), paid(f.shopID, o.ID, f.memberID))
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Handle blocked on a full queue — a stalled mail server would now be stalling checkout")
	}

	close(gate.release)
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// At most one in flight plus one buffered ever reached the sender; the rest
	// were dropped. (They are also deduped by (order_id, kind), so the exact
	// number is bounded by the queue, not by the 20 events.)
	if n := gate.count(); n > 2 {
		t.Fatalf("sender saw %d messages; the queue bound (1) was not enforced", n)
	}
}
