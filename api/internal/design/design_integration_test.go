package design_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/database"
	"ksdevworks/ecommerce/api/internal/design"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// Integration tests of the database-level guarantees: the append-only
// triggers, the hash chain verifier and the retention purge.

type allowAll struct{}

func (allowAll) Allows(context.Context, int, int, string) (bool, error) { return true, nil }

type fixture struct {
	t      *testing.T
	client *ent.Client
	db     *sql.DB
	svc    *design.Service
	now    time.Time
}

const (
	sha1 = "1111111111111111111111111111111111111111"
	sha2 = "2222222222222222222222222222222222222222"
)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	client := testutil.OpenDB(t)
	_, db, err := database.Open(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &fixture{t: t, client: client, db: db, now: time.Now().UTC().Truncate(time.Microsecond)}
	f.svc = &design.Service{Client: client, Authz: allowAll{}, Cfg: design.DefaultConfig(), Now: func() time.Time { return f.now }}
	return f
}

var seq int

// request creates a request through the service (so it has a real chain).
func (f *fixture) request(shop int, site string) *ent.DesignRequest {
	f.t.Helper()
	seq++
	r, _, err := f.svc.CreateRequest(context.Background(), design.CreateInput{
		ShopID: shop, CredentialID: 1, IdempotencyKey: "fixture-key-" + string(rune('a'+seq%26)) + strings.Repeat("x", 6) + time.Now().Format("150405.000000") + string(rune('a'+seq/26%26)),
		Site: site, RequestedBy: 1, Prompt: "p", BaseSHA: sha1,
	})
	if err != nil {
		f.t.Fatalf("create: %v", err)
	}
	return r
}

func (f *fixture) exec(q string, args ...any) error {
	_, err := f.db.Exec(q, args...)
	return err
}

func (f *fixture) mustFail(what, q string, args ...any) {
	f.t.Helper()
	if err := f.exec(q, args...); err == nil {
		f.t.Errorf("%s: the database allowed it", what)
	}
}

