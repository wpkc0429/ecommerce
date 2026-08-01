package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"ksdevworks/ecommerce/api/internal/ent"
	entmember "ksdevworks/ecommerce/api/internal/ent/member"
	entorder "ksdevworks/ecommerce/api/internal/ent/order"
	entorderitem "ksdevworks/ecommerce/api/internal/ent/orderitem"
	entnotification "ksdevworks/ecommerce/api/internal/ent/ordernotification"
	"ksdevworks/ecommerce/api/internal/events"
)

// order_notifications.kind (design D6) — the machine-checkable idempotency axis,
// exactly like point_transactions.kind. Not a string: this value is what the
// UNIQUE(order_id, kind) index arbitrates on, not a label for humans.
const (
	KindOrderPlaced      int16 = 0
	KindPaymentSucceeded int16 = 1
)

// order_notifications.status. A row is created queued and moves to exactly one
// terminal value. A row left at queued means the process died mid-send — that is
// information, not a bug in the state machine.
const (
	StatusQueued int16 = 0
	StatusSent   int16 = 1
	StatusFailed int16 = 2
)

// Defaults for the worker pool (design D5). Small on purpose: at roughly one
// second per message, 4 workers sustain ~240 messages/minute, which is far above
// the Gmail account cap that will run out first (design D9). Making the pool
// bigger would only move the failure from a visible queue to an invisible
// account lockout.
const (
	defaultWorkers   = 4
	defaultQueueSize = 256
	defaultTimeout   = 20 * time.Second
)

// Service turns order domain events into customer emails.
//
// Its dependencies are *ent.Client, a Sender and a logger — the same shape as
// points.Service, and for the same reason: it reacts to events and imports
// nothing from whoever publishes them.
//
// Every query it makes carries shop_id explicitly (design D7). It runs from the
// dispatcher, i.e. outside tenant context — the payment webhook path especially
// so — where the tenant interceptor adds nothing. Without that predicate, an
// event naming another shop's order id would mail that shop's order contents to
// this shop's customer; customer identity is per-shop, so the same email address
// at two shops is two unrelated people.
type Service struct {
	Client *ent.Client
	Sender Sender
	Log    *slog.Logger

	// Workers/QueueSize/Timeout are optional; zero means the defaults above.
	Workers   int
	QueueSize int
	Timeout   time.Duration

	queue     chan job
	wg        sync.WaitGroup
	startOnce sync.Once
	stopOnce  sync.Once
}

type job struct {
	shopID   int
	orderID  int
	memberID int
	kind     int16
}

func (s *Service) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// sendingEnabled reports whether this service should do anything at all.
//
// A nil Sender or the explicit DiscardSender means notifications are off, and
// "off" means completely off: no goroutines, no queries, no order_notifications
// rows (design D10). An unconfigured deployment behaves byte for byte as it did
// before this package existed.
//
// The switch lives here rather than on the Subscribe call in wire.go, and that
// is deliberate: subscription must be unconditional or the wiring lock (check ⑦,
// an equality comparison) would start depending on the environment variables of
// whoever runs the tests, and a lock whose expected value varies is not a lock.
func (s *Service) sendingEnabled() bool {
	if s.Sender == nil {
		return false
	}
	_, discard := s.Sender.(DiscardSender)
	return !discard
}

// Start brings up the worker pool. Safe to call more than once; a no-op when
// notifications are disabled.
func (s *Service) Start() {
	s.startOnce.Do(func() {
		if !s.sendingEnabled() {
			return
		}
		size := s.QueueSize
		if size <= 0 {
			size = defaultQueueSize
		}
		workers := s.Workers
		if workers <= 0 {
			workers = defaultWorkers
		}
		s.queue = make(chan job, size)
		for range workers {
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				for j := range s.queue {
					s.runJob(j)
				}
			}()
		}
	})
}

// Close drains the queue and waits for the workers. Wired into App.Close, so a
// graceful shutdown finishes the messages already accepted.
//
// It does not wait for messages that were never enqueued: this is not a durable
// queue and does not pretend to be (design D5).
func (s *Service) Close() error {
	s.stopOnce.Do(func() {
		if s.queue == nil {
			return
		}
		close(s.queue)
		s.wg.Wait()
	})
	return nil
}

