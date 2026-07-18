// Package points implements the member-tiers-and-points domain (change
// member-tiers-and-points): merchant-configured member tiers, an
// append-only points ledger (point_transactions), and the event-driven
// reactions that award points on payment success and claw them back on
// order return.
//
// This package deliberately never imports order/payment/shipping — it
// depends on *ent.Client only and reacts exclusively to events published on
// the shared events.Dispatcher (design D1): HandleOrderPaid/
// HandleOrderReturned/Handle are dispatcher-facing, everything else is a
// plain shop-scoped service method in the same style as order.Service/
// payment.Service/shipping.Service (explicit shopID/shopMemberID params,
// never ambient context).
package points

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	entsql "entgo.io/ent/dialect/sql"

	"ksdevworks/ecommerce/api/internal/ent"
	entmembertier "ksdevworks/ecommerce/api/internal/ent/membertier"
	entpointtransaction "ksdevworks/ecommerce/api/internal/ent/pointtransaction"
	entshopmember "ksdevworks/ecommerce/api/internal/ent/shopmember"
	"ksdevworks/ecommerce/api/internal/events"
)

// ErrNotFound marks missing resources (handler → 404). Also returned when a
// shopMemberID/tierID exists but belongs to a different shop — callers must
// not distinguish "doesn't exist" from "not yours" (mirrors order.ErrNotFound
// et al.'s convention).
var ErrNotFound = errors.New("points: not found")

// Detail locates one validation problem (JSON Pointer-ish; same convention
// as order.Detail/payment.Detail/shipping.Detail).
type Detail struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

// ValidationError carries 422 payloads (design D12 error envelope, reused
// from order/payment/shipping's shared error envelope convention).
type ValidationError struct {
	Message string
	Details []Detail
}

func (e *ValidationError) Error() string { return e.Message }

func validationErr(pointer, msg string) *ValidationError {
	return &ValidationError{Message: msg, Details: []Detail{{Pointer: pointer, Message: msg}}}
}

// point_transactions.kind (design D4) — the machine-checkable idempotency/
// business-logic axis; reason is the free-text human-readable audit label.
const (
	KindOrderAward       int16 = 0
	KindOrderClawback    int16 = 1
	KindManualAdjustment int16 = 2
)

const (
	reasonOrderAward    = "訂單付款回饋"
	reasonOrderClawback = "訂單退貨扣回"
)

// pointsPerMinorUnit is the platform-wide, hardcoded award ratio (design
// D3): 1 point per 100 minor currency units of orders.total_amount,
// integer division (floor). Not configurable per shop (design Non-Goals).
const pointsPerMinorUnit = 100

// Service implements member-tiers-and-points operations, scoped by explicit
// shopID/shopMemberID/memberID parameters (never ambient tenant/member
// context — same convention as order.Service/payment.Service/
// shipping.Service).
type Service struct {
	Client *ent.Client
	// Log is used only by Handle (design Risks: a dispatcher Handler has no
	// error return, so failures are logged and swallowed, mirroring
	// render.Invalidator's own limitation). Optional — nil is safe (falls
	// back to slog.Default()).
	Log *slog.Logger
}

func (s *Service) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// ── member tiers CRUD (merchant back office) ───────────────────────────

// MemberTierInput is the payload of member tier creation.
type MemberTierInput struct {
	Name            string `json:"name"`
	MinPoints       int32  `json:"min_points"`
	DiscountPercent *int16 `json:"discount_percent"`
}

// MemberTierUpdate is a partial update — nil fields are left unchanged.
type MemberTierUpdate struct {
	Name            *string `json:"name"`
	MinPoints       *int32  `json:"min_points"`
	DiscountPercent *int16  `json:"discount_percent"`
}

func validateDiscountPercent(v int16) error {
	if v < 0 || v > 100 {
		return validationErr("/discount_percent", "discount_percent must be between 0 and 100")
	}
	return nil
}

func (s *Service) CreateMemberTier(ctx context.Context, shopID int, in MemberTierInput) (*ent.MemberTier, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, validationErr("/name", "name is required")
	}
	if in.MinPoints < 0 {
		return nil, validationErr("/min_points", "min_points must be >= 0")
	}
	create := s.Client.MemberTier.Create().SetShopID(shopID).SetName(name).SetMinPoints(in.MinPoints)
	if in.DiscountPercent != nil {
		if err := validateDiscountPercent(*in.DiscountPercent); err != nil {
			return nil, err
		}
		create.SetDiscountPercent(*in.DiscountPercent)
	}
	mt, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("points: create member tier: %w", err)
	}
	return mt, nil
}

