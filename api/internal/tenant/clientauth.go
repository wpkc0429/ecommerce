package tenant

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/publishablekey"
	"ksdevworks/ecommerce/api/internal/ent/shopallowedorigin"
)

// ErrKeyNotFound covers every unusable publishable key — unknown, malformed
// and revoked alike (change storefront-client-auth, spec: 未知金鑰與已撤銷金
// 鑰的回應同構). Callers map it to 404.
var ErrKeyNotFound = errors.New("tenant: publishable key not found")

// KeyPrefix marks every publishable key. Parsing is prefix-based, so a future
// environment infix ("pk_test_") stays unambiguously parseable — the random
// segment below deliberately cannot contain "_" (design D8).
const KeyPrefix = "pk_"

// keyRandomBytes yields 160 bits of entropy → 32 base32 chars. Entropy here is
// not about secrecy (the key is public by design, D1) but about making keys
// unguessable, so nobody can enumerate which shops exist on the platform.
const keyRandomBytes = 20

// keyEncoding is lowercase base32 without padding: the alphabet (a-z2-7)
// excludes "_", which is what keeps the prefix grammar unambiguous.
var keyEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewPublishableKey mints a key value. It is an identifier, not a credential
// (design D1): the caller stores it in plaintext and shows it in full.
func NewPublishableKey() (string, error) {
	buf := make([]byte, keyRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("tenant: generate publishable key: %w", err)
	}
	return KeyPrefix + keyEncoding.EncodeToString(buf), nil
}

// ValidKeyFormat reports whether s is shaped like a publishable key. It is a
// cheap pre-filter that keeps obviously-bogus values from reaching the
// database; it says nothing about whether the key exists or is still live.
func ValidKeyFormat(s string) bool {
	rest, ok := strings.CutPrefix(s, KeyPrefix)
	if !ok || rest == "" || len(s) > 64 {
		return false
	}
	for _, r := range rest {
		// The base32 alphabet, plus "_" so a future environment infix
		// ("pk_test_…") still parses (design D8).
		lower := r >= 'a' && r <= 'z'
		digit := r >= '2' && r <= '7'
		if !lower && !digit && r != '_' {
			return false
		}
	}
	return true
}

// keyEntry is the cached publishable-key → shop mapping. shop_status is cached
// alongside for the same reason routeEntry caches it: the gate below needs it
// on every request. Staleness is bounded by the TTL and cut short by the
// StorefrontClientChanged / SiteMappingChanged invalidations (design D10).
type keyEntry struct {
	ShopID     int   `json:"shop_id"`
	ShopStatus int16 `json:"shop_status"`
}

func keyCacheKey(key string) string { return "pkey:" + key }

func originCacheKey(origin string) string { return "corsorigin:" + origin }

// ResolveKey maps a publishable key to its shop, applying the same shop-status
// gate as Resolve (1 → serve, 0 → ErrShopDisabled, 2 → ErrShopUnderReview).
//
// Revoked and unknown keys both yield ErrKeyNotFound: the key names a client
// deployment, and "was this ever a key?" is not information the API owes an
// unauthenticated caller.
func (r *Resolver) ResolveKey(ctx context.Context, key string) (*Resolution, error) {
	if !ValidKeyFormat(key) {
		return nil, ErrKeyNotFound
	}
	entry, err := r.keyEntryFor(ctx, key)
	if err != nil {
		return nil, err
	}
	switch entry.ShopStatus {
	case 1:
		return &Resolution{ShopID: entry.ShopID, Path: "/"}, nil
	case 0:
		return nil, ErrShopDisabled
	default: // 2 (審核中) and anything unexpected behaves as not found
		return nil, ErrShopUnderReview
	}
}

func (r *Resolver) keyEntryFor(ctx context.Context, key string) (*keyEntry, error) {
	if r.redis != nil {
		if raw, err := r.redis.Get(ctx, keyCacheKey(key)).Bytes(); err == nil {
			var entry keyEntry
			if json.Unmarshal(raw, &entry) == nil {
				return &entry, nil
			}
		}
	}
	row, err := r.client.PublishableKey.Query().
		Where(publishablekey.KeyEQ(key), publishablekey.RevokedAtIsNil()).
		WithShop().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			// Deliberately not negative-cached: a revoked key must stop working
			// immediately (spec: 撤銷即時生效), and unknown keys are not a
			// volume concern the way unknown origins are (every preflight
			// carries an Origin; almost no request carries a bogus key).
			return nil, ErrKeyNotFound
		}
		return nil, fmt.Errorf("tenant: query publishable key: %w", err)
	}
	entry := &keyEntry{ShopID: row.ShopID}
	if row.Edges.Shop != nil {
		entry.ShopStatus = row.Edges.Shop.Status
	}
	if r.redis != nil {
		if raw, merr := json.Marshal(entry); merr == nil {
			if serr := r.redis.Set(ctx, keyCacheKey(key), raw, routeCacheTTL).Err(); serr != nil {
				r.log.Warn("publishable key cache set failed", "err", serr)
			}
		}
	}
	return entry, nil
}

