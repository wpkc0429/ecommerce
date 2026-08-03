-- reverse: change ecpay-per-shop-credentials.
--
-- Rolling back drops every shop's stored ecpay credentials along with the
-- table. That is the inherent property of dropping a new table, not a
-- defect — a re-apply starts with an empty table, same as the first apply,
-- and each shop configures its credentials again.
DROP INDEX "shoppaymentprovidercredential_shop_id_provider";
DROP TABLE "shop_payment_provider_credentials";
