package schema

import (
	"encoding/json"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// User is a back-office account (platform staff or merchant staff).
// email is stored lowercase-normalized. status: 0 停用 / 1 啟用.
//
// password_hash NULL = the password has not been set yet (change
// platform-account-provisioning, design D1): the account cannot log in until
// its owner consumes a setup link. Same expression as members.password_hash.
type User struct {
	ent.Schema
}

func (User) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("email").MaxLen(150).SchemaType(map[string]string{dialect.Postgres: "varchar(150)"}).NotEmpty().Unique(),
		field.String("password_hash").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).Optional().Nillable().Sensitive(),
		field.Int16("status").Default(1),
		field.JSON("meta", json.RawMessage{}).Default(json.RawMessage("{}")),
		// display_name is what teammates see in the design history (design
		// history follow-up). Optional free text the user chose for themself;
		// never derived from email. See docs/design-history-privacy.md.
		field.String("display_name").MaxLen(80).SchemaType(map[string]string{dialect.Postgres: "varchar(80)"}).Optional().Nillable(),
	}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("shop_users", ShopUser.Type).Ref("user"),
		edge.From("role_users", RoleUser.Type).Ref("user"),
		edge.From("user_permissions", UserPermission.Type).Ref("user"),
		edge.From("refresh_tokens", UserRefreshToken.Type).Ref("user"),
		edge.From("credential_tokens", AdminCredentialToken.Type).Ref("user"),
	}
}

func (User) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Checks: map[string]string{
				"users_status_check": "status IN (0, 1)",
			},
		},
	}
}

// ShopUser is pure shop-membership for back-office users (design D4: platform
// admins are NOT represented here — they hold platform-scope roles instead).
type ShopUser struct {
	ent.Schema
}

func (ShopUser) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "shop_user",
			Checks: map[string]string{
				"shop_user_status_check": "status IN (0, 1)",
			},
		},
	}
}

func (ShopUser) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("user_id"),
		// status is the membership state (change merchant-staff-role, design
		// D3): 0 disabled / 1 active. It is NOT the account state
		// (users.status): a shop owner can only affect their own shop.
		field.Int16("status").Default(1),
		field.Time("created_at").Immutable().Default(time.Now),
	}
}

func (ShopUser) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		edge.To("user", User.Type).Unique().Required().Field("user_id"),
	}
}

func (ShopUser) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id", "user_id").Unique(),
		index.Fields("user_id"),
	}
}

// Member is a per-shop storefront consumer identity (change
// member-identity-per-shop, design D1): every row belongs to exactly one shop
// and email/phone are unique *within* that shop, never platform-wide. The
// same email at two shops is two entirely independent identities with
// independent password_hash/status/points/level — a merchant's plaintext
// password never reaches beyond its own shop.
//
// members is tenant-owned data registered in api/internal/tenant.tenantOwned
// (design D4): inside a tenant context every read/write is auto-scoped to the
// context shop. Back-office code paths run *without* tenant context and MUST
// therefore pass shop_id explicitly.
//
// points/level_id back the member-tiers-and-points loyalty feature, merged in
// from the deleted shop_member table (design D2): points is a maintained
// cache of the point_transactions ledger (schema.PointTransaction), level_id
// points at the highest MemberTier the member currently qualifies for, kept
// in sync by points.Service — never written directly by any other package.
//
// password_hash NULL is reserved for future social login / passwordless
// guest identities. status: 0 停用 / 1 啟用.
type Member struct {
	ent.Schema
}

func (Member) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Member) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		// email/phone carry no .Unique() here on purpose (design D3): the
		// real constraints are UNIQUE(shop_id, email)/UNIQUE(shop_id, phone),
		// created by the handwritten migration so their Postgres-default
		// NULLS DISTINCT semantics stay explicit and reviewable — a shop MUST
		// be able to hold many email-NULL rows (phone-only registration,
		// future guest identities).
		field.String("email").MaxLen(150).SchemaType(map[string]string{dialect.Postgres: "varchar(150)"}).Optional().Nillable(),
		field.String("phone").MaxLen(50).SchemaType(map[string]string{dialect.Postgres: "varchar(50)"}).Optional().Nillable(),
		field.String("password_hash").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).Optional().Nillable().Sensitive(),
		field.Int16("status").Default(1),
		field.Int32("points").Default(0),
		// level_id (member-tiers-and-points design D7): field.Int (not
		// Int32) to match MemberTier's default bigint id column — required
		// for the edge below (ent rejects a Field() type mismatch against
		// the target entity's id type).
		field.Int("level_id").Optional().Nillable(),
		field.JSON("meta", json.RawMessage{}).Default(json.RawMessage("{}")),
	}
}

func (Member) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		// member-tiers-and-points design D7: deleting a MemberTier a member
		// currently holds clears level_id (SetNull) rather than blocking the
		// delete or cascading — see MemberTier's own doc comment.
		edge.To("member_tier", MemberTier.Type).Unique().Field("level_id").
			Annotations(entsql.OnDelete(entsql.SetNull)),
		edge.From("refresh_tokens", MemberRefreshToken.Type).Ref("member"),
	}
}

func (Member) Indexes() []ent.Index {
	return []ent.Index{
		// Only the tenant-scoping lookup index is declared here. The
		// (shop_id, email)/(shop_id, phone) pairs are covered by the
		// handwritten UNIQUE indexes (design D3) — declaring non-unique
		// copies of the same column pairs would create literally duplicate
		// B-trees, so they are deliberately not repeated.
		index.Fields("shop_id"),
	}
}

func (Member) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Checks: map[string]string{
				"members_status_check": "status IN (0, 1)",
			},
		},
	}
}
