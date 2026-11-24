package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/account"
	"ksdevworks/ecommerce/api/internal/application"
	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/permission"
	"ksdevworks/ecommerce/api/internal/ent/shop"
	"ksdevworks/ecommerce/api/internal/ent/shopapplication"
	"ksdevworks/ecommerce/api/internal/ent/shopapplicationevent"
	"ksdevworks/ecommerce/api/internal/ent/shopapplicationmessage"
	"ksdevworks/ecommerce/api/internal/ent/shopapplicationtoken"
	"ksdevworks/ecommerce/api/internal/ent/user"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/provision"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/seed"
	"ksdevworks/ecommerce/api/internal/testutil"
	"ksdevworks/ecommerce/api/internal/turnstile"
)

// Throwaway test constants (change platform-website).
const (
	appWebsite   = "https://www.example.test"
	appAdminMail = "root-admin@t.dev"
	appAdminPw   = "seed-admin-pw-for-tests"
	appConsent   = "terms-test-v1"
	// Distinctive values the PII log test searches for.
	piiEmail = "pii.probe.owner@probe-domain.test"
	piiName  = "Zyxwvu Probename"
	piiPhone = "0955123987"
	piiShop  = "Quuxlandia Boutique"
)

type appOpts struct {
	noLimiter  bool
	broad      int
	tight      int
	emailLimit int
	cooldown   time.Duration
	dailyCap   int
	notifier   application.Notifier
}

type appEnv struct {
	client  *ent.Client
	router  http.Handler
	svc     *application.Service
	issuer  *auth.TokenIssuer
	engine  *rbac.Engine
	ts      *turnstile.StubVerifier
	logBuf  *bytes.Buffer
	adminID int
	clock   time.Time
	mu      sync.Mutex
}

func (e *appEnv) now() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.clock
}

func (e *appEnv) advance(d time.Duration) {
	e.mu.Lock()
	e.clock = e.clock.Add(d)
	e.mu.Unlock()
}

