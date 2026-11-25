package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/contentstore"
	"ksdevworks/ecommerce/api/internal/ent"
	entsched "ksdevworks/ecommerce/api/internal/ent/contentschedule"
)

// ScheduleConfig is the policy of scheduled publishing (design D11).
type ScheduleConfig struct {
	MinLead        time.Duration // earliest publish_at from now (2 min)
	MaxAhead       time.Duration // furthest publish_at from now (90 days)
	Grace          time.Duration // later than this past publish_at = missed (1 h)
	RunningTimeout time.Duration // a claim older than this is a crashed run (10 min)
	MaxAttempts    int           // infrastructure failures before the schedule fails (5)
	BatchSize      int           // schedules processed per run (100)
}

// DefaultScheduleConfig is design D11.
func DefaultScheduleConfig() ScheduleConfig {
	return ScheduleConfig{
		MinLead: 2 * time.Minute, MaxAhead: 90 * 24 * time.Hour, Grace: time.Hour,
		RunningTimeout: 10 * time.Minute, MaxAttempts: 5, BatchSize: 100,
	}
}

func (c ScheduleConfig) withDefaults() ScheduleConfig {
	d := DefaultScheduleConfig()
	if c.MinLead < 0 {
		c.MinLead = 0
	}
	if c.MaxAhead <= 0 {
		c.MaxAhead = d.MaxAhead
	}
	if c.Grace <= 0 {
		c.Grace = d.Grace
	}
	if c.RunningTimeout <= 0 {
		c.RunningTimeout = d.RunningTimeout
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = d.MaxAttempts
	}
	if c.BatchSize <= 0 {
		c.BatchSize = d.BatchSize
	}
	return c
}

// Clock is the one time authority of the scheduler. Production uses the
// database's now(), so several API instances and a cron-run command can never
// disagree about "due" because of clock skew between machines.
type Clock interface {
	Now(ctx context.Context) (time.Time, error)
}

// DBClock reads now() from PostgreSQL.
type DBClock struct{ DB *sql.DB }

// Now implements Clock.
func (c DBClock) Now(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := c.DB.QueryRowContext(ctx, `SELECT now()`).Scan(&t)
	return t.UTC(), err
}

// FuncClock adapts a function (tests, a fake clock).
type FuncClock func() time.Time

// Now implements Clock.
func (f FuncClock) Now(context.Context) (time.Time, error) { return f(), nil }

// advisoryLockKey serializes scheduler runs across instances ("content" in hex).
const advisoryLockKey int64 = 0x636f6e74656e74

// Scheduler owns schedules: creating, changing and cancelling them for the
// admin API, and RunOnce, which publishes the ones that are due through the
// same Service.Publish path as a manual publish.
type Scheduler struct {
	Client *ent.Client
	// DB is used for the advisory lock; it must be the pool the ent client uses
	// (or any pool on the same database).
	DB    *sql.DB
	Docs  *Service
	Clock Clock
	// Authorize reports whether the user who scheduled still may publish in the
	// shop at fire time (member and content.publish).
	Authorize func(ctx context.Context, userID, shopID int) bool
	Cfg       ScheduleConfig
	Log       *slog.Logger
}

func (s *Scheduler) cfg() ScheduleConfig { return s.Cfg.withDefaults() }

func (s *Scheduler) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// ── API side ──────────────────────────────────────────────────────────────

// RoundUpToMinute normalizes a requested time to UTC at minute granularity,
// rounding up (never earlier than asked).
func RoundUpToMinute(t time.Time) time.Time {
	t = t.UTC()
	r := t.Truncate(time.Minute)
	if r.Before(t) {
		r = r.Add(time.Minute)
	}
	return r
}

func scheduleInvalid(pointer, msg string) error {
	return &cms.ValidationError{Message: msg, Details: []cms.Detail{{Pointer: pointer, Message: msg}}}
}

