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

// ContentSchema is one version of a shop's content schema: the JSON Schema
// documents that validate its page content (one per page type) and its
// shop-global content (change retire-hosted-storefront-and-themes group 1 =
// the first slice of headless-content-store).
//
// Each row is an immutable version; the shop's active schema is its row with
// the highest version. schema_json format 1 is
//
//	{"format":1,
//	 "types":{"<type_key>":<JSON Schema>},
//	 "type_order":["<type_key>", ...],
//	 "global":<JSON Schema>}
//
// type_order keeps the page types in their registered order (admin page-types
// listing). Rows written by the one-time backfill (source "theme_backfill") may
// also carry a "component_keys" object copied from the removed theme system;
// readers ignore it. source: "theme_backfill" or "registered".
//
// format 2 is what a human registers through the admin API (design D3):
//
//	{"format":2,
//	 "types":{"<type_key>":{"title":"...","schema":<JSON Schema>}},
//	 "documents":{"<key>":{"type":"<type_key>"}},
//	 "collections":{"<prefix->":{"type":"<type_key>","max":N}}}
//
// The type "global" is the shop-global content schema.
type ContentSchema struct {
	ent.Schema
}

func (ContentSchema) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("version"),
		field.JSON("schema_json", json.RawMessage{}),
		// sha256: lower-case hex digest of the normalized document, used to
		// detect "same schema registered again". Rows written by the data
		// backfill carry the digest of the jsonb text form instead; readers
		// that compare must recompute from schema_json (contentschema does).
		field.String("sha256").MaxLen(64).SchemaType(map[string]string{dialect.Postgres: "varchar(64)"}).NotEmpty(),
		field.String("source").MaxLen(30).SchemaType(map[string]string{dialect.Postgres: "varchar(30)"}).NotEmpty(),
		// created_by_user_id: the human who registered this version (change
		// headless-content-store, first slice). NULL for the backfill and the
		// demo seed. No FK on purpose: the audit trail must outlive the account.
		field.Int("created_by_user_id").Optional().Nillable(),
		// snapshot_json: the content of every document this registration put at
		// risk (its draft and, when different, its published snapshot, as they
		// were just before the version took effect), so a removed or renamed
		// field's values are never lost: {"documents":[{"kind","key","draft",
		// "published"}]}. NULL when nothing was at risk. Written once with the
		// row, never updated.
		field.JSON("snapshot_json", json.RawMessage{}).Optional(),
		field.Time("created_at").Immutable().Default(time.Now),
	}
}

func (ContentSchema) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
	}
}

func (ContentSchema) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id", "version").Unique(),
	}
}

func (ContentSchema) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table:  "content_schemas",
			Checks: map[string]string{"content_schemas_version_check": "version >= 1"},
		},
	}
}
