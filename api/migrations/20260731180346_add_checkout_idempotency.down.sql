-- change checkout-idempotency (design D3), reverse.
--
-- Hand-edited alongside the .up.sql: the generator's four "reverse: ..."
-- statements that would have re-created the handwritten constraints were
-- removed, because the up migration no longer drops them.
--
-- Rolling this back loses only the key → order mapping. Orders, stock, points
-- and carts are untouched, so nothing here is irreversible.

-- reverse: create index "checkoutidempotencykey_shop_id_idempotency_key" to table: "checkout_idempotency_keys"
DROP INDEX "checkoutidempotencykey_shop_id_idempotency_key";
-- reverse: create "checkout_idempotency_keys" table
DROP TABLE "checkout_idempotency_keys";
