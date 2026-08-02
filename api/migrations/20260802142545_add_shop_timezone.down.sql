-- reverse: modify "shops" table
--
-- Symmetric to the hand-fixed .up.sql: atlas's raw output also re-CREATEd the
-- four handwritten constraints its diff had proposed dropping. Since the .up.sql
-- never drops them, re-creating them here would fail on the second rollback.
--
-- Rolling back after a merchant has set a time zone discards that setting. That
-- is the inherent property of dropping a new column, not a defect; the merchant
-- sets it again.
ALTER TABLE "shops" DROP COLUMN "timezone";
