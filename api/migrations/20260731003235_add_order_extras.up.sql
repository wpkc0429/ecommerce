-- change order-extras: per-item gift message, per-order requested delivery date,
-- and the shipping fee folded into the order's payable total.
--
-- Hand-fixed (per cmd/migrategen's documented caveat), twice:
--
--  1. The raw atlas diff also proposed dropping the handwritten constraints the
--     ent schema cannot express — members' UNIQUE(shop_id, email/phone)
--     (20260730152723_member_identity_per_shop) and role_user/user_permission's
--     UNIQUE NULLS NOT DISTINCT (20260718063900_rbac_unique_nulls_not_distinct).
--     Those DROP statements were removed from this file (and their re-CREATE
--     counterparts from the .down.sql), same as the sibling change did.
--
--  2. The diff emitted the ADD COLUMNs and the CHECK constraints as one
--     statement. subtotal_amount has no default in the ent schema, so on a
--     non-empty orders table that fails outright; and even with a default of 0,
--     orders_total_amount_composition_check would reject every existing row with
--     a non-zero total. The diff engine emits schema shape only, never the
--     backfill that has to happen between the shape changes (design D10), so the
--     statements below are split and ordered by hand.

-- modify "cart_items" table
ALTER TABLE "cart_items" ADD COLUMN "gift_message" character varying(500) NOT NULL DEFAULT '';
-- modify "order_items" table
ALTER TABLE "order_items" ADD COLUMN "gift_message" character varying(500) NOT NULL DEFAULT '';

-- ── orders: add the amount/shipping/date columns ──────────────────────
-- subtotal_amount carries a temporary DEFAULT 0 purely so the ADD COLUMN can
-- succeed on existing rows; it is dropped below so the column ends up matching
-- the ent schema exactly (otherwise the next diff would propose removing it).
ALTER TABLE "orders"
    ADD COLUMN "subtotal_amount" bigint NOT NULL DEFAULT 0,
    ADD COLUMN "shipping_fee" bigint NOT NULL DEFAULT 0,
    ADD COLUMN "shipping_method_id" bigint NULL,
    ADD COLUMN "shipping_method_name" character varying(100) NULL,
    ADD COLUMN "requested_delivery_date" date NULL;

-- ── backfill (design D10) ─────────────────────────────────────────────
-- Every pre-existing order was placed without a shipping method, so its
-- total_amount already IS the item subtotal. This makes each existing row
-- correct under the new definition of total_amount without changing a single
-- stored value — which is also what keeps every already-issued loyalty point
-- correct under the new subtotal-based award basis (design D5).
UPDATE "orders" SET "subtotal_amount" = "total_amount";

ALTER TABLE "orders" ALTER COLUMN "subtotal_amount" DROP DEFAULT;

-- ── orders: the amount invariants, now that every row satisfies them ──
ALTER TABLE "orders"
    ADD CONSTRAINT "orders_subtotal_amount_check" CHECK (subtotal_amount >= 0),
    ADD CONSTRAINT "orders_shipping_fee_check" CHECK (shipping_fee >= 0),
    ADD CONSTRAINT "orders_total_amount_composition_check" CHECK (total_amount = (subtotal_amount + shipping_fee));
