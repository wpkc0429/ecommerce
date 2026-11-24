// Package contentschema is the per-shop content schema store (change
// retire-hosted-storefront-and-themes group 1, the first slice of
// headless-content-store): the JSON Schemas that validate a shop's page
// content and shop-global content live here, versioned per shop, instead of
// being read from the platform's themes.
//
// The active schema of a shop is its row with the highest version. Rows are
// immutable; a change is a new version.
package contentschema

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"entgo.io/ent/dialect/sql"

	"ksdevworks/ecommerce/api/internal/ent"
	entcs "ksdevworks/ecommerce/api/internal/ent/contentschema"
)

// Sources of a schema version.
const (
	SourceThemeBackfill = "theme_backfill"
	SourceRegistered    = "registered"
)

// ErrNoSchema is returned when a shop has no schema version at all.
var ErrNoSchema = errors.New("contentschema: shop has no content schema")

// Doc is the format-1 schema document stored in content_schemas.schema_json.
//
// For a format-2 row (registered through the admin API, design D3) the same
// struct is the normalized READ view: Types maps each type key to its JSON
// Schema (the wrapper's title is in Titles), Global is the schema of the
// "global" type, and Documents / Collections carry the declarations. Format-2
// rows are never produced by marshaling a Doc.
type Doc struct {
	Format    int                        `json:"format"`
	Types     map[string]json.RawMessage `json:"types"`
	TypeOrder []string                   `json:"type_order,omitempty"`
	Global    json.RawMessage            `json:"global"`

	Titles      map[string]string         `json:"-"`
	Documents   map[string]DocumentDecl   `json:"-"`
	Collections map[string]CollectionDecl `json:"-"`
}

// DocumentDecl declares a fixed document key (format 2).
type DocumentDecl struct {
	Type string `json:"type"`
}

// CollectionDecl declares a dynamic key prefix (format 2).
type CollectionDecl struct {
	Type string `json:"type"`
	Max  int    `json:"max"`
}

// EmptyGlobalSchema is the global schema of a format-2 document that defines
// no "global" type: nothing may be stored.
const EmptyGlobalSchema = `{"type":"object","additionalProperties":false,"properties":{}}`

// TypeInfo describes one page type of a shop's schema.
type TypeInfo struct {
	TypeKey string
}

// Store reads per-shop schema versions.
type Store struct {
	Client *ent.Client
}

// Version is an active schema version.
type Version struct {
	Version int
	Doc     Doc
	// Raw is the stored schema_json (format 1 or 2).
	Raw json.RawMessage
}

// Active returns the shop's highest schema version, or ErrNoSchema.
func (s *Store) Active(ctx context.Context, shopID int) (*Version, error) {
	row, err := s.Client.ContentSchema.Query().
		Where(entcs.ShopIDEQ(shopID)).
		Order(entcs.ByVersion(sql.OrderDesc())).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNoSchema
		}
		return nil, fmt.Errorf("contentschema: load active: %w", err)
	}
	return decodeRow(row)
}

// PageSchema returns the JSON Schema of one page type. ok is false when the
// shop has no schema or the type is not in it.
func (s *Store) PageSchema(ctx context.Context, shopID int, typeKey string) (schema json.RawMessage, ok bool, err error) {
	v, err := s.Active(ctx, shopID)
	if err != nil {
		if errors.Is(err, ErrNoSchema) {
			return nil, false, nil
		}
		return nil, false, err
	}
	sch, found := v.Doc.Types[typeKey]
	return sch, found, nil
}

