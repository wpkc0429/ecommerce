-- Invitations are not representable in the old shape: drop the unaccepted ones first.
DELETE FROM "admin_credential_tokens" WHERE kind = 'invite';
DROP INDEX "admincredentialtoken_shop_id_email";
ALTER TABLE "admin_credential_tokens" DROP CONSTRAINT "admin_credential_tokens_user_shape_check";
ALTER TABLE "admin_credential_tokens" DROP CONSTRAINT "admin_credential_tokens_invite_shape_check";
ALTER TABLE "admin_credential_tokens" DROP CONSTRAINT "admin_credential_tokens_kind_check";
ALTER TABLE "admin_credential_tokens" ADD CONSTRAINT "admin_credential_tokens_kind_check" CHECK (kind IN ('setup', 'reset'));
ALTER TABLE "admin_credential_tokens" DROP CONSTRAINT "admin_credential_tokens_roles_role", DROP COLUMN "role_id", DROP COLUMN "shop_id", DROP COLUMN "email", ALTER COLUMN "user_id" SET NOT NULL;
ALTER TABLE "roles" DROP COLUMN "merchant_assignable";
ALTER TABLE "shop_user" DROP CONSTRAINT "shop_user_status_check", DROP COLUMN "status";
