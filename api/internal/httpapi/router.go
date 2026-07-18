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
	Shops      *ShopsHandler
	Themes     *ThemesHandler
	Pages      *PagesHandler
	Render     *RenderHandler
	Categories *CategoriesHandler // change product-catalog
	Products   *ProductsHandler   // change product-catalog (merchant CRUD + public read)
	Cart       *CartHandler       // change shopping-cart (member self-service, no RBAC)
	Orders     *OrderHandler      // change order-management (member self-service + merchant back office)
	Payments   *PaymentHandler    // change payment-integration (member self-service + merchant back office + provider webhook)
	Shipping   *ShippingHandler   // change shipping-logistics (member self-service + merchant back office)

	AdminMW  func(http.Handler) http.Handler // admin JWT authentication
	TenantMW func(http.Handler) http.Handler // storefront tenant resolution
	MemberMW func(http.Handler) http.Handler // member JWT auth (requires TenantMW)

	// Rate limiting (design auth-rate-limiting): nil disables the check for
	// that route (e.g. in tests that don't wire Redis) — orPassthrough makes
	// every unset field behave exactly as before this feature existed.
	AdminLoginRateLimit     func(http.Handler) http.Handler
	AdminRefreshRateLimit   func(http.Handler) http.Handler
	MemberLoginRateLimit    func(http.Handler) http.Handler
	MemberRegisterRateLimit func(http.Handler) http.Handler
	MemberRefreshRateLimit  func(http.Handler) http.Handler
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
				ar.With(orPassthrough(d.AdminLoginRateLimit)).Post("/auth/login", d.AdminAuth.Login)
				ar.With(orPassthrough(d.AdminRefreshRateLimit)).Post("/auth/refresh", d.AdminAuth.RefreshToken)
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
						if d.Categories != nil {
							d.Categories.MountShop(sh)
						}
						if d.Products != nil {
							d.Products.MountShop(sh)
						}
						if d.Orders != nil {
							d.Orders.MountShopAdmin(sh)
						}
						if d.Payments != nil {
							d.Payments.MountShopAdmin(sh)
						}
						if d.Shipping != nil {
							d.Shipping.MountShopAdmin(sh)
						}
					})
					// Platform-level resources.
					if d.Roles != nil {
						d.Roles.MountPlatform(pr)
					}
					if d.Sites != nil {
						d.Sites.MountPlatform(pr)
					}
					if d.Shops != nil {
						d.Shops.MountPlatform(pr)
					}
					if d.Themes != nil {
						d.Themes.MountPlatform(pr)
					}
				})
			}
		})

		// ── Storefront member APIs + public catalog (tenant context
		// required) ─────────────────────────────────────────────────
		// change product-catalog: the gate no longer requires MemberAuth —
		// the public product endpoints only need tenant resolution. Member
		// auth routes stay nested behind their own nil-check so existing
		// behavior (routes absent unless MemberAuth is wired) is unchanged.
		if d.TenantMW != nil {
			v1.Route("/shop", func(sr chi.Router) {
				sr.Use(d.TenantMW)
				if d.MemberAuth != nil {
					// Rate limit middlewares run after TenantMW so shop-scoped
					// rules (design auth-rate-limiting D2) can read tenant.ShopID.
					sr.With(orPassthrough(d.MemberRegisterRateLimit)).Post("/auth/register", d.MemberAuth.Register)
					sr.With(orPassthrough(d.MemberLoginRateLimit)).Post("/auth/login", d.MemberAuth.Login)
					sr.With(orPassthrough(d.MemberRefreshRateLimit)).Post("/auth/refresh", d.MemberAuth.RefreshToken)
					sr.Post("/auth/logout", d.MemberAuth.Logout)
				}
				// change shopping-cart: the MemberMW-protected group is no
				// longer nested under `d.MemberAuth != nil` — Cart only needs
				// member authentication, not the auth handler itself (mirrors
				// product-catalog design D8's loosening of the public-route
				// mount condition). Real deployments always wire both, so
				// behavior there is unchanged.
				if d.MemberMW != nil {
					sr.Group(func(mr chi.Router) {
						mr.Use(d.MemberMW)
						if d.MemberAuth != nil {
							mr.Get("/me", d.MemberAuth.Me)
						}
						if d.Cart != nil {
							d.Cart.MountShop(mr)
						}
						if d.Orders != nil {
							d.Orders.MountShop(mr)
						}
						if d.Payments != nil {
							d.Payments.MountShop(mr)
						}
						if d.Shipping != nil {
							d.Shipping.MountShop(mr)
						}
					})
				}
				if d.Products != nil {
					d.Products.MountPublic(sr)
				}
			})
		}

		// ── Payment provider webhooks (change payment-integration, design
		// D7) ───────────────────────────────────────────────────────
		// Deliberately outside every tenant/member/admin middleware group:
		// the caller is a payment provider's server, authenticated by
		// signature (Provider.VerifyWebhook), not a JWT or resolved shop
		// domain.
		if d.Payments != nil {
			v1.Post("/webhooks/payments/{provider}", d.Payments.Webhook)
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