func newAppEnv(t *testing.T, o appOpts) *appEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	if err := seed.Run(ctx, client, seed.Options{AdminEmail: appAdminMail, AdminPassword: appAdminPw}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if o.broad == 0 {
		o.broad = 1000
	}
	if o.tight == 0 {
		o.tight = 1000
	}
	if o.emailLimit == 0 {
		o.emailLimit = 1000
	}
	if o.cooldown == 0 {
		o.cooldown = 30 * 24 * time.Hour
	}
	if o.dailyCap == 0 {
		o.dailyCap = 1000
	}
	e := &appEnv{client: client, clock: time.Now().UTC().Truncate(time.Second), logBuf: &bytes.Buffer{}, ts: &turnstile.StubVerifier{}}
	e.adminID = client.User.Query().Where(user.EmailEQ(appAdminMail)).OnlyX(ctx).ID

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	log := slog.New(slog.NewTextHandler(e.logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	engine := rbac.NewEngine(client, rdb, log)
	e.engine = engine
	authz := &httpapi.AuthzMW{Engine: engine}
	dispatcher := events.NewDispatcher(log)
	accounts := &account.Service{
		Client: client, Engine: engine, Refresh: auth.NewRefreshService(client, time.Hour),
		Delivery: account.ManualDelivery{}, PublicURL: acctPublicURL,
		SetupTTL: 72 * time.Hour, ResetTTL: 24 * time.Hour, InviteTTL: 7 * 24 * time.Hour,
	}
	prov := &provision.Service{Client: client, CMS: &cms.Service{Client: client, Dispatcher: dispatcher}, Accounts: accounts, Dispatcher: dispatcher}
	var notifier application.Notifier = application.OutboxNotifier{}
	if o.notifier != nil {
		notifier = o.notifier
	}
	e.svc = &application.Service{
		Client: client, Notifier: notifier, Provision: prov, Log: log, Now: e.now,
		Cfg: application.Config{
			WebsiteURL: appWebsite, ConsentVersion: appConsent, ReapplyCooldown: o.cooldown,
			DailyCap: o.dailyCap, RejectedRetention: 90 * 24 * time.Hour, ApprovedRetention: 365 * 24 * time.Hour,
			IPKey: []byte("test-ip-key"),
		},
	}
	var limiter *ratelimit.Limiter
	if !o.noLimiter {
		limiter = ratelimit.New(rdb, log)
	} else {
		limiter = ratelimit.New(nil, log) // fails open, like a Redis outage
	}
	rl := config.RateLimitConfig{
		ApplicationBroadLimit: o.broad, ApplicationBroadWindow: time.Minute,
		ApplicationTightLimit: o.tight, ApplicationTightWindow: time.Minute,
	}
	e.router = httpapi.New(httpapi.Deps{
		Cfg:       &config.Config{WebsiteURL: appWebsite, CORSAllowedOrigins: []string{"https://admin.example.test"}},
		Log:       log,
		AdminMW:   httpapi.NewAdminMiddleware(issuer),
		AdminAuth: &httpapi.AdminAuthHandler{Client: client, Issuer: issuer, Refresh: auth.NewRefreshService(client, time.Hour), Engine: engine, Log: log},
		ApplicationsPublic: &httpapi.ApplicationsPublicHandler{
			Service: e.svc, Turnstile: e.ts, Limiter: limiter,
			EmailLimit: o.emailLimit, EmailWindow: time.Hour, Log: log,
		},
		ApplicationRateLimit:       httpapi.NewApplicationRateLimit(limiter, rl),
		ApplicationVerifyRateLimit: httpapi.NewApplicationVerifyRateLimit(limiter, rl),
		Applications:               &httpapi.ApplicationsHandler{Service: e.svc, Engine: engine, Authz: authz, Log: log},
		Provisioning:               &httpapi.ProvisioningHandler{Service: prov, Engine: engine, Authz: authz, Log: log},
	})
	return e
}

func validApp(email string) map[string]any {
	return map[string]any{
		"email": email, "contact_name": "王小明", "shop_name": "小明花坊", "phone": "0912-345-678",
		"business_summary": "我們販售手作乾燥花束。", "consent_version": appConsent,
		"turnstile_token": "tok", "website": "",
	}
}

type resp struct {
	code int
	raw  string
	hdr  http.Header
}

func (e *appEnv) req(method, path, token, ip string, body any, hdr map[string]string) resp {
	var buf bytes.Buffer
	if s, ok := body.(string); ok {
		buf.WriteString(s)
	} else if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if ip != "" {
		r.RemoteAddr = ip + ":4321"
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, r)
	return resp{rec.Code, rec.Body.String(), rec.Result().Header}
}

func (e *appEnv) submit(body any, ip string) resp {
	return e.req("POST", "/api/v1/public/shop-applications", "", ip, body, nil)
}

func (e *appEnv) adminTok(t *testing.T) string {
	t.Helper()
	tok, err := e.issuer.IssueAdmin(e.adminID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

var tokenRe = regexp.MustCompile(`#([A-Za-z0-9_-]{20,})`)

// verifyToken returns the newest verification token found in the outbox.
func (e *appEnv) verifyToken(t *testing.T, email string) string {
	t.Helper()
	msg, err := e.client.ShopApplicationMessage.Query().
		Where(shopapplicationmessage.RecipientEmailEQ(email), shopapplicationmessage.KindEQ("verify_email")).
		Order(ent.Desc(shopapplicationmessage.FieldID)).First(context.Background())
	if err != nil {
		t.Fatalf("no verify message for %s: %v", email, err)
	}
	m := tokenRe.FindStringSubmatch(msg.BodyText)
	if m == nil {
		t.Fatalf("no token in body %q", msg.BodyText)
	}
	return m[1]
}

func (e *appEnv) appByEmail(t *testing.T, email string) *ent.ShopApplication {
	t.Helper()
	return e.client.ShopApplication.Query().Where(shopapplication.EmailEQ(email)).Order(ent.Desc(shopapplication.FieldID)).FirstX(context.Background())
}

func (e *appEnv) count(email string) int {
	return e.client.ShopApplication.Query().Where(shopapplication.EmailEQ(email)).CountX(context.Background())
}

func (e *appEnv) verify(token string) resp {
	return e.req("POST", "/api/v1/public/shop-applications/verify", "", "", map[string]any{"token": token}, nil)
}

// readyApplication submits and verifies, returning the application id.
func (e *appEnv) readyApplication(t *testing.T, email string) int {
	t.Helper()
	if r := e.submit(validApp(email), ""); r.code != 202 {
		t.Fatalf("submit: %d %s", r.code, r.raw)
	}
	if r := e.verify(e.verifyToken(t, email)); r.code != 200 {
		t.Fatalf("verify: %d %s", r.code, r.raw)
	}
	return e.appByEmail(t, email).ID
}

// ── public intake ─────────────────────────────────────────────────

func TestApplicationSubmitCreatesRowTokenAndOutboxMessage(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	r := e.submit(validApp("new@a.test"), "198.51.100.7")
	if r.code != 202 || strings.TrimSpace(r.raw) != `{"status":"received"}` {
		t.Fatalf("submit: %d %q", r.code, r.raw)
	}
	app := e.appByEmail(t, "new@a.test")
	if app.Status != "pending_verification" || app.ContactName != "王小明" || app.ConsentVersion != appConsent {
		t.Fatalf("app = %+v", app)
	}
	if app.SubmittedIPHmac == "" || strings.Contains(app.SubmittedIPHmac, "198.51.100.7") {
		t.Fatalf("ip must be stored only as an HMAC, got %q", app.SubmittedIPHmac)
	}
	msg := e.client.ShopApplicationMessage.Query().OnlyX(context.Background())
	if msg.Kind != "verify_email" || msg.Delivery != "manual" || msg.Status != "pending_manual" ||
		!strings.Contains(msg.BodyText, appWebsite+"/apply/verify#") {
		t.Fatalf("outbox message = %+v", msg)
	}
	if e.ts.LastToken != "tok" || e.ts.LastIP != "198.51.100.7" {
		t.Fatalf("turnstile saw token %q ip %q", e.ts.LastToken, e.ts.LastIP)
	}
	// Only the hash of the token is stored.
	plain := e.verifyToken(t, "new@a.test")
	tok := e.client.ShopApplicationToken.Query().OnlyX(context.Background())
	if tok.TokenHash == plain || tok.TokenHash != account.HashToken(plain) {
		t.Fatal("token must be stored as sha256 only")
	}
}

// Scenario: 新 email、重複、冷卻中、已是後台帳號 → 回應位元組相同.
func TestApplicationResponsesAreByteIdenticalForEveryOutcome(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	ctx := context.Background()

	// A rejected applicant (cooling down) and an existing back-office account.
	id := e.readyApplication(t, "rejected@a.test")
	if r := e.req("POST", fmt.Sprintf("/api/v1/admin/shop-applications/%d/reject", id), e.adminTok(t), "",
		map[string]any{"decision_reason": "not now"}, nil); r.code != 200 {
		t.Fatalf("reject: %d %s", r.code, r.raw)
	}
	hash, _ := auth.HashPassword("whatever-long-password")
	e.client.User.Create().SetEmail("hasaccount@a.test").SetPasswordHash(hash).SaveX(ctx)

	base := e.submit(validApp("fresh@a.test"), "")
	dup := e.submit(validApp("fresh@a.test"), "")
	cool := e.submit(validApp("rejected@a.test"), "")
	acct := e.submit(validApp("hasaccount@a.test"), "")
	for name, r := range map[string]resp{"duplicate": dup, "cooldown": cool, "account": acct} {
		if r.code != base.code || r.raw != base.raw || r.hdr.Get("Content-Type") != base.hdr.Get("Content-Type") {
			t.Errorf("%s response differs: %d %q vs %d %q", name, r.code, r.raw, base.code, base.raw)
		}
	}
	if e.count("fresh@a.test") != 1 {
		t.Error("duplicate must not create a second row")
	}
	if e.count("rejected@a.test") != 1 {
		t.Error("cooling-down e-mail must not create a new row")
	}
	// The reviewer is told about the existing account.
	acctApp := e.appByEmail(t, "hasaccount@a.test")
	if !acctApp.RiskFlags[application.FlagEmailHasAccount] {
		t.Errorf("risk flags = %v", acctApp.RiskFlags)
	}
}

func TestApplicationValidationAndHoneypot(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	mutate := func(k string, v any) map[string]any { m := validApp("v@a.test"); m[k] = v; return m }
	cases := map[string]map[string]any{
		"honeypot filled":  mutate("website", "http://spam.test"),
		"consent mismatch": mutate("consent_version", "other"),
		"consent missing":  mutate("consent_version", ""),
		"bad email":        mutate("email", "not-an-email"),
		"empty name":       mutate("contact_name", "  "),
		"crlf in name":     mutate("contact_name", "Bob\r\nBcc: x@y.z"),
		"long name":        mutate("contact_name", strings.Repeat("名", 101)),
		"long shop name":   mutate("shop_name", strings.Repeat("a", 101)),
		"empty shop name":  mutate("shop_name", "\r\n"),
		"bad phone":        mutate("phone", "call me"),
		"long phone":       mutate("phone", strings.Repeat("1", 21)),
		"empty summary":    mutate("business_summary", ""),
		"long summary":     mutate("business_summary", strings.Repeat("好", 1001)),
		"long email":       mutate("email", strings.Repeat("a", 250)+"@b.test"),
	}
	for name, body := range cases {
		r := e.submit(body, "")
		if r.code != 422 {
			t.Errorf("%s: %d %s", name, r.code, r.raw)
		}
	}
	if n := e.client.ShopApplication.Query().CountX(context.Background()); n != 0 {
		t.Errorf("%d rows created by invalid input", n)
	}
	if r := e.submit(`{not json`, ""); r.code != 400 {
		t.Errorf("malformed json: %d", r.code)
	}
	// Exactly 1000 runes and a control-stripped shop name are fine.
	ok := validApp("edge@a.test")
	ok["business_summary"] = strings.Repeat("好", 1000)
	ok["shop_name"] = "Shop\x00 Name"
	if r := e.submit(ok, ""); r.code != 202 {
		t.Fatalf("edge: %d %s", r.code, r.raw)
	}
	if got := e.appByEmail(t, "edge@a.test").ShopName; got != "Shop Name" {
		t.Errorf("shop name = %q", got)
	}
	// Unknown fields are ignored; e-mail is normalized.
	u := validApp("  Mixed.Case@A.Test ")
	u["extra"] = "ignored"
	if r := e.submit(u, ""); r.code != 202 {
		t.Fatalf("unknown field: %d %s", r.code, r.raw)
	}
	if e.count("mixed.case@a.test") != 1 {
		t.Error("e-mail not normalized")
	}
}

func TestApplicationBodyTooLarge(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	big := validApp("big@a.test")
	big["pad"] = strings.Repeat("x", httpapi.MaxApplicationBody)
	if r := e.submit(big, ""); r.code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: %d %s", r.code, r.raw)
	}
	// Chunked body without a Content-Length is cut off as well.
	r := httptest.NewRequest("POST", "/api/v1/public/shop-applications", strings.NewReader(`{"pad":"`+strings.Repeat("y", httpapi.MaxApplicationBody)+`"}`))
	r.ContentLength = -1
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, r)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("unknown length oversize: %d", rec.Code)
	}
	if e.client.ShopApplication.Query().CountX(context.Background()) != 0 {
		t.Fatal("row created")
	}
}

func TestApplicationTurnstileFailureAndOutage(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	e.ts.Err = turnstile.ErrInvalid
	if r := e.submit(validApp("t@a.test"), ""); r.code != 422 || !strings.Contains(r.raw, "/turnstile_token") {
		t.Fatalf("invalid token: %d %s", r.code, r.raw)
	}
	e.ts.Err = fmt.Errorf("%w: boom", turnstile.ErrUnavailable)
	if r := e.submit(validApp("t@a.test"), ""); r.code != 503 || !strings.Contains(r.raw, "verification_unavailable") {
		t.Fatalf("outage must be 503, got %d %s", r.code, r.raw)
	}
	if e.count("t@a.test") != 0 {
		t.Fatal("a request without a verified token must never create data")
	}
	e.ts.Err = nil
	if r := e.submit(validApp("t@a.test"), ""); r.code != 202 {
		t.Fatalf("recovered: %d", r.code)
	}
	// Invalid input is rejected before Turnstile is consulted.
	calls := e.ts.Calls
	bad := validApp("t2@a.test")
	bad["shop_name"] = ""
	e.submit(bad, "")
	if e.ts.Calls != calls {
		t.Error("validation failures must not spend a Turnstile verification")
	}
}

func TestApplicationRateLimits(t *testing.T) {
	t.Run("per-ip broad", func(t *testing.T) {
		e := newAppEnv(t, appOpts{broad: 3})
		for i := 0; i < 3; i++ {
			if r := e.submit(validApp(fmt.Sprintf("b%d@a.test", i)), "203.0.113.5"); r.code != 202 {
				t.Fatalf("#%d: %d", i, r.code)
			}
		}
		r := e.submit(validApp("b9@a.test"), "203.0.113.5")
		if r.code != 429 || r.hdr.Get("Retry-After") == "" {
			t.Fatalf("4th: %d retry-after=%q", r.code, r.hdr.Get("Retry-After"))
		}
		// Another IP is unaffected.
		if r := e.submit(validApp("b9@a.test"), "203.0.113.6"); r.code != 202 {
			t.Fatalf("other ip: %d", r.code)
		}
	})
	t.Run("per-ip-and-email tight", func(t *testing.T) {
		e := newAppEnv(t, appOpts{tight: 2})
		for i := 0; i < 2; i++ {
			e.submit(validApp("same@a.test"), "203.0.113.5")
		}
		if r := e.submit(validApp("same@a.test"), "203.0.113.5"); r.code != 429 {
			t.Fatalf("tight: %d", r.code)
		}
		if r := e.submit(validApp("other@a.test"), "203.0.113.5"); r.code != 202 {
			t.Fatalf("different e-mail from same ip: %d", r.code)
		}
	})
	t.Run("per-email across IPs", func(t *testing.T) {
		e := newAppEnv(t, appOpts{emailLimit: 2})
		for i := 0; i < 2; i++ {
			if r := e.submit(validApp("victim@a.test"), fmt.Sprintf("203.0.113.%d", 10+i)); r.code != 202 {
				t.Fatalf("#%d: %d", i, r.code)
			}
		}
		r := e.submit(validApp("victim@a.test"), "203.0.113.99")
		if r.code != 429 || r.hdr.Get("Retry-After") == "" {
			t.Fatalf("3rd from a new ip: %d %q", r.code, r.hdr.Get("Retry-After"))
		}
		// The per-e-mail quota is only consumed after a valid Turnstile token.
		e.ts.Err = turnstile.ErrInvalid
		for i := 0; i < 5; i++ {
			e.submit(validApp("untouched@a.test"), fmt.Sprintf("203.0.113.%d", 50+i))
		}
		e.ts.Err = nil
		if r := e.submit(validApp("untouched@a.test"), "203.0.113.70"); r.code != 202 {
			t.Fatalf("bot traffic burned a victim's quota: %d", r.code)
		}
	})
	t.Run("verify endpoint per-ip", func(t *testing.T) {
		e := newAppEnv(t, appOpts{broad: 2})
		for i := 0; i < 2; i++ {
			if r := e.req("POST", "/api/v1/public/shop-applications/verify", "", "203.0.113.5", map[string]any{"token": "x"}, nil); r.code != 404 {
				t.Fatalf("#%d: %d", i, r.code)
			}
		}
		if r := e.req("POST", "/api/v1/public/shop-applications/verify", "", "203.0.113.5", map[string]any{"token": "x"}, nil); r.code != 429 {
			t.Fatalf("3rd verify: %d", r.code)
		}
	})
	t.Run("daily cap", func(t *testing.T) {
		e := newAppEnv(t, appOpts{dailyCap: 2})
		e.submit(validApp("d1@a.test"), "")
		e.submit(validApp("d2@a.test"), "")
		if r := e.submit(validApp("d3@a.test"), ""); r.code != 429 {
			t.Fatalf("cap: %d", r.code)
		}
		e.advance(25 * time.Hour)
		if r := e.submit(validApp("d3@a.test"), ""); r.code != 202 {
			t.Fatalf("next day: %d", r.code)
		}
	})
}

// Scenario: Redis 故障時的資料庫後盾.
func TestApplicationDatabaseBackstopWhenLimiterFailsOpen(t *testing.T) {
	e := newAppEnv(t, appOpts{noLimiter: true, broad: 1, tight: 1, emailLimit: 1})
	for i := 0; i < 5; i++ {
		if r := e.submit(validApp("race@a.test"), ""); r.code != 202 {
			t.Fatalf("#%d: %d", i, r.code)
		}
	}
	ctx := context.Background()
	if n := e.count("race@a.test"); n != 1 {
		t.Fatalf("%d open applications for one e-mail", n)
	}
	if n := e.client.ShopApplicationToken.Query().CountX(ctx); n != 1 {
		t.Fatalf("%d tokens minted within 10 minutes", n)
	}
	// Concurrent first submissions: still exactly one row.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); e.submit(validApp("conc@a.test"), "") }()
	}
	wg.Wait()
	if n := e.count("conc@a.test"); n != 1 {
		t.Fatalf("concurrent submits created %d rows", n)
	}
}

