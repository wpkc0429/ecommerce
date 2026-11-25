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
	"github.com/google/uuid"
)

// Design history (change design-history, slice 1). Six tables record every AI
// redesign of a shop's storefront or admin: who asked, what changed, who
// approved. The history is held by the platform, not by the AI edit service.
//
// Things ent cannot express are written by hand in the migrations and are the
// source of truth there: the composite same-shop foreign keys, the CHECK
// expressions, and the append-only triggers (..._add_design_history and
// ..._add_design_history_append_only_triggers). Index names below are pinned
// with StorageKey so the Go code can tell which constraint fired.

// DesignRequest is one redesign. status is a projection of the events: only the
// state machine in internal/design changes it, always with a conditional UPDATE
// and in the same transaction that appends the event.
type DesignRequest struct {
	ent.Schema
}

func (DesignRequest) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.Int("shop_id").Immutable(),
		field.String("site").MaxLen(10).SchemaType(map[string]string{dialect.Postgres: "varchar(10)"}).Immutable(),
		field.String("kind").MaxLen(10).SchemaType(map[string]string{dialect.Postgres: "varchar(10)"}).Immutable(),
		field.String("status").MaxLen(20).SchemaType(map[string]string{dialect.Postgres: "varchar(20)"}).Default("drafting"),
		// No FK on purpose (same reasoning as content_audit_log): the history
		// outlives the account. The service verifies the user at creation.
		field.Int("requested_by_user_id").Immutable(),
		// prompt_text: redacted at intake; NULL after the retention period.
		field.String("prompt_text").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("prompt_sha256").MaxLen(64).SchemaType(map[string]string{dialect.Postgres: "char(64)"}).Immutable(),
		field.JSON("prompt_flags", []string{}).Default([]string{}),
		field.String("base_sha").MaxLen(40).SchemaType(map[string]string{dialect.Postgres: "char(40)"}).Immutable(),
		field.String("head_sha").MaxLen(40).SchemaType(map[string]string{dialect.Postgres: "char(40)"}),
		field.String("candidate_sha").MaxLen(40).SchemaType(map[string]string{dialect.Postgres: "char(40)"}).Optional().Nillable(),
		field.String("published_sha").MaxLen(40).SchemaType(map[string]string{dialect.Postgres: "char(40)"}).Optional().Nillable(),
		field.String("branch").MaxLen(120).SchemaType(map[string]string{dialect.Postgres: "varchar(120)"}),
		field.String("core_version").MaxLen(20).SchemaType(map[string]string{dialect.Postgres: "varchar(20)"}).Default(""),
		field.String("design_floor_version").MaxLen(20).SchemaType(map[string]string{dialect.Postgres: "varchar(20)"}).Default(""),
		// diff_summary: paths and counts only, never file contents (<= 16 KiB).
		field.JSON("diff_summary", json.RawMessage{}).Default(json.RawMessage("{}")),
		// guard: the gatekeeper's verdict (passed, checked_sha, rule ids ...).
		field.JSON("guard", json.RawMessage{}).Optional(),
		// preview_origin: an origin only, never a path or query.
		field.String("preview_origin").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).Optional().Nillable(),
		field.Time("preview_registered_at").Optional().Nillable(),
		field.Time("preview_expires_at").Optional().Nillable(),
		field.Int("approved_by_user_id").Optional().Nillable(),
		field.Time("approved_at").Optional().Nillable(),
		field.String("approved_head_sha").MaxLen(40).SchemaType(map[string]string{dialect.Postgres: "char(40)"}).Optional().Nillable(),
		field.Time("approval_expires_at").Optional().Nillable(),
		field.String("publish_tag").MaxLen(60).SchemaType(map[string]string{dialect.Postgres: "varchar(60)"}).Optional().Nillable(),
		field.Int("rejected_by_user_id").Optional().Nillable(),
		field.Time("rejected_at").Optional().Nillable(),
		field.String("reject_reason").MaxLen(500).SchemaType(map[string]string{dialect.Postgres: "varchar(500)"}).Optional().Nillable(),
		// change_note: the owner's "request changes" note; plain text, data and
		// not an instruction.
		field.String("change_note").MaxLen(500).SchemaType(map[string]string{dialect.Postgres: "varchar(500)"}).Optional().Nillable(),
		field.Time("published_at").Optional().Nillable(),
		field.String("published_tag").MaxLen(60).SchemaType(map[string]string{dialect.Postgres: "varchar(60)"}).Optional().Nillable(),
		field.String("deployment_id").MaxLen(100).SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}).Optional().Nillable(),
		field.UUID("restores_request_id", uuid.UUID{}).Optional().Nillable().Immutable(),
		field.UUID("reverted_by_request_id", uuid.UUID{}).Optional().Nillable(),
		field.String("failure_code").MaxLen(40).SchemaType(map[string]string{dialect.Postgres: "varchar(40)"}).Optional().Nillable(),
		field.String("failure_detail").MaxLen(300).SchemaType(map[string]string{dialect.Postgres: "varchar(300)"}).Optional().Nillable(),
		// rev = the sequence number of the last event; the ETag basis.
		field.Int("rev").Default(1),
		field.String("idempotency_key").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}).Optional().Nillable(),
		field.String("idempotency_fingerprint").MaxLen(64).SchemaType(map[string]string{dialect.Postgres: "char(64)"}).Optional().Nillable(),
		field.Int("claimed_by_credential_id").Optional().Nillable(),
		field.Time("claimed_at").Optional().Nillable(),
		field.Time("claim_expires_at").Optional().Nillable(),
		field.Time("created_at").Immutable().Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (DesignRequest) Indexes() []ent.Index {
	return []ent.Index{
		// Target of the composite same-shop foreign keys.
		index.Fields("shop_id", "id").Unique().StorageKey("design_requests_shop_id_id_key"),
		index.Fields("shop_id", "site", "created_at").StorageKey("design_requests_shop_site_created"),
		// One non-terminal request per (shop, site).
		index.Fields("shop_id", "site").Unique().StorageKey("design_requests_one_active").
			Annotations(entsql.IndexWhere("status IN ('drafting', 'previewing', 'awaiting_approval', 'approved')")),
		index.Fields("shop_id", "idempotency_key").Unique().StorageKey("design_requests_idem_key").
			Annotations(entsql.IndexWhere("idempotency_key IS NOT NULL")),
		index.Fields("shop_id", "publish_tag").Unique().StorageKey("design_requests_publish_tag_key").
			Annotations(entsql.IndexWhere("publish_tag IS NOT NULL")),
		index.Fields("status", "updated_at").StorageKey("design_requests_status_updated"),
	}
}

