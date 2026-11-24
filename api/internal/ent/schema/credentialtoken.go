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
// Change merchant-staff-role (design D5) adds kind "invite": a shop owner's
// invitation of an e-mail address to a shop with a role. An invite row has no
// user (user_id NULL: inviting never creates an account) and carries email,
// shop_id and role_id instead; the composition CHECKs are handwritten in the
// migration (atlas does not detect CHECK expression changes). The table now
// has a shop_id column but is deliberately NOT tenant-owned: invitations are
// looked up by token hash on unauthenticated endpoints, outside any tenant
// context (see notTenantOwned in internal/tenant).
type AdminCredentialToken struct {
	ent.Schema
}

func (AdminCredentialToken) Fields() []ent.Field {
	return []ent.Field{
		// user_id is NULL for invite rows only.
		field.Int("user_id").Optional().Nillable(),
		// kind: setup (new account) | reset (existing account) | invite.
		field.String("kind").MaxLen(16).SchemaType(map[string]string{dialect.Postgres: "varchar(16)"}).NotEmpty(),
		field.String("token_hash").MaxLen(64).SchemaType(map[string]string{dialect.Postgres: "varchar(64)"}).NotEmpty().Unique(),
		// created_by is the issuing admin's user id; NULL when the system
		// issued it. No FK on purpose: the audit trail must outlive the admin.
		field.Int("created_by").Optional().Nillable(),
		// invite-only columns (all NULL for setup/reset).
		field.String("email").MaxLen(150).SchemaType(map[string]string{dialect.Postgres: "varchar(150)"}).Optional().Nillable(),
		field.Int("shop_id").Optional().Nillable(),
		field.Int("role_id").Optional().Nillable(),
		field.Time("expires_at"),
		field.Time("used_at").Optional().Nillable(),
		field.Time("created_at").Immutable().Default(time.Now),
	}
}

func (AdminCredentialToken) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("user", User.Type).Unique().Field("user_id").Annotations(entsql.OnDelete(entsql.NoAction)),
		edge.To("role", Role.Type).Unique().Field("role_id").Annotations(entsql.OnDelete(entsql.NoAction)),
	}
}

func (AdminCredentialToken) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "kind"),
		index.Fields("shop_id", "email"),
	}
}

func (AdminCredentialToken) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "admin_credential_tokens",
			Checks: map[string]string{
				"admin_credential_tokens_kind_check":         "kind IN ('setup', 'reset', 'invite')",
				"admin_credential_tokens_invite_shape_check": "(kind = 'invite') = (shop_id IS NOT NULL AND role_id IS NOT NULL AND email IS NOT NULL)",
				"admin_credential_tokens_user_shape_check":   "(kind <> 'invite') = (user_id IS NOT NULL)",
			},
		},
	}
}
