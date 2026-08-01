-- reverse: change tier-discount-application.
--
-- Hand-fixed: the raw atlas diff again proposed re-CREATEing the handwritten
-- constraints its up counterpart proposed dropping (members' UNIQUE(shop_id,
-- email/phone), role_user/user_permission's UNIQUE NULLS NOT DISTINCT). Those
-- statements were removed — the up migration never drops them, so the down must
-- not recreate them. It also omitted the composition CHECK entirely (atlas
-- compares checks by name; see the up file's note 2), so the DROP/ADD pair below
-- is written by hand.
--
-- **This rollback fails loudly, on purpose, if any discounted order exists.**
-- Restoring `total_amount = subtotal_amount + shipping_fee` is the FIRST
-- statement here precisely so that a table containing a row with
-- discount_amount > 0 aborts the migration while discount_amount is still there
-- to be inspected. Letting it through would leave a batch of orders whose
-- total_amount does not match its own composition, and no way to tell which.
--
-- Before rolling back with discounted orders present, decide what happens to
-- them: either rewrite subtotal_amount to the discounted value (preserving what
-- the customer actually paid, losing the list price) or export and delete them.

-- reverse: amend the composition invariant
ALTER TABLE "orders" DROP CONSTRAINT "orders_total_amount_composition_check";
ALTER TABLE "orders" ADD CONSTRAINT "orders_total_amount_composition_check"
    CHECK (total_amount = (subtotal_amount + shipping_fee));

-- reverse: modify "orders" table
ALTER TABLE "orders"
    DROP CONSTRAINT "orders_tier_snapshot_check",
    DROP CONSTRAINT "orders_discount_percent_check",
    DROP CONSTRAINT "orders_discount_amount_bound_check",
    DROP CONSTRAINT "orders_discount_amount_check",
    DROP COLUMN "member_tier_name",
    DROP COLUMN "discount_percent",
    DROP COLUMN "discount_amount";
