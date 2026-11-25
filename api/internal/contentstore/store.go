// Package contentstore is the persistence layer of the headless content store
// (change headless-content-store slice 2): documents, their bounded revision
// history and the audit log.
//
// It knows nothing about schemas, sanitization or HTTP: callers validate
// first, then hand it the content to store. It is a separate, low package so
// that both the legacy CMS service (internal/cms) and the new content service
// (internal/content, which imports cms) can use it without an import cycle.
//
// Compatibility (design D1, migration path): the legacy tables pages and
// shops.content_json stay in use by the compatibility endpoints. Every write
// here also writes the legacy side in the SAME transaction, and the legacy
// service calls the Sync* functions in the same transaction as its own write,
// so the two views are always equal. The caller owns the transaction.
package contentstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"entgo.io/ent/dialect/sql"

	"ksdevworks/ecommerce/api/internal/ent"
	entdoc "ksdevworks/ecommerce/api/internal/ent/contentdocument"
	entrev "ksdevworks/ecommerce/api/internal/ent/contentrevision"
	entpage "ksdevworks/ecommerce/api/internal/ent/page"
	entshop "ksdevworks/ecommerce/api/internal/ent/shop"
)

// Retention (design D1).
const (
	MaxPublishRevisions     = 20
	MaxSnapshotsPerDocument = 5
)

// Revision kinds.
const (
	KindPublish        = "publish"
	KindSchemaSnapshot = "schema_snapshot"
)

// GlobalKey is the document that carries the shop-global content
// (shops.content_json); its type_key is "global".
const GlobalKey = "global"

// Sentinel errors (handlers map them to HTTP statuses).
var (
	ErrNotFound = errors.New("contentstore: document not found")
	// ErrConflict: the optimistic lock failed (draft_rev moved on).
	ErrConflict = errors.New("contentstore: draft_rev mismatch")
	// ErrExists: a create-only save found the key already taken.
	ErrExists = errors.New("contentstore: document already exists")
)

var keyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidKey reports whether k is a legal document key.
func ValidKey(k string) bool { return keyRe.MatchString(k) }

