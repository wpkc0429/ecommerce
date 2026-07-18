-- reverse: create index "userrefreshtoken_user_id" to table: "user_refresh_tokens"
DROP INDEX "userrefreshtoken_user_id";
-- reverse: create index "user_refresh_tokens_token_hash_key" to table: "user_refresh_tokens"
DROP INDEX "user_refresh_tokens_token_hash_key";
-- reverse: create "user_refresh_tokens" table
DROP TABLE "user_refresh_tokens";
-- reverse: create index "userpermission_user_id_shop_id" to table: "user_permission"
DROP INDEX "userpermission_user_id_shop_id";
-- reverse: create "user_permission" table
DROP TABLE "user_permission";
-- reverse: create index "themepage_theme_id_type_key" to table: "theme_pages"
DROP INDEX "themepage_theme_id_type_key";
-- reverse: create "theme_pages" table
DROP TABLE "theme_pages";
-- reverse: create index "siteshop_site_id_path_prefix" to table: "site_shop"
DROP INDEX "siteshop_site_id_path_prefix";
-- reverse: create index "site_shop_primary_domain_per_shop" to table: "site_shop"
DROP INDEX "site_shop_primary_domain_per_shop";
-- reverse: create index "site_shop_default_shop_per_site" to table: "site_shop"
DROP INDEX "site_shop_default_shop_per_site";
-- reverse: create "site_shop" table
DROP TABLE "site_shop";
-- reverse: create index "sites_domain_key" to table: "sites"
DROP INDEX "sites_domain_key";
-- reverse: create "sites" table
DROP TABLE "sites";
-- reverse: create index "shopuser_user_id" to table: "shop_user"
DROP INDEX "shopuser_user_id";
-- reverse: create index "shopuser_shop_id_user_id" to table: "shop_user"
DROP INDEX "shopuser_shop_id_user_id";
-- reverse: create "shop_user" table
DROP TABLE "shop_user";
-- reverse: create index "shopmember_shop_id_member_id" to table: "shop_member"
DROP INDEX "shopmember_shop_id_member_id";
-- reverse: create index "shopmember_member_id" to table: "shop_member"
DROP INDEX "shopmember_member_id";
-- reverse: create "shop_member" table
DROP TABLE "shop_member";
-- reverse: create index "roleuser_user_id_shop_id" to table: "role_user"
DROP INDEX "roleuser_user_id_shop_id";
-- reverse: create index "roleuser_role_id" to table: "role_user"
DROP INDEX "roleuser_role_id";
-- reverse: create "role_user" table
DROP TABLE "role_user";
-- reverse: create index "users_email_key" to table: "users"
DROP INDEX "users_email_key";
-- reverse: create "users" table
DROP TABLE "users";
-- reverse: create "role_permission" table
DROP TABLE "role_permission";
-- reverse: create index "role_name_scope" to table: "roles"
DROP INDEX "role_name_scope";
-- reverse: create "roles" table
DROP TABLE "roles";
-- reverse: create index "permissions_name_key" to table: "permissions"
DROP INDEX "permissions_name_key";
-- reverse: create "permissions" table
DROP TABLE "permissions";
-- reverse: create index "page_shop_id_status" to table: "pages"
DROP INDEX "page_shop_id_status";
-- reverse: create index "page_shop_id_slug" to table: "pages"
DROP INDEX "page_shop_id_slug";
-- reverse: create "pages" table
DROP TABLE "pages";
-- reverse: create index "memberrefreshtoken_shop_id" to table: "member_refresh_tokens"
DROP INDEX "memberrefreshtoken_shop_id";
-- reverse: create index "memberrefreshtoken_member_id" to table: "member_refresh_tokens"
DROP INDEX "memberrefreshtoken_member_id";
-- reverse: create index "member_refresh_tokens_token_hash_key" to table: "member_refresh_tokens"
DROP INDEX "member_refresh_tokens_token_hash_key";
-- reverse: create "member_refresh_tokens" table
DROP TABLE "member_refresh_tokens";
-- reverse: create index "shop_theme_id" to table: "shops"
DROP INDEX "shop_theme_id";
-- reverse: create "shops" table
DROP TABLE "shops";
-- reverse: create index "themes_code_key" to table: "themes"
DROP INDEX "themes_code_key";
-- reverse: create "themes" table
DROP TABLE "themes";
-- reverse: create index "members_phone_key" to table: "members"
DROP INDEX "members_phone_key";
-- reverse: create index "members_email_key" to table: "members"
DROP INDEX "members_email_key";
-- reverse: create "members" table
DROP TABLE "members";
