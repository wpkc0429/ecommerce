-- Reverse of 20260801140031_add_partial_shipment.
--
-- Hand-fixed like its .up.sql counterpart: the raw atlas diff's re-CREATE
-- statements for members' UNIQUE(shop_id, email/phone) and role_user/
-- user_permission's UNIQUE NULLS NOT DISTINCT were removed, because the
-- corresponding DROPs were removed from the .up.sql — those constraints are
-- handwritten and this change never touches them.
--
-- ── This down migration can legitimately fail, and that is honest ─────
--
-- Re-creating the UNIQUE index on shipments.order_id fails if any order has
-- actually acquired more than one shipment. That is the correct behavior:
-- rolling back a capability that has already been used would have to decide
-- which parcel to forget. The real rollback window is "deployed, but no merchant
-- has shipped partially yet".
--
-- There is no behavioral kill switch to reach for instead, because this change
-- has no behavioral branch: omitting the request's item list still ships
-- everything outstanding, byte for byte as before (design D5). "Do not use the
-- new feature" is itself a complete retreat.

-- shipment_items goes first: order_items.shipped_quantity is dropped below, and
-- the rows here are what justified its values.
DROP INDEX "shipmentitem_shop_id_shipment_id";
DROP INDEX "shipmentitem_shipment_id_order_item_id";
DROP TABLE "shipment_items";

-- Back to "at most one shipment per order" (see the caveat above).
DROP INDEX "shipment_order_id";
CREATE UNIQUE INDEX "shipment_order_id" ON "shipments" ("order_id");

ALTER TABLE "order_items"
    DROP CONSTRAINT "order_items_shipped_quantity_check",
    DROP COLUMN "shipped_quantity";