// Handle is the dispatcher subscriber. **It does no I/O.**
//
// events.Dispatcher.Publish is synchronous and runs on the publisher's
// goroutine, which for both of the events below is an HTTP request — checkout or
// a payment webhook. Doing the SMTP round trip here would make the checkout
// endpoint's latency a function of Gmail's, and a Gmail hiccup would slow down
// every purchase in the system for something the customer never asked for
// (design D5). So this hands over a three-int job and returns.
//
// A full queue drops the message and logs loudly. Dropping beats blocking: the
// alternative is back-pressure applied to checkout, i.e. the coupling this whole
// arrangement exists to prevent.
func (s *Service) Handle(_ context.Context, e events.Event) {
	if !s.sendingEnabled() || s.queue == nil {
		return
	}
	j, ok := jobFor(e)
	if !ok {
		return
	}
	select {
	case s.queue <- j:
	default:
		// No PII: shop/order/member ids and the kind, nothing else (design D13).
		s.logger().Error("notify: queue full, notification dropped",
			"shop_id", j.shopID, "order_id", j.orderID, "kind", j.kind)
	}
}

// jobFor maps the events this package reacts to. Everything else is ignored —
// including OrderReturned, which points reacts to and this package deliberately
// does not (design D2).
func jobFor(e events.Event) (job, bool) {
	switch ev := e.(type) {
	case events.OrderPlaced:
		return job{shopID: ev.ShopID, orderID: ev.OrderID, memberID: ev.MemberID, kind: KindOrderPlaced}, true
	case events.OrderPaymentSucceeded:
		return job{shopID: ev.ShopID, orderID: ev.OrderID, memberID: ev.MemberID, kind: KindPaymentSucceeded}, true
	default:
		return job{}, false
	}
}

func (s *Service) runJob(j job) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	// A context of its own, never the request's: the publishing request has
	// already returned by now, and its context is cancelled — reusing it would
	// make every send fail, in production only.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := s.deliver(ctx, j); err != nil {
		s.logger().Error("notify: delivery failed",
			"shop_id", j.shopID, "order_id", j.orderID, "kind", j.kind, "err", err)
	}
}

// DeliverEvent processes one event synchronously, start to finish.
//
// It exists so tests can exercise the entire path — dedupe, load, render, send,
// record — without racing a worker pool, and so the asynchrony (which has its
// own small test) is not baked into every assertion about content. No production
// path calls it; Handle enqueues and the workers call deliver.
func (s *Service) DeliverEvent(ctx context.Context, e events.Event) error {
	j, ok := jobFor(e)
	if !ok {
		return nil
	}
	return s.deliver(ctx, j)
}