func (DesignRequest) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "design_requests",
			Checks: map[string]string{
				"design_requests_site_check":   "site IN ('storefront', 'admin')",
				"design_requests_kind_check":   "kind IN ('change', 'revert')",
				"design_requests_status_check": "status IN ('drafting', 'previewing', 'awaiting_approval', 'approved', 'published', 'rejected', 'failed', 'cancelled', 'reverted')",
			},
		},
	}
}

// DesignRequestEvent is the append-only event log of a request. Database
// triggers refuse UPDATE and DELETE (except the retention purge); every event
// carries the SHA-256 of its predecessor so a missing or rewritten middle row
// is detectable (cmd/design-sweep -verify).
type DesignRequestEvent struct {
	ent.Schema
}

func (DesignRequestEvent) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.UUID("request_id", uuid.UUID{}),
		field.Int("seq"),
		field.String("kind").MaxLen(30).SchemaType(map[string]string{dialect.Postgres: "varchar(30)"}),
		field.String("from_status").MaxLen(20).SchemaType(map[string]string{dialect.Postgres: "varchar(20)"}).Default(""),
		field.String("to_status").MaxLen(20).SchemaType(map[string]string{dialect.Postgres: "varchar(20)"}).Default(""),
		field.String("actor_type").MaxLen(10).SchemaType(map[string]string{dialect.Postgres: "varchar(10)"}),
		field.Int("actor_user_id").Optional().Nillable(),
		field.Int("actor_credential_id").Optional().Nillable(),
		field.String("head_sha").MaxLen(40).SchemaType(map[string]string{dialect.Postgres: "varchar(40)"}).Default(""),
		// detail: ids, counts, rule ids and reason codes only (<= 8 KiB). Never a
		// prompt, stderr, URL or path.
		field.JSON("detail", json.RawMessage{}).Default(json.RawMessage("{}")),
		field.String("prev_hash").MaxLen(64).SchemaType(map[string]string{dialect.Postgres: "char(64)"}),
		field.String("hash").MaxLen(64).SchemaType(map[string]string{dialect.Postgres: "char(64)"}),
		field.Time("created_at").Immutable().Default(time.Now),
	}
}

func (DesignRequestEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("request_id", "seq").Unique().StorageKey("design_request_events_request_seq_key"),
		index.Fields("shop_id", "created_at").StorageKey("design_request_events_shop_created"),
	}
}

func (DesignRequestEvent) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "design_request_events",
			Checks: map[string]string{
				"design_request_events_kind_check":  "kind IN ('created', 'claimed', 'head_updated', 'guard_run', 'preview_registered', 'screenshots_complete', 'submitted', 'changes_requested', 'approved', 'rejected', 'cancelled', 'guard_rechecked', 'published', 'failed', 'revert_requested', 'reverted_mark', 'expired')",
				"design_request_events_actor_check": "actor_type IN ('service', 'user', 'system')",
			},
		},
	}
}

// DesignScreenshot is the metadata of one stored screenshot (slice 2 writes
// them; slice 1 only creates the table and its append-only guard).
type DesignScreenshot struct {
	ent.Schema
}

