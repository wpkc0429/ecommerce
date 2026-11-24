package application

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/shopapplication"
	"ksdevworks/ecommerce/api/internal/ent/shopapplicationevent"
	"ksdevworks/ecommerce/api/internal/ent/shopapplicationmessage"
	"ksdevworks/ecommerce/api/internal/ent/user"
	"ksdevworks/ecommerce/api/internal/provision"
)

// ListFilter selects applications for the review queue.
type ListFilter struct {
	Status string
	Q      string
	Page   int
	Size   int
}

// ListResult is one page plus queue counters for the admin badge.
type ListResult struct {
	Items []*ent.ShopApplication
	Total int
	// Counts by status over the whole table; PendingMessages is the number of
	// outbox messages an operator still has to hand over (manual or failed).
	Counts          map[string]int
	PendingMessages int
}

// List returns applications newest first.
func (s *Service) List(ctx context.Context, f ListFilter) (*ListResult, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size < 1 || f.Size > 100 {
		f.Size = 20
	}
	q := s.Client.ShopApplication.Query()
	if f.Status != "" {
		q = q.Where(shopapplication.StatusEQ(f.Status))
	}
	if t := strings.TrimSpace(f.Q); t != "" {
		q = q.Where(shopapplication.Or(
			shopapplication.EmailContainsFold(t),
			shopapplication.ShopNameContainsFold(t),
			shopapplication.ContactNameContainsFold(t),
		))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, err
	}
	items, err := q.Order(ent.Desc(shopapplication.FieldID)).Offset((f.Page - 1) * f.Size).Limit(f.Size).All(ctx)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: items, Total: total, Counts: map[string]int{}}
	var rows []struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
	}
	if err := s.Client.ShopApplication.Query().GroupBy(shopapplication.FieldStatus).
		Aggregate(ent.Count()).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	for _, r := range rows {
		res.Counts[r.Status] = r.Count
	}
	res.PendingMessages, err = s.Client.ShopApplicationMessage.Query().
		Where(shopapplicationmessage.StatusIn("pending_manual", "failed")).Count(ctx)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// Detail is one application with its audit timeline and outbox.
type Detail struct {
	App             *ent.ShopApplication
	Events          []*ent.ShopApplicationEvent
	Messages        []*ent.ShopApplicationMessage
	EmailHasAccount bool
}

