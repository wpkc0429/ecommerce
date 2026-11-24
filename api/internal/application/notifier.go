package application

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/shopapplicationmessage"
	"ksdevworks/ecommerce/api/internal/notify"
)

// Delivery modes. Callers decide by Mode, never by which fields happen to be
// present (same philosophy as account.LinkDelivery).
const (
	ModeManual = "manual" // a row waits in the outbox for an operator to hand over
	ModeEmail  = "email"  // a mail worker sends it
)

// Message kinds (shop_application_messages.kind).
const (
	KindVerifyEmail = "verify_email"
	KindApproved    = "approved"
	KindRejected    = "rejected"
)

// Notification is one message to the applicant. It never carries the owner's
// password-setup link: that credential is returned once in the approval
// response and written nowhere.
type Notification struct {
	ApplicationID int
	Kind          string
	To            string
	Subject       string
	Body          string
}

// Delivery reports how a Notification will reach the applicant.
type Delivery struct {
	Mode      string
	MessageID int
}

// Notifier abstracts how the applicant is told something (design D5).
//
// Notify runs INSIDE the caller's transaction (c is the base client or
// tx.Client()), so the outbox row commits atomically with the state change that
// caused it. AfterCommit runs once that transaction is durable: the manual
// implementation has nothing to do, the SMTP one hands the row to its worker.
type Notifier interface {
	Notify(ctx context.Context, c *ent.Client, n Notification) (Delivery, error)
	AfterCommit(ctx context.Context, d Delivery)
}

// OutboxNotifier is the default and the fallback: it only records. An operator
// copies the message from the platform admin and marks it sent.
type OutboxNotifier struct{}

// Notify implements Notifier.
func (OutboxNotifier) Notify(ctx context.Context, c *ent.Client, n Notification) (Delivery, error) {
	row, err := c.ShopApplicationMessage.Create().
		SetApplicationID(n.ApplicationID).SetKind(n.Kind).SetRecipientEmail(n.To).
		SetSubject(n.Subject).SetBodyText(n.Body).
		SetDelivery(ModeManual).SetStatus("pending_manual").
		Save(ctx)
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{Mode: ModeManual, MessageID: row.ID}, nil
}

// AfterCommit implements Notifier.
func (OutboxNotifier) AfterCommit(context.Context, Delivery) {}

// SMTPNotifier sends through a notify.Sender on a bounded worker pool, never on
// the request goroutine (so response time does not depend on whether an e-mail
// was sent: design D2). The row is written first (queued) and the outcome
// recorded after, so a lost send is a visible `failed`/`queued` row an operator
// can hand over manually. Nothing is retried; a restart loses buffered work and
// leaves those rows `queued`.
type SMTPNotifier struct {
	Client  *ent.Client
	Sender  notify.Sender
	Log     *slog.Logger
	Workers int
	Queue   int
	Timeout time.Duration

	once sync.Once
	ch   chan int
	wg   sync.WaitGroup
}

// Start launches the workers. Safe to call once.
func (s *SMTPNotifier) Start() {
	s.once.Do(func() {
		if s.Workers <= 0 {
			s.Workers = 2
		}
		if s.Queue <= 0 {
			s.Queue = 64
		}
		if s.Timeout <= 0 {
			s.Timeout = 20 * time.Second
		}
		s.ch = make(chan int, s.Queue)
		for i := 0; i < s.Workers; i++ {
			s.wg.Add(1)
			go s.work()
		}
	})
}

// Close stops accepting work and waits for the workers to drain.
func (s *SMTPNotifier) Close() {
	if s.ch == nil {
		return
	}
	close(s.ch)
	s.wg.Wait()
}

// Notify implements Notifier.
func (s *SMTPNotifier) Notify(ctx context.Context, c *ent.Client, n Notification) (Delivery, error) {
	row, err := c.ShopApplicationMessage.Create().
		SetApplicationID(n.ApplicationID).SetKind(n.Kind).SetRecipientEmail(n.To).
		SetSubject(n.Subject).SetBodyText(n.Body).
		SetDelivery(ModeEmail).SetStatus("queued").
		Save(ctx)
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{Mode: ModeEmail, MessageID: row.ID}, nil
}

// AfterCommit implements Notifier: enqueue without ever blocking.
func (s *SMTPNotifier) AfterCommit(ctx context.Context, d Delivery) {
	if d.Mode != ModeEmail || s.ch == nil {
		return
	}
	select {
	case s.ch <- d.MessageID:
	default:
		s.fail(context.WithoutCancel(ctx), d.MessageID, "queue full")
	}
}

func (s *SMTPNotifier) work() {
	defer s.wg.Done()
	for id := range s.ch {
		s.deliver(id)
	}
}

func (s *SMTPNotifier) deliver(id int) {
	ctx, cancel := context.WithTimeout(context.Background(), s.Timeout+5*time.Second)
	defer cancel()
	row, err := s.Client.ShopApplicationMessage.Get(ctx, id)
	if err != nil || row.Status != "queued" {
		return
	}
	sendCtx, sendCancel := context.WithTimeout(ctx, s.Timeout)
	defer sendCancel()
	err = s.Sender.Send(sendCtx, notify.Message{To: row.RecipientEmail, Subject: row.Subject, TextBody: row.BodyText})
	if err != nil {
		reason := "send failed"
		if errors.Is(err, notify.ErrHeaderInjection) {
			reason = "invalid header value"
		}
		s.fail(ctx, id, reason)
		return
	}
	// Sent: the body (which may hold a verification link) has done its job.
	_, uerr := s.Client.ShopApplicationMessage.Update().
		Where(shopapplicationmessage.IDEQ(id), shopapplicationmessage.StatusEQ("queued")).
		SetStatus("sent").SetSentAt(time.Now()).SetBodyText("").
		Save(ctx)
	if uerr != nil && s.Log != nil {
		s.Log.Warn("application message: record sent", "message_id", id, "err", uerr)
	}
}

func (s *SMTPNotifier) fail(ctx context.Context, id int, reason string) {
	_, err := s.Client.ShopApplicationMessage.Update().
		Where(shopapplicationmessage.IDEQ(id), shopapplicationmessage.StatusEQ("queued")).
		SetStatus("failed").SetErrorReason(reason).
		Save(ctx)
	if s.Log != nil {
		// Ids and the class only: never the recipient or the body.
		s.Log.Warn("application message not delivered", "message_id", id, "reason", reason, "record_err", err)
	}
}
