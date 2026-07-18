// Package schema — shipping-logistics entities (change shipping-logistics,
// design D1–D10). Both tables below are shop-owned tenant data and are
// registered in api/internal/tenant.tenantOwned; each carries a direct
// shop_id column so the interceptor/hook can scope them (same convention as
// order/payment/cart/catalog).
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

// ShippingMethod is a merchant-configured shipping option (design D1):
// name/carrier are free-text display fields, flat_rate is a single fixed
// fee (no weight/zone rate calculation this phase). is_active lets a
// merchant retire a method without deleting it (and its historical
// association, if any is ever added later).
type ShippingMethod struct {
	ent.Schema
}

func (ShippingMethod) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (ShippingMethod) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.String("name").MaxLen(100).SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}).NotEmpty(),
		// carrier (design D1): the carrier's display name, e.g. "黑貓宅急便",
		// "7-11 超商取貨". Plain text, not an enum/FK — this phase has no
		// carrier-specific program logic to dispatch on (no real carrier API
		// integration; see design Non-Goals).
		field.String("carrier").MaxLen(100).SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}).NotEmpty(),
		// Money: integer minor units, BIGINT/int64 (product-catalog design
		// D1 convention). A single fixed fee — never float, never a
		// weight/zone-derived calculation this phase.
		field.Int64("flat_rate"),
		field.Bool("is_active").Default(true),
	}
}

func (ShippingMethod) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
	}
}

func (ShippingMethod) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id", "is_active"),
	}
}

func (ShippingMethod) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "shipping_methods"},
		entsql.Annotation{
			Checks: map[string]string{
				"shipping_methods_flat_rate_check": "flat_rate >= 0",
			},
		},
	}
}

// Shipment is the (at most one per order) shipment record for an Order
// (design D3/D4). Unlike OrderItem's product_id/sku_id (deliberately
// edge-less informational snapshots), Shipment's order_id is a real,
// required FK — mirrors Payment.order_id exactly, same reasoning: a
// shipment record's entire reason to exist is "this parcel belongs to that
// order", not a point-in-time display snapshot. No OnDelete annotation is
// set, so Postgres's default NO ACTION applies — deleting an order with an
// existing shipment is blocked rather than cascading it away (audit trail).
//
// status (design D2) deliberately reuses orders.fulfillment_status's
// non-zero values (1 shipped / 2 delivered / 3 returned) rather than
// inventing a parallel enum — see shipping.ShippedStatus/DeliveredStatus/
// ReturnedStatus. A shipments row only ever comes into existence at the
// moment of shipping (creating the row IS the shipping event — there is no
// separate "draft" step), so there is no "0/unfulfilled" value to represent
// here: a nonexistent row already means unfulfilled.
//
// order_id carries a UNIQUE index (design D4): the authoritative guard for
// "at most one shipment per order" — order.Service.UpdateFulfillmentStatus
// has no CAS semantics of its own, so this unique index (not application-
// layer read-then-write logic) is what a concurrent double-ship attempt
// ultimately collides against.
type Shipment struct {
	ent.Schema
}

func (Shipment) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Shipment) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("order_id"),
		field.String("carrier").MaxLen(100).SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}).NotEmpty(),
		// tracking_number (design D3): nullable — only present once actually
		// shipped, but the shipment row itself already only exists once
		// shipped (see type doc comment), so in practice this is nullable to
		// accommodate carriers/flows where the tracking number is registered
		// slightly after the parcel physically leaves (e.g. convenience-store
		// drop-off counters), not a distinct lifecycle stage.
		field.String("tracking_number").MaxLen(191).SchemaType(map[string]string{dialect.Postgres: "varchar(191)"}).Optional().Nillable(),
		// status (design D2): 1 shipped / 2 delivered / 3 returned — the same
		// non-zero values as orders.fulfillment_status. Set at creation time
		// to ShippedStatus; the only program-driven transitions are
		// shipped->delivered and shipped->returned (both terminal) via
		// shipping.Service.AdvanceShipment.
		field.Int16("status"),
		// shipped_at (design D3): set at creation time (creating the row IS
		// the shipping event).
		field.Time("shipped_at").Optional().Nillable(),
		// delivered_at (design D3): set only when AdvanceShipment transitions
		// to DeliveredStatus. No separate returned_at column — updated_at
		// (TimeMixin) already marks a returned transition's timestamp, and a
		// single terminal-status branch doesn't warrant its own nullable
		// column.
		field.Time("delivered_at").Optional().Nillable(),
	}
}

func (Shipment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		// One-directional real FK to Order (see type doc comment). No Ref/
		// inverse edge on Order — declared entirely here, so order.go does
		// not need to change (mirrors Payment's own edge declaration).
		edge.To("order", Order.Type).Unique().Required().Field("order_id"),
	}
}

func (Shipment) Indexes() []ent.Index {
	return []ent.Index{
		// design D4: the authoritative "at most one shipment per order" guard.
		index.Fields("order_id").Unique(),
		index.Fields("shop_id", "order_id"),
	}
}

func (Shipment) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Checks: map[string]string{
				"shipments_status_check": "status IN (1, 2, 3)",
			},
		},
	}
}
