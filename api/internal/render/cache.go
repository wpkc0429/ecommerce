// Package render assembles the storefront render bundle (design D8) and
// serves it through a versioned Redis read-through cache with singleflight
// protection and DB-degrade on Redis failure.
package render

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

const bundleTTL = 24 * time.Hour

// Cache implements the versioned key scheme:
//
//	cache:shop:{shop_id}:v{ver}:page:{slug}   — bundle JSON, TTL 24h
//	cache:shop:{shop_id}:ver                  — version counter
//
// Version bumps invalidate a whole tenant at once (old keys expire via TTL);
// single-page publish deletes just that page's key.
type Cache struct {
	redis *redis.Client // nil → cache disabled (assemble every time)
	log   *slog.Logger
	sf    singleflight.Group
}

func NewCache(rdb *redis.Client, log *slog.Logger) *Cache {
	return &Cache{redis: rdb, log: log}
}

func verKey(shopID int) string { return fmt.Sprintf("cache:shop:%d:ver", shopID) }

func pageKey(shopID int, ver int64, slug string) string {
	return fmt.Sprintf("cache:shop:%d:v%d:page:%s", shopID, ver, slug)
}

// version reads the current tenant cache version (absent → 0).
// ok=false means Redis is unavailable.
func (c *Cache) version(ctx context.Context, shopID int) (int64, bool) {
	if c.redis == nil {
		return 0, false
	}
	v, err := c.redis.Get(ctx, verKey(shopID)).Result()
	if err == redis.Nil {
		return 0, true
	}
	if err != nil {
		return 0, false
	}
	n, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil {
		return 0, true
	}
	return n, true
}

// GetPage returns the cached bundle or assembles (and caches) it.
// The bool result reports a cache hit. Any Redis failure degrades to a direct
// DB assembly — 服務不中斷 (spec content-rendering/Redis read-through cache).
func (c *Cache) GetPage(ctx context.Context, shopID int, slug string, assemble func(context.Context) ([]byte, error)) ([]byte, bool, error) {
	ver, redisOK := c.version(ctx, shopID)
	if !redisOK {
		if c.redis != nil {
			c.log.Warn("render cache degraded to DB", "shop", shopID, "slug", slug)
		}
		out, err := assemble(ctx)
		return out, false, err
	}
	key := pageKey(shopID, ver, slug)
	if raw, err := c.redis.Get(ctx, key).Bytes(); err == nil {
		return raw, true, nil
	}

	// Merge concurrent misses for the same key (cache-stampede guard).
	v, err, _ := c.sf.Do(key, func() (any, error) {
		if raw, err := c.redis.Get(ctx, key).Bytes(); err == nil {
			return raw, nil
		}
		out, err := assemble(ctx)
		if err != nil {
			return nil, err
		}
		if err := c.redis.Set(ctx, key, out, bundleTTL).Err(); err != nil {
			c.log.Warn("render cache set failed", "key", key, "err", err)
		}
		return out, nil
	})
	if err != nil {
		return nil, false, err
	}
	return v.([]byte), false, nil
}

// BumpShops invalidates whole tenants by incrementing their version counters
// (design D8: 寧可多失效不可漏失效; old keys are TTL-reclaimed).
func (c *Cache) BumpShops(ctx context.Context, shopIDs ...int) {
	if c.redis == nil || len(shopIDs) == 0 {
		return
	}
	pipe := c.redis.Pipeline()
	for _, id := range shopIDs {
		pipe.Incr(ctx, verKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		c.log.Warn("render cache bump failed", "shops", shopIDs, "err", err)
	}
}

// DeletePage drops one page's bundle under the current version (single-page
// publish/unpublish path — cheaper than a tenant-wide bump).
func (c *Cache) DeletePage(ctx context.Context, shopID int, slug string) {
	if c.redis == nil {
		return
	}
	ver, ok := c.version(ctx, shopID)
	if !ok {
		return
	}
	if err := c.redis.Del(ctx, pageKey(shopID, ver, slug)).Err(); err != nil {
		c.log.Warn("render cache page delete failed", "shop", shopID, "slug", slug, "err", err)
	}
}
