// Package schema — order entities (change order-management, design D1–D9).
// Both tables below are shop-owned tenant data and are registered in
// api/internal/tenant.tenantOwned; each carries a direct shop_id column so
// the interceptor/hook can scope them (same convention as cart/catalog).
package schema

import (
	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Order is the immutable record created by checking out a member's cart
// (design D1/D3). member_id points directly at the platform-wide Member —
// not ShopMember — mirroring Cart's own shape (shopping-cart design D1).
//
// Three independent status axes (design D5):
//   - status: this change's own lifecycle (Created/Cancelled) — full CHECK,
//     this change owns the entire enumeration space.
//   - payment_status: owned by the future payment-integration proposal. This
//     change only ever creates/reads the value 0 (unpaid); the CHECK is
//     deliberately loose (>= 0) so that proposal can define its own
//     enumeration without this change having guessed it wrong up front.
//   - fulfillment_status: same reasoning, owned by shipping-logistics.
//
// total_amount is a snapshot computed once at checkout (sum of order_items'
// price_amount*quantity) and never recomputed — order_items never change
// after creation, so the snapshot never goes stale.
type Order struct {
	ent.Schema
}

func (Order) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Order) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("member_id"),
		// status (design D5): 0 created / 1 cancelled. Fully owned by this
		// change — no other proposal is expected to add values to this axis.
		field.Int16("status").Default(0),
		// payment_status (design D5/D6): 0 unpaid is the only value this
		// change ever sets. payment-integration defines values >= 1 and
		// updates this column exclusively through order.Service.
		// UpdatePaymentStatus (design D6) — never written directly.
		field.Int16("payment_status").Default(0),
		// fulfillment_status (design D5/D6): 0 unfulfilled is the only value
		// this change ever sets. shipping-logistics defines values >= 1 and
		// updates this column exclusively through order.Service.
		// UpdateFulfillmentStatus (design D6) — never written directly.
		field.Int16("fulfillment_status").Default(0),
		field.String("currency").MaxLen(3).SchemaType(map[string]string{dialect.Postgres: "varchar(3)"}).NotEmpty(),
		// Money: integer minor units, BIGINT/int64 (product-catalog design
		// D1). Snapshot of order_items totals at checkout time — never float,
		// never recomputed after creation.
		field.Int64("total_amount"),
		// Structured recipient/address info (design D8) — jsonb, opaque at
		// the ent/DB layer like Product.meta/ProductSKU.options; shape is
		// order.ShippingAddress in api/internal/order, validated at checkout.
		// Required: checkout cannot happen without a shipping address.
		field.JSON("shipping_address", json.RawMessage{}),
		// NULL unless the order has been cancelled (design D7).
		field.Time("cancelled_at").Optional().Nillable(),
	}
}

func (Order) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		edge.To("member", Member.Type).Unique().Required().Field("member_id"),
		// OnDelete(Cascade) MUST live on this assoc ("To") edge, not on
		// OrderItem's inverse "order" edge below — ent's SQL diff only
		// consults the assoc edge's annotations when generating the FK
		// (product-catalog design D7's documented gotcha). This change has
		// no order-deletion endpoint, but the schema stays consistent for
		// whenever one is added.
		edge.To("items", OrderItem.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (Order) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id", "member_id"),
		index.Fields("shop_id", "status"),
	}
}

func (Order) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Checks: map[string]string{
				"orders_status_check":             "status IN (0, 1)",
				"orders_payment_status_check":     "payment_status >= 0",
				"orders_fulfillment_status_check": "fulfillment_status >= 0",
				"orders_total_amount_check":       "total_amount >= 0",
			},
		},
	}
}

// OrderItem is one denormalized line within an Order (design D1/D4). Unlike
// CartItem, product_id/sku_id are plain informational int columns with no
// ent edge and no FK constraint at all (design D4): every field needed to
// display this line (title, sku_code, price, currency, quantity) is already
// snapshotted onto this row at checkout time, so there is nothing left to
// join for — and therefore nothing that needs a delete-time cascade/SET NULL
// rule the way cart_items.sku_id does. product_id/sku_id may point at rows
// that no longer exist; callers must never depend on them resolving.
type OrderItem struct {
	ent.Schema
}

func (OrderItem) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (OrderItem) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "order_items"},
		entsql.Annotation{
			Checks: map[string]string{
				"order_items_quantity_check":     "quantity > 0",
				"order_items_price_amount_check": "price_amount >= 0",
			},
		},
	}
}

func (OrderItem) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("order_id"),
		// Informational only (design D4) — no edge, no FK. May be stale.
		field.Int("product_id").Optional().Nillable(),
		// Denormalized text snapshot (design D4) — survives product deletion
		// or rename untouched.
		field.String("product_title").MaxLen(200).SchemaType(map[string]string{dialect.Postgres: "varchar(200)"}).NotEmpty(),
		// Informational only (design D4) — no edge, no FK. May be stale.
		field.Int("sku_id").Optional().Nillable(),
		field.String("sku_code").MaxLen(64).SchemaType(map[string]string{dialect.Postgres: "varchar(64)"}).NotEmpty(),
		field.Int32("quantity"),
		// Money: integer minor units, BIGINT/int64. Snapshot copied straight
		// from the source cart_item's own snapshot (shopping-cart design D2)
		// — never a live SKU re-query.
		field.Int64("price_amount"),
		field.String("currency").MaxLen(3).SchemaType(map[string]string{dialect.Postgres: "varchar(3)"}).NotEmpty(),
	}
}

func (OrderItem) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		// Inverse of Order.items — no OnDelete annotation here (it would be
		// silently ignored; the Cascade lives on Order's "items" edge above).
		edge.From("order", Order.Type).Ref("items").
			Unique().Required().Field("order_id"),
	}
}

func (OrderItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id"),
		index.Fields("shop_id"),
	}
}