func TestApplicationResendOnlyAfterTenMinutesAndRetiresOldToken(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	e.submit(validApp("re@a.test"), "")
	first := e.verifyToken(t, "re@a.test")
	e.advance(9 * time.Minute)
	e.submit(validApp("re@a.test"), "")
	if n := e.client.ShopApplicationToken.Query().CountX(context.Background()); n != 1 {
		t.Fatalf("resent too early: %d tokens", n)
	}
	e.advance(2 * time.Minute)
	e.submit(validApp("re@a.test"), "")
	second := e.verifyToken(t, "re@a.test")
	if first == second {
		t.Fatal("no new token")
	}
	if r := e.verify(first); r.code != 404 {
		t.Fatalf("old token still works: %d", r.code)
	}
	if r := e.verify(second); r.code != 200 {
		t.Fatalf("new token: %d %s", r.code, r.raw)
	}
	if got := e.appByEmail(t, "re@a.test"); got.Status != "pending_review" || got.EmailVerifiedAt == nil {
		t.Fatalf("app = %+v", got)
	}
}

// ── verification ──────────────────────────────────────────────────

func TestApplicationVerifyTokenRules(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	e.submit(validApp("v@a.test"), "")
	tok := e.verifyToken(t, "v@a.test")
	for name, bad := range map[string]string{"unknown": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "empty": "", "huge": strings.Repeat("a", 4000)} {
		if r := e.verify(bad); r.code != 404 {
			t.Errorf("%s: %d", name, r.code)
		}
	}
	notFound := e.verify("zzz")
	if r := e.verify(tok); r.code != 200 || strings.TrimSpace(r.raw) != `{"status":"verified"}` {
		t.Fatalf("verify: %d %s", r.code, r.raw)
	}
	again := e.verify(tok)
	if again.code != 404 || again.raw != notFound.raw {
		t.Fatalf("reuse: %d %s (want the same 404 as unknown)", again.code, again.raw)
	}
}

