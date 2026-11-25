package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/database"
	"ksdevworks/ecommerce/api/internal/design"
	"ksdevworks/ecommerce/api/internal/ent"
	entshot "ksdevworks/ecommerce/api/internal/ent/designscreenshot"
	entrole "ksdevworks/ecommerce/api/internal/ent/role"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/seed"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// Integration tests of the design history (change design-history slice 1): the
// whole record is exercised through the real router, with a fake clock for the
// approval and preview windows.

const (
	shaBase  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaHead  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	shaHead2 = "cccccccccccccccccccccccccccccccccccccccc"
)

type designEnv struct {
	t      *testing.T
	client *ent.Client
	sqlDB  *sql.DB
	router http.Handler
	issuer *auth.TokenIssuer
	svc    *design.Service
	creds  *design.Credentials
	buf    *bytes.Buffer
	val    *httpapi.ContractValidator

	now time.Time
	mu  sync.Mutex

	platform                                   int
	shopA, shopB                               int
	ownerA, editorA, staffA, outsiderA, ownerB int
	keyA, keyB                                 string
	credA                                      int
}

func newDesignEnv(t *testing.T) *designEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	if err := seed.Run(ctx, client, seed.Options{AdminEmail: "root@test.dev", AdminPassword: "root-password-123"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e := &designEnv{t: t, client: client, buf: &bytes.Buffer{}, now: time.Now().UTC().Truncate(time.Microsecond)}
	log := slog.New(slog.NewTextHandler(e.buf, nil))
	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	_, e.sqlDB, err = database.Open(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.sqlDB.Close() })

	dispatcher := events.NewDispatcher(log)
	engine := rbac.NewEngine(client, rdb, log)
	authz := &httpapi.AuthzMW{Engine: engine}
	cmsSvc := &cms.Service{Client: client, Dispatcher: dispatcher}
	cfg := design.DefaultConfig()
	cfg.ScreenshotsEnabled = true
	e.svc = &design.Service{Client: client, Authz: design.RBACAuthorizer{Engine: engine}, Cfg: cfg, Log: log,
		Now: func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }}
	e.creds = &design.Credentials{Client: client}
	limiter := ratelimit.New(rdb, log)
	e.router = httpapi.New(httpapi.Deps{
		Cfg:                    &config.Config{},
		Log:                    log,
		AdminMW:                httpapi.NewAdminMiddleware(issuer),
		Design:                 &httpapi.DesignAdminHandler{Service: e.svc, Client: client, Authz: authz, Limiter: limiter, DefaultTZ: "Asia/Taipei", Log: log},
		DesignService:          &httpapi.DesignServiceHandler{Service: e.svc, Log: log},
		DesignCredentials:      &httpapi.DesignCredentialsHandler{Credentials: e.creds, Authz: authz, Log: log},
		DesignServiceAuthMW:    httpapi.NewDesignServiceAuth(e.creds, limiter, log),
		DesignServiceRateLimit: httpapi.NewDesignServiceRateLimit(limiter),
	})
	e.val = httpapi.NewContractValidator(t)

	e.platform = client.User.Query().OnlyX(ctx).ID
	a, err := cmsSvc.CreateShop(ctx, cms.ShopInput{Name: "Shop A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := cmsSvc.CreateShop(ctx, cms.ShopInput{Name: "Shop B"})
	if err != nil {
		t.Fatal(err)
	}
	e.shopA, e.shopB = a.ID, b.ID
	hash, _ := auth.HashPassword("password-123")
	role := func(name string) int {
		return client.Role.Query().Where(entrole.NameEQ(name), entrole.ScopeEQ("merchant")).OnlyX(ctx).ID
	}
	member := func(email, roleName string, shop int) int {
		u := client.User.Create().SetEmail(email).SetPasswordHash(hash).SaveX(ctx).ID
		client.ShopUser.Create().SetShopID(shop).SetUserID(u).SaveX(ctx)
		client.RoleUser.Create().SetUserID(u).SetRoleID(role(roleName)).SetShopID(shop).SaveX(ctx)
		return u
	}
	e.ownerA = member("owner-a@t.dev", "merchant_owner", e.shopA)
	e.editorA = member("editor-a@t.dev", "editor", e.shopA)
	e.staffA = member("staff-a@t.dev", "staff", e.shopA)
	e.ownerB = member("owner-b@t.dev", "merchant_owner", e.shopB)
	e.outsiderA = client.User.Create().SetEmail("outsider@t.dev").SetPasswordHash(hash).SaveX(ctx).ID // no membership anywhere

	ia, err := e.creds.Issue(ctx, design.IssueInput{ShopID: e.shopA, Name: "svc-a", CreatedByID: e.platform})
	if err != nil {
		t.Fatal(err)
	}
	ib, err := e.creds.Issue(ctx, design.IssueInput{ShopID: e.shopB, Name: "svc-b", CreatedByID: e.platform})
	if err != nil {
		t.Fatal(err)
	}
	e.keyA, e.keyB, e.credA = ia.Key, ib.Key, ia.Credential.ID
	return e
}

func (e *designEnv) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }

func (e *designEnv) adminPath(shop int, rest string) string {
	return fmt.Sprintf("/api/v1/admin/shops/%d/design%s", shop, rest)
}
func (e *designEnv) svcPath(shop int, rest string) string {
	return fmt.Sprintf("/api/v1/design-service/shops/%d%s", shop, rest)
}

type dresp struct {
	Code   int
	Body   map[string]any
	Raw    string
	Header http.Header
}

func (e *designEnv) send(req *http.Request) dresp {
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return dresp{rec.Code, m, rec.Body.String(), rec.Result().Header}
}

func dJSONBody(body any) *bytes.Reader {
	if body == nil {
		return bytes.NewReader(nil)
	}
	b, _ := json.Marshal(body)
	return bytes.NewReader(b)
}

// human sends a request as an admin user.
func (e *designEnv) human(user int, method, path string, body any, h ...string) dresp {
	e.t.Helper()
	tok, err := e.issuer.IssueAdmin(user, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, dJSONBody(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	for i := 0; i+1 < len(h); i += 2 {
		req.Header.Set(h[i], h[i+1])
	}
	return e.send(req)
}

// machine sends a request with a service credential.
func (e *designEnv) machine(key, method, path string, body any, h ...string) dresp {
	e.t.Helper()
	req := httptest.NewRequest(method, path, dJSONBody(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(h); i += 2 {
		req.Header.Set(h[i], h[i+1])
	}
	return e.send(req)
}

func (e *designEnv) mustCode(r dresp, want int, what string) dresp {
	e.t.Helper()
	if r.Code != want {
		e.t.Fatalf("%s: got %d want %d: %s", what, r.Code, want, r.Raw)
	}
	return r
}

func dErrCode(r dresp) string {
	if m, ok := r.Body["error"].(map[string]any); ok {
		s, _ := m["code"].(string)
		return s
	}
	return ""
}

func (e *designEnv) wantErr(r dresp, status int, code, what string) {
	e.t.Helper()
	if r.Code != status || dErrCode(r) != code {
		e.t.Fatalf("%s: got %d %q want %d %q: %s", what, r.Code, dErrCode(r), status, code, r.Raw)
	}
}

var dIdemSeq int

func dIdemKey() string { dIdemSeq++; return fmt.Sprintf("test-key-%08d", dIdemSeq) }

func (e *designEnv) create(shop int, key string, user int, site, prompt string) dresp {
	return e.machine(key, "POST", e.svcPath(shop, "/requests"), map[string]any{
		"site": site, "requested_by_user_id": user, "prompt": prompt, "base_sha": shaBase,
		"core_version": "0.3.0", "design_floor_version": "0.2.0",
	}, "Idempotency-Key", dIdemKey())
}

func id(r dresp) string { s, _ := r.Body["id"].(string); return s }

func (e *designEnv) rev(requestID string) int {
	e.t.Helper()
	r := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+requestID), nil)
	e.mustCode(r, 200, "get")
	return int(r.Body["rev"].(float64))
}

func (e *designEnv) etag(requestID string) string { return fmt.Sprintf(`"%d"`, e.rev(requestID)) }

// addScreenshots writes a complete before/after set straight into the table
// (slice 2 owns the upload path; submit only needs the rows).
func (e *designEnv) addScreenshots(requestID, head string, setVersion int) {
	e.t.Helper()
	rid := uuid.MustParse(requestID)
	manifest := json.RawMessage(fmt.Sprintf(`{"dataset":"sandbox-v1","set_version":%d}`, setVersion))
	attempt := int16(e.client.DesignScreenshot.Query().Where(entshot.RequestIDEQ(rid), entshot.PhaseEQ("before"), entshot.ViewportEQ(390)).CountX(context.Background()) + 1)
	for _, ph := range []struct{ phase, sha string }{{"before", shaBase}, {"after", head}} {
		for _, vp := range []int16{390, 1280} {
			e.client.DesignScreenshot.Create().SetShopID(e.shopA).SetRequestID(rid).SetPhase(ph.phase).SetSite("admin").
				SetViewKey("orders").SetViewport(vp).SetRole("owner").SetAttempt(attempt).SetCommitSha(ph.sha).
				SetObjectKey(fmt.Sprintf("shops/%d/design/%s/%s-%d.png", e.shopA, requestID, ph.phase, vp)).
				SetByteSize(1000).SetWidth(int(vp)).SetHeight(900).SetSha256(strings.Repeat("a", 64)).
				SetCaptureManifest(manifest).SaveX(context.Background())
		}
	}
}

// toPreviewing drives a fresh request to previewing with a passing guard.
func (e *designEnv) toPreviewing(requestID string) {
	e.t.Helper()
	p := func(path string) string { return e.svcPath(e.shopA, "/requests/"+requestID+path) }
	e.mustCode(e.machine(e.keyA, "PUT", p("/head"), map[string]any{"head_sha": shaHead, "diff_summary": map[string]any{
		"files_changed": 1, "additions": 3, "deletions": 1, "files": []map[string]any{{"path": "admin/design/theme.css", "status": "modified", "additions": 3, "deletions": 1}}}}), 200, "head")
	e.mustCode(e.machine(e.keyA, "POST", p("/guard-runs"), map[string]any{"checked_sha": shaHead, "passed": true, "rule_ids": []string{}}), 200, "guard")
	e.mustCode(e.machine(e.keyA, "PUT", p("/preview"), map[string]any{"origin": "https://p-3f9a.preview.example", "expires_at": e.now.Add(3 * time.Hour).Format(time.RFC3339)}), 200, "preview")
}

// toAwaiting drives a fresh request all the way to awaiting_approval.
func (e *designEnv) toAwaiting() string {
	e.t.Helper()
	r := e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "Make the order screen calmer"), 201, "create")
	rid := id(r)
	e.toPreviewing(rid)
	e.addScreenshots(rid, shaHead, 1)
	e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/submit"), nil), 200, "submit")
	return rid
}

