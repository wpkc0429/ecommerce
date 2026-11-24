-- modify "content_schemas" table
ALTER TABLE "content_schemas" ADD COLUMN "created_by_user_id" bigint NULL, ADD COLUMN "snapshot_json" jsonb NULL;
-- modify "pages" table
ALTER TABLE "pages" ADD COLUMN "needs_attention" boolean NOT NULL DEFAULT false;

-- Permission nodes content.* (change headless-content-store, design D5, Q1).
-- The catalog is normally written by `make seed`; the rows are created here too
-- so the copy below can reference them. Idempotent.
INSERT INTO "permissions" ("name", "description") VALUES
  ('content.view', '檢視內容 schema 與內容文件'),
  ('content.edit', '編輯內容文件草稿'),
  ('content.publish', '發佈與下架內容文件'),
  ('content.manage', '註冊內容 schema（人工、整合動作）')
ON CONFLICT ("name") DO NOTHING;

-- page.* stays as an equivalent alias during the compatibility period: every
-- existing grant of a page.* node (built-in and custom roles, and per-user
-- overrides, including forced denials) is copied to the matching content.*
-- node. content.manage is a NEW capability and is never copied: it is granted
-- by `make seed` to merchant_owner and super_admin only.
WITH map(src, dst) AS (VALUES
  ('page.view', 'content.view'),
  ('page.create', 'content.edit'),
  ('page.edit', 'content.edit'),
  ('page.publish', 'content.publish'),
  ('page.delete', 'content.publish')
)
INSERT INTO "role_permission" ("role_id", "permission_id")
SELECT DISTINCT rp."role_id", dst."id"
FROM "role_permission" rp
JOIN "permissions" src ON src."id" = rp."permission_id"
JOIN map ON map.src = src."name"
JOIN "permissions" dst ON dst."name" = map.dst
ON CONFLICT DO NOTHING;

WITH map(src, dst) AS (VALUES
  ('page.view', 'content.view'),
  ('page.create', 'content.edit'),
  ('page.edit', 'content.edit'),
  ('page.publish', 'content.publish'),
  ('page.delete', 'content.publish')
)
INSERT INTO "user_permission" ("user_id", "permission_id", "shop_id", "is_granted")
SELECT DISTINCT ON (up."user_id", dst."id", up."shop_id") up."user_id", dst."id", up."shop_id", up."is_granted"
FROM "user_permission" up
JOIN "permissions" src ON src."id" = up."permission_id"
JOIN map ON map.src = src."name"
JOIN "permissions" dst ON dst."name" = map.dst
ORDER BY up."user_id", dst."id", up."shop_id", up."is_granted" ASC
ON CONFLICT DO NOTHING;
