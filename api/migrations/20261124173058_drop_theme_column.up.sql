-- change retire-hosted-storefront-and-themes (stage 5, migration 1): drop shops.theme_id.
--
-- DESTRUCTIVE. The column pointed a shop at its theme; since stage 1 nothing reads
-- it (page and global content schemas come from content_schemas). Dropping the
-- column also drops its foreign key and the index shop_theme_id.
-- BEFORE applying on a real database take a backup (see the change's design D5).
-- modify "shops" table
ALTER TABLE "shops" DROP COLUMN "theme_id";
