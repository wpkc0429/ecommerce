-- reverse: create index "productsku_shop_id_sku_code" to table: "product_skus"
DROP INDEX "productsku_shop_id_sku_code";
-- reverse: create index "productsku_product_id" to table: "product_skus"
DROP INDEX "productsku_product_id";
-- reverse: create "product_skus" table
DROP TABLE "product_skus";
-- reverse: create index "productcategory_category_id" to table: "product_category"
DROP INDEX "productcategory_category_id";
-- reverse: create "product_category" table
DROP TABLE "product_category";
-- reverse: create index "product_shop_id_status" to table: "products"
DROP INDEX "product_shop_id_status";
-- reverse: create index "product_shop_id_slug" to table: "products"
DROP INDEX "product_shop_id_slug";
-- reverse: create "products" table
DROP TABLE "products";
-- reverse: create index "category_shop_id_slug" to table: "categories"
DROP INDEX "category_shop_id_slug";
-- reverse: create index "category_shop_id_name" to table: "categories"
DROP INDEX "category_shop_id_name";
-- reverse: create index "category_parent_id" to table: "categories"
DROP INDEX "category_parent_id";
-- reverse: create "categories" table
DROP TABLE "categories";