func TestApplicationVerifyExpiredToken(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	e.submit(validApp("x@a.test"), "")
	tok := e.verifyToken(t, "x@a.test")
	e.advance(25 * time.Hour)
	if r := e.verify(tok); r.code != 404 {
		t.Fatalf("expired token: %d", r.code)
	}
	if e.appByEmail(t, "x@a.test").Status != "pending_verification" {
		t.Fatal("status changed")
	}
}

// Scenario: 並發 verify 恰好一個成功.
func TestApplicationConcurrentVerifyExactlyOneWins(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	e.submit(validApp("c@a.test"), "")
	tok := e.verifyToken(t, "c@a.test")
	var ok, notFound int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch r := e.verify(tok); r.code {
			case 200:
				atomic.AddInt32(&ok, 1)
			case 404:
				atomic.AddInt32(&notFound, 1)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || notFound != 11 {
		t.Fatalf("ok=%d notFound=%d", ok, notFound)
	}
	n := e.client.ShopApplicationEvent.Query().Where(shopapplicationevent.EventEQ("verified")).CountX(context.Background())
	if n != 1 {
		t.Fatalf("%d verified events", n)
	}
}

// ── CORS ──────────────────────────────────────────────────────────

func TestApplicationCORSIsWebsiteOriginOnly(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	pre := func(path, origin string) resp {
		return e.req("OPTIONS", path, "", "", nil, map[string]string{
			"Origin": origin, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "content-type",
		})
	}
	r := pre("/api/v1/public/shop-applications", appWebsite)
	if r.code != 204 || r.hdr.Get("Access-Control-Allow-Origin") != appWebsite {
		t.Fatalf("website preflight: %d %v", r.code, r.hdr)
	}
	if r.hdr.Get("Access-Control-Allow-Credentials") != "" {
		t.Error("credentials must not be allowed")
	}
	for _, o := range []string{"https://evil.test", "https://admin.example.test", "http://www.example.test"} {
		if r := pre("/api/v1/public/shop-applications", o); r.hdr.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("origin %s was granted CORS on the public API", o)
		}
	}
	// The website gets nothing on any other API surface.
	for _, p := range []string{"/api/v1/shop/products", "/api/v1/admin/me", "/api/v1/render/page"} {
		if r := pre(p, appWebsite); r.hdr.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("website origin granted CORS on %s", p)
		}
	}
	// A real response carries the header too.
	r = e.req("POST", "/api/v1/public/shop-applications", "", "", validApp("cors@a.test"), map[string]string{"Origin": appWebsite})
	if r.code != 202 || r.hdr.Get("Access-Control-Allow-Origin") != appWebsite {
		t.Fatalf("actual request: %d %v", r.code, r.hdr)
	}
}

