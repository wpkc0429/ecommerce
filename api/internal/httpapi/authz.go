package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/rbac"
)

// AuthzMW guards admin routes with the three-tier decision (task 4.3).
// The target shop always comes from the URL ({shopID}) — never from client
// payloads (spec multi-tenancy: 後台 API 的目標 shop 由授權上下文決定).
type AuthzMW struct {
	Engine *rbac.Engine
}

func shopIDParam(r *http.Request) (int, bool) {
	id, err := strconv.Atoi(chi.URLParam(r, "shopID"))
	return id, err == nil && id > 0
}

// RequireShopPermission authenticates the admin identity, applies the
// cross-shop membership guard, then decides the permission for the shop in
// the URL.
func (a *AuthzMW) RequireShopPermission(node string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := auth.AdminFrom(r.Context())
			if !ok {
				httpx.Unauthorized(w)
				return
			}
			shopID, ok := shopIDParam(r)
			if !ok {
				httpx.NotFound(w)
				return
			}
			snap, err := a.Engine.Snapshot(r.Context(), id.UserID, shopID)
			if err != nil {
				httpx.Internal(w)
				return
			}
			// Cross-shop guard runs before the permission decision (spec rbac).
			if !snap.CanAccessShop() {
				httpx.Forbidden(w)
				return
			}
			if !snap.Allows(node) {
				httpx.Forbidden(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireShopAnyPermission is RequireShopPermission for a route that accepts
// any one of several equivalent nodes. It exists for the page.* / content.*
// compatibility aliases (change headless-content-store, Q1): the compatibility
// endpoints keep answering to page.* and now also to the matching content.*
// node, so a role that holds either one passes.
func (a *AuthzMW) RequireShopAnyPermission(nodes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := auth.AdminFrom(r.Context())
			if !ok {
				httpx.Unauthorized(w)
				return
			}
			shopID, ok := shopIDParam(r)
			if !ok {
				httpx.NotFound(w)
				return
			}
			snap, err := a.Engine.Snapshot(r.Context(), id.UserID, shopID)
			if err != nil {
				httpx.Internal(w)
				return
			}
			if !snap.CanAccessShop() {
				httpx.Forbidden(w)
				return
			}
			for _, n := range nodes {
				if snap.Allows(n) {
					next.ServeHTTP(w, r)
					return
				}
			}
			httpx.Forbidden(w)
		})
	}
}

// RequireShopAccess authenticates the admin identity and applies only the
// cross-shop membership guard — no permission node is required (change
// admin-commerce-catalog, design D5).
//
// It exists for exactly one kind of route: self-information about the caller's
// own standing in this shop. "What may I do here?" has to be answerable by
// everyone who may enter the shop at all; guarding it with some existing node
// (shop.view, say) would hand a 403 to a custom role that holds only
// product.view, and that user would then face a blank admin — using the
// permission system to defeat the permission system. It leaks nothing either:
// the caller can already obtain the same answer by trying.
func (a *AuthzMW) RequireShopAccess() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := auth.AdminFrom(r.Context())
			if !ok {
				httpx.Unauthorized(w)
				return
			}
			shopID, ok := shopIDParam(r)
			if !ok {
				httpx.NotFound(w)
				return
			}
			snap, err := a.Engine.Snapshot(r.Context(), id.UserID, shopID)
			if err != nil {
				httpx.Internal(w)
				return
			}
			if !snap.CanAccessShop() {
				httpx.Forbidden(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequirePlatformPermission guards platform-level admin routes: the caller
// must hold a platform-scope role and the permission at platform ctx.
func (a *AuthzMW) RequirePlatformPermission(node string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := auth.AdminFrom(r.Context())
			if !ok {
				httpx.Unauthorized(w)
				return
			}
			snap, err := a.Engine.Snapshot(r.Context(), id.UserID, rbac.PlatformCtx)
			if err != nil {
				httpx.Internal(w)
				return
			}
			if !snap.IsPlatformAdmin() || !snap.Allows(node) {
				httpx.Forbidden(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
