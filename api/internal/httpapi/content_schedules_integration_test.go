package httpapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/content"
	"ksdevworks/ecommerce/api/internal/contentstore"
	"ksdevworks/ecommerce/api/internal/ent"
	entaudit "ksdevworks/ecommerce/api/internal/ent/contentauditlog"
	entdoc "ksdevworks/ecommerce/api/internal/ent/contentdocument"
	entrev "ksdevworks/ecommerce/api/internal/ent/contentrevision"
	entsched "ksdevworks/ecommerce/api/internal/ent/contentschedule"
	entshopuser "ksdevworks/ecommerce/api/internal/ent/shopuser"
)

// Integration tests of scheduled publishing (design D11) on a fake clock.

func (e *schedEnv) schedPath(key string) string { return e.docPath("/" + key + "/schedule") }

type schedEnv struct{ *schemaEnv }

func newSchedEnv(t *testing.T) *schedEnv {
	t.Helper()
	e := &schedEnv{docEnv(t)}
	// banner has a reviewed, valid draft at rev 2.
	if rec := e.saveDoc(t, e.ownerA, "banner", `"1"`, note("hello")); rec.Code != 200 {
		t.Fatalf("save banner: %d %s", rec.Code, rec.Body.String())
	}
	return e
}

func (e *schedEnv) put(t *testing.T, user int, key, rev string, body any) (int, map[string]any, string) {
	t.Helper()
	h := map[string]string{}
	if rev != "" {
		h["If-Match"] = rev
	}
	rec := e.doH(t, user, "PUT", e.schedPath(key), body, h)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m, rec.Body.String()
}

func at(t time.Time) map[string]any {
	return map[string]any{"publish_at": t.UTC().Format(time.RFC3339)}
}

func (e *schedEnv) run(t *testing.T) content.RunReport {
	t.Helper()
	rep, err := e.sched.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return rep
}

func (e *schedEnv) docStatus(t *testing.T, key string) (status int16, attention bool, pubRev int64) {
	t.Helper()
	d := e.client.ContentDocument.Query().Where(entdoc.ShopIDEQ(e.shopA), entdoc.KeyEQ(key)).OnlyX(context.Background())
	return d.Status, d.NeedsAttention, d.PublishedRev
}

func (e *schedEnv) schedule(t *testing.T, key string) *ent.ContentSchedule {
	t.Helper()
	r := e.client.ContentSchedule.Query().Where(entsched.ShopIDEQ(e.shopA), entsched.DocKeyEQ(key)).
		Order(ent.Desc(entsched.FieldID)).FirstX(context.Background())
	return r
}

// pubRevs counts the publish revisions of one document of shop A.
func (e *schedEnv) pubRevs(key string) int {
	d := e.client.ContentDocument.Query().Where(entdoc.ShopIDEQ(e.shopA), entdoc.KeyEQ(key)).OnlyX(context.Background())
	return e.client.ContentRevision.Query().Where(entrev.ShopIDEQ(e.shopA), entrev.DocumentIDEQ(d.ID), entrev.KindEQ(contentstore.KindPublish)).CountX(context.Background())
}

func countAudit(e *schemaEnv, action string) int {
	return e.client.ContentAuditLog.Query().Where(entaudit.ActionEQ(action)).CountX(context.Background())
}