// Get loads the review view of one application.
func (s *Service) Get(ctx context.Context, id int) (*Detail, error) {
	app, err := s.Client.ShopApplication.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d := &Detail{App: app}
	d.Events, err = s.Client.ShopApplicationEvent.Query().
		Where(shopapplicationevent.ApplicationIDEQ(id)).Order(ent.Asc(shopapplicationevent.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	d.Messages, err = s.Client.ShopApplicationMessage.Query().
		Where(shopapplicationmessage.ApplicationIDEQ(id)).Order(ent.Asc(shopapplicationmessage.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	if app.Email != "" {
		d.EmailHasAccount, err = s.Client.User.Query().Where(user.EmailEQ(app.Email)).Exist(ctx)
		if err != nil {
			return nil, err
		}
	}
	return d, nil
}

// VerifyManually lets an operator confirm the applicant's contact channel when
// e-mail is not working (design D5). Audited and flagged.
func (s *Service) VerifyManually(ctx context.Context, id, actor int) error {
	now := s.now()
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return err
	}
	app, err := tx.ShopApplication.Query().Where(shopapplication.IDEQ(id)).ForUpdate().Only(ctx)
	if err != nil {
		_ = tx.Rollback()
		if ent.IsNotFound(err) {
			return ErrNotFound
		}
		return err
	}
	flags := map[string]bool{}
	for k, v := range app.RiskFlags {
		flags[k] = v
	}
	flags[FlagManuallyVerified] = true
	n, err := tx.ShopApplication.Update().
		Where(shopapplication.IDEQ(id), shopapplication.StatusEQ(StatusPendingVerification)).
		SetStatus(StatusPendingReview).SetEmailVerifiedAt(now).SetRiskFlags(flags).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if n == 0 {
		_ = tx.Rollback()
		return conflict("application is not waiting for verification")
	}
	if err := addEvent(ctx, tx.Client(), id, &actor, EventVerifiedManually, nil); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Reject declines an application. reason is shown to the applicant (it is also
// put in the notification); note is internal.
func (s *Service) Reject(ctx context.Context, id, actor int, reason, note string) error {
	reason = strings.TrimSpace(reason)
	note = strings.TrimSpace(note)
	if n := utf8.RuneCountInString(reason); n == 0 || n > MaxReasonLen {
		return invalid("/decision_reason", "required, at most 1000 characters; the applicant sees it")
	}
	if utf8.RuneCountInString(note) > MaxNoteLen {
		return invalid("/internal_note", "at most 2000 characters")
	}
	now := s.now()
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return err
	}
	n, err := tx.ShopApplication.Update().
		Where(shopapplication.IDEQ(id), shopapplication.StatusIn(StatusPendingVerification, StatusPendingReview)).
		SetStatus(StatusRejected).SetReviewedBy(actor).SetReviewedAt(now).
		SetDecisionReason(reason).SetInternalNote(note).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if n == 0 {
		_ = tx.Rollback()
		return s.notDecidable(ctx, id)
	}
	app, err := tx.ShopApplication.Get(ctx, id)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	d, err := s.Notifier.Notify(ctx, tx.Client(), Notification{
		ApplicationID: id, Kind: KindRejected, To: app.Email,
		Subject: "關於您的開店申請",
		Body: fmt.Sprintf("您好，\n\n很抱歉，您為「%s」提出的開店申請這次無法通過。\n\n原因：%s\n\n您可在 %d 天後重新申請。\n",
			app.ShopName, reason, int(s.Cfg.ReapplyCooldown.Hours()/24)),
	})
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := addEvent(ctx, tx.Client(), id, &actor, EventRejected, map[string]any{"delivery": d.Mode}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.Notifier.AfterCommit(ctx, d)
	return nil
}

// notDecidable explains why a conditional transition matched nothing.
func (s *Service) notDecidable(ctx context.Context, id int) error {
	app, err := s.Client.ShopApplication.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrNotFound
		}
		return err
	}
	return conflict("application is " + app.Status)
}

// ApproveInput are the operator's choices at approval.
type ApproveInput struct {
	// ShopName overrides the applicant's shop name when non-empty.
	ShopName string
	// Status is the new shop's status; nil means 2 (under review: the
	// storefront is unreachable until the operator flips it to 1).
	Status                 *int16
	ConfirmExistingAccount bool
}

// ApproveResult is the outcome of an approval.
type ApproveResult struct {
	App       *ent.ShopApplication
	Provision *provision.Result
}

const defaultShopStatus int16 = 2

// Approve opens the shop for a verified application in ONE transaction:
// pending_review -> approved (conditional), provision.ProvisionShopTx,
// provisioned_shop_id and the audit event. Any failure rolls everything back
// and leaves the application pending_review, with an approve_failed event
// recorded outside the transaction. The owner's setup link is returned in the
// result only; it is written nowhere.
//
// Anti-takeover: an owner e-mail that already belongs to a back-office account
// is only ever ADDED as a member (ProvisionShopTx never touches an existing
// password) and only with confirm_existing_account.
func (s *Service) Approve(ctx context.Context, id, actor int, in ApproveInput) (*ApproveResult, error) {
	status := defaultShopStatus
	if in.Status != nil {
		status = *in.Status
	}
	now := s.now()
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	rollback := func() { _ = tx.Rollback() }

	app, err := tx.ShopApplication.Query().Where(shopapplication.IDEQ(id)).ForUpdate().Only(ctx)
	if err != nil {
		rollback()
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	switch {
	case app.Status == StatusPendingVerification || (app.Status == StatusPendingReview && app.EmailVerifiedAt == nil):
		rollback()
		return nil, conflict("the applicant's e-mail is not verified yet")
	case app.Status != StatusPendingReview:
		rollback()
		return nil, conflict("application is " + app.Status)
	}
	exists, err := tx.User.Query().Where(user.EmailEQ(app.Email)).Exist(ctx)
	if err != nil {
		rollback()
		return nil, err
	}
	if exists && !in.ConfirmExistingAccount {
		rollback()
		return nil, ErrExistingAccount
	}

	// The conditional UPDATE is the arbiter even though the row is locked.
	n, err := tx.ShopApplication.Update().
		Where(shopapplication.IDEQ(id), shopapplication.StatusEQ(StatusPendingReview), shopapplication.EmailVerifiedAtNotNil()).
		SetStatus(StatusApproved).SetReviewedBy(actor).SetReviewedAt(now).Save(ctx)
	if err != nil || n == 0 {
		rollback()
		if err != nil {
			return nil, err
		}
		return nil, conflict("application can no longer be approved")
	}

	name := strings.TrimSpace(in.ShopName)
	if name == "" {
		name = app.ShopName
	}
	res, issued, err := s.Provision.ProvisionShopTx(ctx, tx, provision.Input{
		Name: name, Status: &status, OwnerEmail: app.Email, ActorID: actor,
	})
	if err != nil {
		rollback()
		s.recordApproveFailure(ctx, id, actor)
		return nil, err
	}
	if _, err := tx.ShopApplication.UpdateOneID(id).SetProvisionedShopID(res.Shop.ID).Save(ctx); err != nil {
		rollback()
		s.recordApproveFailure(ctx, id, actor)
		return nil, err
	}
	d, err := s.Notifier.Notify(ctx, tx.Client(), Notification{
		ApplicationID: id, Kind: KindApproved, To: app.Email,
		Subject: "您的開店申請已核准",
		Body: fmt.Sprintf("您好，\n\n您為「%s」提出的開店申請已核准。我們會另行與您聯絡後續的設定與上線步驟；您的後台帳號密碼設定連結會由我們以另外的管道交給您。\n",
			name),
	})
	if err != nil {
		rollback()
		s.recordApproveFailure(ctx, id, actor)
		return nil, err
	}
	if err := addEvent(ctx, tx.Client(), id, &actor, EventApproved, map[string]any{
		"shop_id": res.Shop.ID, "owner_created": res.Created, "delivery": d.Mode,
	}); err != nil {
		rollback()
		s.recordApproveFailure(ctx, id, actor)
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		s.recordApproveFailure(ctx, id, actor)
		return nil, fmt.Errorf("application: commit: %w", err)
	}
	if err := s.Provision.AfterCommit(ctx, res, issued); err != nil {
		// The shop exists and the application is approved; only the link
		// hand-over failed. Surface it: the operator must issue a reset link.
		return nil, err
	}
	s.Notifier.AfterCommit(ctx, d)
	app, err = s.Client.ShopApplication.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &ApproveResult{App: app, Provision: res}, nil
}

func (s *Service) recordApproveFailure(ctx context.Context, id, actor int) {
	if err := addEvent(context.WithoutCancel(ctx), s.Client, id, &actor, EventApproveFailed, nil); err != nil {
		s.logWarn("application: record approve_failed", "application_id", id, "err", err)
	}
}

// MarkMessageSent records that an operator handed an outbox message over by
// other means (or that a failed/queued send was covered manually).
func (s *Service) MarkMessageSent(ctx context.Context, appID, msgID, actor int) error {
	now := s.now()
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return err
	}
	n, err := tx.ShopApplicationMessage.Update().
		Where(
			shopapplicationmessage.IDEQ(msgID), shopapplicationmessage.ApplicationIDEQ(appID),
			shopapplicationmessage.StatusIn("pending_manual", "queued", "failed"),
		).
		SetStatus("sent").SetSentAt(now).SetMarkedBy(actor).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if n == 0 {
		_ = tx.Rollback()
		exists, qerr := s.Client.ShopApplicationMessage.Query().
			Where(shopapplicationmessage.IDEQ(msgID), shopapplicationmessage.ApplicationIDEQ(appID)).Exist(ctx)
		if qerr != nil {
			return qerr
		}
		if !exists {
			return ErrNotFound
		}
		return conflict("message is already marked sent")
	}
	if err := addEvent(ctx, tx.Client(), appID, &actor, EventMessageMarkedSent, map[string]any{"message_id": msgID}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
