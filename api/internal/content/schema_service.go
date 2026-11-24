package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"entgo.io/ent/dialect/sql"

	"ksdevworks/ecommerce/api/internal/contentschema"
	"ksdevworks/ecommerce/api/internal/ent"
	entcs "ksdevworks/ecommerce/api/internal/ent/contentschema"
	entpage "ksdevworks/ecommerce/api/internal/ent/page"
	entshop "ksdevworks/ecommerce/api/internal/ent/shop"
	"ksdevworks/ecommerce/api/internal/events"
)

// ErrNotFound marks a missing shop or schema version (handler -> 404).
var ErrNotFound = errors.New("content: not found")

// RemovalsError is returned when a registration would drop values or orphan
// documents and the caller did not pass ConfirmRemovals (handler -> 409).
type RemovalsError struct{ Report Report }

func (e *RemovalsError) Error() string {
	return "registering this schema would remove existing content; review the report and retry with confirm_removals=true"
}

// SchemaService registers and reads per-shop content schemas.
//
// Registration is a human action (design D3/Q3): the HTTP layer guards it with
// the content.manage node, which only merchant_owner and the platform
// super_admin hold. Nothing in the repository calls Register automatically, and
// nothing should: see docs/content-schema.md.
type SchemaService struct {
	Client     *ent.Client
	Dispatcher *events.Dispatcher
	Log        *slog.Logger
}

// RegisterOptions of one registration call.
type RegisterOptions struct {
	DryRun          bool
	ConfirmRemovals bool
	ActorUserID     int
}

// RegisterResult is the outcome of Register.
type RegisterResult struct {
	// Version is the active version after the call (on a dry run, the one
	// currently active; 0 when the shop has none).
	Version int
	// Changed is false when the submitted schema equals the active one.
	Changed bool
	DryRun  bool
	// WouldRegisterVersion is set on a dry run that would change something.
	WouldRegisterVersion int
	Report               Report
}

