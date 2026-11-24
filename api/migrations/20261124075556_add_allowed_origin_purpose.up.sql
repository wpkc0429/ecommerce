-- Change admin-api-public-exposure (design D3/D8).
-- modify "shop_allowed_origins" table: what the origin may call (storefront | admin)
ALTER TABLE "shop_allowed_origins" ADD COLUMN "purpose" character varying(20) NOT NULL DEFAULT 'storefront', ADD CONSTRAINT "shop_allowed_origins_purpose_check" CHECK (purpose IN ('storefront', 'admin'));
-- modify "shops" table: per-shop admin branding (logo_url / primary_color / accent_color)
ALTER TABLE "shops" ADD COLUMN "admin_branding" jsonb NOT NULL DEFAULT '{}';