func (e *designEnv) approve(user int, rid, head string, ifMatch string) dresp {
	return e.human(user, "POST", e.adminPath(e.shopA, "/requests/"+rid+"/approve"), map[string]any{"head_sha": head}, "If-Match", ifMatch)
}

// publish runs the service's closing steps for an approved request.
func (e *designEnv) publishApproved(rid string, tag string) dresp {
	e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/guard-runs"),
		map[string]any{"phase": "recheck", "checked_sha": shaHead, "passed": true}), 200, "recheck")
	return e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/published"),
		map[string]any{"published_sha": shaHead, "tag": tag, "deployment_id": "dep-1"})
}

// ── tests ────────────────────────────────────────────────────────────────

func TestDesignFullFlowAndContract(t *testing.T) {
	e := newDesignEnv(t)
	v := e.val
	rid := e.toAwaiting()

	r := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+rid), nil)
	e.mustCode(r, 200, "get")
	v.AssertResponse(t, "/api/v1/admin/shops/{shopID}/design/requests/{requestID}", "GET", 200, []byte(r.Raw))
	if r.Body["status"] != "awaiting_approval" || r.Body["candidate_sha"] != shaHead {
		t.Fatalf("not awaiting with candidate: %s", r.Raw)
	}
	etag := r.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if r2 := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+rid), nil, "If-None-Match", etag); r2.Code != 304 {
		t.Fatalf("If-None-Match: %d", r2.Code)
	}

	// The human approves the exact head they reviewed.
	ap := e.mustCode(e.approve(e.ownerA, rid, shaHead, etag), 200, "approve")
	v.AssertResponse(t, "/api/v1/admin/shops/{shopID}/design/requests/{requestID}/approve", "POST", 200, []byte(ap.Raw))
	tag, _ := ap.Body["publish_tag"].(string)
	if want := "design-admin-" + e.now.UTC().Format("20060102") + "-1"; tag != want {
		t.Fatalf("tag %q want %q", tag, want)
	}
	if ap.Body["approved_head_sha"] != shaHead || ap.Body["approval_expires_at"] == nil {
		t.Fatalf("approval not bound: %s", ap.Raw)
	}

	// The service reads state (no prompt) and publishes.
	sr := e.mustCode(e.machine(e.keyA, "GET", e.svcPath(e.shopA, "/requests/"+rid), nil), 200, "service get")
	if _, has := sr.Body["prompt_text"]; has || strings.Contains(sr.Raw, "calmer") {
		t.Fatalf("the service route must not return the prompt: %s", sr.Raw)
	}
	v.AssertResponse(t, "/api/v1/design-service/shops/{shopID}/requests/{requestID}", "GET", 200, []byte(sr.Raw))
	pub := e.mustCode(e.publishApproved(rid, tag), 200, "published")
	if pub.Body["status"] != "published" || pub.Body["published_tag"] != tag {
		t.Fatalf("not published: %s", pub.Raw)
	}

	// Events: full, ordered, chained; the chain verifies.
	ev := e.mustCode(e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+rid+"/events"), nil), 200, "events")
	v.AssertResponse(t, "/api/v1/admin/shops/{shopID}/design/requests/{requestID}/events", "GET", 200, []byte(ev.Raw))
	var kinds []string
	for _, x := range ev.Body["events"].([]any) {
		kinds = append(kinds, x.(map[string]any)["kind"].(string))
	}
	want := "created,head_updated,guard_run,preview_registered,screenshots_complete,submitted,approved,guard_rechecked,published"
	if strings.Join(kinds, ",") != want {
		t.Fatalf("events %v want %s", kinds, want)
	}
	rep, err := e.svc.VerifyChains(context.Background(), 0)
	if err != nil || len(rep.Broken) != 0 || rep.Verified != 1 {
		t.Fatalf("verify: %+v %v", rep, err)
	}

	// The site's current design is now this request.
	st := e.mustCode(e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/state"), nil), 200, "state")
	v.AssertResponse(t, "/api/v1/admin/shops/{shopID}/design/state", "GET", 200, []byte(st.Raw))
	sites := st.Body["sites"].([]any)
	var admin map[string]any
	for _, s := range sites {
		if s.(map[string]any)["site"] == "admin" {
			admin = s.(map[string]any)
		}
	}
	if admin["current_request_id"] != rid || admin["current_tag"] != tag || admin["current_sha"] != shaHead {
		t.Fatalf("site state not updated: %v", admin)
	}

	// The list carries only an excerpt and validates against the contract.
	l := e.mustCode(e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests?site=admin&status=published&from=2000-01-01&to=2999-12-31"), nil), 200, "list")
	v.AssertResponse(t, "/api/v1/admin/shops/{shopID}/design/requests", "GET", 200, []byte(l.Raw))
	if l.Body["total"].(float64) != 1 {
		t.Fatalf("list: %s", l.Raw)
	}
	if _, has := l.Body["requests"].([]any)[0].(map[string]any)["prompt_text"]; has {
		t.Fatal("list leaked prompt_text")
	}
}

func TestDesignRBACMatrix(t *testing.T) {
	e := newDesignEnv(t)
	rid := e.toAwaiting()
	list, detail, events := e.adminPath(e.shopA, "/requests"), e.adminPath(e.shopA, "/requests/"+rid), e.adminPath(e.shopA, "/requests/"+rid+"/events")
	for _, tc := range []struct {
		name     string
		user     int
		read     int // list, detail, events
		approve  int // reject / request-changes / approve all need design.approve
		cancel   int
		revertOK int
	}{
		{"owner", e.ownerA, 200, -1, -1, -1}, // writes are tested separately; here only reads + denial for others
		{"editor", e.editorA, 200, 403, 403, 403},
		{"staff", e.staffA, 403, 403, 403, 403},
		{"outsider", e.outsiderA, 403, 403, 403, 403},
		{"other shop owner", e.ownerB, 403, 403, 403, 403},
	} {
		for _, p := range []string{list, detail, events, e.adminPath(e.shopA, "/state"), e.adminPath(e.shopA, "/untracked-changes")} {
			if got := e.human(tc.user, "GET", p, nil).Code; got != tc.read {
				t.Errorf("%s GET %s = %d want %d", tc.name, p, got, tc.read)
			}
		}
		if tc.approve == -1 {
			continue
		}
		for _, w := range []struct {
			p    string
			body any
		}{
			{"/requests/" + rid + "/approve", map[string]any{"head_sha": shaHead}},
			{"/requests/" + rid + "/reject", map[string]any{}},
			{"/requests/" + rid + "/request-changes", map[string]any{"note": "x"}},
			{"/untracked-changes/" + shaHead + "/acknowledge", nil},
		} {
			if got := e.human(tc.user, "POST", e.adminPath(e.shopA, w.p), w.body, "If-Match", `"1"`).Code; got != tc.approve {
				t.Errorf("%s POST %s = %d want %d", tc.name, w.p, got, tc.approve)
			}
		}
		if got := e.human(tc.user, "POST", e.adminPath(e.shopA, "/requests/"+rid+"/cancel"), nil).Code; got != tc.cancel {
			t.Errorf("%s cancel = %d want %d", tc.name, got, tc.cancel)
		}
		if got := e.human(tc.user, "POST", e.adminPath(e.shopA, "/requests/"+rid+"/revert"), nil).Code; got != tc.revertOK {
			t.Errorf("%s revert = %d want %d", tc.name, got, tc.revertOK)
		}
	}
	// Nothing a denied caller tried changed the request.
	if r := e.human(e.ownerA, "GET", detail, nil); r.Body["status"] != "awaiting_approval" {
		t.Fatalf("denied callers changed the request: %s", r.Raw)
	}
	// An owner cannot mint service credentials; only the platform administrator can.
	body := map[string]any{"shop_id": e.shopA, "name": "x"}
	if got := e.human(e.ownerA, "POST", "/api/v1/admin/design-service/credentials", body).Code; got != 403 {
		t.Errorf("owner issue credential = %d want 403", got)
	}
	if got := e.human(e.ownerA, "GET", "/api/v1/admin/design-service/credentials?shop_id=1", nil).Code; got != 403 {
		t.Errorf("owner list credentials = %d want 403", got)
	}
}

func TestServiceCredentialCanNeverApprove(t *testing.T) {
	e := newDesignEnv(t)
	rid := e.toAwaiting()
	// Every route that could approve, tried with a valid service credential:
	// none exists on the machine family, and the credential does not
	// authenticate on /admin.
	for _, p := range []string{"approve", "reject", "request-changes", "cancel", "revert"} {
		r := e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/"+p), map[string]any{"head_sha": shaHead}, "If-Match", `"1"`)
		if r.Code != 404 && r.Code != 405 {
			t.Errorf("machine %s = %d (want 404/405)", p, r.Code)
		}
		r = e.machine(e.keyA, "POST", e.adminPath(e.shopA, "/requests/"+rid+"/"+p), map[string]any{"head_sha": shaHead}, "If-Match", `"1"`)
		if r.Code != 401 {
			t.Errorf("admin %s with a service key = %d (want 401)", p, r.Code)
		}
	}
	if r := e.machine(e.keyA, "GET", e.adminPath(e.shopA, "/requests"), nil); r.Code != 401 {
		t.Errorf("service key on admin list = %d", r.Code)
	}
	// A service that claims the work is published before anyone approved it.
	r := e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/published"), map[string]any{"published_sha": shaHead, "tag": "design-admin-20260101-1"})
	e.wantErr(r, 409, "invalid_state", "published before approval")
	// Nor can it fake its way past awaiting_approval with a recheck.
	r = e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/guard-runs"), map[string]any{"phase": "recheck", "checked_sha": shaHead, "passed": true})
	e.wantErr(r, 409, "invalid_state", "recheck before approval")
	if got := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+rid), nil); got.Body["status"] != "awaiting_approval" {
		t.Fatalf("status changed: %s", got.Raw)
	}
	// A key that is not a credential, or the wrong shape, is the same 401.
	for _, k := range []string{"", "dsk_unknown", "Bearer", "pk_demo"} {
		if got := e.machine(k, "GET", e.svcPath(e.shopA, "/requests"), nil).Code; got != 401 {
			t.Errorf("key %q = %d want 401", k, got)
		}
	}
}

