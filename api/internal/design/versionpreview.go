package design

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/designversionpreview"
	"ksdevworks/ecommerce/api/internal/ent/predicate"
)

type predicateVP = predicate.DesignVersionPreview

// Version preview (change design-history, design D25): a throwaway deployment
// of a past release's design layer on the CURRENT core, owned by no request.
//
// Everything here is deliberately outside the history: it never reads or writes
// design_request_events, never takes the request slot (the partial unique index
// of design_requests), never changes a design_requests row and never stores a
// screenshot. The only record is the light usage row of design_version_previews
// (no trigger, no hash chain). The preview is read-only for whoever opens it
// (the core disables every writing ctx.action in preview mode); the platform
// publishes that fact as read_only on every descriptor it hands out.

// Statuses of a version preview.
const (
	VPQueued     = "queued"
	VPClaimed    = "claimed"
	VPReady      = "ready"
	VPFailed     = "failed"
	VPExpired    = "expired"
	VPSuperseded = "superseded"
)

// VersionPreviewStatuses lists the six states.
var VersionPreviewStatuses = []string{VPQueued, VPClaimed, VPReady, VPFailed, VPExpired, VPSuperseded}

// liveVP are the states that count as "the one active preview of a site".
var liveVP = []string{VPQueued, VPClaimed, VPReady}

// Defaults of the version preview policy (design D25 points 7 and 8).
const (
	defaultVPTTL        = 30 * time.Minute
	defaultVPClaimLease = 5 * time.Minute
	defaultVPHourlyCap  = 6
	defaultVPDailyCap   = 30
	// vpLockClass keys the advisory lock that serialises preview creation per
	// shop (so the caps and the one-active-per-site rule cannot race).
	vpLockClass = 0x44565031
)