func (s *SchemaService) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Register validates raw (design D3 steps 1-8), reports its effect on the
// shop's existing content and, unless DryRun, writes the next version.
//
// It never rewrites a document's content. It only flips pages.needs_attention
// and stores a snapshot of what was at risk with the new version.
func (s *SchemaService) Register(ctx context.Context, shopID int, raw []byte, opts RegisterOptions) (*RegisterResult, error) {
	parsed, details := Validate(raw)
	if len(details) > 0 {
		return nil, newValidationError(details)
	}
	if opts.DryRun {
		return s.dryRun(ctx, shopID, parsed, opts)
	}

	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("content: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Serialize registrations of one shop: the version number is max+1.
	if _, err := tx.Shop.Query().Where(entshop.IDEQ(shopID)).ForUpdate().Only(ctx); err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("content: lock shop: %w", err)
	}
	last, err := latest(ctx, tx.ContentSchema.Query().Where(entcs.ShopIDEQ(shopID)))
	if err != nil {
		return nil, err
	}
	if last != nil {
		same, derr := sameDigest(last, parsed.Digest)
		if derr != nil {
			return nil, derr
		}
		if same {
			return &RegisterResult{Version: last.Version, Changed: false, Report: Report{Documents: []DocReport{}}}, nil
		}
	}

	docs, err := loadDocuments(ctx, tx.Page.Query(), tx.Shop.Query(), shopID)
	if err != nil {
		return nil, err
	}
	report := Diff(docs.inputs, typeSchemas(parsed), globalSchema(parsed))
	if report.HasRemovals() && !opts.ConfirmRemovals {
		return nil, &RemovalsError{Report: report}
	}

	next := 1
	if last != nil {
		next = last.Version + 1
	}
	create := tx.ContentSchema.Create().
		SetShopID(shopID).
		SetVersion(next).
		SetSchemaJSON(parsed.Normalized).
		SetSha256(parsed.Digest).
		SetSource(contentschema.SourceRegistered)
	if opts.ActorUserID > 0 {
		create.SetCreatedByUserID(opts.ActorUserID)
	}
	if snap := snapshotOf(report, docs); snap != nil {
		create.SetSnapshotJSON(snap)
	}
	if _, err := create.Save(ctx); err != nil {
		return nil, fmt.Errorf("content: write version %d: %w", next, err)
	}
	for _, d := range report.Documents {
		if d.Kind != KindPage {
			continue
		}
		if err := tx.Page.Update().
			Where(entpage.ShopIDEQ(shopID), entpage.SlugEQ(d.Key)).
			SetNeedsAttention(d.Status == StatusNeedsAttention).
			Exec(ctx); err != nil {
			return nil, fmt.Errorf("content: flag page: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("content: commit: %w", err)
	}

	if s.Dispatcher != nil {
		s.Dispatcher.Publish(ctx, events.ContentSchemaRegistered{ShopID: shopID, Version: next})
	}
	s.audit("content.schema.register", shopID, opts.ActorUserID, next, report)
	return &RegisterResult{Version: next, Changed: true, Report: report}, nil
}

func (s *SchemaService) dryRun(ctx context.Context, shopID int, parsed *Parsed, opts RegisterOptions) (*RegisterResult, error) {
	if _, err := s.Client.Shop.Query().Where(entshop.IDEQ(shopID)).Only(ctx); err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("content: load shop: %w", err)
	}
	last, err := latest(ctx, s.Client.ContentSchema.Query().Where(entcs.ShopIDEQ(shopID)))
	if err != nil {
		return nil, err
	}
	res := &RegisterResult{DryRun: true, Changed: true, WouldRegisterVersion: 1}
	if last != nil {
		res.Version = last.Version
		res.WouldRegisterVersion = last.Version + 1
		same, derr := sameDigest(last, parsed.Digest)
		if derr != nil {
			return nil, derr
		}
		if same {
			res.Changed = false
			res.WouldRegisterVersion = 0
			res.Report = Report{Documents: []DocReport{}}
			s.audit("content.schema.dry_run", shopID, opts.ActorUserID, res.Version, res.Report)
			return res, nil
		}
	}
	docs, err := loadDocuments(ctx, s.Client.Page.Query(), s.Client.Shop.Query(), shopID)
	if err != nil {
		return nil, err
	}
	res.Report = Diff(docs.inputs, typeSchemas(parsed), globalSchema(parsed))
	s.audit("content.schema.dry_run", shopID, opts.ActorUserID, res.Version, res.Report)
	return res, nil
}

// audit writes the structured audit line. It carries identifiers and counts
// only: never schema text, never content (design D9).
func (s *SchemaService) audit(action string, shopID, actor, version int, r Report) {
	removed, attention, orphaned := 0, 0, 0
	for _, d := range r.Documents {
		removed += len(d.RemovedPaths)
		switch d.Status {
		case StatusNeedsAttention:
			attention++
		case StatusOrphaned:
			orphaned++
		}
	}
	s.log().Info("content audit",
		"action", action, "shop_id", shopID, "actor_user_id", actor, "schema_version", version,
		"documents", len(r.Documents), "removed_paths", removed, "needs_attention", attention, "orphaned", orphaned)
}

func typeSchemas(p *Parsed) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(p.Types))
	for k, t := range p.Types {
		if k == "global" {
			continue
		}
		out[k] = t.Schema
	}
	return out
}

func globalSchema(p *Parsed) json.RawMessage {
	if g, ok := p.Types["global"]; ok {
		return g.Schema
	}
	return json.RawMessage(contentschema.EmptyGlobalSchema)
}

func latest(ctx context.Context, q *ent.ContentSchemaQuery) (*ent.ContentSchema, error) {
	row, err := q.Order(entcs.ByVersion(sql.OrderDesc())).First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("content: load active version: %w", err)
	}
	return row, nil
}

func sameDigest(row *ent.ContentSchema, digest string) (bool, error) {
	_, d, err := NormalizeRaw(row.SchemaJSON)
	if err != nil {
		return false, fmt.Errorf("content: normalize stored version %d: %w", row.Version, err)
	}
	return d == digest, nil
}

type loadedDocs struct {
	inputs []DocInput
	byKey  map[string]DocInput
}

