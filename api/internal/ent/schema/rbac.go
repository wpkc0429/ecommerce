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

// Role is a named permission bundle. scope: 'platform' | 'merchant'
// (design D4; PRD 的 guard_name 更名為 scope).
type Role struct {
	ent.Schema
}

func (Role) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Role) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").MaxLen(50).SchemaType(map[string]string{dialect.Postgres: "varchar(50)"}).NotEmpty(),
		field.String("scope").MaxLen(20).SchemaType(map[string]string{dialect.Postgres: "varchar(20)"}),
	}
}

func (Role) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("permissions", Permission.Type).Through("role_permissions", RolePermission.Type),
		edge.From("role_users", RoleUser.Type).Ref("role"),
	}
}

func (Role) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name", "scope").Unique(),
	}
}

func (Role) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Checks: map[string]string{
				"roles_scope_check": "scope IN ('platform', 'merchant')",
			},
		},
	}
}

// Permission is a node in the permission catalog (e.g. page.publish).
type Permission struct {
	ent.Schema
}

func (Permission) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").MaxLen(100).SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}).NotEmpty().Unique(),
		field.String("description").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).Default(""),
	}
}

func (Permission) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("roles", Role.Type).Ref("permissions").Through("role_permissions", RolePermission.Type),
		edge.From("user_permissions", UserPermission.Type).Ref("permission"),
	}
}

// RoleUser assigns a role to a user, scoped by shop_id
// (NULL = platform level; design D4).
//
// UNIQUE NULLS NOT DISTINCT (user_id, role_id, shop_id) cannot be expressed in
// ent and is added by the handwritten migration (task 2.2). The surrogate id
// column is an ent requirement — a deliberate, harmless deviation from D2.
type RoleUser struct {
	ent.Schema
}

func (RoleUser) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "role_user"},
	}
}

func (RoleUser) Fields() []ent.Field {
	return []ent.Field{
		field.Int("user_id"),
		field.Int("role_id"),
		field.Int("shop_id").Optional().Nillable(),
	}
}

func (RoleUser) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("user", User.Type).Unique().Required().Field("user_id"),
		edge.To("role", Role.Type).Unique().Required().Field("role_id"),
		edge.To("shop", Shop.Type).Unique().Field("shop_id").
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (RoleUser) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "shop_id"),
		index.Fields("role_id"),
	}
}

// RolePermission links roles to permissions. Composite PK (role_id, permission_id).
type RolePermission struct {
	ent.Schema
}

func (RolePermission) Annotations() []schema.Annotation {
	return []schema.Annotation{
		field.ID("role_id", "permission_id"),
		entsql.Annotation{Table: "role_permission"},
	}
}

func (RolePermission) Fields() []ent.Field {
	return []ent.Field{
		field.Int("role_id"),
		field.Int("permission_id"),
	}
}

func (RolePermission) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("role", Role.Type).Unique().Required().Field("role_id"),
		edge.To("permission", Permission.Type).Unique().Required().Field("permission_id"),
	}
}

// UserPermission is a per-user override, scoped by shop_id (NULL = platform
// level). is_granted=false is a forced denial that beats role grants
// (design D4 tier 1).
//
// UNIQUE NULLS NOT DISTINCT (user_id, permission_id, shop_id) lives in the
// handwritten migration; surrogate id as in RoleUser.
type UserPermission struct {
	ent.Schema
}

func (UserPermission) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "user_permission"},
	}
}

func (UserPermission) Fields() []ent.Field {
	return []ent.Field{
		field.Int("user_id"),
		field.Int("permission_id"),
		field.Int("shop_id").Optional().Nillable(),
		field.Bool("is_granted"),
	}
}

func (UserPermission) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("user", User.Type).Unique().Required().Field("user_id"),
		edge.To("permission", Permission.Type).Unique().Required().Field("permission_id"),
		edge.To("shop", Shop.Type).Unique().Field("shop_id").
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (UserPermission) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "shop_id"),
	}
}
