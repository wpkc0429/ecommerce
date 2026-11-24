-- reverse: create index "shopapplicationtoken_application_id_created_at" to table: "shop_application_tokens"
DROP INDEX "shopapplicationtoken_application_id_created_at";
-- reverse: create index "shop_application_tokens_token_hash_key" to table: "shop_application_tokens"
DROP INDEX "shop_application_tokens_token_hash_key";
-- reverse: create "shop_application_tokens" table
DROP TABLE "shop_application_tokens";
-- reverse: create index "shopapplicationmessage_status_id" to table: "shop_application_messages"
DROP INDEX "shopapplicationmessage_status_id";
-- reverse: create index "shopapplicationmessage_application_id_id" to table: "shop_application_messages"
DROP INDEX "shopapplicationmessage_application_id_id";
-- reverse: create "shop_application_messages" table
DROP TABLE "shop_application_messages";
-- reverse: create index "shopapplicationevent_application_id_id" to table: "shop_application_events"
DROP INDEX "shopapplicationevent_application_id_id";
-- reverse: create "shop_application_events" table
DROP TABLE "shop_application_events";
-- reverse: create index "shopapplication_status_created_at" to table: "shop_applications"
DROP INDEX "shopapplication_status_created_at";
-- reverse: create index "shopapplication_email_status" to table: "shop_applications"
DROP INDEX "shopapplication_email_status";
-- reverse: create index "shopapplication_email_open_key" to table: "shop_applications"
DROP INDEX "shopapplication_email_open_key";
-- reverse: create index "shop_applications_provisioned_shop_id_key" to table: "shop_applications"
DROP INDEX "shop_applications_provisioned_shop_id_key";
-- reverse: create "shop_applications" table
DROP TABLE "shop_applications";
