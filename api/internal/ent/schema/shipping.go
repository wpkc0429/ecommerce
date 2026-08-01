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

// Shipment is one parcel dispatched for an Order. Unlike OrderItem's
// product_id/sku_id (deliberately edge-less informational snapshots),
// Shipment's order_id is a real, required FK — mirrors Payment.order_id
// exactly, same reasoning: a shipment record's entire reason to exist is
// "this parcel belongs to that order", not a point-in-time display snapshot.
// No OnDelete annotation is set, so Postgres's default NO ACTION applies —
// deleting an order with an existing shipment is blocked rather than
// cascading it away (audit trail).
//
// status (design D2) deliberately reuses orders.fulfillment_status's
// non-zero values (1 shipped / 2 delivered / 3 returned) rather than
// inventing a parallel enum — see shipping.ShippedStatus/DeliveredStatus/
// ReturnedStatus. A shipments row only ever comes into existence at the
// moment of shipping (creating the row IS the shipping event — there is no
// separate "draft" step), so there is no "0/unfulfilled" value to represent
// here: a nonexistent row already means unfulfilled. A shipment also never
// takes orders.fulfillment_status's new value 4 (partially shipped): "partly"
// is a property of an *order*, never of a single parcel.
//
// **An order may have many shipments** (change partial-shipment). order_id
// used to carry a UNIQUE index, which was the authoritative "at most one
// shipment per order" guard; that index is gone and what replaced it is a
// different question entirely. The guard is no longer "has this order shipped
// already" but "is there anything left to ship", and its arbiter is the
// conditional UPDATE on order_items.shipped_quantity plus the CHECK
// constraint bounding it by quantity (design D3) — not application-layer
// read-then-write logic, exactly as before.
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
		// The parcel's contents (change partial-shipment). Cascade lives here,
		// on the assoc ("To") edge, because ent's SQL diff only consults this
		// side when generating the FK (product-catalog design D7's documented
		// gotcha, same as Order.items).
		edge.To("items", ShipmentItem.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (Shipment) Indexes() []ent.Index {
	return []ent.Index{
		// Plain, NOT unique since change partial-shipment — an order may have
		// many parcels. See the type doc comment for what took over as the
		// arbiter.
		index.Fields("order_id"),
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

// ShipmentItem is one order line inside one parcel (change partial-shipment):
// "this shipment contains N of that order item". Both edges are real, required
// FKs — a shipment_items row has no meaning at all detached from either side,
// which is the same test Shipment.order_id/Payment.order_id already pass, and
// the opposite of OrderItem.product_id/sku_id (display snapshots that may
// dangle).
//
// It deliberately carries **no money column**. Shipping fees live once, on the
// order (orders.shipping_fee, a snapshot of the selected method's flat_rate);
// a per-parcel amount here would immediately create a second authority for
// what the customer was charged, and nothing reconciles the two.
//
// It also carries no denormalized product title or sku_code. Everything a
// caller needs to display the line is already on the order item this row points
// at, and that row is itself an immutable snapshot — copying it again would be
// a third copy of the same text, drifting the first time anyone edits one path.
type ShipmentItem struct {
	ent.Schema
}

func (ShipmentItem) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (ShipmentItem) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("shipment_id"),
		field.Int("order_item_id"),
		// How many of that order line are in this parcel. Bounded above by the
		// order item's own quantity, but that bound is enforced where it can be
		// enforced atomically: order_items.shipped_quantity's CHECK plus the
		// conditional UPDATE that increments it (design D3). A CHECK here could
		// only see this row.
		field.Int32("quantity"),
	}
}

func (ShipmentItem) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		edge.From("shipment", Shipment.Type).Ref("items").
			Unique().Required().Field("shipment_id"),
		// One-directional real FK to OrderItem (no inverse edge on OrderItem,
		// mirroring how Shipment declares its own order edge). No OnDelete, so
		// Postgres's NO ACTION applies: an order item that is inside a parcel
		// cannot be deleted out from under it. There is no order/order-item
		// deletion endpoint in this API at all, so this only ever matters as a
		// backstop.
		edge.To("order_item", OrderItem.Type).Unique().Required().Field("order_item_id"),
	}
}

func (ShipmentItem) Indexes() []ent.Index {
	return []ent.Index{
		// One line per order item per parcel: a request naming the same order
		// item twice is rejected in the service layer, and this is the arbiter
		// that makes that a database fact rather than a check someone could
		// later drop (same discipline as point_transactions(order_id, kind)).
		index.Fields("shipment_id", "order_item_id").Unique(),
		index.Fields("shop_id", "shipment_id"),
	}
}

func (ShipmentItem) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "shipment_items"},
		entsql.Annotation{
			Checks: map[string]string{
				"shipment_items_quantity_check": "quantity > 0",
			},
		},
	}
}
