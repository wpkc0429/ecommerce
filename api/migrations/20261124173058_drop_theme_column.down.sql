-- Rollback guard (change retire-hosted-storefront-and-themes, design D5).
-- Migration 1 drops shops.theme_id. Which theme a shop used is not recoverable
-- from anything else (its schemas live on in content_schemas, but not the link),
-- so this rollback is refused. Rolling back means restoring the backup taken
-- before the upgrade (pg_dump of shops, themes, theme_pages, sites, site_shop,
-- content_schemas), not running this file.
DO $$
BEGIN
  RAISE EXCEPTION 'refusing to roll back: shops.theme_id was dropped; restore from the pre-upgrade backup (see comment at the top of this file)';
END
$$;
