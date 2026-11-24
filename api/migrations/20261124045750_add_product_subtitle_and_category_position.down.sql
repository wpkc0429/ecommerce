-- reverse: modify "products" table
ALTER TABLE "products" DROP COLUMN "subtitle";
-- reverse: drop index "productcategory_category_id" from table: "product_category"
CREATE INDEX "productcategory_category_id" ON "product_category" ("category_id");
-- reverse: create index "productcategory_category_id_position_product_id" to table: "product_category"
DROP INDEX "productcategory_category_id_position_product_id";
-- reverse: modify "product_category" table
ALTER TABLE "product_category" DROP CONSTRAINT "product_category_position_check", DROP COLUMN "position";
