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

	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/contentschema"
	"ksdevworks/ecommerce/api/internal/contentstore"
	"ksdevworks/ecommerce/api/internal/ent"
	entdoc "ksdevworks/ecommerce/api/internal/ent/contentdocument"
	entrev "ksdevworks/ecommerce/api/internal/ent/contentrevision"
	"ksdevworks/ecommerce/api/internal/events"
)

// Limits of design D1.
const (
	MaxDocumentBytes      = 128 << 10
	MaxMetaBytes          = 4 << 10
	MaxDocumentsPerShop   = 100
	MaxPublishedPerShop   = 2 << 20
	maxTitleRunes         = 150
	maxListRevisions      = 200
	DocumentSizeErrorText = "document is larger than 128 KiB"
)

// Gate is the write-side validation and sanitization gate; *cms.Service is the
// implementation, so the legacy page endpoints and the document API share one.
type Gate interface {
	ValidateAndSanitize(schema, payload json.RawMessage) (json.RawMessage, error)
}

// ConflictError is the optimistic-lock failure (handler -> 412).
type ConflictError struct{ CurrentRev int64 }

func (e *ConflictError) Error() string { return "the document changed since you loaded it" }

// StateError is an operation the document's state or declaration forbids
// (handler -> 409).
type StateError struct{ Message string }

func (e *StateError) Error() string { return e.Message }

// ErrDocExists is a create-only save on a taken key (handler -> 412).
var ErrDocExists = errors.New("content: document already exists")

// Service is the document API of the content store: draft saves with an
// optimistic lock, publish and unpublish, revisions and restore, delete.
// Permissions are the HTTP layer's job; this layer validates content, enforces
// the declared keys and the size limits, and writes the audit trail.
type Service struct {
	Client     *ent.Client
	Dispatcher *events.Dispatcher
	Gate       Gate
	Log        *slog.Logger
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) schemas() *contentschema.Store { return &contentschema.Store{Client: s.Client} }

func (s *Service) publish(ctx context.Context, e events.Event) {
	if s.Dispatcher != nil {
		s.Dispatcher.Publish(ctx, e)
	}
}

// activeSchema loads the shop's active version or the actionable 422.
func (s *Service) activeSchema(ctx context.Context, shopID int) (*contentschema.Version, error) {
	if _, err := s.Client.Shop.Get(ctx, shopID); err != nil {
		return nil, ErrNotFound
	}
	v, err := s.schemas().Active(ctx, shopID)
	if err != nil {
		if errors.Is(err, contentschema.ErrNoSchema) {
			return nil, cms.NoSchemaError(shopID)
		}
		return nil, err
	}
	return v, nil
}

// typeSchema returns the JSON Schema of a type of the active version.
func typeSchema(v *contentschema.Version, typeKey string) (json.RawMessage, bool) {
	if typeKey == contentstore.GlobalKey {
		return v.Doc.Global, len(v.Doc.Global) > 0
	}
	sch, ok := v.Doc.Types[typeKey]
	return sch, ok
}

// resolveType decides the type of a document that is being created, and
// enforces that the key is declared (format 2) (design D5).
func resolveType(v *contentschema.Version, key, requested string) (string, error) {
	if key == contentstore.GlobalKey {
		return contentstore.GlobalKey, nil
	}
	if v.Doc.Format == 2 {
		if d, ok := v.Doc.Documents[key]; ok {
			return d.Type, nil
		}
		for prefix, c := range v.Doc.Collections {
			if strings.HasPrefix(key, prefix) && len(key) > len(prefix) {
				return c.Type, nil
			}
		}
		return "", &cms.ValidationError{
			Message: "key is not declared by the shop's content schema",
			Details: []cms.Detail{{Pointer: "/key", Message: "declare it in documents, or use a collection prefix"}},
		}
	}
	if requested == "" {
		return "", &cms.ValidationError{
			Message: "type_key is required",
			Details: []cms.Detail{{Pointer: "/type_key", Message: "required when creating a document"}},
		}
	}
	if requested == contentstore.GlobalKey {
		return "", &cms.ValidationError{
			Message: "type_key is not usable here",
			Details: []cms.Detail{{Pointer: "/type_key", Message: "global is the type of the document with key global"}},
		}
	}
	return requested, nil
}

