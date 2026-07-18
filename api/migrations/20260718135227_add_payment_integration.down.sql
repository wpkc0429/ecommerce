-- reverse: create index "payment_shop_id_order_id" to table: "payments"
DROP INDEX "payment_shop_id_order_id";
-- reverse: create index "payment_provider_provider_reference" to table: "payments"
DROP INDEX "payment_provider_provider_reference";
-- reverse: create "payments" table
DROP TABLE "payments";
