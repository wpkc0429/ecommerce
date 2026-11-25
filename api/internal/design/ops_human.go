package design

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/designrequest"
	"ksdevworks/ecommerce/api/internal/ent/designrequestevent"
	"ksdevworks/ecommerce/api/internal/ent/designsitestate"
	"ksdevworks/ecommerce/api/internal/ent/designuntrackedchange"
	"ksdevworks/ecommerce/api/internal/ent/predicate"
)

// This file holds what a person does through the admin routes: reading the
// history, approving, rejecting, asking for changes, cancelling and reverting.

// ListFilter filters the request list.
type ListFilter struct {
	Site, Status, Kind string
	From, To           *time.Time // created_at in [From, To)
	Page, PageSize     int
}

// List returns one page of the shop's requests (newest first) and the total.
func (s *Service) List(ctx context.Context, shopID int, f ListFilter) ([]*ent.DesignRequest, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 100 {
		f.PageSize = 20
	}
	preds := []predicate.DesignRequest{designrequest.ShopIDEQ(shopID)}
	if f.Site != "" {
		preds = append(preds, designrequest.SiteEQ(f.Site))
	}
	if f.Status != "" {
		preds = append(preds, designrequest.StatusEQ(f.Status))
	}
	if f.Kind != "" {
		preds = append(preds, designrequest.KindEQ(f.Kind))
	}
	if f.From != nil {
		preds = append(preds, designrequest.CreatedAtGTE(*f.From))
	}
	if f.To != nil {
		preds = append(preds, designrequest.CreatedAtLT(*f.To))
	}
	total, err := s.Client.DesignRequest.Query().Where(preds...).Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.Client.DesignRequest.Query().Where(preds...).
		Order(ent.Desc(designrequest.FieldCreatedAt), ent.Asc(designrequest.FieldID)).
		Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).All(ctx)
	return rows, total, err
}

// Events returns one page of a request's events (seq ascending).
func (s *Service) Events(ctx context.Context, shopID int, id uuid.UUID, page, size int) ([]*ent.DesignRequestEvent, int, error) {
	if _, err := s.load(ctx, shopID, id); err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 50
	}
	q := s.Client.DesignRequestEvent.Query().Where(designrequestevent.RequestIDEQ(id), designrequestevent.ShopIDEQ(shopID))
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Order(ent.Asc(designrequestevent.FieldSeq)).Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, total, err
}

// ApproveResult carries the outcome of an approval.
type ApproveResult struct {
	Request *ent.DesignRequest
	Replay  bool
}