func (s *Service) GetMemberTier(ctx context.Context, shopID, id int) (*ent.MemberTier, error) {
	mt, err := s.Client.MemberTier.Query().
		Where(entmembertier.IDEQ(id), entmembertier.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	return mt, nil
}

// ListMemberTiers returns every member tier within shopID, ordered by
// min_points ascending (a natural ladder display order).
func (s *Service) ListMemberTiers(ctx context.Context, shopID int) ([]*ent.MemberTier, error) {
	rows, err := s.Client.MemberTier.Query().
		Where(entmembertier.ShopIDEQ(shopID)).
		Order(entmembertier.ByMinPoints()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("points: list member tiers: %w", err)
	}
	return rows, nil
}

func (s *Service) UpdateMemberTier(ctx context.Context, shopID, id int, in MemberTierUpdate) (*ent.MemberTier, error) {
	mt, err := s.Client.MemberTier.Query().
		Where(entmembertier.IDEQ(id), entmembertier.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	upd := mt.Update()
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, validationErr("/name", "name cannot be empty")
		}
		upd.SetName(name)
	}
	if in.MinPoints != nil {
		if *in.MinPoints < 0 {
			return nil, validationErr("/min_points", "min_points must be >= 0")
		}
		upd.SetMinPoints(*in.MinPoints)
	}
	if in.DiscountPercent != nil {
		if err := validateDiscountPercent(*in.DiscountPercent); err != nil {
			return nil, err
		}
		upd.SetDiscountPercent(*in.DiscountPercent)
	}
	mt, err = upd.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("points: update member tier: %w", err)
	}
	return mt, nil
}

// DeleteMemberTier deletes a member tier. Any shop_member currently holding
// it via level_id has that field cleared to NULL by the DB's ON DELETE SET
// NULL FK (design D7) — not application logic here.
func (s *Service) DeleteMemberTier(ctx context.Context, shopID, id int) error {
	mt, err := s.Client.MemberTier.Query().
		Where(entmembertier.IDEQ(id), entmembertier.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return ErrNotFound
	}
	return s.Client.MemberTier.DeleteOne(mt).Exec(ctx)
}

// ── level recompute (design D7) ─────────────────────────────────────────

// recomputeLevel re-evaluates shop_member.level_id against member_tiers
// after any balance change: the highest-min_points tier whose min_points <=
// balance wins; no qualifying tier clears level_id to NULL. Runs inside the
// caller's tx (design D6 — always part of the same transaction as the
// ledger insert + balance update).
func recomputeLevel(ctx context.Context, tx *ent.Tx, shopID, shopMemberID int, balance int32) error {
	tier, err := tx.MemberTier.Query().
		Where(entmembertier.ShopIDEQ(shopID), entmembertier.MinPointsLTE(balance)).
		Order(entmembertier.ByMinPoints(entsql.OrderDesc())).
		First(ctx)
	switch {
	case ent.IsNotFound(err):
		if _, err := tx.ShopMember.UpdateOneID(shopMemberID).ClearLevelID().Save(ctx); err != nil {
			return fmt.Errorf("points: clear level: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("points: query qualifying tier: %w", err)
	default:
		if _, err := tx.ShopMember.UpdateOneID(shopMemberID).SetLevelID(tier.ID).Save(ctx); err != nil {
			return fmt.Errorf("points: set level: %w", err)
		}
		return nil
	}
}

// ── event reactions (design D1/D4/D5) ───────────────────────────────────

// Handle is subscribed to the events dispatcher (mirrors
// render.Invalidator.Handle's subscription pattern exactly). No error
// return — failures are logged and swallowed (design Risks), consistent
// with the dispatcher's existing sole consumer.
func (s *Service) Handle(ctx context.Context, e events.Event) {
	switch ev := e.(type) {
	case events.OrderPaymentSucceeded:
		if err := s.HandleOrderPaid(ctx, ev); err != nil {
			s.logger().Error("points: award on payment failed", "shop_id", ev.ShopID, "order_id", ev.OrderID, "err", err)
		}
	case events.OrderReturned:
		if err := s.HandleOrderReturned(ctx, ev); err != nil {
			s.logger().Error("points: clawback on return failed", "shop_id", ev.ShopID, "order_id", ev.OrderID, "err", err)
		}
	}
}

// HandleOrderPaid idempotently awards points for a paid order (design
// D3/D4): points = TotalAmount / pointsPerMinorUnit (floor). A cheap
// existence pre-check short-circuits the common repeated-delivery case;
// the (order_id, kind) unique index is the actual arbiter — a constraint
// violation on insert means another delivery already won and this call is a
// safe no-op.
func (s *Service) HandleOrderPaid(ctx context.Context, ev events.OrderPaymentSucceeded) error {
	already, err := s.Client.PointTransaction.Query().
		Where(entpointtransaction.OrderIDEQ(ev.OrderID), entpointtransaction.KindEQ(KindOrderAward)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("points: check existing award: %w", err)
	}
	if already {
		return nil
	}

	sm, err := s.Client.ShopMember.Query().
		Where(entshopmember.ShopIDEQ(ev.ShopID), entshopmember.MemberIDEQ(ev.MemberID)).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("points: query shop_member for award: %w", err)
	}

	awarded := int32(ev.TotalAmount / pointsPerMinorUnit)

	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("points: begin tx: %w", err)
	}
	if _, err := tx.PointTransaction.Create().
		SetShopID(ev.ShopID).SetShopMemberID(sm.ID).SetOrderID(ev.OrderID).
		SetPointsDelta(awarded).SetKind(KindOrderAward).SetReason(reasonOrderAward).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		if ent.IsConstraintError(err) {
			// Lost the (order_id, kind) idempotency race (design D4) —
			// another delivery already recorded the award. Safe no-op.
			return nil
		}
		return fmt.Errorf("points: create award transaction: %w", err)
	}
	if _, err := tx.ShopMember.UpdateOneID(sm.ID).AddPoints(awarded).Save(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("points: update balance: %w", err)
	}
	if err := recomputeLevel(ctx, tx, ev.ShopID, sm.ID, sm.Points+awarded); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("points: commit award: %w", err)
	}
	return nil
}

