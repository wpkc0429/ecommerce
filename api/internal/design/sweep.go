package design

import (
	"context"
	"time"

	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/designrequest"
	"ksdevworks/ecommerce/api/internal/ent/designrequestevent"
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