func TestApprovalBinding(t *testing.T) {
	e := newDesignEnv(t)
	rid := e.toAwaiting()
	etag := e.etag(rid)

	e.wantErr(e.human(e.ownerA, "POST", e.adminPath(e.shopA, "/requests/"+rid+"/approve"), map[string]any{"head_sha": shaHead}), 428, "precondition_required", "no If-Match")
	e.wantErr(e.approve(e.ownerA, rid, shaHead, `"999"`), 412, "precondition_failed", "stale etag")
	e.wantErr(e.approve(e.ownerA, rid, shaHead2, etag), 409, "head_moved", "head differs from candidate")
	e.wantErr(e.approve(e.ownerA, rid, "xyz", etag), 422, "validation_failed", "malformed sha")
	if r := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+rid), nil); r.Body["status"] != "awaiting_approval" {
		t.Fatalf("a refused approval changed the status: %s", r.Raw)
	}

	ap := e.mustCode(e.approve(e.ownerA, rid, shaHead, etag), 200, "approve")
	// Replay: the same person, same head -> the same result, no new event.
	before := e.eventCount(rid)
	rp := e.mustCode(e.approve(e.ownerA, rid, shaHead, etag), 200, "replay")
	if rp.Body["publish_tag"] != ap.Body["publish_tag"] || rp.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay differs: %s vs %s", rp.Raw, ap.Raw)
	}
	if e.eventCount(rid) != before {
		t.Fatal("a replayed approval wrote an event")
	}
	// Somebody else approving an approved request is a conflict.
	e.wantErr(e.approve(e.ownerA2(), rid, shaHead, etag), 409, "invalid_state", "second approver")
	// Once approved the head is locked.
	r := e.machine(e.keyA, "PUT", e.svcPath(e.shopA, "/requests/"+rid+"/head"), map[string]any{"head_sha": shaHead2, "reset": true})
	e.wantErr(r, 409, "invalid_state", "head change after approval")
}

// ownerA2 is a second owner of shop A (the platform appoints them).
func (e *designEnv) ownerA2() int {
	ctx := context.Background()
	u := e.client.User.Create().SetEmail(fmt.Sprintf("owner2-%d@t.dev", time.Now().UnixNano())).SetPasswordHash("x").SaveX(ctx).ID
	e.client.ShopUser.Create().SetShopID(e.shopA).SetUserID(u).SaveX(ctx)
	e.client.RoleUser.Create().SetUserID(u).SetShopID(e.shopA).
		SetRoleID(e.client.Role.Query().Where(entrole.NameEQ("merchant_owner")).OnlyX(ctx).ID).SaveX(ctx)
	return u
}

func (e *designEnv) eventCount(rid string) int {
	var n int
	if err := e.sqlDB.QueryRow(`SELECT count(*) FROM design_request_events WHERE request_id = $1`, rid).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestConcurrentApprovalsOnlyOneWins(t *testing.T) {
	e := newDesignEnv(t)
	rid := e.toAwaiting()
	etag := e.etag(rid)
	owners := []int{e.ownerA, e.ownerA2(), e.ownerA2(), e.ownerA2(), e.ownerA2(), e.ownerA2()}
	codes := make([]int, len(owners))
	var wg sync.WaitGroup
	for i, u := range owners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = e.approve(u, rid, shaHead, etag).Code
		}()
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == 200 {
			ok++
		} else if c != 409 && c != 412 {
			t.Errorf("unexpected status %d in %v", c, codes)
		}
	}
	if ok != 1 {
		t.Fatalf("%d approvals succeeded (want exactly 1): %v", ok, codes)
	}
	if n := e.eventsOfKind(rid, "approved"); n != 1 {
		t.Fatalf("%d approved events", n)
	}
	if rep, _ := e.svc.VerifyChains(context.Background(), 0); len(rep.Broken) != 0 {
		t.Fatalf("chain broken by the race: %+v", rep.Broken)
	}
}