func declaredBy(v *contentschema.Version, key string) bool {
	if key == contentstore.GlobalKey || v.Doc.Format != 2 {
		return true
	}
	if _, ok := v.Doc.Documents[key]; ok {
		return true
	}
	for prefix := range v.Doc.Collections {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func rawIsObject(b json.RawMessage) bool {
	t := strings.TrimSpace(string(b))
	return strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}")
}

func (s *Service) tx(ctx context.Context, fn func(c *ent.Client) error) error {
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("content: begin tx: %w", err)
	}
	if err := fn(tx.Client()); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("content: commit: %w", err)
	}
	return nil
}

// ── reads ─────────────────────────────────────────────────────────────────

// List returns every document of the shop ordered by key.
func (s *Service) List(ctx context.Context, shopID int) ([]*ent.ContentDocument, error) {
	if _, err := s.Client.Shop.Get(ctx, shopID); err != nil {
		return nil, ErrNotFound
	}
	if err := contentstore.EnsureDocuments(ctx, s.Client, shopID); err != nil {
		return nil, err
	}
	return s.Client.ContentDocument.Query().
		Where(entdoc.ShopIDEQ(shopID)).Order(entdoc.ByKey()).All(ctx)
}

// Detail is one document with the schema node that describes it.
type Detail struct {
	Doc *ent.ContentDocument
	// SchemaNode is the JSON Schema of the document's type in the active
	// schema, nil when the type is not in it (orphaned type).
	SchemaNode json.RawMessage
	TypeTitle  string
}

// Get returns one document and its schema node.
func (s *Service) Get(ctx context.Context, shopID int, key string) (*Detail, error) {
	if _, err := s.Client.Shop.Get(ctx, shopID); err != nil {
		return nil, ErrNotFound
	}
	if err := contentstore.EnsureDocuments(ctx, s.Client, shopID); err != nil {
		return nil, err
	}
	d, err := contentstore.Get(ctx, s.Client, shopID, key)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	det := &Detail{Doc: d}
	if v, err := s.schemas().Active(ctx, shopID); err == nil {
		if sch, ok := typeSchema(v, d.TypeKey); ok {
			det.SchemaNode = sch
			det.TypeTitle = v.Doc.Titles[d.TypeKey]
		}
	}
	return det, nil
}

func mapStoreErr(err error) error {
	switch {
	case errors.Is(err, contentstore.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, contentstore.ErrExists):
		return ErrDocExists
	}
	return err
}

// ── save ──────────────────────────────────────────────────────────────────

// SaveRequest is one draft save.
type SaveRequest struct {
	TypeKey string
	Title   *string
	Content json.RawMessage
	Meta    json.RawMessage
	// IfMatch is the draft_rev the caller loaded (update).
	IfMatch *int64
	// IfNoneMatch asks for a create: it fails when the key exists.
	IfNoneMatch bool
}

// SaveResult is the stored document and whether it was created.
type SaveResult struct {
	Doc     *ent.ContentDocument
	Created bool
}

