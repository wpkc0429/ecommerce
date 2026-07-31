-- reverse: change order-extras.
--
-- Hand-fixed: the raw atlas diff also proposed re-CREATEing the handwritten
-- constraints its up counterpart proposed dropping (members' UNIQUE(shop_id,
-- email/phone), role_user/user_permission's UNIQUE NULLS NOT DISTINCT). Those
-- statements were removed — the up migration never drops them, so the down must
-- not recreate them.
--
-- Irreversible content (design Migration Plan): dropping shipping_fee /
-- shipping_method_* / requested_delivery_date discards that information. Orders
-- placed with a shipping method revert to a total_amount that no longer matches
-- what was charged. Export shipping_fee before rolling back if any such order
-- exists.

-- reverse: modify "orders" table
ALTER TABLE "orders"
    DROP CONSTRAINT "orders_total_amount_composition_check",
    DROP CONSTRAINT "orders_subtotal_amount_check",
    DROP CONSTRAINT "orders_shipping_fee_check",
    DROP COLUMN "requested_delivery_date",
    DROP COLUMN "shipping_method_name",
    DROP COLUMN "shipping_method_id",
    DROP COLUMN "shipping_fee",
    DROP COLUMN "subtotal_amount";
-- reverse: modify "order_items" table
ALTER TABLE "order_items" DROP COLUMN "gift_message";
-- reverse: modify "cart_items" table
ALTER TABLE "cart_items" DROP COLUMN "gift_message";
