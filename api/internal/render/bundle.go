package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/contentschema"
	"ksdevworks/ecommerce/api/internal/ent"
	entpage "ksdevworks/ecommerce/api/internal/ent/page"
)

// ErrNotFound covers every 404 rendering case: page missing, draft-only,
// shop without theme, type_key unsupported by the current theme (design D3).
var ErrNotFound = errors.New("render: not found")

// Bundle is the client-agnostic SDUI contract (design D8): web SSR and native
// apps consume the same structure.
type Bundle struct {
	Shop BundleShop `json:"shop"`
	Page BundlePage `json:"page"`
}

type BundleShop struct {
	ID      int         `json:"id"`
	Name    string      `json:"name"`
	Theme   BundleTheme `json:"theme"`
	Content any         `json:"content"`
}

type BundleTheme struct {
	Code      string `json:"code"`
	LayoutKey string `json:"layout_key"`
}

type BundlePage struct {
	TypeKey      string `json:"type_key"`
	ComponentKey string `json:"component_key"`
	Title        string `json:"title"`
	Content      any    `json:"content"`
	SEO          any    `json:"seo"`
}

// Assembler builds bundles straight from the database (the cache layer wraps
// it). component_key is resolved dynamically from (shops.theme_id, type_key).
type Assembler struct {
	Client *ent.Client
}

// NormalizeSlug maps a stripped storefront path to a page slug: "/" → "home"
// (design D7 保留字), "/about" → "about".
func NormalizeSlug(path string) string {
	slug := strings.Trim(strings.TrimSpace(path), "/")
	if slug == "" {
		return "home"
	}
	return strings.ToLower(slug)
}

// AssemblePublished renders the public bundle: published pages only
// (spec content-rendering/Published-only public rendering).
func (a *Assembler) AssemblePublished(ctx context.Context, shopID int, slug string) (*Bundle, error) {
	return a.assemble(ctx, shopID, slug, true)
}

// AssembleWorkingCopy renders the draft/working-copy bundle for authenticated
// preview (spec content-rendering/Authenticated preview). Never cached.
func (a *Assembler) AssembleWorkingCopy(ctx context.Context, shopID int, slug string) (*Bundle, error) {
	return a.assemble(ctx, shopID, slug, false)
}

func (a *Assembler) assemble(ctx context.Context, shopID int, slug string, published bool) (*Bundle, error) {
	shop, err := a.Client.Shop.Get(ctx, shopID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("render: load shop: %w", err)
	}
	// Per-shop schema version (change retire-hosted-storefront-and-themes):
	// a shop without one cannot render. The theme row is read only for the
	// transitional shop.theme object until the theme code is removed.
	schemaVer, err := (&contentschema.Store{Client: a.Client}).Active(ctx, shopID)
	if err != nil {
		if errors.Is(err, contentschema.ErrNoSchema) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("render: load content schema: %w", err)
	}
	if shop.ThemeID == nil {
		return nil, ErrNotFound // shop without a theme cannot render
	}
	theme, err := a.Client.Theme.Get(ctx, *shop.ThemeID)
	if err != nil {
		return nil, ErrNotFound
	}

	pageQ := a.Client.Page.Query().Where(entpage.ShopIDEQ(shopID), entpage.SlugEQ(slug))
	if published {
		pageQ = pageQ.Where(entpage.StatusEQ(1), entpage.PublishedJSONNotNil())
	}
	pg, err := pageQ.Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("render: load page: %w", err)
	}

	// Dynamic type resolution (design D3): unsupported type_key → 404.
	pageSchema, ok := schemaVer.Doc.Types[pg.TypeKey]
	if !ok {
		return nil, ErrNotFound
	}

	shopContent, err := cms.Hydrate(schemaVer.Doc.Global, shop.ContentJSON)
	if err != nil {
		return nil, fmt.Errorf("render: hydrate shop content: %w", err)
	}
	payload := pg.PublishedJSON
	if !published {
		payload = pg.ContentJSON
	}
	pageContent, err := cms.Hydrate(pageSchema, payload)
	if err != nil {
		return nil, fmt.Errorf("render: hydrate page content: %w", err)
	}

	var seo map[string]any
	if len(pg.Meta) > 0 {
		_ = json.Unmarshal(pg.Meta, &seo)
	}
	if seo == nil {
		seo = map[string]any{}
	}

	return &Bundle{
		Shop: BundleShop{
			ID:      shop.ID,
			Name:    shop.Name,
			Theme:   BundleTheme{Code: theme.Code, LayoutKey: theme.LayoutKey},
			Content: shopContent,
		},
		Page: BundlePage{
			TypeKey:      pg.TypeKey,
			ComponentKey: schemaVer.Doc.ComponentKeys[pg.TypeKey],
			Title:        pg.Title,
			Content:      pageContent,
			SEO:          seo,
		},
	}, nil
}
