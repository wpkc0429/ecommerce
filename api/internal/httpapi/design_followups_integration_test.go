package httpapi_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/ent/designservicecredential"
)

// Follow-ups to the design history approved on 2026-11-27: shop-scoped display
// names, is_current / revert_target_tag, the pending-approval count, the
// server-side sandbox origin list, and the approval-after-preview-expiry rule.

// release drives a fresh admin request to published with the given head.
func (e *designEnv) release(head string, approver int) (rid, tag string) {
	e.t.Helper()
	rid = id(e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "release "+head[:3]), 201, "create"))
	p := func(path string) string { return e.svcPath(e.shopA, "/requests/"+rid+path) }
	e.mustCode(e.machine(e.keyA, "PUT", p("/head"), map[string]any{"head_sha": head}), 200, "head")
	e.mustCode(e.machine(e.keyA, "POST", p("/guard-runs"), map[string]any{"checked_sha": head, "passed": true}), 200, "guard")
	e.mustCode(e.machine(e.keyA, "PUT", p("/preview"), map[string]any{"origin": "https://p.example", "expires_at": e.now.Add(3 * time.Hour).Format(time.RFC3339)}), 200, "preview")
	e.addScreenshots(rid, head, 1)
	e.mustCode(e.machine(e.keyA, "POST", p("/submit"), nil), 200, "submit")
	ap := e.mustCode(e.approve(approver, rid, head, e.etag(rid)), 200, "approve")
	tag = ap.Body["publish_tag"].(string)
	e.mustCode(e.machine(e.keyA, "POST", p("/guard-runs"), map[string]any{"phase": "recheck", "checked_sha": head, "passed": true}), 200, "recheck")
	e.mustCode(e.machine(e.keyA, "POST", p("/published"), map[string]any{"published_sha": head, "tag": tag}), 200, "published")
	e.advance(time.Minute)
	return rid, tag
}

func rowByID(t *testing.T, list dresp, rid string) map[string]any {
	t.Helper()
	for _, x := range list.Body["requests"].([]any) {
		if m := x.(map[string]any); m["id"] == rid {
			return m
		}
	}
	t.Fatalf("request %s not in list: %s", rid, list.Raw)
	return nil
}

func TestDesignDisplayNamesAreShopScopedAndNeverPII(t *testing.T) {
	e := newDesignEnv(t)
	ctx := context.Background()
	e.client.User.UpdateOneID(e.ownerA).SetDisplayName("Alice Chen").ExecX(ctx)
	rid := e.toAwaiting()

	// Pending count: cheap, in the state endpoint, honours design.view.
	st := e.mustCode(e.human(e.editorA, "GET", e.adminPath(e.shopA, "/state"), nil), 200, "state")
	e.val.AssertResponse(t, "/api/v1/admin/shops/{shopID}/design/state", "GET", 200, []byte(st.Raw))
	if st.Body["pending_approval_count"] != float64(1) {
		t.Fatalf("pending count: %s", st.Raw)
	}
	for _, s := range st.Body["sites"].([]any) {
		m := s.(map[string]any)
		want := float64(0)
		if m["site"] == "admin" {
			want = 1
		}
		if m["pending_approval_count"] != want {
			t.Fatalf("per-site pending count: %s", st.Raw)
		}
	}
	if r := e.human(e.staffA, "GET", e.adminPath(e.shopA, "/state"), nil); r.Code != 403 {
		t.Fatalf("staff has no design.view: %d", r.Code)
	}
	if other := e.mustCode(e.human(e.ownerB, "GET", e.adminPath(e.shopB, "/state"), nil), 200, "other shop"); other.Body["pending_approval_count"] != float64(0) {
		t.Fatalf("another shop's request leaked into the count: %s", other.Raw)
	}

	// An editor (design.view only) sees the proposer's chosen name, never an email.
	l := e.mustCode(e.human(e.editorA, "GET", e.adminPath(e.shopA, "/requests"), nil), 200, "list")
	e.val.AssertResponse(t, "/api/v1/admin/shops/{shopID}/design/requests", "GET", 200, []byte(l.Raw))
	row := rowByID(t, l, rid)
	if row["proposed_by_name"] != "Alice Chen" || row["approved_by_name"] != nil || row["is_current"] != false || row["revert_target_tag"] != nil {
		t.Fatalf("summary: %v", row)
	}

	// The platform admin (no membership, no display name) approves: generic label.
	e.mustCode(e.approve(e.platform, rid, shaHead, e.etag(rid)), 200, "approve by platform admin")
	d := e.mustCode(e.human(e.editorA, "GET", e.adminPath(e.shopA, "/requests/"+rid), nil), 200, "detail")
	e.val.AssertResponse(t, "/api/v1/admin/shops/{shopID}/design/requests/{requestID}", "GET", 200, []byte(d.Raw))
	if d.Body["proposed_by_name"] != "Alice Chen" || d.Body["approved_by_name"] != "Platform admin" {
		t.Fatalf("detail names: %s", d.Raw)
	}
	// A platform admin who set a display name is shown by it.
	e.client.User.UpdateOneID(e.platform).SetDisplayName("Support Sam").ExecX(ctx)
	if d2 := e.human(e.editorA, "GET", e.adminPath(e.shopA, "/requests/"+rid), nil); d2.Body["approved_by_name"] != "Support Sam" {
		t.Fatalf("admin display name: %s", d2.Raw)
	}
	for _, leak := range []string{"owner-a@t.dev", "editor-a@t.dev", "root@test.dev"} {
		if strings.Contains(d.Raw, leak) || strings.Contains(l.Raw, leak) {
			t.Fatalf("an email leaked: %s", leak)
		}
	}
}