// Approve is the human approval, bound to the head sha the person reviewed and
// to the revision (ETag) they saw. It is the only path to `approved`.
func (s *Service) Approve(ctx context.Context, shopID int, id uuid.UUID, userID int, headSHA string, ifMatchRev int, note string) (*ApproveResult, error) {
	if !shaRE.MatchString(headSHA) {
		return nil, ErrInvalid("head_sha must be a 40-character lowercase hex SHA")
	}
	note = SanitizeText(note)
	if utf8.RuneCountInString(note) > 500 {
		return nil, ErrInvalid("note must be at most 500 characters")
	}
	for attempt := 0; attempt < 5; attempt++ {
		cur, err := s.load(ctx, shopID, id)
		if err != nil {
			return nil, err
		}
		// A retry of the same approval (a lost response) is answered with the
		// same result and writes nothing.
		if cur.Status == StatusApproved && cur.ApprovedByUserID != nil && *cur.ApprovedByUserID == userID &&
			cur.ApprovedHeadSha != nil && *cur.ApprovedHeadSha == headSHA {
			return &ApproveResult{Request: cur, Replay: true}, nil
		}
		if cur.Status != StatusAwaitingApproval {
			return nil, ErrInvalidState(fmt.Sprintf("only a request awaiting approval can be approved; this one is %s", cur.Status))
		}
		if cur.CandidateSha == nil || *cur.CandidateSha != headSHA || cur.HeadSha != headSHA {
			return nil, conflict("head_moved", "the head changed since the request was submitted; review it again")
		}
		if cur.Rev != ifMatchRev {
			return nil, newErr(http.StatusPreconditionFailed, "precondition_failed", "the request changed since you loaded it")
		}
		now := s.now()
		tag, err := s.nextTag(ctx, shopID, cur.Site, now)
		if err != nil {
			return nil, err
		}
		var out *ent.DesignRequest
		err = s.inTx(ctx, func(tx *ent.Tx) error {
			row, err := s.step(ctx, tx, step{
				shopID: shopID, id: id, from: []string{StatusAwaitingApproval}, to: StatusApproved, kind: EvApproved,
				actor: UserActor(userID), headSHA: headSHA,
				detail: map[string]any{"publish_tag": tag, "note_length": utf8.RuneCountInString(note)},
				extra: []predicate.DesignRequest{
					designrequest.CandidateShaEQ(headSHA), designrequest.HeadShaEQ(headSHA), designrequest.RevEQ(ifMatchRev),
				},
				set: func(u *ent.DesignRequestUpdate) {
					u.SetApprovedByUserID(userID).SetApprovedAt(now).SetApprovedHeadSha(headSHA).
						SetApprovalExpiresAt(now.Add(s.Cfg.ApprovalTTL)).SetPublishTag(tag)
				},
			})
			out = row
			return err
		})
		switch {
		case err == nil:
			return &ApproveResult{Request: out}, nil
		case isUnique(err, "design_requests_publish_tag_key"):
			continue // another approval took the tag number; take the next one
		case err == errStale:
			// Lost a race with another approval or edit: report what is true now.
			now2, lerr := s.load(ctx, shopID, id)
			if lerr != nil {
				return nil, lerr
			}
			if now2.Status == StatusApproved && now2.ApprovedByUserID != nil && *now2.ApprovedByUserID == userID &&
				now2.ApprovedHeadSha != nil && *now2.ApprovedHeadSha == headSHA {
				return &ApproveResult{Request: now2, Replay: true}, nil
			}
			if now2.Status != StatusAwaitingApproval {
				return nil, ErrInvalidState(fmt.Sprintf("only a request awaiting approval can be approved; this one is %s", now2.Status))
			}
			if now2.CandidateSha == nil || *now2.CandidateSha != headSHA {
				return nil, conflict("head_moved", "the head changed since the request was submitted; review it again")
			}
			return nil, newErr(http.StatusPreconditionFailed, "precondition_failed", "the request changed since you loaded it")
		default:
			return nil, err
		}
	}
	return nil, conflict("tag_conflict", "could not allocate a publish tag; retry")
}

// nextTag issues design-{site}-{YYYYMMDD}-{N} (UTC date); the unique index on
// (shop_id, publish_tag) is what makes N collision-free.
func (s *Service) nextTag(ctx context.Context, shopID int, site string, now time.Time) (string, error) {
	prefix := fmt.Sprintf("design-%s-%s-", site, now.UTC().Format("20060102"))
	rows, err := s.Client.DesignRequest.Query().
		Where(designrequest.ShopIDEQ(shopID), designrequest.PublishTagHasPrefix(prefix)).
		Select(designrequest.FieldPublishTag).Strings(ctx)
	if err != nil {
		return "", err
	}
	maxN := 0
	for _, t := range rows {
		if n, err := strconv.Atoi(strings.TrimPrefix(t, prefix)); err == nil && n > maxN {
			maxN = n
		}
	}
	return prefix + strconv.Itoa(maxN+1), nil
}

// Reject records the owner's rejection of a request awaiting approval.
func (s *Service) Reject(ctx context.Context, shopID int, id uuid.UUID, userID int, reason string) (*ent.DesignRequest, error) {
	reason = SanitizeText(reason)
	if utf8.RuneCountInString(reason) > 500 {
		return nil, ErrInvalid("reason must be at most 500 characters")
	}
	now := s.now()
	var out *ent.DesignRequest
	err := s.inTx(ctx, func(tx *ent.Tx) error {
		row, err := s.step(ctx, tx, step{
			shopID: shopID, id: id, from: []string{StatusAwaitingApproval}, to: StatusRejected, kind: EvRejected,
			actor: UserActor(userID), detail: map[string]any{"reason_length": utf8.RuneCountInString(reason)},
			set: func(u *ent.DesignRequestUpdate) {
				u.SetRejectedByUserID(userID).SetRejectedAt(now)
				if reason != "" {
					u.SetRejectReason(reason)
				}
			},
		})
		out = row
		return err
	})
	if err != nil {
		return nil, s.wrap(ctx, shopID, id, "reject", err)
	}
	return out, nil
}

