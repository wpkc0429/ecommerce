package application

import (
	"context"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/shopapplication"
	"ksdevworks/ecommerce/api/internal/ent/shopapplicationmessage"
)

// PurgeReport lists what a purge run did or, in a dry run, would do. Ids only:
// the report itself must not become a place PII leaks to.
type PurgeReport struct {
	// Deleted: unverified applications removed entirely (with their tokens and
	// outbox messages through ON DELETE CASCADE).
	Deleted []int
	// Blanked: decided applications whose PII columns (and message bodies and
	// recipients) were cleared. The row, its status and the audit events stay.
	Blanked []int
}

// Purge applies the retention policy (design D8). It is idempotent: a blanked
// application is skipped (purged_at set) and a deleted one is gone. With apply
// false it only reports.
//
//   - pending_verification older than UnverifiedTTL: deleted.
//   - rejected / expired / withdrawn: blanked after
//     max(RejectedRetention, ReapplyCooldown) from the decision, so the cooldown
//     can still be enforced for as long as it runs.
//   - approved: blanked after ApprovedRetention.
//   - pending_review never ages out: a human must decide it.
func (s *Service) Purge(ctx context.Context, apply bool) (*PurgeReport, error) {
	now := s.now()
	rep := &PurgeReport{}

	rejectedAfter := s.Cfg.RejectedRetention
	if s.Cfg.ReapplyCooldown > rejectedAfter {
		rejectedAfter = s.Cfg.ReapplyCooldown
	}

	stale, err := s.Client.ShopApplication.Query().
		Where(shopapplication.StatusEQ(StatusPendingVerification), shopapplication.CreatedAtLT(now.Add(-UnverifiedTTL))).
		IDs(ctx)
	if err != nil {
		return nil, err
	}
	rep.Deleted = stale

	decidedRejected, err := s.Client.ShopApplication.Query().
		Where(
			shopapplication.StatusIn(StatusRejected, StatusExpired, StatusWithdrawn),
			shopapplication.PurgedAtIsNil(),
			// updated_at is the decision time for expired/withdrawn rows that
			// have no reviewed_at.
			shopapplication.Or(
				shopapplication.And(shopapplication.ReviewedAtNotNil(), shopapplication.ReviewedAtLT(now.Add(-rejectedAfter))),
				shopapplication.And(shopapplication.ReviewedAtIsNil(), shopapplication.UpdatedAtLT(now.Add(-rejectedAfter))),
			),
		).IDs(ctx)
	if err != nil {
		return nil, err
	}
	decidedApproved, err := s.Client.ShopApplication.Query().
		Where(
			shopapplication.StatusEQ(StatusApproved), shopapplication.PurgedAtIsNil(),
			shopapplication.ReviewedAtLT(now.Add(-s.Cfg.ApprovedRetention)),
		).IDs(ctx)
	if err != nil {
		return nil, err
	}
	rep.Blanked = append(append([]int{}, decidedRejected...), decidedApproved...)

	if !apply {
		return rep, nil
	}
	for _, id := range rep.Deleted {
		if err := s.deleteApplication(ctx, id, "unverified_expired"); err != nil {
			return rep, err
		}
	}
	for _, id := range rep.Blanked {
		if err := s.blank(ctx, id, "retention"); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// PurgeEmail removes one person's data on request (privacy request). Open
// applications are deleted, decided ones blanked, regardless of retention.
func (s *Service) PurgeEmail(ctx context.Context, email string, apply bool) (*PurgeReport, error) {
	norm, ok := auth.NormalizeEmail(email)
	if !ok {
		return nil, invalid("/email", "must be a valid e-mail address")
	}
	rep := &PurgeReport{}
	apps, err := s.Client.ShopApplication.Query().Where(shopapplication.EmailEQ(norm)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range apps {
		switch a.Status {
		case StatusPendingVerification, StatusPendingReview:
			rep.Deleted = append(rep.Deleted, a.ID)
		default:
			rep.Blanked = append(rep.Blanked, a.ID)
		}
	}
	if !apply {
		return rep, nil
	}
	for _, id := range rep.Deleted {
		if err := s.deleteApplication(ctx, id, "subject_request"); err != nil {
			return rep, err
		}
	}
	for _, id := range rep.Blanked {
		if err := s.blank(ctx, id, "subject_request"); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func (s *Service) deleteApplication(ctx context.Context, id int, reason string) error {
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return err
	}
	if err := addEvent(ctx, tx.Client(), id, nil, EventExpired, map[string]any{"reason": reason}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := addEvent(ctx, tx.Client(), id, nil, EventPurged, map[string]any{"reason": reason, "mode": "deleted"}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.ShopApplication.DeleteOneID(id).Exec(ctx); err != nil && !ent.IsNotFound(err) {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Service) blank(ctx context.Context, id int, reason string) error {
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return err
	}
	n, err := tx.ShopApplication.Update().
		Where(shopapplication.IDEQ(id), shopapplication.PurgedAtIsNil()).
		SetEmail("").SetContactName("").SetPhone("").SetShopName("").SetBusinessSummary("").
		SetDecisionReason("").SetInternalNote("").SetSubmittedIPHmac("").
		SetPurgedAt(s.now()).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if n == 0 {
		_ = tx.Rollback() // already blanked by an earlier run
		return nil
	}
	if _, err := tx.ShopApplicationMessage.Update().
		Where(shopapplicationmessage.ApplicationIDEQ(id)).
		SetRecipientEmail("").SetBodyText("").Save(ctx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := addEvent(ctx, tx.Client(), id, nil, EventPurged, map[string]any{"reason": reason, "mode": "blanked"}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