func TestDesignEventActorNamesAndNoLeakToServiceRoutes(t *testing.T) {
	e := newDesignEnv(t)
	ctx := context.Background()
	e.client.User.UpdateOneID(e.ownerA).SetDisplayName("Alice Chen").ExecX(ctx)
	rid := e.toAwaiting()
	e.mustCode(e.approve(e.ownerA, rid, shaHead, e.etag(rid)), 200, "approve")

	ev := e.mustCode(e.human(e.editorA, "GET", e.adminPath(e.shopA, "/requests/"+rid+"/events"), nil), 200, "events")
	e.val.AssertResponse(t, "/api/v1/admin/shops/{shopID}/design/requests/{requestID}/events", "GET", 200, []byte(ev.Raw))
	got := map[string]string{}
	for _, x := range ev.Body["events"].([]any) {
		m := x.(map[string]any)
		n, _ := m["actor_name"].(string)
		got[m["kind"].(string)] = n
	}
	if got["approved"] != "Alice Chen" || got["head_updated"] != "Design service" {
		t.Fatalf("actor names: %v", got)
	}
	for _, leak := range []string{"owner-a@t.dev", "editor-a@t.dev", "root@test.dev"} {
		if strings.Contains(ev.Raw, leak) {
			t.Fatalf("events leaked %q: %s", leak, ev.Raw)
		}
	}
	// Machine routes: no names, whatever the credential.
	sr := e.mustCode(e.machine(e.keyA, "GET", e.svcPath(e.shopA, "/requests/"+rid), nil), 200, "service get")
	for _, k := range []string{"proposed_by_name", "approved_by_name", "rejected_by_name", "is_current", "revert_target_tag"} {
		if _, has := sr.Body[k]; has {
			t.Fatalf("service route returned %s: %s", k, sr.Raw)
		}
	}
	if strings.Contains(sr.Raw, "Alice") {
		t.Fatalf("service route leaked a display name: %s", sr.Raw)
	}
	q := e.mustCode(e.machine(e.keyA, "GET", e.svcPath(e.shopA, "/requests"), nil), 200, "service list")
	if strings.Contains(q.Raw, "Alice") || strings.Contains(q.Raw, "_name") {
		t.Fatalf("service list leaked a name: %s", q.Raw)
	}
}

