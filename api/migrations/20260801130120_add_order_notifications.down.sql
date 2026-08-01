-- reverse: change order-notification-email.
--
-- Hand-fixed: the raw atlas diff again proposed re-CREATEing the handwritten
-- constraints its up counterpart proposed dropping (members' UNIQUE(shop_id,
-- email/phone), role_user/user_permission's UNIQUE NULLS NOT DISTINCT). Those
-- statements were removed — the up migration never drops them, so the down must
-- not recreate them.
--
-- **This rollback loses the send ledger, and that has one consequence worth
-- naming**: order_notifications is the only record of which customers were
-- emailed about which orders and at which address. Dropping it and re-applying
-- the migration would make every past order look un-notified — so if any
-- subscriber were ever re-run against historical events afterwards (there is no
-- such path today, but a retry worker is the obvious future one), customers
-- would receive duplicates of emails they already have.
--
-- Rolling back is nevertheless safe for the *live* system: notifications can
-- simply be switched off by clearing SMTP_USERNAME/SMTP_PASSWORD, which needs no
-- migration at all and is the correct lever for "stop sending".
DROP INDEX "ordernotification_shop_id_order_id";
DROP INDEX "ordernotification_order_id_kind";
DROP TABLE "order_notifications";
