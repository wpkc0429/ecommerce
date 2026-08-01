-- change tier-discount-application: member_tiers.discount_percent stops being an
-- inert column and starts changing what the customer pays.
--
-- Hand-fixed (per cmd/migrategen's documented caveat), twice:
--
--  1. The raw atlas diff again proposed dropping the handwritten constraints the
--     ent schema cannot express — members' UNIQUE(shop_id, email/phone)
--     (20260730152723_member_identity_per_shop) and role_user/user_permission's
--     UNIQUE NULLS NOT DISTINCT (20260718063900_rbac_unique_nulls_not_distinct).
--     Those DROP statements were removed from this file (and their re-CREATE
--     counterparts from the .down.sql), same as the two sibling changes did.
--
--  2. **The diff did NOT notice that orders_total_amount_composition_check's
--     expression changed.** Atlas compares CHECK constraints by name, so an
--     amended expression under an unchanged name is invisible to it: the diff
--     emitted the three new columns and the four new checks, and left the old
--     `total_amount = subtotal_amount + shipping_fee` in place — under which the
--     first discounted order would have been rejected at INSERT. The DROP/ADD
--     pair at the bottom of this file is written by hand. **Anyone amending a
--     CHECK expression in a future change must do the same; the generator will
--     stay silent.**
--
-- No backfill is needed (design D5): discount_amount's DEFAULT 0 makes every
-- pre-existing row satisfy the new equation as it stands, because
-- `total = subtotal - 0 + fee` is exactly the old equation. Not one stored value
-- moves — which is also what keeps every already-issued loyalty point correct
-- under the new award basis (design D6).

-- ── orders: the discount snapshot (design D3) ─────────────────────────
-- Its own statement, ahead of the constraints below: those checks reference
-- columns that have to exist first.
ALTER TABLE "orders"
    ADD COLUMN "discount_amount" bigint NOT NULL DEFAULT 0,
    ADD COLUMN "discount_percent" smallint NULL,
    ADD COLUMN "member_tier_name" character varying(100) NULL;

-- ── orders: the new invariants (design D5) ────────────────────────────
ALTER TABLE "orders"
    ADD CONSTRAINT "orders_discount_amount_check" CHECK (discount_amount >= 0),
    ADD CONSTRAINT "orders_discount_amount_bound_check" CHECK (discount_amount <= subtotal_amount),
    ADD CONSTRAINT "orders_discount_percent_check" CHECK ((discount_percent IS NULL) OR ((discount_percent >= 0) AND (discount_percent <= 100))),
    ADD CONSTRAINT "orders_tier_snapshot_check" CHECK ((member_tier_name IS NULL) = (discount_percent IS NULL));

-- ── orders: amend the composition invariant (see note 2 above) ────────
ALTER TABLE "orders" DROP CONSTRAINT "orders_total_amount_composition_check";
ALTER TABLE "orders" ADD CONSTRAINT "orders_total_amount_composition_check"
    CHECK (total_amount = ((subtotal_amount - discount_amount) + shipping_fee));
