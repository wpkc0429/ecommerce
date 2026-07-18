// Package schema — product catalog entities (change product-catalog, design
// D1–D9). All four tables below are shop-owned tenant data and are
// registered in api/internal/tenant.tenantOwned; each carries a direct
// shop_id column (including the join table ProductCategory) so the
// interceptor/hook can scope them without joining through their parent
// entities (design D7).
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

// Category is a shop-owned, tree-structured product category (design D3).
// parent_id NULL marks a root category. Cycle prevention on parent_id
// reassignment is enforced in the catalog service layer, not the database
// (design D3 — no efficient way to express "no cycles" as a SQL CHECK).
type Category struct {
	ent.Schema
}

func (Category) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Category) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("parent_id").Optional().Nillable(),
		field.String("name").MaxLen(100).SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}).NotEmpty(),
		field.String("slug").MaxLen(150).SchemaType(map[string]string{dialect.Postgres: "varchar(150)"}).NotEmpty(),
		field.Int32("position").Default(0),
	}
}

func (Category) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		// Self-reference (design D3): "children" is the assoc edge, "parent"
		// its unique inverse bound to the parent_id column declared above.
		edge.To("children", Category.Type).
			From("parent").
			Unique().
			Field("parent_id"),
		// M2M with Product via the ProductCategory edge schema (design D2).
		// No OnDelete(Cascade) on the category side — a category with
		// product associations cannot be deleted at the service layer
		// (design D4 RESTRICT), and the bare FK backs that up in the DB.
		edge.From("products", Product.Type).Ref("categories").Through("product_categories", ProductCategory.Type),
	}
}

func (Category) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id", "name").Unique(),
		index.Fields("shop_id", "slug").Unique(),
		index.Fields("parent_id"),
	}
}

// Product is a shop-owned catalog product (design D2). status: 0 草稿 / 1 已發佈
// (semantics mirror Page). meta carries SEO fields (seo_title/seo_keywords/
// seo_description), same shape as Page.meta.
type Product struct {
	ent.Schema
}

func (Product) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Product) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.String("title").MaxLen(200).SchemaType(map[string]string{dialect.Postgres: "varchar(200)"}).NotEmpty(),
		field.String("slug").MaxLen(200).SchemaType(map[string]string{dialect.Postgres: "varchar(200)"}).NotEmpty(),
		field.Text("description").Default(""),
		field.Int16("status").Default(0),
		field.JSON("meta", json.RawMessage{}).Default(json.RawMessage("{}")),
	}
}

func (Product) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		// OnDelete(Cascade) MUST live on this assoc ("To") edge, not on
		// ProductSKU's inverse "product" edge below — ent's SQL diff only
		// consults the assoc edge's annotations when generating the FK
		// (entc/gen/graph.go skips inverse edges), so an annotation placed
		// on the inverse side is silently ignored.
		edge.To("skus", ProductSKU.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		// M2M with Category (design D2). OnDelete(Cascade) on the product
		// side of the join lives on ProductCategory's "product" edge below —
		// deleting a product cleans up its category associations.
		edge.To("categories", Category.Type).Through("product_categories", ProductCategory.Type),
	}
}

func (Product) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id", "slug").Unique(),
		index.Fields("shop_id", "status"),
	}
}

func (Product) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Checks: map[string]string{
				"products_status_check": "status IN (0, 1)",
			},
		},
	}
}

// ProductSKU is a purchasable variant of a Product (design D1/D5/D6). Money
// fields (price_amount) are BIGINT integer minor units — see design D1, the
// binding contract for every later Phase 2 proposal (cart/order/payment):
// never float, never numeric. stock_qty is a single non-negative counter
// (design D6) — no multi-warehouse or reservation locking in this phase, but
// the column is standalone so those can be layered on without touching it.
type ProductSKU struct {
	ent.Schema
}

func (ProductSKU) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (ProductSKU) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "product_skus"},
		entsql.Annotation{
			Checks: map[string]string{
				"product_skus_price_amount_check": "price_amount >= 0",
				"product_skus_stock_qty_check":    "stock_qty >= 0",
			},
		},
	}
}

func (ProductSKU) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("product_id"),
		field.String("sku_code").MaxLen(64).SchemaType(map[string]string{dialect.Postgres: "varchar(64)"}).NotEmpty(),
		// Option values keyed by attribute name (e.g. {"size":"M","color":"red"}).
		// A JSON object, not an array (design D5): options are always looked
		// up by known key, never iterated in display order, so jsonb's lack
		// of key-order preservation (unlike CMS page_schema.sections, which
		// IS order-sensitive and therefore an array) is safe here.
		field.JSON("options", json.RawMessage{}).Default(json.RawMessage("{}")),
		// Money: integer minor units, BIGINT/int64 (design D1). Never float.
		field.Int64("price_amount"),
		// ISO 4217 alpha code. Per-SKU (not shop-global) to leave room for
		// multi-currency shops later; this phase does not implement currency
		// conversion and all seed/demo data is single-currency TWD.
		field.String("currency").MaxLen(3).SchemaType(map[string]string{dialect.Postgres: "varchar(3)"}).Default("TWD"),
		field.Int32("stock_qty").Default(0),
		field.Bool("is_active").Default(true),
	}
}

func (ProductSKU) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		edge.From("product", Product.Type).Ref("skus").
			Unique().Required().Field("product_id"),
	}
}

func (ProductSKU) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id", "sku_code").Unique(),
		index.Fields("product_id"),
	}
}

// ProductCategory is the Product⇄Category M2M join (design D2). shop_id is
// duplicated here (defense in depth, design D7) even though it is derivable
// via product_id/category_id, so the tenant interceptor/hook — which
// requires a direct shop_id column — covers this table too.
type ProductCategory struct {
	ent.Schema
}

func (ProductCategory) Annotations() []schema.Annotation {
	return []schema.Annotation{
		field.ID("product_id", "category_id"),
		entsql.Annotation{Table: "product_category"},
	}
}

func (ProductCategory) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("product_id"),
		field.Int("category_id"),
	}
}

func (ProductCategory) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("shop", Shop.Type).Unique().Required().Field("shop_id"),
		// Cascade on the product side: deleting a product removes its
		// category associations (products have no RESTRICT semantics).
		edge.To("product", Product.Type).Unique().Required().Field("product_id").
			Annotations(entsql.OnDelete(entsql.Cascade)),
		// No cascade on the category side: category deletion is guarded by
		// the service layer (design D4 RESTRICT) whenever associations
		// exist; the bare FK is a defense-in-depth backstop.
		edge.To("category", Category.Type).Unique().Required().Field("category_id"),
	}
}

func (ProductCategory) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("category_id"),
	}
}
