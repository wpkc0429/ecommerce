package design

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
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

// This file holds the operations of the machine route family: what the AI edit
// service may do. Nothing here approves, rejects, cancels or reverts.

const maxPromptRunes = 4000

// CreateInput is POST /requests of the service.
type CreateInput struct {
	ShopID         int
	CredentialID   int
	IdempotencyKey string
	Site           string
	RequestedBy    int
	Prompt         string
	BaseSHA        string
	// RequestID is optional: the service may pick the id so it can name the
	// branch ai/<shop>/<id> before the call. Default: the platform generates it.
	RequestID    *uuid.UUID
	Branch       string
	CoreVersion  string
	FloorVersion string
}

// DefaultBranch is the branch the platform records when the service sends none.
func DefaultBranch(shopID int, id uuid.UUID) string {
	return fmt.Sprintf("ai/shop-%d/%s", shopID, id)
}

var branchRE = regexp.MustCompile(`^ai/[a-z0-9][a-z0-9-]*/([0-9a-f-]{36})$`)
var versionRE = regexp.MustCompile(`^[0-9A-Za-z.+-]{0,20}$`)

func fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CreateRequest records a new redesign request (service). The second result is
// true when a new row was written, false for an idempotent replay.
func (s *Service) CreateRequest(ctx context.Context, in CreateInput) (*ent.DesignRequest, bool, error) {
	if !idemKeyR.MatchString(in.IdempotencyKey) {
		return nil, false, newErr(http.StatusUnprocessableEntity, "invalid_idempotency_key", "Idempotency-Key must be 8-255 printable ASCII characters")
	}
	if !IsSite(in.Site) {
		return nil, false, ErrInvalid("site must be storefront or admin")
	}
	if !shaRE.MatchString(in.BaseSHA) {
		return nil, false, ErrInvalid("base_sha must be a 40-character lowercase hex SHA")
	}
	if !versionRE.MatchString(in.CoreVersion) || !versionRE.MatchString(in.FloorVersion) {
		return nil, false, ErrInvalid("core_version and design_floor_version must be at most 20 characters of [0-9A-Za-z.+-]")
	}
	if in.RequestedBy <= 0 {
		return nil, false, ErrInvalid("requested_by_user_id is required")
	}
	prompt := SanitizeText(in.Prompt)
	if utf8.RuneCountInString(prompt) > maxPromptRunes {
		return nil, false, ErrInvalid("prompt must be at most 4000 characters")
	}
	prompt, flags := RedactPII(prompt)
	if strings.TrimSpace(prompt) == "" {
		return nil, false, ErrInvalid("prompt is required")
	}
	id := uuid.New()
	if in.RequestID != nil {
		id = *in.RequestID
	}
	branch := in.Branch
	if branch == "" {
		branch = DefaultBranch(in.ShopID, id)
	} else if m := branchRE.FindStringSubmatch(branch); m == nil || m[1] != id.String() || len(branch) > 120 {
		return nil, false, ErrInvalid("branch must be ai/<name>/<request id>")
	}
	// The fingerprint covers what the caller SENT (not the id or branch the
	// platform generated), so a replay of the same request matches.
	sentID := ""
	if in.RequestID != nil {
		sentID = in.RequestID.String()
	}
	fp := fingerprint(in.Site, fmt.Sprint(in.RequestedBy), prompt, in.BaseSHA, in.Branch, sentID, in.CoreVersion, in.FloorVersion)

	// Idempotency: the same key with the same content is the same request.
	if row, err := s.byIdempotencyKey(ctx, in.ShopID, in.IdempotencyKey); err != nil {
		return nil, false, err
	} else if row != nil {
		return replayOrReused(row, fp)
	}

	// The requester must be an active member holding design.request: a
	// compromised service cannot hang a request on an arbitrary person.
	ok, err := s.Authz.Allows(ctx, in.RequestedBy, in.ShopID, PermRequest)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, newErr(http.StatusForbidden, "requester_not_permitted", "requested_by_user_id is not an active member holding design.request")
	}

	// Daily cap (a database COUNT: it does not depend on Redis).
	if s.Cfg.DailyRequestCap > 0 {
		n, err := s.Client.DesignRequest.Query().Where(
			designrequest.ShopIDEQ(in.ShopID), designrequest.KindEQ(KindChange),
			designrequest.CreatedAtGTE(DayStart(s.now())),
		).Count(ctx)
		if err != nil {
			return nil, false, err
		}
		if n >= s.Cfg.DailyRequestCap {
			return nil, false, newErr(http.StatusTooManyRequests, "daily_cap_reached", "the shop reached today's design request limit")
		}
	}

	var created *ent.DesignRequest
	err = s.inTx(ctx, func(tx *ent.Tx) error {
		now := s.now()
		row, err := tx.DesignRequest.Create().
			SetID(id).SetShopID(in.ShopID).SetSite(in.Site).SetKind(KindChange).SetStatus(StatusDrafting).
			SetRequestedByUserID(in.RequestedBy).SetPromptText(prompt).SetPromptSha256(sha(prompt)).
			SetPromptFlags(nonNil(flags)).SetBaseSha(in.BaseSHA).SetHeadSha(in.BaseSHA).SetBranch(branch).
			SetCoreVersion(in.CoreVersion).SetDesignFloorVersion(in.FloorVersion).
			SetIdempotencyKey(in.IdempotencyKey).SetIdempotencyFingerprint(fp).
			SetRev(1).SetCreatedAt(now).SetUpdatedAt(now).Save(ctx)
		if err != nil {
			return err
		}
		created = row
		return s.appendEvent(ctx, tx, row, EvCreated, "", StatusDrafting, ServiceActor(in.CredentialID), row.HeadSha,
			map[string]any{"site": in.Site, "kind": KindChange, "flags": nonNil(flags)}, now)
	})
	switch {
	case isUnique(err, "design_requests_one_active"):
		return nil, false, conflict("request_in_progress", "the site already has a request in progress; finish or cancel it first")
	case isUnique(err, "design_requests_idem_key"):
		// Lost a race with the same key: serve the winner.
		row, qerr := s.byIdempotencyKey(ctx, in.ShopID, in.IdempotencyKey)
		if qerr != nil || row == nil {
			return nil, false, err
		}
		return replayOrReused(row, fp)
	case isUnique(err, "design_requests_pkey"):
		return nil, false, conflict("request_exists", "a request with this id already exists")
	case err != nil:
		return nil, false, err
	}
	return created, true, nil
}

