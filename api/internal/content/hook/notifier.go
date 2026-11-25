package hook

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"ksdevworks/ecommerce/api/internal/contentstore"
	"ksdevworks/ecommerce/api/internal/cryptbox"
	"ksdevworks/ecommerce/api/internal/ent"
	entphook "ksdevworks/ecommerce/api/internal/ent/contentpublishhook"
	"ksdevworks/ecommerce/api/internal/events"
)

// Event names of the request body (design D7 point 6). The body never carries
// content.
const (
	EventPublished   = "content.published"
	EventUnpublished = "content.unpublished"
	EventTest        = "content.test"
	EventManual      = "content.manual"
)

// Audit actions written to content_audit_log (host and result category only).
const (
	AuditSet          = "hook.set"
	AuditDelete       = "hook.delete"
	AuditTest         = "hook.test"
	AuditFire         = "hook.fire"
	AuditDisable      = "hook.disable"
	AuditSecretRotate = "hook.secret_rotate"
)

// Disabled reasons.
const (
	DisabledManual   = "manual"
	DisabledFailures = "consecutive_failures"
)

// Errors of the management API.
var (
	ErrNotConfigured = errors.New("hook: no publish hook is configured for this shop")
	ErrCapReached    = errors.New("hook: the daily call cap of this shop is reached")
	ErrDisabled      = errors.New("hook: the publish hook is disabled")
)

// Config is the operational policy of the notifier (config.ContentOpsConfig
// carries the production values).
type Config struct {
	Debounce      time.Duration // quiet time before a burst is sent (30 s)
	MaxWait       time.Duration // a continuous burst is sent after this at the latest
	MinInterval   time.Duration // shortest gap between two calls of a shop (60 s)
	DailyCap      int           // calls per shop per UTC day (30)
	QueueSize     int           // bounded in-memory queue (256)
	FlushInterval time.Duration // how often the flusher looks for due hooks
	Retries       int           // extra attempts on timeout / 5xx / 429 (2)
	Backoff       time.Duration // first retry delay, doubled each time (2 s)
	FailureLimit  int           // consecutive failed calls that disable the hook (5)
	Concurrency   int           // parallel sends per tick (4)
}

// DefaultConfig is design D7.
func DefaultConfig() Config {
	return Config{
		Debounce: 30 * time.Second, MaxWait: 5 * time.Minute, MinInterval: 60 * time.Second,
		DailyCap: 30, QueueSize: 256, FlushInterval: 5 * time.Second,
		Retries: 2, Backoff: 2 * time.Second, FailureLimit: 5, Concurrency: 4,
	}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.Debounce <= 0 {
		c.Debounce = d.Debounce
	}
	if c.MaxWait <= 0 {
		c.MaxWait = d.MaxWait
	}
	if c.MinInterval < 0 {
		c.MinInterval = 0
	}
	if c.DailyCap <= 0 {
		c.DailyCap = d.DailyCap
	}
	if c.QueueSize <= 0 {
		c.QueueSize = d.QueueSize
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = d.FlushInterval
	}
	if c.Retries < 0 {
		c.Retries = 0
	}
	if c.Backoff <= 0 {
		c.Backoff = d.Backoff
	}
	if c.FailureLimit <= 0 {
		c.FailureLimit = d.FailureLimit
	}
	if c.Concurrency <= 0 {
		c.Concurrency = d.Concurrency
	}
	return c
}

// Notifier owns the hook configuration, the debounce state and the delivery.
//
// Flow: the event handler (Handle) only enqueues into a bounded channel — no
// I/O, so a publish request is never slowed down. Writer goroutines mark the
// shop's hook row "pending". The flusher (Tick) sends one call per shop once
// the shop has been quiet for the debounce window, claiming the row with a
// conditional UPDATE so several API instances and the one-shot scheduler never
// send twice. Because the pending state lives in the row, a restart does not
// lose a trigger (an improvement on design D7, which accepted the loss).
type Notifier struct {
	Client *ent.Client
	Box    *cryptbox.Box
	Sender Poster
	Log    *slog.Logger
	Cfg    Config
	policy Policy
	// nowFn and sleepFn are replaceable (SetClock) for tests.
	nowFn   func() time.Time
	sleepFn func(ctx context.Context, d time.Duration) error

	once   sync.Once
	mu     sync.RWMutex
	closed bool
	queue  chan job
	jobs   sync.WaitGroup // enqueued but not yet marked
	bg     sync.WaitGroup // writer + flusher goroutines
	stop   chan struct{}
}

