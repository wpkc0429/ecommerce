// Package schema — merchant application entities (change platform-website,
// design D4). These four tables are PLATFORM data, not tenant data: an
// application exists before any shop does. None of them has a column named
// shop_id (the shop an approval creates is provisioned_shop_id), so the
// tenancy interceptor test needs no classification for them.
package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ShopApplication is one public request to open a shop.
//
// PII: email, contact_name, phone, shop_name, business_summary. Retention is
// applied by cmd/purge-applications (design D8): unverified rows are deleted,
// others have these columns blanked and purged_at set. The source IP is never
// stored, only HMAC(key, ip).
//
// Every state transition is a conditional UPDATE decided by rows affected,
// never an application-level read-then-write.
type ShopApplication struct {
	ent.Schema
}

func (ShopApplication) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (ShopApplication) Fields() []ent.Field {
	return []ent.Field{
		field.String("status").MaxLen(24).SchemaType(map[string]string{dialect.Postgres: "varchar(24)"}).
			Default("pending_verification"),
		// email is normalized (auth.NormalizeEmail). Blank after a PII purge.
		field.String("email").MaxLen(254).SchemaType(map[string]string{dialect.Postgres: "varchar(254)"}),
		field.Time("email_verified_at").Optional().Nillable(),
		field.String("contact_name").MaxLen(100).SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}),
		field.String("phone").MaxLen(20).SchemaType(map[string]string{dialect.Postgres: "varchar(20)"}).Default(""),
		field.String("shop_name").MaxLen(100).SchemaType(map[string]string{dialect.Postgres: "varchar(100)"}),
		field.Text("business_summary"),
		field.String("consent_version").MaxLen(32).SchemaType(map[string]string{dialect.Postgres: "varchar(32)"}),
		field.Time("consented_at"),
		// risk_flags: honeypot-free booleans such as filled_too_fast,
		// email_has_account, manually_verified. No PII.
		field.JSON("risk_flags", map[string]bool{}).Optional().
			Default(map[string]bool{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.String("submitted_ip_hmac").MaxLen(64).SchemaType(map[string]string{dialect.Postgres: "varchar(64)"}).Default(""),
		field.Int("reviewed_by").Optional().Nillable(),
		field.Time("reviewed_at").Optional().Nillable(),
		// decision_reason is shown to the applicant (rejection); internal_note
		// never is.
		field.Text("decision_reason").Default(""),
		field.Text("internal_note").Default(""),
		// provisioned_shop_id is deliberately NOT called shop_id and has no FK
		// (see the package comment). UNIQUE: one application opens one shop.
		field.Int("provisioned_shop_id").Optional().Nillable().Unique(),
		field.Time("purged_at").Optional().Nillable(),
	}
}

func (ShopApplication) Indexes() []ent.Index {
	return []ent.Index{
		// At most one unfinished application per e-mail — the database
		// backstop that still holds when the rate limiter fails open.
		index.Fields("email").Unique().
			Annotations(entsql.IndexWhere("status IN ('pending_verification', 'pending_review')")).
			StorageKey("shopapplication_email_open_key"),
		index.Fields("status", "created_at"),
		index.Fields("email", "status"),
	}
}

func (ShopApplication) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "shop_applications",
			Checks: map[string]string{
				"shop_applications_status_check": "status IN ('pending_verification', 'pending_review', 'approved', 'rejected', 'expired', 'withdrawn')",
			},
		},
	}
}

// ShopApplicationToken is a one-time e-mail verification token (design D6):
// SHA-256 only, consumed atomically, a newer one retires older ones.
type ShopApplicationToken struct {
	ent.Schema
}

func (ShopApplicationToken) Fields() []ent.Field {
	return []ent.Field{
		field.Int("application_id"),
		field.String("token_hash").MaxLen(64).SchemaType(map[string]string{dialect.Postgres: "varchar(64)"}).NotEmpty().Unique(),
		field.Time("expires_at"),
		field.Time("used_at").Optional().Nillable(),
		field.Time("created_at").Immutable().Default(time.Now),
	}
}

func (ShopApplicationToken) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("application", ShopApplication.Type).Field("application_id").Unique().Required().
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (ShopApplicationToken) Indexes() []ent.Index {
	return []ent.Index{index.Fields("application_id", "created_at")}
}

func (ShopApplicationToken) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "shop_application_tokens"}}
}

// ShopApplicationEvent is the audit trail. application_id has NO foreign key on
// purpose: events outlive a purged (deleted) application. detail MUST NOT hold
// an e-mail, name or phone.
type ShopApplicationEvent struct {
	ent.Schema
}

func (ShopApplicationEvent) Fields() []ent.Field {
	return []ent.Field{
		field.Int("application_id"),
		field.Int("actor_user_id").Optional().Nillable(),
		field.String("event").MaxLen(32).SchemaType(map[string]string{dialect.Postgres: "varchar(32)"}).NotEmpty(),
		field.JSON("detail", map[string]any{}).Optional().
			Default(map[string]any{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Time("created_at").Immutable().Default(time.Now),
	}
}

func (ShopApplicationEvent) Indexes() []ent.Index {
	return []ent.Index{index.Fields("application_id", "id")}
}

func (ShopApplicationEvent) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "shop_application_events",
			Checks: map[string]string{
				"shop_application_events_event_check": "event IN ('submitted', 'verification_sent', 'verified', 'verified_manually', 'approved', 'approve_failed', 'rejected', 'note', 'message_marked_sent', 'expired', 'purged')",
			},
		},
	}
}

// ShopApplicationMessage is the notification outbox (design D5). Rows carry the
// recipient e-mail and body, so they are blanked with the application's PII.
// The owner's password-setup link is NEVER written here.
type ShopApplicationMessage struct {
	ent.Schema
}

func (ShopApplicationMessage) Fields() []ent.Field {
	return []ent.Field{
		field.Int("application_id"),
		field.String("kind").MaxLen(24).SchemaType(map[string]string{dialect.Postgres: "varchar(24)"}),
		field.String("recipient_email").MaxLen(254).SchemaType(map[string]string{dialect.Postgres: "varchar(254)"}),
		field.String("subject").MaxLen(200).SchemaType(map[string]string{dialect.Postgres: "varchar(200)"}),
		field.Text("body_text"),
		field.String("delivery").MaxLen(8).SchemaType(map[string]string{dialect.Postgres: "varchar(8)"}),
		// status: pending_manual | queued (e-mail, not yet handed over) | sent | failed.
		field.String("status").MaxLen(16).SchemaType(map[string]string{dialect.Postgres: "varchar(16)"}),
		field.String("error_reason").MaxLen(200).SchemaType(map[string]string{dialect.Postgres: "varchar(200)"}).Default(""),
		field.Time("created_at").Immutable().Default(time.Now),
		field.Time("sent_at").Optional().Nillable(),
		field.Int("marked_by").Optional().Nillable(),
	}
}

func (ShopApplicationMessage) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("application", ShopApplication.Type).Field("application_id").Unique().Required().
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (ShopApplicationMessage) Indexes() []ent.Index {
	return []ent.Index{index.Fields("status", "id"), index.Fields("application_id", "id")}
}

func (ShopApplicationMessage) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{
			Table: "shop_application_messages",
			Checks: map[string]string{
				"shop_application_messages_kind_check":     "kind IN ('verify_email', 'approved', 'rejected')",
				"shop_application_messages_delivery_check": "delivery IN ('manual', 'email')",
				"shop_application_messages_status_check":   "status IN ('pending_manual', 'queued', 'sent', 'failed')",
			},
		},
	}
}
