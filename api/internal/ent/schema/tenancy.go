package schema

import (
	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Shop is a tenant (merchant). Design D2.
// status: 0 停用 / 1 啟用 / 2 審核中.
type Shop struct {
	ent.Schema
}

func (Shop) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Shop) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").MaxLen(100).SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}).NotEmpty(),
		field.Int16("status").Default(1),
		// Global payload (validated against the shop's content schema, the
		// "global" document of its active content_schemas version).
		field.JSON("content_json", json.RawMessage{}).Default(json.RawMessage("{}")),
		field.JSON("meta", json.RawMessage{}).Default(json.RawMessage("{}")),
		// timezone (change merchant-reports, design D1): the shop's IANA time
		// zone name, and the single authority for what "one day" means anywhere
		// in this system.
		//
		// **NULL is not the same fact as "Asia/Taipei".** NULL means "this
		// merchant has never set a time zone"; a value means they chose one.
		// Today both behave identically (reports fall back to
		// REPORT_DEFAULT_TIMEZONE), but collapsing them costs two things:
		// the merchant can no longer tell whether they still need to set it,
		// and the platform can no longer change its default and have
		// never-configured shops follow — a backfilled column turns that
		// setting into one that silently affects nobody.
		//
		// Existing rows are therefore NOT backfilled. The migration adds a
		// nullable column and touches no stored value.
		//
		// An IANA NAME, never a fixed UTC offset: an offset cannot express a
		// daylight-saving transition, and the resulting error appears only on
		// the switchover day, silently, in a report nothing validates.
		//
		// Resolution and validation live in internal/shoptz — there is exactly
		// one fallback implementation, not one per caller.
		field.String("timezone").MaxLen(64).
			SchemaType(map[string]string{dialect.Postgres: "varchar(64)"}).
			Optional().Nillable(),
		// admin_branding (change admin-api-public-exposure, design D8): per-shop
		// look of the SHARED merchant admin SPA - {logo_url, primary_color,
		// accent_color}. Pure data; the API never branches on which front end
		// reads it. Written through PUT /admin/shops/{id}/settings.
		field.JSON("admin_branding", json.RawMessage{}).Default(json.RawMessage("{}")).
			Annotations(entsql.Default("{}")),
	}
}

func (Shop) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("pages", Page.Type),
	}
}

func (Shop) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Checks: map[string]string{
				"shops_status_check": "status IN (0, 1, 2)",
			},
		},
	}
}
