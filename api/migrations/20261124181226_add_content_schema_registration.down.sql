-- Remove the content.* permission nodes and every grant of them.
DELETE FROM "user_permission" WHERE "permission_id" IN (SELECT "id" FROM "permissions" WHERE "name" LIKE 'content.%');
DELETE FROM "role_permission" WHERE "permission_id" IN (SELECT "id" FROM "permissions" WHERE "name" LIKE 'content.%');
DELETE FROM "permissions" WHERE "name" LIKE 'content.%';
-- reverse: modify "pages" table
ALTER TABLE "pages" DROP COLUMN "needs_attention";
-- reverse: modify "content_schemas" table
ALTER TABLE "content_schemas" DROP COLUMN "snapshot_json", DROP COLUMN "created_by_user_id";