// ── review: permissions ───────────────────────────────────────────

func TestApplicationReviewRoutesNeedPlatformPermissions(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	ctx := context.Background()
	id := e.readyApplication(t, "perm@a.test")
	base := "/api/v1/admin/shop-applications"
	paths := []struct{ method, path string }{
		{"GET", base}, {"GET", fmt.Sprintf("%s/%d", base, id)},
		{"POST", fmt.Sprintf("%s/%d/approve", base, id)}, {"POST", fmt.Sprintf("%s/%d/reject", base, id)},
		{"POST", fmt.Sprintf("%s/%d/verify-manually", base, id)}, {"POST", fmt.Sprintf("%s/%d/messages/1/mark-sent", base, id)},
	}
	for _, p := range paths {
		if r := e.req(p.method, p.path, "", "", map[string]any{}, nil); r.code != 401 {
			t.Errorf("%s %s without a token: %d", p.method, p.path, r.code)
		}
	}
	// A merchant owner (shop-level role) has nothing here.
	shopID := e.client.Shop.Create().SetName("S").SetStatus(1).SaveX(ctx).ID
	hash, _ := auth.HashPassword("long-enough-password")
	owner := e.client.User.Create().SetEmail("owner@s.test").SetPasswordHash(hash).SaveX(ctx)
	ownerRole := e.client.Role.Query().Where().AllX(ctx)
	for _, rl := range ownerRole {
		if rl.Name == "merchant_owner" {
			e.client.RoleUser.Create().SetUserID(owner.ID).SetRoleID(rl.ID).SetShopID(shopID).SaveX(ctx)
		}
	}
	ownerTok, _ := e.issuer.IssueAdmin(owner.ID, []int{shopID})
	for _, p := range paths {
		if r := e.req(p.method, p.path, ownerTok, "", map[string]any{}, nil); r.code != 403 {
			t.Errorf("merchant owner %s %s: %d", p.method, p.path, r.code)
		}
	}
	// view-only: reads work, writes do not.
	viewer := e.platformUser(t, "viewer@p.test", "shop_application.view")
	vt, _ := e.issuer.IssueAdmin(viewer, nil)
	if r := e.req("GET", base, vt, "", nil, nil); r.code != 200 {
		t.Errorf("viewer list: %d", r.code)
	}
	if r := e.req("POST", fmt.Sprintf("%s/%d/reject", base, id), vt, "", map[string]any{"decision_reason": "x"}, nil); r.code != 403 {
		t.Errorf("viewer reject: %d", r.code)
	}
	// review without account.manage / shop.create cannot approve.
	for _, missing := range []string{"account.manage", "shop.create"} {
		perms := []string{"shop_application.view", "shop_application.review", "account.manage", "shop.create"}
		var keep []string
		for _, p := range perms {
			if p != missing {
				keep = append(keep, p)
			}
		}
		uid := e.platformUser(t, "no-"+missing+"@p.test", keep...)
		tok, _ := e.issuer.IssueAdmin(uid, nil)
		if r := e.req("POST", fmt.Sprintf("%s/%d/approve", base, id), tok, "", map[string]any{}, nil); r.code != 403 {
			t.Errorf("approve without %s: %d %s", missing, r.code, r.raw)
		}
	}
	if e.client.Shop.Query().Where(shop.NameEQ("小明花坊")).ExistX(ctx) {
		t.Fatal("a forbidden approval created a shop")
	}
	if e.appByEmail(t, "perm@a.test").Status != "pending_review" {
		t.Fatal("status changed by forbidden calls")
	}
}

// platformUser creates an account holding a platform role with exactly perms.
func (e *appEnv) platformUser(t *testing.T, email string, perms ...string) int {
	t.Helper()
	ctx := context.Background()
	rl := e.client.Role.Create().SetName("r-" + email).SetScope("platform").SaveX(ctx)
	for _, n := range perms {
		pid := e.client.Permission.Query().Where(permission.NameEQ(n)).OnlyX(ctx).ID
		e.client.RolePermission.Create().SetRoleID(rl.ID).SetPermissionID(pid).SaveX(ctx)
	}
	hash, _ := auth.HashPassword("long-enough-password")
	u := e.client.User.Create().SetEmail(email).SetPasswordHash(hash).SaveX(ctx)
	e.client.RoleUser.Create().SetUserID(u.ID).SetRoleID(rl.ID).SaveX(ctx)
	return u.ID
}

// ── review: list / get ────────────────────────────────────────────

func TestApplicationListAndGet(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	e.readyApplication(t, "one@a.test")
	e.submit(validApp("two@a.test"), "")
	tok := e.adminTok(t)

	r := e.req("GET", "/api/v1/admin/shop-applications?status=pending_verification", tok, "", nil, nil)
	var list struct {
		Items  []map[string]any `json:"items"`
		Total  int              `json:"total"`
		Counts map[string]int   `json:"counts"`
		Pend   int              `json:"pending_messages"`
	}
	if r.code != 200 || json.Unmarshal([]byte(r.raw), &list) != nil {
		t.Fatalf("list: %d %s", r.code, r.raw)
	}
	if list.Total != 1 || list.Items[0]["email"] != "two@a.test" || list.Items[0]["email_verified"] != false {
		t.Fatalf("filtered list = %s", r.raw)
	}
	if list.Counts["pending_review"] != 1 || list.Counts["pending_verification"] != 1 || list.Pend != 2 {
		t.Fatalf("counts = %+v pending=%d", list.Counts, list.Pend)
	}
	if _, has := list.Items[0]["business_summary"]; has {
		t.Error("list rows must not carry the free-text summary")
	}
	r = e.req("GET", "/api/v1/admin/shop-applications?q=ONE@a", tok, "", nil, nil)
	_ = json.Unmarshal([]byte(r.raw), &list)
	if list.Total != 1 || list.Items[0]["email"] != "one@a.test" {
		t.Fatalf("search: %s", r.raw)
	}

	id := e.appByEmail(t, "one@a.test").ID
	r = e.req("GET", fmt.Sprintf("/api/v1/admin/shop-applications/%d", id), tok, "", nil, nil)
	if r.code != 200 {
		t.Fatalf("get: %d %s", r.code, r.raw)
	}
	for _, must := range []string{`"events"`, `"messages"`, `"verified"`, `"verify_email"`, `"business_summary"`, `"email_has_account":false`} {
		if !strings.Contains(r.raw, must) {
			t.Errorf("detail lacks %s: %s", must, r.raw)
		}
	}
	if strings.Contains(r.raw, "ip_hmac") {
		t.Error("the IP HMAC must not be exposed")
	}
	if r := e.req("GET", "/api/v1/admin/shop-applications/99999", tok, "", nil, nil); r.code != 404 {
		t.Errorf("unknown id: %d", r.code)
	}
}