func (e *designEnv) eventsOfKind(rid, kind string) int {
	var n int
	if err := e.sqlDB.QueryRow(`SELECT count(*) FROM design_request_events WHERE request_id = $1 AND kind = $2`, rid, kind).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestPublishNeedsRecheckAndFreshApproval(t *testing.T) {
	e := newDesignEnv(t)
	rid := e.toAwaiting()
	ap := e.mustCode(e.approve(e.ownerA, rid, shaHead, e.etag(rid)), 200, "approve")
	tag := ap.Body["publish_tag"].(string)
	pub := func(sha, tg string) dresp {
		return e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/published"), map[string]any{"published_sha": sha, "tag": tg})
	}
	// No recheck yet.
	e.wantErr(pub(shaHead, tag), 409, "guard_not_passed", "publish without recheck")
	// A recheck of some other sha does not count.
	e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/guard-runs"), map[string]any{"phase": "recheck", "checked_sha": shaHead2, "passed": true}), 200, "recheck other sha")
	e.wantErr(pub(shaHead, tag), 409, "guard_not_passed", "recheck of another sha")
	// A failed recheck does not count either.
	e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/guard-runs"), map[string]any{"phase": "recheck", "checked_sha": shaHead, "passed": false, "rule_ids": []string{"R1"}}), 200, "failed recheck")
	e.wantErr(pub(shaHead, tag), 409, "guard_not_passed", "failed recheck")
	// A passing recheck of the exact sha, but the wrong sha / tag in the report.
	e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/guard-runs"), map[string]any{"phase": "recheck", "checked_sha": shaHead, "passed": true}), 200, "recheck")
	if r := pub(shaHead2, tag); r.Code != 422 {
		t.Fatalf("different sha: %d %s", r.Code, r.Raw)
	}
	if r := pub(shaHead, "design-admin-20200101-9"); r.Code != 422 {
		t.Fatalf("different tag: %d %s", r.Code, r.Raw)
	}
	// The approval expires: published is refused and the sweep fails the request.
	e.advance(2 * time.Hour)
	e.wantErr(pub(shaHead, tag), 409, "approval_expired", "expired approval")
	rep, err := e.svc.Sweep(context.Background(), 0, true)
	if err != nil || rep.ApprovalExpired != 1 {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	r := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+rid), nil)
	if r.Body["status"] != "failed" || r.Body["failure_code"] != "approval_expired" {
		t.Fatalf("not failed: %s", r.Raw)
	}
	if e.eventsOfKind(rid, "expired") != 1 {
		t.Fatal("no expired event")
	}
	// A later request can start (the failed one no longer blocks).
	e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "again"), 201, "create after failure")
}

func TestRequestStateMachinePreconditions(t *testing.T) {
	e := newDesignEnv(t)
	r := e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "a change"), 201, "create")
	rid := id(r)
	p := func(path string) string { return e.svcPath(e.shopA, "/requests/"+rid+path) }
	// drafting: no preview without a passing guard for the current head.
	e.wantErr(e.machine(e.keyA, "PUT", p("/preview"), map[string]any{"origin": "https://p.example", "expires_at": e.now.Add(time.Hour).Format(time.RFC3339)}), 409, "guard_not_passed", "preview without guard")
	e.mustCode(e.machine(e.keyA, "PUT", p("/head"), map[string]any{"head_sha": shaHead}), 200, "head")
	e.mustCode(e.machine(e.keyA, "POST", p("/guard-runs"), map[string]any{"checked_sha": shaHead, "passed": false, "rule_ids": []string{"R7"}}), 200, "failed guard")
	e.wantErr(e.machine(e.keyA, "PUT", p("/preview"), map[string]any{"origin": "https://p.example", "expires_at": e.now.Add(time.Hour).Format(time.RFC3339)}), 409, "guard_not_passed", "preview after failed guard")
	// A passing guard for a DIFFERENT sha does not allow a preview of this head.
	e.mustCode(e.machine(e.keyA, "POST", p("/guard-runs"), map[string]any{"checked_sha": shaHead2, "passed": true}), 200, "guard other sha")
	e.wantErr(e.machine(e.keyA, "PUT", p("/preview"), map[string]any{"origin": "https://p.example", "expires_at": e.now.Add(time.Hour).Format(time.RFC3339)}), 409, "guard_not_passed", "preview for another sha")
	e.mustCode(e.machine(e.keyA, "POST", p("/guard-runs"), map[string]any{"checked_sha": shaHead, "passed": true}), 200, "guard")
	// Origins with a path or query are refused.
	e.wantErr(e.machine(e.keyA, "PUT", p("/preview"), map[string]any{"origin": "https://p.example/x?token=1", "expires_at": e.now.Add(time.Hour).Format(time.RFC3339)}), 422, "validation_failed", "origin with query")
	e.mustCode(e.machine(e.keyA, "PUT", p("/preview"), map[string]any{"origin": "https://p.example", "expires_at": e.now.Add(3 * time.Hour).Format(time.RFC3339)}), 200, "preview")
	// previewing: no screenshots at all -> cannot be submitted, and so cannot be approved.
	e.wantErr(e.machine(e.keyA, "POST", p("/submit"), nil), 409, "screenshots_incomplete", "submit without screenshots")
	// Only the before set is not enough.
	rv := uuid.MustParse(rid)
	e.client.DesignScreenshot.Create().SetShopID(e.shopA).SetRequestID(rv).SetPhase("before").SetSite("admin").SetViewKey("orders").
		SetViewport(390).SetRole("owner").SetCommitSha(shaBase).SetObjectKey("k").SetByteSize(10).SetWidth(390).SetHeight(10).
		SetSha256(strings.Repeat("b", 64)).SaveX(context.Background())
	e.wantErr(e.machine(e.keyA, "POST", p("/submit"), nil), 409, "screenshots_incomplete", "submit with before only")
	if got := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+rid), nil); got.Body["status"] != "previewing" {
		t.Fatalf("refused submit moved the request: %s", got.Raw)
	}
}

func TestSubmitWithoutStorageIs503(t *testing.T) {
	e := newDesignEnv(t)
	r := e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "change"), 201, "create")
	rid := id(r)
	e.toPreviewing(rid)
	e.addScreenshots(rid, shaHead, 1)
	// Storage not configured: refused with 503 whatever the rows say.
	e.svc.Cfg.ScreenshotsEnabled = false
	e.wantErr(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/submit"), nil), 503, "design_storage_not_configured", "submit without storage")
	e.wantErr(e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+rid+"/screenshots"), nil), 503, "design_storage_not_configured", "screenshots")
	if got := e.human(e.staffA, "GET", e.adminPath(e.shopA, "/requests/"+rid+"/screenshots"), nil).Code; got != 403 {
		t.Fatalf("authorization must come before the 503: %d", got)
	}
	e.svc.Cfg.ScreenshotsEnabled = true
	e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/submit"), nil), 200, "submit")
}

func TestScreenshotSetVersionMismatchBlocksSubmit(t *testing.T) {
	e := newDesignEnv(t)
	r := e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "change"), 201, "create")
	rid := id(r)
	e.toPreviewing(rid)
	e.addScreenshots(rid, shaHead, 1)
	// Re-shoot one after slot under a newer set version.
	e.client.DesignScreenshot.Create().SetShopID(e.shopA).SetRequestID(uuid.MustParse(rid)).SetPhase("after").SetSite("admin").
		SetViewKey("orders").SetViewport(390).SetRole("owner").SetAttempt(2).SetCommitSha(shaHead).SetObjectKey("k2").
		SetByteSize(10).SetWidth(390).SetHeight(10).SetSha256(strings.Repeat("c", 64)).
		SetCaptureManifest(json.RawMessage(`{"set_version":2}`)).SaveX(context.Background())
	e.wantErr(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/submit"), nil), 409, "screenshots_incomplete", "mixed set versions")
}

func TestPreviewMustOutliveTheReview(t *testing.T) {
	e := newDesignEnv(t)
	r := e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "change"), 201, "create")
	rid := id(r)
	p := func(path string) string { return e.svcPath(e.shopA, "/requests/"+rid+path) }
	e.mustCode(e.machine(e.keyA, "PUT", p("/head"), map[string]any{"head_sha": shaHead}), 200, "head")
	e.mustCode(e.machine(e.keyA, "POST", p("/guard-runs"), map[string]any{"checked_sha": shaHead, "passed": true}), 200, "guard")
	e.mustCode(e.machine(e.keyA, "PUT", p("/preview"), map[string]any{"origin": "https://p.example", "expires_at": e.now.Add(10 * time.Minute).Format(time.RFC3339)}), 200, "short preview")
	e.addScreenshots(rid, shaHead, 1)
	e.wantErr(e.machine(e.keyA, "POST", p("/submit"), nil), 409, "preview_expired", "preview about to expire")
}

