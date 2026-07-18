// Package schema — shopping cart entities (change shopping-cart, design
// D1–D9). Both tables below are shop-owned tenant data and are registered in
// api/internal/tenant.tenantOwned; each carries a direct shop_id column so
// the interceptor/hook can scope them (same convention as the catalog
// tables, product-catalog design D7).
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

// Cart is a member's self-service shopping cart, shop-scoped (design D1:
// member_id points directly at the platform-wide Member — not ShopMember —
// mirroring ShopMember's own shop_id+member_id shape rather than adding an
// indirection through it). At most one active (status=0) cart exists per
// (shop_id, member_id) — enforced by the partial unique index below (design
// D3). currency is fixed by the first item added (design D4) and is
// therefore NOT NULL: a Cart row is only ever created once that first item
// exists (design D3 — GetCartView returns an ephemeral empty view when no
// row exists yet, it never creates one).
type Cart struct {
	ent.Schema
}

func (Cart) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Cart) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("member_id"),
		// status (design D8): 0 active / 1 converted / 2 abandoned. This
		// change only ever creates status=0 rows and never transitions them
		// — converting to an order (1) is order-management's job, and
		// abandonment detection (2) is unimplemented future work.
		field.Int16("status").Default(0),
		field.String("currency").MaxLen(3).SchemaType(map[string]string{dialect.Postgres: "varchar(3)"}).NotEmpty(),
	}
}

func (Cart) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		edge.To("member", Member.Type).Unique().Required().Field("member_id"),
		// OnDelete(Cascade) MUST live on this assoc ("To") edge, not on
		// CartItem's inverse "cart" edge below — ent's SQL diff only
		// consults the assoc edge's annotations when generating the FK
		// (product-catalog design D7's documented gotcha). This change has
		// no cart-deletion endpoint, but the schema stays consistent for
		// whenever one is added.
		edge.To("items", CartItem.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (Cart) Indexes() []ent.Index {
	return []ent.Index{
		// At most one active cart per (shop, member) — design D3. Other
		// statuses (converted/abandoned) are excluded so a member can always
		// start a fresh active cart after checkout.
		index.Fields("shop_id", "member_id").Unique().
			Annotations(entsql.IndexWhere("status = 0")).
			StorageKey("carts_one_active_per_member"),
		index.Fields("member_id"),
	}
}

func (Cart) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Checks: map[string]string{
				"carts_status_check": "status IN (0, 1, 2)",
			},
		},
	}
}

// CartItem is one SKU line within a Cart (design D2/D5/D7). price_amount and
// currency are a snapshot copied from the SKU at add-time (design D2/spec
// "Price snapshot on add") — cart totals are always computed from this
// snapshot, never a live SKU re-query, so a later price change on the SKU
// never moves an existing cart item's displayed amount or the cart total.
// sku_id is nullable (design D7): when the referencing SKU is deleted, the
// FK is set to NULL by the database rather than cascading the item away, so
// the price/quantity snapshot survives for the member (and order-management)
// to see; the item is then permanently unpurchasable.
type CartItem struct {
	ent.Schema
}

func (CartItem) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (CartItem) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "cart_items"},
		entsql.Annotation{
			Checks: map[string]string{
				"cart_items_quantity_check":     "quantity > 0",
				"cart_items_price_amount_check": "price_amount >= 0",
			},
		},
	}
}

func (CartItem) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("cart_id"),
		field.Int("sku_id").Optional().Nillable(),
		field.Int32("quantity"),
		// Money: integer minor units, BIGINT/int64 (product-catalog design
		// D1). Never float, never numeric.
		field.Int64("price_amount"),
		field.String("currency").MaxLen(3).SchemaType(map[string]string{dialect.Postgres: "varchar(3)"}).NotEmpty(),
	}
}

func (CartItem) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		// Inverse of Cart.items — no OnDelete annotation here (it would be
		// silently ignored; the Cascade lives on Cart's "items" edge above).
		edge.From("cart", Cart.Type).Ref("items").
			Unique().Required().Field("cart_id"),
		// Assoc edge to the SKU (design D7): SET NULL on delete, so removing
		// a SKU (or cascading from its product's deletion, product-catalog
		// design D2/D7) detaches this item from the SKU without deleting the
		// item itself.
		edge.To("sku", ProductSKU.Type).Unique().Field("sku_id").
			Annotations(entsql.OnDelete(entsql.SetNull)),
	}
}

func (CartItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("cart_id"),
		index.Fields("sku_id"),
	}
}