// RequestChanges sends a request back to drafting with a note for the service;
// the candidate is voided so no earlier review can be approved afterwards.
func (s *Service) RequestChanges(ctx context.Context, shopID int, id uuid.UUID, userID int, note string) (*ent.DesignRequest, error) {
	note = SanitizeText(note)
	if strings.TrimSpace(note) == "" || utf8.RuneCountInString(note) > 500 {
		return nil, ErrInvalid("note is required (at most 500 characters)")
	}
	var out *ent.DesignRequest
	err := s.inTx(ctx, func(tx *ent.Tx) error {
		row, err := s.step(ctx, tx, step{
			shopID: shopID, id: id, from: []string{StatusAwaitingApproval}, to: StatusDrafting, kind: EvChangesRequested,
			actor: UserActor(userID), detail: map[string]any{"note_length": utf8.RuneCountInString(note)},
			set: func(u *ent.DesignRequestUpdate) {
				u.ClearCandidateSha().SetChangeNote(note).ClearPreviewOrigin().ClearPreviewRegisteredAt().ClearPreviewExpiresAt().ClearGuard()
			},
		})
		out = row
		return err
	})
	if err != nil {
		return nil, s.wrap(ctx, shopID, id, "request changes", err)
	}
	return out, nil
}

// Cancel withdraws a non-terminal request (a person's decision).
func (s *Service) Cancel(ctx context.Context, shopID int, id uuid.UUID, userID int) (*ent.DesignRequest, error) {
	var out *ent.DesignRequest
	err := s.inTx(ctx, func(tx *ent.Tx) error {
		row, err := s.step(ctx, tx, step{
			shopID: shopID, id: id, from: ActiveStatuses, to: StatusCancelled, kind: EvCancelled, actor: UserActor(userID),
		})
		out = row
		return err
	})
	if err != nil {
		return nil, s.wrap(ctx, shopID, id, "cancel", err)
	}
	return out, nil
}

// compareVersions compares dotted numeric versions ("0.3.0"); a part that is
// not a number counts as 0. Empty is the lowest.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Revert opens a NEW request that restores the design of a past release. It
// never rewrites history (design D13).
func (s *Service) Revert(ctx context.Context, shopID int, targetID uuid.UUID, userID int, note string) (*ent.DesignRequest, error) {
	note = SanitizeText(note)
	if utf8.RuneCountInString(note) > 500 {
		return nil, ErrInvalid("note must be at most 500 characters")
	}
	target, err := s.load(ctx, shopID, targetID)
	if err != nil {
		return nil, err
	}
	if (target.Status != StatusPublished && target.Status != StatusReverted) || target.PublishedTag == nil {
		return nil, ErrInvalidState("only a request that was published can be restored")
	}
	st, err := s.Client.DesignSiteState.Query().Where(designsitestate.ShopIDEQ(shopID), designsitestate.SiteEQ(target.Site)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, conflict("site_state_unknown", "the site's current design is not recorded yet")
	}
	if err != nil {
		return nil, err
	}
	if st.CurrentRequestID != nil && *st.CurrentRequestID == target.ID {
		return nil, conflict("already_current", "this is already the current design")
	}
	if st.DesignFloorVersion != "" && compareVersions(target.CoreVersion, st.DesignFloorVersion) < 0 {
		return nil, conflict("revert_below_core_floor", "that design predates a core update that required design changes; restoring it would fall back to the built-in screens")
	}
	if !shaRE.MatchString(st.CurrentSha) {
		return nil, conflict("site_state_unknown", "the site's current commit is not recorded yet")
	}
	prompt := "Restore the design published as " + *target.PublishedTag
	if note != "" {
		prompt += ". " + note
	}
	prompt, flags := RedactPII(prompt)
	id := uuid.New()
	var out *ent.DesignRequest
	err = s.inTx(ctx, func(tx *ent.Tx) error {
		now := s.now()
		row, err := tx.DesignRequest.Create().
			SetID(id).SetShopID(shopID).SetSite(target.Site).SetKind(KindRevert).SetStatus(StatusDrafting).
			SetRequestedByUserID(userID).SetPromptText(prompt).SetPromptSha256(sha(prompt)).SetPromptFlags(nonNil(flags)).
			SetBaseSha(st.CurrentSha).SetHeadSha(st.CurrentSha).SetBranch(DefaultBranch(shopID, id)).
			SetCoreVersion(st.CoreVersion).SetDesignFloorVersion(st.DesignFloorVersion).
			SetRestoresRequestID(target.ID).SetRev(1).SetCreatedAt(now).SetUpdatedAt(now).Save(ctx)
		if err != nil {
			return err
		}
		out = row
		if err := s.appendEvent(ctx, tx, row, EvCreated, "", StatusDrafting, UserActor(userID), row.HeadSha,
			map[string]any{"site": row.Site, "kind": KindRevert}, now); err != nil {
			return err
		}
		// The second event needs rev 2: bump it the same way a step does.
		n, err := tx.DesignRequest.Update().Where(designrequest.IDEQ(id), designrequest.ShopIDEQ(shopID)).AddRev(1).Save(ctx)
		if err != nil || n != 1 {
			return fmt.Errorf("design: revert event: %v", err)
		}
		row2, err := tx.DesignRequest.Get(ctx, id)
		if err != nil {
			return err
		}
		out = row2
		return s.appendEvent(ctx, tx, row2, EvRevertRequested, StatusDrafting, StatusDrafting, UserActor(userID), row2.HeadSha,
			map[string]any{"restores_request_id": target.ID.String(), "restores_tag": *target.PublishedTag}, now)
	})
	switch {
	case isUnique(err, "design_requests_one_active"):
		return nil, conflict("request_in_progress", "the site already has a request in progress; finish or cancel it first")
	case err != nil:
		return nil, err
	}
	return out, nil
}