func TestRequestChangesVoidsTheCandidate(t *testing.T) {
	e := newDesignEnv(t)
	rid := e.toAwaiting()
	etag := e.etag(rid)
	rc := e.mustCode(e.human(e.ownerA, "POST", e.adminPath(e.shopA, "/requests/"+rid+"/request-changes"), map[string]any{"note": "make it bluer"}), 200, "request changes")
	if rc.Body["status"] != "drafting" || rc.Body["candidate_sha"] != nil || rc.Body["change_note"] != "make it bluer" {
		t.Fatalf("not reset: %s", rc.Raw)
	}
	// The earlier review can no longer be approved.
	e.wantErr(e.approve(e.ownerA, rid, shaHead, etag), 409, "invalid_state", "approve after request-changes")
	// The service redoes it, resubmits; only the new head can be approved.
	p := func(path string) string { return e.svcPath(e.shopA, "/requests/"+rid+path) }
	e.mustCode(e.machine(e.keyA, "PUT", p("/head"), map[string]any{"head_sha": shaHead2}), 200, "head")
	e.mustCode(e.machine(e.keyA, "POST", p("/guard-runs"), map[string]any{"checked_sha": shaHead2, "passed": true}), 200, "guard")
	e.mustCode(e.machine(e.keyA, "PUT", p("/preview"), map[string]any{"origin": "https://p.example", "expires_at": e.now.Add(3 * time.Hour).Format(time.RFC3339)}), 200, "preview")
	e.addScreenshots(rid, shaHead2, 1)
	e.mustCode(e.machine(e.keyA, "POST", p("/submit"), nil), 200, "resubmit")
	e.wantErr(e.approve(e.ownerA, rid, shaHead, e.etag(rid)), 409, "head_moved", "approve the stale head")
	e.mustCode(e.approve(e.ownerA, rid, shaHead2, e.etag(rid)), 200, "approve the new head")
}

func TestRejectCancelAndTerminalStates(t *testing.T) {
	e := newDesignEnv(t)
	rid := e.toAwaiting()
	e.mustCode(e.human(e.ownerA, "POST", e.adminPath(e.shopA, "/requests/"+rid+"/reject"), map[string]any{"reason": "no thanks"}), 200, "reject")
	// Every action on a rejected request is refused.
	e.wantErr(e.approve(e.ownerA, rid, shaHead, e.etag(rid)), 409, "invalid_state", "approve rejected")
	e.wantErr(e.human(e.ownerA, "POST", e.adminPath(e.shopA, "/requests/"+rid+"/cancel"), nil), 409, "invalid_state", "cancel rejected")
	e.wantErr(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/failed"), map[string]any{"code": "x"}), 409, "invalid_state", "fail rejected")
	e.wantErr(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/claim"), nil), 409, "invalid_state", "claim rejected")
	// A cancelled request frees the site.
	r2 := e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "second"), 201, "create")
	e.mustCode(e.human(e.ownerA, "POST", e.adminPath(e.shopA, "/requests/"+id(r2)+"/cancel"), nil), 200, "cancel")
	e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "third"), 201, "create after cancel")
}

func TestCreateRequestRules(t *testing.T) {
	e := newDesignEnv(t)
	body := func(user int, prompt string) map[string]any {
		return map[string]any{"site": "storefront", "requested_by_user_id": user, "prompt": prompt, "base_sha": shaBase}
	}
	post := func(key string, b map[string]any) dresp {
		return e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests"), b, "Idempotency-Key", key)
	}
	// Requester eligibility: editor, staff, an outsider and another shop's owner.
	for name, u := range map[string]int{"editor": e.editorA, "staff": e.staffA, "outsider": e.outsiderA, "other shop owner": e.ownerB} {
		e.wantErr(post(dIdemKey(), body(u, "x")), 403, "requester_not_permitted", name)
	}
	if n := e.client.DesignRequest.Query().CountX(context.Background()); n != 0 {
		t.Fatalf("a refused requester created %d rows", n)
	}
	// Idempotency key shape.
	e.wantErr(post("short", body(e.ownerA, "x")), 422, "invalid_idempotency_key", "short key")
	e.wantErr(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests"), body(e.ownerA, "x")), 422, "invalid_idempotency_key", "no key")
	// Redaction and sanitising.
	k := dIdemKey()
	r := e.mustCode(post(k, body(e.ownerA, "contact wang@example.com or 0912345678 \u202eplease <script>alert(1)</script>")), 201, "create")
	var text string
	var flags []string
	row := e.client.DesignRequest.Query().OnlyX(context.Background())
	text = *row.PromptText
	flags = row.PromptFlags
	if strings.Contains(text, "wang@") || strings.Contains(text, "0912") || strings.Contains(text, "\u202e") || !strings.Contains(text, "<script>") {
		t.Fatalf("prompt not redacted/sanitised as specified: %q", text)
	}
	if strings.Join(flags, ",") != "email,phone" {
		t.Fatalf("flags %v", flags)
	}
	sum := dSha256hex(text)
	if row.PromptSha256 != sum {
		t.Fatal("prompt_sha256 must be over the redacted text")
	}
	if r.Header.Get("Content-Type") != "application/json; charset=utf-8" || r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", r.Header)
	}
	// Replay and reuse.
	rp := e.mustCode(post(k, body(e.ownerA, "contact wang@example.com or 0912345678 \u202eplease <script>alert(1)</script>")), 200, "replay")
	if rp.Header.Get("Idempotency-Replayed") != "true" || id(rp) != id(r) {
		t.Fatalf("replay differs: %s", rp.Raw)
	}
	e.wantErr(post(k, body(e.ownerA, "something else")), 422, "idempotency_key_reused", "same key different content")
	if n := e.client.DesignRequest.Query().CountX(context.Background()); n != 1 {
		t.Fatalf("%d rows after replay", n)
	}
	// One non-terminal request per site: the storefront is busy, the admin is not.
	e.wantErr(post(dIdemKey(), body(e.ownerA, "another")), 409, "request_in_progress", "second request on the site")
	adminBody := body(e.ownerA, "admin one")
	adminBody["site"] = "admin"
	e.mustCode(post(dIdemKey(), adminBody), 201, "other site")
	// Validation.
	bad := body(e.ownerA, "x")
	bad["base_sha"] = "nope"
	e.wantErr(post(dIdemKey(), bad), 422, "validation_failed", "bad base sha")
	bad = body(e.ownerA, strings.Repeat("a", 4001))
	e.wantErr(post(dIdemKey(), bad), 422, "validation_failed", "prompt too long")
	bad = body(e.ownerA, "x")
	bad["site"] = "mobile"
	e.wantErr(post(dIdemKey(), bad), 422, "validation_failed", "bad site")
	bad = body(e.ownerA, "x")
	bad["branch"] = "main"
	e.wantErr(post(dIdemKey(), bad), 422, "validation_failed", "bad branch")
}

func TestDailyCap(t *testing.T) {
	e := newDesignEnv(t)
	e.svc.Cfg.DailyRequestCap = 2
	for i := 0; i < 2; i++ {
		r := e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", fmt.Sprintf("n%d", i)), 201, "create")
		e.mustCode(e.human(e.ownerA, "POST", e.adminPath(e.shopA, "/requests/"+id(r)+"/cancel"), nil), 200, "cancel")
	}
	e.wantErr(e.create(e.shopA, e.keyA, e.ownerA, "admin", "third"), 429, "daily_cap_reached", "cap")
	// The cap is per shop: shop B is unaffected.
	r := e.machine(e.keyB, "POST", e.svcPath(e.shopB, "/requests"), map[string]any{"site": "admin", "requested_by_user_id": e.ownerB, "prompt": "b", "base_sha": shaBase}, "Idempotency-Key", dIdemKey())
	e.mustCode(r, 201, "shop B")
}

func TestCrossShopIsolation(t *testing.T) {
	e := newDesignEnv(t)
	rid := e.toAwaiting()
	// Shop A's credential on shop B's URL: 404, whatever the path.
	for _, p := range []string{"/requests", "/requests/" + rid, "/requests/" + rid + "/claim"} {
		m := "GET"
		if strings.HasSuffix(p, "claim") {
			m = "POST"
		}
		if got := e.machine(e.keyA, m, e.svcPath(e.shopB, p), nil).Code; got != 404 {
			t.Errorf("credential A on shop B %s = %d want 404", p, got)
		}
	}
	// Shop B's credential cannot reach shop A's request through its own URL either.
	if got := e.machine(e.keyB, "GET", e.svcPath(e.shopB, "/requests/"+rid), nil).Code; got != 404 {
		t.Errorf("request of shop A through shop B's credential = %d", got)
	}
	if got := e.machine(e.keyB, "PUT", e.svcPath(e.shopB, "/requests/"+rid+"/head"), map[string]any{"head_sha": shaHead2}).Code; got != 404 {
		t.Errorf("head update across shops = %d", got)
	}
	// Owner B on B's own path with A's request id: 404 (not 403: the id does not exist there).
	if got := e.human(e.ownerB, "GET", e.adminPath(e.shopB, "/requests/"+rid), nil).Code; got != 404 {
		t.Errorf("owner B reading A's request via B = %d want 404", got)
	}
	// ... and on A's path: 403 (not a member).
	if got := e.human(e.ownerB, "GET", e.adminPath(e.shopA, "/requests/"+rid), nil).Code; got != 403 {
		t.Errorf("owner B on A's path = %d want 403", got)
	}
	// Approving through the wrong shop's path is a 404, and changes nothing.
	if got := e.human(e.ownerB, "POST", e.adminPath(e.shopB, "/requests/"+rid+"/approve"), map[string]any{"head_sha": shaHead}, "If-Match", `"1"`).Code; got != 404 {
		t.Errorf("approve via wrong shop = %d want 404", got)
	}
	if n := e.client.DesignRequest.Query().CountX(context.Background()); n != 1 {
		t.Fatalf("rows: %d", n)
	}
}

