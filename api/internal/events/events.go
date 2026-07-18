// Package events is the domain-event layer that centralizes cache
// invalidation (design D8): services publish events after their transaction
// commits; subscribers (render cache, route cache) react. Handlers must never
// be called before the write is durable — 失效與寫入交易成功綁定.
package events

import (
	"context"
	"log/slog"
)

// Event is a domain event. Concrete types below.
type Event interface{ EventName() string }

// PagePublished — publish action completed; the page's cache key is deleted.
type PagePublished struct {
	ShopID int
	Slug   string
}

func (PagePublished) EventName() string { return "page.published" }

// PageUnpublished — page taken offline; its cache key is deleted.
type PageUnpublished struct {
	ShopID int
	Slug   string
}

func (PageUnpublished) EventName() string { return "page.unpublished" }

// ShopContentUpdated — shop global payload changed; tenant-wide version bump.
type ShopContentUpdated struct{ ShopID int }

func (ShopContentUpdated) EventName() string { return "shop.content_updated" }

// ThemeSwitched — shop switched theme; tenant-wide version bump.
type ThemeSwitched struct{ ShopID int }

func (ThemeSwitched) EventName() string { return "shop.theme_switched" }

// ThemeUpdated — platform updated a theme; every shop on it gets a bump.
type ThemeUpdated struct{ ThemeID int }

func (ThemeUpdated) EventName() string { return "theme.updated" }

// SiteMappingChanged — sites/site_shop rows changed; route cache entries for
// the affected hosts are dropped, and mapped shops get a version bump.
type SiteMappingChanged struct {
	Hosts   []string
	ShopIDs []int
}

func (SiteMappingChanged) EventName() string { return "site.mapping_changed" }

// Handler consumes events; it must be non-blocking-ish and never panic the
// request (the dispatcher recovers and logs).
type Handler func(ctx context.Context, e Event)

// Dispatcher fans events out synchronously (post-commit, same request).
type Dispatcher struct {
	log      *slog.Logger
	handlers []Handler
}

func NewDispatcher(log *slog.Logger) *Dispatcher {
	return &Dispatcher{log: log}
}

// Subscribe registers a handler for all events (handlers filter by type).
func (d *Dispatcher) Subscribe(h Handler) {
	d.handlers = append(d.handlers, h)
}

// Publish delivers events to every subscriber. Call only after the writing
// transaction committed successfully.
func (d *Dispatcher) Publish(ctx context.Context, evs ...Event) {
	for _, e := range evs {
		for _, h := range d.handlers {
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						d.log.Error("event handler panic", "event", e.EventName(), "panic", rec)
					}
				}()
				h(ctx, e)
			}()
		}
	}
}
