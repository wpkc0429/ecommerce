-- reverse of the up migration: drop the two tables added by change
-- storefront-client-auth. No data depends on them; the only loss is the
-- registered keys/origins themselves. Clients fall back to Host resolution
-- (design D7), which is exactly the pre-change behavior for every consumer in
-- this repository.

-- reverse: create index "shopallowedorigin_shop_id" to table: "shop_allowed_origins"
DROP INDEX "shopallowedorigin_shop_id";
-- reverse: create index "shop_allowed_origins_origin_key" to table: "shop_allowed_origins"
DROP INDEX "shop_allowed_origins_origin_key";
-- reverse: create "shop_allowed_origins" table
DROP TABLE "shop_allowed_origins";
-- reverse: create index "publishablekey_shop_id" to table: "publishable_keys"
DROP INDEX "publishablekey_shop_id";
-- reverse: create index "publishable_keys_key_key" to table: "publishable_keys"
DROP INDEX "publishable_keys_key_key";
-- reverse: create "publishable_keys" table
DROP TABLE "publishable_keys";