// SaveDraft creates or updates a document's draft. The caller sent either
// IfMatch or IfNoneMatch (the handler answers 428 otherwise).
func (s *Service) SaveDraft(ctx context.Context, shopID int, key string, req SaveRequest, actor int) (*SaveResult, error) {
	if !contentstore.ValidKey(key) {
		return nil, &cms.ValidationError{Message: "invalid key", Details: []cms.Detail{{Pointer: "/key", Message: "must match ^[a-z0-9][a-z0-9-]{0,62}$"}}}
	}
	v, err := s.activeSchema(ctx, shopID)
	if err != nil {
		return nil, err
	}
	if err := contentstore.EnsureDocuments(ctx, s.Client, shopID); err != nil {
		return nil, err
	}
	existing, err := contentstore.Get(ctx, s.Client, shopID, key)
	if err != nil && !errors.Is(err, contentstore.ErrNotFound) {
		return nil, err
	}
	exists := err == nil

	var typeKey string
	switch {
	case exists:
		if req.IfNoneMatch {
			return nil, ErrDocExists
		}
		typeKey = existing.TypeKey
	default:
		if req.IfMatch != nil {
			return nil, ErrNotFound
		}
		typeKey, err = resolveType(v, key, req.TypeKey)
		if err != nil {
			return nil, err
		}
		if err := s.checkCreateQuota(ctx, shopID, v, key); err != nil {
			return nil, err
		}
	}
	schema, ok := typeSchema(v, typeKey)
	if !ok {
		return nil, &cms.ValidationError{
			Message: "the document's type is not in the shop's content schema",
			Details: []cms.Detail{{Pointer: "/type_key", Message: "unsupported type"}},
		}
	}
	content := req.Content
	if len(content) == 0 {
		content = json.RawMessage("{}")
	}
	if len(content) > MaxDocumentBytes {
		return nil, &cms.ValidationError{Message: DocumentSizeErrorText, Details: []cms.Detail{{Pointer: "/content", Message: DocumentSizeErrorText}}}
	}
	if len(req.Meta) > MaxMetaBytes {
		return nil, &cms.ValidationError{Message: "meta is larger than 4 KiB", Details: []cms.Detail{{Pointer: "/meta", Message: "too large"}}}
	}
	if len(req.Meta) > 0 && !rawIsObject(req.Meta) {
		return nil, &cms.ValidationError{Message: "meta must be an object", Details: []cms.Detail{{Pointer: "/meta", Message: "must be an object"}}}
	}
	if req.Title != nil && len([]rune(*req.Title)) > maxTitleRunes {
		return nil, &cms.ValidationError{Message: "title is too long", Details: []cms.Detail{{Pointer: "/title", Message: "at most 150 characters"}}}
	}
	clean, err := s.Gate.ValidateAndSanitize(schema, content)
	if err != nil {
		return nil, err
	}
	if len(clean) > MaxDocumentBytes {
		return nil, &cms.ValidationError{Message: DocumentSizeErrorText, Details: []cms.Detail{{Pointer: "/content", Message: DocumentSizeErrorText}}}
	}

	title := ""
	if req.Title != nil {
		title = *req.Title
	} else if !exists {
		title = v.Doc.Titles[typeKey]
		if title == "" {
			title = key
		}
	}
	// A draft that passes the active schema leaves needs-attention unless the
	// published copy is still invalid (publishing the fixed draft clears that).
	attention := false
	if exists && len(existing.PublishedJSON) > 0 {
		attention = cms.ValidatePayload(schema, existing.PublishedJSON) != nil
	}

	var res SaveResult
	err = s.tx(ctx, func(c *ent.Client) error {
		doc, created, serr := contentstore.SaveDraft(ctx, c, contentstore.SaveInput{
			ShopID: shopID, Key: key, TypeKey: typeKey, Title: title, Draft: clean, Meta: req.Meta,
			ExpectedRev: req.IfMatch, CreateOnly: req.IfNoneMatch,
			SchemaVersion: v.Version, NeedsAttention: attention, Declared: declaredBy(v, key), Actor: actor,
		})
		if serr != nil {
			return serr
		}
		res = SaveResult{Doc: doc, Created: created}
		return contentstore.Audit(ctx, c, contentstore.AuditEntry{
			ShopID: shopID, Actor: actor, Action: "doc.save", Key: key,
			Detail: map[string]any{"rev": doc.DraftRev, "schema_version": v.Version, "created": created},
		})
	})
	if err != nil {
		switch {
		case errors.Is(err, contentstore.ErrConflict):
			cur, _ := contentstore.Get(ctx, s.Client, shopID, key)
			ce := &ConflictError{}
			if cur != nil {
				ce.CurrentRev = cur.DraftRev
			}
			return nil, ce
		default:
			return nil, mapStoreErr(err)
		}
	}
	s.log().Info("content audit", "action", "doc.save", "shop_id", shopID, "actor_user_id", actor,
		"doc_key", key, "rev", res.Doc.DraftRev, "schema_version", v.Version)
	return &res, nil
}