// withRetention runs fn in a transaction that is allowed past the triggers.
func (f *fixture) withRetention(fn func(tx *sql.Tx)) {
	f.t.Helper()
	tx, err := f.db.Begin()
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT set_config('design.retention','on',true)`); err != nil {
		f.t.Fatal(err)
	}
	fn(tx)
	if err := tx.Commit(); err != nil {
		f.t.Fatal(err)
	}
}

func TestAppendOnlyTriggersRefuseUpdateAndDelete(t *testing.T) {
	f := newFixture(t)
	r := f.request(1, "admin")
	// events
	f.mustFail("UPDATE event", `UPDATE design_request_events SET kind = 'failed' WHERE request_id = $1`, r.ID)
	f.mustFail("UPDATE event detail", `UPDATE design_request_events SET detail = '{"x":1}' WHERE request_id = $1`, r.ID)
	f.mustFail("DELETE event", `DELETE FROM design_request_events WHERE request_id = $1`, r.ID)
	// immutable request columns
	for _, col := range []struct{ name, set string }{
		{"shop_id", "shop_id = 99"}, {"kind", "kind = 'revert'"}, {"site", "site = 'storefront'"},
		{"requested_by_user_id", "requested_by_user_id = 9"}, {"prompt_sha256", "prompt_sha256 = repeat('0',64)"},
		{"base_sha", "base_sha = repeat('9',40)"}, {"created_at", "created_at = now() - interval '1 day'"},
		{"id", "id = gen_random_uuid()"},
	} {
		f.mustFail("UPDATE requests."+col.name, `UPDATE design_requests SET `+col.set+` WHERE id = $1`, r.ID)
	}
	f.mustFail("DELETE request", `DELETE FROM design_requests WHERE id = $1`, r.ID)
	// mutable columns still work (the state machine needs them)
	if err := f.exec(`UPDATE design_requests SET failure_detail = 'ok' WHERE id = $1`, r.ID); err != nil {
		t.Errorf("a mutable column was blocked: %v", err)
	}
	// screenshots: only purged_at may change
	if _, err := f.client.DesignScreenshot.Create().SetShopID(1).SetRequestID(r.ID).SetPhase("before").SetSite("admin").SetViewKey("home").
		SetViewport(390).SetRole("owner").SetCommitSha(sha1).SetObjectKey("k").SetByteSize(5).SetWidth(390).SetHeight(5).SetSha256(strings.Repeat("a", 64)).Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mustFail("UPDATE screenshot object_key", `UPDATE design_screenshots SET object_key = 'other' WHERE request_id = $1`, r.ID)
	f.mustFail("UPDATE screenshot sha", `UPDATE design_screenshots SET sha256 = repeat('b',64) WHERE request_id = $1`, r.ID)
	f.mustFail("DELETE screenshot", `DELETE FROM design_screenshots WHERE request_id = $1`, r.ID)
	if err := f.exec(`UPDATE design_screenshots SET purged_at = now() WHERE request_id = $1`, r.ID); err != nil {
		t.Errorf("purged_at must be updatable: %v", err)
	}
	// untracked changes: the acknowledgement once, nothing else
	f.client.DesignUntrackedChange.Create().SetShopID(1).SetSite("admin").SetCommitSha(sha2).SetCommittedAt(f.now).SaveX(context.Background())
	f.mustFail("UPDATE untracked subject", `UPDATE design_untracked_changes SET subject = 'x'`)
	f.mustFail("DELETE untracked", `DELETE FROM design_untracked_changes`)
	if err := f.exec(`UPDATE design_untracked_changes SET acknowledged_by_user_id = 3, acknowledged_at = now()`); err != nil {
		t.Errorf("first acknowledgement refused: %v", err)
	}
	f.mustFail("second acknowledgement", `UPDATE design_untracked_changes SET acknowledged_by_user_id = 4, acknowledged_at = now()`)

	// The retention switch is scoped to its transaction and is the only way past.
	f.withRetention(func(tx *sql.Tx) {
		if _, err := tx.Exec(`UPDATE design_request_events SET detail = '{"purged":true}' WHERE request_id = $1`, r.ID); err != nil {
			t.Errorf("retention could not update: %v", err)
		}
	})
	f.mustFail("UPDATE event after the retention transaction ended", `UPDATE design_request_events SET kind = 'failed' WHERE request_id = $1`, r.ID)
	// ... and a session-level SET does not leak into other statements as 'on'.
	if err := f.exec(`SELECT set_config('design.retention','off',false)`); err != nil {
		t.Fatal(err)
	}
	f.mustFail("DELETE event with retention off", `DELETE FROM design_request_events WHERE request_id = $1`, r.ID)
}

func TestVerifyChainsDetectsAnEditedMiddleRow(t *testing.T) {
	f := newFixture(t)
	r := f.request(1, "admin")
	ctx := context.Background()
	// Build a few events through the real state machine.
	if _, err := f.svc.UpdateHead(ctx, 1, r.ID, 1, sha2, design.DiffSummary{}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RecordGuard(ctx, 1, r.ID, 1, design.GuardInput{CheckedSHA: sha2, Passed: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ReportFailed(ctx, 1, r.ID, 1, "boom", "x"); err != nil {
		t.Fatal(err)
	}
	rep, err := f.svc.VerifyChains(ctx, 0)
	if err != nil || len(rep.Broken) != 0 || rep.Verified != 1 {
		t.Fatalf("intact chain: %+v %v", rep, err)
	}
	// Someone with enough privilege rewrites the middle event.
	f.withRetention(func(tx *sql.Tx) {
		if _, err := tx.Exec(`UPDATE design_request_events SET detail = '{"passed":false}' WHERE request_id = $1 AND seq = 3`, r.ID); err != nil {
			t.Fatal(err)
		}
	})
	rep, _ = f.svc.VerifyChains(ctx, 0)
	if len(rep.Broken) != 1 || rep.Broken[0].Seq != 3 {
		t.Fatalf("edit not detected: %+v", rep)
	}
	// ... and a removed middle row is detected too (on a second request).
	r2 := f.request(2, "admin")
	if _, err := f.svc.UpdateHead(ctx, 2, r2.ID, 1, sha2, design.DiffSummary{}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ReportFailed(ctx, 2, r2.ID, 1, "boom", "x"); err != nil {
		t.Fatal(err)
	}
	f.withRetention(func(tx *sql.Tx) {
		if _, err := tx.Exec(`DELETE FROM design_request_events WHERE request_id = $1 AND seq = 2`, r2.ID); err != nil {
			t.Fatal(err)
		}
	})
	rep, _ = f.svc.VerifyChains(ctx, 2)
	if len(rep.Broken) != 1 {
		t.Fatalf("removed row not detected: %+v", rep)
	}
}

func TestEventAndRequestCommitTogether(t *testing.T) {
	f := newFixture(t)
	r := f.request(1, "admin")
	ctx := context.Background()
	// A conflicting move must write neither the status nor an event.
	before := f.countEvents(r.ID)
	if _, err := f.svc.Submit(ctx, 1, r.ID, 1); err == nil {
		t.Fatal("submit from drafting must fail")
	}
	if f.countEvents(r.ID) != before {
		t.Fatal("a refused move left an event behind")
	}
	cur, _ := f.svc.Get(ctx, 1, r.ID)
	if cur.Rev != before {
		t.Fatalf("rev %d != events %d", cur.Rev, before)
	}
	// Every successful move: rev equals the event count.
	if _, err := f.svc.ReportFailed(ctx, 1, r.ID, 1, "x", ""); err != nil {
		t.Fatal(err)
	}
	cur, _ = f.svc.Get(ctx, 1, r.ID)
	if cur.Rev != f.countEvents(r.ID) || cur.Rev != 2 {
		t.Fatalf("rev %d events %d", cur.Rev, f.countEvents(r.ID))
	}
}

func (f *fixture) countEvents(id uuid.UUID) int {
	var n int
	if err := f.db.QueryRow(`SELECT count(*) FROM design_request_events WHERE request_id = $1`, id).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// ── retention ───────────────────────────────────────────────────────────

func (f *fixture) backdate(id uuid.UUID, age time.Duration) {
	f.t.Helper()
	f.withRetention(func(tx *sql.Tx) {
		at := f.now.Add(-age)
		if _, err := tx.Exec(`UPDATE design_requests SET created_at = $2 WHERE id = $1`, id, at); err != nil {
			f.t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE design_screenshots SET created_at = $2 WHERE request_id = $1`, id, at); err != nil {
			f.t.Fatal(err)
		}
	})
}