func (DesignScreenshot) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.Int("shop_id"),
		field.UUID("request_id", uuid.UUID{}),
		field.String("phase").MaxLen(6).SchemaType(map[string]string{dialect.Postgres: "varchar(6)"}),
		field.String("site").MaxLen(10).SchemaType(map[string]string{dialect.Postgres: "varchar(10)"}),
		field.String("view_key").MaxLen(40).SchemaType(map[string]string{dialect.Postgres: "varchar(40)"}),
		field.Int16("viewport"),
		field.String("role").MaxLen(10).SchemaType(map[string]string{dialect.Postgres: "varchar(10)"}),
		field.Int16("attempt").Default(1),
		field.String("commit_sha").MaxLen(40).SchemaType(map[string]string{dialect.Postgres: "char(40)"}),
		field.String("object_key").MaxLen(255).SchemaType(map[string]string{dialect.Postgres: "varchar(255)"}),
		field.String("content_type").MaxLen(20).SchemaType(map[string]string{dialect.Postgres: "varchar(20)"}).Default("image/png"),
		field.Int("byte_size"),
		field.Int("width"),
		field.Int("height"),
		field.String("sha256").MaxLen(64).SchemaType(map[string]string{dialect.Postgres: "char(64)"}),
		field.Int("diff_ppm").Optional().Nillable(),
		field.JSON("capture_manifest", json.RawMessage{}).Default(json.RawMessage("{}")),
		field.Time("purged_at").Optional().Nillable(),
		field.Time("created_at").Immutable().Default(time.Now),
	}
}

func (DesignScreenshot) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("request_id", "phase", "role", "viewport", "view_key", "attempt").Unique().StorageKey("design_screenshots_slot_attempt_key"),
		index.Fields("shop_id", "created_at").StorageKey("design_screenshots_shop_created"),
	}
}

func (DesignScreenshot) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "design_screenshots",
			Checks: map[string]string{
				"design_screenshots_phase_check":    "phase IN ('before', 'after')",
				"design_screenshots_site_check":     "site IN ('storefront', 'admin')",
				"design_screenshots_view_key_check": "view_key ~ '^[a-z0-9-]+$'",
				"design_screenshots_viewport_check": "viewport IN (390, 1280)",
				"design_screenshots_role_check":     "role IN ('owner', 'staff', 'anon')",
				"design_screenshots_type_check":     "content_type = 'image/png'",
				"design_screenshots_size_check":     "byte_size BETWEEN 1 AND 2097152",
				"design_screenshots_width_check":    "width = viewport",
				"design_screenshots_height_check":   "height BETWEEN 1 AND 10000",
			},
		},
	}
}

// DesignSiteState is the current design of one site of a shop, reported by the
// service and updated by the publish flow.
type DesignSiteState struct {
	ent.Schema
}

func (DesignSiteState) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.String("site").MaxLen(10).SchemaType(map[string]string{dialect.Postgres: "varchar(10)"}),
		field.UUID("current_request_id", uuid.UUID{}).Optional().Nillable(),
		field.String("current_tag").MaxLen(60).SchemaType(map[string]string{dialect.Postgres: "varchar(60)"}).Default(""),
		field.String("current_sha").MaxLen(40).SchemaType(map[string]string{dialect.Postgres: "varchar(40)"}).Default(""),
		field.String("core_version").MaxLen(20).SchemaType(map[string]string{dialect.Postgres: "varchar(20)"}).Default(""),
		field.String("design_floor_version").MaxLen(20).SchemaType(map[string]string{dialect.Postgres: "varchar(20)"}).Default(""),
		field.String("last_seen_main_sha").MaxLen(40).SchemaType(map[string]string{dialect.Postgres: "varchar(40)"}).Default(""),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (DesignSiteState) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id", "site").Unique().StorageKey("design_site_state_shop_site_key"),
	}
}

func (DesignSiteState) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table:  "design_site_state",
			Checks: map[string]string{"design_site_state_site_check": "site IN ('storefront', 'admin')"},
		},
	}
}

// DesignUntrackedChange is a commit on main that touched design paths but
// belongs to no request (found by the service's reconciliation).
type DesignUntrackedChange struct {
	ent.Schema
}

func (DesignUntrackedChange) Fields() []ent.Field {
	return []ent.Field{
		field.Int("shop_id"),
		field.String("site").MaxLen(10).SchemaType(map[string]string{dialect.Postgres: "varchar(10)"}),
		field.String("commit_sha").MaxLen(40).SchemaType(map[string]string{dialect.Postgres: "char(40)"}),
		field.Time("committed_at"),
		// subject: plain text; never an author name or e-mail (personal data).
		field.String("subject").MaxLen(200).SchemaType(map[string]string{dialect.Postgres: "varchar(200)"}).Default(""),
		field.Int("files_changed").Default(0),
		field.Int("additions").Default(0),
		field.Int("deletions").Default(0),
		field.Time("detected_at").Immutable().Default(time.Now),
		field.Int("acknowledged_by_user_id").Optional().Nillable(),
		field.Time("acknowledged_at").Optional().Nillable(),
	}
}

func (DesignUntrackedChange) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("shop_id", "site", "commit_sha").Unique().StorageKey("design_untracked_changes_commit_key"),
	}
}

func (DesignUntrackedChange) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table:  "design_untracked_changes",
			Checks: map[string]string{"design_untracked_changes_site_check": "site IN ('storefront', 'admin')"},
		},
	}
}
