package tenant

import (
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/ent"
)

// Resolution errors map to design D12 responses. A shop is named by its
// publishable key (see clientauth.go); Host/site resolution was removed by
// change retire-hosted-storefront-and-themes.
var (
	ErrShopUnderReview = errors.New("tenant: shop under review") // 404
	ErrShopDisabled    = errors.New("tenant: shop disabled")     // 503
)

// routeCacheTTL bounds how long a cached publishable-key or origin entry may
// be served after the underlying rows change without an invalidation event.
const routeCacheTTL = 5 * time.Minute

// Resolution is the outcome of resolving a tenant.
type Resolution struct {
	ShopID int
	// Path is the caller-supplied storefront path (the render endpoint stores
	// the ?path= value here) — the input for page slug resolution.
	Path string
}

// Resolver resolves publishable keys and registered origins to shops, with a
// Redis cache (pkey:{key}, corsorigin2:{origin}).
type Resolver struct {
	client *ent.Client
	redis  *redis.Client // optional; nil or failing → DB directly
	log    *slog.Logger
}

func NewResolver(client *ent.Client, rdb *redis.Client, log *slog.Logger) *Resolver {
	return &Resolver{client: client, redis: rdb, log: log}
}
