// Package schema — media entities (change product-images-r2, design D3/D5).
// Both tables are shop-owned tenant data registered in tenant.tenantOwned.
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
	"github.com/google/uuid"
)

// MediaUpload is an upload INTENT (design D3): the API mints the object key
// and a presigned PUT URL, and later accepts only the intent's id — never a
// URL or an arbitrary key — so "only our image domain" is structural.
type MediaUpload struct {
	ent.Schema
}

func (MediaUpload) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.Int("shop_id"),
		field.String("object_key").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).NotEmpty(),
		field.String("content_type").MaxLen(50).SchemaType(map[string]string{dialect.Postgres: "varchar(50)"}).NotEmpty(),
		field.Int64("size_bytes"),
		// product_image | content_image
		field.String("purpose").MaxLen(20).SchemaType(map[string]string{dialect.Postgres: "varchar(20)"}).NotEmpty(),
		field.Int("created_by"),
		field.Time("created_at").Immutable().Default(time.Now),
		field.Time("expires_at"),
		// consumed_at is the arbiter of "use an intent once": a conditional
		// UPDATE ... WHERE consumed_at IS NULL plus the affected-row count.
		field.Time("consumed_at").Optional().Nillable(),
	}
}

func (MediaUpload) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
	}
}

func (MediaUpload) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id", "id"),
	}
}

func (MediaUpload) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Checks: map[string]string{
				"media_uploads_purpose_check": "purpose IN ('product_image', 'content_image')",
				"media_uploads_size_check":    "size_bytes > 0",
			},
		},
	}
}

// ProductImage is one image of a product (design D5). image_key is the storage
// key WITHOUT variant or extension; URLs are composed at read time from
// IMAGE_PUBLIC_BASE_URL, so changing the domain needs no data migration.
type ProductImage struct {
	ent.Schema
}

func (ProductImage) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("product_id"),
		field.String("image_key").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).NotEmpty().Unique(),
		field.Int32("position").Default(0),
		field.String("alt").Default("").SchemaType(map[string]string{dialect.Postgres: "varchar(300)"}),
		field.Int32("width"),
		field.Int32("height"),
		field.Time("created_at").Immutable().Default(time.Now),
	}
}

func (ProductImage) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		edge.From("product", Product.Type).Ref("images").
			Unique().Required().Field("product_id"),
	}
}

func (ProductImage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("product_id", "position", "id"),
	}
}

func (ProductImage) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Checks: map[string]string{
				"product_images_position_check": "position >= 0",
			},
		},
	}
}
