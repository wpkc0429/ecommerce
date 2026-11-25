package content

import (
	"context"
	"time"

	"ksdevworks/ecommerce/api/internal/contentstore"
	"ksdevworks/ecommerce/api/internal/ent"
	entaudit "ksdevworks/ecommerce/api/internal/ent/contentauditlog"
	entsched "ksdevworks/ecommerce/api/internal/ent/contentschedule"
	"ksdevworks/ecommerce/api/internal/ent/predicate"
)

// PurgeConfig is the retention policy (design D12).
type PurgeConfig struct {
	// AuditRetention: content_audit_log rows older than this are purged (180 days).
	AuditRetention time.Duration
	// ScheduleRetention: finished schedule rows older than this are purged (90 days).
	ScheduleRetention time.Duration
	// Shop limits the purge to one shop (0 = all).
	Shop int
}

// PurgeReport says what a purge found (dry run) or removed (apply). It holds
// counts and time ranges only - the audit log holds no personal data, but the
// report does not print rows anyway.
type PurgeReport struct {
	Applied bool
	// Audit.
	AuditCutoff   time.Time
	AuditRows     int
	AuditByAction map[string]int
	AuditOldest   *time.Time
	AuditNewest   *time.Time
	// Finished schedules.
	ScheduleCutoff time.Time
	ScheduleRows   int
	ScheduleOldest *time.Time
	ScheduleNewest *time.Time
}

const purgeBatch = 1000

// Purge removes content_audit_log rows past AuditRetention and finished
// schedule rows past ScheduleRetention. apply=false only counts. It is
// idempotent (a second run finds nothing new), runs in batches so it never
// holds a long transaction, and never touches pending or running schedules.
func Purge(ctx context.Context, c *ent.Client, now time.Time, cfg PurgeConfig, apply bool) (*PurgeReport, error) {
	if cfg.AuditRetention <= 0 {
		cfg.AuditRetention = 180 * 24 * time.Hour
	}
	if cfg.ScheduleRetention <= 0 {
		cfg.ScheduleRetention = 90 * 24 * time.Hour
	}
	rep := &PurgeReport{
		Applied:        apply,
		AuditCutoff:    now.Add(-cfg.AuditRetention),
		ScheduleCutoff: now.Add(-cfg.ScheduleRetention),
		AuditByAction:  map[string]int{},
	}

	audPreds := []predicate.ContentAuditLog{entaudit.CreatedAtLT(rep.AuditCutoff)}
	if cfg.Shop > 0 {
		audPreds = append(audPreds, entaudit.ShopIDEQ(cfg.Shop))
	}
	var byAction []struct {
		Action string `json:"action"`
		Count  int    `json:"count"`
	}
	if err := c.ContentAuditLog.Query().Where(audPreds...).GroupBy(entaudit.FieldAction).
		Aggregate(ent.Count()).Scan(ctx, &byAction); err != nil {
		return nil, err
	}
	for _, r := range byAction {
		rep.AuditByAction[r.Action] = r.Count
		rep.AuditRows += r.Count
	}
	if rep.AuditRows > 0 {
		if first, err := c.ContentAuditLog.Query().Where(audPreds...).Order(ent.Asc(entaudit.FieldCreatedAt)).First(ctx); err == nil {
			t := first.CreatedAt
			rep.AuditOldest = &t
		}
		if last, err := c.ContentAuditLog.Query().Where(audPreds...).Order(ent.Desc(entaudit.FieldCreatedAt)).First(ctx); err == nil {
			t := last.CreatedAt
			rep.AuditNewest = &t
		}
	}

	schedPreds := []predicate.ContentSchedule{
		entsched.StatusNotIn(contentstore.SchedPending, contentstore.SchedRunning),
		entsched.Or(entsched.FinishedAtLT(rep.ScheduleCutoff),
			entsched.And(entsched.FinishedAtIsNil(), entsched.UpdatedAtLT(rep.ScheduleCutoff))),
	}
	if cfg.Shop > 0 {
		schedPreds = append(schedPreds, entsched.ShopIDEQ(cfg.Shop))
	}
	n, err := c.ContentSchedule.Query().Where(schedPreds...).Count(ctx)
	if err != nil {
		return nil, err
	}
	rep.ScheduleRows = n
	if n > 0 {
		if first, err := c.ContentSchedule.Query().Where(schedPreds...).Order(ent.Asc(entsched.FieldPublishAt)).First(ctx); err == nil {
			t := first.PublishAt
			rep.ScheduleOldest = &t
		}
		if last, err := c.ContentSchedule.Query().Where(schedPreds...).Order(ent.Desc(entsched.FieldPublishAt)).First(ctx); err == nil {
			t := last.PublishAt
			rep.ScheduleNewest = &t
		}
	}
	if !apply {
		return rep, nil
	}

	for {
		ids, err := c.ContentAuditLog.Query().Where(audPreds...).Order(ent.Asc(entaudit.FieldID)).Limit(purgeBatch).IDs(ctx)
		if err != nil {
			return rep, err
		}
		if len(ids) == 0 {
			break
		}
		if _, err := c.ContentAuditLog.Delete().Where(entaudit.IDIn(ids...)).Exec(ctx); err != nil {
			return rep, err
		}
	}
	for {
		ids, err := c.ContentSchedule.Query().Where(schedPreds...).Order(ent.Asc(entsched.FieldID)).Limit(purgeBatch).IDs(ctx)
		if err != nil {
			return rep, err
		}
		if len(ids) == 0 {
			break
		}
		if _, err := c.ContentSchedule.Delete().Where(entsched.IDIn(ids...)).Exec(ctx); err != nil {
			return rep, err
		}
	}
	return rep, nil
}
