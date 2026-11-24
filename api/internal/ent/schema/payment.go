// Package schema — payment entities (change payment-integration, design
// D1–D10). payments is shop-owned tenant data and is registered in
// api/internal/tenant.tenantOwned; it carries a direct shop_id column so
// the interceptor/hook can scope it (same convention as order/cart/catalog).
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

// Payment is one payment attempt against an Order (design D3). Unlike
// OrderItem's product_id/sku_id (deliberately edge-less/FK-less
// informational snapshots — order-management design D4), Payment's order_id
// is a real, required FK: a payment record's entire reason to exist is "this
// money belongs to that order", not a point-in-time display snapshot, and
// there is no order-deletion endpoint today. No OnDelete annotation is set,
// so Postgres's default NO ACTION applies — deleting an order with existing
// payment rows is blocked rather than cascading them away, protecting the
// financial audit trail (more conservative than OrderItem's Cascade, and
// deliberately so).
//
// An order MAY have many payments (first attempt fails, retry succeeds) —
// order_id carries no uniqueness constraint. The (provider, provider_reference)
// pair MUST be globally unique: it is the only lookup key the webhook route
// has available, since that route runs without tenant context (design D7).
type Payment struct {
	ent.Schema
}

func (Payment) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Payment) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("order_id"),
		// provider (design D1/D2): the Provider.Name() that created this
		// attempt, e.g. "mock". Also the {provider} webhook path segment.
		field.String("provider").MaxLen(32).SchemaType(map[string]string{dialect.Postgres: "varchar(32)"}).NotEmpty(),
		// provider_reference (design D3/D6/D7): the provider-side
		// transaction identifier, generated at InitiatePayment time and
		// echoed back by the webhook. The sole idempotent webhook lookup
		// key (with provider) — see the unique index below.
		field.String("provider_reference").MaxLen(191).SchemaType(map[string]string{dialect.Postgres: "varchar(191)"}).NotEmpty(),
		// provider_transaction_id (change ecpay-payment-provider, design D4):
		// the provider's OWN identifier, learned only when its webhook arrives.
		//
		// 真實金流商的流程裡有**兩個**號碼，而哪一個當查找鍵不是選擇題：
		// provider_reference 必須在 InitiatePayment 建列的那一刻就存在，而綠界的
		// TradeNo 那時候還不存在——所以 provider_reference 只能是我們自己產生的
		// MerchantTradeNo。TradeNo 卻是商家在綠界後台、對帳單、客服對話裡最常
		// 拿到的那一個，而且**綠界的退款 API 兩個號碼都是必填**。
		//
		// NULL（而不是空字串）表示「這家 provider 沒有第二個識別碼」或「還沒收到」
		// ——mock 與本欄位出現前的每一列都是 NULL，如實。
		//
		// 刻意**沒有**唯一索引：它不是查找鍵，而且綠界對同一筆交易的重送會帶
		// 同一個 TradeNo，加了唯一索引反而會讓合法的重送炸掉。
		field.String("provider_transaction_id").MaxLen(64).
			SchemaType(map[string]string{dialect.Postgres: "varchar(64)"}).Optional().Nillable(),
		// paid_at (change ecpay-payment-provider, design D12): when the PROVIDER
		// says the money moved — 綠界通知裡的 PaymentDate，也就是出現在對帳單上
		// 的那個時間。
		//
		// 刻意不用 updated_at 代替：updated_at 的定義是「最後一次寫入這一列」，
		// 今天它碰巧接近付款時間，但任何未來的寫入都會蓋掉它，而那一天不會有人
		// 記得它曾經被當成付款時間用。用一個定義不同、只是暫時碰巧相等的欄位去
		// 回答對帳問題，是那種寫錯不會噴錯的錯誤。
		//
		// change order-and-invitation-polish, design D3 補充：**成功的付款一定有
		// paid_at**。provider 回報了時間（綠界 PaymentDate）就用它；沒回報（mock、
		// 或綠界略去 PaymentDate）就由 payment.Service.HandleWebhook 在記錄成功的
		// 那一刻寫入「我們處理的時間」——這是明確的 fallback，不是把 updated_at 當
		// 付款時間用。失敗／待處理的付款仍是 NULL。上面那句警告仍然成立，只有
		// 該 change 的 migration 為歷史列做了一次性的 updated_at 近似回填。
		field.Time("paid_at").Optional().Nillable(),
		// Money: integer minor units, BIGINT/int64 (product-catalog design
		// D1 convention). Always equals the order's total_amount at the time
		// this attempt was created — this change supports full-amount
		// payment only (design Non-Goals); the invariant amount <=
		// order.total_amount is enforced by payment.Service, not a DB CHECK
		// (Postgres CHECK cannot reference another table).
		field.Int64("amount"),
		field.String("currency").MaxLen(3).SchemaType(map[string]string{dialect.Postgres: "varchar(3)"}).NotEmpty(),
		// status (design D5): 0 pending / 1 succeeded / 2 failed / 3
		// refunded. This change's only program-driven transitions are
		// pending->succeeded and pending->failed (via
		// payment.Service.HandleWebhook); refunded is reserved for a future
		// refunds change, same reasoning as orders.payment_status below.
		field.Int16("status").Default(0),
	}
}

func (Payment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		// One-directional real FK to Order (see type doc comment for why
		// this deliberately differs from OrderItem's edge-less product_id/
		// sku_id). No Ref/inverse edge on Order — declared entirely here,
		// so order.go does not need to change.
		edge.To("order", Order.Type).Unique().Required().Field("order_id"),
	}
}

func (Payment) Indexes() []ent.Index {
	return []ent.Index{
		// Idempotent webhook lookup key (design D6/D7) — MUST be unique:
		// provider_reference is generated by us (or the real provider) to be
		// unique per attempt, and this is the only way the tenant-context-
		// free webhook route can locate the right row.
		index.Fields("provider", "provider_reference").Unique(),
		index.Fields("shop_id", "order_id"),
	}
}

func (Payment) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Checks: map[string]string{
				"payments_amount_check": "amount > 0",
				"payments_status_check": "status IN (0, 1, 2, 3)",
			},
		},
	}
}
