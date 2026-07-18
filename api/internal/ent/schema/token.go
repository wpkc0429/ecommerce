package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UserRefreshToken stores hashed back-office refresh tokens with rotation
// lineage (rotated_from) and revocation (design D9).
type UserRefreshToken struct {
	ent.Schema
}

func (UserRefreshToken) Fields() []ent.Field {
	return []ent.Field{
		field.Int("user_id"),
		field.String("token_hash").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).NotEmpty().Unique(),
		field.Time("expires_at"),
		field.Time("revoked_at").Optional().Nillable(),
		field.Int("rotated_from").Optional().Nillable(),
		field.Time("created_at").Immutable().Default(time.Now),
	}
}

func (UserRefreshToken) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("user", User.Type).Unique().Required().Field("user_id"),
	}
}

func (UserRefreshToken) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id"),
	}
}

// MemberRefreshToken is the member-side counterpart, additionally bound to the
// shop context the token was issued in.
type MemberRefreshToken struct {
	ent.Schema
}

func (MemberRefreshToken) Fields() []ent.Field {
	return []ent.Field{
		field.Int("member_id"),
		field.Int("shop_id"),
		field.String("token_hash").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).NotEmpty().Unique(),
		field.Time("expires_at"),
		field.Time("revoked_at").Optional().Nillable(),
		field.Int("rotated_from").Optional().Nillable(),
		field.Time("created_at").Immutable().Default(time.Now),
	}
}

func (MemberRefreshToken) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("member", Member.Type).Unique().Required().Field("member_id"),
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
	}
}

func (MemberRefreshToken) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("member_id"),
		index.Fields("shop_id"),
	}
}
