package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/contentschema"
	"ksdevworks/ecommerce/api/internal/contentstore"
	"ksdevworks/ecommerce/api/internal/ent"
	entdoc "ksdevworks/ecommerce/api/internal/ent/contentdocument"
)

// Reader assembles the PUBLIC read bodies of the content store (design D2): the
// published copy of a document, hydrated against the shop's active schema, as
// the exact bytes the endpoint serves (and the cache stores). It reads in a
// tenant context (the data-layer interceptor adds shop_id) and also states the
// shop id in every condition.
type Reader struct {
	Client *ent.Client
}

// ErrUnavailable covers every "no such published document" case: the key does
// not exist, exists only as a draft, is unpublished, or the shop has no
// schema. The endpoint answers all of them with the same bytes.
var ErrUnavailable = errors.New("content: not available")

// docBody is the single-document response shape.
type docBody struct {
	Key           string          `json:"key"`
	TypeKey       string          `json:"type_key"`
	Title         string          `json:"title"`
	SchemaVersion int             `json:"schema_version"`
	PublishedAt   *time.Time      `json:"published_at"`
	Content       json.RawMessage `json:"content"`
	Meta          json.RawMessage `json:"meta"`
}

// previewBody additionally says it is a draft.
type previewBody struct {
	docBody
	Status   int16 `json:"status"`
	DraftRev int64 `json:"draft_rev"`
	Preview  bool  `json:"preview"`
}

// hydrated returns payload hydrated by the type's schema in the active
// version. A document whose type left the schema is served as stored: its
// published content must not disappear because a schema changed (design D2).
func hydrated(v *contentschema.Version, typeKey string, payload json.RawMessage) (json.RawMessage, error) {
	if sch, ok := typeSchema(v, typeKey); ok {
		out, err := cms.HydrateJSON(sch, payload)
		if err != nil {
			return nil, fmt.Errorf("content: hydrate: %w", err)
		}
		return out, nil
	}
	if len(payload) == 0 {
		return json.RawMessage("{}"), nil
	}
	return payload, nil
}

func metaOrEmpty(m json.RawMessage) json.RawMessage {
	if len(m) == 0 {
		return json.RawMessage("{}")
	}
	return m
}

func (r *Reader) publishedBody(v *contentschema.Version, d *ent.ContentDocument) (docBody, error) {
	content, err := hydrated(v, d.TypeKey, d.PublishedJSON)
	if err != nil {
		return docBody{}, err
	}
	return docBody{
		Key: d.Key, TypeKey: d.TypeKey, Title: d.Title, SchemaVersion: d.PublishedSchemaVersion,
		PublishedAt: d.PublishedAt, Content: content, Meta: metaOrEmpty(d.Meta),
	}, nil
}

func (r *Reader) schema(ctx context.Context, shopID int) (*contentschema.Version, error) {
	v, err := (&contentschema.Store{Client: r.Client}).Active(ctx, shopID)
	if err != nil {
		if errors.Is(err, contentschema.ErrNoSchema) {
			return nil, ErrUnavailable
		}
		return nil, err
	}
	return v, nil
}

// published loads a published document: status 1 and a published copy.
func (r *Reader) published(ctx context.Context, shopID int, key string) (*ent.ContentDocument, error) {
	d, err := r.Client.ContentDocument.Query().
		Where(entdoc.ShopIDEQ(shopID), entdoc.KeyEQ(key), entdoc.StatusEQ(1), entdoc.PublishedJSONNotNil()).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrUnavailable
		}
		return nil, err
	}
	return d, nil
}

// DocumentBody is the response body of GET /shop/content/{key}.
func (r *Reader) DocumentBody(ctx context.Context, shopID int, key string) ([]byte, error) {
	if !contentstore.ValidKey(key) {
		return nil, ErrUnavailable
	}
	v, err := r.schema(ctx, shopID)
	if err != nil {
		return nil, err
	}
	d, err := r.published(ctx, shopID, key)
	if err != nil {
		return nil, err
	}
	b, err := r.publishedBody(v, d)
	if err != nil {
		return nil, err
	}
	return json.Marshal(b)
}

type listEntry struct {
	Key         string          `json:"key"`
	TypeKey     string          `json:"type_key"`
	Title       string          `json:"title"`
	PublishedAt *time.Time      `json:"published_at"`
	ETag        string          `json:"etag"`
	Content     json.RawMessage `json:"content,omitempty"`
	Meta        json.RawMessage `json:"meta,omitempty"`
}

// ETagOf is the strong validator of a response body: the first 16 hex digits
// of its SHA-256, quoted.
func ETagOf(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// ListBody is the response body of GET /shop/content. With includeContent every
// entry carries its hydrated content and meta (bounded by the 2 MiB published
// budget); keys, when non-empty, restricts the entries.
func (r *Reader) ListBody(ctx context.Context, shopID int, includeContent bool, keys []string) ([]byte, error) {
	v, err := r.schema(ctx, shopID)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return json.Marshal(map[string]any{"schema_version": 0, "documents": []listEntry{}})
		}
		return nil, err
	}
	q := r.Client.ContentDocument.Query().
		Where(entdoc.ShopIDEQ(shopID), entdoc.StatusEQ(1), entdoc.PublishedJSONNotNil())
	if len(keys) > 0 {
		q = q.Where(entdoc.KeyIn(keys...))
	}
	docs, err := q.Order(entdoc.ByKey()).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]listEntry, 0, len(docs))
	for _, d := range docs {
		b, err := r.publishedBody(v, d)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		e := listEntry{Key: d.Key, TypeKey: d.TypeKey, Title: d.Title, PublishedAt: d.PublishedAt, ETag: ETagOf(raw)}
		if includeContent {
			e.Content, e.Meta = b.Content, b.Meta
		}
		out = append(out, e)
	}
	return json.Marshal(map[string]any{"schema_version": v.Version, "documents": out})
}

// PreviewBody is the draft of a document, hydrated, for the preview endpoint.
func (r *Reader) PreviewBody(ctx context.Context, shopID int, key string) ([]byte, error) {
	if !contentstore.ValidKey(key) {
		return nil, ErrUnavailable
	}
	v, err := r.schema(ctx, shopID)
	if err != nil {
		return nil, err
	}
	d, err := r.Client.ContentDocument.Query().Where(entdoc.ShopIDEQ(shopID), entdoc.KeyEQ(key)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrUnavailable
		}
		return nil, err
	}
	content, err := hydrated(v, d.TypeKey, d.DraftJSON)
	if err != nil {
		return nil, err
	}
	return json.Marshal(previewBody{
		docBody: docBody{Key: d.Key, TypeKey: d.TypeKey, Title: d.Title, SchemaVersion: d.SchemaVersion,
			PublishedAt: d.PublishedAt, Content: content, Meta: metaOrEmpty(d.Meta)},
		Status: d.Status, DraftRev: d.DraftRev, Preview: true,
	})
}