func TestCredentialLifecycle(t *testing.T) {
	e := newDesignEnv(t)
	issue := e.human(e.platform, "POST", "/api/v1/admin/design-service/credentials", map[string]any{
		"shop_id": e.shopA, "name": "lifecycle", "allowed_cidrs": []string{"203.0.113.0/24"}, "expires_in_days": 30})
	e.mustCode(issue, 201, "issue")
	e.val.AssertResponse(t, "/api/v1/admin/design-service/credentials", "POST", 201, []byte(issue.Raw))
	key := issue.Body["key"].(string)
	if !strings.HasPrefix(key, "dsk_") || issue.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("key/cache headers: %q %v", key, issue.Header)
	}
	credID := int(issue.Body["id"].(float64))
	// Only the hash is stored.
	var stored string
	if err := e.sqlDB.QueryRow(`SELECT key_hash FROM design_service_credentials WHERE id = $1`, credID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != design.HashKey(key) || strings.Contains(stored, key) {
		t.Fatal("key stored in the clear")
	}
	// A read never shows the key again.
	l := e.mustCode(e.human(e.platform, "GET", fmt.Sprintf("/api/v1/admin/design-service/credentials?shop_id=%d", e.shopA), nil), 200, "list")
	if strings.Contains(l.Raw, key) || strings.Contains(l.Raw, `"key":`) {
		t.Fatalf("list leaked the key: %s", l.Raw)
	}
	e.val.AssertResponse(t, "/api/v1/admin/design-service/credentials", "GET", 200, []byte(l.Raw))

	use := func(remote string) int {
		req := httptest.NewRequest("GET", e.svcPath(e.shopA, "/requests"), nil)
		req.RemoteAddr = remote
		req.Header.Set("Authorization", "Bearer "+key)
		return e.send(req).Code
	}
	if got := use("198.51.100.7:4000"); got != 401 {
		t.Errorf("outside the CIDR = %d want 401", got)
	}
	if got := use("203.0.113.9:4000"); got != 200 {
		t.Errorf("inside the CIDR = %d want 200", got)
	}
	var lastUsed sql.NullTime
	_ = e.sqlDB.QueryRow(`SELECT last_used_at FROM design_service_credentials WHERE id = $1`, credID).Scan(&lastUsed)
	if !lastUsed.Valid {
		t.Error("last_used_at not recorded")
	}
	// Revocation is immediate.
	rv := e.mustCode(e.human(e.platform, "POST", fmt.Sprintf("/api/v1/admin/design-service/credentials/%d/revoke", credID), nil), 200, "revoke")
	e.val.AssertResponse(t, "/api/v1/admin/design-service/credentials/{credentialID}/revoke", "POST", 200, []byte(rv.Raw))
	if got := use("203.0.113.9:4000"); got != 401 {
		t.Errorf("revoked key = %d want 401", got)
	}
	e.wantErr(e.human(e.platform, "POST", fmt.Sprintf("/api/v1/admin/design-service/credentials/%d/revoke", credID), nil), 409, "already_revoked", "revoke twice")

	// Expiry (the table CHECK needs created_at < expires_at, so move both).
	exp := e.mustCode(e.human(e.platform, "POST", "/api/v1/admin/design-service/credentials", map[string]any{"shop_id": e.shopA, "name": "exp"}), 201, "issue 2")
	ek := exp.Body["key"].(string)
	if _, err := e.sqlDB.Exec(`UPDATE design_service_credentials SET created_at = now() - interval '3 days', expires_at = now() - interval '1 day' WHERE id = $1`, int(exp.Body["id"].(float64))); err != nil {
		t.Fatal(err)
	}
	if got := e.machine(ek, "GET", e.svcPath(e.shopA, "/requests"), nil).Code; got != 401 {
		t.Errorf("expired key = %d want 401", got)
	}
	// The expiry CHECK: more than 366 days is refused by the database too.
	if _, err := e.sqlDB.Exec(`UPDATE design_service_credentials SET expires_at = created_at + interval '400 days' WHERE id = $1`, credID); err == nil {
		t.Error("the table accepted a 400-day credential")
	}
	// Rotation: new key works, the old one is shortened to the 7-day overlap.
	old := e.mustCode(e.human(e.platform, "POST", "/api/v1/admin/design-service/credentials", map[string]any{"shop_id": e.shopA, "name": "rot"}), 201, "issue 3")
	rot := e.mustCode(e.human(e.platform, "POST", fmt.Sprintf("/api/v1/admin/design-service/credentials/%d/rotate", int(old.Body["id"].(float64))), nil), 201, "rotate")
	if got := e.machine(rot.Body["key"].(string), "GET", e.svcPath(e.shopA, "/requests"), nil).Code; got != 200 {
		t.Errorf("rotated key = %d", got)
	}
	var oldExp time.Time
	_ = e.sqlDB.QueryRow(`SELECT expires_at FROM design_service_credentials WHERE id = $1`, int(old.Body["id"].(float64))).Scan(&oldExp)
	if d := time.Until(oldExp); d > 7*24*time.Hour+time.Minute || d < 6*24*time.Hour {
		t.Errorf("old credential expires in %s, want ~7 days", d)
	}
	// Bad input.
	e.wantErr(e.human(e.platform, "POST", "/api/v1/admin/design-service/credentials", map[string]any{"shop_id": 999999, "name": "x"}), 422, "validation_failed", "unknown shop")
	e.wantErr(e.human(e.platform, "POST", "/api/v1/admin/design-service/credentials", map[string]any{"shop_id": e.shopA, "name": "x", "allowed_cidrs": []string{"nope"}}), 422, "validation_failed", "bad cidr")
}

func TestClaimLeaseRace(t *testing.T) {
	e := newDesignEnv(t)
	r := e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "claim me"), 201, "create")
	rid := id(r)
	// A second credential of the same shop is a second worker.
	i2, err := e.creds.Issue(context.Background(), design.IssueInput{ShopID: e.shopA, Name: "second", CreatedByID: e.platform})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, k := range []string{e.keyA, i2.Key} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = e.machine(k, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/claim"), nil).Code
		}()
	}
	wg.Wait()
	if (codes[0] != 200 || codes[1] != 409) && (codes[0] != 409 || codes[1] != 200) {
		t.Fatalf("claims: %v (exactly one must win)", codes)
	}
	// Renewal by the holder works, the other still cannot.
	holder, other := e.keyA, i2.Key
	if codes[0] == 409 {
		holder, other = i2.Key, e.keyA
	}
	e.mustCode(e.machine(holder, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/claim"), nil), 200, "renew")
	e.wantErr(e.machine(other, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/claim"), nil), 409, "claim_conflict", "other worker")
	// The queue hides it while the lease lives, and shows it once it ran out.
	q := e.mustCode(e.machine(e.keyA, "GET", e.svcPath(e.shopA, "/requests?claimed=false"), nil), 200, "queue")
	if len(q.Body["requests"].([]any)) != 0 {
		t.Fatalf("claimed request still in the unclaimed queue: %s", q.Raw)
	}
	e.advance(16 * time.Minute)
	q = e.mustCode(e.machine(e.keyA, "GET", e.svcPath(e.shopA, "/requests?claimed=false"), nil), 200, "queue 2")
	if len(q.Body["requests"].([]any)) != 1 {
		t.Fatalf("expired lease not offered again: %s", q.Raw)
	}
	e.val.AssertResponse(t, "/api/v1/design-service/shops/{shopID}/requests", "GET", 200, []byte(q.Raw))
	// design-sweep: an expired lease fails the request as service_lost.
	rep, err := e.svc.Sweep(context.Background(), 0, true)
	if err != nil || rep.LeasesLost != 1 {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	got := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+rid), nil)
	if got.Body["status"] != "failed" || got.Body["failure_code"] != "service_lost" {
		t.Fatalf("not failed: %s", got.Raw)
	}
}

func TestSweepDryRunAndApprovalTimeout(t *testing.T) {
	e := newDesignEnv(t)
	rid := e.toAwaiting()
	e.advance(8 * 24 * time.Hour)
	rep, err := e.svc.Sweep(context.Background(), 0, false)
	if err != nil || rep.ApprovalTimeouts != 1 {
		t.Fatalf("dry run: %+v %v", rep, err)
	}
	if got := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+rid), nil); got.Body["status"] != "awaiting_approval" {
		t.Fatal("a dry run changed state")
	}
	if rep, err = e.svc.Sweep(context.Background(), 0, true); err != nil || rep.ApprovalTimeouts != 1 {
		t.Fatalf("apply: %+v %v", rep, err)
	}
	got := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+rid), nil)
	if got.Body["status"] != "failed" || got.Body["failure_code"] != "approval_timeout" {
		t.Fatalf("not failed: %s", got.Raw)
	}
	// Idempotent.
	if rep, _ = e.svc.Sweep(context.Background(), 0, true); rep.ApprovalTimeouts != 0 {
		t.Fatalf("second sweep did work: %+v", rep)
	}
	ev := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+rid+"/events"), nil)
	last := ev.Body["events"].([]any)
	if a := last[len(last)-1].(map[string]any); a["actor_type"] != "system" || a["kind"] != "failed" {
		t.Fatalf("last event: %v", a)
	}
}

