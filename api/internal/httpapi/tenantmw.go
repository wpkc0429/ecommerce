package httpapi

import (
	"errors"
	"net/http"

	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// NewTenantMiddleware resolves the storefront tenant for member-facing APIs
// (task 5.1, design D5): X-Site-Domain header wins (native app channel),
// otherwise the Host header. X-Site-Path (defaulting to "/") feeds the
// path-prefix disambiguation when one domain hosts several shops.
func NewTenantMiddleware(resolver *tenant.Resolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Header.Get("X-Site-Domain")
			if host == "" {
				host = r.Host
			}
			path := r.Header.Get("X-Site-Path")
			if path == "" {
				path = "/"
			}
			res, err := resolver.Resolve(r.Context(), host, path)
			if err != nil {
				WriteResolveError(w, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(tenant.WithShopID(r.Context(), res.ShopID)))
		})
	}
}

// WriteResolveError maps resolver errors to design D12 responses.
func WriteResolveError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tenant.ErrShopDisabled):
		httpx.ShopDisabled(w) // 503 → storefront maintenance page
	case errors.Is(err, tenant.ErrSiteNotFound),
		errors.Is(err, tenant.ErrNoDefaultShop),
		errors.Is(err, tenant.ErrShopUnderReview):
		httpx.NotFound(w)
	default:
		httpx.Internal(w)
	}
}
