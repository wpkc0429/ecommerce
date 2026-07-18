-- Handwritten (task 2.2): constraints ent cannot express.
-- UNIQUE NULLS NOT DISTINCT (PostgreSQL 15+) prevents duplicate rows when
-- shop_id IS NULL (platform-level assignments/overrides) — design D2.
ALTER TABLE "role_user"
    ADD CONSTRAINT "uq_role_user_user_role_shop"
    UNIQUE NULLS NOT DISTINCT ("user_id", "role_id", "shop_id");
ALTER TABLE "user_permission"
    ADD CONSTRAINT "uq_user_permission_user_perm_shop"
    UNIQUE NULLS NOT DISTINCT ("user_id", "permission_id", "shop_id");
