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
type User struct {
	ent.Schema
}

func (User) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("email").MaxLen(150).SchemaType(map[string]string{dialect.Postgres: "varchar(150)"}).NotEmpty().Unique(),
		field.String("password_hash").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).NotEmpty().Sensitive(),
		field.Int16("status").Default(1),
		field.JSON("meta", json.RawMessage{}).Default(json.RawMessage("{}")),
	}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("shop_users", ShopUser.Type).Ref("user"),
		edge.From("role_users", RoleUser.Type).Ref("user"),
		edge.From("user_permissions", UserPermission.Type).Ref("user"),
		edge.From("refresh_tokens", UserRefreshToken.Type).Ref("user"),
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
		entsql.Annotation{Table: "shop_user"},
	}
}

func (ShopUser) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("user_id"),
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

// Member is a platform-wide storefront consumer identity (email/phone unique
// across the platform by design); per-shop membership lives in shop_member.
// password_hash NULL is reserved for future social login. status: 0 停用 / 1 啟用.
type Member struct {
	ent.Schema
}

func (Member) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Member) Fields() []ent.Field {
	return []ent.Field{
		field.String("email").MaxLen(150).SchemaType(map[string]string{dialect.Postgres: "varchar(150)"}).Optional().Nillable().Unique(),
		field.String("phone").MaxLen(50).SchemaType(map[string]string{dialect.Postgres: "varchar(50)"}).Optional().Nillable().Unique(),
		field.String("password_hash").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).Optional().Nillable().Sensitive(),
		field.Int16("status").Default(1),
		field.JSON("meta", json.RawMessage{}).Default(json.RawMessage("{}")),
	}
}

func (Member) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("shop_members", ShopMember.Type).Ref("member"),
		edge.From("refresh_tokens", MemberRefreshToken.Type).Ref("member"),
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

// ShopMember is the per-shop membership of a member (跨店會籍隔離).
// points/level_id back the member-tiers-and-points loyalty feature: points
// is a maintained cache of the point_transactions ledger (design D6,
// schema.PointTransaction), level_id points at the highest MemberTier the
// member currently qualifies for (design D7), kept in sync by
// points.Service — never written directly by any other package.
type ShopMember struct {
	ent.Schema
}

func (ShopMember) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "shop_member"},
	}
}

func (ShopMember) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (ShopMember) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("member_id"),
		field.Int32("points").Default(0),
		// level_id (member-tiers-and-points design D7): field.Int (not
		// Int32) to match MemberTier's default bigint id column — required
		// for the edge below (ent rejects a Field() type mismatch against
		// the target entity's id type).
		field.Int("level_id").Optional().Nillable(),
	}
}

func (ShopMember) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		edge.To("member", Member.Type).Unique().Required().Field("member_id"),
		// member-tiers-and-points design D7: deleting a MemberTier a member
		// currently holds clears level_id (SetNull) rather than blocking the
		// delete or cascading — see MemberTier's own doc comment.
		edge.To("member_tier", MemberTier.Type).Unique().Field("level_id").
			Annotations(entsql.OnDelete(entsql.SetNull)),
	}
}

func (ShopMember) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id", "member_id").Unique(),
		index.Fields("member_id"),
	}
}