// deliver performs one notification.
//
// Order of operations, and why (design D6):
//
//	1. load the order and the buyer — both scoped by an explicit shop_id
//	2. no email address → stop, silently, with no row
//	3. reserve: INSERT the row. Losing the UNIQUE(order_id, kind) race means
//	   another delivery already handled this one; that is a no-op, not an error
//	4. render
//	5. send
//	6. record the outcome on the reserved row
//
// Step 3 before step 5 makes this **at-most-once**. The alternative (send, then
// record) risks a second copy of "訂單已成立" every time the write or the process
// fails after a successful send — a visible failure — and buys only the ability
// to retry, which this system does not have. A send that fails leaves a row
// marked failed with its reason, so a lost email is a queryable fact rather than
// a log line that rotates away.
func (s *Service) deliver(ctx context.Context, j job) error {
	if !s.sendingEnabled() {
		return nil
	}

	ord, err := s.Client.Order.Query().
		Where(entorder.IDEQ(j.orderID), entorder.ShopIDEQ(j.shopID)).
		// Explicitly by id: the confirmation email lists these lines, and since
		// change partial-shipment order_items is a table that gets UPDATEd, so an
		// unordered eager-load no longer comes back in insertion order (see
		// order.orderItemsByID for the full account).
		WithItems(func(q *ent.OrderItemQuery) { q.Order(entorderitem.ByID()) }).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("notify: load order: %w", err)
	}

	m, err := s.Client.Member.Query().
		Where(entmember.IDEQ(j.memberID), entmember.ShopIDEQ(j.shopID)).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("notify: load member: %w", err)
	}
	// members.email is Optional/Nillable — the schema reserves phone-only
	// identities. Nothing to send to, nothing to record, and nothing is wrong:
	// return quietly rather than manufacturing an error the customer's order
	// would then be judged by.
	if m.Email == nil || *m.Email == "" {
		s.logger().Info("notify: customer has no email address, skipping",
			"shop_id", j.shopID, "order_id", j.orderID, "member_id", j.memberID, "kind", j.kind)
		return nil
	}
	recipient := *m.Email

	shop, err := s.Client.Shop.Get(ctx, j.shopID)
	if err != nil {
		return fmt.Errorf("notify: load shop: %w", err)
	}

	// Step 3 — the reservation. The unique index is the arbiter, never a
	// preceding existence query: two concurrent webhook deliveries would both
	// pass a read-then-write check.
	row, err := s.Client.OrderNotification.Create().
		SetShopID(j.shopID).SetOrderID(j.orderID).SetKind(j.kind).
		SetRecipientEmail(recipient).SetStatus(StatusQueued).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			// Another delivery of the same event already claimed this
			// notification. Safe no-op — the same shape points.Service uses for
			// its (order_id, kind) award guard.
			return nil
		}
		return fmt.Errorf("notify: reserve notification: %w", err)
	}

	msg, err := s.render(j.kind, RenderData{ShopName: shop.Name, Order: ord, Recipient: recipient})
	if err != nil {
		s.markFailed(ctx, row.ID, err)
		return err
	}

	if err := s.Sender.Send(ctx, msg); err != nil {
		s.markFailed(ctx, row.ID, err)
		return err
	}

	if _, err := s.Client.OrderNotification.UpdateOneID(row.ID).
		SetStatus(StatusSent).SetSentAt(time.Now()).
		Save(ctx); err != nil {
		// The mail is already gone; the row being stale is the lesser problem,
		// and re-sending to "fix" the row would be the worse one.
		return fmt.Errorf("notify: record sent notification: %w", err)
	}
	return nil
}

func (s *Service) render(kind int16, d RenderData) (Message, error) {
	switch kind {
	case KindOrderPlaced:
		return RenderOrderPlaced(d), nil
	case KindPaymentSucceeded:
		return RenderPaymentSucceeded(d), nil
	default:
		return Message{}, fmt.Errorf("notify: unknown notification kind %d", kind)
	}
}

// markFailed records a failed attempt on the reserved row.
//
// The reason stored is the error text — a transport error (dial timeout, auth
// rejected, MTA 5xx). Senders in this package are required not to put the
// recipient address or message content into their errors (design D13), because
// this is where those strings would land.
func (s *Service) markFailed(ctx context.Context, rowID int, cause error) {
	reason := cause.Error()
	if len(reason) > 500 {
		reason = reason[:500]
	}
	// The caller's context may already be the one that failed (a timeout), so
	// this write gets a fresh, short-lived one — otherwise the record of the
	// failure is itself lost to the failure.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := s.Client.OrderNotification.UpdateOneID(rowID).
		SetStatus(StatusFailed).SetErrorReason(reason).
		Save(writeCtx); err != nil && !errors.Is(err, context.Canceled) {
		s.logger().Error("notify: record failed notification", "notification_id", rowID, "err", err)
	}
}

// SentCount reports how many notifications this shop's rows record for one
// order, by kind. Used by tests; there is no HTTP endpoint over this table
// (design D2 Non-Goals).
func (s *Service) SentCount(ctx context.Context, shopID, orderID int, kind int16) (int, error) {
	return s.Client.OrderNotification.Query().
		Where(
			entnotification.ShopIDEQ(shopID),
			entnotification.OrderIDEQ(orderID),
			entnotification.KindEQ(kind),
		).
		Count(ctx)
}
