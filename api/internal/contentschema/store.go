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
	entshop "ksdevworks/ecommerce/api/internal/ent/shop"
	"ksdevworks/ecommerce/api/internal/ent/themepage"
)

// Sources of a schema version.
const (
	SourceThemeBackfill = "theme_backfill"
	SourceRegistered    = "registered"
)

// ErrNoSchema is returned when a shop has no schema version at all.
var ErrNoSchema = errors.New("contentschema: shop has no content schema")

// Doc is the format-1 schema document stored in content_schemas.schema_json.
type Doc struct {
	Format        int                        `json:"format"`
	Types         map[string]json.RawMessage `json:"types"`
	TypeOrder     []string                   `json:"type_order,omitempty"`
	Global        json.RawMessage            `json:"global"`
	ComponentKeys map[string]string          `json:"component_keys,omitempty"`
}

// TypeInfo describes one page type of a shop's schema.
type TypeInfo struct {
	TypeKey      string
	ComponentKey string
}

// Store reads per-shop schema versions.
type Store struct {
	Client *ent.Client
}

// Version is an active schema version.
type Version struct {
	Version int
	Doc     Doc
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
			out = append(out, TypeInfo{TypeKey: k, ComponentKey: d.ComponentKeys[k]})
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
		out = append(out, TypeInfo{TypeKey: k, ComponentKey: d.ComponentKeys[k]})
	}
	return out
}

func decodeRow(row *ent.ContentSchema) (*Version, error) {
	var d Doc
	if err := json.Unmarshal(row.SchemaJSON, &d); err != nil {
		return nil, fmt.Errorf("contentschema: decode shop %d version %d: %w", row.ShopID, row.Version, err)
	}
	if d.Types == nil {
		d.Types = map[string]json.RawMessage{}
	}
	return &Version{Version: row.Version, Doc: d}, nil
}

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

// Writer writes schema versions through a set of ent clients, so the same code
// runs on a plain client or inside a caller-owned transaction.
type Writer struct {
	cs *ent.ContentSchemaClient
	th *ent.ThemeClient
	tp *ent.ThemePageClient
	sh *ent.ShopClient
}

// WriterFor returns a Writer on a plain client.
func WriterFor(c *ent.Client) Writer {
	return Writer{cs: c.ContentSchema, th: c.Theme, tp: c.ThemePage, sh: c.Shop}
}

// WriterForTx returns a Writer inside tx.
func WriterForTx(tx *ent.Tx) Writer {
	return Writer{cs: tx.ContentSchema, th: tx.Theme, tp: tx.ThemePage, sh: tx.Shop}
}

// FromTheme builds the format-1 document of a theme.
func (w Writer) FromTheme(ctx context.Context, themeID int) (Doc, error) {
	t, err := w.th.Get(ctx, themeID)
	if err != nil {
		return Doc{}, fmt.Errorf("contentschema: load theme %d: %w", themeID, err)
	}
	tps, err := w.tp.Query().Where(themepage.ThemeIDEQ(themeID)).Order(themepage.ByID()).All(ctx)
	if err != nil {
		return Doc{}, fmt.Errorf("contentschema: load theme pages of %d: %w", themeID, err)
	}
	d := Doc{
		Format:        1,
		Types:         map[string]json.RawMessage{},
		TypeOrder:     []string{},
		Global:        t.ConfigSchema,
		ComponentKeys: map[string]string{},
	}
	for _, p := range tps {
		d.Types[p.TypeKey] = p.PageSchema
		d.TypeOrder = append(d.TypeOrder, p.TypeKey)
		d.ComponentKeys[p.TypeKey] = p.ComponentKey
	}
	return d, nil
}

// WriteFromTheme writes the shop's next schema version as a copy of the theme's
// schemas, unless the active version already has the same content (then it is
// a no-op and written is false). Used by every path that applies or edits a
// theme until the theme code is removed.
func (w Writer) WriteFromTheme(ctx context.Context, shopID, themeID int) (written bool, err error) {
	d, err := w.FromTheme(ctx, themeID)
	if err != nil {
		return false, err
	}
	return w.write(ctx, shopID, d, SourceThemeBackfill)
}

// SyncThemeShops re-copies a theme's schemas to every shop applying it (a
// platform theme edit used to take effect for all of them at once).
func (w Writer) SyncThemeShops(ctx context.Context, themeID int) error {
	ids, err := w.sh.Query().Where(entshop.ThemeIDEQ(themeID)).IDs(ctx)
	if err != nil {
		return fmt.Errorf("contentschema: list shops of theme %d: %w", themeID, err)
	}
	for _, id := range ids {
		if _, err := w.WriteFromTheme(ctx, id, themeID); err != nil {
			return err
		}
	}
	return nil
}

func (w Writer) write(ctx context.Context, shopID int, d Doc, source string) (bool, error) {
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
