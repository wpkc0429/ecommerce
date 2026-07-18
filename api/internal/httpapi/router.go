// Package httpapi wires the chi router: /healthz plus every public endpoint
// under the /api/v1 prefix (design D13 — v1 accepts additive changes only).
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/httpx"
)

// Deps carries the dependencies of the HTTP layer. Fields are added as
// vertical slices land; nil-able deps gate their route groups.
type Deps struct {
	Cfg *config.Config
	Log *slog.Logger

	Health func() error // optional extra health probe (e.g. DB ping)

	AdminAuth  *AdminAuthHandler
	MemberAuth *MemberAuthHandler
	Roles      *RolesHandler
	Sites      *SitesHandler
	Themes     *ThemesHandler
	Pages      *PagesHandler
	Render     *RenderHandler

	AdminMW  func(http.Handler) http.Handler // admin JWT authentication
	TenantMW func(http.Handler) http.Handler // storefront tenant resolution
	MemberMW func(http.Handler) http.Handler // member JWT auth (requires TenantMW)
}

// New assembles the full router.
func New(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(requestLogger(d.Log))
	r.Use(recoverer(d.Log))
	if d.Cfg != nil && len(d.Cfg.CORSAllowedOrigins) > 0 {
		r.Use(NewCORSMiddleware(d.Cfg.CORSAllowedOrigins))
	}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if d.Health != nil {
			if err := d.Health(); err != nil {
				httpx.WriteError(w, http.StatusServiceUnavailable, "unhealthy", err.Error(), nil)
				return
			}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/api/v1", func(v1 chi.Router) {
		// ── Admin (back office) ────────────────────────────────
		v1.Route("/admin", func(ar chi.Router) {
			if d.AdminAuth != nil {
				ar.Post("/auth/login", d.AdminAuth.Login)
				ar.Post("/auth/refresh", d.AdminAuth.RefreshToken)
				ar.Post("/auth/logout", d.AdminAuth.Logout)
			}
			if d.AdminMW != nil {
				ar.Group(func(pr chi.Router) {
					pr.Use(d.AdminMW)
					if d.AdminAuth != nil {
						pr.Get("/me", d.AdminAuth.Me)
					}
					// Merchant-scoped resources share the /shops/{shopID}
					// group; the target shop always comes from the URL and is
					// enforced by the RBAC middleware.
					pr.Route("/shops/{shopID}", func(sh chi.Router) {
						if d.Roles != nil {
							d.Roles.MountShop(sh)
						}
						if d.Themes != nil {
							d.Themes.MountShop(sh)
						}
						if d.Pages != nil {
							d.Pages.MountShop(sh)
						}
					})
					// Platform-level resources.
					if d.Roles != nil {
						d.Roles.MountPlatform(pr)
					}
					if d.Sites != nil {
						d.Sites.MountPlatform(pr)
					}
					if d.Themes != nil {
						d.Themes.MountPlatform(pr)
					}
				})
			}
		})

		// ── Storefront member APIs (tenant context required) ───
		if d.MemberAuth != nil && d.TenantMW != nil {
			v1.Route("/shop", func(sr chi.Router) {
				sr.Use(d.TenantMW)
				sr.Post("/auth/register", d.MemberAuth.Register)
				sr.Post("/auth/login", d.MemberAuth.Login)
				sr.Post("/auth/refresh", d.MemberAuth.RefreshToken)
				sr.Post("/auth/logout", d.MemberAuth.Logout)
				if d.MemberMW != nil {
					sr.Group(func(mr chi.Router) {
						mr.Use(d.MemberMW)
						mr.Get("/me", d.MemberAuth.Me)
					})
				}
			})
		}

		// ── Render bundle API (SSR / native apps) ──────────────
		if d.Render != nil {
			v1.Get("/render/page", d.Render.Page)       // resolves tenant internally from X-Site-Domain/Host + path
			v1.Get("/render/preview", d.Render.Preview) // short-lived preview token variant
		}

		v1.NotFound(func(w http.ResponseWriter, _ *http.Request) { httpx.NotFound(w) })
		v1.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
			httpx.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		})
	})

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { httpx.NotFound(w) })
	return r
}
