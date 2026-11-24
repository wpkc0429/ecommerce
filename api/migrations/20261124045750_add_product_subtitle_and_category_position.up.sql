-- modify "product_category" table
ALTER TABLE "product_category" ADD COLUMN "position" integer NOT NULL DEFAULT 0, ADD CONSTRAINT "product_category_position_check" CHECK ("position" >= 0);
-- create index "productcategory_category_id_position_product_id" to table: "product_category"
CREATE INDEX "productcategory_category_id_position_product_id" ON "product_category" ("category_id", "position", "product_id");
-- drop index "productcategory_category_id" from table: "product_category"
DROP INDEX "productcategory_category_id";
-- modify "products" table
ALTER TABLE "products" ADD COLUMN "subtitle" character varying(200) NOT NULL DEFAULT '';