func TestSchedulePermissionsAndInputRules(t *testing.T) {
	e := newSchedEnv(t)
	when := e.clockNow.Add(30 * time.Minute)
	for _, tc := range []struct {
		name string
		user int
		want int
	}{
		{"editor (no content.publish)", e.editorA, 403},
		{"staff", e.staffA, 403},
		{"page.* only", e.pageOnly, 403},
		{"other shop owner", e.ownerB, 403},
	} {
		if code, _, raw := e.put(t, tc.user, "banner", `"2"`, at(when)); code != tc.want {
			t.Errorf("%s PUT: %d %s", tc.name, code, raw)
		}
		if rec := e.doH(t, tc.user, "DELETE", e.schedPath("banner"), nil, nil); rec.Code != 403 {
			t.Errorf("%s DELETE: %d", tc.name, rec.Code)
		}
	}
	// Reading needs content.view: the editor has it, staff does not.
	if rec := e.doH(t, e.editorA, "GET", e.path(e.shopA, "/content/schedules"), nil, nil); rec.Code != 200 {
		t.Errorf("editor list: %d", rec.Code)
	}
	if rec := e.doH(t, e.staffA, "GET", e.path(e.shopA, "/content/schedules"), nil, nil); rec.Code != 403 {
		t.Errorf("staff list: %d", rec.Code)
	}
	if e.client.ContentSchedule.Query().CountX(context.Background()) != 0 {
		t.Fatal("a denied caller created a schedule")
	}

	// Input rules.
	if code, _, _ := e.put(t, e.ownerA, "banner", "", at(when)); code != 428 {
		t.Errorf("no If-Match: %d", code)
	}
	if code, m, _ := e.put(t, e.ownerA, "banner", `"1"`, at(when)); code != 412 || m["error"] == nil {
		t.Errorf("stale rev: %d %v", code, m)
	}
	for name, body := range map[string]any{
		"missing":          map[string]any{},
		"no offset":        map[string]any{"publish_at": "2099-01-01T10:00:00"},
		"garbage":          map[string]any{"publish_at": "tomorrow"},
		"in the past":      at(e.clockNow.Add(-time.Hour)),
		"under min lead":   at(e.clockNow.Add(60 * time.Second)),
		"beyond 90 days":   at(e.clockNow.Add(91 * 24 * time.Hour)),
		"date only":        map[string]any{"publish_at": "2099-01-01"},
		"unix time number": map[string]any{"publish_at": 4102444800},
	} {
		code, _, raw := e.put(t, e.ownerA, "banner", `"2"`, body)
		if code != 422 && code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	if code, _, _ := e.put(t, e.ownerA, "nope", `"1"`, at(when)); code != 404 {
		t.Errorf("unknown document: %d", code)
	}
	// An offset is converted to UTC; the time is rounded UP to the minute.
	loc := time.FixedZone("UTC+8", 8*3600)
	sec := when.Truncate(time.Minute).Add(20 * time.Second).In(loc)
	code, m, raw := e.put(t, e.ownerA, "banner", `"2"`, map[string]any{"publish_at": sec.Format(time.RFC3339)})
	if code != 201 {
		t.Fatalf("create: %d %s", code, raw)
	}
	stored, _ := time.Parse(time.RFC3339, m["publish_at"].(string))
	if !stored.Equal(when.Truncate(time.Minute).Add(time.Minute)) || stored.Location() != time.UTC {
		t.Fatalf("stored %s", m["publish_at"])
	}
	if m["status"] != "pending" || m["draft_rev"].(float64) != 2 || m["key"] != "banner" {
		t.Fatalf("shape: %v", m)
	}
	// Invalid drafts cannot be scheduled (they could not be published either).
	e.client.ContentDocument.Update().Where(entdoc.ShopIDEQ(e.shopA), entdoc.KeyEQ("global")).
		SetDraftJSON(json.RawMessage(`{"name":"` + strings.Repeat("x", 80) + `"}`)).AddDraftRev(1).ExecX(context.Background())
	if code, _, _ := e.put(t, e.ownerA, "global", `"2"`, at(when)); code != 422 {
		t.Errorf("invalid draft: %d", code)
	}
}

func TestScheduleRescheduleCancelAndListing(t *testing.T) {
	e := newSchedEnv(t)
	t1 := e.clockNow.Add(30 * time.Minute)
	code, m1, raw := e.put(t, e.ownerA, "banner", `"2"`, at(t1))
	if code != 201 {
		t.Fatalf("%d %s", code, raw)
	}
	// Reschedule: same row, new time; 200 not 201.
	t2 := e.clockNow.Add(3 * time.Hour)
	code, m2, raw := e.put(t, e.ownerA, "banner", `"2"`, at(t2))
	if code != 200 || m2["id"] != m1["id"] {
		t.Fatalf("reschedule: %d %s", code, raw)
	}
	if e.client.ContentSchedule.Query().CountX(context.Background()) != 1 {
		t.Fatal("rescheduling must not create a second pending schedule")
	}
	// Document list and detail carry the time; the schedule list shows it.
	rec := e.doH(t, e.ownerA, "GET", e.docPath(""), nil, nil)
	if !strings.Contains(rec.Body.String(), `"scheduled_publish_at":"`+content.RoundUpToMinute(t2).Format("2006-01-02T15:04")) {
		t.Fatalf("document list lacks scheduled_publish_at: %s", rec.Body.String())
	}
	detail := jsonOf(t, e.doH(t, e.ownerA, "GET", e.docPath("/banner"), nil, nil))
	if detail["scheduled_publish_at"] == nil {
		t.Fatal("detail lacks scheduled_publish_at")
	}
	up := jsonOf(t, e.doH(t, e.ownerA, "GET", e.path(e.shopA, "/content/schedules"), nil, nil))
	if l := up["schedules"].([]any); len(l) != 1 {
		t.Fatalf("upcoming: %v", up)
	}
	// Cancel.
	if rec := e.doH(t, e.ownerA, "DELETE", e.schedPath("banner"), nil, nil); rec.Code != 204 {
		t.Fatalf("cancel: %d", rec.Code)
	}
	if rec := e.doH(t, e.ownerA, "DELETE", e.schedPath("banner"), nil, nil); rec.Code != 404 {
		t.Fatalf("second cancel: %d", rec.Code)
	}
	got := jsonOf(t, e.doH(t, e.ownerA, "GET", e.schedPath("banner"), nil, nil))
	if got["status"] != "cancelled" || got["result"] != "cancelled" {
		t.Fatalf("latest schedule: %v", got)
	}
	if up := jsonOf(t, e.doH(t, e.ownerA, "GET", e.path(e.shopA, "/content/schedules"), nil, nil)); len(up["schedules"].([]any)) != 0 {
		t.Fatalf("cancelled schedule still upcoming: %v", up)
	}
	recent := jsonOf(t, e.doH(t, e.ownerA, "GET", e.path(e.shopA, "/content/schedules?scope=recent"), nil, nil))
	if len(recent["schedules"].([]any)) != 1 {
		t.Fatalf("recent: %v", recent)
	}
	if rec := e.doH(t, e.ownerA, "GET", e.path(e.shopA, "/content/schedules?scope=bogus"), nil, nil); rec.Code != 422 {
		t.Fatalf("bad scope: %d", rec.Code)
	}
	// Nothing was published by cancelling or by running.
	e.clockNow = t2.Add(time.Hour)
	if rep := e.run(t); rep.Due != 0 {
		t.Fatalf("%+v", rep)
	}
	if st, _, _ := e.docStatus(t, "banner"); st != 0 {
		t.Fatal("cancelled schedule published")
	}
	// Audit: set (x2: create + reschedule) and cancel are recorded.
	if countAudit(e.schemaEnv, contentstore.AuditScheduleSet) != 2 || countAudit(e.schemaEnv, contentstore.AuditScheduleCancel) != 1 {
		t.Fatal("audit rows missing")
	}
}

func TestDueScheduleIsPublishedThroughTheSamePathAsAManualPublish(t *testing.T) {
	e := newSchedEnv(t)
	// A publish hook so the shared path's hook trigger is visible.
	e.doJSON(t, e.ownerA, "PUT", hookPath(e.schemaEnv, ""), map[string]any{"url": cfHookURL})
	when := e.clockNow.Add(10 * time.Minute)
	if code, _, raw := e.put(t, e.ownerA, "banner", `"2"`, at(when)); code != 201 {
		t.Fatalf("%d %s", code, raw)
	}
	// Not yet due.
	e.clockNow = when.Add(-time.Minute)
	if rep := e.run(t); rep.Due != 0 {
		t.Fatalf("early: %+v", rep)
	}
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/banner", nil); rec.Code != 404 {
		t.Fatalf("published before its time: %d", rec.Code)
	}
	// Due.
	e.clockNow = content.RoundUpToMinute(when).Add(30 * time.Second)
	rep := e.run(t)
	if rep.Due != 1 || rep.Published != 1 {
		t.Fatalf("%+v", rep)
	}
	rec := e.pub(t, "schema-a", "/api/v1/shop/content/banner", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"title":"hello"`) {
		t.Fatalf("public read after the run: %d %s", rec.Code, rec.Body.String())
	}
	// Same artifacts as a manual publish: a revision, the publish audit row
	// (marked scheduled), the schedule row, and the hook trigger.
	if n := e.pubRevs("banner"); n != 1 {
		t.Fatalf("publish revisions: %d", n)
	}
	var pub *ent.ContentAuditLog
	for _, a := range e.client.ContentAuditLog.Query().Where(entaudit.ActionEQ("doc.publish")).AllX(context.Background()) {
		pub = a
	}
	var pd map[string]any
	if pub != nil {
		_ = json.Unmarshal(pub.Detail, &pd)
	}
	if pub == nil || pd["scheduled"] != true || pub.ActorUserID == nil || *pub.ActorUserID != e.ownerA {
		t.Fatalf("publish audit row: %+v", pub)
	}
	s := e.schedule(t, "banner")
	if s.Status != "done" || s.Result != "published" || s.FinishedAt == nil {
		t.Fatalf("schedule row: %+v", s)
	}
	e.hookN.DrainInline(context.Background())
	if h := e.client.ContentPublishHook.Query().OnlyX(context.Background()); h.PendingSince == nil || h.PendingEvent != "content.published" {
		t.Fatalf("hook not triggered by the scheduled publish: %+v", h)
	}
	// Running again changes nothing.
	if rep := e.run(t); rep.Due != 0 {
		t.Fatalf("second run: %+v", rep)
	}
	if n := e.pubRevs("banner"); n != 1 {
		t.Fatalf("second run republished: %d", n)
	}
}

func TestEditingAfterSchedulingInvalidatesTheSchedule(t *testing.T) {
	e := newSchedEnv(t)
	when := e.clockNow.Add(10 * time.Minute)
	e.put(t, e.ownerA, "banner", `"2"`, at(when))
	// Any draft save (here an editor's) supersedes it at once, visibly.
	if rec := e.saveDoc(t, e.editorA, "banner", `"2"`, note("changed after scheduling")); rec.Code != 200 {
		t.Fatalf("save: %d", rec.Code)
	}
	s := e.schedule(t, "banner")
	if s.Status != "superseded" || s.Result != "edited" {
		t.Fatalf("after edit: %+v", s)
	}
	if countAudit(e.schemaEnv, contentstore.AuditScheduleSuperseded) != 1 {
		t.Fatal("no audit row for the superseded schedule")
	}
	e.clockNow = when.Add(time.Minute)
	if rep := e.run(t); rep.Due != 0 || rep.Published != 0 {
		t.Fatalf("%+v", rep)
	}
	if st, _, _ := e.docStatus(t, "banner"); st != 0 {
		t.Fatal("an edited draft went live unreviewed")
	}
	// Restore also counts as an edit.
	e.put(t, e.ownerA, "banner", `"3"`, at(e.clockNow.Add(10*time.Minute)))
	// The guard at fire time is the authority even when the eager path is
	// bypassed (here: a direct SQL edit, as a legacy tool might do).
	e.client.ContentSchedule.Update().Where(entsched.StatusEQ("pending")).
		SetPublishAt(e.clockNow.Add(-time.Minute)).SetDraftRev(1).ExecX(context.Background())
	rep := e.run(t)
	if rep.Superseded < 1 || rep.Published != 0 {
		t.Fatalf("stale draft_rev must never publish: %+v", rep)
	}
}

func TestManualPublishSupersedesAndDeleteCancels(t *testing.T) {
	e := newSchedEnv(t)
	when := e.clockNow.Add(10 * time.Minute)
	e.put(t, e.ownerA, "banner", `"2"`, at(when))
	if rec := e.doH(t, e.ownerA, "POST", e.docPath("/banner/publish"), nil, nil); rec.Code != 200 {
		t.Fatalf("manual publish: %d", rec.Code)
	}
	if s := e.schedule(t, "banner"); s.Status != "superseded" || s.Result != "published_manually" {
		t.Fatalf("%+v", s)
	}
	// Delete: a collection member with a schedule.
	if rec := e.saveDoc(t, e.ownerA, "promo-a", "*", map[string]any{"type_key": "note", "content": map[string]any{"title": "p"}}); rec.Code != 201 {
		t.Fatalf("create promo: %d %s", rec.Code, rec.Body.String())
	}
	if code, _, raw := e.put(t, e.ownerA, "promo-a", `"1"`, at(when)); code != 201 {
		t.Fatalf("schedule promo: %d %s", code, raw)
	}
	if rec := e.doH(t, e.ownerA, "DELETE", e.docPath("/promo-a"), nil, nil); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if s := e.schedule(t, "promo-a"); s.Status != "cancelled" || s.Result != "document_deleted" {
		t.Fatalf("%+v", s)
	}
}

func TestValidationFailureAtFireTimePublishesNothingAndFlagsTheDocument(t *testing.T) {
	e := newSchedEnv(t)
	when := e.clockNow.Add(10 * time.Minute)
	e.put(t, e.ownerA, "banner", `"2"`, at(when))
	// A stricter schema is registered after scheduling: title may now have 3
	// characters at most. The draft ("hello") no longer passes.
	strict := `{"format":2,"types":{"note":{"title":"Note","schema":{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string","maxLength":3,"default":""},"body":{"type":"string","maxLength":5000,"default":"","x-editor":"richtext"},"link":{"type":"string","maxLength":200,"default":"","x-editor":"link"}}}},"global":{"title":"Global","schema":` + globalSchema + `}},"documents":{"banner":{"type":"note"},"global":{"type":"global"}},"collections":{"promo-":{"type":"note","max":2}}}`
	if code, _, raw := e.do(t, e.ownerA, "PUT", e.path(e.shopA, "/content/schema?confirm_removals=true"), []byte(strict)); code != 200 {
		t.Fatalf("register: %d %s", code, raw)
	}
	e.clockNow = when.Add(time.Minute)
	rep := e.run(t)
	if rep.Failed != 1 || rep.Published != 0 {
		t.Fatalf("%+v", rep)
	}
	st, attention, _ := e.docStatus(t, "banner")
	if st != 0 || !attention {
		t.Fatalf("status %d attention %v: nothing may be published and the document must need attention", st, attention)
	}
	s := e.schedule(t, "banner")
	if s.Status != "failed" || s.Result != "invalid" || !strings.Contains(string(s.Failure), "/title") {
		t.Fatalf("schedule: %+v failure=%s", s, s.Failure)
	}
	if strings.Contains(string(s.Failure), "hello") {
		t.Fatal("the failure record must not carry content")
	}
	if countAudit(e.schemaEnv, contentstore.AuditScheduleFailed) != 1 {
		t.Fatal("no failure audit row")
	}
	if rec := e.pub(t, "schema-a", "/api/v1/shop/content/banner", nil); rec.Code != 404 {
		t.Fatal("the document is public")
	}
	// The failure is visible through the API.
	got := jsonOf(t, e.doH(t, e.ownerA, "GET", e.schedPath("banner"), nil, nil))
	if got["status"] != "failed" || got["result"] != "invalid" {
		t.Fatalf("%v", got)
	}
}

func TestMissedWindowAndLateWithinGrace(t *testing.T) {
	e := newSchedEnv(t)
	when := e.clockNow.Add(10 * time.Minute)
	e.put(t, e.ownerA, "banner", `"2"`, at(when))
	// The API/scheduler was down for 30 minutes: within the 1 h grace, so it
	// is published late and the lateness is recorded.
	e.clockNow = when.Add(30 * time.Minute)
	if rep := e.run(t); rep.Published != 1 {
		t.Fatalf("%+v", rep)
	}
	if s := e.schedule(t, "banner"); s.Status != "done" || s.LateSeconds < 29*60 {
		t.Fatalf("%+v", s)
	}
	// A second document is down for 3 hours: missed, not published.
	e.doH(t, e.ownerA, "POST", e.docPath("/banner/unpublish"), nil, nil)
	e.saveDoc(t, e.ownerA, "banner", `"2"`, note("again"))
	when2 := e.clockNow.Add(10 * time.Minute)
	if code, _, raw := e.put(t, e.ownerA, "banner", `"3"`, at(when2)); code != 201 {
		t.Fatalf("%d %s", code, raw)
	}
	e.clockNow = when2.Add(3 * time.Hour)
	rep := e.run(t)
	if rep.Missed != 1 || rep.Published != 0 {
		t.Fatalf("%+v", rep)
	}
	if st, _, _ := e.docStatus(t, "banner"); st != 0 {
		t.Fatal("a missed schedule published")
	}
	if s := e.schedule(t, "banner"); s.Status != "missed" || s.Result != "missed" {
		t.Fatalf("%+v", s)
	}
	if countAudit(e.schemaEnv, contentstore.AuditScheduleMissed) != 1 {
		t.Fatal("no missed audit row")
	}
}

func TestScheduleIsRevalidatedForThePersonWhoMadeIt(t *testing.T) {
	e := newSchedEnv(t)
	// A custom role holds content.publish; the person is then removed from the shop.
	when := e.clockNow.Add(10 * time.Minute)
	if code, _, raw := e.put(t, e.contentOnly, "banner", `"2"`, at(when)); code != 201 {
		t.Fatalf("schedule as content-only: %d %s", code, raw)
	}
	e.client.ShopUser.Update().Where(entshopuser.ShopIDEQ(e.shopA), entshopuser.UserIDEQ(e.contentOnly)).SetStatus(0).ExecX(context.Background())
	e.engine.InvalidateUsers(context.Background(), e.contentOnly)
	e.clockNow = when.Add(time.Minute)
	rep := e.run(t)
	if rep.Failed != 1 || rep.Published != 0 {
		t.Fatalf("%+v", rep)
	}
	if s := e.schedule(t, "banner"); s.Status != "failed" || s.Result != "permission_revoked" {
		t.Fatalf("%+v", s)
	}
	if st, _, _ := e.docStatus(t, "banner"); st != 0 {
		t.Fatal("published on behalf of a disabled member")
	}
}

func TestTwoSchedulersPublishOnce(t *testing.T) {
	e := newSchedEnv(t)
	when := e.clockNow.Add(10 * time.Minute)
	e.put(t, e.ownerA, "banner", `"2"`, at(when))
	e.clockNow = when.Add(time.Minute)

	// Another scheduler holds the advisory lock (a session lock on a separate
	// connection): this run must skip, not publish.
	conn, err := e.sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_lock($1)`, int64(0x636f6e74656e74)); err != nil {
		t.Fatal(err)
	}
	if rep := e.run(t); !rep.Skipped || rep.Published != 0 {
		t.Fatalf("expected the run to skip while the lock is held: %+v", rep)
	}
	if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, int64(0x636f6e74656e74)); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	// Two schedulers racing: exactly one publish, no duplicate revision.
	var wg sync.WaitGroup
	reps := make([]content.RunReport, 2)
	for i := range reps {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			reps[i], _ = e.sched.RunOnce(context.Background())
		}()
	}
	wg.Wait()
	if reps[0].Published+reps[1].Published != 1 {
		t.Fatalf("published %d + %d, want exactly 1", reps[0].Published, reps[1].Published)
	}
	if n := e.pubRevs("banner"); n != 1 {
		t.Fatalf("revisions: %d", n)
	}
	if n := countAudit(e.schemaEnv, "doc.publish"); n != 1 {
		t.Fatalf("publish audit rows: %d", n)
	}
}

func TestStuckRunningClaimIsRecoveredAndNotRepublished(t *testing.T) {
	e := newSchedEnv(t)
	when := e.clockNow.Add(10 * time.Minute)
	e.put(t, e.ownerA, "banner", `"2"`, at(when))
	e.clockNow = when.Add(30 * time.Minute)
	// A run claimed it and crashed 20 minutes ago.
	e.client.ContentSchedule.Update().Where(entsched.ShopIDEQ(e.shopA)).
		SetStatus("running").SetClaimedAt(e.clockNow.Add(-20 * time.Minute)).ExecX(context.Background())
	if rep := e.run(t); rep.Published != 1 {
		t.Fatalf("recovered claim not processed: %+v", rep)
	}
	if st, _, _ := e.docStatus(t, "banner"); st != 1 {
		t.Fatal("not published")
	}
	// The crashed run had in fact published before dying: the retry must see
	// the draft already live and write nothing again.
	e.saveDoc(t, e.ownerA, "promo-b", "*", map[string]any{"type_key": "note", "content": map[string]any{"title": "x"}})
	if code, _, raw := e.put(t, e.ownerA, "promo-b", `"1"`, at(e.clockNow.Add(10*time.Minute))); code != 201 {
		t.Fatalf("%d %s", code, raw)
	}
	e.doH(t, e.ownerA, "POST", e.docPath("/promo-b/publish"), nil, nil) // supersedes the pending one
	// Force the crashed-run shape: schedule row back to running although the doc is live.
	e.client.ContentSchedule.Update().Where(entsched.DocKeyEQ("promo-b")).
		SetStatus("running").SetResult("").ClearFinishedAt().SetClaimedAt(e.clockNow.Add(-20 * time.Minute)).
		SetPublishAt(e.clockNow.Add(-time.Minute)).ExecX(context.Background())
	before := e.pubRevs("promo-b")
	rep := e.run(t)
	after := e.pubRevs("promo-b")
	if rep.AlreadyPublished != 1 || before != after {
		t.Fatalf("%+v revisions %d -> %d", rep, before, after)
	}
}

func TestOneScheduleAtATimePerDocument(t *testing.T) {
	e := newSchedEnv(t)
	when := e.clockNow.Add(10 * time.Minute)
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _, _ := e.put(t, e.ownerA, "banner", `"2"`, at(when.Add(time.Duration(i)*time.Minute)))
			codes[i] = code
		}()
	}
	wg.Wait()
	if n := e.client.ContentSchedule.Query().Where(entsched.StatusEQ("pending")).CountX(context.Background()); n != 1 {
		t.Fatalf("pending schedules: %d (codes %v)", n, codes)
	}
}