func TestDesignIsCurrentAndRevertTargetTag(t *testing.T) {
	e := newDesignEnv(t)
	r1, tag1 := e.release(shaHead, e.ownerA)
	r2, tag2 := e.release(shaHead2, e.ownerA)

	list := func() dresp {
		return e.mustCode(e.human(e.editorA, "GET", e.adminPath(e.shopA, "/requests"), nil), 200, "list")
	}
	l := list()
	if rowByID(t, l, r1)["is_current"] != false || rowByID(t, l, r2)["is_current"] != true {
		t.Fatalf("is_current after two releases: %s", l.Raw)
	}
	// is_current agrees with the state endpoint (one source of truth).
	st := e.mustCode(e.human(e.editorA, "GET", e.adminPath(e.shopA, "/state"), nil), 200, "state")
	for _, s := range st.Body["sites"].([]any) {
		if m := s.(map[string]any); m["site"] == "admin" && (m["current_request_id"] != r2 || m["current_tag"] != tag2) {
			t.Fatalf("state: %s", st.Raw)
		}
	}
	d := e.mustCode(e.human(e.editorA, "GET", e.adminPath(e.shopA, "/requests/"+r2), nil), 200, "detail")
	if d.Body["is_current"] != true {
		t.Fatalf("detail is_current: %s", d.Raw)
	}
	// A published request is never answered 304, because is_current can change without its rev.
	if r := e.human(e.editorA, "GET", e.adminPath(e.shopA, "/requests/"+r1), nil, "If-None-Match", e.etag(r1)); r.Code != http.StatusOK || r.Body["is_current"] != false {
		t.Fatalf("a published request is never answered 304: %d %s", r.Code, r.Raw)
	}

	// A revert request names the tag it restores.
	rv := e.mustCode(e.human(e.ownerA, "POST", e.adminPath(e.shopA, "/requests/"+r1+"/revert"), nil), 201, "revert")
	e.val.AssertResponse(t, "/api/v1/admin/shops/{shopID}/design/requests/{requestID}/revert", "POST", 201, []byte(rv.Raw))
	if rv.Body["revert_target_tag"] != tag1 || rv.Body["is_current"] != false {
		t.Fatalf("revert request: %s", rv.Raw)
	}
	l = list()
	if rowByID(t, l, id(rv))["revert_target_tag"] != tag1 || rowByID(t, l, r2)["revert_target_tag"] != nil {
		t.Fatalf("list revert_target_tag: %s", l.Raw)
	}
	if rowByID(t, l, id(rv))["proposed_by_name"] != "Team member" {
		t.Fatalf("a member without a display name must read as the neutral label: %s", l.Raw)
	}
}

// Documented in docs/design-history.md: the preview's TTL is only checked when
// the service submits; approval is allowed after it as long as head_sha is
// unchanged and the request is still awaiting approval.
func TestApprovalAfterPreviewExpiryIsAllowedWhileHeadUnchanged(t *testing.T) {
	e := newDesignEnv(t)
	rid := id(e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "calmer"), 201, "create"))
	p := func(path string) string { return e.svcPath(e.shopA, "/requests/"+rid+path) }
	e.mustCode(e.machine(e.keyA, "PUT", p("/head"), map[string]any{"head_sha": shaHead}), 200, "head")
	e.mustCode(e.machine(e.keyA, "POST", p("/guard-runs"), map[string]any{"checked_sha": shaHead, "passed": true}), 200, "guard")
	e.mustCode(e.machine(e.keyA, "PUT", p("/preview"), map[string]any{"origin": "https://p.example", "expires_at": e.now.Add(45 * time.Minute).Format(time.RFC3339)}), 200, "preview")
	e.addScreenshots(rid, shaHead, 1)
	e.mustCode(e.machine(e.keyA, "POST", p("/submit"), nil), 200, "submit")
	etag := e.etag(rid)

	e.advance(2 * time.Hour) // the preview (45 min) and an hour-long window would both be over
	d := e.mustCode(e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+rid), nil), 200, "detail")
	if d.Body["status"] != "awaiting_approval" {
		t.Fatalf("an expired preview must not change the status by itself: %s", d.Raw)
	}
	ap := e.mustCode(e.approve(e.ownerA, rid, shaHead, etag), 200, "approve after the preview expired")
	// The approval window (DESIGN_APPROVAL_TTL) is counted from the approval, not from submit.
	exp, err := time.Parse(time.RFC3339Nano, ap.Body["approval_expires_at"].(string))
	if err != nil || !exp.After(e.now) {
		t.Fatalf("approval window must start at approval: %s", ap.Raw)
	}
}

func b64Manifest(origin string) string {
	extra := ""
	if origin != "" {
		extra = fmt.Sprintf(`,"origin":%q`, origin)
	}
	m := fmt.Sprintf(`{"tool":"design-capture","tool_version":"1","dataset":"sandbox-v1","api_origin_class":"sandbox"%s,"captured_at":"2026-11-27T10:00:00Z","set_version":1}`, extra)
	return base64.StdEncoding.EncodeToString([]byte(m))
}