func (s *Service) checkCreateQuota(ctx context.Context, shopID int, v *contentschema.Version, key string) error {
	n, err := s.Client.ContentDocument.Query().Where(entdoc.ShopIDEQ(shopID)).Count(ctx)
	if err != nil {
		return err
	}
	if n >= MaxDocumentsPerShop {
		return &cms.ValidationError{Message: "the shop already has the maximum of 100 documents"}
	}
	if v.Doc.Format == 2 {
		if _, fixed := v.Doc.Documents[key]; !fixed {
			for prefix, c := range v.Doc.Collections {
				if strings.HasPrefix(key, prefix) && c.Max > 0 {
					cnt, err := s.Client.ContentDocument.Query().
						Where(entdoc.ShopIDEQ(shopID), entdoc.KeyHasPrefix(prefix)).Count(ctx)
					if err != nil {
						return err
					}
					if cnt >= c.Max {
						return &cms.ValidationError{Message: fmt.Sprintf("collection %q is limited to %d documents", prefix, c.Max)}
					}
				}
			}
		}
	}
	return nil
}

// ── publish / unpublish / delete ─────────────────────────────────────────

// Publish validates the draft against the active schema and publishes it.
// expectedRev, when non-nil, is the draft_rev the caller reviewed.
func (s *Service) Publish(ctx context.Context, shopID int, key string, expectedRev *int64, actor int) (*ent.ContentDocument, error) {
	return s.publishDoc(ctx, shopID, key, expectedRev, actor, 0)
}

// PublishScheduled is the scheduler's publish: exactly Publish (same
// validation, revision, audit row, events and therefore cache invalidation and
// publish hook), with the schedule id recorded in the audit detail.
func (s *Service) PublishScheduled(ctx context.Context, shopID int, key string, expectedRev int64, actor, scheduleID int) (*ent.ContentDocument, error) {
	return s.publishDoc(ctx, shopID, key, &expectedRev, actor, scheduleID)
}

func (s *Service) publishDoc(ctx context.Context, shopID int, key string, expectedRev *int64, actor, scheduleID int) (*ent.ContentDocument, error) {
	v, err := s.activeSchema(ctx, shopID)
	if err != nil {
		return nil, err
	}
	if err := contentstore.EnsureDocuments(ctx, s.Client, shopID); err != nil {
		return nil, err
	}
	d, err := contentstore.Get(ctx, s.Client, shopID, key)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	schema, ok := typeSchema(v, d.TypeKey)
	if !ok {
		return nil, &cms.ValidationError{Message: "the document's type is not in the shop's content schema"}
	}
	if err := cms.ValidatePayload(schema, d.DraftJSON); err != nil {
		return nil, err
	}
	if err := s.checkPublishedBudget(ctx, shopID, d); err != nil {
		return nil, err
	}
	var out *ent.ContentDocument
	err = s.tx(ctx, func(c *ent.Client) error {
		var perr error
		out, perr = contentstore.Publish(ctx, c, shopID, key, expectedRev, v.Version, actor)
		if perr != nil {
			return perr
		}
		detail := map[string]any{"rev": out.DraftRev, "schema_version": v.Version}
		if scheduleID > 0 {
			detail["scheduled"] = true
			detail["schedule_id"] = scheduleID
		}
		return contentstore.Audit(ctx, c, contentstore.AuditEntry{
			ShopID: shopID, Actor: actor, Action: "doc.publish", Key: key, Detail: detail,
		})
	})
	if err != nil {
		return nil, s.mapLockErr(ctx, shopID, key, err)
	}
	s.afterPublish(ctx, shopID, key, out.DraftRev)
	s.log().Info("content audit", "action", "doc.publish", "shop_id", shopID, "actor_user_id", actor,
		"doc_key", key, "rev", out.DraftRev, "schema_version", v.Version)
	return out, nil
}

// afterPublish fires the events: the new content namespace, plus the legacy
// ones that keep the compatibility render cache correct.
func (s *Service) afterPublish(ctx context.Context, shopID int, key string, rev int64) {
	s.publish(ctx, events.ContentPublished{ShopID: shopID, Key: key, Rev: rev})
	if key == contentstore.GlobalKey {
		s.publish(ctx, events.ShopContentUpdated{ShopID: shopID})
	} else {
		s.publish(ctx, events.PagePublished{ShopID: shopID, Slug: key})
	}
}

func (s *Service) mapLockErr(ctx context.Context, shopID int, key string, err error) error {
	if errors.Is(err, contentstore.ErrConflict) {
		ce := &ConflictError{}
		if cur, gerr := contentstore.Get(ctx, s.Client, shopID, key); gerr == nil {
			ce.CurrentRev = cur.DraftRev
		}
		return ce
	}
	return mapStoreErr(err)
}

