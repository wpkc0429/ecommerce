-- reverse: create index "cartitem_sku_id" to table: "cart_items"
DROP INDEX "cartitem_sku_id";
-- reverse: create index "cartitem_cart_id" to table: "cart_items"
DROP INDEX "cartitem_cart_id";
-- reverse: create "cart_items" table
DROP TABLE "cart_items";
-- reverse: create index "carts_one_active_per_member" to table: "carts"
DROP INDEX "carts_one_active_per_member";
-- reverse: create index "cart_member_id" to table: "carts"
DROP INDEX "cart_member_id";
-- reverse: create "carts" table
DROP TABLE "carts";