// HandleOrderReturned idempotently claws back the points awarded for a
// returned order, capped at the member's current balance (design D5): never
// drives shop_member.points negative. If the order was never awarded any
// points (e.g. shipped unpaid), the clawback amount is 0 — a zero-delta row
// is still recorded so the (order_id, kind) unique index keeps guarding
// against reprocessing.
func (s *Service) HandleOrderReturned(ctx context.Context, ev events.OrderReturned) error {
	already, err := s.Client.PointTransaction.Query().
		Where(entpointtransaction.OrderIDEQ(ev.OrderID), entpointtransaction.KindEQ(KindOrderClawback)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("points: check existing clawback: %w", err)
	}
	if already {
		return nil
	}

	var awarded int32
	awardRow, err := s.Client.PointTransaction.Query().
		Where(entpointtransaction.OrderIDEQ(ev.OrderID), entpointtransaction.KindEQ(KindOrderAward)).
		Only(ctx)
	switch {
	case ent.IsNotFound(err):
		awarded = 0
	case err != nil:
		return fmt.Errorf("points: query award transaction: %w", err)
	default:
		awarded = awardRow.PointsDelta
	}

	sm, err := s.Client.ShopMember.Query().
		Where(entshopmember.ShopIDEQ(ev.ShopID), entshopmember.MemberIDEQ(ev.MemberID)).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("points: query shop_member for clawback: %w", err)
	}

	clawback := awarded
	if sm.Points < clawback {
		clawback = sm.Points
	}
	if clawback < 0 {
		clawback = 0
	}

	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("points: begin tx: %w", err)
	}
	if _, err := tx.PointTransaction.Create().
		SetShopID(ev.ShopID).SetShopMemberID(sm.ID).SetOrderID(ev.OrderID).
		SetPointsDelta(-clawback).SetKind(KindOrderClawback).SetReason(reasonOrderClawback).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		if ent.IsConstraintError(err) {
			// Lost the (order_id, kind) idempotency race (design D4).
			return nil
		}
		return fmt.Errorf("points: create clawback transaction: %w", err)
	}
	if clawback != 0 {
		if _, err := tx.ShopMember.UpdateOneID(sm.ID).AddPoints(-clawback).Save(ctx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("points: update balance: %w", err)
		}
	}
	if err := recomputeLevel(ctx, tx, ev.ShopID, sm.ID, sm.Points-clawback); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("points: commit clawback: %w", err)
	}
	return nil
}

// ── manual adjustment (merchant back office, design D10) ───────────────