// checkPublishedBudget keeps the shop's published content under 2 MiB, which
// is what bounds the bulk read.
func (s *Service) checkPublishedBudget(ctx context.Context, shopID int, publishing *ent.ContentDocument) error {
	docs, err := s.Client.ContentDocument.Query().
		Where(entdoc.ShopIDEQ(shopID), entdoc.StatusEQ(1), entdoc.KeyNEQ(publishing.Key)).
		Select(entdoc.FieldPublishedJSON).All(ctx)
	if err != nil {
		return err
	}
	total := len(publishing.DraftJSON)
	for _, d := range docs {
		total += len(d.PublishedJSON)
	}
	if total > MaxPublishedPerShop {
		return &cms.ValidationError{Message: "the shop's published content would exceed 2 MiB"}
	}
	return nil
}

// Unpublish takes a document offline.
func (s *Service) Unpublish(ctx context.Context, shopID int, key string, actor int) (*ent.ContentDocument, error) {
	if key == contentstore.GlobalKey {
		return nil, &StateError{Message: "the global document cannot be unpublished; replace its content instead"}
	}
	if err := contentstore.EnsureDocuments(ctx, s.Client, shopID); err != nil {
		return nil, err
	}
	var out *ent.ContentDocument
	err := s.tx(ctx, func(c *ent.Client) error {
		var uerr error
		out, uerr = contentstore.Unpublish(ctx, c, shopID, key, actor)
		if uerr != nil {
			return uerr
		}
		return contentstore.Audit(ctx, c, contentstore.AuditEntry{
			ShopID: shopID, Actor: actor, Action: "doc.unpublish", Key: key, Detail: map[string]any{"rev": out.DraftRev},
		})
	})
	if err != nil {
		return nil, mapStoreErr(err)
	}
	s.publish(ctx, events.ContentUnpublished{ShopID: shopID, Key: key})
	s.publish(ctx, events.PageUnpublished{ShopID: shopID, Slug: key})
	s.log().Info("content audit", "action", "doc.unpublish", "shop_id", shopID, "actor_user_id", actor, "doc_key", key)
	return out, nil
}

// Delete removes a document that the schema does not pin: a collection member
// or an orphan (declared = false). Declared fixed documents can only be emptied.
func (s *Service) Delete(ctx context.Context, shopID int, key string, actor int) error {
	if err := contentstore.EnsureDocuments(ctx, s.Client, shopID); err != nil {
		return err
	}
	d, err := contentstore.Get(ctx, s.Client, shopID, key)
	if err != nil {
		return mapStoreErr(err)
	}
	v, err := s.activeSchema(ctx, shopID)
	if err != nil {
		return err
	}
	if key == contentstore.GlobalKey {
		return &StateError{Message: "the global document cannot be deleted"}
	}
	if v.Doc.Format == 2 {
		if _, fixed := v.Doc.Documents[key]; fixed {
			return &StateError{Message: "a document declared in the schema's documents cannot be deleted; empty it instead"}
		}
	} else if key == "home" {
		return &StateError{Message: "the home document cannot be deleted"}
	}
	wasPublished := d.Status == 1
	err = s.tx(ctx, func(c *ent.Client) error {
		if _, derr := contentstore.Delete(ctx, c, shopID, key); derr != nil {
			return derr
		}
		return contentstore.Audit(ctx, c, contentstore.AuditEntry{
			ShopID: shopID, Actor: actor, Action: "doc.delete", Key: key, Detail: map[string]any{"was_published": wasPublished},
		})
	})
	if err != nil {
		return mapStoreErr(err)
	}
	s.publish(ctx, events.ContentUnpublished{ShopID: shopID, Key: key})
	if wasPublished {
		s.publish(ctx, events.PageUnpublished{ShopID: shopID, Slug: key})
	}
	s.log().Info("content audit", "action", "doc.delete", "shop_id", shopID, "actor_user_id", actor, "doc_key", key)
	return nil
}

// ── revisions ─────────────────────────────────────────────────────────────

