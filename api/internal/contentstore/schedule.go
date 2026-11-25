package contentstore

import (
	"context"
	"time"

	"ksdevworks/ecommerce/api/internal/ent"
	entsched "ksdevworks/ecommerce/api/internal/ent/contentschedule"
	"ksdevworks/ecommerce/api/internal/ent/predicate"
)

// Schedule statuses and results (design D11).
const (
	SchedPending    = "pending"
	SchedRunning    = "running"
	SchedDone       = "done"
	SchedFailed     = "failed"
	SchedMissed     = "missed"
	SchedCancelled  = "cancelled"
	SchedSuperseded = "superseded"

	ResultPublished         = "published"
	ResultAlreadyPublished  = "already_published"
	ResultInvalid           = "invalid"
	ResultPermissionRevoked = "permission_revoked"
	ResultEdited            = "edited"
	ResultPublishedManually = "published_manually"
	ResultDocumentDeleted   = "document_deleted"
	ResultCancelled         = "cancelled"
	ResultMissed            = "missed"
	ResultError             = "error"
)

// Audit actions of schedules.
const (
	AuditScheduleSet        = "doc.schedule_set"
	AuditScheduleCancel     = "doc.schedule_cancel"
	AuditScheduleDone       = "doc.schedule_done"
	AuditScheduleFailed     = "doc.schedule_failed"
	AuditScheduleMissed     = "doc.schedule_missed"
	AuditScheduleSuperseded = "doc.schedule_superseded"
)

// finishPending closes the PENDING schedules of a document that match preds
// (a claimed `running` schedule belongs to the scheduler and is left alone) and
// writes one audit row per schedule. It runs in the caller's transaction, so a
// schedule is invalidated atomically with the write that invalidated it.
func finishPending(ctx context.Context, c *ent.Client, shopID int, key, status, result, action string, actor int, extra ...predicate.ContentSchedule) (int, error) {
	preds := append([]predicate.ContentSchedule{
		entsched.ShopIDEQ(shopID), entsched.DocKeyEQ(key), entsched.StatusEQ(SchedPending),
	}, extra...)
	rows, err := c.ContentSchedule.Query().Where(preds...).All(ctx)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	now := time.Now()
	for _, r := range rows {
		n, err := c.ContentSchedule.Update().
			Where(entsched.IDEQ(r.ID), entsched.StatusEQ(SchedPending)).
			SetStatus(status).SetResult(result).SetFinishedAt(now).Save(ctx)
		if err != nil {
			return 0, err
		}
		if n == 0 {
			continue
		}
		if err := Audit(ctx, c, AuditEntry{
			ShopID: shopID, Actor: actor, Action: action, Key: key,
			Detail: map[string]any{"schedule_id": r.ID, "result": result, "draft_rev": r.DraftRev},
		}); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

// SupersedeEdited invalidates the pending schedules of a document whose draft
// moved on (draft_rev != the rev they were bound to).
func SupersedeEdited(ctx context.Context, c *ent.Client, shopID int, key string, newRev int64, actor int) error {
	_, err := finishPending(ctx, c, shopID, key, SchedSuperseded, ResultEdited, AuditScheduleSuperseded, actor,
		entsched.DraftRevNEQ(newRev))
	return err
}

// SupersedePublished invalidates the pending schedules bound to the draft rev
// that was just published by hand.
func SupersedePublished(ctx context.Context, c *ent.Client, shopID int, key string, publishedRev int64, actor int) error {
	_, err := finishPending(ctx, c, shopID, key, SchedSuperseded, ResultPublishedManually, AuditScheduleSuperseded, actor,
		entsched.DraftRevEQ(publishedRev))
	return err
}

// CancelForDeleted cancels the pending schedules of a deleted document.
func CancelForDeleted(ctx context.Context, c *ent.Client, shopID int, key string, actor int) error {
	_, err := finishPending(ctx, c, shopID, key, SchedCancelled, ResultDocumentDeleted, AuditScheduleCancel, actor)
	return err
}
