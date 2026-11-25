package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"

	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/contentschema"
	"ksdevworks/ecommerce/api/internal/contentstore"
	"ksdevworks/ecommerce/api/internal/ent"
	entdoc "ksdevworks/ecommerce/api/internal/ent/contentdocument"
	entrev "ksdevworks/ecommerce/api/internal/ent/contentrevision"
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

	if err := contentstore.EnsureDocuments(ctx, tx.Client(), shopID); err != nil {
		return nil, fmt.Errorf("content: ensure documents: %w", err)
	}
	docs, err := loadDocuments(ctx, tx.Client(), shopID)
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
	if _, err := create.Save(ctx); err != nil {
		return nil, fmt.Errorf("content: write version %d: %w", next, err)
	}
	c := tx.Client()
	// Snapshot what is at risk, then flag the documents (design D4).
	for _, d := range report.Documents {
		if d.AtRisk() {
			if err := contentstore.AddSchemaSnapshot(ctx, c, shopID, d.Key, next, opts.ActorUserID); err != nil {
				return nil, fmt.Errorf("content: snapshot %q: %w", d.Key, err)
			}
		}
		if err := contentstore.SetFlags(ctx, c, shopID, d.Key, d.Status == StatusNeedsAttention, declaredUnder(parsed, d.Key)); err != nil {
			return nil, fmt.Errorf("content: flag %q: %w", d.Key, err)
		}
	}
	created, err := createDeclared(ctx, c, shopID, parsed, next, opts.ActorUserID)
	if err != nil {
		return nil, err
	}
	if err := contentstore.Audit(ctx, c, contentstore.AuditEntry{
		ShopID: shopID, Actor: opts.ActorUserID, Action: "schema.register",
		Detail: auditDetail(next, report, created),
	}); err != nil {
		return nil, fmt.Errorf("content: audit: %w", err)
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
			s.auditRow(ctx, shopID, opts.ActorUserID, "schema.dry_run", auditDetail(res.Version, res.Report, 0))
			return res, nil
		}
	}
	docs, err := loadDocuments(ctx, s.Client, shopID)
	if err != nil {
		return nil, err
	}
	res.Report = Diff(docs.inputs, typeSchemas(parsed), globalSchema(parsed))
	s.audit("content.schema.dry_run", shopID, opts.ActorUserID, res.Version, res.Report)
	s.auditRow(ctx, shopID, opts.ActorUserID, "schema.dry_run", auditDetail(res.Version, res.Report, 0))
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
}

// loadDocuments reads the shop's documents for the compatibility check. The
// document table is the source; legacy pages that have no document yet (rows
// written around the compatibility layer) are read from pages so a dry run,
// which writes nothing, still sees them.
func loadDocuments(ctx context.Context, c *ent.Client, shopID int) (*loadedDocs, error) {
	docs, err := c.ContentDocument.Query().Where(entdoc.ShopIDEQ(shopID)).Order(entdoc.ByID()).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("content: load documents: %w", err)
	}
	ld := &loadedDocs{}
	have := map[string]bool{}
	for _, d := range docs {
		have[d.Key] = true
		di := DocInput{Kind: KindPage, Key: d.Key, TypeKey: d.TypeKey, Draft: d.DraftJSON, Published: d.PublishedJSON}
		if d.Key == contentstore.GlobalKey {
			di.Kind, di.TypeKey = KindGlobal, "global"
		}
		ld.inputs = append(ld.inputs, di)
	}
	pages, err := c.Page.Query().Where(entpage.ShopIDEQ(shopID)).Order(entpage.ByID()).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("content: load pages: %w", err)
	}
	for _, p := range pages {
		if have[p.Slug] {
			continue
		}
		ld.inputs = append(ld.inputs, DocInput{Kind: KindPage, Key: p.Slug, TypeKey: p.TypeKey, Draft: p.ContentJSON, Published: p.PublishedJSON})
	}
	// shops.content_json is the live projection of the global document; when
	// it was written around the compatibility layer it is what is live.
	if shop, err := c.Shop.Get(ctx, shopID); err == nil && len(shop.ContentJSON) > 0 {
		for i := range ld.inputs {
			if ld.inputs[i].Key == contentstore.GlobalKey && !contentstore.SameJSON(ld.inputs[i].Published, shop.ContentJSON) {
				ld.inputs[i].Draft, ld.inputs[i].Published = shop.ContentJSON, shop.ContentJSON
			}
		}
		if !have[contentstore.GlobalKey] {
			ld.inputs = append(ld.inputs, DocInput{Kind: KindGlobal, Key: "global", TypeKey: "global", Draft: shop.ContentJSON})
		}
	}
	return ld, nil
}