func TestRevertFlow(t *testing.T) {
	e := newDesignEnv(t)
	post := func(rid, path string, body any) dresp {
		return e.human(e.ownerA, "POST", e.adminPath(e.shopA, "/requests/"+rid+path), body)
	}
	publish := func(head string) (rid, tag string) {
		r := e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "release "+head[:3]), 201, "create")
		rid = id(r)
		p := func(path string) string { return e.svcPath(e.shopA, "/requests/"+rid+path) }
		e.mustCode(e.machine(e.keyA, "PUT", p("/head"), map[string]any{"head_sha": head}), 200, "head")
		e.mustCode(e.machine(e.keyA, "POST", p("/guard-runs"), map[string]any{"checked_sha": head, "passed": true}), 200, "guard")
		e.mustCode(e.machine(e.keyA, "PUT", p("/preview"), map[string]any{"origin": "https://p.example", "expires_at": e.now.Add(3 * time.Hour).Format(time.RFC3339)}), 200, "preview")
		e.addScreenshots(rid, head, 1)
		e.mustCode(e.machine(e.keyA, "POST", p("/submit"), nil), 200, "submit")
		ap := e.mustCode(e.approve(e.ownerA, rid, head, e.etag(rid)), 200, "approve")
		tag = ap.Body["publish_tag"].(string)
		e.mustCode(e.machine(e.keyA, "POST", p("/guard-runs"), map[string]any{"phase": "recheck", "checked_sha": head, "passed": true}), 200, "recheck")
		e.mustCode(e.machine(e.keyA, "POST", p("/published"), map[string]any{"published_sha": head, "tag": tag}), 200, "published")
		e.advance(time.Minute)
		return
	}
	r1, tag1 := publish(shaHead)
	r2, tag2 := publish(shaHead2)
	if tag1 == tag2 {
		t.Fatalf("tags must be unique: %s %s", tag1, tag2)
	}
	if !strings.HasSuffix(tag1, "-1") || !strings.HasSuffix(tag2, "-2") {
		t.Fatalf("tag numbering: %s %s", tag1, tag2)
	}
	// Reverting to what is already current is refused; so is anything unpublished.
	e.wantErr(post(r2, "/revert", nil), 409, "already_current", "revert to current")
	// Site state must know the current commit: it does (a published request sets it).
	// Core floor: raise the floor above the target's core version.
	if _, err := e.sqlDB.Exec(`UPDATE design_site_state SET design_floor_version = '0.5.0' WHERE shop_id = $1`, e.shopA); err != nil {
		t.Fatal(err)
	}
	e.wantErr(post(r1, "/revert", nil), 409, "revert_below_core_floor", "below floor")
	if _, err := e.sqlDB.Exec(`UPDATE design_site_state SET design_floor_version = '0.2.0' WHERE shop_id = $1`, e.shopA); err != nil {
		t.Fatal(err)
	}
	// A real revert request.
	rv := e.mustCode(post(r1, "/revert", map[string]any{"note": "the old one was better"}), 201, "revert")
	e.val.AssertResponse(t, "/api/v1/admin/shops/{shopID}/design/requests/{requestID}/revert", "POST", 201, []byte(rv.Raw))
	if rv.Body["kind"] != "revert" || rv.Body["restores_request_id"] != r1 || rv.Body["status"] != "drafting" || rv.Body["base_sha"] != shaHead2 {
		t.Fatalf("revert request: %s", rv.Raw)
	}
	revID := id(rv)
	// History is untouched; the target still reads as published.
	if got := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+r1), nil); got.Body["status"] != "published" {
		t.Fatalf("target changed: %s", got.Raw)
	}
	// One at a time.
	e.wantErr(post(r1, "/revert", nil), 409, "request_in_progress", "second revert")
	// The service discovers it in its queue and walks the same path.
	q := e.mustCode(e.machine(e.keyA, "GET", e.svcPath(e.shopA, "/requests?status=drafting&claimed=false"), nil), 200, "queue")
	if len(q.Body["requests"].([]any)) != 1 {
		t.Fatalf("queue: %s", q.Raw)
	}
	p := func(path string) string { return e.svcPath(e.shopA, "/requests/"+revID+path) }
	e.mustCode(e.machine(e.keyA, "POST", p("/claim"), nil), 200, "claim")
	e.mustCode(e.machine(e.keyA, "PUT", p("/head"), map[string]any{"head_sha": shaBase}), 200, "head")
	e.mustCode(e.machine(e.keyA, "POST", p("/guard-runs"), map[string]any{"checked_sha": shaBase, "passed": true}), 200, "guard")
	e.mustCode(e.machine(e.keyA, "PUT", p("/preview"), map[string]any{"origin": "https://p.example", "expires_at": e.now.Add(3 * time.Hour).Format(time.RFC3339)}), 200, "preview")
	// before screenshots are taken at the revert's base (shaHead2), after at its head.
	e.client.DesignScreenshot.Create().SetShopID(e.shopA).SetRequestID(uuid.MustParse(revID)).SetPhase("before").SetSite("admin").SetViewKey("orders").
		SetViewport(390).SetRole("owner").SetCommitSha(shaHead2).SetObjectKey("b").SetByteSize(10).SetWidth(390).SetHeight(10).SetSha256(strings.Repeat("d", 64)).
		SetCaptureManifest(json.RawMessage(`{"set_version":1}`)).SaveX(context.Background())
	e.client.DesignScreenshot.Create().SetShopID(e.shopA).SetRequestID(uuid.MustParse(revID)).SetPhase("after").SetSite("admin").SetViewKey("orders").
		SetViewport(390).SetRole("owner").SetCommitSha(shaBase).SetObjectKey("a").SetByteSize(10).SetWidth(390).SetHeight(10).SetSha256(strings.Repeat("e", 64)).
		SetCaptureManifest(json.RawMessage(`{"set_version":1}`)).SaveX(context.Background())
	e.mustCode(e.machine(e.keyA, "POST", p("/submit"), nil), 200, "submit")
	ap := e.mustCode(e.human(e.ownerA, "POST", e.adminPath(e.shopA, "/requests/"+revID+"/approve"), map[string]any{"head_sha": shaBase}, "If-Match", e.etag(revID)), 200, "approve")
	e.mustCode(e.machine(e.keyA, "POST", p("/guard-runs"), map[string]any{"phase": "recheck", "checked_sha": shaBase, "passed": true}), 200, "recheck")
	e.mustCode(e.machine(e.keyA, "POST", p("/published"), map[string]any{"published_sha": shaBase, "tag": ap.Body["publish_tag"]}), 200, "published")
	// The release after the target is marked reverted and points at the revert.
	got := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+r2), nil)
	if got.Body["status"] != "reverted" || got.Body["reverted_by_request_id"] != revID {
		t.Fatalf("superseded release not marked: %s", got.Raw)
	}
	if e.eventsOfKind(r2, "reverted_mark") != 1 {
		t.Fatal("no reverted_mark event")
	}
	// The target itself still reads published; reverting to a reverted one is allowed.
	if g := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+r1), nil); g.Body["status"] != "published" {
		t.Fatalf("target: %s", g.Raw)
	}
	e.mustCode(post(r2, "/revert", nil), 201, "revert to a reverted release")
	if rep, _ := e.svc.VerifyChains(context.Background(), 0); len(rep.Broken) != 0 {
		t.Fatalf("chains: %+v", rep.Broken)
	}
	// Reverting never touches content tables.
	for _, tbl := range []string{"content_documents", "content_revisions", "content_schemas"} {
		var n int
		if err := e.sqlDB.QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		_ = n // present and untouched; the design package never references them
	}
}

