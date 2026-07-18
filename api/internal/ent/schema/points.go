// Package schema — member-tiers-and-points entities (change
// member-tiers-and-points, design D1–D10). Both tables below are shop-owned
// tenant data and are registered in api/internal/tenant.tenantOwned; each
// carries a direct shop_id column so the interceptor/hook can scope them
// (same convention as order/payment/shipping/cart/catalog).
package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// MemberTier is a merchant-configured loyalty tier (design D2/D7/D8):
// min_points is compared against shop_member.points — the member's current
// cached balance, not a separately-tracked lifetime-earned counter (design
// D2 — see that decision for why current balance is the correct axis given
// this phase has no point-spending feature). discount_percent is a reserved,
// currently-unused field (design Non-Goals: no discount-application logic
// this phase) for a future redemption/pricing feature.
type MemberTier struct {
	ent.Schema
}

func (MemberTier) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (MemberTier) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.String("name").MaxLen(100).SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}).NotEmpty(),
		// min_points (design D2/D7): the points.Service recompute helper
		// picks the highest-min_points tier whose min_points <= the member's
		// current shop_member.points.
		field.Int32("min_points").Default(0),
		// discount_percent (design Non-Goals): reserved, unused by any
		// business logic this phase — no code path reads it to compute a
		// discount.
		field.Int16("discount_percent").Optional().Nillable(),
	}
}

func (MemberTier) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
	}
}

func (MemberTier) Indexes() []ent.Index {
	return []ent.Index{
		// design D7: the level-recompute query orders by min_points desc
		// within a shop.
		index.Fields("shop_id", "min_points"),
	}
}

func (MemberTier) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "member_tiers"},
		entsql.Annotation{
			Checks: map[string]string{
				"member_tiers_min_points_check":       "min_points >= 0",
				"member_tiers_discount_percent_check": "discount_percent IS NULL OR (discount_percent >= 0 AND discount_percent <= 100)",
			},
		},
	}
}

// PointTransaction is one append-only ledger row (design D4/D6): the source
// of truth for a shop_member's points; shop_member.points is a maintained
// cache kept in lockstep by points.Service within the same ent.Tx as each
// insert here (design D6) — never recomputed by summing this table on read.
//
// kind (design D4) is the machine-checkable idempotency/business-logic axis
// (0 order award / 1 order-return clawback / 2 manual adjustment) — reason
// is the free-text human-readable audit label the data model calls for.
// order_id + kind carries a UNIQUE index: for kind 0/1 (always a real
// order_id) this is the authoritative guard against double-award/
// double-clawback, mirroring payments(provider, provider_reference) and
// shipments(order_id) being the final concurrency arbiter elsewhere in this
// codebase rather than an application-level read-then-write check (design
// D4). kind 2 (manual adjustment) always has order_id NULL, and Postgres's
// standard (non "NULLS NOT DISTINCT") unique index treats every NULL as
// distinct, so manual adjustments are never constrained by it.
type PointTransaction struct {
	ent.Schema
}

func (PointTransaction) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (PointTransaction) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("shop_member_id"),
		// order_id (design D4): NULL for manual adjustments (kind 2); always
		// set for award/clawback (kind 0/1). Real FK — mirrors Payment/
		// Shipment's order_id (this row's entire reason to exist, when
		// present, is "this ledger entry belongs to that order"), but
		// Optional here since manual adjustments have none.
		field.Int("order_id").Optional().Nillable(),
		// points_delta (design D5): signed; may legitimately be 0 for a
		// clawback row when nothing was left to claw back (still recorded so
		// the unique index below keeps guarding against reprocessing).
		field.Int32("points_delta"),
		// kind (design D4): 0 order award / 1 order-return clawback / 2
		// manual adjustment. See type doc comment for the full idempotency
		// rationale.
		field.Int16("kind"),
		field.String("reason").MaxLen(200).SchemaType(map[string]string{dialect.Postgres: "varchar(200)"}).NotEmpty(),
	}
}

func (PointTransaction) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		edge.To("shop_member", ShopMember.Type).Unique().Required().Field("shop_member_id"),
		// One-directional, optional FK to Order (see type doc comment for why
		// this mirrors Payment/Shipment's order_id edge but is not Required).
		// Unlike Payment/Shipment's Required order_id (default NO ACTION on
		// delete), ent generates ON DELETE SET NULL here because the field is
		// optional — a hypothetical future order-deletion path would clear
		// order_id on this row rather than being blocked or cascading the
		// ledger row away, preserving the points_delta/reason/shop_member_id
		// audit trail. No Ref/inverse edge on Order — declared entirely here,
		// so order.go does not need to change (mirrors Payment/Shipment's own
		// edges).
		edge.To("order", Order.Type).Unique().Field("order_id"),
	}
}

func (PointTransaction) Indexes() []ent.Index {
	return []ent.Index{
		// design D4: the authoritative "at most one award / at most one
		// clawback per order" guard. NULL order_id (manual adjustments) is
		// never constrained by it — see type doc comment.
		index.Fields("order_id", "kind").Unique(),
		index.Fields("shop_id", "shop_member_id"),
	}
}

func (PointTransaction) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "point_transactions"},
		entsql.Annotation{
			Checks: map[string]string{
				"point_transactions_kind_check": "kind IN (0, 1, 2)",
			},
		},
	}
}
