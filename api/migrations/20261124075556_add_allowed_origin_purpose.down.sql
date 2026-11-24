-- reverse: modify "shops" table
ALTER TABLE "shops" DROP COLUMN "admin_branding";
-- reverse: modify "shop_allowed_origins" table
ALTER TABLE "shop_allowed_origins" DROP CONSTRAINT "shop_allowed_origins_purpose_check", DROP COLUMN "purpose";
