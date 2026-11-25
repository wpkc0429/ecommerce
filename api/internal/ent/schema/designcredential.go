package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DesignServiceCredential authenticates the AI edit service for exactly one
// shop (change design-history D8). Only the SHA-256 of the key is stored; the
// key is shown once at issue time. It is deliberately NOT a tenant-owned table:
// authentication looks the credential up by hash before any shop is known.
type DesignServiceCredential struct {
	ent.Schema
}

func (DesignServiceCredential) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.String("name").MaxLen(100).SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}).NotEmpty(),
		field.String("key_prefix").MaxLen(12).SchemaType(map[string]string{dialect.Postgres: "varchar(12)"}),
		field.String("key_hash").MaxLen(64).SchemaType(map[string]string{dialect.Postgres: "char(64)"}),
		field.JSON("allowed_cidrs", []string{}).Optional(),
		field.Int("created_by_user_id"),
		field.Time("created_at").Immutable().Default(time.Now),
		field.Time("expires_at"),
		field.Time("last_used_at").Optional().Nillable(),
		field.Time("revoked_at").Optional().Nillable(),
	}
}

func (DesignServiceCredential) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("key_hash").Unique().StorageKey("design_service_credentials_key_hash_key"),
		index.Fields("shop_id").StorageKey("design_service_credentials_shop"),
	}
}

func (DesignServiceCredential) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "design_service_credentials",
			Checks: map[string]string{
				"design_service_credentials_expiry_check": "expires_at > created_at AND expires_at <= created_at + interval '366 days'",
			},
		},
	}
}
