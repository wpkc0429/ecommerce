package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/site"
	"ksdevworks/ecommerce/api/internal/ent/siteshop"
)

// Resolution errors map to design D12 responses.
var (
	ErrSiteNotFound    = errors.New("tenant: domain not bound") // 404
	ErrNoDefaultShop   = errors.New("tenant: no shop matched")  // 404
	ErrShopUnderReview = errors.New("tenant: shop under review") // 404
	ErrShopDisabled    = errors.New("tenant: shop disabled")     // 503
)

const routeCacheTTL = 5 * time.Minute

// Mapping is one site_shop row plus the shop's status, as cached.
type Mapping struct {
	ShopID     int     `json:"shop_id"`
	PathPrefix *string `json:"path_prefix"`
	IsPrimary  bool    `json:"is_primary"`
	ShopStatus int16   `json:"shop_status"`
}

type routeEntry struct {
	SiteID   int       `json:"site_id"`
	Mappings []Mapping `json:"mappings"`
}

// Resolution is the outcome of host+path resolution.
type Resolution struct {
	SiteID int
	ShopID int
	// Path is the request path with the matched prefix stripped — the input
	// for page slug resolution ("/brand-a/about" → "/about").
	Path string
}

// Resolver implements design D5: Host (or X-Site-Domain) → site → longest
// path-prefix match → shop, with a Redis route cache (route:{host}, TTL 5m).
type Resolver struct {
	client *ent.Client
	redis  *redis.Client // optional; nil or failing → DB directly
	log    *slog.Logger
}

func NewResolver(client *ent.Client, rdb *redis.Client, log *slog.Logger) *Resolver {
	return &Resolver{client: client, redis: rdb, log: log}
}

// NormalizeHost lowercases and strips any port from a Host header value.
func NormalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		return h
	}
	return strings.TrimSuffix(host, ".")
}

// NormalizePathPrefix canonicalizes a stored prefix to "/x" form (leading
// slash, no trailing slash). Empty/"/" → "".
func NormalizePathPrefix(p string) string {
	p = strings.TrimSpace(strings.ToLower(p))
	p = "/" + strings.Trim(p, "/")
	if p == "/" {
		return ""
	}
	return p
}

// Resolve maps (host, path) to a shop. Shop status gating (spec
// multi-tenancy/Shop status gating): 1 → serve, 0 → ErrShopDisabled (503),
// 2 → ErrShopUnderReview (404).
func (r *Resolver) Resolve(ctx context.Context, host, path string) (*Resolution, error) {
	host = NormalizeHost(host)
	if host == "" {
		return nil, ErrSiteNotFound
	}
	entry, err := r.entryFor(ctx, host)
	if err != nil {
		return nil, err
	}
	m, stripped, ok := MatchMapping(entry.Mappings, path)
	if !ok {
		return nil, ErrNoDefaultShop
	}
	switch m.ShopStatus {
	case 1:
		return &Resolution{SiteID: entry.SiteID, ShopID: m.ShopID, Path: stripped}, nil
	case 0:
		return nil, ErrShopDisabled
	default: // 2 (審核中) and anything unexpected behaves as not found
		return nil, ErrShopUnderReview
	}
}

// MatchMapping performs longest-prefix matching (segment-aware) and falls
// back to the default (NULL-prefix) mapping. Exported for unit tests.
func MatchMapping(mappings []Mapping, path string) (Mapping, string, bool) {
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var best *Mapping
	bestLen := -1
	var defaultMapping *Mapping
	for i := range mappings {
		m := &mappings[i]
		if m.PathPrefix == nil {
			defaultMapping = m
			continue
		}
		prefix := NormalizePathPrefix(*m.PathPrefix)
		if prefix == "" {
			defaultMapping = m
			continue
		}
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			if len(prefix) > bestLen {
				best = m
				bestLen = len(prefix)
			}
		}
	}
	if best != nil {
		stripped := path[bestLen:]
		if stripped == "" {
			stripped = "/"
		}
		return *best, stripped, true
	}
	if defaultMapping != nil {
		return *defaultMapping, path, true
	}
	return Mapping{}, "", false
}

func routeKey(host string) string { return "route:" + host }

func (r *Resolver) entryFor(ctx context.Context, host string) (*routeEntry, error) {
	if r.redis != nil {
		if raw, err := r.redis.Get(ctx, routeKey(host)).Bytes(); err == nil {
			var entry routeEntry
			if json.Unmarshal(raw, &entry) == nil {
				return &entry, nil
			}
		}
	}
	entry, err := r.loadEntry(ctx, host)
	if err != nil {
		return nil, err
	}
	if r.redis != nil {
		if raw, merr := json.Marshal(entry); merr == nil {
			if serr := r.redis.Set(ctx, routeKey(host), raw, routeCacheTTL).Err(); serr != nil {
				r.log.Warn("route cache set failed", "host", host, "err", serr)
			}
		}
	}
	return entry, nil
}

// loadEntry reads the site and its mappings (with shop status) from the DB.
// Note: shop status is cached inside the route entry; Phase 1 has no shop
// status mutation API, so staleness is bounded by the 5-minute TTL.
func (r *Resolver) loadEntry(ctx context.Context, host string) (*routeEntry, error) {
	s, err := r.client.Site.Query().Where(site.DomainEQ(host)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSiteNotFound
		}
		return nil, fmt.Errorf("tenant: query site: %w", err)
	}
	rows, err := r.client.SiteShop.Query().
		Where(siteshop.SiteIDEQ(s.ID)).
		WithShop().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("tenant: query mappings: %w", err)
	}
	entry := &routeEntry{SiteID: s.ID}
	for _, row := range rows {
		m := Mapping{ShopID: row.ShopID, PathPrefix: row.PathPrefix, IsPrimary: row.IsPrimary}
		if row.Edges.Shop != nil {
			m.ShopStatus = row.Edges.Shop.Status
		}
		entry.Mappings = append(entry.Mappings, m)
	}
	// Deterministic order (longest prefixes first) — not required for
	// correctness (MatchMapping scans all) but stabilizes cached payloads.
	sort.SliceStable(entry.Mappings, func(i, j int) bool {
		pi, pj := "", ""
		if entry.Mappings[i].PathPrefix != nil {
			pi = *entry.Mappings[i].PathPrefix
		}
		if entry.Mappings[j].PathPrefix != nil {
			pj = *entry.Mappings[j].PathPrefix
		}
		return len(pi) > len(pj)
	})
	return entry, nil
}

// InvalidateHosts drops route cache entries (spec multi-tenancy/Route
// resolution cache: 對應異動後立即生效).
func (r *Resolver) InvalidateHosts(ctx context.Context, hosts ...string) {
	if r.redis == nil || len(hosts) == 0 {
		return
	}
	keys := make([]string, 0, len(hosts))
	for _, h := range hosts {
		keys = append(keys, routeKey(NormalizeHost(h)))
	}
	if err := r.redis.Del(ctx, keys...).Err(); err != nil {
		r.log.Warn("route cache invalidation failed", "hosts", hosts, "err", err)
	}
}
