-- reverse: create index "shippingmethod_shop_id_is_active" to table: "shipping_methods"
DROP INDEX "shippingmethod_shop_id_is_active";
-- reverse: create "shipping_methods" table
DROP TABLE "shipping_methods";
-- reverse: create index "shipment_shop_id_order_id" to table: "shipments"
DROP INDEX "shipment_shop_id_order_id";
-- reverse: create index "shipment_order_id" to table: "shipments"
DROP INDEX "shipment_order_id";
-- reverse: create "shipments" table
DROP TABLE "shipments";
