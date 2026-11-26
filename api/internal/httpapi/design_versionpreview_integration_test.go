package httpapi_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/design"
	"ksdevworks/ecommerce/api/internal/ent/designrequest"
	"ksdevworks/ecommerce/api/internal/ent/designrequestevent"
	"ksdevworks/ecommerce/api/internal/ent/designversionpreview"
)

// Integration tests of the version preview (change design-history, design D25).

const (
	vpPreviewPath = "/api/v1/admin/shops/{shopID}/design/requests/{requestID}/version-preview"
	vpGetPath     = "/api/v1/admin/shops/{shopID}/design/version-previews/{previewID}"
)

// publishRelease drives one request to published and returns its id and tag.
func (e *designEnv) publishRelease(head string) (rid, tag string) {
	e.t.Helper()
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

func (e *designEnv) vpCreate(user int, rid string) dresp {
	return e.human(user, "POST", e.adminPath(e.shopA, "/requests/"+rid+"/version-preview"), nil)
}

func (e *designEnv) vpGet(user int, pid string) dresp {
	return e.human(user, "GET", e.adminPath(e.shopA, "/version-previews/"+pid), nil)
}

func (e *designEnv) vpCount() int {
	return e.client.DesignVersionPreview.Query().CountX(context.Background())
}

// vpDeploy is the fake service: claim then ready.
func (e *designEnv) vpDeploy(pid, origin string) dresp {
	e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/version-previews/"+pid+"/claim"), nil), 200, "claim")
	return e.machine(e.keyA, "PUT", e.svcPath(e.shopA, "/version-previews/"+pid+"/ready"),
		map[string]any{"origin": origin, "expires_at": e.now.Add(25 * time.Minute).Format(time.RFC3339)})
}

func TestVersionPreviewFlowNeverTouchesTheHistory(t *testing.T) {
	e := newDesignEnv(t)
	r1, _ := e.publishRelease(shaHead)
	_, _ = e.publishRelease(shaHead2) // r2 is now the current design

	// A request in progress occupies the site's slot: the preview must not care.
	busy := e.toAwaiting()
	revBefore := e.rev(busy)
	eventsBefore := e.client.DesignRequestEvent.Query().CountX(context.Background())
	requestsBefore := e.client.DesignRequest.Query().CountX(context.Background())
	shotsBefore := e.client.DesignScreenshot.Query().CountX(context.Background())

	cr := e.mustCode(e.vpCreate(e.ownerA, r1), 202, "create version preview")
	e.val.AssertResponse(t, vpPreviewPath, "POST", 202, []byte(cr.Raw))
	pid := id(cr)
	if cr.Body["status"] != "queued" || cr.Body["read_only"] != true {
		t.Fatalf("descriptor: %s", cr.Raw)
	}
	if _, has := cr.Body["origin"]; has {
		t.Fatalf("origin before ready: %s", cr.Raw)
	}

	// The service finds it, claims it; a path in the origin is refused (422) and
	// the row stays claimed; a clean origin makes it ready.
	q := e.mustCode(e.machine(e.keyA, "GET", e.svcPath(e.shopA, "/version-previews"), nil), 200, "queue")
	e.val.AssertResponse(t, "/api/v1/design-service/shops/{shopID}/version-previews", "GET", 200, []byte(q.Raw))
	if list := q.Body["version_previews"].([]any); len(list) != 1 || list[0].(map[string]any)["id"] != pid || list[0].(map[string]any)["read_only"] != true {
		t.Fatalf("queue: %s", q.Raw)
	}
	cl := e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/version-previews/"+pid+"/claim"), nil), 200, "claim")
	e.val.AssertResponse(t, "/api/v1/design-service/shops/{shopID}/version-previews/{previewID}/claim", "POST", 200, []byte(cl.Raw))
	secret := "https://vp-secret-host.preview.example"
	exp := e.now.Add(25 * time.Minute).Format(time.RFC3339)
	bad := e.machine(e.keyA, "PUT", e.svcPath(e.shopA, "/version-previews/"+pid+"/ready"), map[string]any{"origin": secret + "/admin?x=1", "expires_at": exp})
	e.wantErr(bad, 422, "validation_failed", "origin with a path")
	if got := e.vpGet(e.ownerA, pid); got.Body["status"] != "claimed" {
		t.Fatalf("a bad origin must leave the row claimed: %s", got.Raw)
	}
	e.wantErr(e.machine(e.keyA, "PUT", e.svcPath(e.shopA, "/version-previews/"+pid+"/ready"), map[string]any{"origin": secret, "expires_at": e.now.Add(3 * time.Hour).Format(time.RFC3339)}),
		422, "validation_failed", "expiry beyond the TTL")
	rd := e.mustCode(e.machine(e.keyA, "PUT", e.svcPath(e.shopA, "/version-previews/"+pid+"/ready"), map[string]any{"origin": secret, "expires_at": exp}), 200, "ready")
	e.val.AssertResponse(t, "/api/v1/design-service/shops/{shopID}/version-previews/{previewID}/ready", "PUT", 200, []byte(rd.Raw))
	if strings.Contains(rd.Raw, "vp-secret-host") {
		t.Fatalf("the service view leaks the origin: %s", rd.Raw)
	}
	// A retried report is answered, not rejected.
	e.mustCode(e.machine(e.keyA, "PUT", e.svcPath(e.shopA, "/version-previews/"+pid+"/ready"), map[string]any{"origin": secret, "expires_at": exp}), 200, "ready replay")

	// Only the initiator gets the origin; another editor and a super admin do not.
	own := e.mustCode(e.vpGet(e.ownerA, pid), 200, "owner get")
	e.val.AssertResponse(t, vpGetPath, "GET", 200, []byte(own.Raw))
	if own.Body["origin"] != secret || own.Body["status"] != "ready" || own.Body["read_only"] != true {
		t.Fatalf("initiator view: %s", own.Raw)
	}
	for name, user := range map[string]int{"editor": e.editorA, "platform": e.platform} {
		got := e.mustCode(e.vpGet(user, pid), 200, name+" get")
		if _, has := got.Body["origin"]; has || strings.Contains(got.Raw, "vp-secret-host") {
			t.Fatalf("%s sees the origin: %s", name, got.Raw)
		}
	}

	// Nothing of the history moved: slot, events, requests, screenshots.
	if e.rev(busy) != revBefore {
		t.Fatal("the request's rev moved")
	}
	if got := e.human(e.ownerA, "GET", e.adminPath(e.shopA, "/requests/"+busy), nil); got.Body["status"] != "awaiting_approval" || got.Body["preview_read_only"] != true {
		t.Fatalf("the busy request changed or lacks the read-only flag: %s", got.Raw)
	}
	if n := e.client.DesignRequestEvent.Query().CountX(context.Background()); n != eventsBefore {
		t.Fatalf("events %d -> %d", eventsBefore, n)
	}
	if n := e.client.DesignRequest.Query().CountX(context.Background()); n != requestsBefore {
		t.Fatalf("requests %d -> %d", requestsBefore, n)
	}
	if n := e.client.DesignScreenshot.Query().CountX(context.Background()); n != shotsBefore {
		t.Fatalf("screenshots %d -> %d", shotsBefore, n)
	}
	if n := e.client.DesignRequestEvent.Query().Where(designrequestevent.RequestIDEQ(mustParse(r1)), designrequestevent.KindEQ("revert_requested")).CountX(context.Background()); n != 0 {
		t.Fatal("a version preview wrote a revert event")
	}
	// The request side of the machine family still shows the read-only flag.
	sv := e.mustCode(e.machine(e.keyA, "GET", e.svcPath(e.shopA, "/requests/"+busy), nil), 200, "service request")
	if sv.Body["preview_read_only"] != true {
		t.Fatalf("service request lacks the flag: %s", sv.Raw)
	}

	// A newer preview of the same site supersedes this one; the service learns
	// which deployment to tear down.
	cr2 := e.mustCode(e.vpCreate(e.ownerA, r1), 202, "second preview")
	if got := e.vpGet(e.ownerA, pid); got.Body["status"] != "superseded" {
		t.Fatalf("first preview not superseded: %s", got.Raw)
	}
	if _, has := e.vpGet(e.ownerA, pid).Body["origin"]; has {
		t.Fatal("a superseded preview still shows its origin")
	}
	sup := e.mustCode(e.machine(e.keyA, "GET", e.svcPath(e.shopA, "/version-previews?status=superseded&updated_since="+e.now.Add(-time.Hour).UTC().Format(time.RFC3339)), nil), 200, "superseded list")
	if list := sup.Body["version_previews"].([]any); len(list) != 1 || list[0].(map[string]any)["id"] != pid {
		t.Fatalf("superseded list: %s", sup.Raw)
	}
	live := e.client.DesignVersionPreview.Query().Where(designversionpreview.StatusIn("queued", "claimed", "ready")).CountX(context.Background())
	if live != 1 || id(cr2) == pid {
		t.Fatalf("live previews: %d", live)
	}
	// A preview does not make the request side any harder: the busy request is
	// still approvable (nothing about it changed).
	e.mustCode(e.approve(e.ownerA, busy, shaHead, e.etag(busy)), 200, "approve the busy request")

	// The origin never reaches the logs.
	if strings.Contains(e.buf.String(), "vp-secret-host") {
		t.Fatal("the logs contain the preview origin")
	}
	if !strings.Contains(e.buf.String(), "design version preview") {
		t.Fatal("no usage log line")
	}
}

func mustParse(s string) uuid.UUID { return uuid.MustParse(s) }

func TestVersionPreviewCompatibilityRefusalsCreateNothing(t *testing.T) {
	e := newDesignEnv(t)
	r1, _ := e.publishRelease(shaHead)
	r2, _ := e.publishRelease(shaHead2)
	// The target is the current design.
	e.wantErr(e.vpCreate(e.ownerA, r2), 409, "already_current", "current design")
	// Below the core floor: the message says why.
	if _, err := e.sqlDB.Exec(`UPDATE design_site_state SET design_floor_version = '0.5.0' WHERE shop_id = $1`, e.shopA); err != nil {
		t.Fatal(err)
	}
	r := e.vpCreate(e.ownerA, r1)
	e.wantErr(r, 409, "revert_below_core_floor", "below floor")
	if !strings.Contains(r.Raw, "core update") {
		t.Fatalf("no reason in the refusal: %s", r.Raw)
	}
	// Not a published design.
	if _, err := e.sqlDB.Exec(`UPDATE design_site_state SET design_floor_version = '0.2.0' WHERE shop_id = $1`, e.shopA); err != nil {
		t.Fatal(err)
	}
	rej := e.toAwaiting()
	e.mustCode(e.human(e.ownerA, "POST", e.adminPath(e.shopA, "/requests/"+rej+"/reject"), map[string]any{"reason": "no"}), 200, "reject")
	e.wantErr(e.vpCreate(e.ownerA, rej), 409, "invalid_state", "rejected target")

	if n := e.vpCount(); n != 0 {
		t.Fatalf("%d rows after refusals", n)
	}
	// Refusals use no cap: the owner can still make the full hourly allowance.
	for i := 0; i < 6; i++ {
		e.mustCode(e.vpCreate(e.ownerA, r1), 202, "after refusals")
	}
	var vpEvents int
	if err := e.sqlDB.QueryRow(`SELECT count(*) FROM design_request_events WHERE kind NOT IN ('created','claimed','head_updated','guard_run','preview_registered','screenshots_complete','submitted','changes_requested','approved','rejected','cancelled','guard_rechecked','published','failed','revert_requested','reverted_mark','expired')`).Scan(&vpEvents); err != nil || vpEvents != 0 {
		t.Fatalf("unexpected event kinds: %d %v", vpEvents, err)
	}
	// Cross-shop and unknown targets.
	e.wantErr(e.human(e.ownerB, "POST", e.adminPath(e.shopB, "/requests/"+r1+"/version-preview"), nil), 404, "not_found", "another shop's request")
}

func TestVersionPreviewPermissionsAndMachineFamily(t *testing.T) {
	e := newDesignEnv(t)
	r1, _ := e.publishRelease(shaHead)
	_, _ = e.publishRelease(shaHead2)

	e.mustCode(e.vpCreate(e.editorA, r1), 202, "editor holds design.view")
	pid := id(e.mustCode(e.vpCreate(e.ownerA, r1), 202, "owner"))
	e.mustCode(e.vpCreate(e.platform, r1), 202, "super admin")
	for name, user := range map[string]int{"staff": e.staffA, "outsider": e.outsiderA} {
		if got := e.vpCreate(user, r1); got.Code != 403 {
			t.Fatalf("%s: %d %s", name, got.Code, got.Raw)
		}
		if got := e.vpGet(user, pid); got.Code != 403 {
			t.Fatalf("%s read: %d %s", name, got.Code, got.Raw)
		}
	}
	// Another shop's owner: 403 on our shop, 404 on theirs.
	if got := e.human(e.ownerB, "GET", e.adminPath(e.shopA, "/version-previews/"+pid), nil); got.Code != 403 {
		t.Fatalf("owner B on shop A: %d", got.Code)
	}
	e.wantErr(e.human(e.ownerB, "GET", e.adminPath(e.shopB, "/version-previews/"+pid), nil), 404, "not_found", "A's preview under B")
	// The service cannot create one (no such route) and cannot reach the human routes.
	if got := e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/requests/"+r1+"/version-preview"), nil); got.Code != 404 && got.Code != 405 {
		t.Fatalf("service create: %d %s", got.Code, got.Raw)
	}
	if got := e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/version-previews"), map[string]any{"target_request_id": r1}); got.Code != 404 && got.Code != 405 {
		t.Fatalf("service create via the collection: %d", got.Code)
	}
	if got := e.machine(e.keyA, "POST", e.adminPath(e.shopA, "/requests/"+r1+"/version-preview"), nil); got.Code != 401 {
		t.Fatalf("service key on the human route: %d", got.Code)
	}
	// Another shop's credential sees and claims nothing of ours.
	e.wantErr(e.machine(e.keyB, "POST", e.svcPath(e.shopB, "/version-previews/"+pid+"/claim"), nil), 404, "not_found", "B claims A's preview")
	if q := e.machine(e.keyB, "GET", e.svcPath(e.shopB, "/version-previews"), nil); len(q.Body["version_previews"].([]any)) != 0 {
		t.Fatalf("B's queue: %s", q.Raw)
	}
	// Unknown ids and bad status filters.
	e.wantErr(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/version-previews/00000000-0000-0000-0000-000000000000/claim"), nil), 404, "not_found", "unknown")
	e.wantErr(e.machine(e.keyA, "GET", e.svcPath(e.shopA, "/version-previews?status=nope"), nil), 422, "validation_failed", "bad status")
}