// ── review: approve ───────────────────────────────────────────────

func TestApplicationApproveOpensShopAndReturnsLinkOnce(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	ctx := context.Background()
	id := e.readyApplication(t, "owner@new.test")
	tok := e.adminTok(t)
	path := fmt.Sprintf("/api/v1/admin/shop-applications/%d/approve", id)

	r := e.req("POST", path, tok, "", map[string]any{}, nil)
	if r.code != 201 {
		t.Fatalf("approve: %d %s", r.code, r.raw)
	}
	var out struct {
		Application map[string]any `json:"application"`
		Shop        map[string]any `json:"shop"`
		Owner       map[string]any `json:"owner"`
	}
	if err := json.Unmarshal([]byte(r.raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.Owner["delivery"] != "manual" || out.Owner["created"] != true || out.Owner["email"] != "owner@new.test" {
		t.Fatalf("owner = %v", out.Owner)
	}
	setupURL, _ := out.Owner["setup_url"].(string)
	if !strings.Contains(setupURL, "/#/set-password/") {
		t.Fatalf("setup_url = %q", setupURL)
	}
	shopID := int(out.Shop["id"].(float64))
	if out.Shop["name"] != "小明花坊" || int(out.Shop["status"].(float64)) != 2 {
		t.Fatalf("shop = %v (default status must be 2, under review)", out.Shop)
	}
	app := e.appByEmail(t, "owner@new.test")
	if app.Status != "approved" || app.ProvisionedShopID == nil || *app.ProvisionedShopID != shopID || app.ReviewedBy == nil || *app.ReviewedBy != e.adminID {
		t.Fatalf("application = %+v", app)
	}
	owner := e.client.User.Query().Where(user.EmailEQ("owner@new.test")).OnlyX(ctx)
	if owner.PasswordHash != nil {
		t.Fatal("approval must never set a password")
	}

	// The one-time link is written nowhere.
	plain := setupURL[strings.Index(setupURL, "set-password/")+len("set-password/"):]
	dump := e.dumpApplicationTables(t)
	if strings.Contains(dump, plain) || strings.Contains(dump, account.HashToken(plain)) {
		t.Fatal("the owner's setup token reached an application table")
	}
	// ...and only the approved/verification messages exist, none with a link.
	for _, m := range e.client.ShopApplicationMessage.Query().AllX(ctx) {
		if strings.Contains(m.BodyText, "set-password") {
			t.Fatalf("message %d contains a setup link", m.ID)
		}
	}
	msg := e.client.ShopApplicationMessage.Query().Where(shopapplicationmessage.KindEQ("approved")).OnlyX(ctx)
	if msg.RecipientEmail != "owner@new.test" || msg.Status != "pending_manual" {
		t.Fatalf("approved message = %+v", msg)
	}

	// Approving again is a conflict and creates nothing.
	if r := e.req("POST", path, tok, "", map[string]any{}, nil); r.code != 409 {
		t.Fatalf("second approve: %d", r.code)
	}
	if n := e.client.Shop.Query().Where(shop.NameEQ("小明花坊")).CountX(ctx); n != 1 {
		t.Fatalf("%d shops", n)
	}
}

func TestApplicationApproveOverridesNameAndStatus(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	id := e.readyApplication(t, "o@new.test")
	r := e.req("POST", fmt.Sprintf("/api/v1/admin/shop-applications/%d/approve", id), e.adminTok(t), "",
		map[string]any{"shop_name": "改過的店名", "status": 1}, nil)
	if r.code != 201 || !strings.Contains(r.raw, "改過的店名") {
		t.Fatalf("%d %s", r.code, r.raw)
	}
	if got := e.client.Shop.Query().Where(shop.NameEQ("改過的店名")).OnlyX(context.Background()); got.Status != 1 {
		t.Fatalf("status = %d", got.Status)
	}
}

// Scenario: 未驗證不可核准.
func TestApplicationApproveRequiresVerification(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	e.submit(validApp("u@a.test"), "")
	id := e.appByEmail(t, "u@a.test").ID
	path := fmt.Sprintf("/api/v1/admin/shop-applications/%d/approve", id)
	if r := e.req("POST", path, e.adminTok(t), "", map[string]any{}, nil); r.code != 409 {
		t.Fatalf("unverified approve: %d %s", r.code, r.raw)
	}
	if e.client.Shop.Query().CountX(context.Background()) != 0 {
		t.Fatal("shop created")
	}
	// Operator manual confirmation makes it reviewable, flagged and audited.
	vm := fmt.Sprintf("/api/v1/admin/shop-applications/%d/verify-manually", id)
	if r := e.req("POST", vm, e.adminTok(t), "", nil, nil); r.code != 200 {
		t.Fatalf("verify-manually: %d %s", r.code, r.raw)
	}
	got := e.appByEmail(t, "u@a.test")
	if got.Status != "pending_review" || !got.RiskFlags[application.FlagManuallyVerified] || got.EmailVerifiedAt == nil {
		t.Fatalf("app = %+v", got)
	}
	ev := e.client.ShopApplicationEvent.Query().Where(shopapplicationevent.EventEQ("verified_manually")).OnlyX(context.Background())
	if ev.ActorUserID == nil || *ev.ActorUserID != e.adminID {
		t.Fatalf("event = %+v", ev)
	}
	if r := e.req("POST", vm, e.adminTok(t), "", nil, nil); r.code != 409 {
		t.Fatalf("second manual verification: %d", r.code)
	}
	if r := e.req("POST", path, e.adminTok(t), "", map[string]any{}, nil); r.code != 201 {
		t.Fatalf("approve after manual verification: %d %s", r.code, r.raw)
	}
}

// Scenario: 開店失敗整筆回滾.
func TestApplicationApproveFailureRollsBackEverything(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	ctx := context.Background()
	id := e.readyApplication(t, "rb@a.test")
	path := fmt.Sprintf("/api/v1/admin/shop-applications/%d/approve", id)
	r := e.req("POST", path, e.adminTok(t), "", map[string]any{"status": 9}, nil) // invalid shop status
	if r.code != 422 {
		t.Fatalf("approve with failing provisioning: %d %s", r.code, r.raw)
	}
	app := e.appByEmail(t, "rb@a.test")
	if app.Status != "pending_review" || app.ProvisionedShopID != nil || app.ReviewedAt != nil {
		t.Fatalf("application not restored: %+v", app)
	}
	if e.client.Shop.Query().CountX(ctx) != 0 {
		t.Fatal("a shop survived the rollback")
	}
	if e.client.User.Query().Where(user.EmailEQ("rb@a.test")).ExistX(ctx) {
		t.Fatal("an owner account survived the rollback")
	}
	if e.client.ShopApplicationMessage.Query().Where(shopapplicationmessage.KindEQ("approved")).ExistX(ctx) {
		t.Fatal("approval message survived the rollback")
	}
	if !e.client.ShopApplicationEvent.Query().Where(shopapplicationevent.EventEQ("approve_failed")).ExistX(ctx) {
		t.Fatal("no approve_failed event recorded")
	}
	// A correct retry works.
	if r := e.req("POST", path, e.adminTok(t), "", map[string]any{}, nil); r.code != 201 {
		t.Fatalf("retry: %d %s", r.code, r.raw)
	}
}

// Anti-takeover: an existing account only gains a membership, never a password.
func TestApplicationApproveExistingAccountNeedsConfirmationAndKeepsPassword(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	ctx := context.Background()
	hash, _ := auth.HashPassword("the-victims-own-password")
	victim := e.client.User.Create().SetEmail("victim@a.test").SetPasswordHash(hash).SaveX(ctx)
	id := e.readyApplication(t, "victim@a.test")
	path := fmt.Sprintf("/api/v1/admin/shop-applications/%d/approve", id)

	r := e.req("POST", path, e.adminTok(t), "", map[string]any{}, nil)
	if r.code != 409 || !strings.Contains(r.raw, "confirm_existing_account_required") {
		t.Fatalf("unconfirmed: %d %s", r.code, r.raw)
	}
	if e.client.Shop.Query().CountX(ctx) != 0 || e.appByEmail(t, "victim@a.test").Status != "pending_review" {
		t.Fatal("unconfirmed approval changed state")
	}
	detail := e.req("GET", fmt.Sprintf("/api/v1/admin/shop-applications/%d", id), e.adminTok(t), "", nil, nil)
	if !strings.Contains(detail.raw, `"email_has_account":true`) {
		t.Fatalf("detail does not warn: %s", detail.raw)
	}

	r = e.req("POST", path, e.adminTok(t), "", map[string]any{"confirm_existing_account": true}, nil)
	if r.code != 201 {
		t.Fatalf("confirmed: %d %s", r.code, r.raw)
	}
	if strings.Contains(r.raw, "setup_url") || !strings.Contains(r.raw, `"created":false`) {
		t.Fatalf("an existing account must get no setup link: %s", r.raw)
	}
	after := e.client.User.GetX(ctx, victim.ID)
	if after.PasswordHash == nil || *after.PasswordHash != hash {
		t.Fatal("the existing account's password changed")
	}
	if e.client.AdminCredentialToken.Query().CountX(ctx) != 0 {
		t.Fatal("a credential token was issued for an existing account")
	}
}

func TestApplicationConcurrentApproveCreatesExactlyOneShop(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	id := e.readyApplication(t, "cc@a.test")
	path := fmt.Sprintf("/api/v1/admin/shop-applications/%d/approve", id)
	tok := e.adminTok(t)
	var created, conflicts int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch r := e.req("POST", path, tok, "", map[string]any{}, nil); r.code {
			case 201:
				atomic.AddInt32(&created, 1)
			case 409:
				atomic.AddInt32(&conflicts, 1)
			}
		}()
	}
	wg.Wait()
	if created != 1 || conflicts != 5 {
		t.Fatalf("created=%d conflicts=%d", created, conflicts)
	}
	if n := e.client.Shop.Query().CountX(context.Background()); n != 1 {
		t.Fatalf("%d shops", n)
	}
}

