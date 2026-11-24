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

// AdminCredentialToken is a one-time password setup / reset token for a
// back-office account (change platform-account-provisioning, design D2).
//
// Only the SHA-256 of the token is stored; the plaintext exists once, in the
// link the caller receives. A token is consumed with a conditional UPDATE
// (used_at IS NULL AND expires_at > now), so exactly one concurrent consumer
// wins. Issuing a new token of the same kind for a user retires the older
// unused ones (used_at set), so a re-issued link never leaves two live keys.
//
// The table carries no shop_id, so it is not tenant-owned.
type AdminCredentialToken struct {
	ent.Schema
}

func (AdminCredentialToken) Fields() []ent.Field {
	return []ent.Field{
		field.Int("user_id"),
		// kind: setup (new account) | reset (existing account).
		field.String("kind").MaxLen(16).SchemaType(map[string]string{dialect.Postgres: "varchar(16)"}).NotEmpty(),
		field.String("token_hash").MaxLen(64).SchemaType(map[string]string{dialect.Postgres: "varchar(64)"}).NotEmpty().Unique(),
		// created_by is the issuing admin's user id; NULL when the system
		// issued it. No FK on purpose: the audit trail must outlive the admin.
		field.Int("created_by").Optional().Nillable(),
		field.Time("expires_at"),
		field.Time("used_at").Optional().Nillable(),
		field.Time("created_at").Immutable().Default(time.Now),
	}
}

func (AdminCredentialToken) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("user", User.Type).Unique().Required().Field("user_id"),
	}
}

func (AdminCredentialToken) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "kind"),
	}
}

func (AdminCredentialToken) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "admin_credential_tokens",
			Checks: map[string]string{
				"admin_credential_tokens_kind_check": "kind IN ('setup', 'reset')",
			},
		},
	}
}
