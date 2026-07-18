-- reverse: create index "pointtransaction_shop_id_shop_member_id" to table: "point_transactions"
DROP INDEX "pointtransaction_shop_id_shop_member_id";
-- reverse: create index "pointtransaction_order_id_kind" to table: "point_transactions"
DROP INDEX "pointtransaction_order_id_kind";
-- reverse: create "point_transactions" table
DROP TABLE "point_transactions";
-- reverse: modify "shop_member" table
ALTER TABLE "shop_member" DROP CONSTRAINT "shop_member_member_tiers_member_tier", ALTER COLUMN "level_id" TYPE integer;
-- reverse: create index "membertier_shop_id_min_points" to table: "member_tiers"
DROP INDEX "membertier_shop_id_min_points";
-- reverse: create "member_tiers" table
DROP TABLE "member_tiers";
