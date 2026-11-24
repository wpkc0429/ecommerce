-- change order-and-invitation-polish
--
-- order_items.options: the SKU's option values (e.g. {"size":"M"}) snapshotted
-- when the order is created, so clients stop parsing sku_code suffixes.
-- NULL only for rows that predate the column.
ALTER TABLE "order_items" ADD COLUMN "options" jsonb NULL;

-- Best-available backfill for existing orders: the SKU's CURRENT options, where
-- the SKU row still exists. For a SKU edited since the order was placed this is
-- not the value at order time, which is no longer recoverable; rows whose SKU is
-- gone stay NULL. Idempotent (only touches NULL).
UPDATE "order_items" AS oi
SET "options" = ps."options"
FROM "product_skus" AS ps
WHERE oi."sku_id" = ps."id"
  AND oi."shop_id" = ps."shop_id"
  AND oi."options" IS NULL;

-- payments.paid_at was left NULL for succeeded payments by providers that
-- report no timestamp (mock; ECPay without PaymentDate). The payment row's
-- updated_at is when the success was recorded, the closest available instant.
-- Idempotent (only touches NULL). payments.status 1 = succeeded.
UPDATE "payments"
SET "paid_at" = "updated_at"
WHERE "status" = 1 AND "paid_at" IS NULL;