func replayOrReused(row *ent.DesignRequest, fp string) (*ent.DesignRequest, bool, error) {
	if row.IdempotencyFingerprint == nil || *row.IdempotencyFingerprint != fp {
		return nil, false, newErr(http.StatusUnprocessableEntity, "idempotency_key_reused", "this Idempotency-Key was used with a different request")
	}
	return row, false, nil
}

func (s *Service) byIdempotencyKey(ctx context.Context, shopID int, key string) (*ent.DesignRequest, error) {
	row, err := s.Client.DesignRequest.Query().
		Where(designrequest.ShopIDEQ(shopID), designrequest.IdempotencyKeyEQ(key)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return row, err
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ListQueue is the service's work queue: requests in a status, optionally only
// the unclaimed (or whose lease ran out).
func (s *Service) ListQueue(ctx context.Context, shopID int, status string, unclaimed bool, limit int) ([]*ent.DesignRequest, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	q := s.Client.DesignRequest.Query().Where(designrequest.ShopIDEQ(shopID))
	if status != "" {
		q.Where(designrequest.StatusEQ(status))
	} else {
		q.Where(designrequest.StatusIn(ActiveStatuses...))
	}
	if unclaimed {
		q.Where(designrequest.Or(designrequest.ClaimExpiresAtIsNil(), designrequest.ClaimExpiresAtLT(s.now())))
	}
	return q.Order(ent.Asc(designrequest.FieldCreatedAt), ent.Asc(designrequest.FieldID)).Limit(limit).All(ctx)
}

// Claim takes (or renews, for the same credential) the 15-minute lease.
func (s *Service) Claim(ctx context.Context, shopID int, id uuid.UUID, credID int) (*ent.DesignRequest, error) {
	now := s.now()
	var out *ent.DesignRequest
	err := s.inTx(ctx, func(tx *ent.Tx) error {
		row, err := s.step(ctx, tx, step{
			shopID: shopID, id: id, from: []string{StatusDrafting, StatusPreviewing}, kind: EvClaimed,
			actor: ServiceActor(credID),
			extra: []predicate.DesignRequest{designrequest.Or(
				designrequest.ClaimExpiresAtIsNil(), designrequest.ClaimExpiresAtLT(now),
				designrequest.ClaimedByCredentialIDEQ(credID))},
			set: func(u *ent.DesignRequestUpdate) {
				u.SetClaimedAt(now).SetClaimExpiresAt(now.Add(s.Cfg.ClaimLease)).SetClaimedByCredentialID(credID)
			},
			detail: map[string]any{"lease_seconds": int(s.Cfg.ClaimLease.Seconds())},
		})
		out = row
		return err
	})
	if err != nil {
		cur, lerr := s.load(ctx, shopID, id)
		if lerr != nil {
			return nil, lerr
		}
		if cur.Status == StatusDrafting || cur.Status == StatusPreviewing {
			return nil, conflict("claim_conflict", "another worker holds the lease")
		}
		return nil, s.wrap(ctx, shopID, id, "claim", err)
	}
	return out, nil
}

// DiffSummary is the paths-and-counts-only summary of a change.
type DiffSummary struct {
	FilesChanged int        `json:"files_changed"`
	Additions    int        `json:"additions"`
	Deletions    int        `json:"deletions"`
	Files        []DiffFile `json:"files"`
}

// DiffFile is one entry of a DiffSummary.
type DiffFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

func normalizeDiff(in DiffSummary) (json.RawMessage, error) {
	if len(in.Files) > 300 {
		return nil, ErrInvalid("diff_summary.files may list at most 300 files")
	}
	for i := range in.Files {
		f := &in.Files[i]
		f.Path = SanitizeText(f.Path)
		if f.Path == "" || len(f.Path) > 300 {
			return nil, ErrInvalid("diff_summary.files[].path must be 1-300 characters")
		}
		f.Status = SanitizeText(f.Status)
		if len(f.Status) > 20 {
			return nil, ErrInvalid("diff_summary.files[].status is too long")
		}
		if f.Additions < 0 || f.Deletions < 0 {
			return nil, ErrInvalid("diff_summary counts must not be negative")
		}
	}
	if in.FilesChanged < 0 || in.Additions < 0 || in.Deletions < 0 {
		return nil, ErrInvalid("diff_summary counts must not be negative")
	}
	if in.Files == nil {
		in.Files = []DiffFile{}
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	if len(raw) > 16*1024 {
		return nil, ErrInvalid("diff_summary is larger than 16 KiB")
	}
	return raw, nil
}

// UpdateHead records a new head (service). In previewing or awaiting_approval
// it needs reset=true and sends the request back to drafting, which voids any
// candidate. After approval the head is locked.
func (s *Service) UpdateHead(ctx context.Context, shopID int, id uuid.UUID, credID int, head string, diff DiffSummary, reset bool) (*ent.DesignRequest, error) {
	if !shaRE.MatchString(head) {
		return nil, ErrInvalid("head_sha must be a 40-character lowercase hex SHA")
	}
	raw, err := normalizeDiff(diff)
	if err != nil {
		return nil, err
	}
	cur, err := s.load(ctx, shopID, id)
	if err != nil {
		return nil, err
	}
	from := []string{StatusDrafting}
	to := ""
	if cur.Status == StatusPreviewing || cur.Status == StatusAwaitingApproval {
		if !reset {
			return nil, ErrInvalidState("the head can only change in drafting; send reset=true to go back to drafting")
		}
		from, to = []string{cur.Status}, StatusDrafting
	}
	var out *ent.DesignRequest
	err = s.inTx(ctx, func(tx *ent.Tx) error {
		row, err := s.step(ctx, tx, step{
			shopID: shopID, id: id, from: from, to: to, kind: EvHeadUpdated, actor: ServiceActor(credID),
			headSHA: head,
			detail:  map[string]any{"files_changed": diff.FilesChanged, "additions": diff.Additions, "deletions": diff.Deletions, "reset": reset},
			set: func(u *ent.DesignRequestUpdate) {
				u.SetHeadSha(head).SetDiffSummary(raw).ClearGuard().ClearCandidateSha().
					ClearPreviewOrigin().ClearPreviewRegisteredAt().ClearPreviewExpiresAt()
			},
		})
		out = row
		return err
	})
	if err != nil {
		return nil, s.wrap(ctx, shopID, id, "update head", err)
	}
	return out, nil
}

// GuardInput is the gatekeeper's verdict as the service reports it.
type GuardInput struct {
	Phase          string // "initial" (default) or "recheck"
	CheckedSHA     string
	Passed         bool
	ExitCode       int
	RuleIDs        []string
	ViolationCount int
	GuardVersion   string
}

var ruleIDRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,80}$`)

// RecordGuard stores a gatekeeper run. A recheck is only accepted on an
// approved request and is what lets `published` be recorded later.
func (s *Service) RecordGuard(ctx context.Context, shopID int, id uuid.UUID, credID int, in GuardInput) (*ent.DesignRequest, error) {
	if !shaRE.MatchString(in.CheckedSHA) {
		return nil, ErrInvalid("checked_sha must be a 40-character lowercase hex SHA")
	}
	if len(in.RuleIDs) > 50 {
		return nil, ErrInvalid("at most 50 rule ids")
	}
	for _, r := range in.RuleIDs {
		if !ruleIDRE.MatchString(r) {
			return nil, ErrInvalid("rule ids must match [A-Za-z0-9_.:-]{1,80}")
		}
	}
	if len(in.GuardVersion) > 40 {
		return nil, ErrInvalid("guard_version is too long")
	}
	now := s.now()
	g := map[string]any{
		"passed": in.Passed, "checked_sha": in.CheckedSHA, "exit_code": in.ExitCode,
		"rule_ids": nonNil(in.RuleIDs), "violation_count": in.ViolationCount,
		"guard_version": in.GuardVersion, "checked_at": now.Format(time.RFC3339),
	}
	detail := map[string]any{"passed": in.Passed, "checked_sha": in.CheckedSHA, "rule_ids": nonNil(in.RuleIDs), "violation_count": in.ViolationCount}
	var out *ent.DesignRequest
	var err error
	switch in.Phase {
	case "", "initial":
		raw, _ := json.Marshal(g)
		err = s.inTx(ctx, func(tx *ent.Tx) error {
			row, err := s.step(ctx, tx, step{
				shopID: shopID, id: id, from: []string{StatusDrafting, StatusPreviewing}, kind: EvGuardRun,
				actor: ServiceActor(credID), detail: detail, headSHA: in.CheckedSHA,
				set: func(u *ent.DesignRequestUpdate) { u.SetGuard(raw) },
			})
			out = row
			return err
		})
	case "recheck":
		err = s.inTx(ctx, func(tx *ent.Tx) error {
			row, err := s.step(ctx, tx, step{
				shopID: shopID, id: id, from: []string{StatusApproved}, kind: EvGuardRechecked,
				actor: ServiceActor(credID), detail: detail, headSHA: in.CheckedSHA,
			})
			out = row
			return err
		})
	default:
		return nil, ErrInvalid("phase must be initial or recheck")
	}
	if err != nil {
		return nil, s.wrap(ctx, shopID, id, "record guard run", err)
	}
	return out, nil
}

// PreviewOrigin validates a preview origin: scheme://host[:port] only.
func PreviewOrigin(raw string) (string, error) {
	if raw == "" || len(raw) > 255 {
		return "", ErrInvalid("origin is required (at most 255 characters)")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, " \t\r\n") {
		return "", ErrInvalid("origin must be an http(s) origin without path, query or fragment")
	}
	return u.Scheme + "://" + u.Host, nil
}

// RegisterPreview records the preview and moves drafting -> previewing. It
// needs a passing gatekeeper run for exactly the current head.
func (s *Service) RegisterPreview(ctx context.Context, shopID int, id uuid.UUID, credID int, origin string, expiresAt time.Time) (*ent.DesignRequest, error) {
	o, err := PreviewOrigin(origin)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if !expiresAt.After(now) {
		return nil, ErrInvalid("expires_at must be in the future")
	}
	cur, err := s.load(ctx, shopID, id)
	if err != nil {
		return nil, err
	}
	if cur.Status != StatusDrafting && cur.Status != StatusPreviewing {
		return nil, ErrInvalidState("a preview can only be registered in drafting or previewing")
	}
	if !guardPassedFor(cur) {
		return nil, conflict("guard_not_passed", "the gatekeeper has not passed for the current head")
	}
	var out *ent.DesignRequest
	err = s.inTx(ctx, func(tx *ent.Tx) error {
		row, err := s.step(ctx, tx, step{
			shopID: shopID, id: id, from: []string{StatusDrafting, StatusPreviewing}, to: StatusPreviewing,
			kind: EvPreviewRegistered, actor: ServiceActor(credID),
			detail: map[string]any{"expires_at": expiresAt.UTC().Format(time.RFC3339)},
			// The head must still be the one the verdict was for.
			extra: []predicate.DesignRequest{designrequest.HeadShaEQ(cur.HeadSha)},
			set: func(u *ent.DesignRequestUpdate) {
				u.SetPreviewOrigin(o).SetPreviewRegisteredAt(now).SetPreviewExpiresAt(Micros(expiresAt))
			},
		})
		out = row
		return err
	})
	if err != nil {
		return nil, s.wrap(ctx, shopID, id, "register preview", err)
	}
	return out, nil
}

func guardPassedFor(r *ent.DesignRequest) bool {
	if len(r.Guard) == 0 {
		return false
	}
	var g struct {
		Passed     bool   `json:"passed"`
		CheckedSHA string `json:"checked_sha"`
	}
	if json.Unmarshal(r.Guard, &g) != nil {
		return false
	}
	return g.Passed && g.CheckedSHA == r.HeadSha
}

// Submit moves previewing -> awaiting_approval and locks candidate_sha.
func (s *Service) Submit(ctx context.Context, shopID int, id uuid.UUID, credID int) (*ent.DesignRequest, error) {
	cur, err := s.load(ctx, shopID, id)
	if err != nil {
		return nil, err
	}
	if cur.Status != StatusPreviewing {
		return nil, ErrInvalidState("only a request in previewing can be submitted")
	}
	if !s.Cfg.ScreenshotsEnabled {
		return nil, newErr(http.StatusServiceUnavailable, "design_storage_not_configured", "screenshot storage is not configured, so a request cannot be submitted for approval")
	}
	if cerr := s.screenshotsComplete(ctx, cur); cerr != nil {
		return nil, cerr
	}
	now := s.now()
	if cur.PreviewExpiresAt == nil || cur.PreviewExpiresAt.Before(now.Add(s.Cfg.MinPreviewRemaining)) {
		return nil, conflict("preview_expired", "the preview expires too soon to be reviewed; register a new one")
	}
	var out *ent.DesignRequest
	err = s.inTx(ctx, func(tx *ent.Tx) error {
		if _, err := s.step(ctx, tx, step{
			shopID: shopID, id: id, from: []string{StatusPreviewing}, kind: EvScreenshotsDone, actor: ServiceActor(credID),
			extra:  []predicate.DesignRequest{designrequest.HeadShaEQ(cur.HeadSha)},
			detail: map[string]any{},
		}); err != nil {
			return err
		}
		row, err := s.step(ctx, tx, step{
			shopID: shopID, id: id, from: []string{StatusPreviewing}, to: StatusAwaitingApproval, kind: EvSubmitted,
			actor: ServiceActor(credID), detail: map[string]any{"candidate_sha": cur.HeadSha},
			extra: []predicate.DesignRequest{designrequest.HeadShaEQ(cur.HeadSha), designrequest.PreviewExpiresAtGTE(now.Add(s.Cfg.MinPreviewRemaining))},
			set:   func(u *ent.DesignRequestUpdate) { u.SetCandidateSha(cur.HeadSha) },
		})
		out = row
		return err
	})
	if err != nil {
		return nil, s.wrap(ctx, shopID, id, "submit", err)
	}
	return out, nil
}

// ReportFailed marks any non-terminal request failed (service).
func (s *Service) ReportFailed(ctx context.Context, shopID int, id uuid.UUID, credID int, code, detail string) (*ent.DesignRequest, error) {
	if !regexp.MustCompile(`^[a-z0-9_]{1,40}$`).MatchString(code) {
		return nil, ErrInvalid("code must be 1-40 characters of [a-z0-9_]")
	}
	detail = SanitizeText(detail)
	if utf8.RuneCountInString(detail) > 300 {
		return nil, ErrInvalid("detail must be at most 300 characters")
	}
	var out *ent.DesignRequest
	err := s.inTx(ctx, func(tx *ent.Tx) error {
		row, err := s.step(ctx, tx, step{
			shopID: shopID, id: id, from: ActiveStatuses, to: StatusFailed, kind: EvFailed, actor: ServiceActor(credID),
			detail: map[string]any{"code": code},
			set:    func(u *ent.DesignRequestUpdate) { u.SetFailureCode(code).SetFailureDetail(detail) },
		})
		out = row
		return err
	})
	if err != nil {
		return nil, s.wrap(ctx, shopID, id, "report failure", err)
	}
	return out, nil
}

// PublishedInput is what the service reports after deploying.
type PublishedInput struct {
	PublishedSHA string
	Tag          string
	DeploymentID string
}

// ReportPublished records `published`. The platform accepts it only for an
// approved, unexpired request whose exact approved SHA was re-checked after the
// approval, with the tag the platform issued: a service cannot make the
// platform recognise a release nobody approved (design D10).
func (s *Service) ReportPublished(ctx context.Context, shopID int, id uuid.UUID, credID int, in PublishedInput) (*ent.DesignRequest, error) {
	if len(in.DeploymentID) > 100 {
		return nil, ErrInvalid("deployment_id must be at most 100 characters")
	}
	cur, err := s.load(ctx, shopID, id)
	if err != nil {
		return nil, err
	}
	if cur.Status != StatusApproved {
		return nil, ErrInvalidState("only an approved request can be recorded as published")
	}
	now := s.now()
	if cur.ApprovalExpiresAt == nil || !now.Before(*cur.ApprovalExpiresAt) {
		return nil, conflict("approval_expired", "the approval has expired; the change must be redone and approved again")
	}
	if cur.ApprovedHeadSha == nil || in.PublishedSHA != *cur.ApprovedHeadSha {
		return nil, ErrInvalid("published_sha must equal the approved head sha")
	}
	if cur.PublishTag == nil || in.Tag != *cur.PublishTag {
		return nil, ErrInvalid("tag must equal the publish tag issued at approval")
	}
	rechecked, err := s.rechecked(ctx, cur)
	if err != nil {
		return nil, err
	}
	if !rechecked {
		return nil, conflict("guard_not_passed", "no passing gatekeeper recheck of the approved sha was recorded after the approval")
	}
	var out *ent.DesignRequest
	err = s.inTx(ctx, func(tx *ent.Tx) error {
		row, err := s.step(ctx, tx, step{
			shopID: shopID, id: id, from: []string{StatusApproved}, to: StatusPublished, kind: EvPublished,
			actor: ServiceActor(credID), headSHA: in.PublishedSHA,
			detail: map[string]any{"tag": in.Tag},
			extra: []predicate.DesignRequest{
				designrequest.ApprovalExpiresAtGT(now),
				designrequest.ApprovedHeadShaEQ(in.PublishedSHA),
				designrequest.PublishTagEQ(in.Tag),
			},
			set: func(u *ent.DesignRequestUpdate) {
				u.SetPublishedSha(in.PublishedSHA).SetPublishedTag(in.Tag).SetPublishedAt(now)
				if in.DeploymentID != "" {
					u.SetDeploymentID(in.DeploymentID)
				}
			},
		})
		if err != nil {
			return err
		}
		out = row
		return s.afterPublished(ctx, tx, row)
	})
	if err != nil {
		return nil, s.wrap(ctx, shopID, id, "record published", err)
	}
	return out, nil
}

// rechecked reports whether a passing guard_rechecked event for the approved
// sha exists after the latest approval.
func (s *Service) rechecked(ctx context.Context, r *ent.DesignRequest) (bool, error) {
	appr, err := s.Client.DesignRequestEvent.Query().
		Where(designrequestevent.RequestIDEQ(r.ID), designrequestevent.KindEQ(EvApproved)).
		Order(ent.Desc(designrequestevent.FieldSeq)).First(ctx)
	if err != nil {
		return false, err
	}
	evs, err := s.Client.DesignRequestEvent.Query().
		Where(designrequestevent.RequestIDEQ(r.ID), designrequestevent.KindEQ(EvGuardRechecked),
			designrequestevent.SeqGT(appr.Seq), designrequestevent.HeadShaEQ(*r.ApprovedHeadSha)).All(ctx)
	if err != nil {
		return false, err
	}
	for _, e := range evs {
		var d struct {
			Passed bool `json:"passed"`
		}
		if json.Unmarshal(e.Detail, &d) == nil && d.Passed {
			return true, nil
		}
	}
	return false, nil
}

// afterPublished updates the site's current design and, for a revert, marks
// the superseded releases (design D13).
func (s *Service) afterPublished(ctx context.Context, tx *ent.Tx, row *ent.DesignRequest) error {
	now := s.now()
	err := tx.DesignSiteState.Create().
		SetShopID(row.ShopID).SetSite(row.Site).SetCurrentRequestID(row.ID).SetCurrentTag(*row.PublishedTag).
		SetCurrentSha(*row.PublishedSha).SetCoreVersion(row.CoreVersion).SetDesignFloorVersion(row.DesignFloorVersion).
		SetUpdatedAt(now).
		OnConflictColumns(designsitestate.FieldShopID, designsitestate.FieldSite).
		UpdateCurrentRequestID().UpdateCurrentTag().UpdateCurrentSha().UpdateCoreVersion().
		UpdateDesignFloorVersion().UpdateUpdatedAt().Exec(ctx)
	if err != nil {
		return err
	}
	if row.Kind != KindRevert || row.RestoresRequestID == nil {
		return nil
	}
	target, err := tx.DesignRequest.Get(ctx, *row.RestoresRequestID)
	if err != nil || target.PublishedAt == nil {
		return err
	}
	// Every release after the target and before this revert is superseded.
	later, err := tx.DesignRequest.Query().Where(
		designrequest.ShopIDEQ(row.ShopID), designrequest.SiteEQ(row.Site), designrequest.StatusEQ(StatusPublished),
		designrequest.PublishedAtGT(*target.PublishedAt), designrequest.IDNEQ(row.ID),
	).All(ctx)
	if err != nil {
		return err
	}
	for _, l := range later {
		if _, err := s.step(ctx, tx, step{
			shopID: l.ShopID, id: l.ID, from: []string{StatusPublished}, to: StatusReverted, kind: EvRevertedMark,
			actor: SystemActor(), detail: map[string]any{"reverted_by": row.ID.String()},
			set: func(u *ent.DesignRequestUpdate) { u.SetRevertedByRequestID(row.ID) },
		}); err != nil {
			return err
		}
	}
	return nil
}

// SiteStateInput is PUT /sites/{site}/state.
type SiteStateInput struct {
	MainSHA      string
	CoreVersion  string
	FloorVersion string
}

// PutSiteState records what the service sees on main (it never changes which
// request is current: only a recorded publish does).
func (s *Service) PutSiteState(ctx context.Context, shopID int, site string, in SiteStateInput) (*ent.DesignSiteState, error) {
	if !IsSite(site) {
		return nil, ErrInvalid("site must be storefront or admin")
	}
	if in.MainSHA != "" && !shaRE.MatchString(in.MainSHA) {
		return nil, ErrInvalid("main_sha must be a 40-character lowercase hex SHA")
	}
	if !versionRE.MatchString(in.CoreVersion) || !versionRE.MatchString(in.FloorVersion) {
		return nil, ErrInvalid("versions must be at most 20 characters of [0-9A-Za-z.+-]")
	}
	err := s.Client.DesignSiteState.Create().
		SetShopID(shopID).SetSite(site).SetLastSeenMainSha(in.MainSHA).SetCoreVersion(in.CoreVersion).
		SetDesignFloorVersion(in.FloorVersion).SetUpdatedAt(s.now()).
		OnConflictColumns(designsitestate.FieldShopID, designsitestate.FieldSite).
		UpdateLastSeenMainSha().UpdateCoreVersion().UpdateDesignFloorVersion().UpdateUpdatedAt().Exec(ctx)
	if err != nil {
		return nil, err
	}
	return s.Client.DesignSiteState.Query().Where(designsitestate.ShopIDEQ(shopID), designsitestate.SiteEQ(site)).Only(ctx)
}

// UntrackedInput is one commit found outside any request.
type UntrackedInput struct {
	Site         string
	CommitSHA    string
	CommittedAt  time.Time
	Subject      string
	FilesChanged int
	Additions    int
	Deletions    int
}

// ReportUntracked stores up to 100 untracked commits and returns how many were
// new (duplicates are ignored).
func (s *Service) ReportUntracked(ctx context.Context, shopID int, items []UntrackedInput) (int, error) {
	if len(items) == 0 || len(items) > 100 {
		return 0, ErrInvalid("send between 1 and 100 commits")
	}
	for _, it := range items {
		if !IsSite(it.Site) || !shaRE.MatchString(it.CommitSHA) || it.CommittedAt.IsZero() {
			return 0, ErrInvalid("each commit needs a valid site, commit_sha and committed_at")
		}
		if it.FilesChanged < 0 || it.Additions < 0 || it.Deletions < 0 {
			return 0, ErrInvalid("counts must not be negative")
		}
	}
	added := 0
	for _, it := range items {
		subj := firstLine(SanitizeText(it.Subject))
		if r := []rune(subj); len(r) > 200 {
			subj = string(r[:200])
		}
		exists, err := s.Client.DesignUntrackedChange.Query().Where(
			designuntrackedchange.ShopIDEQ(shopID), designuntrackedchange.SiteEQ(it.Site),
			designuntrackedchange.CommitShaEQ(it.CommitSHA)).Exist(ctx)
		if err != nil {
			return added, err
		}
		if exists {
			continue
		}
		_, err = s.Client.DesignUntrackedChange.Create().
			SetShopID(shopID).SetSite(it.Site).SetCommitSha(it.CommitSHA).SetCommittedAt(it.CommittedAt).
			SetSubject(subj).SetFilesChanged(it.FilesChanged).SetAdditions(it.Additions).SetDeletions(it.Deletions).
			SetDetectedAt(s.now()).Save(ctx)
		if isUnique(err, "design_untracked_changes_commit_key") {
			continue
		}
		if err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
