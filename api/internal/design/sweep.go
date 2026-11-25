package design

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/designrequest"
	"ksdevworks/ecommerce/api/internal/ent/designrequestevent"
	"ksdevworks/ecommerce/api/internal/ent/designscreenshot"
	"ksdevworks/ecommerce/api/internal/ent/predicate"
)

type predicateReq = predicate.DesignRequest

// SweepReport counts what a sweep did (or would do).
type SweepReport struct {
	LeasesLost       int // claim lease ran out -> failed(service_lost)
	ApprovalTimeouts int // awaiting_approval too long -> failed(approval_timeout)
	ApprovalExpired  int // approved but not published in time -> failed(approval_expired)
	Broken           []BrokenChain
	Verified         int
}

// BrokenChain names a request whose event chain does not verify.
type BrokenChain struct {
	RequestID uuid.UUID
	Seq       int
	Reason    string
}

// Sweep moves stuck requests to failed with a system event (apply=false only
// counts). shopID 0 means every shop.
func (s *Service) Sweep(ctx context.Context, shopID int, apply bool) (*SweepReport, error) {
	rep := &SweepReport{}
	now := s.now()
	scope := func(q *ent.DesignRequestQuery) *ent.DesignRequestQuery {
		if shopID > 0 {
			q.Where(designrequest.ShopIDEQ(shopID))
		}
		return q
	}
	type job struct {
		dst    *int
		q      *ent.DesignRequestQuery
		from   []string
		kind   string
		code   string
		detail string
	}
	jobs := []job{
		{&rep.LeasesLost, scope(s.Client.DesignRequest.Query()).Where(
			designrequest.StatusIn(StatusDrafting, StatusPreviewing), designrequest.ClaimExpiresAtLT(now)),
			[]string{StatusDrafting, StatusPreviewing}, EvFailed, "service_lost", "the worker's claim lease expired"},
		{&rep.ApprovalTimeouts, scope(s.Client.DesignRequest.Query()).Where(
			designrequest.StatusEQ(StatusAwaitingApproval), designrequest.UpdatedAtLT(now.Add(-s.Cfg.ApprovalPendingTimeout))),
			[]string{StatusAwaitingApproval}, EvFailed, "approval_timeout", "nobody approved it in time"},
		{&rep.ApprovalExpired, scope(s.Client.DesignRequest.Query()).Where(
			designrequest.StatusEQ(StatusApproved), designrequest.ApprovalExpiresAtLT(now)),
			[]string{StatusApproved}, EvExpired, "approval_expired", "the approval expired before publishing"},
	}
	for _, j := range jobs {
		rows, err := j.q.All(ctx)
		if err != nil {
			return rep, err
		}
		for _, r := range rows {
			if !apply {
				*j.dst++
				continue
			}
			code, detail := j.code, j.detail
			err := s.inTx(ctx, func(tx *ent.Tx) error {
				_, err := s.step(ctx, tx, step{
					shopID: r.ShopID, id: r.ID, from: j.from, to: StatusFailed, kind: j.kind, actor: SystemActor(),
					detail: map[string]any{"code": code},
					extra:  stuckGuard(j.code, now, s.Cfg.ApprovalPendingTimeout),
					set:    func(u *ent.DesignRequestUpdate) { u.SetFailureCode(code).SetFailureDetail(detail) },
				})
				return err
			})
			if err == errStale {
				continue // it moved on by itself in the meantime
			}
			if err != nil {
				return rep, err
			}
			*j.dst++
		}
	}
	return rep, nil
}

// stuckGuard repeats the selection condition inside the UPDATE so a request
// that progressed between the scan and the update is left alone.
func stuckGuard(code string, now time.Time, pending time.Duration) []predicateReq {
	switch code {
	case "service_lost":
		return []predicateReq{designrequest.ClaimExpiresAtLT(now)}
	case "approval_timeout":
		return []predicateReq{designrequest.UpdatedAtLT(now.Add(-pending))}
	default:
		return []predicateReq{designrequest.ApprovalExpiresAtLT(now)}
	}
}

