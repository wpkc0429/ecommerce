package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/content/hook"
	entaudit "ksdevworks/ecommerce/api/internal/ent/contentauditlog"
)

// stubPoster stands in for the outbound sender: the integration tests never
// make a network call.
type stubPoster struct {
	mu     sync.Mutex
	calls  []stubCall
	result hook.Result
	block  chan struct{}
}

type stubCall struct {
	url     string
	body    []byte
	headers map[string]string
}

func (p *stubPoster) Post(_ context.Context, rawURL string, body []byte, headers map[string]string) hook.Result {
	if p.block != nil {
		<-p.block
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, stubCall{rawURL, body, headers})
	if p.result.Status == "" {
		return hook.Result{Status: hook.StatusOK, HTTPStatus: 200}
	}
	return p.result
}

func (p *stubPoster) count() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.calls) }

const cfHookURL = "https://api.cloudflare.com/client/v4/workers/builds/deploy_hooks/ab12cd34-ef56-7890-abcd-ef1234567890"

func hookPath(e *schemaEnv, rest string) string {
	return e.path(e.shopA, "/content/publish-hook"+rest)
}

func TestPublishHookPermissions(t *testing.T) {
	e := docEnv(t)
	body := map[string]any{"url": cfHookURL}
	for _, tc := range []struct {
		name string
		user int
		want int
	}{
		{"owner", e.ownerA, 200},
		{"content.manage custom role", e.contentMgr, 200},
		{"editor", e.editorA, 403},
		{"staff", e.staffA, 403},
		{"content.view/edit/publish only", e.contentOnly, 403},
		{"page.* only", e.pageOnly, 403},
		{"other shop owner", e.ownerB, 403},
	} {
		for _, req := range []struct{ method, rest string }{
			{"GET", ""}, {"PUT", ""}, {"DELETE", ""}, {"POST", "/test"}, {"POST", "/fire"}, {"POST", "/signing-secret"},
		} {
			if tc.want != 403 {
				break
			}
			code, _, raw := e.doJSON(t, tc.user, req.method, hookPath(e, req.rest), body)
			if code != 403 {
				t.Errorf("%s %s %s: %d %s", tc.name, req.method, req.rest, code, raw)
			}
		}
		if tc.want == 200 {
			if code, _, raw := e.doJSON(t, tc.user, "GET", hookPath(e, ""), nil); code != 200 {
				t.Errorf("%s GET: %d %s", tc.name, code, raw)
			}
		}
	}
	// None of the denied calls changed anything.
	if e.client.ContentPublishHook.Query().CountX(context.Background()) != 0 {
		t.Fatal("a denied caller created a hook")
	}
}

