-- Rollback guard (change platform-account-provisioning, design Migration Plan).
-- Accounts created through the provisioning API have password_hash NULL (the
-- owner has not set a password yet). Restoring NOT NULL would either fail
-- halfway or require deleting those accounts, which is destructive, so this
-- rollback refuses while any such account exists.
-- Manual steps if you really must roll back: resolve every account listed by
--   SELECT id, email FROM users WHERE password_hash IS NULL;
-- (delete the account, or set a hash through the password-setup link), then
-- run this migration down again.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM users WHERE password_hash IS NULL) THEN
    RAISE EXCEPTION 'refusing to roll back: users with NULL password_hash exist (see comment at the top of this file)';
  END IF;
END
$$;
-- reverse: create index "admincredentialtoken_user_id_kind" to table: "admin_credential_tokens"
DROP INDEX "admincredentialtoken_user_id_kind";
-- reverse: create index "admin_credential_tokens_token_hash_key" to table: "admin_credential_tokens"
DROP INDEX "admin_credential_tokens_token_hash_key";
-- reverse: create "admin_credential_tokens" table
DROP TABLE "admin_credential_tokens";
-- reverse: modify "users" table
ALTER TABLE "users" ALTER COLUMN "password_hash" SET NOT NULL;
