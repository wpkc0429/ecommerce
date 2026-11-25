package design

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/designrequest"
	"ksdevworks/ecommerce/api/internal/ent/designrequestevent"
	"ksdevworks/ecommerce/api/internal/ent/predicate"
)

// Authorizer answers "may this user do <node> in this shop" (internal/rbac).
type Authorizer interface {
	Allows(ctx context.Context, userID, shopID int, node string) (bool, error)
}

// Config is the policy of the design history (internal/config/design.go).
type Config struct {
	ApprovalTTL            time.Duration // how long an approval stays usable
	MinPreviewRemaining    time.Duration // preview must live at least this long at submit
	DailyRequestCap        int           // service-created requests per shop per UTC day
	ClaimLease             time.Duration
	ApprovalPendingTimeout time.Duration // awaiting_approval older than this fails
	// ScreenshotsEnabled is false in slice 1: no storage exists yet, so submit
	// answers 503 design_storage_not_configured (design D18, migration plan).
	ScreenshotsEnabled bool

	// Slice 2 (screenshots). Zero values mean the documented defaults.
	ShopQuotaBytes     int64         // stored screenshot bytes per shop (default none = unlimited in unit tests; config defaults 2 GiB)
	ScreenshotURLTTL   time.Duration // signed read URL lifetime (default 5m)
	ProcessConcurrency int           // concurrent PNG decode/encode (default 2)
}

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{
		ApprovalTTL: time.Hour, MinPreviewRemaining: 30 * time.Minute, DailyRequestCap: 20,
		ClaimLease: 15 * time.Minute, ApprovalPendingTimeout: 7 * 24 * time.Hour,
	}
}

// Service is the design-history domain service.
type Service struct {
	Client *ent.Client
	Authz  Authorizer
	Cfg    Config
	Log    *slog.Logger
	// Store is the PRIVATE screenshot bucket; nil = not configured (uploads and
	// reads answer 503 design_storage_not_configured).
	Store ObjectStore
	// Now is the clock; nil means time.Now. Tests inject a fake one.
	Now func() time.Time

	semOnce sync.Once
	sem     chan struct{}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return Micros(s.Now())
	}
	return Micros(time.Now())
}

// Actor is whoever caused an event.
type Actor struct {
	Type   string
	UserID *int
	CredID *int
}

// UserActor, ServiceActor and SystemActor build the three actor kinds.
func UserActor(id int) Actor    { return Actor{Type: ActorUser, UserID: &id} }
func ServiceActor(id int) Actor { return Actor{Type: ActorService, CredID: &id} }
func SystemActor() Actor        { return Actor{Type: ActorSystem} }