// Schedule creates or changes (reschedules) the pending schedule of a document.
// ifMatch is the draft_rev the caller reviewed: it must equal the current one,
// and the schedule is bound to it.
func (s *Scheduler) Schedule(ctx context.Context, shopID int, key string, publishAt time.Time, ifMatch int64, actor int) (*ent.ContentSchedule, bool, error) {
	cfg := s.cfg()
	now, err := s.Clock.Now(ctx)
	if err != nil {
		return nil, false, err
	}
	at := RoundUpToMinute(publishAt)
	if at.Before(now.Add(cfg.MinLead)) {
		return nil, false, scheduleInvalid("/publish_at", fmt.Sprintf("must be at least %s from now", cfg.MinLead))
	}
	if at.After(now.Add(cfg.MaxAhead)) {
		return nil, false, scheduleInvalid("/publish_at", fmt.Sprintf("must be within %d days from now", int(cfg.MaxAhead.Hours()/24)))
	}
	if err := contentstore.EnsureDocuments(ctx, s.Client, shopID); err != nil {
		return nil, false, err
	}
	d, err := contentstore.Get(ctx, s.Client, shopID, key)
	if err != nil {
		return nil, false, mapStoreErr(err)
	}
	if d.DraftRev != ifMatch {
		return nil, false, &ConflictError{CurrentRev: d.DraftRev}
	}
	// Fail fast: a draft that cannot be published now will not be published
	// later either (the same check runs again at fire time).
	v, err := s.Docs.activeSchema(ctx, shopID)
	if err != nil {
		return nil, false, err
	}
	schema, ok := typeSchema(v, d.TypeKey)
	if !ok {
		return nil, false, &cms.ValidationError{Message: "the document's type is not in the shop's content schema"}
	}
	if err := cms.ValidatePayload(schema, d.DraftJSON); err != nil {
		return nil, false, err
	}

	var out *ent.ContentSchedule
	created := false
	err = s.Docs.tx(ctx, func(c *ent.Client) error {
		existing, qerr := c.ContentSchedule.Query().
			Where(entsched.ShopIDEQ(shopID), entsched.DocKeyEQ(key), entsched.StatusIn(contentstore.SchedPending, contentstore.SchedRunning)).
			ForUpdate().Only(ctx)
		switch {
		case ent.IsNotFound(qerr):
			cr := c.ContentSchedule.Create().SetShopID(shopID).SetDocKey(key).
				SetDraftRev(d.DraftRev).SetPublishAt(at)
			if actor > 0 {
				cr.SetCreatedByUserID(actor)
			}
			var cerr error
			out, cerr = cr.Save(ctx)
			if cerr != nil {
				if ent.IsConstraintError(cerr) {
					return &StateError{Message: "another schedule of this document was just created; reload and try again"}
				}
				return cerr
			}
			created = true
		case qerr != nil:
			return qerr
		case existing.Status == contentstore.SchedRunning:
			return &StateError{Message: "this document's scheduled publish is being processed right now"}
		default:
			up := existing.Update().SetPublishAt(at).SetDraftRev(d.DraftRev)
			if actor > 0 {
				up.SetCreatedByUserID(actor)
			}
			var uerr error
			out, uerr = up.Save(ctx)
			if uerr != nil {
				return uerr
			}
		}
		return contentstore.Audit(ctx, c, contentstore.AuditEntry{
			ShopID: shopID, Actor: actor, Action: contentstore.AuditScheduleSet, Key: key,
			Detail: map[string]any{"schedule_id": out.ID, "publish_at": at.Format(time.RFC3339), "draft_rev": d.DraftRev, "rescheduled": !created},
		})
	})
	if err != nil {
		return nil, false, err
	}
	s.log().Info("content audit", "action", contentstore.AuditScheduleSet, "shop_id", shopID, "actor_user_id", actor,
		"doc_key", key, "schedule_id", out.ID)
	return out, created, nil
}