// ── review: reject ────────────────────────────────────────────────

func TestApplicationRejectRulesAndCooldown(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	ctx := context.Background()
	id := e.readyApplication(t, "rj@a.test")
	path := fmt.Sprintf("/api/v1/admin/shop-applications/%d/reject", id)
	tok := e.adminTok(t)
	for name, body := range map[string]map[string]any{
		"missing reason": {}, "blank reason": {"decision_reason": "   "}, "too long": {"decision_reason": strings.Repeat("理", 1001)},
	} {
		if r := e.req("POST", path, tok, "", body, nil); r.code != 422 {
			t.Errorf("%s: %d", name, r.code)
		}
	}
	if e.appByEmail(t, "rj@a.test").Status != "pending_review" {
		t.Fatal("invalid rejection changed status")
	}
	r := e.req("POST", path, tok, "", map[string]any{"decision_reason": "資料不足，請補充商品說明", "internal_note": "looks like spam"}, nil)
	if r.code != 200 {
		t.Fatalf("reject: %d %s", r.code, r.raw)
	}
	app := e.appByEmail(t, "rj@a.test")
	if app.Status != "rejected" || app.DecisionReason != "資料不足，請補充商品說明" || app.InternalNote != "looks like spam" {
		t.Fatalf("app = %+v", app)
	}
	// The applicant is told the reason (outbox) and the internal note is not.
	msg := e.client.ShopApplicationMessage.Query().Where(shopapplicationmessage.KindEQ("rejected")).OnlyX(ctx)
	if !strings.Contains(msg.BodyText, "資料不足，請補充商品說明") || strings.Contains(msg.BodyText, "spam") || msg.RecipientEmail != "rj@a.test" {
		t.Fatalf("rejection message = %+v", msg)
	}
	// Terminal: no second rejection, no approval.
	if r := e.req("POST", path, tok, "", map[string]any{"decision_reason": "again"}, nil); r.code != 409 {
		t.Errorf("reject twice: %d", r.code)
	}
	if r := e.req("POST", fmt.Sprintf("/api/v1/admin/shop-applications/%d/approve", id), tok, "", map[string]any{}, nil); r.code != 409 {
		t.Errorf("approve rejected: %d", r.code)
	}
	if r := e.req("POST", fmt.Sprintf("/api/v1/admin/shop-applications/%d/verify-manually", id), tok, "", nil, nil); r.code != 409 {
		t.Errorf("verify-manually rejected: %d", r.code)
	}

	// 30-day cooldown: silently nothing inside it, a fresh application after it.
	e.advance(29 * 24 * time.Hour)
	if r := e.submit(validApp("rj@a.test"), ""); r.code != 202 {
		t.Fatalf("resubmit in cooldown: %d", r.code)
	}
	if e.count("rj@a.test") != 1 {
		t.Fatal("cooldown not enforced")
	}
	e.advance(2 * 24 * time.Hour)
	if r := e.submit(validApp("rj@a.test"), ""); r.code != 202 {
		t.Fatalf("resubmit after cooldown: %d", r.code)
	}
	if e.count("rj@a.test") != 2 {
		t.Fatal("re-application after the cooldown was not accepted")
	}
}

