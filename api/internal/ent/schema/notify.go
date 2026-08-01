// Package schema — order-notification-email entities (change
// order-notification-email, design D6/D13/D16). Shop-owned tenant data,
// registered in api/internal/tenant.tenantOwned; carries a direct shop_id
// column so the interceptor/hook can scope it (same convention as
// order/payment/shipping/points).
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

// OrderNotification is one attempt to email a customer about one order.
//
// ── Why this table exists (design D6) ──────────────────────────────────
//
// payment.Service.HandleWebhook publishes events.OrderPaymentSucceeded on
// EVERY delivery that re-confirms an already-succeeded payment, not just the
// first — its own doc comment says so, and 綠界 redelivers every 5–15 minutes,
// up to 4 times a day, whenever it does not receive `1|OK`. So duplicate
// delivery is expected behaviour, not an edge case, and "the customer received
// two 訂單已成立 emails" is a visible failure.
//
// The arbiter is therefore the UNIQUE(order_id, kind) index below — the
// database, never an application-level read-then-write. This is the same idiom
// as point_transactions(order_id, kind), payments(provider,
// provider_reference), shipments(order_id) and
// checkout_idempotency_keys(shop_id, idempotency_key).
//
// ── Reserve first, then send: at-most-once (design D6) ─────────────────
//
// The row is created BEFORE the message is handed to the sender. The reverse
// order (send, then record) would risk sending twice whenever the write or the
// process fails after the send — and its only benefit is "a later delivery can
// retry", which is worth exactly nothing here because **this system has no
// retry**. So: at-most-once, with a failed send recorded as a row
// (status=StatusFailed + error_reason) rather than only as a log line that
// rotates away.
//
// ── What is deliberately NOT stored (design D13) ───────────────────────
//
// The rendered message body is not stored. Storing it would put a second full
// copy of the recipient's name, phone and address in the database, and the only
// need it would serve ("show me what we sent") does not exist today.
//
// recipient_email IS stored, and it is a deliberate PII snapshot with a real
// consumer: members has no email-change history, so once a customer edits their
// address, "where did we actually send it" becomes unanswerable. It is returned
// by no API and written to no log.
type OrderNotification struct {
	ent.Schema
}

func (OrderNotification) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (OrderNotification) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("order_id"),
		// kind: 0 訂單成立 / 1 付款成功. An int16 enumeration rather than a
		// string, for the same reason point_transactions.kind is one: it is the
		// machine-checkable idempotency axis, not a human-readable label.
		field.Int16("kind"),
		// recipient_email is the address this attempt was actually sent to — see
		// the type doc comment for why the snapshot earns its place.
		field.String("recipient_email").
			MaxLen(150).SchemaType(map[string]string{dialect.Postgres: "varchar(150)"}).NotEmpty(),
		// status: 0 已預約(queued) / 1 已寄出(sent) / 2 失敗(failed). A row is
		// created queued, then moves to exactly one terminal value. A row stuck
		// at queued means the process died mid-send — visible, and honest.
		field.Int16("status").Default(0),
		field.Time("sent_at").Optional().Nillable(),
		// error_reason carries the sender's error for a failed attempt. It is a
		// transport error (dial timeout, auth rejected, 5xx from the MTA) and
		// MUST NOT be used to carry message content.
		field.String("error_reason").
			MaxLen(500).SchemaType(map[string]string{dialect.Postgres: "varchar(500)"}).Optional().Nillable(),
	}
}

func (OrderNotification) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		// Required order_id, mirroring Payment/Shipment rather than
		// PointTransaction (which is optional only because manual adjustments
		// have no order). A notification without an order has no meaning.
		// No Ref/inverse edge on Order, so order.go does not change.
		edge.To("order", Order.Type).Unique().Required().Field("order_id"),
	}
}

func (OrderNotification) Indexes() []ent.Index {
	return []ent.Index{
		// design D6: the authoritative "at most one email of this kind per
		// order" guard. Both columns are always set, so unlike
		// point_transactions(order_id, kind) there is no NULL escape hatch here.
		index.Fields("order_id", "kind").Unique(),
		index.Fields("shop_id", "order_id"),
	}
}

func (OrderNotification) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "order_notifications"},
		entsql.Annotation{
			Checks: map[string]string{
				"order_notifications_kind_check":   "kind IN (0, 1)",
				"order_notifications_status_check": "status IN (0, 1, 2)",
			},
		},
	}
}