// RevisionInfo is a revision without its content.
type RevisionInfo struct {
	RevNo         int
	Kind          string
	Title         string
	SchemaVersion int
	ActorUserID   *int
	CreatedAt     time.Time
}

// Revisions lists a document's history, newest first.
func (s *Service) Revisions(ctx context.Context, shopID int, key string) ([]RevisionInfo, error) {
	d, err := contentstore.Get(ctx, s.Client, shopID, key)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	rows, err := s.Client.ContentRevision.Query().
		Where(entrev.ShopIDEQ(shopID), entrev.DocumentIDEQ(d.ID)).
		Order(ent.Desc(entrev.FieldRevNo)).Limit(maxListRevisions).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RevisionInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, RevisionInfo{RevNo: r.RevNo, Kind: r.Kind, Title: r.Title, SchemaVersion: r.SchemaVersion, ActorUserID: r.ActorUserID, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// Revision returns one revision with its content.
func (s *Service) Revision(ctx context.Context, shopID int, key string, revNo int) (*ent.ContentRevision, error) {
	d, err := contentstore.Get(ctx, s.Client, shopID, key)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	r, err := s.Client.ContentRevision.Query().
		Where(entrev.ShopIDEQ(shopID), entrev.DocumentIDEQ(d.ID), entrev.RevNoEQ(revNo)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return r, nil
}

// RestoreResult is the draft after a restore.
type RestoreResult struct {
	Doc *ent.ContentDocument
	// Valid is false when the restored content does not pass the active schema
	// (a snapshot of a retired shape): the draft then needs attention and
	// cannot be published until it is fixed and saved.
	Valid bool
}

// Restore puts a revision's content back as the DRAFT. It never publishes: that
// is a separate action with its own permission and audit row.
func (s *Service) Restore(ctx context.Context, shopID int, key string, revNo int, ifMatch *int64, actor int) (*RestoreResult, error) {
	v, err := s.activeSchema(ctx, shopID)
	if err != nil {
		return nil, err
	}
	rev, err := s.Revision(ctx, shopID, key, revNo)
	if err != nil {
		return nil, err
	}
	d, err := contentstore.Get(ctx, s.Client, shopID, key)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	schema, ok := typeSchema(v, d.TypeKey)
	if !ok {
		return nil, &cms.ValidationError{Message: "the document's type is not in the shop's content schema"}
	}
	draft := rev.JSON
	valid := true
	if clean, serr := s.Gate.ValidateAndSanitize(schema, rev.JSON); serr == nil {
		draft = clean
	} else {
		var ve *cms.ValidationError
		if !errors.As(serr, &ve) {
			return nil, serr
		}
		valid = false
	}
	var out *ent.ContentDocument
	err = s.tx(ctx, func(c *ent.Client) error {
		var serr error
		out, _, serr = contentstore.SaveDraft(ctx, c, contentstore.SaveInput{
			ShopID: shopID, Key: key, TypeKey: d.TypeKey, Title: d.Title, Draft: draft, Meta: d.Meta,
			ExpectedRev: ifMatch, SchemaVersion: v.Version,
			NeedsAttention: !valid || len(d.PublishedJSON) > 0 && cms.ValidatePayload(schema, d.PublishedJSON) != nil,
			Declared:       d.Declared, Actor: actor,
		})
		if serr != nil {
			return serr
		}
		return contentstore.Audit(ctx, c, contentstore.AuditEntry{
			ShopID: shopID, Actor: actor, Action: "doc.restore", Key: key,
			Detail: map[string]any{"from_rev_no": revNo, "kind": rev.Kind, "rev": out.DraftRev, "valid": valid},
		})
	})
	if err != nil {
		return nil, s.mapLockErr(ctx, shopID, key, err)
	}
	s.log().Info("content audit", "action", "doc.restore", "shop_id", shopID, "actor_user_id", actor,
		"doc_key", key, "from_rev_no", revNo, "rev", out.DraftRev)
	return &RestoreResult{Doc: out, Valid: valid}, nil
}

// SortedKeys is a small helper for deterministic output in tests and docs.
func SortedKeys(docs []*ent.ContentDocument) []string {
	keys := make([]string, 0, len(docs))
	for _, d := range docs {
		keys = append(keys, d.Key)
	}
	sort.Strings(keys)
	return keys
}
