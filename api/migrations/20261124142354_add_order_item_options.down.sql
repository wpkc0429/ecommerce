-- reverse: modify "order_items" table
-- (The paid_at backfill is data, not schema, and is deliberately not undone: a
-- recorded payment time is correct whichever schema version reads it.)
ALTER TABLE "order_items" DROP COLUMN "options";
