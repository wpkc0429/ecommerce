package schema

import (
	"encoding/json"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ContentDocument is one document of a shop's headless content store (change
// headless-content-store slice 2, design D1). It is keyed by a stable per-shop
// key (never a route), carries a draft and a published copy, and an optimistic
// lock (draft_rev). status: 0 draft / 1 published.
//
// The compatibility tables (pages, shops.content_json) are kept equal to it in
// the same transaction by internal/contentstore, so the old endpoints keep
// their behavior byte for byte.
type ContentDocument struct {
	ent.Schema
}

func (ContentDocument) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.String("key").MaxLen(63).SchemaType(map[string]string{dialect.Postgres: "varchar(63)"}).NotEmpty(),
		field.String("type_key").MaxLen(50).SchemaType(map[string]string{dialect.Postgres: "varchar(50)"}).NotEmpty(),
		field.String("title").MaxLen(150).SchemaType(map[string]string{dialect.Postgres: "varchar(150)"}).Default(""),
		field.Int16("status").Default(0),
		field.JSON("draft_json", json.RawMessage{}).Default(json.RawMessage("{}")),
		field.JSON("published_json", json.RawMessage{}).Optional(),
		field.JSON("meta", json.RawMessage{}).Default(json.RawMessage("{}")),
		// draft_rev: +1 on every draft save; the optimistic lock and the ETag
		// basis of the admin API. published_rev is the draft_rev at publish.
		field.Int64("draft_rev").Default(1),
		field.Int64("published_rev").Default(0),
		field.Int("schema_version").Default(0),
		field.Int("published_schema_version").Default(0),
		field.Bool("needs_attention").Default(false),
		field.Bool("declared").Default(true),
		field.Time("created_at").Immutable().Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
		field.Time("published_at").Optional().Nillable(),
		// No FK on purpose: the audit trail must outlive the account.
		field.Int("updated_by_user_id").Optional().Nillable(),
	}
}

func (ContentDocument) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id", "key").Unique(),
		index.Fields("shop_id", "status"),
	}
}

func (ContentDocument) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "content_documents",
			Checks: map[string]string{
				"content_documents_status_check": "status IN (0, 1)",
				"content_documents_key_check":    "key ~ '^[a-z0-9][a-z0-9-]{0,62}$'",
			},
		},
	}
}

// ContentRevision is the bounded history of a document (design D1): one row per
// publish (the 20 most recent are kept) and one per schema change that put the
// document at risk (kind schema_snapshot, the 5 most recent schema versions are
// kept). Restoring one only ever writes the draft.
type ContentRevision struct {
	ent.Schema
}

func (ContentRevision) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		// document_id: no FK; the document's delete removes its revisions in the
		// same transaction (internal/contentstore).
		field.Int("document_id"),
		field.Int("rev_no"),
		field.String("kind").MaxLen(20).SchemaType(map[string]string{dialect.Postgres: "varchar(20)"}),
		field.JSON("json", json.RawMessage{}),
		// published_json: only on schema_snapshot rows whose published copy
		// differed from the draft.
		field.JSON("published_json", json.RawMessage{}).Optional(),
		field.String("title").MaxLen(150).SchemaType(map[string]string{dialect.Postgres: "varchar(150)"}).Default(""),
		field.JSON("meta", json.RawMessage{}).Default(json.RawMessage("{}")),
		field.Int("schema_version").Default(0),
		field.Int("actor_user_id").Optional().Nillable(),
		field.Time("created_at").Immutable().Default(time.Now),
	}
}

func (ContentRevision) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("document_id", "rev_no").Unique(),
		index.Fields("shop_id", "document_id", "kind"),
	}
}

func (ContentRevision) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "content_revisions",
			Checks: map[string]string{
				"content_revisions_rev_no_check": "rev_no >= 1",
				"content_revisions_kind_check":   "kind IN ('publish', 'schema_snapshot')",
			},
		},
	}
}

// ContentAuditLog is the small dedicated audit trail of the content store
// (design D1). detail holds ids and counts only: never content, never a URL.
type ContentAuditLog struct {
	ent.Schema
}

func (ContentAuditLog) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.Int("actor_user_id").Optional().Nillable(),
		field.String("action").MaxLen(30).SchemaType(map[string]string{dialect.Postgres: "varchar(30)"}).NotEmpty(),
		field.String("doc_key").MaxLen(63).SchemaType(map[string]string{dialect.Postgres: "varchar(63)"}).Default(""),
		field.JSON("detail", json.RawMessage{}).Default(json.RawMessage("{}")),
		field.Time("created_at").Immutable().Default(time.Now),
	}
}

func (ContentAuditLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id", "created_at"),
		index.Fields("created_at"),
	}
}

func (ContentAuditLog) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "content_audit_log"}}
}
