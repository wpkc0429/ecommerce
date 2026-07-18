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
			if !snap.HasPlatformRole || !snap.Allows(node) {
				httpx.Forbidden(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