// Get loads one document of a shop by key. The shop_id condition is explicit
// (admin paths run without tenant context).
func Get(ctx context.Context, c *ent.Client, shopID int, key string) (*ent.ContentDocument, error) {
	d, err := c.ContentDocument.Query().
		Where(entdoc.ShopIDEQ(shopID), entdoc.KeyEQ(key)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return d, nil
}

func lock(ctx context.Context, c *ent.Client, shopID int, key string) (*ent.ContentDocument, error) {
	d, err := c.ContentDocument.Query().
		Where(entdoc.ShopIDEQ(shopID), entdoc.KeyEQ(key)).ForUpdate().Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return d, nil
}

// SaveInput is one draft save. Content must already be validated and
// sanitized.
type SaveInput struct {
	ShopID  int
	Key     string
	TypeKey string
	Title   string
	Draft   json.RawMessage
	Meta    json.RawMessage
	// ExpectedRev is the optimistic lock for an update: the save applies only
	// when the stored draft_rev equals it. Nil = unconditional.
	ExpectedRev *int64
	// CreateOnly makes the save fail with ErrExists when the key exists.
	CreateOnly     bool
	SchemaVersion  int
	NeedsAttention bool
	Declared       bool
	Actor          int
}

// SaveDraft creates or updates the draft of a document and writes the legacy
// side (the pages row; the global document's draft is not in
// shops.content_json, which is the published copy). created reports whether
// the row was created.
func SaveDraft(ctx context.Context, c *ent.Client, in SaveInput) (doc *ent.ContentDocument, created bool, err error) {
	if len(in.Meta) == 0 {
		in.Meta = json.RawMessage("{}")
	}
	if len(in.Draft) == 0 {
		in.Draft = json.RawMessage("{}")
	}
	existing, err := lock(ctx, c, in.ShopID, in.Key)
	switch {
	case errors.Is(err, ErrNotFound):
		if in.ExpectedRev != nil {
			return nil, false, ErrNotFound
		}
		cr := c.ContentDocument.Create().
			SetShopID(in.ShopID).SetKey(in.Key).SetTypeKey(in.TypeKey).SetTitle(in.Title).
			SetDraftJSON(in.Draft).SetMeta(in.Meta).
			SetSchemaVersion(in.SchemaVersion).SetNeedsAttention(in.NeedsAttention).SetDeclared(in.Declared)
		if in.Actor > 0 {
			cr.SetUpdatedByUserID(in.Actor)
		}
		doc, err = cr.Save(ctx)
		if err != nil {
			if ent.IsConstraintError(err) {
				return nil, false, ErrExists
			}
			return nil, false, err
		}
		created = true
	case err != nil:
		return nil, false, err
	default:
		if in.CreateOnly {
			return nil, false, ErrExists
		}
		if in.ExpectedRev != nil && *in.ExpectedRev != existing.DraftRev {
			return nil, false, ErrConflict
		}
		up := existing.Update().
			SetTypeKey(in.TypeKey).SetDraftJSON(in.Draft).SetMeta(in.Meta).
			SetSchemaVersion(in.SchemaVersion).SetNeedsAttention(in.NeedsAttention).
			AddDraftRev(1)
		if in.Title != "" || existing.Title == "" {
			up.SetTitle(in.Title)
		}
		if in.Actor > 0 {
			up.SetUpdatedByUserID(in.Actor)
		}
		doc, err = up.Save(ctx)
		if err != nil {
			return nil, false, err
		}
	}
	if in.Key != GlobalKey {
		if err := writeBackDraft(ctx, c, doc); err != nil {
			return nil, false, err
		}
	}
	return doc, created, nil
}

// Publish copies the (already validated) draft to the published side, writes
// the publish revision, prunes history and updates the legacy side.
// expectedRev, when non-nil, must equal draft_rev (ErrConflict otherwise).
func Publish(ctx context.Context, c *ent.Client, shopID int, key string, expectedRev *int64, schemaVersion, actor int) (*ent.ContentDocument, error) {
	d, err := lock(ctx, c, shopID, key)
	if err != nil {
		return nil, err
	}
	if expectedRev != nil && *expectedRev != d.DraftRev {
		return nil, ErrConflict
	}
	up := d.Update().
		SetPublishedJSON(d.DraftJSON).SetStatus(1).
		SetPublishedRev(d.DraftRev).SetPublishedSchemaVersion(schemaVersion).
		SetPublishedAt(time.Now()).SetNeedsAttention(false)
	if actor > 0 {
		up.SetUpdatedByUserID(actor)
	}
	d, err = up.Save(ctx)
	if err != nil {
		return nil, err
	}
	if err := addRevision(ctx, c, d, KindPublish, d.PublishedJSON, nil, schemaVersion, actor); err != nil {
		return nil, err
	}
	if err := writeBackPublished(ctx, c, d); err != nil {
		return nil, err
	}
	return d, nil
}

// Unpublish takes a document offline. published_json is kept (as the legacy
// page does) but is never served while status is 0.
func Unpublish(ctx context.Context, c *ent.Client, shopID int, key string, actor int) (*ent.ContentDocument, error) {
	d, err := lock(ctx, c, shopID, key)
	if err != nil {
		return nil, err
	}
	up := d.Update().SetStatus(0)
	if actor > 0 {
		up.SetUpdatedByUserID(actor)
	}
	d, err = up.Save(ctx)
	if err != nil {
		return nil, err
	}
	if key != GlobalKey {
		if _, err := c.Page.Update().
			Where(entpage.ShopIDEQ(shopID), entpage.SlugEQ(key)).SetStatus(0).Save(ctx); err != nil {
			return nil, fmt.Errorf("contentstore: write back unpublish: %w", err)
		}
	}
	return d, nil
}

// Delete removes a document, its revisions and its legacy page row.
func Delete(ctx context.Context, c *ent.Client, shopID int, key string) (*ent.ContentDocument, error) {
	d, err := lock(ctx, c, shopID, key)
	if err != nil {
		return nil, err
	}
	if _, err := c.ContentRevision.Delete().
		Where(entrev.ShopIDEQ(shopID), entrev.DocumentIDEQ(d.ID)).Exec(ctx); err != nil {
		return nil, err
	}
	if err := c.ContentDocument.DeleteOne(d).Exec(ctx); err != nil {
		return nil, err
	}
	if key != GlobalKey {
		if _, err := c.Page.Delete().Where(entpage.ShopIDEQ(shopID), entpage.SlugEQ(key)).Exec(ctx); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// ── revisions ─────────────────────────────────────────────────────────────

// addRevision appends a revision (rev_no = max+1, serialized by the document
// row lock the caller holds) and prunes the history of that kind.
func addRevision(ctx context.Context, c *ent.Client, d *ent.ContentDocument, kind string, body, published json.RawMessage, schemaVersion, actor int) error {
	last, err := c.ContentRevision.Query().
		Where(entrev.ShopIDEQ(d.ShopID), entrev.DocumentIDEQ(d.ID)).
		Order(entrev.ByRevNo(sql.OrderDesc())).First(ctx)
	next := 1
	if err == nil {
		next = last.RevNo + 1
	} else if !ent.IsNotFound(err) {
		return err
	}
	cr := c.ContentRevision.Create().
		SetShopID(d.ShopID).SetDocumentID(d.ID).SetRevNo(next).SetKind(kind).
		SetJSON(body).SetTitle(d.Title).SetMeta(d.Meta).SetSchemaVersion(schemaVersion)
	if len(published) > 0 {
		cr.SetPublishedJSON(published)
	}
	if actor > 0 {
		cr.SetActorUserID(actor)
	}
	if _, err := cr.Save(ctx); err != nil {
		return err
	}
	keep := MaxPublishRevisions
	if kind == KindSchemaSnapshot {
		keep = MaxSnapshotsPerDocument
	}
	return prune(ctx, c, d, kind, keep)
}

func prune(ctx context.Context, c *ent.Client, d *ent.ContentDocument, kind string, keep int) error {
	old, err := c.ContentRevision.Query().
		Where(entrev.ShopIDEQ(d.ShopID), entrev.DocumentIDEQ(d.ID), entrev.KindEQ(kind)).
		Order(entrev.ByRevNo(sql.OrderDesc())).
		Offset(keep).IDs(ctx)
	if err != nil {
		return err
	}
	if len(old) == 0 {
		return nil
	}
	_, err = c.ContentRevision.Delete().Where(entrev.IDIn(old...)).Exec(ctx)
	return err
}

// AddSchemaSnapshot records the content a schema change put at risk (design
// D4): the draft and, when different, the published copy.
func AddSchemaSnapshot(ctx context.Context, c *ent.Client, shopID int, key string, schemaVersion, actor int) error {
	d, err := lock(ctx, c, shopID, key)
	if err != nil {
		return err
	}
	var pub json.RawMessage
	if len(d.PublishedJSON) > 0 && string(d.PublishedJSON) != string(d.DraftJSON) {
		pub = d.PublishedJSON
	}
	return addRevision(ctx, c, d, KindSchemaSnapshot, d.DraftJSON, pub, schemaVersion, actor)
}

// SetFlags updates needs_attention and declared of a document (schema
// registration) and mirrors needs_attention to the legacy page row.
func SetFlags(ctx context.Context, c *ent.Client, shopID int, key string, needsAttention, declared bool) error {
	if _, err := c.ContentDocument.Update().
		Where(entdoc.ShopIDEQ(shopID), entdoc.KeyEQ(key)).
		SetNeedsAttention(needsAttention).SetDeclared(declared).Save(ctx); err != nil {
		return err
	}
	if key != GlobalKey {
		_, err := c.Page.Update().
			Where(entpage.ShopIDEQ(shopID), entpage.SlugEQ(key)).
			SetNeedsAttention(needsAttention).Save(ctx)
		return err
	}
	return nil
}

// ── legacy write-back ─────────────────────────────────────────────────────

func writeBackDraft(ctx context.Context, c *ent.Client, d *ent.ContentDocument) error {
	p, err := c.Page.Query().Where(entpage.ShopIDEQ(d.ShopID), entpage.SlugEQ(d.Key)).Only(ctx)
	if ent.IsNotFound(err) {
		_, err = c.Page.Create().
			SetShopID(d.ShopID).SetTypeKey(d.TypeKey).SetTitle(d.Title).SetSlug(d.Key).
			SetStatus(0).SetContentJSON(d.DraftJSON).SetMeta(d.Meta).SetNeedsAttention(d.NeedsAttention).
			Save(ctx)
		return err
	}
	if err != nil {
		return err
	}
	_, err = p.Update().
		SetTypeKey(d.TypeKey).SetTitle(d.Title).SetContentJSON(d.DraftJSON).SetMeta(d.Meta).
		SetNeedsAttention(d.NeedsAttention).Save(ctx)
	return err
}

func writeBackPublished(ctx context.Context, c *ent.Client, d *ent.ContentDocument) error {
	if d.Key == GlobalKey {
		_, err := c.Shop.Update().Where(entshop.IDEQ(d.ShopID)).SetContentJSON(d.PublishedJSON).Save(ctx)
		return err
	}
	if err := writeBackDraft(ctx, c, d); err != nil {
		return err
	}
	_, err := c.Page.Update().
		Where(entpage.ShopIDEQ(d.ShopID), entpage.SlugEQ(d.Key)).
		SetPublishedJSON(d.PublishedJSON).SetStatus(1).SetNeedsAttention(false).Save(ctx)
	return err
}

// ── legacy -> documents (the compatibility endpoints' writes) ─────────────

// PageSync describes a write the legacy page service just made to a pages row.
type PageSync struct {
	// OldSlug is the page's slug before the write ("" when just created).
	OldSlug       string
	Page          *ent.Page
	DraftChanged  bool
	Published     bool
	// PublishedRewritten: the published copy was rewritten in place (the
	// sanitize-content backfill) - copied to the document without a revision.
	PublishedRewritten bool
	SchemaVersion int
}

// SyncPage makes the document of a legacy page equal to the page row: the
// document is created from the page when missing (self-healing for rows that
// predate the backfill), renamed when the slug changed, and a publish writes
// a revision. Call it in the same transaction as the legacy write.
func SyncPage(ctx context.Context, c *ent.Client, s PageSync) error {
	p := s.Page
	lookup := p.Slug
	if s.OldSlug != "" {
		lookup = s.OldSlug
	}
	d, err := lock(ctx, c, p.ShopID, lookup)
	if errors.Is(err, ErrNotFound) && s.OldSlug != "" && s.OldSlug != p.Slug {
		d, err = lock(ctx, c, p.ShopID, p.Slug)
	}
	if errors.Is(err, ErrNotFound) {
		cr := c.ContentDocument.Create().
			SetShopID(p.ShopID).SetKey(p.Slug).SetTypeKey(p.TypeKey).SetTitle(p.Title).
			SetStatus(p.Status).SetDraftJSON(p.ContentJSON).SetMeta(p.Meta).
			SetSchemaVersion(s.SchemaVersion).SetNeedsAttention(p.NeedsAttention)
		if len(p.PublishedJSON) > 0 {
			cr.SetPublishedJSON(p.PublishedJSON).SetPublishedRev(1).SetPublishedSchemaVersion(s.SchemaVersion)
			if p.Status == 1 {
				cr.SetPublishedAt(time.Now())
			}
		}
		d, err = cr.Save(ctx)
		if err != nil {
			return fmt.Errorf("contentstore: create document for page %q: %w", p.Slug, err)
		}
		if len(p.PublishedJSON) > 0 && s.Published {
			return addRevision(ctx, c, d, KindPublish, p.PublishedJSON, nil, s.SchemaVersion, 0)
		}
		return nil
	}
	if err != nil {
		return err
	}
	up := d.Update().
		SetKey(p.Slug).SetTypeKey(p.TypeKey).SetTitle(p.Title).SetStatus(p.Status).
		SetMeta(p.Meta).SetNeedsAttention(p.NeedsAttention)
	rev := d.DraftRev
	if s.DraftChanged {
		up.SetDraftJSON(p.ContentJSON).SetSchemaVersion(s.SchemaVersion).AddDraftRev(1)
		rev++
	}
	if s.PublishedRewritten && !s.Published {
		up.SetPublishedJSON(p.PublishedJSON)
	}
	if s.Published {
		up.SetPublishedJSON(p.PublishedJSON).SetPublishedRev(rev).
			SetPublishedSchemaVersion(s.SchemaVersion).SetPublishedAt(time.Now())
	}
	d, err = up.Save(ctx)
	if err != nil {
		return fmt.Errorf("contentstore: sync page %q: %w", p.Slug, err)
	}
	if s.Published {
		return addRevision(ctx, c, d, KindPublish, p.PublishedJSON, nil, s.SchemaVersion, 0)
	}
	return nil
}

// SyncPageDelete removes the document of a deleted legacy page.
func SyncPageDelete(ctx context.Context, c *ent.Client, shopID int, slug string) error {
	d, err := lock(ctx, c, shopID, slug)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := c.ContentRevision.Delete().
		Where(entrev.ShopIDEQ(shopID), entrev.DocumentIDEQ(d.ID)).Exec(ctx); err != nil {
		return err
	}
	return c.ContentDocument.DeleteOne(d).Exec(ctx)
}

// SyncGlobal records a legacy write of shops.content_json: the global document
// takes the content as both draft and published copy (the legacy semantic is
// "live at once"), and a publish revision is written. The document is created
// when missing.
func SyncGlobal(ctx context.Context, c *ent.Client, shopID int, content json.RawMessage, schemaVersion int) error {
	d, err := lock(ctx, c, shopID, GlobalKey)
	if errors.Is(err, ErrNotFound) {
		d, err = c.ContentDocument.Create().
			SetShopID(shopID).SetKey(GlobalKey).SetTypeKey(GlobalKey).SetTitle("Global").
			SetStatus(1).SetDraftJSON(content).SetPublishedJSON(content).
			SetPublishedRev(1).SetSchemaVersion(schemaVersion).SetPublishedSchemaVersion(schemaVersion).
			SetPublishedAt(time.Now()).Save(ctx)
		if err != nil {
			return fmt.Errorf("contentstore: create global document: %w", err)
		}
		return addRevision(ctx, c, d, KindPublish, content, nil, schemaVersion, 0)
	}
	if err != nil {
		return err
	}
	d, err = d.Update().
		SetDraftJSON(content).AddDraftRev(1).SetStatus(1).SetPublishedJSON(content).
		SetPublishedRev(d.DraftRev + 1).SetSchemaVersion(schemaVersion).SetPublishedSchemaVersion(schemaVersion).
		SetPublishedAt(time.Now()).SetNeedsAttention(false).Save(ctx)
	if err != nil {
		return err
	}
	return addRevision(ctx, c, d, KindPublish, content, nil, schemaVersion, 0)
}

// EnsureGlobal creates the global document of a shop from shops.content_json
// when it does not exist yet (shop creation).
func EnsureGlobal(ctx context.Context, c *ent.Client, shopID int) error {
	ok, err := c.ContentDocument.Query().Where(entdoc.ShopIDEQ(shopID), entdoc.KeyEQ(GlobalKey)).Exist(ctx)
	if err != nil || ok {
		return err
	}
	shop, err := c.Shop.Get(ctx, shopID)
	if err != nil {
		return err
	}
	return SyncGlobal(ctx, c, shopID, shop.ContentJSON, 0)
}

// AuditEntry is one audit row. Detail carries ids and counts only: never
// content, never URLs, never e-mail addresses (design D9).
type AuditEntry struct {
	ShopID int
	Actor  int
	Action string
	Key    string
	Detail map[string]any
}

// Audit writes one content_audit_log row.
func Audit(ctx context.Context, c *ent.Client, e AuditEntry) error {
	detail, err := json.Marshal(e.Detail)
	if err != nil || e.Detail == nil {
		detail = []byte("{}")
	}
	cr := c.ContentAuditLog.Create().
		SetShopID(e.ShopID).SetAction(e.Action).SetDocKey(e.Key).SetDetail(detail)
	if e.Actor > 0 {
		cr.SetActorUserID(e.Actor)
	}
	_, err = cr.Save(ctx)
	return err
}

// EnsureDocuments creates the documents that do not exist yet for a shop's
// legacy pages and its global content. After the data migration this finds
// nothing to do; it keeps rows that were written around the compatibility
// layer (tests, manual SQL) from being invisible to the new API.
func EnsureDocuments(ctx context.Context, c *ent.Client, shopID int) error {
	pages, err := c.Page.Query().Where(entpage.ShopIDEQ(shopID)).Order(entpage.ByID()).All(ctx)
	if err != nil {
		return err
	}
	keys, err := c.ContentDocument.Query().Where(entdoc.ShopIDEQ(shopID)).Select(entdoc.FieldKey).Strings(ctx)
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(keys))
	for _, k := range keys {
		have[k] = true
	}
	for _, p := range pages {
		if have[p.Slug] || !ValidKey(p.Slug) || p.Slug == GlobalKey {
			continue
		}
		if err := SyncPage(ctx, c, PageSync{Page: p, Published: p.Status == 1}); err != nil {
			return err
		}
	}
	return ensureGlobalCurrent(ctx, c, shopID)
}

// ensureGlobalCurrent creates the global document when missing and re-syncs it
// when shops.content_json was written around the compatibility layer (the
// column is the live projection of the global document's published copy, so a
// difference can only come from a writer that bypassed the layer: a direct SQL
// edit or an old tool). The column is what was live, so it wins.
func ensureGlobalCurrent(ctx context.Context, c *ent.Client, shopID int) error {
	d, err := c.ContentDocument.Query().Where(entdoc.ShopIDEQ(shopID), entdoc.KeyEQ(GlobalKey)).Only(ctx)
	if ent.IsNotFound(err) {
		return EnsureGlobal(ctx, c, shopID)
	}
	if err != nil {
		return err
	}
	shop, err := c.Shop.Get(ctx, shopID)
	if err != nil {
		return err
	}
	if sameJSON(d.PublishedJSON, shop.ContentJSON) {
		return nil
	}
	return SyncGlobal(ctx, c, shopID, shop.ContentJSON, d.PublishedSchemaVersion)
}

// SameJSON reports whether two JSON documents are equal as values.
func SameJSON(a, b json.RawMessage) bool { return sameJSON(a, b) }

func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xa) == string(yb)
}