func (s *Service) inTx(ctx context.Context, fn func(tx *ent.Tx) error) error {
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// step is one state-machine move: a conditional UPDATE of the request row and
// the event that records it, in the caller's transaction. It is the only code
// that changes a request's status or rev.
type step struct {
	shopID int
	id     uuid.UUID
	from   []string // the statuses the move is legal from (the UPDATE's WHERE)
	to     string   // "" = the status does not change (an event only)
	kind   string
	actor  Actor
	detail map[string]any
	// headSHA overrides the event's head_sha (default: the request's head).
	headSHA string
	extra   []predicate.DesignRequest
	set     func(u *ent.DesignRequestUpdate)
}

// errStale is returned by step when the conditional UPDATE matched no row;
// callers translate it into the precise error for their endpoint.
var errStale = errors.New("design: request changed underneath")

func (s *Service) step(ctx context.Context, tx *ent.Tx, st step) (*ent.DesignRequest, error) {
	if st.to != "" {
		for _, f := range st.from {
			if f != st.to && !Allowed(f, st.to, st.actor.Type) {
				return nil, fmt.Errorf("design: %s may not move %s -> %s", st.actor.Type, f, st.to)
			}
		}
	}
	now := s.now()
	// Lock the row first: it serializes writers of one request and tells us the
	// status the event moves from (the UPDATE's WHERE still decides legality).
	before, err := tx.DesignRequest.Query().Where(designrequest.IDEQ(st.id), designrequest.ShopIDEQ(st.shopID)).ForUpdate().Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound()
	}
	if err != nil {
		return nil, err
	}
	preds := []predicate.DesignRequest{
		designrequest.IDEQ(st.id), designrequest.ShopIDEQ(st.shopID), designrequest.StatusIn(st.from...),
	}
	preds = append(preds, st.extra...)
	u := tx.DesignRequest.Update().Where(preds...).AddRev(1).SetUpdatedAt(now)
	if st.to != "" {
		u.SetStatus(st.to)
	}
	if st.set != nil {
		st.set(u)
	}
	n, err := u.Save(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errStale
	}
	row, err := tx.DesignRequest.Query().Where(designrequest.IDEQ(st.id), designrequest.ShopIDEQ(st.shopID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	from := before.Status
	head := st.headSHA
	if head == "" {
		head = row.HeadSha
	}
	if err := s.appendEvent(ctx, tx, row, st.kind, from, st.to, st.actor, head, st.detail, now); err != nil {
		return nil, err
	}
	return row, nil
}

// appendEvent writes event number row.Rev, chained to number row.Rev-1. The
// caller holds the request row (its UPDATE or INSERT in this transaction), so
// no other writer can interleave a sequence number.
func (s *Service) appendEvent(ctx context.Context, tx *ent.Tx, row *ent.DesignRequest, kind, from, to string, a Actor, head string, detail map[string]any, at time.Time) error {
	prev := GenesisHash
	if row.Rev > 1 {
		p, err := tx.DesignRequestEvent.Query().
			Where(designrequestevent.RequestIDEQ(row.ID), designrequestevent.SeqEQ(row.Rev-1)).Only(ctx)
		if err != nil {
			return fmt.Errorf("design: previous event: %w", err)
		}
		prev = p.Hash
	}
	if detail == nil {
		detail = map[string]any{}
	}
	rawDetail, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	rawDetail, err = CanonicalJSON(rawDetail)
	if err != nil {
		return err
	}
	rec := EventRecord{
		ShopID: row.ShopID, RequestID: row.ID.String(), Seq: row.Rev, Kind: kind, From: from, To: to,
		ActorType: a.Type, ActorUser: a.UserID, ActorCred: a.CredID, HeadSHA: head, Detail: rawDetail,
		CreatedAt: at, PrevHash: prev,
	}
	h, err := ComputeHash(rec)
	if err != nil {
		return err
	}
	c := tx.DesignRequestEvent.Create().
		SetShopID(row.ShopID).SetRequestID(row.ID).SetSeq(row.Rev).SetKind(kind).
		SetFromStatus(from).SetToStatus(to).SetActorType(a.Type).SetHeadSha(head).
		SetDetail(rawDetail).SetPrevHash(prev).SetHash(h).SetCreatedAt(Micros(at))
	if a.UserID != nil {
		c.SetActorUserID(*a.UserID)
	}
	if a.CredID != nil {
		c.SetActorCredentialID(*a.CredID)
	}
	_, err = c.Save(ctx)
	return err
}

// load reads a request of the shop (404 for a request of another shop).
func (s *Service) load(ctx context.Context, shopID int, id uuid.UUID) (*ent.DesignRequest, error) {
	row, err := s.Client.DesignRequest.Query().Where(designrequest.IDEQ(id), designrequest.ShopIDEQ(shopID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound()
	}
	return row, err
}

// Get returns one request of the shop.
func (s *Service) Get(ctx context.Context, shopID int, id uuid.UUID) (*ent.DesignRequest, error) {
	return s.load(ctx, shopID, id)
}

// staleErr turns "the conditional UPDATE matched nothing" into the most precise
// conflict: the request is gone (404) or in a state that does not allow this.
func (s *Service) staleErr(ctx context.Context, shopID int, id uuid.UUID, what string) error {
	row, err := s.load(ctx, shopID, id)
	if err != nil {
		return err
	}
	return ErrInvalidState(fmt.Sprintf("%s: the request is %s", what, row.Status))
}

// wrap maps a step error to a domain error.
func (s *Service) wrap(ctx context.Context, shopID int, id uuid.UUID, what string, err error) error {
	if errors.Is(err, errStale) {
		return s.staleErr(ctx, shopID, id, what)
	}
	return err
}

func conflict(code, msg string) *Error { return newErr(http.StatusConflict, code, msg) }

func isUnique(err error, constraint string) bool {
	return err != nil && ent.IsConstraintError(err) && strings.Contains(err.Error(), constraint)
}

// DayStart is midnight UTC of t's day (the daily request cap window).
func DayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