// AdjustPoints applies a merchant-initiated manual point adjustment
// (customer-service compensation etc.): rejected outright (422) if the
// resulting balance would go negative — unlike HandleOrderReturned's
// clawback, this is a direct merchant action and is never silently capped
// (design D10).
func (s *Service) AdjustPoints(ctx context.Context, shopID, shopMemberID int, delta int32, reason string) (*ent.ShopMember, error) {
	reason = strings.TrimSpace(reason)
	if delta == 0 {
		return nil, validationErr("/points_delta", "points_delta must not be zero")
	}
	if reason == "" {
		return nil, validationErr("/reason", "reason is required")
	}

	sm, err := s.Client.ShopMember.Query().
		Where(entshopmember.IDEQ(shopMemberID), entshopmember.ShopIDEQ(shopID)).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}

	newBalance := sm.Points + delta
	if newBalance < 0 {
		return nil, validationErr("/points_delta", "adjustment would drive points negative")
	}

	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("points: begin tx: %w", err)
	}
	if _, err := tx.PointTransaction.Create().
		SetShopID(shopID).SetShopMemberID(sm.ID).
		SetPointsDelta(delta).SetKind(KindManualAdjustment).SetReason(reason).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("points: create adjustment transaction: %w", err)
	}
	if _, err := tx.ShopMember.UpdateOneID(sm.ID).AddPoints(delta).Save(ctx); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("points: update balance: %w", err)
	}
	if err := recomputeLevel(ctx, tx, shopID, sm.ID, newBalance); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("points: commit adjustment: %w", err)
	}

	updated, err := s.Client.ShopMember.Query().
		Where(entshopmember.IDEQ(sm.ID)).
		WithMemberTier().
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("points: reload shop_member: %w", err)
	}
	return updated, nil
}

// ── reads (merchant back office + member self-service) ─────────────────

// ListParams are pagination inputs for a ledger listing (mirrors
// order.ListParams).
type ListParams struct {
	Page     int
	PageSize int
}

// TransactionPage is a page of point_transactions plus pagination metadata
// (mirrors order.OrderPage).
type TransactionPage struct {
	Transactions []*ent.PointTransaction
	Total        int
	Page         int
	PageSize     int
}

func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	switch {
	case pageSize <= 0:
		pageSize = 20
	case pageSize > 100:
		pageSize = 100
	}
	return page, pageSize
}

// GetMemberPointsAdmin returns shopMemberID's balance/tier within shopID
// (merchant back office — no member-identity check, the caller's shop
// access was already enforced by the RBAC middleware, mirrors
// order.Service.GetOrderAdmin).
func (s *Service) GetMemberPointsAdmin(ctx context.Context, shopID, shopMemberID int) (*ent.ShopMember, error) {
	sm, err := s.Client.ShopMember.Query().
		Where(entshopmember.IDEQ(shopMemberID), entshopmember.ShopIDEQ(shopID)).
		WithMemberTier().
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	return sm, nil
}

// ListTransactionsAdmin returns shopMemberID's ledger within shopID, newest
// first, paginated. Verifies the member itself is visible in shopID first
// (mirrors payment.Service.ListForOrderAdmin/shipping.Service.
// ListShipmentsAdmin's "confirm scope before listing" convention).
func (s *Service) ListTransactionsAdmin(ctx context.Context, shopID, shopMemberID int, params ListParams) (*TransactionPage, error) {
	if _, err := s.GetMemberPointsAdmin(ctx, shopID, shopMemberID); err != nil {
		return nil, err
	}
	return s.listTransactions(ctx, shopID, shopMemberID, params)
}

// GetMemberPointsSelf returns the caller's own balance/tier, resolved from
// (shopID, memberID) — never accepts a caller-supplied shop_member_id
// (member self-service convention, mirrors order.Service.GetOrder).
func (s *Service) GetMemberPointsSelf(ctx context.Context, shopID, memberID int) (*ent.ShopMember, error) {
	sm, err := s.Client.ShopMember.Query().
		Where(entshopmember.ShopIDEQ(shopID), entshopmember.MemberIDEQ(memberID)).
		WithMemberTier().
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	return sm, nil
}

// ListTransactionsSelf returns the caller's own ledger, resolved from
// (shopID, memberID) — never accepts a caller-supplied shop_member_id.
func (s *Service) ListTransactionsSelf(ctx context.Context, shopID, memberID int, params ListParams) (*TransactionPage, error) {
	sm, err := s.Client.ShopMember.Query().
		Where(entshopmember.ShopIDEQ(shopID), entshopmember.MemberIDEQ(memberID)).
		Only(ctx)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.listTransactions(ctx, shopID, sm.ID, params)
}

func (s *Service) listTransactions(ctx context.Context, shopID, shopMemberID int, params ListParams) (*TransactionPage, error) {
	page, pageSize := normalizePage(params.Page, params.PageSize)
	q := s.Client.PointTransaction.Query().
		Where(entpointtransaction.ShopIDEQ(shopID), entpointtransaction.ShopMemberIDEQ(shopMemberID))
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("points: count transactions: %w", err)
	}
	rows, err := q.
		Order(entpointtransaction.ByID(entsql.OrderDesc())).
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("points: list transactions: %w", err)
	}
	return &TransactionPage{Transactions: rows, Total: total, Page: page, PageSize: pageSize}, nil
}
