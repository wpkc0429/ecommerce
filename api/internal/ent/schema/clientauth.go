// Package schema — storefront-client-auth entities (change
// storefront-client-auth, design D1/D4/D5). Both tables below are shop-owned
// tenant data carrying a direct shop_id column and are registered in
// api/internal/tenant.tenantOwned.
//
// Note on execution context: the two resolution paths that read these tables
// (publishable key → shop, origin → registered?) deliberately run *before* a
// tenant context exists — they are what establishes it. tenantOwned membership
// therefore only governs the back-office/self-service paths; the resolution
// queries scope themselves by the unique key/origin column.
package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// PublishableKey is a public, revocable identifier a self-hosted storefront
// client (merchant-authored static site / SPA / native app) sends in
// X-Publishable-Key to declare which shop it represents (design D1).
//
// It is an IDENTIFIER, NOT A CREDENTIAL: the value ships inside front-end
// JavaScript, is stored in plaintext and indexed (hashing would only make
// resolution costlier and the back office unable to display it), and its
// disclosure is not a security incident. It grants nothing — access control
// stays with the member JWT, RBAC, CORS, rate limiting and each endpoint's own
// authorization rules.
//
// revoked_at is a soft delete (design D4): key is globally unique, so keeping
// the row guarantees a revoked string can never be resurrected by a random
// collision or a manual re-create, and preserves the "whose key was this?"
// audit answer. Rotation needs several live keys per shop at once, which is
// why this is a table rather than a column on shops.
type PublishableKey struct {
	ent.Schema
}

func (PublishableKey) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		// key: "pk_" + 32 lowercase base32 chars (design D8). Unique across
		// the platform; stored in plaintext by design (see type comment).
		field.String("key").MaxLen(64).SchemaType(map[string]string{dialect.Postgres: "varchar(64)"}).NotEmpty().Unique(),
		// label is a human-facing note about which deployment holds the key
		// ("ondo-prod", "ondo-preview"). Purely descriptive — no code path
		// makes any decision from it.
		field.String("label").MaxLen(100).SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}).Default(""),
		field.Time("created_at").Immutable().Default(time.Now),
		field.Time("revoked_at").Optional().Nillable(),
	}
}

func (PublishableKey) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
	}
}

func (PublishableKey) Indexes() []ent.Index {
	return []ent.Index{
		// The back-office listing ("this shop's keys") and the cache
		// invalidation on shop status change both query by shop_id.
		index.Fields("shop_id"),
	}
}

func (PublishableKey) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "publishable_keys"},
	}
}

// ShopAllowedOrigin registers a browser origin that may call the API
// cross-origin on a shop's behalf (design D3/D5).
//
// origin is globally unique on purpose: the CORS preflight carries no
// publishable key (custom headers have no value at that stage), so the only
// available lookup is origin → "registered by anyone?". A globally unique
// column makes that lookup a single point read and prevents one merchant from
// claiming another's origin. Platform-owned origins (admin SPA, web dev,
// any future platform-hosted multi-tenant front end) stay in the
// CORS_ALLOWED_ORIGINS environment list and are never rows here.
//
// Origins live on the shop rather than on a key because rotation must not
// require duplicating the origin list across the old and new key (design D5).
type ShopAllowedOrigin struct {
	ent.Schema
}

func (ShopAllowedOrigin) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		// origin is normalized to scheme://host[:port] — lowercase, no path,
		// no trailing slash (tenant.NormalizeOrigin).
		field.String("origin").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).NotEmpty().Unique(),
		field.Time("created_at").Immutable().Default(time.Now),
	}
}

func (ShopAllowedOrigin) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
	}
}

func (ShopAllowedOrigin) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id"),
	}
}

func (ShopAllowedOrigin) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "shop_allowed_origins"},
	}
}
