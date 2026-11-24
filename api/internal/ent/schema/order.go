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
		// D1). Never float, never recomputed after creation.
		//
		// Four amounts, fixed relative to each other by a DB CHECK (change
		// order-extras design D6/D7, extended by change tier-discount-application
		// design D5):
		//
		//	subtotal_amount = Σ(order_items.price_amount × quantity)   ← pre-discount, always
		//	discount_amount = the member tier discount actually applied, or 0
		//	shipping_fee    = the selected shipping method's flat_rate, or 0
		//	total_amount    = subtotal_amount − discount_amount + shipping_fee  ← what is charged
		//
		// total_amount changed meaning in change order-extras: it used to be the
		// item subtotal alone, which meant payment.InitiatePayment charged less
		// than the storefront displayed. No stored value moved (the backfill set
		// subtotal_amount = total_amount, shipping_fee = 0, under which every
		// pre-existing row was already correct); what changed is that a future
		// order may have total_amount > Σ(items). tier-discount-application did
		// NOT change its meaning again — only the arithmetic that composes it, so
		// an order may now also be *less* than subtotal + fee.
		//
		// Loyalty points are awarded on subtotal_amount − discount_amount
		// (tier-discount-application design D6): shipping is a pass-through cost,
		// and a discount and its points are two forms of the same loyalty budget —
		// awarding on the pre-discount amount multiplies the merchant's cost behind
		// a knob that shows them only one number.
		field.Int64("total_amount"),
		field.Int64("subtotal_amount"),
		field.Int64("shipping_fee").Default(0),
		// discount_amount / discount_percent / member_tier_name are ONE snapshot,
		// taken at checkout and never recomputed (design D3/D8). Storing only the
		// rate — let alone only the tier — would leave a number nobody can
		// reproduce afterwards: discount_percent is a setting the merchant edits at
		// will, and a member_tiers row can be deleted outright. Same discipline as
		// order_items.price_amount and orders.shipping_fee/shipping_method_name.
		field.Int64("discount_amount").Default(0),
		// The rate ACTUALLY applied. NULL means "held no tier at checkout"; a tier
		// whose own discount_percent is NULL snapshots as 0 here, so "belonged to a
		// tier with no discount" stays distinguishable from "belonged to no tier".
		field.Int16("discount_percent").Optional().Nillable(),
		// Denormalized text snapshot, surviving a later rename or deletion of the
		// tier. The tier *id* is deliberately not snapshotted (design D3): what the
		// customer agreed to is a name and a rate, and unlike shipping methods
		// nothing ever selects a tier by id — order-extras design D4's own
		// criterion, applied to a case where the round trip does not exist.
		field.String("member_tier_name").MaxLen(100).
			SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}).Optional().Nillable(),
		// Informational only (change order-extras, design D4) — no edge, no FK,
		// exactly like OrderItem.product_id/sku_id. A real FK would change an
		// existing endpoint's behavior: DELETE /shipping-methods/{id} would
		// either start failing for any method ever used, or SET NULL its way
		// through historical orders. Everything this order needs to display and
		// reconcile is snapshotted below; callers must never depend on this id
		// resolving.
		field.Int("shipping_method_id").Optional().Nillable(),
		// Denormalized text snapshot — what the customer actually chose and
		// agreed to pay for, surviving a later rename or deletion of the method.
		// carrier is deliberately NOT snapshotted (design D4): shipping.Service.
		// CreateShipment requires the back office to state the carrier
		// explicitly, so nothing breaks without it.
		field.String("shipping_method_name").MaxLen(100).
			SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}).Optional().Nillable(),
		// requested_delivery_date (change order-extras, design D3): a calendar
		// day, stored as Postgres `date` — NOT a timestamp. The customer picks a
		// day, not an instant; storing 2026-08-05T00:00:00Z would be 08-04 in
		// every timezone west of UTC, and that off-by-one would be written into
		// the data rather than merely displayed.
		//
		// Deliberately its own column rather than an optional key inside
		// shipping_address: that jsonb answers "where to", this answers "when" —
		// and a jsonb string is neither validatable nor indexable, which kills
		// the one query a florist actually wants ("what ships tomorrow").
		field.Time("requested_delivery_date").Optional().Nillable().
			SchemaType(map[string]string{dialect.Postgres: "date"}),
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
				"orders_subtotal_amount_check":    "subtotal_amount >= 0",
				"orders_shipping_fee_check":       "shipping_fee >= 0",
				"orders_discount_amount_check":    "discount_amount >= 0",
				// change tier-discount-application, design D5: a discount can never
				// make the goods portion negative. Without this the failure would
				// surface as orders_total_amount_check ("the total is negative"),
				// which names a symptom rather than the line that was crossed.
				"orders_discount_amount_bound_check": "discount_amount <= subtotal_amount",
				"orders_discount_percent_check":      "discount_percent IS NULL OR (discount_percent >= 0 AND discount_percent <= 100)",
				// A half-written tier snapshot ("a name but no rate") is a row
				// nobody can explain, and can only come from a mistake. Same
				// discipline as checkout_idempotency_keys' completed⇔has-order.
				"orders_tier_snapshot_check": "(member_tier_name IS NULL) = (discount_percent IS NULL)",
				// change order-extras, design D7 (amended by change
				// tier-discount-application, design D5): the composition of the four
				// amounts is a database fact, not a line of Go arithmetic that
				// the next order-creating path could forget to reproduce.
				"orders_total_amount_composition_check": "total_amount = subtotal_amount - discount_amount + shipping_fee",
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
				// change partial-shipment, design D3: you cannot ship what was
				// never ordered. This lives in the database for the same reason
				// orders_total_amount_composition_check does — it is the second,
				// independent arbiter behind the conditional UPDATE, so a
				// mistyped predicate aborts the transaction instead of writing an
				// impossible number that nothing would ever notice.
				"order_items_shipped_quantity_check": "shipped_quantity >= 0 AND shipped_quantity <= quantity",
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
		// options (change order-and-invitation-polish, design D4): the SKU's
		// option values (e.g. {"size":"M","color":"red"}) as they were when the
		// order was created — a snapshot like sku_code, so a later SKU edit or
		// deletion never changes what the customer ordered. NULL only for rows
		// created before this column existed (the migration back-fills those
		// with the SKU's then-current options where the SKU still exists).
		field.JSON("options", json.RawMessage{}).Optional(),
		// Money: integer minor units, BIGINT/int64. Snapshot copied straight
		// from the source cart_item's own snapshot (shopping-cart design D2)
		// — never a live SKU re-query.
		field.Int64("price_amount"),
		field.String("currency").MaxLen(3).SchemaType(map[string]string{dialect.Postgres: "varchar(3)"}).NotEmpty(),
		// gift_message (change order-extras, design D1): copied from the source
		// cart_item at checkout, then frozen — same discipline as
		// product_title/sku_code. The customer's own words belong in the
		// permanent snapshot, not behind a join to a converted cart that a
		// future cleanup job is entitled to delete.
		// No ent MaxLen validator, for the same byte-vs-character reason as
		// CartItem.gift_message; the value arriving here has already passed
		// cart.Service's character-counted check.
		field.String("gift_message").
			SchemaType(map[string]string{dialect.Postgres: "varchar(500)"}).Default(""),
		// shipped_quantity (change partial-shipment, design D3): how many of
		// this line have left the shop. **The only mutable column on this
		// table** — everything above it is a permanent snapshot, and that
		// contrast is the point.
		//
		// It is a stored counter rather than a SUM over shipment_items, and the
		// reason is not performance: the over-shipment guard has to be the same
		// shape as the one concurrency-safe decrement this API has (order.Service.
		// Checkout's stock loop), i.e. one conditional UPDATE plus an affected-rows
		// check. A SUM cannot appear in that UPDATE's predicate without either
		// SELECT … FOR UPDATE or an application-level read-then-write, and this
		// codebase refuses both (checkout-idempotency design D3).
		//
		// Written only by shipping.Service, inside the transaction that also
		// writes the shipments/shipment_items rows justifying it. Never by
		// checkout, never by cancellation (which only proceeds while
		// fulfillment_status is still 0, i.e. while this is still 0 everywhere),
		// never by any back-office endpoint.
		//
		// Never decremented on return. Returns are terminal and an order with a
		// returned shipment accepts no further shipments, so a decrement would
		// have no consumer — and the moment this stops being monotonic, "how much
		// has ever shipped" and "how much is out there now" become two different
		// numbers wearing one name.
		field.Int32("shipped_quantity").Default(0),
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