func TestAdminScreenshotsCheckTheSandboxOriginList(t *testing.T) {
	e, _ := newShotEnv(t)
	rid := e.newAdminRequest()
	img := shotPNG(t, 390, 20, 4)
	up := func(origin string) dresp {
		return e.upload(e.keyA, rid, "before", "owner", 390, "orders", img, "X-Capture-Manifest", b64Manifest(origin))
	}

	// Development/test with no list: the existing manifest rules decide.
	e.mustCode(up(""), 201, "no list, dev")

	// Production-like with no list: admin captures are refused outright.
	e.svc.Cfg.SandboxOriginsRequired = true
	e.wantErr(up("http://127.0.0.1:13001"), 422, "live_data_capture_refused", "production, empty list")

	// With a list: exact match only.
	e.svc.Cfg.SandboxOrigins = []string{"http://127.0.0.1:13001"}
	e.mustCode(e.upload(e.keyA, rid, "before", "owner", 1280, "orders", shotPNG(t, 1280, 20, 9), "X-Capture-Manifest", b64Manifest("HTTP://127.0.0.1:13001")), 201, "listed origin")
	e.wantErr(up("https://merchant.example.com"), 422, "live_data_capture_refused", "unlisted origin")
	e.wantErr(up("http://127.0.0.1:13002"), 422, "live_data_capture_refused", "other port")
	e.wantErr(up(""), 422, "live_data_capture_refused", "missing origin")
	e.wantErr(up("not an origin"), 422, "invalid_screenshot", "malformed origin")

	// What is stored is the normalized origin.
	shots := e.mustCode(e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+rid+"/screenshots"), nil), 200, "screenshots")
	if !strings.Contains(shots.Raw, `"origin":"http://127.0.0.1:13001"`) {
		t.Fatalf("manifest origin not recorded: %s", shots.Raw)
	}

	// The list does not apply to storefront captures.
	e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+rid+"/failed"), map[string]any{"code": "service_error"}), 200, "free the site")
	sf := id(e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "storefront", "bigger photos"), 201, "storefront request"))
	e.mustCode(e.upload(e.keyA, sf, "before", "anon", 390, "home", img, "X-Capture-Manifest", b64Manifest("")), 201, "storefront ignores the list")
}

func TestDesignStateDesignEnabled(t *testing.T) {
	e := newDesignEnv(t)
	ctx := context.Background()
	enabled := func(user, shop int) bool {
		t.Helper()
		r := e.mustCode(e.human(user, "GET", e.adminPath(shop, "/state"), nil), 200, "state")
		e.val.AssertResponse(t, "/api/v1/admin/shops/{shopID}/design/state", "GET", 200, []byte(r.Raw))
		v, ok := r.Body["design_enabled"].(bool)
		if !ok {
			t.Fatalf("design_enabled missing: %s", r.Raw)
		}
		if strings.Contains(r.Raw, "dsk_") || strings.Contains(r.Raw, "key_hash") {
			t.Fatalf("credential detail leaked: %s", r.Raw)
		}
		return v
	}
	// Active credential (issued by the env) -> true; the other shop is independent.
	if !enabled(e.editorA, e.shopA) || !enabled(e.ownerB, e.shopB) {
		t.Fatal("active credential should enable")
	}
	// Revoked only -> false (shop A); shop B unaffected.
	e.client.DesignServiceCredential.Update().Where(designservicecredential.ShopIDEQ(e.shopA)).SetRevokedAt(e.now).ExecX(ctx)
	if enabled(e.editorA, e.shopA) {
		t.Fatal("revoked-only must be false")
	}
	if !enabled(e.ownerB, e.shopB) {
		t.Fatal("shop B must stay enabled")
	}
	// Expired only -> false.
	if _, err := e.sqlDB.Exec(`UPDATE design_service_credentials SET revoked_at = NULL, created_at = now() - interval '3 days', expires_at = now() - interval '1 day' WHERE shop_id = $1`, e.shopA); err != nil {
		t.Fatal(err)
	}
	if enabled(e.editorA, e.shopA) {
		t.Fatal("expired-only must be false")
	}
	// Staff cannot read.
	if r := e.human(e.staffA, "GET", e.adminPath(e.shopA, "/state"), nil); r.Code != 403 {
		t.Fatalf("staff: %d", r.Code)
	}
	// One request (any status) -> true, even with no usable credential.
	e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "x"), 401, "revoked key cannot create")
	if _, err := e.sqlDB.Exec(`UPDATE design_service_credentials SET expires_at = now() + interval '1 day' WHERE shop_id = $1`, e.shopA); err != nil {
		t.Fatal(err)
	}
	e.mustCode(e.create(e.shopA, e.keyA, e.ownerA, "admin", "x"), 201, "create")
	e.client.DesignServiceCredential.Update().Where(designservicecredential.ShopIDEQ(e.shopA)).SetRevokedAt(e.now).ExecX(ctx)
	if !enabled(e.editorA, e.shopA) {
		t.Fatal("a request must enable")
	}
	// A request in shop A does not enable a credential-less shop B.
	e.client.DesignServiceCredential.Update().Where(designservicecredential.ShopIDEQ(e.shopB)).SetRevokedAt(e.now).ExecX(ctx)
	if enabled(e.ownerB, e.shopB) {
		t.Fatal("shop A's request leaked into shop B")
	}
}
