package application_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/application"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/shopapplication"
	"ksdevworks/ecommerce/api/internal/ent/shopapplicationevent"
	"ksdevworks/ecommerce/api/internal/ent/shopapplicationmessage"
	"ksdevworks/ecommerce/api/internal/ent/shopapplicationtoken"
	"ksdevworks/ecommerce/api/internal/notify"
	"ksdevworks/ecommerce/api/internal/testutil"
)

const consent = "terms-v1"

type env struct {
	client *ent.Client
	svc    *application.Service
	now    time.Time
}

func newEnv(t *testing.T, n application.Notifier) *env {
	t.Helper()
	client := testutil.OpenDB(t)
	e := &env{client: client, now: time.Now().UTC().Truncate(time.Second)}
	if n == nil {
		n = application.OutboxNotifier{}
	}
	e.svc = &application.Service{
		Client: client, Notifier: n, Now: func() time.Time { return e.now },
		Cfg: application.Config{
			WebsiteURL: "https://www.example.test", ConsentVersion: consent, ReapplyCooldown: 30 * 24 * time.Hour,
			DailyCap: 100, RejectedRetention: 90 * 24 * time.Hour, ApprovedRetention: 365 * 24 * time.Hour,
		},
	}
	return e
}

func (e *env) submit(t *testing.T, email string) {
	t.Helper()
	v, err := e.svc.Validate(application.SubmitInput{
		Email: email, ContactName: "Pat", ShopName: "Pat Shop", BusinessSummary: "things", ConsentVersion: consent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Submit(context.Background(), v, "198.51.100.1"); err != nil {
		t.Fatal(err)
	}
}

// seedApp inserts an application directly in a given state and age.
func (e *env) seedApp(t *testing.T, email, status string, created time.Time, reviewed *time.Time) *ent.ShopApplication {
	t.Helper()
	b := e.client.ShopApplication.Create().SetEmail(email).SetContactName("Name " + email).SetPhone("0912").
		SetShopName("Shop " + email).SetBusinessSummary("summary " + email).SetConsentVersion("v").SetConsentedAt(created).
		SetStatus(status).SetCreatedAt(created).SetUpdatedAt(created).SetDecisionReason("because " + email).SetInternalNote("note").
		SetSubmittedIPHmac("deadbeef")
	if reviewed != nil {
		b.SetReviewedAt(*reviewed)
	}
	app := b.SaveX(context.Background())
	e.client.ShopApplicationMessage.Create().SetApplicationID(app.ID).SetKind("rejected").SetRecipientEmail(email).
		SetSubject("s").SetBodyText("body for " + email).SetDelivery("manual").SetStatus("pending_manual").SaveX(context.Background())
	e.client.ShopApplicationToken.Create().SetApplicationID(app.ID).SetTokenHash(fmt.Sprintf("h-%s-%d", email, app.ID)).SetExpiresAt(created.Add(time.Hour)).SaveX(context.Background())
	return app
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

// ── SMTPNotifier ──────────────────────────────────────────────────

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSMTPNotifierSendsAfterCommitAndRecordsSent(t *testing.T) {
	sender := &notify.RecordingSender{}
	client := testutil.OpenDB(t)
	n := &application.SMTPNotifier{Client: client, Sender: sender, Workers: 1, Queue: 8, Timeout: 2 * time.Second}
	n.Start()
	defer n.Close()
	e := &env{client: client, now: time.Now().UTC()}
	e.svc = &application.Service{Client: client, Notifier: n, Now: func() time.Time { return e.now },
		Cfg: application.Config{WebsiteURL: "https://www.example.test", ConsentVersion: consent, ReapplyCooldown: time.Hour}}
	e.submit(t, "smtp@a.test")

	ctx := context.Background()
	waitFor(t, "message sent", func() bool {
		return client.ShopApplicationMessage.Query().Where(shopapplicationmessage.StatusEQ("sent")).ExistX(ctx)
	})
	msg := client.ShopApplicationMessage.Query().OnlyX(ctx)
	if msg.Delivery != "email" || msg.BodyText != "" || msg.SentAt == nil {
		t.Fatalf("row = %+v (the body must be dropped once sent)", msg)
	}
}

func TestSMTPNotifierFailureIsRecordedAndNeverBlocksSubmit(t *testing.T) {
	sender := &notify.RecordingSender{Err: fmt.Errorf("535 auth failed for pw-secret")}
	client := testutil.OpenDB(t)
	n := &application.SMTPNotifier{Client: client, Sender: sender, Workers: 1, Queue: 8, Timeout: time.Second}
	n.Start()
	defer n.Close()
	e := &env{client: client, now: time.Now().UTC()}
	e.svc = &application.Service{Client: client, Notifier: n, Now: func() time.Time { return e.now },
		Cfg: application.Config{WebsiteURL: "https://www.example.test", ConsentVersion: consent}}
	e.submit(t, "smtpfail@a.test") // returns nil even though sending will fail

	waitFor(t, "failed row", func() bool {
		return client.ShopApplicationMessage.Query().Where(shopapplicationmessage.StatusEQ("failed")).ExistX(context.Background())
	})
	msg := client.ShopApplicationMessage.Query().OnlyX(context.Background())
	if msg.ErrorReason != "send failed" || strings.Contains(msg.ErrorReason, "pw-secret") {
		t.Fatalf("error_reason = %q", msg.ErrorReason)
	}
	if msg.BodyText == "" {
		t.Fatal("a failed message keeps its body so an operator can hand it over")
	}
}

type blockingSender struct {
	release chan struct{}
	mu      sync.Mutex
	sent    int
}

func (b *blockingSender) Send(ctx context.Context, _ notify.Message) error {
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	b.mu.Lock()
	b.sent++
	b.mu.Unlock()
	return nil
}

func TestSMTPNotifierQueueFullDropsToFailedRow(t *testing.T) {
	bs := &blockingSender{release: make(chan struct{})}
	client := testutil.OpenDB(t)
	n := &application.SMTPNotifier{Client: client, Sender: bs, Workers: 1, Queue: 1, Timeout: 5 * time.Second}
	n.Start()
	e := &env{client: client, now: time.Now().UTC()}
	e.svc = &application.Service{Client: client, Notifier: n, Now: func() time.Time { return e.now },
		Cfg: application.Config{WebsiteURL: "https://www.example.test", ConsentVersion: consent}}
	for i := 0; i < 6; i++ {
		e.submit(t, fmt.Sprintf("q%d@a.test", i)) // none of these may block
	}
	failed := client.ShopApplicationMessage.Query().Where(shopapplicationmessage.StatusEQ("failed"), shopapplicationmessage.ErrorReasonEQ("queue full")).CountX(context.Background())
	if failed < 1 {
		t.Fatalf("expected at least one queue-full drop, got %d", failed)
	}
	close(bs.release)
	n.Close()
}

// ── Purge ─────────────────────────────────────────────────────────

func TestPurgeDryRunWritesNothingAndReportsIDsOnly(t *testing.T) {
	e := newEnv(t, nil)
	old := e.now.Add(-days(8))
	a := e.seedApp(t, "stale@a.test", "pending_verification", old, nil)
	rejAt := e.now.Add(-days(100))
	b := e.seedApp(t, "rej@a.test", "rejected", rejAt, &rejAt)

	rep, err := e.svc.Purge(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Deleted) != 1 || rep.Deleted[0] != a.ID || len(rep.Blanked) != 1 || rep.Blanked[0] != b.ID {
		t.Fatalf("report = %+v", rep)
	}
	if e.client.ShopApplication.Query().CountX(context.Background()) != 2 ||
		e.client.ShopApplication.GetX(context.Background(), b.ID).Email != "rej@a.test" ||
		e.client.ShopApplicationEvent.Query().CountX(context.Background()) != 0 {
		t.Fatal("dry run wrote something")
	}
}

func TestPurgeUnverifiedAfterSevenDaysDeletesRowTokensMessagesButKeepsEvents(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	fresh := e.seedApp(t, "fresh@a.test", "pending_verification", e.now.Add(-days(6)), nil)
	stale := e.seedApp(t, "stale@a.test", "pending_verification", e.now.Add(-days(8)), nil)
	if _, err := e.client.ShopApplicationEvent.Create().SetApplicationID(stale.ID).SetEvent("submitted").Save(ctx); err != nil {
		t.Fatal(err)
	}

	rep, err := e.svc.Purge(ctx, true)
	if err != nil || len(rep.Deleted) != 1 || rep.Deleted[0] != stale.ID {
		t.Fatalf("purge: %+v %v", rep, err)
	}
	if e.client.ShopApplication.Query().Where(shopapplication.IDEQ(stale.ID)).ExistX(ctx) {
		t.Fatal("stale application survived")
	}
	if !e.client.ShopApplication.Query().Where(shopapplication.IDEQ(fresh.ID)).ExistX(ctx) {
		t.Fatal("a 6-day-old unverified application was purged")
	}
	if e.client.ShopApplicationToken.Query().Where(shopapplicationtoken.ApplicationIDEQ(stale.ID)).ExistX(ctx) ||
		e.client.ShopApplicationMessage.Query().Where(shopapplicationmessage.ApplicationIDEQ(stale.ID)).ExistX(ctx) {
		t.Fatal("tokens or messages (with the e-mail address) survived")
	}
	evs := e.client.ShopApplicationEvent.Query().Where(shopapplicationevent.ApplicationIDEQ(stale.ID)).AllX(ctx)
	names := map[string]bool{}
	for _, ev := range evs {
		names[ev.Event] = true
	}
	if !names["submitted"] || !names["expired"] || !names["purged"] {
		t.Fatalf("events after purge: %v", names)
	}
	// Idempotent.
	rep, err = e.svc.Purge(ctx, true)
	if err != nil || len(rep.Deleted) != 0 || len(rep.Blanked) != 0 {
		t.Fatalf("second run: %+v %v", rep, err)
	}
}

func TestPurgeRejectedAfterRetentionBlanksPIIKeepsRowAndEvents(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	at89, at91 := e.now.Add(-days(89)), e.now.Add(-days(91))
	young := e.seedApp(t, "young@a.test", "rejected", at89, &at89)
	old := e.seedApp(t, "old@a.test", "rejected", at91, &at91)
	e.client.ShopApplicationEvent.Create().SetApplicationID(old.ID).SetEvent("rejected").SetDetail(map[string]any{"delivery": "manual"}).SaveX(ctx)

	rep, err := e.svc.Purge(ctx, true)
	if err != nil || len(rep.Blanked) != 1 || rep.Blanked[0] != old.ID {
		t.Fatalf("purge: %+v %v", rep, err)
	}
	got := e.client.ShopApplication.GetX(ctx, old.ID)
	if got.Email != "" || got.ContactName != "" || got.Phone != "" || got.ShopName != "" || got.BusinessSummary != "" ||
		got.DecisionReason != "" || got.InternalNote != "" || got.SubmittedIPHmac != "" || got.PurgedAt == nil || got.Status != "rejected" {
		t.Fatalf("row not blanked: %+v", got)
	}
	if e.client.ShopApplication.GetX(ctx, young.ID).Email != "young@a.test" {
		t.Fatal("a 89-day-old rejection was purged")
	}
	msg := e.client.ShopApplicationMessage.Query().Where(shopapplicationmessage.ApplicationIDEQ(old.ID)).OnlyX(ctx)
	if msg.RecipientEmail != "" || msg.BodyText != "" {
		t.Fatalf("message PII survived: %+v", msg)
	}
	if e.client.ShopApplicationEvent.Query().Where(shopapplicationevent.ApplicationIDEQ(old.ID)).CountX(ctx) != 2 { // rejected + purged
		t.Fatal("events must be kept (and a purged event added)")
	}
	rep, _ = e.svc.Purge(ctx, true)
	if len(rep.Blanked) != 0 {
		t.Fatalf("not idempotent: %+v", rep)
	}
}

func TestPurgeNeverBlanksBeforeTheCooldownEnds(t *testing.T) {
	e := newEnv(t, nil)
	e.svc.Cfg.RejectedRetention = days(10)
	e.svc.Cfg.ReapplyCooldown = days(30)
	at := e.now.Add(-days(20)) // past retention, inside the cooldown
	a := e.seedApp(t, "cool@a.test", "rejected", at, &at)
	rep, _ := e.svc.Purge(context.Background(), true)
	if len(rep.Blanked) != 0 || e.client.ShopApplication.GetX(context.Background(), a.ID).Email == "" {
		t.Fatalf("purged during cooldown: %+v", rep)
	}
	e.now = e.now.Add(days(11))
	rep, _ = e.svc.Purge(context.Background(), true)
	if len(rep.Blanked) != 1 {
		t.Fatalf("not purged after cooldown: %+v", rep)
	}
}

func TestPurgeApprovedAfterOneYear(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	at364, at366 := e.now.Add(-days(364)), e.now.Add(-days(366))
	keep := e.seedApp(t, "keep@a.test", "approved", at364, &at364)
	drop := e.seedApp(t, "drop@a.test", "approved", at366, &at366)
	pending := e.seedApp(t, "wait@a.test", "pending_review", e.now.Add(-days(400)), nil)

	rep, err := e.svc.Purge(ctx, true)
	if err != nil || len(rep.Blanked) != 1 || rep.Blanked[0] != drop.ID {
		t.Fatalf("purge: %+v %v", rep, err)
	}
	if e.client.ShopApplication.GetX(ctx, keep.ID).Email == "" || e.client.ShopApplication.GetX(ctx, pending.ID).Email == "" {
		t.Fatal("purged a young approval or an undecided application")
	}
}

func TestPurgeByEmailHandlesPrivacyRequests(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	at := e.now.Add(-days(1))
	open := e.seedApp(t, "me@a.test", "pending_review", at, nil)
	done := e.seedApp(t, "ME2@a.test", "rejected", at, &at) // different address, untouched
	_ = done
	rej := e.seedApp(t, "me@a.test", "rejected", at, &at)

	if _, err := e.svc.PurgeEmail(ctx, "not-an-email", true); err == nil {
		t.Fatal("bad e-mail accepted")
	}
	rep, err := e.svc.PurgeEmail(ctx, " Me@A.test ", false)
	if err != nil || len(rep.Deleted) != 1 || len(rep.Blanked) != 1 {
		t.Fatalf("dry run: %+v %v", rep, err)
	}
	if !e.client.ShopApplication.Query().Where(shopapplication.IDEQ(open.ID)).ExistX(ctx) {
		t.Fatal("dry run deleted")
	}
	if _, err := e.svc.PurgeEmail(ctx, "me@a.test", true); err != nil {
		t.Fatal(err)
	}
	if e.client.ShopApplication.Query().Where(shopapplication.IDEQ(open.ID)).ExistX(ctx) {
		t.Fatal("open application not deleted")
	}
	if e.client.ShopApplication.GetX(ctx, rej.ID).Email != "" {
		t.Fatal("decided application not blanked")
	}
	if e.client.ShopApplication.Query().Where(shopapplication.EmailEQ("me2@a.test")).CountX(ctx)+
		e.client.ShopApplication.Query().Where(shopapplication.EmailEQ("ME2@a.test")).CountX(ctx) != 1 {
		t.Fatal("another person's application was touched")
	}
}