// CheckoutIdempotencyKey records one client-supplied idempotency key and the
// order it produced (change checkout-idempotency, design D3). It is shop-owned
// tenant data and is registered in api/internal/tenant.tenantOwned.
//
// The whole mechanism rests on UNIQUE(shop_id, idempotency_key) being the
// arbiter: reserving a key is an INSERT, and losing that INSERT is how a
// duplicate request learns it is a duplicate — never an application-level
// read-then-write. That is the same discipline as payments(provider,
// provider_reference), shipments(order_id) and point_transactions(order_id,
// kind); completion and release reuse the other half of it (a conditional
// UPDATE/DELETE plus an affected-rows check).
//
// The unique index is Postgres's default NULLS DISTINCT, which is immaterial
// here because both columns are NOT NULL. Stated explicitly because this
// codebase has two handwritten NULLS NOT DISTINCT constraints (site_shop,
// role_user) that must NOT be copied onto this table.
//
// shop_id scopes the key: shop A can never replay shop B's key, because a key
// string used by both is simply two rows. That is a tenant-isolation
// requirement, not a nicety.
type CheckoutIdempotencyKey struct {
	ent.Schema
}

func (CheckoutIdempotencyKey) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (CheckoutIdempotencyKey) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		// The raw client-supplied value, treated as opaque (design D2): no
		// structure is required or parsed, only bounds (8–255 printable ASCII)
		// enforced in the service layer before anything reaches this column.
		field.String("idempotency_key").MaxLen(255).
			SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).NotEmpty(),
		// SHA-256 hex of "caller scope \n raw request body" (design D7). The
		// caller scope is what stops two customers of one shop who picked the
		// same weak key and posted a byte-identical body from seeing each
		// other's order.
		field.String("request_hash").MaxLen(64).
			SchemaType(map[string]string{dialect.Postgres: "char(64)"}).NotEmpty(),
		// 0 in_progress / 1 completed. Constants live in internal/idempotency.
		field.Int16("status").Default(0),
		// Informational only — no edge, no FK, exactly like
		// orders.shipping_method_id and order_items.product_id/sku_id. An
		// optional ent edge would generate ON DELETE SET NULL, which would
		// directly violate the completed⇔has-order CHECK below; this API has no
		// order-deletion path at all, so the FK buys nothing and risks a
		// self-contradicting row.
		field.Int("order_id").Optional().Nillable(),
		// Each row carries its own deadline rather than being compared against
		// "created_at + current config" (design D8): changing the setting must
		// not retroactively redefine how long an existing row promised to live,
		// and expiry then reduces to a plain column comparison.
		field.Time("expires_at"),
	}
}

func (CheckoutIdempotencyKey) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
	}
}

func (CheckoutIdempotencyKey) Indexes() []ent.Index {
	return []ent.Index{
		// The arbiter (design D3/D4). Scoped by shop, so the same key string in
		// two shops is two independent rows.
		index.Fields("shop_id", "idempotency_key").Unique(),
	}
}

func (CheckoutIdempotencyKey) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "checkout_idempotency_keys"},
		entsql.Annotation{
			Checks: map[string]string{
				"checkout_idempotency_keys_status_check": "status IN (0, 1)",
				// completed ⇔ has an order. A database fact rather than a
				// convention that only lives in Go, so the next writer of this
				// table does not have to remember it.
				"checkout_idempotency_keys_order_id_check": "(status = 0 AND order_id IS NULL) OR (status = 1 AND order_id IS NOT NULL)",
			},
		},
	}
}
