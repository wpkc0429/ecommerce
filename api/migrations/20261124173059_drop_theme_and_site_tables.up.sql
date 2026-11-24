-- change retire-hosted-storefront-and-themes (stage 5, migration 2): drop the theme
-- and site tables and the permission nodes that governed them.
--
-- DESTRUCTIVE. Data that disappears: every theme (themes, theme_pages: the starter
-- theme and its page types), every site and its shop mapping (sites, site_shop:
-- the demo.localhost binding of the demo shop and any domain bound through the
-- sites API or the provisioning wizard), and the permission nodes theme.view,
-- theme.manage and shop.manage_domains together with every role/user grant of
-- them. Page and global content schemas are NOT lost: they were copied into
-- content_schemas by the add_content_schemas migration (and are written there by
-- seed/registration since). Pages and shops.content_json are untouched.
-- BEFORE applying on a real database take a backup (see the change's design D5).
DELETE FROM "role_permission" WHERE "permission_id" IN (SELECT "id" FROM "permissions" WHERE "name" IN ('theme.view', 'theme.manage', 'shop.manage_domains'));
DELETE FROM "user_permission" WHERE "permission_id" IN (SELECT "id" FROM "permissions" WHERE "name" IN ('theme.view', 'theme.manage', 'shop.manage_domains'));
DELETE FROM "permissions" WHERE "name" IN ('theme.view', 'theme.manage', 'shop.manage_domains');
-- drop "site_shop" table (its handwritten NULLS NOT DISTINCT constraints go with it)
DROP TABLE "site_shop";
-- drop "sites" table
DROP TABLE "sites";
-- drop "theme_pages" table
DROP TABLE "theme_pages";
-- drop "themes" table
DROP TABLE "themes";
