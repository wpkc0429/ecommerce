-- Change merchant-staff-role (design D3/D4/D5).
-- modify "shop_user" table: membership state (0 disabled / 1 active)
ALTER TABLE "shop_user" ADD COLUMN "status" smallint NOT NULL DEFAULT 1, ADD CONSTRAINT "shop_user_status_check" CHECK (status = ANY (ARRAY[0, 1]));
-- modify "roles" table: only merchant_assignable roles may be granted by a merchant-level caller
ALTER TABLE "roles" ADD COLUMN "merchant_assignable" boolean NOT NULL DEFAULT false;
-- modify "admin_credential_tokens" table: invite rows (no user; email + shop + role)
ALTER TABLE "admin_credential_tokens" ALTER COLUMN "user_id" DROP NOT NULL, ADD COLUMN "email" character varying(150) NULL, ADD COLUMN "shop_id" bigint NULL, ADD COLUMN "role_id" bigint NULL, ADD CONSTRAINT "admin_credential_tokens_roles_role" FOREIGN KEY ("role_id") REFERENCES "roles" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION;
-- HANDWRITTEN: atlas matches CHECKs by name and does not notice a changed expression,
-- so the kind CHECK (now allowing 'invite') is dropped and re-added explicitly.
ALTER TABLE "admin_credential_tokens" DROP CONSTRAINT "admin_credential_tokens_kind_check";
ALTER TABLE "admin_credential_tokens" ADD CONSTRAINT "admin_credential_tokens_kind_check" CHECK (kind IN ('setup', 'reset', 'invite'));
-- HANDWRITTEN: composition CHECKs. An invite row has shop+role+email and no user;
-- every other kind has a user.
ALTER TABLE "admin_credential_tokens" ADD CONSTRAINT "admin_credential_tokens_invite_shape_check" CHECK ((kind = 'invite') = (shop_id IS NOT NULL AND role_id IS NOT NULL AND email IS NOT NULL));
ALTER TABLE "admin_credential_tokens" ADD CONSTRAINT "admin_credential_tokens_user_shape_check" CHECK ((kind <> 'invite') = (user_id IS NOT NULL));
-- create index "admincredentialtoken_shop_id_email" to table: "admin_credential_tokens"
CREATE INDEX "admincredentialtoken_shop_id_email" ON "admin_credential_tokens" ("shop_id", "email");