func TestVersionPreviewGuardFailureIsVisibleToTheInitiator(t *testing.T) {
	e := newDesignEnv(t)
	r1, _ := e.publishRelease(shaHead)
	_, _ = e.publishRelease(shaHead2)
	pid := id(e.mustCode(e.vpCreate(e.ownerA, r1), 202, "create"))
	e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/version-previews/"+pid+"/claim"), nil), 200, "claim")
	// Another credential of the same shop cannot fail a lease it does not hold.
	other, err := e.creds.Issue(context.Background(), design.IssueInput{ShopID: e.shopA, Name: "svc-a2", CreatedByID: e.platform})
	if err != nil {
		t.Fatal(err)
	}
	e.wantErr(e.machine(other.Key, "POST", e.svcPath(e.shopA, "/version-previews/"+pid+"/failed"), map[string]any{"code": "guard_failed"}), 409, "claim_conflict", "foreign failure")
	e.wantErr(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/version-previews/"+pid+"/failed"), map[string]any{"code": "Bad Code"}), 422, "validation_failed", "bad code")
	f := e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/version-previews/"+pid+"/failed"),
		map[string]any{"code": "guard_failed", "detail": "the current core refuses this layer", "rule_ids": []string{"no-core-import", "fixed-warning"}}), 200, "failed")
	e.val.AssertResponse(t, "/api/v1/design-service/shops/{shopID}/version-previews/{previewID}/failed", "POST", 200, []byte(f.Raw))
	got := e.mustCode(e.vpGet(e.ownerA, pid), 200, "read")
	e.val.AssertResponse(t, vpGetPath, "GET", 200, []byte(got.Raw))
	rules, _ := got.Body["failure_rule_ids"].([]any)
	if got.Body["status"] != "failed" || got.Body["failure_code"] != "guard_failed" || len(rules) != 2 || rules[0] != "no-core-import" {
		t.Fatalf("the initiator cannot see why: %s", got.Raw)
	}
	if _, has := got.Body["origin"]; has {
		t.Fatal("a failed preview shows an origin")
	}
	// A retried failure report is answered; a failed row is final.
	e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/version-previews/"+pid+"/failed"), map[string]any{"code": "guard_failed"}), 200, "failed replay")
	e.wantErr(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/version-previews/"+pid+"/claim"), nil), 409, "invalid_state", "claim a failed one")
	// And it frees the site's slot: a new preview is accepted without superseding anything.
	e.mustCode(e.vpCreate(e.ownerA, r1), 202, "retry")
}

