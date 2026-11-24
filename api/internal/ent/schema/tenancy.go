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
		field.Int("theme_id").Optional().Nillable(),
		field.String("name").MaxLen(100).SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}).NotEmpty(),
		field.Int16("status").Default(1),
		// Global payload for the currently applied theme (validated against
		// themes.config_schema).
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
		edge.To("theme", Theme.Type).Unique().Field("theme_id"),
		edge.To("pages", Page.Type),
		edge.From("sites", Site.Type).Ref("shops").Through("site_mappings", SiteShop.Type),
	}
}

func (Shop) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("theme_id"),
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

// Site is a bound domain. domain is stored lowercase-normalized.
// ssl_status: 0 無 / 1 有效 / 2 失效（Phase 1 僅保留欄位，人工維護）.
type Site struct {
	ent.Schema
}

func (Site) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Site) Fields() []ent.Field {
	return []ent.Field{
		field.String("domain").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).NotEmpty().Unique(),
		field.Int16("ssl_status").Default(0),
		field.JSON("meta", json.RawMessage{}).Default(json.RawMessage("{}")),
	}
}

func (Site) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shops", Shop.Type).Through("site_mappings", SiteShop.Type),
	}
}

func (Site) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Checks: map[string]string{
				"sites_ssl_status_check": "ssl_status IN (0, 1, 2)",
			},
		},
	}
}

// SiteShop is the site↔shop many-to-many mapping with routing disambiguation
// (design D5): path_prefix NULL marks the site's default shop; is_primary
// marks the shop's primary domain. Composite PK (site_id, shop_id).
type SiteShop struct {
	ent.Schema
}

func (SiteShop) Annotations() []schema.Annotation {
	return []schema.Annotation{
		field.ID("site_id", "shop_id"),
		entsql.Annotation{Table: "site_shop"},
	}
}

func (SiteShop) Fields() []ent.Field {
	return []ent.Field{
		field.Int("site_id"),
		field.Int("shop_id"),
		// Normalized as "/prefix" (leading slash, no trailing slash).
		field.String("path_prefix").MaxLen(100).SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}).Optional().Nillable(),
		field.Bool("is_primary").Default(false),
		field.Time("created_at").Immutable().Default(time.Now),
	}
}

func (SiteShop) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("site", Site.Type).Unique().Required().Field("site_id"),
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
	}
}

func (SiteShop) Indexes() []ent.Index {
	return []ent.Index{
		// 同站前綴不重複（NULL 前綴由 partial unique 去重）。
		index.Fields("site_id", "path_prefix").Unique(),
		// 每站僅一個預設商家。
		index.Fields("site_id").Unique().
			Annotations(entsql.IndexWhere("path_prefix IS NULL")).
			StorageKey("site_shop_default_shop_per_site"),
		// 每商家僅一個主網域。
		index.Fields("shop_id").Unique().
			Annotations(entsql.IndexWhere("is_primary")).
			StorageKey("site_shop_primary_domain_per_shop"),
	}
}