func TestPublishHookSetNeverReadsBack(t *testing.T) {
	e := docEnv(t)
	code, st, raw := e.doJSON(t, e.ownerA, "GET", hookPath(e, ""), nil)
	if code != 200 || st["configured"] != false {
		t.Fatalf("empty status: %d %s", code, raw)
	}
	// Bad URLs: 422 with a reason that does not echo the URL.
	for _, bad := range []string{"http://api.cloudflare.com/x", "https://evil.test/hook-secret-xyz", "https://10.0.0.5/x", "https://u:p@api.cloudflare.com/x", "https://api.cloudflare.com:8443/x"} {
		code, _, raw := e.doJSON(t, e.ownerA, "PUT", hookPath(e, ""), map[string]any{"url": bad})
		if code != 422 {
			t.Errorf("%s: %d %s", bad, code, raw)
		}
		if strings.Contains(raw, "hook-secret-xyz") {
			t.Errorf("error echoes the URL: %s", raw)
		}
	}
	// Dry run validates the shape and stores nothing.
	code, st, raw = e.doJSON(t, e.ownerA, "PUT", hookPath(e, "?dry_run=true"), map[string]any{"url": cfHookURL})
	if code != 200 || st["host"] != "api.cloudflare.com" || e.client.ContentPublishHook.Query().CountX(context.Background()) != 0 {
		t.Fatalf("dry run: %d %s", code, raw)
	}
	if code, _, _ = e.doJSON(t, e.ownerA, "PUT", hookPath(e, "?dry_run=true"), map[string]any{"url": "https://evil.test/x"}); code != 422 {
		t.Fatalf("dry run of a bad URL: %d", code)
	}
	// Set.
	code, st, raw = e.doJSON(t, e.ownerA, "PUT", hookPath(e, ""), map[string]any{"url": cfHookURL})
	if code != 200 || st["configured"] != true || st["enabled"] != true {
		t.Fatalf("set: %d %s", code, raw)
	}
	secret := mustRotate(t, e)
	var all strings.Builder
	for _, rec := range []string{raw} {
		all.WriteString(rec)
	}
	_, _, got := e.doJSON(t, e.ownerA, "GET", hookPath(e, ""), nil)
	all.WriteString(got)
	for _, leak := range []string{"ab12cd34", "deploy_hooks", secret, "ciphertext", "client/v4"} {
		if strings.Contains(all.String(), leak) {
			t.Fatalf("response leaks %q: %s", leak, all.String())
		}
	}
	if !strings.Contains(got, `"url_masked":"https://api.cloudflare.com/…7890"`) {
		t.Fatalf("mask: %s", got)
	}
	// The secret is shown once: no endpoint returns it again.
	_, _, again := e.doJSON(t, e.ownerA, "GET", hookPath(e, ""), nil)
	if strings.Contains(again, secret) {
		t.Fatal("secret readable")
	}
	// Audit rows and the application log carry neither.
	for _, r := range e.client.ContentAuditLog.Query().Where(entaudit.ActionHasPrefix("hook.")).AllX(context.Background()) {
		if strings.Contains(string(r.Detail), "ab12cd34") || strings.Contains(string(r.Detail), secret) {
			t.Fatalf("audit leaks: %s", r.Detail)
		}
	}
	if strings.Contains(e.buf.String(), "ab12cd34") || strings.Contains(e.buf.String(), secret) {
		t.Fatal("application log leaks the URL or the secret")
	}
	// Disable / enable without resending the URL.
	code, st, _ = e.doJSON(t, e.ownerA, "PUT", hookPath(e, ""), map[string]any{"enabled": false})
	if code != 200 || st["enabled"] != false || st["disabled_reason"] != "manual" {
		t.Fatalf("disable: %d %v", code, st)
	}
	// Nothing to change.
	if code, _, _ = e.doJSON(t, e.ownerA, "PUT", hookPath(e, ""), map[string]any{}); code != 422 {
		t.Fatalf("empty PUT: %d", code)
	}
	// Delete.
	if code, _, _ = e.doJSON(t, e.ownerA, "DELETE", hookPath(e, ""), nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _, _ = e.doJSON(t, e.ownerA, "DELETE", hookPath(e, ""), nil); code != 404 {
		t.Fatalf("second delete: %d", code)
	}
	if code, _, _ = e.doJSON(t, e.ownerA, "POST", hookPath(e, "/test"), nil); code != 404 {
		t.Fatalf("test without hook: %d", code)
	}
	if code, _, _ = e.doJSON(t, e.ownerA, "PUT", hookPath(e, ""), map[string]any{"enabled": true}); code != 404 {
		t.Fatalf("enable without hook: %d", code)
	}
}

func mustRotate(t *testing.T, e *schemaEnv) string {
	t.Helper()
	code, m, raw := e.doJSON(t, e.ownerA, "POST", hookPath(e, "/signing-secret"), nil)
	s, _ := m["signing_secret"].(string)
	if code != 200 || len(s) != 64 {
		t.Fatalf("rotate: %d %s", code, raw)
	}
	return s
}

func TestPublishHookTestAndFireAreRateLimitedAndReportOnlyTheCategory(t *testing.T) {
	e := docEnv(t)
	e.doJSON(t, e.ownerA, "PUT", hookPath(e, ""), map[string]any{"url": cfHookURL})
	e.hookPoster.result = hook.Result{Status: hook.StatusHTTPError, HTTPStatus: 404}
	code, m, raw := e.doJSON(t, e.ownerA, "POST", hookPath(e, "/test"), nil)
	if code != 200 || m["ok"] != false || m["result"] != "http_error" || m["http_status"].(float64) != 404 {
		t.Fatalf("test: %d %s", code, raw)
	}
	if len(m) != 3 {
		t.Fatalf("test response must carry only ok/result/http_status: %s", raw)
	}
	e.hookPoster.result = hook.Result{Status: hook.StatusOK, HTTPStatus: 200}
	code, m, _ = e.doJSON(t, e.ownerA, "POST", hookPath(e, "/fire"), nil)
	if code != 200 || m["ok"] != true {
		t.Fatalf("fire: %d %v", code, m)
	}
	// 3 calls per minute and shop (test and fire share the bucket): 2 used, the
	// third is fine, the fourth is 429 with Retry-After.
	if code, _, _ = e.doJSON(t, e.ownerA, "POST", hookPath(e, "/test"), nil); code != 200 {
		t.Fatalf("third call: %d", code)
	}
	rec := e.doH(t, e.ownerA, "POST", hookPath(e, "/fire"), nil, nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("fourth call: %d %s", rec.Code, rec.Body.String())
	}
	if e.hookPoster.count() != 3 {
		t.Fatalf("receiver calls: %d", e.hookPoster.count())
	}
	// The audit trail names the host and the category, nothing else.
	acts := e.client.ContentAuditLog.Query().Where(entaudit.ActionIn("hook.test", "hook.fire")).AllX(context.Background())
	if len(acts) != 3 {
		t.Fatalf("audit rows: %d", len(acts))
	}
	for _, a := range acts {
		var d map[string]any
		_ = json.Unmarshal(a.Detail, &d)
		if d["host"] != "api.cloudflare.com" || d["result"] == nil || strings.Contains(string(a.Detail), "ab12cd34") {
			t.Fatalf("audit detail: %s", a.Detail)
		}
	}
}

func TestPublishingTriggersExactlyOneDebouncedCall(t *testing.T) {
	e := docEnv(t)
	e.doJSON(t, e.ownerA, "PUT", hookPath(e, ""), map[string]any{"url": cfHookURL})
	secret := mustRotate(t, e)
	// Publish three documents through the real API.
	code := e.saveDoc(t, e.ownerA, "promo-a", "*", map[string]any{"type_key": "note", "content": map[string]any{"title": "a"}}).Code
	if code != 201 {
		t.Fatalf("create: %d", code)
	}
	for _, key := range []string{"banner", "promo-a", "global"} {
		rec := e.doH(t, e.ownerA, "POST", e.docPath("/"+key+"/publish"), nil, nil)
		if rec.Code != 200 {
			t.Fatalf("publish %s: %d %s", key, rec.Code, rec.Body.String())
		}
	}
	e.hookN.DrainInline(context.Background())
	if e.hookPoster.count() != 0 {
		t.Fatal("publishing must not call the receiver itself")
	}
	row := e.client.ContentPublishHook.Query().OnlyX(context.Background())
	if row.PendingSince == nil || row.PendingCount < 3 {
		t.Fatalf("pending state: %+v", row)
	}
	// A flusher inside the debounce window sends nothing; later it sends one.
	if rep, _ := e.hookN.Tick(context.Background(), false); rep.Sent != 0 {
		t.Fatalf("debounce ignored: %+v", rep)
	}
	e.hookN.SetClock(func() time.Time { return time.Now().Add(31 * time.Second) }, nil)
	if rep, _ := e.hookN.Tick(context.Background(), false); rep.Sent != 1 {
		t.Fatalf("expected one call: %+v", rep)
	}
	if e.hookPoster.count() != 1 {
		t.Fatalf("calls: %d", e.hookPoster.count())
	}
	c := e.hookPoster.calls[0]
	if c.url != cfHookURL || !hook.Verify([]byte(secret), mustAtoi(t, c.headers[hook.HeaderTimestamp]), c.body, c.headers[hook.HeaderSignature]) {
		t.Fatal("call is not signed with the shop's secret")
	}
	var body map[string]any
	_ = json.Unmarshal(c.body, &body)
	if body["event"] != "content.published" || body["coalesced"].(float64) < 3 {
		t.Fatalf("body: %s", c.body)
	}
	// Unpublishing also triggers.
	e.hookN.SetClock(nil, nil)
	if rec := e.doH(t, e.ownerA, "POST", e.docPath("/promo-a/unpublish"), nil, nil); rec.Code != 200 {
		t.Fatalf("unpublish: %d", rec.Code)
	}
	e.hookN.DrainInline(context.Background())
	if r := e.client.ContentPublishHook.Query().OnlyX(context.Background()); r.PendingSince == nil || r.PendingEvent != "content.unpublished" {
		t.Fatalf("unpublish not recorded: %+v", r)
	}
}

func mustAtoi(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("not a number: %q", s)
		}
		n = n*10 + int64(r-'0')
	}
	return n
}

func TestPublishIsNotSlowedByAHangingReceiver(t *testing.T) {
	e := docEnv(t)
	e.doJSON(t, e.ownerA, "PUT", hookPath(e, ""), map[string]any{"url": cfHookURL})
	e.hookPoster.block = make(chan struct{}) // the receiver never answers
	defer close(e.hookPoster.block)
	e.hookN.Cfg.Debounce = time.Millisecond
	e.hookN.Start()
	defer func() { _ = e.hookN.Close() }()
	start := time.Now()
	for i := 0; i < 5; i++ {
		if rec := e.doH(t, e.ownerA, "POST", e.docPath("/banner/publish"), nil, nil); rec.Code != 200 {
			t.Fatalf("publish: %d", rec.Code)
		}
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("5 publishes took %s with a hanging receiver", d)
	}
}