func TestVersionPreviewRateLimits(t *testing.T) {
	e := newDesignEnv(t)
	r1, _ := e.publishRelease(shaHead)
	_, _ = e.publishRelease(shaHead2)

	// Per user per hour: the 7th is 429 with Retry-After and creates no row.
	for i := 0; i < 6; i++ {
		e.mustCode(e.vpCreate(e.ownerA, r1), 202, "within the hourly cap")
		e.advance(time.Minute)
	}
	n := e.vpCount()
	over := e.vpCreate(e.ownerA, r1)
	e.wantErr(over, 429, "hourly_cap_reached", "7th in an hour")
	if over.Header.Get("Retry-After") == "" || e.vpCount() != n {
		t.Fatalf("429 without Retry-After or with a row: %q", over.Header.Get("Retry-After"))
	}
	// Another member is not affected by the owner's hourly count.
	e.mustCode(e.vpCreate(e.editorA, r1), 202, "editor has their own allowance")
	// The window slides: after an hour the owner may preview again.
	e.advance(time.Hour)
	e.mustCode(e.vpCreate(e.ownerA, r1), 202, "after the hour")

	// Per shop per UTC day, counted from the table.
	e.svc.Cfg.VersionPreviewDailyCap = e.vpCountToday() + 1
	e.mustCode(e.vpCreate(e.editorA, r1), 202, "last of the day")
	day := e.vpCreate(e.ownerA, r1)
	e.wantErr(day, 429, "daily_cap_reached", "shop daily cap")
	if day.Header.Get("Retry-After") == "" {
		t.Fatal("daily cap without Retry-After")
	}
}