// NormalizeOrigin canonicalizes a browser Origin to "scheme://host[:port]"
// (lowercase, no path, no trailing slash). It returns "" for anything that is
// not a usable http/https origin — including "null", wildcards and values
// carrying a path, which are rejected at registration time.
func NormalizeOrigin(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return ""
	}
	host := strings.ToLower(u.Host)
	if host == "" || strings.ContainsAny(host, "*? ") {
		return ""
	}
	return scheme + "://" + host
}

// IsRegisteredOrigin reports whether the origin is registered by any shop.
//
// "any shop" is the only question answerable at CORS time: a preflight carries
// no publishable key (custom headers have no value in an OPTIONS preflight),
// so there is no tenant context yet — the registered-origin set is itself the
// origin → shop index (design D3). Errors resolve to false: a failing lookup
// must not hand out cross-origin access.
func (r *Resolver) IsRegisteredOrigin(ctx context.Context, origin string) bool {
	origin = NormalizeOrigin(origin)
	if origin == "" {
		return false
	}
	ck := originCacheKey(origin)
	if r.redis != nil {
		if raw, err := r.redis.Get(ctx, ck).Result(); err == nil {
			return raw == "1"
		}
	}
	found, err := r.client.ShopAllowedOrigin.Query().
		Where(shopallowedorigin.OriginEQ(origin)).
		Exist(ctx)
	if err != nil {
		r.log.Warn("allowed origin lookup failed", "origin", origin, "err", err)
		return false
	}
	if r.redis != nil {
		// Negative caching included on purpose: every preflight from an
		// unregistered origin would otherwise hit the database (design D10).
		val := "0"
		if found {
			val = "1"
		}
		if serr := r.redis.Set(ctx, ck, val, routeCacheTTL).Err(); serr != nil {
			r.log.Warn("allowed origin cache set failed", "origin", origin, "err", serr)
		}
	}
	return found
}

// InvalidateKeys drops cached key resolutions (spec: 撤銷即時生效).
func (r *Resolver) InvalidateKeys(ctx context.Context, keys ...string) {
	if r.redis == nil || len(keys) == 0 {
		return
	}
	cks := make([]string, 0, len(keys))
	for _, k := range keys {
		cks = append(cks, keyCacheKey(k))
	}
	if err := r.redis.Del(ctx, cks...).Err(); err != nil {
		r.log.Warn("publishable key cache invalidation failed", "err", err)
	}
}

// InvalidateOrigins drops cached origin lookups (both positive and negative
// entries — registering a new origin must invalidate its cached "no").
func (r *Resolver) InvalidateOrigins(ctx context.Context, origins ...string) {
	if r.redis == nil || len(origins) == 0 {
		return
	}
	cks := make([]string, 0, len(origins))
	for _, o := range origins {
		if norm := NormalizeOrigin(o); norm != "" {
			cks = append(cks, originCacheKey(norm))
		}
	}
	if len(cks) == 0 {
		return
	}
	if err := r.redis.Del(ctx, cks...).Err(); err != nil {
		r.log.Warn("allowed origin cache invalidation failed", "err", err)
	}
}

// InvalidateShopKeys drops every cached key resolution of one shop. Used when
// the shop's own status changes (the cached entry embeds shop_status), so a
// disabled shop's keys stop serving at the same moment its domains do.
func (r *Resolver) InvalidateShopKeys(ctx context.Context, shopID int) {
	if r.redis == nil {
		return
	}
	keys, err := r.client.PublishableKey.Query().
		Where(publishablekey.ShopIDEQ(shopID)).
		Select(publishablekey.FieldKey).
		Strings(ctx)
	if err != nil {
		r.log.Warn("publishable key lookup for invalidation failed", "shop", shopID, "err", err)
		return
	}
	r.InvalidateKeys(ctx, keys...)
}
