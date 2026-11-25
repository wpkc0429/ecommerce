-- hand-written: the self-referencing same-shop foreign keys depend on design_requests_shop_id_id_key, so they go first
ALTER TABLE "design_requests" DROP CONSTRAINT "design_requests_restores_fk", DROP CONSTRAINT "design_requests_reverted_by_fk";
-- reverse: create index "design_untracked_changes_commit_key" to table: "design_untracked_changes"
DROP INDEX "design_untracked_changes_commit_key";
-- reverse: create "design_untracked_changes" table
DROP TABLE "design_untracked_changes";
-- reverse: create index "design_site_state_shop_site_key" to table: "design_site_state"
DROP INDEX "design_site_state_shop_site_key";
-- reverse: create "design_site_state" table
DROP TABLE "design_site_state";
-- reverse: create index "design_service_credentials_shop" to table: "design_service_credentials"
DROP INDEX "design_service_credentials_shop";
-- reverse: create index "design_service_credentials_key_hash_key" to table: "design_service_credentials"
DROP INDEX "design_service_credentials_key_hash_key";
-- reverse: create "design_service_credentials" table
DROP TABLE "design_service_credentials";
-- reverse: create index "design_screenshots_slot_attempt_key" to table: "design_screenshots"
DROP INDEX "design_screenshots_slot_attempt_key";
-- reverse: create index "design_screenshots_shop_created" to table: "design_screenshots"
DROP INDEX "design_screenshots_shop_created";
-- reverse: create "design_screenshots" table
DROP TABLE "design_screenshots";
-- reverse: create index "design_request_events_shop_created" to table: "design_request_events"
DROP INDEX "design_request_events_shop_created";
-- reverse: create index "design_request_events_request_seq_key" to table: "design_request_events"
DROP INDEX "design_request_events_request_seq_key";
-- reverse: create "design_request_events" table
DROP TABLE "design_request_events";
-- reverse: create index "design_requests_status_updated" to table: "design_requests"
DROP INDEX "design_requests_status_updated";
-- reverse: create index "design_requests_shop_site_created" to table: "design_requests"
DROP INDEX "design_requests_shop_site_created";
-- reverse: create index "design_requests_shop_id_id_key" to table: "design_requests"
DROP INDEX "design_requests_shop_id_id_key";
-- reverse: create index "design_requests_publish_tag_key" to table: "design_requests"
DROP INDEX "design_requests_publish_tag_key";
-- reverse: create index "design_requests_one_active" to table: "design_requests"
DROP INDEX "design_requests_one_active";
-- reverse: create index "design_requests_idem_key" to table: "design_requests"
DROP INDEX "design_requests_idem_key";
-- reverse: create "design_requests" table
DROP TABLE "design_requests";