func (e *designEnv) vpCountToday() int {
	return e.client.DesignVersionPreview.Query().Where(designversionpreview.CreatedAtGTE(design.DayStart(e.now))).CountX(context.Background())
}

func TestVersionPreviewExpiryAndLeaseSweep(t *testing.T) {
	e := newDesignEnv(t)
	r1, _ := e.publishRelease(shaHead)
	_, _ = e.publishRelease(shaHead2)
	origin := "https://vp-exp.preview.example"

	// A ready preview past its expiry reads as expired at once and the sweep
	// records it; the sweep is idempotent and counts without -apply.
	pid := id(e.mustCode(e.vpCreate(e.ownerA, r1), 202, "create"))
	e.mustCode(e.vpDeploy(pid, origin), 200, "ready")
	e.advance(26 * time.Minute)
	got := e.mustCode(e.vpGet(e.ownerA, pid), 200, "read after expiry")
	if got.Body["status"] != "expired" {
		t.Fatalf("not expired: %s", got.Raw)
	}
	if _, has := got.Body["origin"]; has {
		t.Fatal("an expired preview still shows its origin")
	}
	dry, err := e.svc.SweepVersionPreviews(context.Background(), 0, false)
	if err != nil || dry.Expired != 1 || dry.LeasesLost != 0 {
		t.Fatalf("dry sweep: %+v %v", dry, err)
	}
	if st := e.client.DesignVersionPreview.GetX(context.Background(), mustParse(pid)).Status; st != "ready" {
		t.Fatalf("the dry run wrote: %s", st)
	}
	rep, err := e.svc.SweepVersionPreviews(context.Background(), 0, true)
	if err != nil || rep.Expired != 1 {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	if st := e.client.DesignVersionPreview.GetX(context.Background(), mustParse(pid)).Status; st != "expired" {
		t.Fatalf("not recorded: %s", st)
	}
	again, _ := e.svc.SweepVersionPreviews(context.Background(), 0, true)
	if again.Expired != 0 || again.LeasesLost != 0 {
		t.Fatalf("not idempotent: %+v", again)
	}
	// The service is told to take the deployment down.
	exp := e.mustCode(e.machine(e.keyA, "GET", e.svcPath(e.shopA, "/version-previews?status=expired"), nil), 200, "expired list")
	if list := exp.Body["version_previews"].([]any); len(list) != 1 {
		t.Fatalf("expired list: %s", exp.Raw)
	}
	// An expired preview cannot be claimed or made ready.
	e.wantErr(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/version-previews/"+pid+"/claim"), nil), 409, "invalid_state", "claim expired")

	// A claim whose lease ran out becomes failed(service_lost).
	pid2 := id(e.mustCode(e.vpCreate(e.ownerA, r1), 202, "second"))
	e.mustCode(e.machine(e.keyA, "POST", e.svcPath(e.shopA, "/version-previews/"+pid2+"/claim"), nil), 200, "claim")
	e.advance(6 * time.Minute)
	rep, err = e.svc.SweepVersionPreviews(context.Background(), e.shopA, true)
	if err != nil || rep.LeasesLost != 1 || rep.Expired != 0 {
		t.Fatalf("lease sweep: %+v %v", rep, err)
	}
	g2 := e.mustCode(e.vpGet(e.ownerA, pid2), 200, "read lost")
	if g2.Body["status"] != "failed" || g2.Body["failure_code"] != "service_lost" {
		t.Fatalf("lost lease: %s", g2.Raw)
	}
	// A queued preview nobody picked up expires too, and the sweep never touches
	// the history tables.
	pid3 := id(e.mustCode(e.vpCreate(e.ownerA, r1), 202, "third"))
	e.advance(31 * time.Minute)
	if _, err := e.svc.Sweep(context.Background(), 0, true); err != nil {
		t.Fatal(err)
	}
	if st := e.client.DesignVersionPreview.GetX(context.Background(), mustParse(pid3)).Status; st != "expired" {
		t.Fatalf("queued not expired by Sweep: %s", st)
	}
	if n := e.client.DesignRequestEvent.Query().Where(designrequestevent.KindEQ("failed")).CountX(context.Background()); n != 0 {
		t.Fatalf("the sweep failed %d requests", n)
	}
	// The request slot is still free of preview influence.
	if n := e.client.DesignRequest.Query().Where(designrequest.StatusIn(design.ActiveStatuses...)).CountX(context.Background()); n != 0 {
		t.Fatalf("%d active requests", n)
	}
}

func TestVersionPreviewOneLivePerSiteUnderConcurrency(t *testing.T) {
	e := newDesignEnv(t)
	r1, _ := e.publishRelease(shaHead)
	_, _ = e.publishRelease(shaHead2)
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = e.vpCreate(e.ownerA, r1).Code
		}()
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == 202 {
			ok++
		} else if c != 409 && c != 429 {
			t.Fatalf("unexpected status %d in %v", c, codes)
		}
	}
	live := e.client.DesignVersionPreview.Query().Where(designversionpreview.StatusIn("queued", "claimed", "ready")).CountX(context.Background())
	if live != 1 || ok < 1 {
		t.Fatalf("live=%d ok=%d codes=%v", live, ok, codes)
	}
	// The database refuses a second live row even if the code were wrong.
	if _, err := e.sqlDB.Exec(`INSERT INTO design_version_previews (id, shop_id, site, target_request_id, target_tag, requested_by_user_id, status, failure_rule_ids, created_at, updated_at, expires_at)
		VALUES (gen_random_uuid(), $1, 'admin', gen_random_uuid(), 't', 1, 'queued', '[]', now(), now(), now() + interval '1 hour')`, e.shopA); err == nil {
		t.Fatal("the database accepted a second live preview for the site")
	}
}
