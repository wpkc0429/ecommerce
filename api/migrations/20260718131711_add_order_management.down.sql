-- reverse: create index "orderitem_shop_id" to table: "order_items"
DROP INDEX "orderitem_shop_id";
-- reverse: create index "orderitem_order_id" to table: "order_items"
DROP INDEX "orderitem_order_id";
-- reverse: create "order_items" table
DROP TABLE "order_items";
-- reverse: create index "order_shop_id_status" to table: "orders"
DROP INDEX "order_shop_id_status";
-- reverse: create index "order_shop_id_member_id" to table: "orders"
DROP INDEX "order_shop_id_member_id";
-- reverse: create "orders" table
DROP TABLE "orders";
