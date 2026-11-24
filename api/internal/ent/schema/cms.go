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

// Page is a tenant-owned CMS page. type_key binds it dynamically to a page type
// of the shop's content schema (no FK — design D3). content_json is the
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
