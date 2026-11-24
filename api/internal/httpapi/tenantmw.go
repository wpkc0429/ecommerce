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
// group from the X-Publishable-Key header (change storefront-client-auth,
// design D6/D7). A missing, unknown, malformed or revoked key is a 404
// (unknown_publishable_key) and there is no fallback: the Host-based site
// resolution that used to back this up was removed by change
// retire-hosted-storefront-and-themes. X-Site-Domain / X-Site-Path are not
// read either.
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

// resolveStorefrontTenant resolves the publishable key; no key is the same
// answer as an unknown key.
func resolveStorefrontTenant(r *http.Request, resolver *tenant.Resolver) (*tenant.Resolution, error) {
	return resolver.ResolveKey(r.Context(), r.Header.Get(PublishableKeyHeader))
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
	case errors.Is(err, tenant.ErrShopUnderReview):
		httpx.NotFound(w)
	default:
		httpx.Internal(w)
	}
}