// VerifyChains recomputes every request's event hash chain.
func (s *Service) VerifyChains(ctx context.Context, shopID int) (*SweepReport, error) {
	rep := &SweepReport{}
	q := s.Client.DesignRequest.Query().Order(ent.Asc(designrequest.FieldCreatedAt), ent.Asc(designrequest.FieldID))
	if shopID > 0 {
		q.Where(designrequest.ShopIDEQ(shopID))
	}
	reqs, err := q.All(ctx)
	if err != nil {
		return rep, err
	}
	for _, r := range reqs {
		evs, err := s.Client.DesignRequestEvent.Query().Where(designrequestevent.RequestIDEQ(r.ID)).
			Order(ent.Asc(designrequestevent.FieldSeq)).All(ctx)
		if err != nil {
			return rep, err
		}
		recs := make([]EventRecord, 0, len(evs))
		for _, e := range evs {
			recs = append(recs, EventRecord{
				ShopID: e.ShopID, RequestID: e.RequestID.String(), Seq: e.Seq, Kind: e.Kind, From: e.FromStatus, To: e.ToStatus,
				ActorType: e.ActorType, ActorUser: e.ActorUserID, ActorCred: e.ActorCredentialID, HeadSHA: e.HeadSha,
				Detail: e.Detail, CreatedAt: e.CreatedAt, PrevHash: e.PrevHash, StoredHash: e.Hash,
			})
		}
		if seq, why := VerifyChain(recs); seq != 0 {
			rep.Broken = append(rep.Broken, BrokenChain{RequestID: r.ID, Seq: seq, Reason: why})
			continue
		}
		if len(recs) != r.Rev {
			rep.Broken = append(rep.Broken, BrokenChain{RequestID: r.ID, Seq: len(recs), Reason: "events do not reach the request's rev (a tail was removed)"})
			continue
		}
		rep.Verified++
	}
	return rep, nil
}

// deleter adapts a variadic-delete object store to ObjectDeleter.
type deleter struct{ s ObjectStore }

func (d deleter) Delete(ctx context.Context, key string) error { return d.s.Delete(ctx, key) }

// DeleterOf wraps an ObjectStore as the purge command's ObjectDeleter.
func DeleterOf(s ObjectStore) ObjectDeleter { return deleter{s} }

// OrphanReport compares the bucket with the screenshots table. It only reads.
type OrphanReport struct {
	Objects    int      // objects listed under the prefix
	Referenced int      // of those, referenced by a row that still holds its object
	Orphans    []string // objects no live row points at (upload that lost its row, purge that lost its delete)
	Missing    int      // live rows whose object is not in the bucket
}

// Orphans lists the private bucket (shop 0 = every shop) and diffs it against
// design_screenshots.
func (s *Service) Orphans(ctx context.Context, shop int) (*OrphanReport, error) {
	if !s.storageOn() {
		return nil, errStorageNotConfigured()
	}
	prefix := "shops/"
	if shop > 0 {
		prefix = fmt.Sprintf("shops/%d/design/", shop)
	}
	keys, err := s.Store.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	q := s.Client.DesignScreenshot.Query().Where(designscreenshot.PurgedAtIsNil())
	if shop > 0 {
		q = q.Where(designscreenshot.ShopIDEQ(shop))
	}
	live, err := q.Select(designscreenshot.FieldObjectKey).Strings(ctx)
	if err != nil {
		return nil, err
	}
	liveSet := make(map[string]bool, len(live))
	for _, k := range live {
		liveSet[k] = true
	}
	rep := &OrphanReport{Objects: len(keys)}
	inBucket := make(map[string]bool, len(keys))
	for _, k := range keys {
		inBucket[k] = true
		if liveSet[k] {
			rep.Referenced++
		} else {
			rep.Orphans = append(rep.Orphans, k)
		}
	}
	for _, k := range live {
		if !inBucket[k] {
			rep.Missing++
		}
	}
	return rep, nil
}
