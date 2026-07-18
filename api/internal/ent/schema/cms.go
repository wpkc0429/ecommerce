package schema

import (
	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Theme is a platform-managed theme. config_schema is a JSON Schema
// (draft 2020-12) describing the shop-global payload; layout_key maps to the
// storefront component library (design D1/D6).
type Theme struct {
	ent.Schema
}

func (Theme) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Theme) Fields() []ent.Field {
	return []ent.Field{
		field.String("code").MaxLen(50).SchemaType(map[string]string{dialect.Postgres: "varchar(50)"}).NotEmpty().Unique(),
		field.String("name").MaxLen(100).SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}).NotEmpty(),
		field.String("layout_key").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).NotEmpty(),
		field.JSON("config_schema", json.RawMessage{}).Default(json.RawMessage("{}")),
		field.Bool("is_active").Default(true),
	}
}

func (Theme) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("pages", ThemePage.Type),
		edge.From("shops", Shop.Type).Ref("theme"),
	}
}

// ThemePage registers a page type supported by a theme: type_key resolved
// dynamically from pages (design D3), component_key maps to the storefront
// component, page_schema is a JSON Schema (draft 2020-12).
type ThemePage struct {
	ent.Schema
}

func (ThemePage) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (ThemePage) Fields() []ent.Field {
	return []ent.Field{
		field.Int("theme_id"),
		field.String("type_key").MaxLen(50).SchemaType(map[string]string{dialect.Postgres: "varchar(50)"}).NotEmpty(),
		field.String("component_key").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).NotEmpty(),
		field.JSON("page_schema", json.RawMessage{}).Default(json.RawMessage("{}")),
	}
}

func (ThemePage) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("theme", Theme.Type).Ref("pages").Unique().Required().Field("theme_id"),
	}
}

func (ThemePage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("theme_id", "type_key").Unique(),
	}
}

// Page is a tenant-owned CMS page. type_key binds it to the shop's current
// theme dynamically (no FK to theme_pages — design D3). content_json is the
// working copy; published_json the published snapshot (design D7).
// status: 0 草稿 / 1 已發佈.
type Page struct {
	ent.Schema
}

func (Page) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Page) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.String("type_key").MaxLen(50).SchemaType(map[string]string{dialect.Postgres: "varchar(50)"}).NotEmpty(),
		field.String("title").MaxLen(150).SchemaType(map[string]string{dialect.Postgres: "varchar(150)"}).Default(""),
		field.String("slug").MaxLen(150).SchemaType(map[string]string{dialect.Postgres: "varchar(150)"}).NotEmpty(),
		field.Int16("status").Default(0),
		field.JSON("content_json", json.RawMessage{}).Default(json.RawMessage("{}")),
		field.JSON("published_json", json.RawMessage{}).Optional(),
		field.JSON("meta", json.RawMessage{}).Default(json.RawMessage("{}")),
	}
}

func (Page) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("shop", Shop.Type).Ref("pages").Unique().Required().Field("shop_id"),
	}
}

func (Page) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id", "slug").Unique(),
		index.Fields("shop_id", "status"),
	}
}

func (Page) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Checks: map[string]string{
				"pages_status_check": "status IN (0, 1)",
			},
		},
	}
}
