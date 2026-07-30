-- change member-identity-per-shop: storefront customer identity becomes
-- per-shop. members gains shop_id, absorbs shop_member's points/level_id, and
-- its platform-wide UNIQUE(email)/UNIQUE(phone) become UNIQUE(shop_id, email)/
-- UNIQUE(shop_id, phone). point_transactions.shop_member_id becomes member_id.
-- shop_member is dropped. Steps follow design.md's Migration Plan 1–7.
--
-- Hand-fixed (per cmd/migrategen's documented caveat): the raw atlas diff also
-- proposed dropping role_user/user_permission's handwritten UNIQUE NULLS NOT
-- DISTINCT constraints (20260718063900_rbac_unique_nulls_not_distinct) — the
-- diff engine doesn't know ent's schema can't express them. Those DROP/re-ADD
-- statements were removed from this file (and its .down.sql counterpart).
-- Likewise DROP TABLE "shop_member", the per-shop unique indexes and every
-- data-movement statement below are handwritten: the diff engine emits schema
-- shape only, never the backfill that has to happen between the shape changes.

-- ── Guard: the 1:1 expansion in step 2 assumes no cross-shop identity ──
-- design Migration Plan step 2 / Open Questions: current data has none. If a
-- member ever held memberships at two shops, splitting it into two rows would
-- also require repointing orders/carts/tokens — out of scope, so abort loudly
-- rather than silently picking one shop.
DO $$
DECLARE
    n bigint;
BEGIN
    SELECT count(*) INTO n FROM (
        SELECT member_id FROM "shop_member" GROUP BY member_id HAVING count(*) > 1
    ) x;
    IF n > 0 THEN
        RAISE EXCEPTION 'member_identity_per_shop: % member(s) belong to more than one shop; the per-shop split is out of this migration''s scope', n;
    END IF;
END $$;

-- ── Step 1: members.shop_id, temporarily nullable ─────────────────────
ALTER TABLE "members" ADD COLUMN "shop_id" bigint NULL;

-- ── Step 2: backfill shop_id from shop_member, drop orphan identities ──
UPDATE "members" m
SET "shop_id" = sm."shop_id"
FROM "shop_member" sm
WHERE sm."member_id" = m."id";

-- Identities with no membership anywhere cannot be assigned to a shop. Their
-- refresh tokens go first (throwaway credentials); if such a row is still
-- referenced by orders/carts the FK aborts the whole migration, which is the
-- intended loud failure — it would mean data the design did not anticipate.
DELETE FROM "member_refresh_tokens" WHERE "member_id" IN (SELECT "id" FROM "members" WHERE "shop_id" IS NULL);
DELETE FROM "members" WHERE "shop_id" IS NULL;

-- ── Step 3: absorb shop_member's loyalty columns and backfill ─────────
ALTER TABLE "members"
    ADD COLUMN "points" integer NOT NULL DEFAULT 0,
    ADD COLUMN "level_id" bigint NULL;

UPDATE "members" m
SET "points" = sm."points", "level_id" = sm."level_id"
FROM "shop_member" sm
WHERE sm."member_id" = m."id";

-- ── Step 4: shop_id becomes mandatory, with its FKs and lookup index ──
ALTER TABLE "members" ALTER COLUMN "shop_id" SET NOT NULL;
ALTER TABLE "members"
    ADD CONSTRAINT "members_shops_shop" FOREIGN KEY ("shop_id") REFERENCES "shops" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION,
    ADD CONSTRAINT "members_member_tiers_member_tier" FOREIGN KEY ("level_id") REFERENCES "member_tiers" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
CREATE INDEX "member_shop_id" ON "members" ("shop_id");

-- ── Step 5: platform-wide uniqueness → per-shop uniqueness ────────────
-- design D3: NULLS DISTINCT is Postgres's default and is spelled out here on
-- purpose. It MUST NOT become NULLS NOT DISTINCT (as role_user/user_permission
-- deliberately are) — one shop must be able to hold many email-NULL or
-- phone-NULL customers (phone-only registration, future guest identities).
DROP INDEX "members_email_key";
DROP INDEX "members_phone_key";
CREATE UNIQUE INDEX "uq_members_shop_email" ON "members" ("shop_id", "email") NULLS DISTINCT;
CREATE UNIQUE INDEX "uq_members_shop_phone" ON "members" ("shop_id", "phone") NULLS DISTINCT;

-- ── Step 6: point_transactions.shop_member_id → member_id ─────────────
ALTER TABLE "point_transactions" DROP CONSTRAINT "point_transactions_shop_member_shop_member";
DROP INDEX "pointtransaction_shop_id_shop_member_id";
ALTER TABLE "point_transactions" RENAME COLUMN "shop_member_id" TO "member_id";
-- The renamed column still holds shop_member ids; translate them into the
-- members ids they stood for, before the new FK starts enforcing.
UPDATE "point_transactions" pt
SET "member_id" = sm."member_id"
FROM "shop_member" sm
WHERE sm."id" = pt."member_id";
ALTER TABLE "point_transactions"
    ADD CONSTRAINT "point_transactions_members_member" FOREIGN KEY ("member_id") REFERENCES "members" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION;
CREATE INDEX "pointtransaction_shop_id_member_id" ON "point_transactions" ("shop_id", "member_id");

-- ── Step 7: shop_member is now strictly redundant ─────────────────────
DROP TABLE "shop_member";

-- ── Post-migration reconciliation (design Migration Plan) ─────────────
-- Every member's cached balance must equal the sum of its ledger rows (which
-- also settles the per-shop SUM comparison the design asks for, since both
-- sides are now shop-scoped by construction). Any mismatch means the
-- points/level_id backfill lost data — abort.
DO $$
DECLARE
    bad bigint;
BEGIN
    SELECT count(*) INTO bad
    FROM "members" m
    LEFT JOIN (
        SELECT "member_id", sum("points_delta")::integer AS total
        FROM "point_transactions" GROUP BY "member_id"
    ) pt ON pt."member_id" = m."id"
    WHERE m."points" <> COALESCE(pt.total, 0);
    IF bad > 0 THEN
        RAISE EXCEPTION 'member_identity_per_shop: % member(s) whose points cache disagrees with the point_transactions ledger', bad;
    END IF;
END $$;
