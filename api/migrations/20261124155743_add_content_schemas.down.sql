-- reverse: create index "contentschema_shop_id_version" to table: "content_schemas"
DROP INDEX "contentschema_shop_id_version";
-- reverse: create "content_schemas" table
-- (The table only holds copies of theme schemas and registered schemas; themes
-- and theme_pages are untouched by this migration, so dropping it is safe.)
DROP TABLE "content_schemas";
