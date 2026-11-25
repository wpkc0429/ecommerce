package httpapi_test

import (
	"context"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/content"
	entaudit "ksdevworks/ecommerce/api/internal/ent/contentauditlog"
	entsched "ksdevworks/ecommerce/api/internal/ent/contentschedule"
)

func TestPurgeDryRunApplyAndIdempotency(t *testing.T) {
	e := newSchemaEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-200 * 24 * time.Hour)
	recent := now.Add(-10 * 24 * time.Hour)
	mk := func(shop int, action string, at time.Time) {
		e.client.ContentAuditLog.Create().SetShopID(shop).SetAction(action).SetDocKey("home").SetCreatedAt(at).SaveX(ctx)
	}
	for i := 0; i < 3; i++ {
		mk(e.shopA, "doc.save", old)
	}
	mk(e.shopA, "doc.publish", old)
	mk(e.shopB, "doc.save", old)
	mk(e.shopA, "doc.save", recent)
	mk(e.shopB, "hook.fire", recent)
	// Edge: exactly inside the retention window is kept.
	mk(e.shopA, "doc.save", now.Add(-179*24*time.Hour))
	before := e.client.ContentAuditLog.Query().CountX(ctx)

	sched := func(shop int, key, status string, publishAt, finished time.Time) {
		c := e.client.ContentSchedule.Create().SetShopID(shop).SetDocKey(key).SetDraftRev(1).SetPublishAt(publishAt).SetStatus(status)
		if status != "pending" && status != "running" {
			c.SetFinishedAt(finished)
		}
		c.SaveX(ctx)
	}
	sched(e.shopA, "a", "done", old, old)
	sched(e.shopA, "b", "failed", old, old)
	sched(e.shopB, "a", "missed", old, old)
	sched(e.shopA, "c", "done", recent, recent) // finished recently: kept
	sched(e.shopA, "d", "pending", old, time.Time{})
	sched(e.shopB, "d", "running", old, time.Time{}) // an old claim must never be purged

	cfg := content.PurgeConfig{}
	// Dry run: counts and ranges, nothing deleted.
	rep, err := content.Purge(ctx, e.client, now, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Applied || rep.AuditRows != 5 || rep.AuditByAction["doc.save"] != 4 || rep.AuditByAction["doc.publish"] != 1 || rep.ScheduleRows != 3 {
		t.Fatalf("dry run report: %+v", rep)
	}
	if rep.AuditOldest == nil || rep.AuditNewest == nil || rep.ScheduleOldest == nil {
		t.Fatalf("ranges missing: %+v", rep)
	}
	if e.client.ContentAuditLog.Query().CountX(ctx) != before || e.client.ContentSchedule.Query().CountX(ctx) != 6 {
		t.Fatal("the dry run deleted something")
	}
	// Restricting to a shop.
	repB, _ := content.Purge(ctx, e.client, now, content.PurgeConfig{Shop: e.shopB}, false)
	if repB.AuditRows != 1 || repB.ScheduleRows != 1 {
		t.Fatalf("shop filter: %+v", repB)
	}
	// Apply.
	rep, err = content.Purge(ctx, e.client, now, cfg, true)
	if err != nil || !rep.Applied || rep.AuditRows != 5 || rep.ScheduleRows != 3 {
		t.Fatalf("apply: %v %+v", err, rep)
	}
	if got := e.client.ContentAuditLog.Query().CountX(ctx); got != before-5 {
		t.Fatalf("audit rows left: %d, want %d", got, before-5)
	}
	if e.client.ContentAuditLog.Query().Where(entaudit.CreatedAtLT(now.Add(-180*24*time.Hour))).CountX(ctx) != 0 {
		t.Fatal("an expired audit row survived")
	}
	if got := e.client.ContentSchedule.Query().CountX(ctx); got != 3 {
		t.Fatalf("schedules left: %d (recent finished + pending + running)", got)
	}
	if e.client.ContentSchedule.Query().Where(entsched.StatusIn("pending", "running")).CountX(ctx) != 2 {
		t.Fatal("an unfinished schedule was purged")
	}
	// Idempotent: nothing left to do, and a second apply changes nothing.
	rep, err = content.Purge(ctx, e.client, now, cfg, true)
	if err != nil || rep.AuditRows != 0 || rep.ScheduleRows != 0 {
		t.Fatalf("second apply: %v %+v", err, rep)
	}
	// Batches: more rows than one batch.
	for i := 0; i < 2300; i++ {
		e.client.ContentAuditLog.Create().SetShopID(e.shopA).SetAction("doc.save").SetCreatedAt(old).SaveX(ctx)
	}
	rep, err = content.Purge(ctx, e.client, now, cfg, true)
	if err != nil || rep.AuditRows != 2300 || e.client.ContentAuditLog.Query().Where(entaudit.CreatedAtLT(now.Add(-180*24*time.Hour))).CountX(ctx) != 0 {
		t.Fatalf("batched apply: %v %+v", err, rep)
	}
}