// GlobalSchema returns the shop-global content schema. ok is false when the
// shop has no schema.
func (s *Store) GlobalSchema(ctx context.Context, shopID int) (schema json.RawMessage, ok bool, err error) {
	v, err := s.Active(ctx, shopID)
	if err != nil {
		if errors.Is(err, ErrNoSchema) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return v.Doc.Global, true, nil
}

// Types lists the shop's page types in their registered order.
func (s *Store) Types(ctx context.Context, shopID int) ([]TypeInfo, error) {
	v, err := s.Active(ctx, shopID)
	if err != nil {
		if errors.Is(err, ErrNoSchema) {
			return []TypeInfo{}, nil
		}
		return nil, err
	}
	return v.Doc.TypeInfos(), nil
}

// TypeInfos lists the document's page types: type_order first, then any
// remaining keys alphabetically.
func (d Doc) TypeInfos() []TypeInfo {
	out := make([]TypeInfo, 0, len(d.Types))
	seen := map[string]bool{}
	for _, k := range d.TypeOrder {
		if _, ok := d.Types[k]; ok && !seen[k] {
			seen[k] = true
			out = append(out, TypeInfo{TypeKey: k})
		}
	}
	rest := make([]string, 0)
	for k := range d.Types {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		out = append(out, TypeInfo{TypeKey: k})
	}
	return out
}

// format2 is the stored shape of a registered content.schema.json.
type format2 struct {
	Format int `json:"format"`
	Types  map[string]struct {
		Title  string          `json:"title"`
		Schema json.RawMessage `json:"schema"`
	} `json:"types"`
	Documents   map[string]DocumentDecl   `json:"documents"`
	Collections map[string]CollectionDecl `json:"collections"`
}

func decodeRow(row *ent.ContentSchema) (*Version, error) {
	var head struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(row.SchemaJSON, &head); err != nil {
		return nil, fmt.Errorf("contentschema: decode shop %d version %d: %w", row.ShopID, row.Version, err)
	}
	if head.Format == 2 {
		var f format2
		if err := json.Unmarshal(row.SchemaJSON, &f); err != nil {
			return nil, fmt.Errorf("contentschema: decode shop %d version %d: %w", row.ShopID, row.Version, err)
		}
		d := Doc{
			Format:      2,
			Types:       map[string]json.RawMessage{},
			Titles:      map[string]string{},
			Documents:   f.Documents,
			Collections: f.Collections,
			Global:      json.RawMessage(EmptyGlobalSchema),
		}
		for k, t := range f.Types {
			if k == "global" {
				d.Global = t.Schema
				continue
			}
			d.Types[k] = t.Schema
			d.Titles[k] = t.Title
		}
		return &Version{Version: row.Version, Doc: d, Raw: row.SchemaJSON}, nil
	}
	var d Doc
	if err := json.Unmarshal(row.SchemaJSON, &d); err != nil {
		return nil, fmt.Errorf("contentschema: decode shop %d version %d: %w", row.ShopID, row.Version, err)
	}
	if d.Types == nil {
		d.Types = map[string]json.RawMessage{}
	}
	return &Version{Version: row.Version, Doc: d, Raw: row.SchemaJSON}, nil
}

// DecodeRow exposes the row decoder to the registration service.
func DecodeRow(row *ent.ContentSchema) (*Version, error) { return decodeRow(row) }

// Digest returns the hex SHA-256 of the normalized (keys sorted, compact)
// form of the document, so two semantically equal documents hash equally no
// matter how the database or the writer ordered their keys.
func Digest(d Doc) (string, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return "", err
	}
	norm, err := json.Marshal(generic)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(norm)
	return hex.EncodeToString(sum[:]), nil
}

// Writer writes schema versions through one ent client, so the same code runs
// on a plain client or inside a caller-owned transaction.
type Writer struct {
	cs *ent.ContentSchemaClient
}

// WriterFor returns a Writer on a plain client.
func WriterFor(c *ent.Client) Writer { return Writer{cs: c.ContentSchema} }

// WriterForTx returns a Writer inside tx.
func WriterForTx(tx *ent.Tx) Writer { return Writer{cs: tx.ContentSchema} }

// Write stores d as the shop's next schema version, unless the active version
// already has the same content (then it is a no-op and written is false).
func (w Writer) Write(ctx context.Context, shopID int, d Doc, source string) (written bool, err error) {
	digest, err := Digest(d)
	if err != nil {
		return false, fmt.Errorf("contentschema: digest: %w", err)
	}
	next := 1
	last, err := w.cs.Query().Where(entcs.ShopIDEQ(shopID)).Order(entcs.ByVersion(sql.OrderDesc())).First(ctx)
	switch {
	case err == nil:
		cur, derr := decodeRow(last)
		if derr != nil {
			return false, derr
		}
		if cur.Doc.Format == 2 {
			// A registered (format 2) schema is owned by whoever registered it;
			// a re-run of a format-1 writer such as the demo seed must not
			// replace it.
			return false, nil
		}
		curDigest, derr := Digest(cur.Doc)
		if derr != nil {
			return false, derr
		}
		if curDigest == digest {
			return false, nil
		}
		next = last.Version + 1
	case ent.IsNotFound(err):
	default:
		return false, fmt.Errorf("contentschema: load latest: %w", err)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return false, err
	}
	if _, err := w.cs.Create().
		SetShopID(shopID).
		SetVersion(next).
		SetSchemaJSON(raw).
		SetSha256(digest).
		SetSource(source).
		Save(ctx); err != nil {
		return false, fmt.Errorf("contentschema: write shop %d version %d: %w", shopID, next, err)
	}
	return true, nil
}