var vpFailCodeRE = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
var vpRuleIDRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,60}$`)

func (s *Service) vpTTL() time.Duration {
	if s.Cfg.VersionPreviewTTL > 0 {
		return s.Cfg.VersionPreviewTTL
	}
	return defaultVPTTL
}

func (s *Service) vpLease() time.Duration {
	if s.Cfg.VersionPreviewClaimLease > 0 {
		return s.Cfg.VersionPreviewClaimLease
	}
	return defaultVPClaimLease
}

func (s *Service) vpHourlyCap() int {
	if s.Cfg.VersionPreviewHourlyCap > 0 {
		return s.Cfg.VersionPreviewHourlyCap
	}
	return defaultVPHourlyCap
}

func (s *Service) vpDailyCap() int {
	if s.Cfg.VersionPreviewDailyCap > 0 {
		return s.Cfg.VersionPreviewDailyCap
	}
	return defaultVPDailyCap
}

// EffectiveVPStatus is the status a reader should see: a live row whose
// expires_at has passed is "expired" even before the sweep writes it.
func EffectiveVPStatus(p *ent.DesignVersionPreview, now time.Time) string {
	switch p.Status {
	case VPQueued, VPClaimed, VPReady:
		if !p.ExpiresAt.After(now) {
			return VPExpired
		}
	}
	return p.Status
}

// Clock returns the service clock (for the HTTP layer's expiry decisions).
func (s *Service) Clock() time.Time { return s.now() }

func capErr(code, msg string, retry time.Duration) *Error {
	e := newErr(http.StatusTooManyRequests, code, msg)
	if retry < time.Second {
		retry = time.Second
	}
	e.RetryAfter = retry
	return e
}

func (s *Service) logVP(msg string, p *ent.DesignVersionPreview) {
	if s.Log == nil {
		return
	}
	// Ids, status and counts only: never the origin (design D20).
	s.Log.Info(msg, "version_preview_id", p.ID.String(), "shop_id", p.ShopID, "site", p.Site, "status", p.Status)
}

// CreateVersionPreview records a person's request for a version preview of a
// past release. The compatibility check (the same function the revert uses) runs
// first and synchronously: a refusal creates no row, so it uses no cap and
// deploys nothing. Creating supersedes the site's earlier live preview.
func (s *Service) CreateVersionPreview(ctx context.Context, shopID int, targetID uuid.UUID, userID int) (*ent.DesignVersionPreview, error) {
	target, err := s.load(ctx, shopID, targetID)
	if err != nil {
		return nil, err
	}
	st, err := s.checkRestorable(ctx, shopID, target)
	if err != nil {
		return nil, err
	}
	var out *ent.DesignVersionPreview
	err = s.inTx(ctx, func(tx *ent.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, vpLockClass, shopID); err != nil {
			return err
		}
		now := s.now()
		// Per (shop, user) per hour: counted from the table, not from Redis.
		hourAgo := now.Add(-time.Hour)
		recent, err := tx.DesignVersionPreview.Query().Where(
			designversionpreview.ShopIDEQ(shopID), designversionpreview.RequestedByUserIDEQ(userID),
			designversionpreview.CreatedAtGT(hourAgo)).
			Order(ent.Asc(designversionpreview.FieldCreatedAt)).All(ctx)
		if err != nil {
			return err
		}
		if cp := s.vpHourlyCap(); len(recent) >= cp {
			// The row that must age out of the window for one more to fit.
			return capErr("hourly_cap_reached", "you reached the hourly version preview limit; try again later",
				recent[len(recent)-cp].CreatedAt.Add(time.Hour).Sub(now))
		}
		// Per shop per UTC day.
		day := DayStart(now)
		n, err := tx.DesignVersionPreview.Query().Where(
			designversionpreview.ShopIDEQ(shopID), designversionpreview.CreatedAtGTE(day)).Count(ctx)
		if err != nil {
			return err
		}
		if n >= s.vpDailyCap() {
			return capErr("daily_cap_reached", "the shop reached today's version preview limit",
				day.Add(24*time.Hour).Sub(now))
		}
		// One live preview per (shop, site): the older one is superseded and the
		// service tears its deployment down.
		if _, err := tx.DesignVersionPreview.Update().Where(
			designversionpreview.ShopIDEQ(shopID), designversionpreview.SiteEQ(target.Site),
			designversionpreview.StatusIn(liveVP...)).
			SetStatus(VPSuperseded).SetUpdatedAt(now).Save(ctx); err != nil {
			return err
		}
		row, err := tx.DesignVersionPreview.Create().
			SetShopID(shopID).SetSite(target.Site).SetTargetRequestID(target.ID).SetTargetTag(*target.PublishedTag).
			SetRequestedByUserID(userID).SetStatus(VPQueued).
			SetCoreVersion(st.CoreVersion).SetDesignFloorVersion(st.DesignFloorVersion).
			SetCreatedAt(now).SetUpdatedAt(now).SetExpiresAt(now.Add(s.vpTTL())).Save(ctx)
		out = row
		return err
	})
	if err != nil {
		if isUnique(err, "design_version_previews_one_active") {
			return nil, conflict("version_preview_in_progress", "another version preview of this site was just requested; try again")
		}
		return nil, err
	}
	s.logVP("design version preview requested", out)
	return out, nil
}

// GetVersionPreview returns one version preview of the shop (404 for another
// shop's).
func (s *Service) GetVersionPreview(ctx context.Context, shopID int, id uuid.UUID) (*ent.DesignVersionPreview, error) {
	row, err := s.Client.DesignVersionPreview.Query().Where(
		designversionpreview.IDEQ(id), designversionpreview.ShopIDEQ(shopID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound()
	}
	return row, err
}

// ListVersionPreviews is the service's queue. status defaults to queued; the
// other states let the service find deployments to tear down (superseded,
// expired). since limits to rows changed at or after that time.
func (s *Service) ListVersionPreviews(ctx context.Context, shopID int, status string, since *time.Time, limit int) ([]*ent.DesignVersionPreview, error) {
	if status == "" {
		status = VPQueued
	}
	ok := false
	for _, v := range VersionPreviewStatuses {
		ok = ok || v == status
	}
	if !ok {
		return nil, ErrInvalid("status must be one of queued, claimed, ready, failed, expired, superseded")
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	q := s.Client.DesignVersionPreview.Query().Where(designversionpreview.ShopIDEQ(shopID), designversionpreview.StatusEQ(status))
	if status == VPQueued {
		q.Where(designversionpreview.ExpiresAtGT(s.now())) // a request past its deadline is not work
	}
	if since != nil {
		q.Where(designversionpreview.UpdatedAtGTE(*since))
	}
	return q.Order(ent.Asc(designversionpreview.FieldCreatedAt), ent.Asc(designversionpreview.FieldID)).Limit(limit).All(ctx)
}

// ClaimVersionPreview takes (or renews, for the same credential) the 5-minute
// lease on a queued preview.
func (s *Service) ClaimVersionPreview(ctx context.Context, shopID int, id uuid.UUID, credID int) (*ent.DesignVersionPreview, error) {
	now := s.now()
	n, err := s.Client.DesignVersionPreview.Update().Where(
		designversionpreview.IDEQ(id), designversionpreview.ShopIDEQ(shopID), designversionpreview.ExpiresAtGT(now),
		designversionpreview.Or(
			designversionpreview.StatusEQ(VPQueued),
			designversionpreview.And(designversionpreview.StatusEQ(VPClaimed), designversionpreview.Or(
				designversionpreview.ClaimExpiresAtLT(now), designversionpreview.ClaimedByCredentialIDEQ(credID)))),
	).SetStatus(VPClaimed).SetClaimedByCredentialID(credID).SetClaimExpiresAt(now.Add(s.vpLease())).SetUpdatedAt(now).Save(ctx)
	if err != nil {
		return nil, err
	}
	cur, lerr := s.GetVersionPreview(ctx, shopID, id)
	if lerr != nil {
		return nil, lerr
	}
	if n == 0 {
		if cur.Status == VPClaimed && cur.ExpiresAt.After(now) {
			return nil, conflict("claim_conflict", "another worker holds the lease")
		}
		return nil, ErrInvalidState(fmt.Sprintf("claim: the version preview is %s", EffectiveVPStatus(cur, now)))
	}
	s.logVP("design version preview claimed", cur)
	return cur, nil
}

// ReadyVersionPreview records that the service deployed the preview. Only the
// claimant may report it; origin is scheme + host only; expires_at must lie in
// the future and within the configured TTL from now. A bad origin is a 422 and
// the row stays claimed.
func (s *Service) ReadyVersionPreview(ctx context.Context, shopID int, id uuid.UUID, credID int, origin string, expiresAt time.Time) (*ent.DesignVersionPreview, error) {
	o, err := PreviewOrigin(origin)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if !expiresAt.After(now) {
		return nil, ErrInvalid("expires_at must be in the future")
	}
	if expiresAt.After(now.Add(s.vpTTL())) {
		return nil, ErrInvalid(fmt.Sprintf("expires_at must be within %s from now", s.vpTTL()))
	}
	expiresAt = Micros(expiresAt)
	cur, err := s.GetVersionPreview(ctx, shopID, id)
	if err != nil {
		return nil, err
	}
	// A retry of the same report (a lost response) is answered, not rejected.
	if cur.Status == VPReady && cur.ClaimedByCredentialID != nil && *cur.ClaimedByCredentialID == credID &&
		cur.PreviewOrigin != nil && *cur.PreviewOrigin == o && cur.ExpiresAt.Equal(expiresAt) {
		return cur, nil
	}
	n, err := s.Client.DesignVersionPreview.Update().Where(
		designversionpreview.IDEQ(id), designversionpreview.ShopIDEQ(shopID),
		designversionpreview.StatusEQ(VPClaimed), designversionpreview.ClaimedByCredentialIDEQ(credID),
		designversionpreview.ExpiresAtGT(now),
	).SetStatus(VPReady).SetPreviewOrigin(o).SetReadyAt(now).SetExpiresAt(expiresAt).ClearClaimExpiresAt().SetUpdatedAt(now).Save(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		cur, err = s.GetVersionPreview(ctx, shopID, id)
		if err != nil {
			return nil, err
		}
		if cur.Status == VPClaimed && cur.ClaimedByCredentialID != nil && *cur.ClaimedByCredentialID != credID {
			return nil, conflict("claim_conflict", "another worker holds the lease")
		}
		return nil, ErrInvalidState(fmt.Sprintf("ready: the version preview is %s", EffectiveVPStatus(cur, now)))
	}
	cur, err = s.GetVersionPreview(ctx, shopID, id)
	if err == nil {
		s.logVP("design version preview ready", cur)
	}
	return cur, err
}

// FailVersionPreview records that the service could not (or would not) deploy.
// guard_failed carries the gatekeeper's rule ids so the person who asked can see
// why; detail is plain text. Allowed from queued or claimed (by this credential,
// or whose lease ran out).
func (s *Service) FailVersionPreview(ctx context.Context, shopID int, id uuid.UUID, credID int, code, detail string, ruleIDs []string) (*ent.DesignVersionPreview, error) {
	if !vpFailCodeRE.MatchString(code) {
		return nil, ErrInvalid("code must be 1-40 characters of [a-z0-9_]")
	}
	detail = SanitizeText(detail)
	if utf8.RuneCountInString(detail) > 300 {
		return nil, ErrInvalid("detail must be at most 300 characters")
	}
	if len(ruleIDs) > 20 {
		return nil, ErrInvalid("rule_ids may list at most 20 rules")
	}
	rules := make([]string, 0, len(ruleIDs))
	for _, r := range ruleIDs {
		r = strings.TrimSpace(r)
		if !vpRuleIDRE.MatchString(r) {
			return nil, ErrInvalid("rule_ids must be 1-60 characters of [A-Za-z0-9._:-]")
		}
		rules = append(rules, r)
	}
	now := s.now()
	cur, err := s.GetVersionPreview(ctx, shopID, id)
	if err != nil {
		return nil, err
	}
	if cur.Status == VPFailed && cur.FailureCode != nil && *cur.FailureCode == code {
		return cur, nil // a retried report
	}
	u := s.Client.DesignVersionPreview.Update().Where(
		designversionpreview.IDEQ(id), designversionpreview.ShopIDEQ(shopID),
		designversionpreview.Or(
			designversionpreview.StatusEQ(VPQueued),
			designversionpreview.And(designversionpreview.StatusEQ(VPClaimed), designversionpreview.Or(
				designversionpreview.ClaimedByCredentialIDEQ(credID), designversionpreview.ClaimExpiresAtLT(now)))),
	).SetStatus(VPFailed).SetFailureCode(code).SetFailureRuleIds(rules).ClearClaimExpiresAt().SetUpdatedAt(now)
	if detail != "" {
		u.SetFailureDetail(detail)
	}
	n, err := u.Save(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		cur, err = s.GetVersionPreview(ctx, shopID, id)
		if err != nil {
			return nil, err
		}
		if cur.Status == VPClaimed {
			return nil, conflict("claim_conflict", "another worker holds the lease")
		}
		return nil, ErrInvalidState(fmt.Sprintf("failed: the version preview is %s", cur.Status))
	}
	cur, err = s.GetVersionPreview(ctx, shopID, id)
	if err == nil {
		s.logVP("design version preview failed", cur)
	}
	return cur, err
}

// VersionPreviewSweep counts what the sweep did (or would do).
type VersionPreviewSweep struct {
	LeasesLost int // claimed, lease ran out -> failed(service_lost)
	Expired    int // live, past expires_at -> expired
}

// SweepVersionPreviews marks version previews whose lease ran out failed
// (service_lost) and those past their expiry expired. It touches only
// design_version_previews. shopID 0 means every shop. Idempotent.
func (s *Service) SweepVersionPreviews(ctx context.Context, shopID int, apply bool) (*VersionPreviewSweep, error) {
	rep := &VersionPreviewSweep{}
	now := s.now()
	scope := func() []predicateVP {
		if shopID > 0 {
			return []predicateVP{designversionpreview.ShopIDEQ(shopID)}
		}
		return nil
	}
	lost := append(scope(), designversionpreview.StatusEQ(VPClaimed), designversionpreview.ClaimExpiresAtLT(now))
	if !apply {
		n, err := s.Client.DesignVersionPreview.Query().Where(lost...).Count(ctx)
		if err != nil {
			return rep, err
		}
		rep.LeasesLost = n
		// Claimed rows whose lease ran out are counted once, as lost, so the
		// expiry count below excludes them.
		exp := append(scope(), designversionpreview.StatusIn(liveVP...), designversionpreview.ExpiresAtLTE(now),
			designversionpreview.Not(designversionpreview.And(designversionpreview.StatusEQ(VPClaimed), designversionpreview.ClaimExpiresAtLT(now))))
		rep.Expired, err = s.Client.DesignVersionPreview.Query().Where(exp...).Count(ctx)
		return rep, err
	}
	n, err := s.Client.DesignVersionPreview.Update().Where(lost...).
		SetStatus(VPFailed).SetFailureCode("service_lost").SetFailureDetail("the worker's claim lease expired").
		ClearClaimExpiresAt().SetUpdatedAt(now).Save(ctx)
	if err != nil {
		return rep, err
	}
	rep.LeasesLost = n
	exp := append(scope(), designversionpreview.StatusIn(liveVP...), designversionpreview.ExpiresAtLTE(now))
	n, err = s.Client.DesignVersionPreview.Update().Where(exp...).SetStatus(VPExpired).SetUpdatedAt(now).Save(ctx)
	rep.Expired = n
	return rep, err
}
