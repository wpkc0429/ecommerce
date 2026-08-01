-- change ecpay-payment-provider: the two facts manual reconciliation needs and
-- the payments table did not have (design D4/D12).
--
-- Hand-fixed (per cmd/migrategen's documented caveat, same as the three sibling
-- changes before it): the raw atlas diff again proposed dropping the handwritten
-- constraints the ent schema cannot express — members' UNIQUE(shop_id, email)
-- and UNIQUE(shop_id, phone) (20260730152723_member_identity_per_shop) and
-- role_user/user_permission's UNIQUE NULLS NOT DISTINCT
-- (20260718063900_rbac_unique_nulls_not_distinct). Those DROP statements were
-- removed from this file, and their re-CREATE counterparts from the .down.sql.
--
-- **No CHECK expression is amended by this change**, so atlas's documented
-- blindness to that (it compares CHECK constraints by name) does not apply here.
-- Stated explicitly because the previous migration's long warning about it is
-- the first thing a reader of this directory will have just gone past.
--
-- No backfill: both columns are NULL for every pre-existing row, and NULL is the
-- honest answer for them — the mock provider has no second identifier and
-- reports no payment time, so there is nothing that could be filled in.

-- ── payments: the provider's own transaction id, and the provider's own
--    statement of when the money moved (design D4/D12) ──────────────────
ALTER TABLE "payments"
    ADD COLUMN "provider_transaction_id" character varying(64) NULL,
    ADD COLUMN "paid_at" timestamptz NULL;