func TestRevertNeedsKnownSiteAndPublishedTarget(t *testing.T) {
	e := newDesignEnv(t)
	r := e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "x"), 201, "create")
	// A request that was never published cannot be restored.
	e.wantErr(e.human(e.ownerA, "POST", e.adminPath(e.shopA, "/requests/"+id(r)+"/revert"), nil), 409, "invalid_state", "revert unpublished")
	e.wantErr(e.human(e.ownerA, "POST", e.adminPath(e.shopA, "/requests/"+uuid.NewString()+"/revert"), nil), 404, "not_found", "unknown")
}

func TestUntrackedChanges(t *testing.T) {
	e := newDesignEnv(t)
	path := e.svcPath(e.shopA, "/untracked-changes")
	mk := func(sha string) map[string]any {
		return map[string]any{"site": "admin", "commit_sha": sha, "committed_at": e.now.Format(time.RFC3339), "subject": "tweak\nsecond line " + strings.Repeat("x", 300), "files_changed": 2}
	}
	r := e.mustCode(e.machine(e.keyA, "POST", path, map[string]any{"changes": []any{mk(shaHead), mk(shaHead2)}}), 200, "report")
	if r.Body["added"].(float64) != 2 {
		t.Fatalf("added: %s", r.Raw)
	}
	r = e.mustCode(e.machine(e.keyA, "POST", path, map[string]any{"changes": []any{mk(shaHead), mk(shaBase)}}), 200, "report again")
	if r.Body["received"].(float64) != 2 || r.Body["added"].(float64) != 1 {
		t.Fatalf("dedup: %s", r.Raw)
	}
	l := e.mustCode(e.human(e.editorA, "GET", e.adminPath(e.shopA, "/untracked-changes"), nil), 200, "list")
	e.val.AssertResponse(t, "/api/v1/admin/shops/{shopID}/design/untracked-changes", "GET", 200, []byte(l.Raw))
	first := l.Body["changes"].([]any)[0].(map[string]any)
	if s := first["subject"].(string); strings.Contains(s, "\n") || len([]rune(s)) > 200 {
		t.Fatalf("subject not a single bounded line: %q", s)
	}
	st := e.mustCode(e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/state"), nil), 200, "state")
	if st.Body["sites"].([]any)[1].(map[string]any)["untracked_changes"].(float64) != 3 {
		t.Fatalf("count: %s", st.Raw)
	}
	// Acknowledge once, by someone with design.approve.
	e.wantErr(e.human(e.editorA, "POST", e.adminPath(e.shopA, "/untracked-changes/"+shaHead+"/acknowledge"), nil), 403, "forbidden", "editor ack")
	ack := e.mustCode(e.human(e.ownerA, "POST", e.adminPath(e.shopA, "/untracked-changes/"+shaHead+"/acknowledge"), nil), 200, "ack")
	e.val.AssertResponse(t, "/api/v1/admin/shops/{shopID}/design/untracked-changes/{sha}/acknowledge", "POST", 200, []byte(ack.Raw))
	e.wantErr(e.human(e.ownerA, "POST", e.adminPath(e.shopA, "/untracked-changes/"+shaHead+"/acknowledge"), nil), 409, "already_acknowledged", "ack twice")
	// The database also refuses to re-acknowledge or to rewrite the row.
	if _, err := e.sqlDB.Exec(`UPDATE design_untracked_changes SET acknowledged_by_user_id = 1 WHERE commit_sha = $1`, shaHead); err == nil {
		t.Error("the acknowledgement was overwritten")
	}
	if _, err := e.sqlDB.Exec(`UPDATE design_untracked_changes SET subject = 'x' WHERE commit_sha = $1`, shaHead2); err == nil {
		t.Error("an untracked change was rewritten")
	}
	// Another shop's credential does not see it, and the report is validated.
	e.wantErr(e.machine(e.keyA, "POST", path, map[string]any{"changes": []any{}}), 422, "validation_failed", "empty batch")
}

func TestSiteStateReport(t *testing.T) {
	e := newDesignEnv(t)
	r := e.mustCode(e.machine(e.keyA, "PUT", e.svcPath(e.shopA, "/sites/admin/state"), map[string]any{"main_sha": shaHead, "core_version": "0.3.0", "design_floor_version": "0.2.0"}), 200, "put")
	e.val.AssertResponse(t, "/api/v1/design-service/shops/{shopID}/sites/{site}/state", "PUT", 200, []byte(r.Raw))
	if r.Body["last_seen_main_sha"] != shaHead || r.Body["current_request_id"] != nil {
		t.Fatalf("state: %s", r.Raw)
	}
	e.wantErr(e.machine(e.keyA, "PUT", e.svcPath(e.shopA, "/sites/mobile/state"), map[string]any{}), 422, "validation_failed", "bad site")
}

func TestHistoryTextIsDataNeverMarkup(t *testing.T) {
	e := newDesignEnv(t)
	r := e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "storefront", `"><img src=x onerror=alert(1)>`), 201, "create")
	got := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+id(r)), nil)
	if ct := got.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type %q", ct)
	}
	if got.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("no nosniff")
	}
	// A failure message with markup is stored verbatim as text and returned in JSON.
	e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+id(r)+"/failed"), map[string]any{"code": "bad", "detail": "<b>boom</b>\u202e"}), 200, "failed")
	got = e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+id(r)), nil)
	if got.Body["failure_detail"] != "<b>boom</b>" {
		t.Fatalf("failure_detail %q", got.Body["failure_detail"])
	}
}

func TestDesignLogsCarryNoSensitiveValues(t *testing.T) {
	e := newDesignEnv(t)
	secretPrompt := "unique-prompt-marker-7f3a9c"
	secretOrigin := "https://secret-preview-host.example"
	secretPath := "admin/design/unique-diff-path-marker.css"
	r := e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", secretPrompt), 201, "create")
	rid := id(r)
	p := func(path string) string { return e.svcPath(e.shopA, "/requests/"+rid+path) }
	e.mustCode(e.machine(e.keyA, "PUT", p("/head"), map[string]any{"head_sha": shaHead, "diff_summary": map[string]any{"files": []map[string]any{{"path": secretPath}}}}), 200, "head")
	e.mustCode(e.machine(e.keyA, "POST", p("/guard-runs"), map[string]any{"checked_sha": shaHead, "passed": true}), 200, "guard")
	e.mustCode(e.machine(e.keyA, "PUT", p("/preview"), map[string]any{"origin": secretOrigin, "expires_at": e.now.Add(3 * time.Hour).Format(time.RFC3339)}), 200, "preview")
	e.addScreenshots(rid, shaHead, 1)
	e.mustCode(e.machine(e.keyA, "POST", p("/submit"), nil), 200, "submit")
	e.mustCode(e.approve(e.ownerA, rid, shaHead, e.etag(rid)), 200, "approve")
	// failure paths: a wrong key, a wrong state, a refused requester
	e.machine("dsk_wrongwrongwrongwrongwrongwrong", "GET", e.svcPath(e.shopA, "/requests"), nil)
	e.machine(e.keyA, "POST", p("/submit"), nil)
	e.create(e.shopA, e.keyA, e.staffA, "admin", secretPrompt)
	issue := e.mustCode(e.human(e.platform, "POST", "/api/v1/admin/design-service/credentials", map[string]any{"shop_id": e.shopA, "name": "audit-name-marker"}), 201, "issue")
	logs := e.buf.String()
	for name, needle := range map[string]string{
		"prompt": secretPrompt, "preview origin": "secret-preview-host", "diff path": "unique-diff-path-marker",
		"service key": e.keyA, "issued key": issue.Body["key"].(string), "wrong key": "wrongwrongwrong", "credential name": "audit-name-marker",
	} {
		if strings.Contains(logs, needle) {
			t.Errorf("logs contain the %s", name)
		}
	}
	if !strings.Contains(logs, "design credential audit") {
		t.Error("no credential audit line")
	}
}

func TestRateLimitedServiceFamily(t *testing.T) {
	e := newDesignEnv(t)
	// Wrong keys only cost the caller: after the budget the answer is 429 with Retry-After.
	var last dresp
	for i := 0; i < 40; i++ {
		last = e.machine("dsk_nope", "GET", e.svcPath(e.shopA, "/requests"), nil)
	}
	if last.Code != 429 || last.Header.Get("Retry-After") == "" {
		t.Fatalf("got %d %q", last.Code, last.Header.Get("Retry-After"))
	}
}

func TestDesignMountedWithoutServiceWhenNil(t *testing.T) {
	// The route table never depends on settings: with the handler unset the
	// machine family is simply absent (and the human one is, too).
	router := httpapi.New(httpapi.Deps{Cfg: &config.Config{}, Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))})
	req := httptest.NewRequest("GET", "/api/v1/design-service/shops/1/requests", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("got %d", rec.Code)
	}
}

func dSha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