func (f *fixture) finish(r *ent.DesignRequest, status string) {
	f.t.Helper()
	f.withRetention(func(tx *sql.Tx) {
		if _, err := tx.Exec(`UPDATE design_requests SET status = $2 WHERE id = $1`, r.ID, status); err != nil {
			f.t.Fatal(err)
		}
	})
}

func (f *fixture) shot(r *ent.DesignRequest, key string) {
	f.client.DesignScreenshot.Create().SetShopID(r.ShopID).SetRequestID(r.ID).SetPhase("before").SetSite("admin").SetViewKey("home").
		SetViewport(390).SetRole("owner").SetCommitSha(sha1).SetObjectKey(key).SetByteSize(5).SetWidth(390).SetHeight(5).
		SetSha256(strings.Repeat("a", 64)).SetCaptureManifest(json.RawMessage(`{}`)).SaveX(context.Background())
}

type fakeStore struct{ deleted []string }

func (s *fakeStore) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	return nil
}

func (f *fixture) purger(store design.ObjectDeleter) *design.Purger {
	return &design.Purger{Client: f.client, DB: f.db, Store: store, Now: func() time.Time { return f.now }}
}

func day(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func TestPurgeRetentionBoundariesAndSafety(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cfg := design.DefaultPurgeConfig()
	var all []*ent.DesignRequest
	mk := func(shop int, site, status string, age time.Duration, shotKey string) *ent.DesignRequest {
		r := f.request(shop, site)
		f.shot(r, shotKey)
		f.finish(r, status)
		f.backdate(r.ID, age)
		all = append(all, r)
		return r
	}
	oldPub := mk(1, "admin", "published", day(366), "shops/1/old-pub.png")
	freshPub := mk(1, "storefront", "published", day(364), "shops/1/fresh-pub.png")
	oldFailed := mk(2, "admin", "failed", day(31), "shops/2/old-failed.png")
	freshFailed := mk(2, "storefront", "failed", day(29), "shops/2/fresh-failed.png")
	// A request still in progress, however old, is never touched.
	active := f.request(3, "admin")
	f.shot(active, "shops/3/active.png")
	f.backdate(active.ID, day(5000))
	// A very old finished request (audit retention 730 days).
	ancient := mk(3, "storefront", "cancelled", day(800), "shops/3/ancient.png")

	// Dry run: counts only, nothing written.
	rep, err := f.purger(nil).Purge(ctx, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Prompts != 2 || rep.Screenshots != 3 || rep.Requests != 1 {
		t.Fatalf("dry run counts: %+v", rep)
	}
	if f.client.DesignScreenshot.Query().CountX(ctx) != 6 || f.client.DesignRequest.Query().CountX(ctx) != 6 {
		t.Fatal("a dry run changed data")
	}

	store := &fakeStore{}
	rep, err = f.purger(store).Purge(ctx, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Prompts != 2 || rep.Requests != 1 {
		t.Fatalf("apply counts: %+v", rep)
	}
	// The 366-day published request lost its prompt and screenshot object, kept its row and events.
	pr, _ := f.svc.Get(ctx, 1, oldPub.ID)
	if pr.PromptText != nil || pr.PromptSha256 == "" {
		t.Fatalf("prompt not blanked / hash lost: %+v", pr.PromptText)
	}
	if f.countEvents(oldPub.ID) == 0 {
		t.Fatal("events of a purged-prompt request were deleted")
	}
	// The 364-day one is untouched.
	fp, _ := f.svc.Get(ctx, 1, freshPub.ID)
	if fp.PromptText == nil {
		t.Fatal("a request inside retention lost its prompt")
	}
	// Unpublished screenshots go after 30 days, not 29.
	_ = oldFailed
	_ = freshFailed
	got := map[string]bool{}
	for _, k := range store.deleted {
		got[k] = true
	}
	for _, want := range []string{"shops/1/old-pub.png", "shops/2/old-failed.png", "shops/3/ancient.png"} {
		if !got[want] {
			t.Errorf("object %s not deleted (deleted %v)", want, store.deleted)
		}
	}
	for _, keep := range []string{"shops/1/fresh-pub.png", "shops/2/fresh-failed.png", "shops/3/active.png"} {
		if got[keep] {
			t.Errorf("object %s deleted too early", keep)
		}
	}
	// The ancient request is gone entirely (cascade included); the active one is not.
	if _, err := f.svc.Get(ctx, 3, ancient.ID); err == nil {
		t.Fatal("a request past the audit retention survived")
	}
	if _, err := f.svc.Get(ctx, 3, active.ID); err != nil {
		t.Fatalf("an active request was purged: %v", err)
	}
	if f.countEvents(ancient.ID) != 0 {
		t.Fatal("events of a purged request remain")
	}
	// Idempotent: a second run does nothing.
	rep2, err := f.purger(&fakeStore{}).Purge(ctx, cfg, true)
	if err != nil || rep2.Prompts != 0 || rep2.Screenshots != 0 || rep2.Requests != 0 {
		t.Fatalf("second run: %+v %v", rep2, err)
	}
	// Chains of what is left still verify.
	if vr, _ := f.svc.VerifyChains(ctx, 0); len(vr.Broken) != 0 {
		t.Fatalf("chains: %+v", vr.Broken)
	}
}

func TestPurgeShopFilterCredentialsUntrackedAndAllForShop(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.request(1, "admin"), f.request(2, "admin")
	for _, r := range []*ent.DesignRequest{a, b} {
		f.shot(r, "shops/x/"+r.ID.String())
		f.finish(r, "published")
		f.backdate(r.ID, day(400))
	}
	cfg := design.DefaultPurgeConfig()
	cfg.Shop = 1
	rep, err := f.purger(&fakeStore{}).Purge(ctx, cfg, true)
	if err != nil || rep.Prompts != 1 {
		t.Fatalf("shop-scoped: %+v %v", rep, err)
	}
	if pb, _ := f.svc.Get(ctx, 2, b.ID); pb.PromptText == nil {
		t.Fatal("-shop 1 touched shop 2")
	}

	// credentials: revoked/expired ones are purged after 90 days; live ones never.
	for i, n := range []string{"old-revoked", "old-expired", "live"} {
		f.client.DesignServiceCredential.Create().SetShopID(1).SetName(n).SetKeyPrefix("dsk_").SetKeyHash(strings.Repeat(string(rune('a'+i)), 64)).
			SetCreatedByUserID(1).SetCreatedAt(f.now.Add(-day(300))).SetExpiresAt(f.now.Add(day(10))).SaveX(ctx)
	}
	if err := f.exec(`UPDATE design_service_credentials SET revoked_at = now() - interval '100 days' WHERE name = 'old-revoked'`); err != nil {
		t.Fatal(err)
	}
	if err := f.exec(`UPDATE design_service_credentials SET expires_at = now() - interval '100 days' WHERE name = 'old-expired'`); err != nil {
		t.Fatal(err)
	}
	// untracked rows older than 730 days.
	f.client.DesignUntrackedChange.Create().SetShopID(1).SetSite("admin").SetCommitSha(sha1).SetCommittedAt(f.now).SetDetectedAt(f.now.Add(-day(800))).SaveX(ctx)
	f.client.DesignUntrackedChange.Create().SetShopID(1).SetSite("admin").SetCommitSha(sha2).SetCommittedAt(f.now).SetDetectedAt(f.now.Add(-day(10))).SaveX(ctx)
	cfg.Shop = 0
	rep, err = f.purger(&fakeStore{}).Purge(ctx, cfg, true)
	if err != nil || rep.Credentials != 2 || rep.Untracked != 1 {
		t.Fatalf("creds/untracked: %+v %v", rep, err)
	}
	if f.client.DesignServiceCredential.Query().CountX(ctx) != 1 || f.client.DesignUntrackedChange.Query().CountX(ctx) != 1 {
		t.Fatal("wrong rows left")
	}

	// export has no signed URLs and covers only the shop.
	ex, err := f.purger(nil).ExportShop(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ex)
	if len(ex.Requests) != 1 || strings.Contains(string(raw), "X-Amz") || strings.Contains(string(raw), "http") {
		t.Fatalf("export: %s", raw)
	}
	// all-for-shop: dry run counts, apply deletes only that shop.
	f.client.DesignSiteState.Create().SetShopID(2).SetSite("admin").SaveX(ctx)
	live := f.request(2, "storefront") // non-terminal: it goes too
	_ = live
	rep, err = f.purger(&fakeStore{}).AllForShop(ctx, 2, false)
	if err != nil || rep.Requests != 2 {
		t.Fatalf("dry run: %+v %v", rep, err)
	}
	if f.client.DesignRequest.Query().CountX(ctx) != 3 {
		t.Fatal("dry run deleted")
	}
	store := &fakeStore{}
	if _, err = f.purger(store).AllForShop(ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	if n := f.client.DesignRequest.Query().CountX(ctx); n != 1 {
		t.Fatalf("%d requests left, want 1 (shop 1's)", n)
	}
	if _, err := f.svc.Get(ctx, 1, a.ID); err != nil {
		t.Fatalf("shop 1 lost its request: %v", err)
	}
	if f.client.DesignSiteState.Query().CountX(ctx) != 0 {
		t.Fatal("site state of the deleted shop remains")
	}
}

func TestPurgeLeavesRequestsThatAreStillReferenced(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// An old finished revert request that a recent release still points at.
	old := f.request(1, "admin")
	f.finish(old, "published")
	f.backdate(old.ID, day(900))
	recent := f.request(1, "storefront")
	f.finish(recent, "published")
	f.withRetention(func(tx *sql.Tx) {
		if _, err := tx.Exec(`UPDATE design_requests SET reverted_by_request_id = $2 WHERE id = $1`, recent.ID, old.ID); err != nil {
			t.Fatal(err)
		}
	})
	rep, err := f.purger(&fakeStore{}).Purge(ctx, design.DefaultPurgeConfig(), true)
	if err != nil {
		t.Fatalf("purge failed on a referenced row: %v", err)
	}
	if rep.Requests != 0 {
		t.Fatalf("deleted a request that something still points at: %+v", rep)
	}
	if _, err := f.svc.Get(ctx, 1, old.ID); err != nil {
		t.Fatalf("referenced request is gone: %v", err)
	}
}