func loadDocuments(ctx context.Context, pq *ent.PageQuery, sq *ent.ShopQuery, shopID int) (*loadedDocs, error) {
	pages, err := pq.Where(entpage.ShopIDEQ(shopID)).Order(entpage.ByID()).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("content: load pages: %w", err)
	}
	shop, err := sq.Where(entshop.IDEQ(shopID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("content: load shop content: %w", err)
	}
	ld := &loadedDocs{byKey: map[string]DocInput{}}
	for _, p := range pages {
		di := DocInput{Kind: KindPage, Key: p.Slug, TypeKey: p.TypeKey, Draft: p.ContentJSON, Published: p.PublishedJSON}
		ld.inputs = append(ld.inputs, di)
		ld.byKey[KindPage+":"+p.Slug] = di
	}
	if len(shop.ContentJSON) > 0 {
		di := DocInput{Kind: KindGlobal, Key: "global", TypeKey: "global", Draft: shop.ContentJSON}
		ld.inputs = append(ld.inputs, di)
		ld.byKey[KindGlobal+":global"] = di
	}
	return ld, nil
}

// snapshotOf serializes the at-risk documents as they are right now.
func snapshotOf(r Report, ld *loadedDocs) json.RawMessage {
	type entry struct {
		Kind      string          `json:"kind"`
		Key       string          `json:"key"`
		Status    string          `json:"status"`
		Draft     json.RawMessage `json:"draft"`
		Published json.RawMessage `json:"published,omitempty"`
	}
	var list []entry
	for _, d := range r.Documents {
		if !d.AtRisk() {
			continue
		}
		in := ld.byKey[d.Kind+":"+d.Key]
		e := entry{Kind: d.Kind, Key: d.Key, Status: d.Status, Draft: in.Draft}
		if len(in.Published) > 0 && string(in.Published) != string(in.Draft) {
			e.Published = in.Published
		}
		list = append(list, e)
	}
	if len(list) == 0 {
		return nil
	}
	b, err := json.Marshal(map[string]any{"documents": list})
	if err != nil {
		return nil
	}
	return b
}

// ── reads ─────────────────────────────────────────────────────────────

// VersionInfo describes one stored schema version.
type VersionInfo struct {
	Version         int
	Format          int
	SHA256          string
	Source          string
	CreatedByUserID *int
	CreatedAt       time.Time
	HasSnapshot     bool
}

// VersionDetail is a version with its schema document (and optional snapshot).
type VersionDetail struct {
	VersionInfo
	Schema   json.RawMessage
	Snapshot json.RawMessage
}

func info(row *ent.ContentSchema) VersionInfo {
	format := 1
	if v, err := contentschema.DecodeRow(row); err == nil && v.Doc.Format == 2 {
		format = 2
	}
	return VersionInfo{
		Version: row.Version, Format: format, SHA256: row.Sha256, Source: row.Source,
		CreatedByUserID: row.CreatedByUserID, CreatedAt: row.CreatedAt, HasSnapshot: len(row.SnapshotJSON) > 0,
	}
}

// Get returns one version (version 0 = the active one).
func (s *SchemaService) Get(ctx context.Context, shopID, version int, withSnapshot bool) (*VersionDetail, error) {
	if _, err := s.Client.Shop.Query().Where(entshop.IDEQ(shopID)).Only(ctx); err != nil {
		return nil, ErrNotFound
	}
	q := s.Client.ContentSchema.Query().Where(entcs.ShopIDEQ(shopID))
	var row *ent.ContentSchema
	var err error
	if version > 0 {
		row, err = q.Where(entcs.VersionEQ(version)).Only(ctx)
	} else {
		row, err = q.Order(entcs.ByVersion(sql.OrderDesc())).First(ctx)
	}
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("content: load version: %w", err)
	}
	d := &VersionDetail{VersionInfo: info(row), Schema: row.SchemaJSON}
	if withSnapshot {
		d.Snapshot = row.SnapshotJSON
	}
	return d, nil
}

// Versions lists every stored version, newest first.
func (s *SchemaService) Versions(ctx context.Context, shopID int) ([]VersionInfo, error) {
	if _, err := s.Client.Shop.Query().Where(entshop.IDEQ(shopID)).Only(ctx); err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.Client.ContentSchema.Query().
		Where(entcs.ShopIDEQ(shopID)).
		Order(entcs.ByVersion(sql.OrderDesc())).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("content: list versions: %w", err)
	}
	out := make([]VersionInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, info(r))
	}
	return out, nil
}
