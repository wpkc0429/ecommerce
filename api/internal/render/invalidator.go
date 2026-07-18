package render

import (
	"context"
	"log/slog"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/shop"
	"ksdevworks/ecommerce/api/internal/events"
)

// Invalidator translates domain events into cache invalidation (task 7.3).
// Handlers run post-commit via the events dispatcher — never inline in HTTP
// handlers (design D8: 失效集中於領域事件層).
type Invalidator struct {
	Cache  *Cache
	Client *ent.Client
	Log    *slog.Logger
}

// Handle is subscribed to the events dispatcher.
func (i *Invalidator) Handle(ctx context.Context, e events.Event) {
	switch ev := e.(type) {
	case events.PagePublished:
		i.Cache.DeletePage(ctx, ev.ShopID, ev.Slug)
	case events.PageUnpublished:
		i.Cache.DeletePage(ctx, ev.ShopID, ev.Slug)
	case events.ShopContentUpdated:
		i.Cache.BumpShops(ctx, ev.ShopID)
	case events.ThemeSwitched:
		i.Cache.BumpShops(ctx, ev.ShopID)
	case events.ThemeUpdated:
		// 主題級失效: every shop applying this theme gets a version bump.
		ids, err := i.Client.Shop.Query().
			Where(shop.ThemeIDEQ(ev.ThemeID)).
			Select(shop.FieldID).
			Ints(ctx)
		if err != nil {
			i.Log.Error("theme invalidation: list shops", "theme", ev.ThemeID, "err", err)
			return
		}
		i.Cache.BumpShops(ctx, ids...)
	case events.SiteMappingChanged:
		i.Cache.BumpShops(ctx, ev.ShopIDs...)
	}
}
