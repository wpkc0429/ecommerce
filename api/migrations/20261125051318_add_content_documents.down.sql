-- reverse: create index "contentrevision_shop_id_document_id_kind" to table: "content_revisions"
DROP INDEX "contentrevision_shop_id_document_id_kind";
-- reverse: create index "contentrevision_document_id_rev_no" to table: "content_revisions"
DROP INDEX "contentrevision_document_id_rev_no";
-- reverse: create "content_revisions" table
DROP TABLE "content_revisions";
-- reverse: create index "contentdocument_shop_id_status" to table: "content_documents"
DROP INDEX "contentdocument_shop_id_status";
-- reverse: create index "contentdocument_shop_id_key" to table: "content_documents"
DROP INDEX "contentdocument_shop_id_key";
-- reverse: create "content_documents" table
DROP TABLE "content_documents";
-- reverse: create index "contentauditlog_shop_id_created_at" to table: "content_audit_log"
DROP INDEX "contentauditlog_shop_id_created_at";
-- reverse: create index "contentauditlog_created_at" to table: "content_audit_log"
DROP INDEX "contentauditlog_created_at";
-- reverse: create "content_audit_log" table
DROP TABLE "content_audit_log";
