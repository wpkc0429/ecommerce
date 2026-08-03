// Package schema — shop-payment-credentials entity (change
// ecpay-per-shop-credentials, design D3/D5/D6). ShopPaymentProviderCredential
// is shop-owned tenant data carrying a direct shop_id column and is registered
// in api/internal/tenant.tenantOwned.
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

// ShopPaymentProviderCredential is one shop's own credentials for one payment
// provider (design D5). `(shop_id, provider)` is unique — one credential set
// per provider per shop, the same shape `payments.(provider,
// provider_reference)` uses for a different reason.
//
// Fields split by what they actually are (design D3), the same distinction
// storefront-client-auth already drew for the publishable key: MerchantID and
// ChoosePayment are identifiers/settings, not secrets — knowing MerchantID
// alone lets nobody forge anything, so they are plain columns, editable and
// readable in full. HashKey/HashIV are genuine secrets (inputs to
// CheckMacValue — knowing them means being able to forge a valid webhook
// signature), so they are stored only as cryptbox ciphertext and this schema
// has no field that could serialize their plaintext by accident.
type ShopPaymentProviderCredential struct {
	ent.Schema
}

func (ShopPaymentProviderCredential) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (ShopPaymentProviderCredential) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		// provider: same registry key as payments.provider (e.g. "ecpay").
		field.String("provider").MaxLen(32).SchemaType(map[string]string{dialect.Postgres: "varchar(32)"}).NotEmpty(),
		field.String("merchant_id").MaxLen(191).SchemaType(map[string]string{dialect.Postgres: "varchar(191)"}).NotEmpty(),
		// hash_key_ciphertext/hash_iv_ciphertext: cryptbox.Box.Encrypt output
		// (nonce||ciphertext). Never decrypted for display — the admin API only
		// ever reports whether they are set (design D6).
		field.Bytes("hash_key_ciphertext"),
		field.Bytes("hash_iv_ciphertext"),
		// choose_payment: 綠界's ChoosePayment field. Optional — a nil value
		// means "use ecpay.Provider's own Credit default", mirroring how
		// ecpay.Config.ChoosePayment behaved before this change.
		field.String("choose_payment").MaxLen(32).
			SchemaType(map[string]string{dialect.Postgres: "varchar(32)"}).Optional().Nillable(),
	}
}

func (ShopPaymentProviderCredential) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
	}
}

func (ShopPaymentProviderCredential) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id", "provider").Unique(),
	}
}

func (ShopPaymentProviderCredential) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "shop_payment_provider_credentials"},
	}
}