// SiteView is one site's current design plus its open warnings.
type SiteView struct {
	Site      string
	State     *ent.DesignSiteState
	Untracked int
}

// State returns the current design of both sites and the count of untracked
// changes nobody has acknowledged.
func (s *Service) State(ctx context.Context, shopID int) ([]SiteView, error) {
	var out []SiteView
	for _, site := range []string{SiteStorefront, SiteAdmin} {
		v := SiteView{Site: site}
		st, err := s.Client.DesignSiteState.Query().Where(designsitestate.ShopIDEQ(shopID), designsitestate.SiteEQ(site)).Only(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return nil, err
		}
		v.State = st
		v.Untracked, err = s.Client.DesignUntrackedChange.Query().Where(
			designuntrackedchange.ShopIDEQ(shopID), designuntrackedchange.SiteEQ(site), designuntrackedchange.AcknowledgedAtIsNil()).Count(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// ListUntracked returns one page of untracked changes (newest first).
func (s *Service) ListUntracked(ctx context.Context, shopID int, site string, page, size int) ([]*ent.DesignUntrackedChange, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 50
	}
	q := s.Client.DesignUntrackedChange.Query().Where(designuntrackedchange.ShopIDEQ(shopID))
	if site != "" {
		q.Where(designuntrackedchange.SiteEQ(site))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Order(ent.Desc(designuntrackedchange.FieldCommittedAt), ent.Asc(designuntrackedchange.FieldID)).
		Offset((page - 1) * size).Limit(size).All(ctx)
	return rows, total, err
}

// AckUntracked sets the acknowledgement of an untracked commit, once. One
// commit can touch both sites, so every row of that sha is acknowledged.
func (s *Service) AckUntracked(ctx context.Context, shopID int, sha string, userID int) ([]*ent.DesignUntrackedChange, error) {
	if !shaRE.MatchString(sha) {
		return nil, ErrNotFound()
	}
	rows, err := s.Client.DesignUntrackedChange.Query().Where(
		designuntrackedchange.ShopIDEQ(shopID), designuntrackedchange.CommitShaEQ(sha)).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotFound()
	}
	n, err := s.Client.DesignUntrackedChange.Update().
		Where(designuntrackedchange.ShopIDEQ(shopID), designuntrackedchange.CommitShaEQ(sha), designuntrackedchange.AcknowledgedAtIsNil()).
		SetAcknowledgedByUserID(userID).SetAcknowledgedAt(s.now()).Save(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, conflict("already_acknowledged", "this change was already acknowledged")
	}
	return s.Client.DesignUntrackedChange.Query().Where(
		designuntrackedchange.ShopIDEQ(shopID), designuntrackedchange.CommitShaEQ(sha)).All(ctx)
}
