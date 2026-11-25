package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/cryptbox"
	"ksdevworks/ecommerce/api/internal/ent"
	entaudit "ksdevworks/ecommerce/api/internal/ent/contentauditlog"
	entphook "ksdevworks/ecommerce/api/internal/ent/contentpublishhook"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/testutil"
)

const hookURL = "https://api.cloudflare.com/client/v4/workers/builds/deploy_hooks/ab12cd34-ef56-7890-abcd-ef1234567890"

// fakePoster records the calls and answers from a script.
type fakePoster struct {
	mu      sync.Mutex
	script  []Result // consumed in order; the last repeats
	calls   []postedCall
	started chan struct{}
	block   chan struct{}
}

type postedCall struct {
	url     string
	body    []byte
	headers map[string]string
}

func (f *fakePoster) Post(_ context.Context, rawURL string, body []byte, headers map[string]string) Result {
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, postedCall{rawURL, body, headers})
	if len(f.script) == 0 {
		return Result{Status: StatusOK, HTTPStatus: 200}
	}
	i := len(f.calls) - 1
	if i >= len(f.script) {
		i = len(f.script) - 1
	}
	return f.script[i]
}

func (f *fakePoster) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

type hookEnv struct {
	client *ent.Client
	n      *Notifier
	poster *fakePoster
	shop   int
	now    time.Time
	sleeps []time.Duration
	logs   *bytes.Buffer
	box    *cryptbox.Box
}

func (e *hookEnv) advance(d time.Duration) { e.now = e.now.Add(d) }

func newHookEnv(t *testing.T, mut func(*Config)) *hookEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	box, err := cryptbox.ParseKey("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatal(err)
	}
	e := &hookEnv{client: client, poster: &fakePoster{}, logs: &bytes.Buffer{}, box: box,
		now: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)}
	cfg := DefaultConfig()
	cfg.Backoff = 2 * time.Second
	if mut != nil {
		mut(&cfg)
	}
	e.n = &Notifier{Client: client, Box: box, Sender: e.poster, Cfg: cfg,
		Log: slog.New(slog.NewTextHandler(e.logs, nil))}
	e.n.SetClock(func() time.Time { return e.now }, func(_ context.Context, d time.Duration) error {
		e.sleeps = append(e.sleeps, d)
		return nil
	})
	e.shop = client.Shop.Create().SetName("Hook Shop").SaveX(context.Background()).ID
	return e
}

func (e *hookEnv) configure(t *testing.T) {
	t.Helper()
	u := hookURL
	if _, err := e.n.Set(context.Background(), e.shop, 0, SetInput{URL: &u}); err != nil {
		t.Fatalf("set: %v", err)
	}
}

func (e *hookEnv) publish(n int) {
	for i := 0; i < n; i++ {
		e.n.Handle(context.Background(), events.ContentPublished{ShopID: e.shop, Key: "home", Rev: int64(i + 1)})
	}
	e.n.DrainInline(context.Background())
}