// Cancel cancels the pending schedule of a document.
func (s *Scheduler) Cancel(ctx context.Context, shopID int, key string, actor int) error {
	return s.Docs.tx(ctx, func(c *ent.Client) error {
		row, err := c.ContentSchedule.Query().
			Where(entsched.ShopIDEQ(shopID), entsched.DocKeyEQ(key), entsched.StatusEQ(contentstore.SchedPending)).
			ForUpdate().Only(ctx)
		if ent.IsNotFound(err) {
			// A running schedule cannot be cancelled; say so rather than 404.
			if running, _ := c.ContentSchedule.Query().Where(entsched.ShopIDEQ(shopID), entsched.DocKeyEQ(key), entsched.StatusEQ(contentstore.SchedRunning)).Exist(ctx); running {
				return &StateError{Message: "this document's scheduled publish is being processed right now"}
			}
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := row.Update().SetStatus(contentstore.SchedCancelled).SetResult(contentstore.ResultCancelled).
			SetFinishedAt(time.Now()).Save(ctx); err != nil {
			return err
		}
		return contentstore.Audit(ctx, c, contentstore.AuditEntry{
			ShopID: shopID, Actor: actor, Action: contentstore.AuditScheduleCancel, Key: key,
			Detail: map[string]any{"schedule_id": row.ID, "result": contentstore.ResultCancelled},
		})
	})
}

// Get returns the document's pending (or running) schedule, else the most
// recent finished one; ErrNotFound when it never had any.
func (s *Scheduler) Get(ctx context.Context, shopID int, key string) (*ent.ContentSchedule, error) {
	row, err := s.Client.ContentSchedule.Query().
		Where(entsched.ShopIDEQ(shopID), entsched.DocKeyEQ(key)).
		Order(ent.Desc(entsched.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	return row, err
}

// List scopes.
const (
	ScopeUpcoming = "upcoming" // pending and running, soonest first
	ScopeRecent   = "recent"   // finished, newest first
)

// List returns the shop's schedules (at most limit).
func (s *Scheduler) List(ctx context.Context, shopID int, scope string, limit int) ([]*ent.ContentSchedule, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	q := s.Client.ContentSchedule.Query().Where(entsched.ShopIDEQ(shopID))
	if scope == ScopeRecent {
		q = q.Where(entsched.StatusNotIn(contentstore.SchedPending, contentstore.SchedRunning)).
			Order(ent.Desc(entsched.FieldFinishedAt), ent.Desc(entsched.FieldID))
	} else {
		q = q.Where(entsched.StatusIn(contentstore.SchedPending, contentstore.SchedRunning)).
			Order(ent.Asc(entsched.FieldPublishAt), ent.Asc(entsched.FieldID))
	}
	return q.Limit(limit).All(ctx)
}

// PendingByKey maps document keys to the publish_at of their pending schedule
// (for the document list).
func (s *Scheduler) PendingByKey(ctx context.Context, shopID int) (map[string]time.Time, error) {
	rows, err := s.Client.ContentSchedule.Query().
		Where(entsched.ShopIDEQ(shopID), entsched.StatusIn(contentstore.SchedPending, contentstore.SchedRunning)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(rows))
	for _, r := range rows {
		out[r.DocKey] = r.PublishAt
	}
	return out, nil
}

// ── runner ────────────────────────────────────────────────────────────────

// RunReport counts what one run did.
type RunReport struct {
	// Skipped: another scheduler holds the advisory lock; nothing was done.
	Skipped          bool
	Due              int
	Published        int
	AlreadyPublished int
	Failed           int
	Missed           int
	Superseded       int
	Cancelled        int
	Retry            int // infrastructure failure: left pending for the next run
}

// RunOnce publishes the schedules that are due. It is safe to call from
// several processes at once: a Postgres advisory lock makes the second run
// skip, and every schedule is claimed with a conditional UPDATE besides.
func (s *Scheduler) RunOnce(ctx context.Context) (RunReport, error) {
	var rep RunReport
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return rep, err
	}
	defer func() { _ = tx.Rollback() }()
	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, advisoryLockKey).Scan(&locked); err != nil {
		return rep, err
	}
	if !locked {
		rep.Skipped = true
		return rep, nil
	}
	now, err := s.Clock.Now(ctx)
	if err != nil {
		return rep, err
	}
	cfg := s.cfg()
	if err := s.recoverStuck(ctx, now, cfg); err != nil {
		return rep, err
	}
	due, err := s.Client.ContentSchedule.Query().
		Where(entsched.StatusEQ(contentstore.SchedPending), entsched.PublishAtLTE(now)).
		Order(ent.Asc(entsched.FieldPublishAt), ent.Asc(entsched.FieldID)).Limit(cfg.BatchSize).All(ctx)
	if err != nil {
		return rep, err
	}
	rep.Due = len(due)
	for _, sc := range due {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		s.process(ctx, sc, now, cfg, &rep)
	}
	return rep, tx.Commit()
}

// recoverStuck resets claims whose run crashed. Publishing is idempotent
// (process notices an already published draft), so re-running is safe.
func (s *Scheduler) recoverStuck(ctx context.Context, now time.Time, cfg ScheduleConfig) error {
	_, err := s.Client.ContentSchedule.Update().
		Where(entsched.StatusEQ(contentstore.SchedRunning), entsched.ClaimedAtLT(now.Add(-cfg.RunningTimeout))).
		SetStatus(contentstore.SchedPending).ClearClaimedAt().Save(ctx)
	return err
}

func (s *Scheduler) finish(ctx context.Context, sc *ent.ContentSchedule, status, result, action string, now time.Time, detail map[string]any, failure json.RawMessage) {
	up := s.Client.ContentSchedule.UpdateOneID(sc.ID).SetStatus(status).SetResult(result).SetFinishedAt(now)
	if len(failure) > 0 {
		up.SetFailure(failure)
	}
	if late := int(now.Sub(sc.PublishAt).Seconds()); late > 0 {
		up.SetLateSeconds(late)
	}
	if _, err := up.Save(ctx); err != nil {
		s.log().Error("content schedule: finish failed", "schedule_id", sc.ID, "err_class", "db")
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["schedule_id"] = sc.ID
	detail["result"] = result
	detail["draft_rev"] = sc.DraftRev
	if action != "" {
		actor := 0
		if sc.CreatedByUserID != nil {
			actor = *sc.CreatedByUserID
		}
		_ = contentstore.Audit(ctx, s.Client, contentstore.AuditEntry{
			ShopID: sc.ShopID, Actor: actor, Action: action, Key: sc.DocKey, Detail: detail,
		})
	}
	s.log().Info("content schedule", "schedule_id", sc.ID, "shop_id", sc.ShopID, "doc_key", sc.DocKey, "status", status, "result", result)
}

func (s *Scheduler) process(ctx context.Context, sc *ent.ContentSchedule, now time.Time, cfg ScheduleConfig, rep *RunReport) {
	n, err := s.Client.ContentSchedule.Update().
		Where(entsched.IDEQ(sc.ID), entsched.StatusEQ(contentstore.SchedPending)).
		SetStatus(contentstore.SchedRunning).SetClaimedAt(now).AddAttempts(1).Save(ctx)
	if err != nil {
		s.log().Error("content schedule: claim failed", "schedule_id", sc.ID, "err_class", "db")
		rep.Retry++
		return
	}
	if n == 0 {
		return // taken by another scheduler
	}
	sc.Attempts++
	actor := 0
	if sc.CreatedByUserID != nil {
		actor = *sc.CreatedByUserID
	}

	if now.Sub(sc.PublishAt) > cfg.Grace {
		s.finish(ctx, sc, contentstore.SchedMissed, contentstore.ResultMissed, contentstore.AuditScheduleMissed, now,
			map[string]any{"late_seconds": int(now.Sub(sc.PublishAt).Seconds())}, nil)
		rep.Missed++
		return
	}
	d, err := contentstore.Get(ctx, s.Client, sc.ShopID, sc.DocKey)
	if errors.Is(err, contentstore.ErrNotFound) {
		s.finish(ctx, sc, contentstore.SchedCancelled, contentstore.ResultDocumentDeleted, contentstore.AuditScheduleCancel, now, nil, nil)
		rep.Cancelled++
		return
	}
	if err != nil {
		s.retryOrFail(ctx, sc, now, cfg, rep)
		return
	}
	if d.DraftRev != sc.DraftRev {
		s.finish(ctx, sc, contentstore.SchedSuperseded, contentstore.ResultEdited, contentstore.AuditScheduleSuperseded, now,
			map[string]any{"current_rev": d.DraftRev}, nil)
		rep.Superseded++
		return
	}
	if d.Status == 1 && d.PublishedRev == sc.DraftRev {
		s.finish(ctx, sc, contentstore.SchedDone, contentstore.ResultAlreadyPublished, contentstore.AuditScheduleDone, now, nil, nil)
		rep.AlreadyPublished++
		return
	}
	if s.Authorize != nil && !s.Authorize(ctx, actor, sc.ShopID) {
		s.finish(ctx, sc, contentstore.SchedFailed, contentstore.ResultPermissionRevoked, contentstore.AuditScheduleFailed, now, nil, nil)
		rep.Failed++
		return
	}
	_, err = s.Docs.PublishScheduled(ctx, sc.ShopID, sc.DocKey, sc.DraftRev, actor, sc.ID)
	var ve *cms.ValidationError
	var ce *ConflictError
	switch {
	case err == nil:
		s.finish(ctx, sc, contentstore.SchedDone, contentstore.ResultPublished, contentstore.AuditScheduleDone, now, nil, nil)
		rep.Published++
	case errors.As(err, &ve):
		// The draft no longer passes the current schema (or the shop's budget):
		// nothing is published, the document is flagged, the schedule fails
		// visibly. Fixing the draft and scheduling again is a human decision.
		_ = contentstore.SetFlags(ctx, s.Client, sc.ShopID, sc.DocKey, true, d.Declared)
		s.finish(ctx, sc, contentstore.SchedFailed, contentstore.ResultInvalid, contentstore.AuditScheduleFailed, now, nil, pointersOf(ve))
		rep.Failed++
	case errors.As(err, &ce):
		s.finish(ctx, sc, contentstore.SchedSuperseded, contentstore.ResultEdited, contentstore.AuditScheduleSuperseded, now,
			map[string]any{"current_rev": ce.CurrentRev}, nil)
		rep.Superseded++
	case errors.Is(err, ErrNotFound):
		s.finish(ctx, sc, contentstore.SchedCancelled, contentstore.ResultDocumentDeleted, contentstore.AuditScheduleCancel, now, nil, nil)
		rep.Cancelled++
	default:
		s.retryOrFail(ctx, sc, now, cfg, rep)
	}
}

// retryOrFail handles infrastructure errors: the schedule goes back to pending
// for the next run, until MaxAttempts.
func (s *Scheduler) retryOrFail(ctx context.Context, sc *ent.ContentSchedule, now time.Time, cfg ScheduleConfig, rep *RunReport) {
	if sc.Attempts >= cfg.MaxAttempts {
		s.finish(ctx, sc, contentstore.SchedFailed, contentstore.ResultError, contentstore.AuditScheduleFailed, now,
			map[string]any{"attempts": sc.Attempts}, nil)
		rep.Failed++
		return
	}
	_, _ = s.Client.ContentSchedule.UpdateOneID(sc.ID).SetStatus(contentstore.SchedPending).ClearClaimedAt().Save(ctx)
	s.log().Warn("content schedule: will retry", "schedule_id", sc.ID, "attempts", sc.Attempts)
	rep.Retry++
}

func pointersOf(ve *cms.ValidationError) json.RawMessage {
	ptrs := make([]string, 0, 5)
	for _, d := range ve.Details {
		if d.Pointer != "" && len(ptrs) < 5 {
			ptrs = append(ptrs, d.Pointer)
		}
	}
	raw, _ := json.Marshal(map[string]any{"pointers": ptrs})
	return raw
}

// Loop runs RunOnce every interval until ctx is done (the optional in-process
// ticker of the API server). Errors are logged and the loop continues.
func (s *Scheduler) Loop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rep, err := s.RunOnce(ctx)
			if err != nil {
				s.log().Warn("content scheduler run failed", "err_class", "run")
				continue
			}
			if rep.Due > 0 {
				s.log().Info("content scheduler run", "due", rep.Due, "published", rep.Published, "failed", rep.Failed, "missed", rep.Missed)
			}
		}
	}
}
