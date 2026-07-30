package httpapi

import (
	"errors"
	"net/http"

	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// PublishableKeyHeader carries the client's tenant declaration (change
// storefront-client-auth). Header only: a query parameter would leak the value
// into access logs and Referer headers and invite treating it as a secret,
// which it is not (design D1).
const PublishableKeyHeader = "X-Publishable-Key"

// NewTenantMiddleware resolves the storefront tenant for the /api/v1/shop/*
// group (change storefront-client-auth, design D6/D7):
//
//	X-Publishable-Key present → resolve it; an unusable key is a 404 and MUST
//	                            NOT fall back (a fallback would make revocation
//	                            meaningless).
//	otherwise                 → the Host header, i.e. the site's default shop.
//
// X-Site-Domain / X-Site-Path are deliberately NOT read here anymore. They
// were an unregistered, unrevocable equivalent of the publishable key; leaving
// them in place would mean a revoked key could simply switch back to the
// header and keep going. The header survives only on the render bundle
// endpoint, where the caller is the first-party SSR relaying the visitor's
// Host and the payload is published public content (see render.go).
func NewTenantMiddleware(resolver *tenant.Resolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			res, err := resolveStorefrontTenant(r, resolver)
			if err != nil {
				WriteResolveError(w, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(tenant.WithShopID(r.Context(), res.ShopID)))
		})
	}
}

// resolveStorefrontTenant applies the key-then-Host precedence.
func resolveStorefrontTenant(r *http.Request, resolver *tenant.Resolver) (*tenant.Resolution, error) {
	if key := r.Header.Get(PublishableKeyHeader); key != "" {
		return resolver.ResolveKey(r.Context(), key)
	}
	return resolver.Resolve(r.Context(), r.Host, "/")
}

// WriteResolveError maps resolver errors to design D12 responses.
func WriteResolveError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tenant.ErrShopDisabled):
		httpx.ShopDisabled(w) // 503 → storefront maintenance page
	case errors.Is(err, tenant.ErrKeyNotFound):
		// Unknown, malformed and revoked keys are indistinguishable by design
		// (spec storefront-client-auth). The dedicated code exists so a
		// merchant integrating a front end can tell "my key is wrong" apart
		// from "this page does not exist".
		httpx.WriteError(w, http.StatusNotFound, "unknown_publishable_key",
			"publishable key is unknown or revoked", nil)
	case errors.Is(err, tenant.ErrSiteNotFound),
		errors.Is(err, tenant.ErrNoDefaultShop),
		errors.Is(err, tenant.ErrShopUnderReview):
		httpx.NotFound(w)
	default:
		httpx.Internal(w)
	}
}