type job struct {
	shopID int
	event  string
	key    string
	rev    int64
}

func (n *Notifier) init() {
	n.once.Do(func() {
		n.Cfg = n.Cfg.withDefaults()
		n.queue = make(chan job, n.Cfg.QueueSize)
		n.stop = make(chan struct{})
	})
}

func (n *Notifier) now() time.Time {
	if n.nowFn != nil {
		return n.nowFn()
	}
	return time.Now()
}

func (n *Notifier) sleep(ctx context.Context, d time.Duration) error {
	if n.sleepFn != nil {
		return n.sleepFn(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// SetPolicy sets the URL policy used to validate hook URLs when they are
// configured (the sender applies its own copy again on every call).
func (n *Notifier) SetPolicy(p Policy) { n.policy = p }

// SetClock replaces the clock and the retry sleep (tests; call before use).
func (n *Notifier) SetClock(now func() time.Time, sleep func(ctx context.Context, d time.Duration) error) {
	n.nowFn, n.sleepFn = now, sleep
}

func (n *Notifier) log() *slog.Logger {
	if n.Log != nil {
		return n.Log
	}
	return slog.Default()
}

// ── event side ────────────────────────────────────────────────────────────

// Handle is the dispatcher subscriber. It only enqueues; when the queue is
// full the trigger is dropped and logged (back-pressure must never reach the
// publish request).
func (n *Notifier) Handle(_ context.Context, e events.Event) {
	n.init()
	var j job
	switch ev := e.(type) {
	case events.ContentPublished:
		j = job{shopID: ev.ShopID, event: EventPublished, key: ev.Key, rev: ev.Rev}
	case events.ContentUnpublished:
		j = job{shopID: ev.ShopID, event: EventUnpublished, key: ev.Key}
	default:
		return
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.closed {
		return
	}
	n.jobs.Add(1)
	select {
	case n.queue <- j:
	default:
		n.jobs.Done()
		n.log().Warn("content hook queue is full; a trigger was dropped", "shop_id", j.shopID)
	}
}

// Start launches the writer and the flusher goroutines.
func (n *Notifier) Start() {
	n.init()
	n.bg.Add(2)
	go func() {
		defer n.bg.Done()
		for j := range n.queue {
			n.markPending(context.Background(), j)
			n.jobs.Done()
		}
	}()
	go func() {
		defer n.bg.Done()
		t := time.NewTicker(n.Cfg.FlushInterval)
		defer t.Stop()
		for {
			select {
			case <-n.stop:
				return
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				if _, err := n.Tick(ctx, false); err != nil {
					n.log().Warn("content hook flush failed", "err_class", "tick")
				}
				cancel()
			}
		}
	}()
}

// Close stops accepting events, drains what was accepted and stops the
// goroutines. It is safe to call without Start.
func (n *Notifier) Close() error {
	n.init()
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	close(n.queue)
	close(n.stop)
	n.mu.Unlock()
	n.bg.Wait()
	return nil
}

// Drain waits until every accepted event has been written to the hook rows.
// The one-shot scheduler calls it before Tick(force).
func (n *Notifier) Drain() { n.init(); n.jobs.Wait() }

// DrainInline processes whatever is queued on the calling goroutine; for
// processes that never called Start (the one-shot command, tests).
func (n *Notifier) DrainInline(ctx context.Context) {
	n.init()
	for {
		select {
		case j := <-n.queue:
			n.markPending(ctx, j)
			n.jobs.Done()
		default:
			return
		}
	}
}

// markPending records a publish on the shop's hook row (nothing happens when
// the shop has no enabled hook).
func (n *Notifier) markPending(ctx context.Context, j job) {
	tx, err := n.Client.Tx(ctx)
	if err != nil {
		n.log().Warn("content hook mark failed", "shop_id", j.shopID, "err_class", "begin")
		return
	}
	h, err := tx.ContentPublishHook.Query().
		Where(entphook.ShopIDEQ(j.shopID), entphook.EnabledEQ(true)).ForUpdate().Only(ctx)
	if err != nil {
		_ = tx.Rollback()
		if !ent.IsNotFound(err) {
			n.log().Warn("content hook mark failed", "shop_id", j.shopID, "err_class", "load")
		}
		return
	}
	now := n.now()
	up := h.Update().SetPendingLastAt(now).SetPendingEvent(j.event).SetPendingKey(j.key).
		SetPendingRev(j.rev).AddPendingCount(1)
	if h.PendingSince == nil {
		up.SetPendingSince(now)
	}
	if _, err := up.Save(ctx); err != nil {
		_ = tx.Rollback()
		n.log().Warn("content hook mark failed", "shop_id", j.shopID, "err_class", "update")
		return
	}
	if err := tx.Commit(); err != nil {
		n.log().Warn("content hook mark failed", "shop_id", j.shopID, "err_class", "commit")
	}
}

// ── flusher ───────────────────────────────────────────────────────────────

// TickReport counts what one Tick did.
type TickReport struct {
	Sent     int // calls accepted by the receiver
	Failed   int // calls that failed after retries
	Deferred int // due but held back by the minimum interval
	Dropped  int // due but dropped by the daily cap
}

// Tick sends the calls that are due. force ignores the debounce window (the
// one-shot scheduler uses it: its burst is already complete); the minimum
// interval and the daily cap always apply.
func (n *Notifier) Tick(ctx context.Context, force bool) (TickReport, error) {
	n.init()
	now := n.now()
	rows, err := n.Client.ContentPublishHook.Query().
		Where(entphook.EnabledEQ(true), entphook.PendingSinceNotNil()).All(ctx)
	if err != nil {
		return TickReport{}, err
	}
	var (
		rep TickReport
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, n.Cfg.Concurrency)
	)
	for _, h := range rows {
		due := force ||
			(h.PendingLastAt != nil && !now.Before(h.PendingLastAt.Add(n.Cfg.Debounce))) ||
			!now.Before(h.PendingSince.Add(n.Cfg.MaxWait))
		if !due {
			continue
		}
		if h.LastAttemptAt != nil && now.Sub(*h.LastAttemptAt) < n.Cfg.MinInterval {
			rep.Deferred++
			continue
		}
		claimed, err := n.Client.ContentPublishHook.Update().
			Where(entphook.IDEQ(h.ID), entphook.PendingSinceEQ(*h.PendingSince)).
			ClearPendingSince().ClearPendingLastAt().SetPendingCount(0).SetPendingEvent("").
			SetPendingKey("").SetPendingRev(0).SetLastAttemptAt(now).Save(ctx)
		if err != nil {
			return rep, err
		}
		if claimed == 0 {
			continue // another instance took it
		}
		h := h
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			outcome := n.sendClaimed(ctx, h, now)
			mu.Lock()
			defer mu.Unlock()
			switch outcome {
			case StatusOK:
				rep.Sent++
			case StatusRateLimited:
				rep.Dropped++
			default:
				rep.Failed++
			}
		}()
	}
	wg.Wait()
	return rep, nil
}

func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// reserveCap atomically takes one call of today's budget.
func (n *Notifier) reserveCap(ctx context.Context, id int, now time.Time) (bool, error) {
	day := utcDay(now)
	same, err := n.Client.ContentPublishHook.Update().
		Where(entphook.IDEQ(id), entphook.CapDayEQ(day), entphook.CapCountLT(n.Cfg.DailyCap)).
		AddCapCount(1).Save(ctx)
	if err != nil {
		return false, err
	}
	if same > 0 {
		return true, nil
	}
	fresh, err := n.Client.ContentPublishHook.Update().
		Where(entphook.IDEQ(id), entphook.Or(entphook.CapDayIsNil(), entphook.CapDayNEQ(day))).
		SetCapDay(day).SetCapCount(1).Save(ctx)
	if err != nil {
		return false, err
	}
	return fresh > 0, nil
}

type trigger struct {
	event     string
	key       string
	rev       int64
	coalesced int
}

// sendClaimed delivers one claimed pending trigger and records the outcome.
func (n *Notifier) sendClaimed(ctx context.Context, h *ent.ContentPublishHook, now time.Time) string {
	tr := trigger{event: h.PendingEvent, key: h.PendingKey, rev: h.PendingRev, coalesced: h.PendingCount}
	if tr.event == "" {
		tr.event = EventPublished
	}
	ok, err := n.reserveCap(ctx, h.ID, now)
	if err != nil {
		n.log().Warn("content hook cap check failed", "shop_id", h.ShopID, "err_class", "db")
		return StatusNetworkError
	}
	if !ok {
		n.record(ctx, h, Result{Status: StatusRateLimited}, 0, now, tr, "automatic")
		return StatusRateLimited
	}
	res, attempts := n.deliver(ctx, h, tr, n.Cfg.Retries)
	n.record(ctx, h, res, attempts, now, tr, "automatic")
	return res.Status
}

// deliver decrypts the stored URL and secret, signs the body and posts it with
// bounded retries (timeout, 5xx and 429 only).
func (n *Notifier) deliver(ctx context.Context, h *ent.ContentPublishHook, tr trigger, retries int) (Result, int) {
	rawURL, err := n.Box.Decrypt(h.URLCiphertext)
	if err != nil {
		return Result{Status: StatusNotAllowed, URLCode: "undecryptable"}, 1
	}
	secret, err := n.Box.Decrypt(h.SigningSecretCiphertext)
	if err != nil {
		return Result{Status: StatusNotAllowed, URLCode: "undecryptable"}, 1
	}
	at := n.now()
	body := map[string]any{"event": tr.event, "shop_id": h.ShopID, "at": at.UTC().Format(time.RFC3339)}
	if tr.key != "" {
		body["key"] = tr.key
	}
	if tr.rev > 0 {
		body["rev"] = tr.rev
	}
	if tr.coalesced > 1 {
		body["coalesced"] = tr.coalesced
	}
	raw, _ := json.Marshal(body)
	headers := map[string]string{
		HeaderTimestamp: fmt.Sprintf("%d", at.Unix()),
		HeaderSignature: Sign([]byte(secret), at.Unix(), raw),
	}
	delay := n.Cfg.Backoff
	var res Result
	attempts := 0
	for {
		attempts++
		res = n.Sender.Post(ctx, rawURL, raw, headers)
		if res.OK() || !res.Retryable || attempts > retries {
			return res, attempts
		}
		if err := n.sleep(ctx, delay); err != nil {
			return res, attempts
		}
		delay *= 2
	}
}

// record stores the outcome, applies the failure limit and writes the audit
// row. Audit rows carry the host and the result category only.
func (n *Notifier) record(ctx context.Context, h *ent.ContentPublishHook, res Result, attempts int, now time.Time, tr trigger, how string) {
	up := n.Client.ContentPublishHook.UpdateOneID(h.ID).
		SetLastStatus(res.Status).SetLastHTTPStatus(res.HTTPStatus)
	disabled := false
	switch {
	case res.OK():
		up.SetLastSuccessAt(now).SetConsecutiveFailures(0)
	case res.Status == StatusRateLimited:
		// Not a failure of the receiver: the cap held the call back.
	default:
		fails := h.ConsecutiveFailures + 1
		up.SetConsecutiveFailures(fails)
		if fails >= n.Cfg.FailureLimit {
			up.SetEnabled(false).SetDisabledReason(DisabledFailures).
				ClearPendingSince().ClearPendingLastAt().SetPendingCount(0)
			disabled = true
		}
	}
	if _, err := up.Save(ctx); err != nil {
		n.log().Warn("content hook outcome not stored", "shop_id", h.ShopID, "err_class", "db")
	}
	detail := map[string]any{
		"host": h.Host, "result": res.Status, "http_status": res.HTTPStatus,
		"attempts": attempts, "coalesced": tr.coalesced, "trigger": how, "event": tr.event,
	}
	_ = contentstore.Audit(ctx, n.Client, contentstore.AuditEntry{ShopID: h.ShopID, Action: AuditFire, Key: tr.key, Detail: detail})
	if disabled {
		_ = contentstore.Audit(ctx, n.Client, contentstore.AuditEntry{
			ShopID: h.ShopID, Action: AuditDisable,
			Detail: map[string]any{"host": h.Host, "reason": DisabledFailures, "consecutive_failures": h.ConsecutiveFailures + 1},
		})
	}
	n.log().Info("content hook", "shop_id", h.ShopID, "result", res.Status, "http_status", res.HTTPStatus,
		"attempts", attempts, "trigger", how)
}

// ── management ────────────────────────────────────────────────────────────

// Status is the read model of a shop's hook. It never contains the URL or the
// signing secret.
type Status struct {
	Configured          bool       `json:"configured"`
	Enabled             bool       `json:"enabled"`
	Host                string     `json:"host"`
	URLMasked           string     `json:"url_masked"`
	LastStatus          string     `json:"last_status"`
	LastHTTPStatus      int        `json:"last_http_status"`
	LastAttemptAt       *time.Time `json:"last_attempt_at"`
	LastSuccessAt       *time.Time `json:"last_success_at"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	DisabledReason      string     `json:"disabled_reason"`
	Failed              bool       `json:"failed"`
	Pending             bool       `json:"pending"`
	PendingCount        int        `json:"pending_count"`
	DailyCount          int        `json:"daily_count"`
	DailyCap            int        `json:"daily_cap"`
	UpdatedAt           *time.Time `json:"updated_at"`
}

func (n *Notifier) status(h *ent.ContentPublishHook) *Status {
	n.init()
	if h == nil {
		return &Status{Configured: false, DailyCap: n.Cfg.DailyCap}
	}
	st := &Status{
		Configured: true, Enabled: h.Enabled, Host: h.Host,
		URLMasked:  "https://" + h.Host + "/…" + h.URLHint,
		LastStatus: h.LastStatus, LastHTTPStatus: h.LastHTTPStatus,
		LastAttemptAt: h.LastAttemptAt, LastSuccessAt: h.LastSuccessAt,
		ConsecutiveFailures: h.ConsecutiveFailures, DisabledReason: h.DisabledReason,
		Failed:  h.DisabledReason == DisabledFailures,
		Pending: h.PendingSince != nil, PendingCount: h.PendingCount,
		DailyCap: n.Cfg.DailyCap,
	}
	if h.CapDay != nil && utcDay(*h.CapDay).Equal(utcDay(n.now())) {
		st.DailyCount = h.CapCount
	}
	u := h.UpdatedAt
	st.UpdatedAt = &u
	return st
}

func (n *Notifier) load(ctx context.Context, shopID int) (*ent.ContentPublishHook, error) {
	h, err := n.Client.ContentPublishHook.Query().Where(entphook.ShopIDEQ(shopID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return h, nil
}

// Status returns the shop's hook state (Configured=false when there is none).
func (n *Notifier) Status(ctx context.Context, shopID int) (*Status, error) {
	h, err := n.load(ctx, shopID)
	if err != nil {
		return nil, err
	}
	return n.status(h), nil
}

// Validate checks a URL against the policy without storing or calling it.
func (n *Notifier) Validate(rawURL string) (*Status, error) {
	t, err := n.policy.ParseURL(rawURL)
	if err != nil {
		return nil, err
	}
	return &Status{Host: t.Host, URLMasked: "https://" + t.Host + "/…" + t.Hint}, nil
}

// SetInput changes a shop's hook. URL nil leaves the URL alone; Enabled nil
// leaves the switch alone (a new URL enables the hook unless Enabled says no).
type SetInput struct {
	URL     *string
	Enabled *bool
}

func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Set creates or changes the hook. The URL is validated against the policy
// first; it is stored encrypted and is never readable afterwards.
func (n *Notifier) Set(ctx context.Context, shopID, actor int, in SetInput) (*Status, error) {
	n.init()
	var target *Target
	if in.URL != nil {
		t, err := n.policy.ParseURL(*in.URL)
		if err != nil {
			return nil, err
		}
		target = t
	}
	h, err := n.load(ctx, shopID)
	if err != nil {
		return nil, err
	}
	if target == nil && h == nil {
		return nil, ErrNotConfigured
	}
	var out *ent.ContentPublishHook
	switch h {
	case nil:
		ct, err := n.Box.Encrypt(*in.URL)
		if err != nil {
			return nil, err
		}
		secret, err := newSecret()
		if err != nil {
			return nil, err
		}
		sct, err := n.Box.Encrypt(secret)
		if err != nil {
			return nil, err
		}
		cr := n.Client.ContentPublishHook.Create().
			SetShopID(shopID).SetURLCiphertext(ct).SetHost(target.Host).SetURLHint(target.Hint).
			SetSigningSecretCiphertext(sct).SetEnabled(in.Enabled == nil || *in.Enabled)
		if in.Enabled != nil && !*in.Enabled {
			cr.SetDisabledReason(DisabledManual)
		}
		if actor > 0 {
			cr.SetUpdatedByUserID(actor)
		}
		out, err = cr.Save(ctx)
		if err != nil {
			return nil, err
		}
	default:
		up := h.Update()
		if target != nil {
			ct, err := n.Box.Encrypt(*in.URL)
			if err != nil {
				return nil, err
			}
			up.SetURLCiphertext(ct).SetHost(target.Host).SetURLHint(target.Hint).
				SetConsecutiveFailures(0).SetLastStatus("").SetLastHTTPStatus(0)
			if in.Enabled == nil {
				up.SetEnabled(true).SetDisabledReason("")
			}
		}
		if in.Enabled != nil {
			if *in.Enabled {
				up.SetEnabled(true).SetDisabledReason("").SetConsecutiveFailures(0)
			} else {
				up.SetEnabled(false).SetDisabledReason(DisabledManual).
					ClearPendingSince().ClearPendingLastAt().SetPendingCount(0)
			}
		}
		if actor > 0 {
			up.SetUpdatedByUserID(actor)
		}
		out, err = up.Save(ctx)
		if err != nil {
			return nil, err
		}
	}
	_ = contentstore.Audit(ctx, n.Client, contentstore.AuditEntry{
		ShopID: shopID, Actor: actor, Action: AuditSet,
		Detail: map[string]any{"host": out.Host, "enabled": out.Enabled, "url_changed": target != nil},
	})
	n.log().Info("content hook configured", "shop_id", shopID, "actor_user_id", actor, "enabled", out.Enabled)
	return n.status(out), nil
}

// Delete removes the hook.
func (n *Notifier) Delete(ctx context.Context, shopID, actor int) error {
	h, err := n.load(ctx, shopID)
	if err != nil {
		return err
	}
	if h == nil {
		return ErrNotConfigured
	}
	if err := n.Client.ContentPublishHook.DeleteOne(h).Exec(ctx); err != nil {
		return err
	}
	_ = contentstore.Audit(ctx, n.Client, contentstore.AuditEntry{
		ShopID: shopID, Actor: actor, Action: AuditDelete, Detail: map[string]any{"host": h.Host},
	})
	return nil
}

// RotateSecret generates a new signing secret and returns it exactly once; it
// is stored encrypted and is not readable afterwards.
func (n *Notifier) RotateSecret(ctx context.Context, shopID, actor int) (string, error) {
	h, err := n.load(ctx, shopID)
	if err != nil {
		return "", err
	}
	if h == nil {
		return "", ErrNotConfigured
	}
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	ct, err := n.Box.Encrypt(secret)
	if err != nil {
		return "", err
	}
	if _, err := h.Update().SetSigningSecretCiphertext(ct).Save(ctx); err != nil {
		return "", err
	}
	_ = contentstore.Audit(ctx, n.Client, contentstore.AuditEntry{
		ShopID: shopID, Actor: actor, Action: AuditSecretRotate, Detail: map[string]any{"host": h.Host},
	})
	return secret, nil
}

// Test makes one call to the stored URL (single attempt, no retries, does not
// touch the failure counters or the minimum interval) and reports the result
// category. The call goes through the same sender and therefore only to an
// allowlisted host and a checked address.
func (n *Notifier) Test(ctx context.Context, shopID, actor int) (Result, error) {
	n.init()
	h, err := n.load(ctx, shopID)
	if err != nil {
		return Result{}, err
	}
	if h == nil {
		return Result{}, ErrNotConfigured
	}
	res, attempts := n.deliver(ctx, h, trigger{event: EventTest}, 0)
	_ = contentstore.Audit(ctx, n.Client, contentstore.AuditEntry{
		ShopID: shopID, Actor: actor, Action: AuditTest,
		Detail: map[string]any{"host": h.Host, "result": res.Status, "http_status": res.HTTPStatus, "attempts": attempts},
	})
	return res, nil
}

// Fire is the manual rebuild: it sends now (no debounce, no minimum interval),
// takes one call of the daily budget, and updates the state like an automatic
// call.
func (n *Notifier) Fire(ctx context.Context, shopID, actor int) (Result, error) {
	n.init()
	h, err := n.load(ctx, shopID)
	if err != nil {
		return Result{}, err
	}
	if h == nil {
		return Result{}, ErrNotConfigured
	}
	if !h.Enabled {
		return Result{}, ErrDisabled
	}
	now := n.now()
	ok, err := n.reserveCap(ctx, h.ID, now)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, ErrCapReached
	}
	if _, err := h.Update().SetLastAttemptAt(now).Save(ctx); err != nil {
		return Result{}, err
	}
	tr := trigger{event: EventManual}
	res, attempts := n.deliver(ctx, h, tr, n.Cfg.Retries)
	n.record(ctx, h, res, attempts, now, tr, fmt.Sprintf("manual:%d", actor))
	return res, nil
}