func (e *hookEnv) tick(t *testing.T, force bool) TickReport {
	t.Helper()
	rep, err := e.n.Tick(context.Background(), force)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestHookIsStoredEncryptedAndNeverReadBack(t *testing.T) {
	e := newHookEnv(t, nil)
	u := hookURL
	st, err := e.n.Set(context.Background(), e.shop, 0, SetInput{URL: &u})
	if err != nil {
		t.Fatal(err)
	}
	row := e.client.ContentPublishHook.Query().OnlyX(context.Background())
	for _, b := range [][]byte{row.URLCiphertext, row.SigningSecretCiphertext} {
		if bytes.Contains(b, []byte("deploy_hooks")) || bytes.Contains(b, []byte("ab12cd34")) {
			t.Fatal("ciphertext contains the URL in the clear")
		}
	}
	if row.Host != "api.cloudflare.com" || row.URLHint != "7890"[0:0]+hookURL[len(hookURL)-4:] {
		t.Fatalf("host/hint: %q %q", row.Host, row.URLHint)
	}
	secret, err := e.n.RotateSecret(context.Background(), e.shop, 0)
	if err != nil || len(secret) != 64 {
		t.Fatalf("rotate: %v %q", err, secret)
	}
	got, _ := e.n.Status(context.Background(), e.shop)
	for _, v := range []any{st, got} {
		raw, _ := json.Marshal(v)
		s := string(raw)
		if strings.Contains(s, "ab12cd34") || strings.Contains(s, "deploy_hooks") || strings.Contains(s, secret) || strings.Contains(s, "signing") {
			t.Fatalf("status leaks: %s", s)
		}
		if !strings.Contains(s, "api.cloudflare.com") || !strings.Contains(s, "…"+hookURL[len(hookURL)-4:]) {
			t.Fatalf("status lacks host or mask: %s", s)
		}
	}
	// Audit rows and logs carry neither the URL nor the secret.
	rows := e.client.ContentAuditLog.Query().Where(entaudit.ActionHasPrefix("hook.")).AllX(context.Background())
	if len(rows) < 2 {
		t.Fatalf("audit rows: %d", len(rows))
	}
	for _, r := range rows {
		if strings.Contains(string(r.Detail), "ab12cd34") || strings.Contains(string(r.Detail), secret) {
			t.Fatalf("audit leaks: %s", r.Detail)
		}
	}
	if strings.Contains(e.logs.String(), "ab12cd34") || strings.Contains(e.logs.String(), secret) {
		t.Fatalf("log leaks: %s", e.logs.String())
	}
}

func TestSetRejectsDisallowedURLAndKeepsTheOldOne(t *testing.T) {
	e := newHookEnv(t, nil)
	e.configure(t)
	for _, bad := range []string{"http://api.cloudflare.com/x", "https://evil.test/x", "https://10.0.0.1/x", "https://api.cloudflare.com/client/v4/user", "https://u:p@api.cloudflare.com/x"} {
		bad := bad
		if _, err := e.n.Set(context.Background(), e.shop, 0, SetInput{URL: &bad}); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	row := e.client.ContentPublishHook.Query().OnlyX(context.Background())
	plain, _ := e.box.Decrypt(row.URLCiphertext)
	if plain != hookURL {
		t.Fatal("a rejected update changed the stored URL")
	}
	if _, err := e.n.Validate("https://evil.test/x"); err == nil {
		t.Fatal("dry run accepted a bad URL")
	}
	if st, err := e.n.Validate(hookURL); err != nil || st.Host != "api.cloudflare.com" {
		t.Fatalf("dry run: %v %+v", err, st)
	}
	if e.client.ContentPublishHook.Query().CountX(context.Background()) != 1 {
		t.Fatal("dry run wrote a row")
	}
}

func TestDebounceMergesABurstIntoOneSignedCall(t *testing.T) {
	e := newHookEnv(t, nil)
	e.configure(t)
	secret, _ := e.n.RotateSecret(context.Background(), e.shop, 0)
	e.publish(10)
	if r := e.tick(t, false); r.Sent != 0 || e.poster.count() != 0 {
		t.Fatalf("sent before the debounce window: %+v", r)
	}
	e.advance(20 * time.Second)
	e.publish(1) // a late publish pushes the window out
	e.advance(20 * time.Second)
	if e.tick(t, false).Sent != 0 {
		t.Fatal("window was not extended by the late publish")
	}
	e.advance(15 * time.Second)
	if r := e.tick(t, false); r.Sent != 1 {
		t.Fatalf("%+v", r)
	}
	if e.poster.count() != 1 {
		t.Fatalf("calls: %d", e.poster.count())
	}
	c := e.poster.calls[0]
	if c.url != hookURL {
		t.Fatal("the stored URL was not used")
	}
	var body map[string]any
	_ = json.Unmarshal(c.body, &body)
	if body["event"] != EventPublished || body["coalesced"].(float64) != 11 || int(body["shop_id"].(float64)) != e.shop {
		t.Fatalf("body: %s", c.body)
	}
	if _, has := body["content"]; has || strings.Contains(string(c.body), "draft") {
		t.Fatal("body must not carry content")
	}
	ts := e.now.Unix()
	if c.headers[HeaderTimestamp] != "1764068460"[0:0]+itoa(ts) || !Verify([]byte(secret), ts, c.body, c.headers[HeaderSignature]) {
		t.Fatalf("signature does not verify: %v", c.headers)
	}
	// Nothing left pending.
	if e.tick(t, true).Sent != 0 {
		t.Fatal("a second call was made for the same burst")
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestMaxWaitBoundsAContinuousBurst(t *testing.T) {
	e := newHookEnv(t, func(c *Config) { c.MaxWait = 2 * time.Minute })
	e.configure(t)
	for i := 0; i < 8; i++ { // a publish every 20 s keeps the debounce open
		e.publish(1)
		e.advance(20 * time.Second)
		e.tick(t, false)
	}
	if e.poster.count() != 1 {
		t.Fatalf("expected the max wait to flush once, got %d calls", e.poster.count())
	}
}

func TestMinIntervalDefersWithoutLosingTheTrigger(t *testing.T) {
	e := newHookEnv(t, func(c *Config) { c.Debounce = 5 * time.Second; c.MinInterval = 60 * time.Second })
	e.configure(t)
	e.publish(1)
	e.advance(6 * time.Second)
	if e.tick(t, false).Sent != 1 {
		t.Fatal("first call")
	}
	e.publish(1)
	e.advance(6 * time.Second)
	if r := e.tick(t, false); r.Sent != 0 || r.Deferred != 1 {
		t.Fatalf("expected deferral: %+v", r)
	}
	e.advance(60 * time.Second)
	if r := e.tick(t, false); r.Sent != 1 {
		t.Fatalf("the deferred trigger was lost: %+v", r)
	}
	if e.poster.count() != 2 {
		t.Fatalf("calls: %d", e.poster.count())
	}
}

func TestDailyCapDropsAndResetsNextDay(t *testing.T) {
	e := newHookEnv(t, func(c *Config) { c.Debounce = time.Second; c.MinInterval = 0; c.DailyCap = 2 })
	e.configure(t)
	for i := 0; i < 3; i++ {
		e.publish(1)
		e.advance(2 * time.Second)
		e.tick(t, false)
		e.advance(time.Minute)
	}
	if e.poster.count() != 2 {
		t.Fatalf("calls with cap 2: %d", e.poster.count())
	}
	st, _ := e.n.Status(context.Background(), e.shop)
	if st.LastStatus != StatusRateLimited || st.DailyCount != 2 || st.Pending {
		t.Fatalf("status: %+v", st)
	}
	if st.Failed || st.ConsecutiveFailures != 0 || !st.Enabled {
		t.Fatalf("a held-back call must not count as a failure: %+v", st)
	}
	if _, err := e.n.Fire(context.Background(), e.shop, 1); err != ErrCapReached {
		t.Fatalf("manual fire past the cap: %v", err)
	}
	e.advance(24 * time.Hour)
	e.publish(1)
	e.advance(2 * time.Second)
	if e.tick(t, false).Sent != 1 {
		t.Fatal("cap did not reset on the next UTC day")
	}
}

func TestRetriesOnlyTimeoutAnd5xxWithBackoff(t *testing.T) {
	e := newHookEnv(t, func(c *Config) { c.Debounce = time.Second })
	e.configure(t)
	e.poster.script = []Result{{Status: StatusHTTPError, HTTPStatus: 503, Retryable: true}, {Status: StatusTimeout, Retryable: true}, {Status: StatusOK, HTTPStatus: 200}}
	e.publish(1)
	e.advance(2 * time.Second)
	if r := e.tick(t, false); r.Sent != 1 {
		t.Fatalf("%+v", r)
	}
	if e.poster.count() != 3 || len(e.sleeps) != 2 || e.sleeps[0] != 2*time.Second || e.sleeps[1] != 4*time.Second {
		t.Fatalf("attempts %d sleeps %v", e.poster.count(), e.sleeps)
	}
	// A 404 is final: one attempt.
	e2 := newHookEnv(t, func(c *Config) { c.Debounce = time.Second })
	e2.configure(t)
	e2.poster.script = []Result{{Status: StatusHTTPError, HTTPStatus: 404}}
	e2.publish(1)
	e2.advance(2 * time.Second)
	if r := e2.tick(t, false); r.Failed != 1 || e2.poster.count() != 1 || len(e2.sleeps) != 0 {
		t.Fatalf("%+v calls %d", r, e2.poster.count())
	}
	// Retries are bounded: always-503 stops after 1 + 2 attempts.
	e3 := newHookEnv(t, func(c *Config) { c.Debounce = time.Second })
	e3.configure(t)
	e3.poster.script = []Result{{Status: StatusHTTPError, HTTPStatus: 503, Retryable: true}}
	e3.publish(1)
	e3.advance(2 * time.Second)
	e3.tick(t, false)
	if e3.poster.count() != 3 {
		t.Fatalf("attempts: %d", e3.poster.count())
	}
}

func TestFiveConsecutiveFailuresDisableTheHookVisibly(t *testing.T) {
	e := newHookEnv(t, func(c *Config) { c.Debounce = time.Second; c.MinInterval = 0; c.DailyCap = 100 })
	e.configure(t)
	e.poster.script = []Result{{Status: StatusHTTPError, HTTPStatus: 404}}
	for i := 0; i < 5; i++ {
		e.publish(1)
		e.advance(2 * time.Second)
		e.tick(t, false)
		e.advance(time.Second)
	}
	st, _ := e.n.Status(context.Background(), e.shop)
	if st.Enabled || !st.Failed || st.DisabledReason != DisabledFailures || st.ConsecutiveFailures != 5 || st.LastHTTPStatus != 404 {
		t.Fatalf("status after five failures: %+v", st)
	}
	n := e.client.ContentAuditLog.Query().Where(entaudit.ActionEQ(AuditDisable)).CountX(context.Background())
	if n != 1 {
		t.Fatalf("disable audit rows: %d", n)
	}
	// A disabled hook records nothing and sends nothing.
	before := e.poster.count()
	e.publish(1)
	e.advance(time.Minute)
	e.tick(t, true)
	if e.poster.count() != before {
		t.Fatal("a disabled hook was called")
	}
	// Re-enabling resets the counter.
	on := true
	st, err := e.n.Set(context.Background(), e.shop, 1, SetInput{Enabled: &on})
	if err != nil || !st.Enabled || st.ConsecutiveFailures != 0 || st.Failed {
		t.Fatalf("re-enable: %v %+v", err, st)
	}
}

func TestSuccessResetsTheFailureCounter(t *testing.T) {
	e := newHookEnv(t, func(c *Config) { c.Debounce = time.Second; c.MinInterval = 0 })
	e.configure(t)
	e.poster.script = []Result{{Status: StatusHTTPError, HTTPStatus: 404}, {Status: StatusHTTPError, HTTPStatus: 404}, {Status: StatusOK, HTTPStatus: 200}}
	for i := 0; i < 3; i++ {
		e.publish(1)
		e.advance(2 * time.Second)
		e.tick(t, false)
		e.advance(time.Second)
	}
	st, _ := e.n.Status(context.Background(), e.shop)
	if st.ConsecutiveFailures != 0 || st.LastStatus != StatusOK || st.LastSuccessAt == nil || !st.Enabled {
		t.Fatalf("%+v", st)
	}
}

func TestTwoFlushersSendOnce(t *testing.T) {
	e := newHookEnv(t, func(c *Config) { c.Debounce = time.Second })
	e.configure(t)
	second := &Notifier{Client: e.client, Box: e.box, Sender: e.poster, Cfg: e.n.Cfg, Log: e.n.Log}
	second.SetClock(func() time.Time { return e.now }, nil)
	e.publish(3)
	e.advance(2 * time.Second)
	var wg sync.WaitGroup
	for _, n := range []*Notifier{e.n, second} {
		n := n
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = n.Tick(context.Background(), false) }()
	}
	wg.Wait()
	if e.poster.count() != 1 {
		t.Fatalf("two instances sent %d calls", e.poster.count())
	}
}

func TestHandleNeverBlocksAndDropsWhenTheQueueIsFull(t *testing.T) {
	e := newHookEnv(t, func(c *Config) { c.QueueSize = 2 })
	e.configure(t)
	start := time.Now()
	for i := 0; i < 50; i++ { // no writer started: the queue fills up
		e.n.Handle(context.Background(), events.ContentPublished{ShopID: e.shop, Key: "home"})
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("Handle blocked")
	}
	if !strings.Contains(e.logs.String(), "queue is full") {
		t.Fatal("drops must be logged")
	}
	// Other events are ignored.
	e.n.Handle(context.Background(), events.PagePublished{ShopID: e.shop, Slug: "x"})
	e.n.DrainInline(context.Background())
	row := e.client.ContentPublishHook.Query().OnlyX(context.Background())
	if row.PendingCount != 2 {
		t.Fatalf("expected exactly the two queued triggers to be recorded, got %d", row.PendingCount)
	}
}

func TestHandleDoesNotWaitForASlowReceiver(t *testing.T) {
	e := newHookEnv(t, func(c *Config) { c.Debounce = time.Millisecond; c.FlushInterval = 10 * time.Millisecond })
	e.configure(t)
	e.poster.block = make(chan struct{})
	e.poster.started = make(chan struct{}, 10)
	e.n.Start()
	defer func() { close(e.poster.block); _ = e.n.Close() }()
	e.n.nowFn = nil // real clock for the background flusher
	e.n.Handle(context.Background(), events.ContentPublished{ShopID: e.shop, Key: "home"})
	select {
	case <-e.poster.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the flusher never called the receiver")
	}
	start := time.Now()
	for i := 0; i < 100; i++ {
		e.n.Handle(context.Background(), events.ContentPublished{ShopID: e.shop, Key: "home"})
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("publishing was slowed down by a hanging receiver: %s", d)
	}
}

func TestTestCallAndManualFireAndNoHookIsANoop(t *testing.T) {
	e := newHookEnv(t, func(c *Config) { c.DailyCap = 3 })
	if _, err := e.n.Test(context.Background(), e.shop, 1); err != ErrNotConfigured {
		t.Fatalf("test without hook: %v", err)
	}
	// Events for a shop without a hook cost nothing and create nothing.
	e.publish(2)
	if e.client.ContentPublishHook.Query().CountX(context.Background()) != 0 {
		t.Fatal("a row appeared")
	}
	e.configure(t)
	e.poster.script = []Result{{Status: StatusHTTPError, HTTPStatus: 500, Retryable: true}}
	res, err := e.n.Test(context.Background(), e.shop, 7)
	if err != nil || res.Status != StatusHTTPError || e.poster.count() != 1 {
		t.Fatalf("test call must be a single attempt: %v %+v calls=%d", err, res, e.poster.count())
	}
	var body map[string]any
	_ = json.Unmarshal(e.poster.calls[0].body, &body)
	if body["event"] != EventTest {
		t.Fatalf("event: %v", body["event"])
	}
	row := e.client.ContentPublishHook.Query().OnlyX(context.Background())
	if row.ConsecutiveFailures != 0 || row.CapCount != 0 || row.LastAttemptAt != nil {
		t.Fatalf("the test call must not touch counters: %+v", row)
	}
	e.poster.script = []Result{{Status: StatusOK, HTTPStatus: 200}}
	res, err = e.n.Fire(context.Background(), e.shop, 7)
	if err != nil || !res.OK() {
		t.Fatalf("fire: %v %+v", err, res)
	}
	row = e.client.ContentPublishHook.Query().OnlyX(context.Background())
	if row.CapCount != 1 || row.LastSuccessAt == nil {
		t.Fatalf("fire state: %+v", row)
	}
	off := false
	_, _ = e.n.Set(context.Background(), e.shop, 7, SetInput{Enabled: &off})
	if _, err := e.n.Fire(context.Background(), e.shop, 7); err != ErrDisabled {
		t.Fatalf("fire while disabled: %v", err)
	}
	if err := e.n.Delete(context.Background(), e.shop, 7); err != nil {
		t.Fatal(err)
	}
	if err := e.n.Delete(context.Background(), e.shop, 7); err != ErrNotConfigured {
		t.Fatalf("second delete: %v", err)
	}
}

func TestUnreachableOrDisallowedReceiverIsRecordedAsFailure(t *testing.T) {
	e := newHookEnv(t, func(c *Config) { c.Debounce = time.Second })
	e.configure(t)
	e.poster.script = []Result{{Status: StatusBlocked}}
	e.publish(1)
	e.advance(2 * time.Second)
	e.tick(t, false)
	st, _ := e.n.Status(context.Background(), e.shop)
	if st.LastStatus != StatusBlocked || st.ConsecutiveFailures != 1 {
		t.Fatalf("%+v", st)
	}
	if e.poster.count() != 1 {
		t.Fatal("a blocked result must not be retried")
	}
}

var _ = atomic.Int32{}
var _ = entphook.FieldShopID