// declaredUnder reports whether key is declared by the registered schema.
// Format 1 (and the shop-global document) declares everything.
func declaredUnder(p *Parsed, key string) bool {
	if key == contentstore.GlobalKey {
		return true
	}
	if _, ok := p.Documents[key]; ok {
		return true
	}
	for prefix := range p.Collections {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// createDeclared creates the fixed documents the schema declares and that do
// not exist yet, as empty drafts filled with the type's hydrated defaults
// (design D3). It returns how many it created.
func createDeclared(ctx context.Context, c *ent.Client, shopID int, p *Parsed, version, actor int) (int, error) {
	keys := make([]string, 0, len(p.Documents))
	for k := range p.Documents {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	n := 0
	for _, key := range keys {
		if key == contentstore.GlobalKey {
			continue
		}
		exists, err := c.ContentDocument.Query().Where(entdoc.ShopIDEQ(shopID), entdoc.KeyEQ(key)).Exist(ctx)
		if err != nil {
			return n, err
		}
		if exists {
			continue
		}
		decl := p.Documents[key]
		t, ok := p.Types[decl.Type]
		if !ok {
			continue
		}
		draft, err := cms.HydrateJSON(t.Schema, nil)
		if err != nil {
			return n, fmt.Errorf("content: hydrate defaults of %q: %w", decl.Type, err)
		}
		title := t.Title
		if title == "" {
			title = key
		}
		if _, _, err := contentstore.SaveDraft(ctx, c, contentstore.SaveInput{
			ShopID: shopID, Key: key, TypeKey: decl.Type, Title: title, Draft: draft,
			CreateOnly: true, SchemaVersion: version, Declared: true, Actor: actor,
		}); err != nil {
			return n, fmt.Errorf("content: create declared document %q: %w", key, err)
		}
		n++
	}
	return n, nil
}

// auditDetail is the PII-free detail of a schema audit row: identifiers and
// counts only.
func auditDetail(version int, r Report, created int) map[string]any {
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
	return map[string]any{
		"schema_version": version, "documents": len(r.Documents), "removed_paths": removed,
		"needs_attention": attention, "orphaned": orphaned, "created_documents": created,
	}
}

func (s *SchemaService) auditRow(ctx context.Context, shopID, actor int, action string, detail map[string]any) {
	if err := contentstore.Audit(ctx, s.Client, contentstore.AuditEntry{ShopID: shopID, Actor: actor, Action: action, Detail: detail}); err != nil {
		s.log().Warn("content audit row failed", "action", action, "shop_id", shopID, "err", err)
	}
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

// snapshotOfVersion rebuilds the version's snapshot ({"documents":[{key,
// draft, published}]}) from the schema_snapshot revisions it wrote (slice 2
// moved it out of content_schemas.snapshot_json). nil when nothing was at risk.
func (s *SchemaService) snapshotOfVersion(ctx context.Context, shopID, version int) json.RawMessage {
	revs, err := s.Client.ContentRevision.Query().
		Where(entrev.ShopIDEQ(shopID), entrev.KindEQ(contentstore.KindSchemaSnapshot), entrev.SchemaVersionEQ(version)).
		Order(entrev.ByDocumentID()).All(ctx)
	if err != nil || len(revs) == 0 {
		return nil
	}
	type entry struct {
		Kind      string          `json:"kind"`
		Key       string          `json:"key"`
		Draft     json.RawMessage `json:"draft"`
		Published json.RawMessage `json:"published,omitempty"`
	}
	list := make([]entry, 0, len(revs))
	for _, r := range revs {
		d, err := s.Client.ContentDocument.Query().Where(entdoc.IDEQ(r.DocumentID), entdoc.ShopIDEQ(shopID)).Only(ctx)
		if err != nil {
			continue
		}
		kind := KindPage
		if d.Key == contentstore.GlobalKey {
			kind = KindGlobal
		}
		list = append(list, entry{Kind: kind, Key: d.Key, Draft: r.JSON, Published: r.PublishedJSON})
	}
	b, err := json.Marshal(map[string]any{"documents": list})
	if err != nil {
		return nil
	}
	return b
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
		CreatedByUserID: row.CreatedByUserID, CreatedAt: row.CreatedAt,
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
	d.HasSnapshot = s.snapshotVersions(ctx, shopID)[row.Version]
	if withSnapshot {
		d.Snapshot = s.snapshotOfVersion(ctx, shopID, row.Version)
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
	snaps := s.snapshotVersions(ctx, shopID)
	for _, r := range rows {
		i := info(r)
		i.HasSnapshot = snaps[r.Version]
		out = append(out, i)
	}
	return out, nil
}

// snapshotVersions is the set of schema versions that wrote snapshot revisions.
func (s *SchemaService) snapshotVersions(ctx context.Context, shopID int) map[int]bool {
	vs, err := s.Client.ContentRevision.Query().
		Where(entrev.ShopIDEQ(shopID), entrev.KindEQ(contentstore.KindSchemaSnapshot)).
		Select(entrev.FieldSchemaVersion).Ints(ctx)
	out := map[int]bool{}
	if err != nil {
		return out
	}
	for _, v := range vs {
		out[v] = true
	}
	return out
}