func TestApplicationRejectWorksBeforeVerification(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	e.submit(validApp("early@a.test"), "")
	id := e.appByEmail(t, "early@a.test").ID
	r := e.req("POST", fmt.Sprintf("/api/v1/admin/shop-applications/%d/reject", id), e.adminTok(t), "", map[string]any{"decision_reason": "spam"}, nil)
	if r.code != 200 {
		t.Fatalf("%d %s", r.code, r.raw)
	}
}

// ── outbox ────────────────────────────────────────────────────────

func TestApplicationMarkMessageSent(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	e.submit(validApp("m@a.test"), "")
	id := e.appByEmail(t, "m@a.test").ID
	msg := e.client.ShopApplicationMessage.Query().OnlyX(context.Background())
	path := fmt.Sprintf("/api/v1/admin/shop-applications/%d/messages/%d/mark-sent", id, msg.ID)
	if r := e.req("POST", path, e.adminTok(t), "", nil, nil); r.code != 200 {
		t.Fatalf("mark-sent: %d %s", r.code, r.raw)
	}
	got := e.client.ShopApplicationMessage.GetX(context.Background(), msg.ID)
	if got.Status != "sent" || got.SentAt == nil || got.MarkedBy == nil || *got.MarkedBy != e.adminID {
		t.Fatalf("message = %+v", got)
	}
	if r := e.req("POST", path, e.adminTok(t), "", nil, nil); r.code != 409 {
		t.Errorf("twice: %d", r.code)
	}
	wrong := fmt.Sprintf("/api/v1/admin/shop-applications/%d/messages/%d/mark-sent", id+99, msg.ID)
	if r := e.req("POST", wrong, e.adminTok(t), "", nil, nil); r.code != 404 {
		t.Errorf("message of another application: %d", r.code)
	}
}

// ── PII ───────────────────────────────────────────────────────────

func TestApplicationLogsAndEventsContainNoPII(t *testing.T) {
	e := newAppEnv(t, appOpts{})
	ctx := context.Background()
	body := validApp(piiEmail)
	body["contact_name"], body["phone"], body["shop_name"] = piiName, piiPhone, piiShop
	if r := e.submit(body, "198.51.100.77"); r.code != 202 {
		t.Fatalf("submit: %d", r.code)
	}
	e.submit(body, "198.51.100.77") // duplicate path
	if r := e.verify(e.verifyToken(t, piiEmail)); r.code != 200 {
		t.Fatal("verify")
	}
	id := e.appByEmail(t, piiEmail).ID
	if r := e.req("POST", fmt.Sprintf("/api/v1/admin/shop-applications/%d/approve", id), e.adminTok(t), "", map[string]any{}, nil); r.code != 201 {
		t.Fatalf("approve: %d %s", r.code, r.raw)
	}
	// And a rejection path.
	e.submit(validApp("other@a.test"), "")
	oid := e.appByEmail(t, "other@a.test").ID
	e.req("POST", fmt.Sprintf("/api/v1/admin/shop-applications/%d/reject", oid), e.adminTok(t), "", map[string]any{"decision_reason": "no"}, nil)

	logs := e.logBuf.String()
	for _, needle := range []string{piiEmail, piiName, piiPhone, piiShop} {
		if strings.Contains(logs, needle) {
			t.Errorf("log output contains %q", needle)
		}
	}
	for _, ev := range e.client.ShopApplicationEvent.Query().AllX(ctx) {
		raw, _ := json.Marshal(ev.Detail)
		for _, needle := range []string{piiEmail, piiName, piiPhone, piiShop, "王小明", "@"} {
			if strings.Contains(string(raw), needle) {
				t.Errorf("event %s detail %s contains %q", ev.Event, raw, needle)
			}
		}
	}
}

func (e *appEnv) dumpApplicationTables(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	for _, a := range e.client.ShopApplication.Query().AllX(ctx) {
		fmt.Fprintf(&b, "%+v\n", a)
	}
	for _, m := range e.client.ShopApplicationMessage.Query().AllX(ctx) {
		fmt.Fprintf(&b, "%+v\n", m)
	}
	for _, ev := range e.client.ShopApplicationEvent.Query().AllX(ctx) {
		fmt.Fprintf(&b, "%+v %v\n", ev, ev.Detail)
	}
	for _, tk := range e.client.ShopApplicationToken.Query().Where(shopapplicationtoken.IDGT(0)).AllX(ctx) {
		fmt.Fprintf(&b, "%+v\n", tk)
	}
	return b.String()
}

// ── partial unique index ──────────────────────────────────────────

func TestApplicationOneOpenApplicationPerEmailIndex(t *testing.T) {
	client := testutil.OpenDB(t)
	ctx := context.Background()
	mk := func(status string) error {
		_, err := client.ShopApplication.Create().SetEmail("idx@a.test").SetContactName("n").SetShopName("s").
			SetBusinessSummary("b").SetConsentVersion("v").SetConsentedAt(time.Now()).SetStatus(status).Save(ctx)
		return err
	}
	if err := mk("pending_verification"); err != nil {
		t.Fatal(err)
	}
	if err := mk("pending_review"); !ent.IsConstraintError(err) {
		t.Fatalf("second open application: %v", err)
	}
	if err := mk("pending_verification"); !ent.IsConstraintError(err) {
		t.Fatalf("second unverified application: %v", err)
	}
	client.ShopApplication.Update().SetStatus("rejected").ExecX(ctx)
	if err := mk("pending_verification"); err != nil {
		t.Fatalf("a new application after a rejection must be allowed: %v", err)
	}
	if err := mk("rejected"); err != nil {
		t.Fatalf("decided rows may repeat: %v", err)
	}
	if err := mk("not_a_status"); err == nil {
		t.Fatal("status CHECK missing")
	}
}
