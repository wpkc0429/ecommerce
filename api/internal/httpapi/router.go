// Package httpapi wires the chi router: /healthz plus every public endpoint
// under the /api/v1 prefix (design D13 — v1 accepts additive changes only).
package httpapi

import (
	"log/slog"
	"net/http"
	"net/netip"

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
	Points     *PointsHandler     // change member-tiers-and-points (member self-service + merchant back office)
	Members    *MembersHandler    // change member-directory (merchant back office, read-only customer roster)
	ClientAuth *ClientAuthHandler // change storefront-client-auth (publishable keys + allowed origins)
	Guest      *GuestHandler      // change guest-checkout (unauthenticated checkout + order access token routes)
	// change merchant-reports: the platform's first aggregate queries, and the
	// merchant-scoped settings endpoint that gives them a time zone to cut days by.
	Reports      *ReportsHandler
	ShopSettings *ShopSettingsHandler
	// PaymentCredentials: change ecpay-per-shop-credentials (merchant-scoped
	// management of its own ecpay MerchantID/HashKey/HashIV/ChoosePayment).
	PaymentCredentials *PaymentCredentialsHandler
	// Media: change product-images-r2 (upload intents, product image
	// sub-resource, CMS content images, media config). Always set in
	// production: with storage unconfigured its routes stay mounted and answer
	// 503 media_not_configured (the route table never depends on settings).
	Media *MediaHandler
	// Accounts / Provisioning: change platform-account-provisioning (platform
	// account management, password setup endpoints, one-call shop opening).
	Accounts     *AccountsHandler
	Provisioning *ProvisioningHandler
	// Team: change merchant-staff-role (owner-facing member management).
	Team *TeamHandler

	AdminMW       func(http.Handler) http.Handler // admin JWT authentication
	TenantMW      func(http.Handler) http.Handler // storefront tenant resolution
	MemberMW      func(http.Handler) http.Handler // member JWT auth (requires TenantMW)
	OrderAccessMW func(http.Handler) http.Handler // order access token auth (requires TenantMW)

	// CORSOrigins resolves per-shop registered browser origins (change
	// storefront-client-auth). nil → only Cfg.CORSAllowedOrigins applies,
	// which is the pre-change behavior every existing test relies on.
	CORSOrigins OriginRegistry

	// Rate limiting (design auth-rate-limiting): nil disables the check for
	// that route (e.g. in tests that don't wire Redis) — orPassthrough makes
	// every unset field behave exactly as before this feature existed.
	AdminLoginRateLimit     func(http.Handler) http.Handler
	AdminRefreshRateLimit   func(http.Handler) http.Handler
	MemberLoginRateLimit    func(http.Handler) http.Handler
	MemberRegisterRateLimit func(http.Handler) http.Handler
	MemberRefreshRateLimit  func(http.Handler) http.Handler
	GuestCheckoutRateLimit  func(http.Handler) http.Handler
	// AdminPasswordSetupRateLimit guards the two unauthenticated password
	// setup endpoints (IP-scoped).
	AdminPasswordSetupRateLimit func(http.Handler) http.Handler
	// AdminInviteRateLimit bounds invitation creation per shop + caller; the
	// unauthenticated invitation endpoints reuse AdminPasswordSetupRateLimit
	// (change merchant-staff-role).
	AdminInviteRateLimit func(http.Handler) http.Handler
}

// New assembles the full router.
func New(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// Client IP before anything that logs or rate-limits by it. Only
	// CF-Connecting-IP from Cfg.TrustedProxies is believed (see clientip.go).
	var trustedProxies []netip.Prefix
	if d.Cfg != nil {
		trustedProxies = d.Cfg.TrustedProxies
	}
	r.Use(NewClientIPMiddleware(trustedProxies))
	r.Use(requestLogger(d.Log))
	r.Use(recoverer(d.Log))
	// CORS is mounted whenever either source of allowed origins is wired: a
	// deployment may have an empty env list and rely entirely on registered
	// per-shop origins (change storefront-client-auth).
	var corsOrigins []string
	if d.Cfg != nil {
		corsOrigins = d.Cfg.CORSAllowedOrigins
	}
	if len(corsOrigins) > 0 || d.CORSOrigins != nil {
		r.Use(NewCORSMiddleware(corsOrigins, d.CORSOrigins))
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
			if d.Accounts != nil {
				d.Accounts.MountAuth(ar, d.AdminPasswordSetupRateLimit)
			}
			if d.Team != nil {
				d.Team.MountAuth(ar, d.AdminPasswordSetupRateLimit)
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
						if d.Points != nil {
							d.Points.MountShopAdmin(sh)
						}
						if d.Members != nil {
							d.Members.MountShopAdmin(sh)
						}
						if d.ClientAuth != nil {
							d.ClientAuth.MountShopAdmin(sh)
						}
						if d.Reports != nil {
							d.Reports.MountShopAdmin(sh)
						}
						if d.ShopSettings != nil {
							d.ShopSettings.MountShopAdmin(sh)
						}
						if d.PaymentCredentials != nil {
							d.PaymentCredentials.MountShopAdmin(sh)
						}
						if d.Media != nil {
							d.Media.MountShopAdmin(sh)
						}
						if d.Provisioning != nil {
							d.Provisioning.MountShop(sh)
						}
						if d.Team != nil {
							d.Team.MountShopAdmin(sh, d.AdminInviteRateLimit)
						}
					})
					if d.Media != nil {
						d.Media.MountAdmin(pr)
					}
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
					if d.Accounts != nil {
						d.Accounts.MountPlatform(pr)
					}
					if d.Provisioning != nil {
						d.Provisioning.MountPlatform(pr)
					}
					if d.Team != nil {
						d.Team.MountAdmin(pr)
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
						if d.Points != nil {
							d.Points.MountShop(mr)
						}
					})
				}
				// change guest-checkout: an unauthenticated checkout plus the
				// routes an order access token can reach. Mounted inside the
				// tenant-resolved group but deliberately OUTSIDE MemberMW —
				// these callers have no member identity, and the token-guarded
				// routes carry their own middleware whose context value is not
				// the one member handlers read (design D4).
				if d.Guest != nil {
					sr.Route("/guest", func(gr chi.Router) {
						d.Guest.MountShop(gr, d.GuestCheckoutRateLimit)
						if d.OrderAccessMW != nil {
							gr.Group(func(tr chi.Router) {
								tr.Use(d.OrderAccessMW)
								d.Guest.MountOrderAccess(tr)
							})
						}
					})
				}
				if d.Products != nil {
					d.Products.MountPublic(sr)
				}
				if d.Categories != nil {
					d.Categories.MountPublic(sr)
				}
				// change order-extras: the storefront needs the shop's
				// selectable shipping methods (and their fees) before any
				// identity exists — a guest sees the checkout total before
				// entering an email. Public, tenant-scoped, active only.
				if d.Shipping != nil {
					d.Shipping.MountPublic(sr)
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
			// change ecpay-per-shop-credentials (design D2): additive route for
			// providers whose credentials are resolved per shop — {shopID} is
			// unverified until the provider's own signature check succeeds; it
			// only selects which shop's secret to try. mock keeps using the
			// route above, unchanged.
			v1.Post("/webhooks/payments/{provider}/{shopID}", d.Payments.Webhook)
		}

		// ── Render bundle API (SSR / native apps) ──────────────
		if d.Render != nil {
			v1.Get("/render/page", d.Render.Page)       // resolves tenant internally: X-Publishable-Key → X-Site-Domain → Host
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
