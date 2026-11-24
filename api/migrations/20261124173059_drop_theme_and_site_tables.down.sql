-- Rollback guard (change retire-hosted-storefront-and-themes, design D5).
-- Migration 2 drops themes, theme_pages, sites, site_shop and deletes the
-- theme.*/shop.manage_domains permission nodes. Recreating empty tables would
-- pretend the data survived, so this rollback is refused. Rolling back means
-- restoring the backup taken before the upgrade (pg_dump of themes, theme_pages,
-- sites, site_shop and shops.theme_id, plus content_schemas), then re-running
-- the previous release's seed to recreate the permission nodes.
DO $$
BEGIN
  RAISE EXCEPTION 'refusing to roll back: theme and site tables were dropped; restore from the pre-upgrade backup (see comment at the top of this file)';
END
$$;
