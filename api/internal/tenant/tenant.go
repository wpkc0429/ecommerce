// Package tenant carries the resolved shop context of a storefront request
// and enforces tenant data isolation at the data-access layer (design D5,
// spec multi-tenancy/Tenant data isolation enforcement).
package tenant

import (
	"context"
)

type ctxKey struct{}

// WithShopID marks ctx as scoped to the given tenant shop.
func WithShopID(ctx context.Context, shopID int) context.Context {
	return context.WithValue(ctx, ctxKey{}, shopID)
}

// ShopID returns the tenant shop of ctx, if any.
func ShopID(ctx context.Context) (int, bool) {
	id, ok := ctx.Value(ctxKey{}).(int)
	return id, ok
}
