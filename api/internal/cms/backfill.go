package cms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"ksdevworks/ecommerce/api/internal/contentschema"
	"ksdevworks/ecommerce/api/internal/contentstore"
	"ksdevworks/ecommerce/api/internal/ent"
	entpage "ksdevworks/ecommerce/api/internal/ent/page"
	entshop "ksdevworks/ecommerce/api/internal/ent/shop"
	"ksdevworks/ecommerce/api/internal/events"
)

// BackfillItem is one line of the rich text backfill report. It deliberately
// carries ids and a verdict only — never content, which may hold merchant
// data (change cms-rich-text-sanitization, design D7).
type BackfillItem struct {
	Kind    string // "page" | "shop"
	ID      int    // page id or shop id
	ShopID  int
	Field   string // "content_json" | "published_json"
	Changed bool
	Skipped string // non-empty: why the row could not be checked
}

// BackfillSummary totals a backfill run.
type BackfillSummary struct {
	Checked, Changed, Skipped int
}

// BackfillRichText re-sanitizes every stored CMS payload against the schema of
// the shop's CURRENT content schema: pages.content_json, pages.published_json and
// shops.content_json (design D7). It is idempotent — a second run reports zero
// changes — and writes nothing unless apply is true.
//
// Why rows can need it even after the write path sanitizes: content written
// before this change, and content written under a theme whose schema did not
// yet mark a field `x-editor: richtext` (a theme update or switch can turn a
// stored plain string into rendered HTML without any new write).
//
// After writing, cache invalidation goes through the existing domain events
// (PagePublished for a changed published_json, ShopContentUpdated for shop
// content) — never a direct Redis call (project rule). The dispatcher may be
// nil (dry runs, tests that do not care), in which case nothing is published.
//
// Runs outside any tenant context, so every query here names shop_id
// explicitly where it matters and the interceptor does not apply.
func (s *Service) BackfillRichText(ctx context.Context, apply bool, report func(BackfillItem)) (BackfillSummary, error) {
	var sum BackfillSummary
	emit := func(it BackfillItem) {
		switch {
		case it.Skipped != "":
			sum.Skipped++
		default:
			sum.Checked++
			if it.Changed {
				sum.Changed++
			}
		}
		if report != nil {
			report(it)
		}
	}

	shops, err := s.Client.Shop.Query().Order(entshop.ByID()).All(ctx)
	if err != nil {
		return sum, fmt.Errorf("cms: backfill: list shops: %w", err)
	}
	for _, sh := range shops {
		if err := s.backfillShop(ctx, sh, apply, emit); err != nil {
			return sum, err
		}
	}
	return sum, nil
}

func (s *Service) backfillShop(ctx context.Context, sh *ent.Shop, apply bool, emit func(BackfillItem)) error {
	pages, err := s.Client.Page.Query().
		Where(entpage.ShopIDEQ(sh.ID)).
		Order(entpage.ByID()).
		All(ctx)
	if err != nil {
		return fmt.Errorf("cms: backfill: list pages of shop %d: %w", sh.ID, err)
	}
	active, err := s.schemas().Active(ctx, sh.ID)
	if errors.Is(err, contentschema.ErrNoSchema) {
		emit(BackfillItem{Kind: "shop", ID: sh.ID, ShopID: sh.ID, Field: "content_json", Skipped: "no content schema"})
		for _, p := range pages {
			emit(BackfillItem{Kind: "page", ID: p.ID, ShopID: sh.ID, Field: "content_json", Skipped: "shop has no content schema"})
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("cms: backfill: load content schema of shop %d: %w", sh.ID, err)
	}

	// Shop global content.
	clean, changed, err := SanitizeRichText(active.Doc.Global, sh.ContentJSON)
	if err != nil {
		emit(BackfillItem{Kind: "shop", ID: sh.ID, ShopID: sh.ID, Field: "content_json", Skipped: "unparseable content"})
	} else {
		if changed && apply {
			version := s.activeSchemaVersion(ctx, sh.ID)
			if err := s.inTx(ctx, func(c *ent.Client) error {
				if _, err := c.Shop.UpdateOneID(sh.ID).SetContentJSON(clean).Save(ctx); err != nil {
					return err
				}
				return contentstore.SyncGlobal(ctx, c, sh.ID, clean, version)
			}); err != nil {
				return fmt.Errorf("cms: backfill: write shop %d: %w", sh.ID, err)
			}
			s.publish(ctx, events.ShopContentUpdated{ShopID: sh.ID})
		}
		emit(BackfillItem{Kind: "shop", ID: sh.ID, ShopID: sh.ID, Field: "content_json", Changed: changed})
	}

	for _, p := range pages {
		schema := active.Doc.Types[p.TypeKey]
		if schema == nil {
			emit(BackfillItem{Kind: "page", ID: p.ID, ShopID: sh.ID, Field: "content_json", Skipped: "type_key not in the content schema"})
			continue
		}
		if err := s.backfillPage(ctx, p, schema, apply, emit); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) backfillPage(ctx context.Context, p *ent.Page, schema json.RawMessage, apply bool, emit func(BackfillItem)) error {
	draft, draftChanged, err := SanitizeRichText(schema, p.ContentJSON)
	if err != nil {
		emit(BackfillItem{Kind: "page", ID: p.ID, ShopID: p.ShopID, Field: "content_json", Skipped: "unparseable content"})
		draftChanged = false
	} else {
		emit(BackfillItem{Kind: "page", ID: p.ID, ShopID: p.ShopID, Field: "content_json", Changed: draftChanged})
	}

	var published json.RawMessage
	publishedChanged := false
	if len(p.PublishedJSON) > 0 && string(p.PublishedJSON) != "null" {
		published, publishedChanged, err = SanitizeRichText(schema, p.PublishedJSON)
		if err != nil {
			emit(BackfillItem{Kind: "page", ID: p.ID, ShopID: p.ShopID, Field: "published_json", Skipped: "unparseable content"})
			publishedChanged = false
		} else {
			emit(BackfillItem{Kind: "page", ID: p.ID, ShopID: p.ShopID, Field: "published_json", Changed: publishedChanged})
		}
	}

	if !apply || (!draftChanged && !publishedChanged) {
		return nil
	}
	version := s.activeSchemaVersion(ctx, p.ShopID)
	if err := s.inTx(ctx, func(c *ent.Client) error {
		upd := c.Page.UpdateOneID(p.ID).Where(entpage.ShopIDEQ(p.ShopID))
		if draftChanged {
			upd.SetContentJSON(draft)
		}
		if publishedChanged {
			upd.SetPublishedJSON(published)
		}
		np, err := upd.Save(ctx)
		if err != nil {
			return err
		}
		// The document store is kept equal in the same transaction.
		return contentstore.SyncPage(ctx, c, contentstore.PageSync{
			Page: np, DraftChanged: draftChanged, PublishedRewritten: publishedChanged, SchemaVersion: version,
		})
	}); err != nil {
		return fmt.Errorf("cms: backfill: write page %d: %w", p.ID, err)
	}
	if publishedChanged {
		// Same event a publish emits: the render invalidator deletes the
		// page's cache key, so the next render serves the sanitized snapshot.
		s.publish(ctx, events.PagePublished{ShopID: p.ShopID, Slug: p.Slug})
		s.publish(ctx, events.ContentPublished{ShopID: p.ShopID, Key: p.Slug})
	}
	return nil
}

func (s *Service) publish(ctx context.Context, e events.Event) {
	if s.Dispatcher != nil {
		s.Dispatcher.Publish(ctx, e)
	}
}
