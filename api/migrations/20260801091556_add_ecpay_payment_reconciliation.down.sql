-- reverse: change ecpay-payment-provider.
--
-- Hand-fixed: the raw atlas diff again proposed re-CREATEing the handwritten
-- constraints its up counterpart proposed dropping (members' UNIQUE(shop_id,
-- email/phone), role_user/user_permission's UNIQUE NULLS NOT DISTINCT). Those
-- statements were removed — the up migration never drops them, so the down must
-- not recreate them.
--
-- **This rollback is silently lossy, and there is no way to make it otherwise.**
-- provider_transaction_id holds 綠界的 TradeNo — a value that exists nowhere else
-- in this database and cannot be recomputed from anything we keep; it is also a
-- required parameter of 綠界's refund API. paid_at holds the provider's own
-- statement of when the money moved, which updated_at only approximates.
--
-- Before rolling back with any ECPay payment present, export the payments table.
ALTER TABLE "payments"
    DROP COLUMN "paid_at",
    DROP COLUMN "provider_transaction_id";
