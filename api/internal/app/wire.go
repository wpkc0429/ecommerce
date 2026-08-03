package app

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cart"
	"ksdevworks/ecommerce/api/internal/catalog"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/cryptbox"
	"ksdevworks/ecommerce/api/internal/database"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/guest"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/idempotency"
	"ksdevworks/ecommerce/api/internal/member"
	"ksdevworks/ecommerce/api/internal/notify"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/payment"
	"ksdevworks/ecommerce/api/internal/payment/ecpay"
	"ksdevworks/ecommerce/api/internal/points"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/render"
	"ksdevworks/ecommerce/api/internal/report"
	"ksdevworks/ecommerce/api/internal/shipping"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// wire connects infrastructure (PostgreSQL, Redis) and attaches domain
// services to the router deps. Filled in progressively: auth (M2) done;
// RBAC (M3), tenancy (M4), CMS + rendering (M5) pending.
func (a *App) wire(ctx context.Context, deps *httpapi.Deps) error {
	cfg := a.cfg

	client, db, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	a.closers = append(a.closers, client.Close)
	deps.Health = func() error { return db.Ping() }

	// Redis is cache-only; a failed ping degrades (DB fallback) rather than
	// blocking boot (design risk: Redis 故障 degrade 而非 down). Timeouts are
	// tight and retries capped so an outage costs bounded latency per op
	// instead of stalling render requests.
	rdb := redis.NewClient(&redis.Options{
		Addr:         cfg.RedisAddr,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
		MaxRetries:   1,
	})
	a.closers = append(a.closers, rdb.Close)
	if err := rdb.Ping(ctx).Err(); err != nil {
		a.log.Warn("redis unreachable at boot — caches degrade to DB", "addr", cfg.RedisAddr, "err", err)
	}
	a.redis = rdb

	issuer, err := auth.NewTokenIssuer(cfg.AdminJWTSecret, cfg.MemberJWTSecret, cfg.JWTIssuer, cfg.AccessTokenTTL)
	if err != nil {
		return fmt.Errorf("token issuer: %w", err)
	}
	refresh := auth.NewRefreshService(client, cfg.RefreshTokenTTL)

	deps.AdminAuth = &httpapi.AdminAuthHandler{Client: client, Issuer: issuer, Refresh: refresh, Log: a.log}
	deps.MemberAuth = &httpapi.MemberAuthHandler{Client: client, Issuer: issuer, Refresh: refresh, Log: a.log}
	deps.AdminMW = httpapi.NewAdminMiddleware(issuer)
	deps.MemberMW = httpapi.NewMemberMiddleware(issuer)
	// change guest-checkout: a third token family (aud=order:{shop}) guarding
	// only the routes that act on the one order it names.
	deps.OrderAccessMW = httpapi.NewOrderAccessMiddleware(issuer)

	// Auth endpoint rate limiting (design auth-rate-limiting): reuses the
	// same Redis client as the render/authz caches — rate limiting is
	// defense-in-depth, not a new infra dependency.
	limiter := ratelimit.New(rdb, a.log)
	deps.AdminLoginRateLimit = httpapi.NewAdminLoginRateLimit(limiter, cfg.RateLimit)
	deps.AdminRefreshRateLimit = httpapi.NewAdminRefreshRateLimit(limiter, cfg.RateLimit)
	deps.MemberLoginRateLimit = httpapi.NewMemberLoginRateLimit(limiter, cfg.RateLimit)
	deps.MemberRegisterRateLimit = httpapi.NewMemberRegisterRateLimit(limiter, cfg.RateLimit)
	deps.MemberRefreshRateLimit = httpapi.NewMemberRefreshRateLimit(limiter, cfg.RateLimit)
	deps.GuestCheckoutRateLimit = httpapi.NewGuestCheckoutRateLimit(limiter, cfg.RateLimit)

	engine := rbac.NewEngine(client, rdb, a.log)
	authz := &httpapi.AuthzMW{Engine: engine}
	deps.Roles = &httpapi.RolesHandler{Client: client, Engine: engine, Authz: authz, Log: a.log}

	// Domain events → cache invalidation (design D8: 失效集中於領域事件層).
	dispatcher := events.NewDispatcher(a.log)

	resolver := tenant.NewResolver(client, rdb, a.log)
	dispatcher.Subscribe(func(ctx context.Context, e events.Event) {
		if ev, ok := e.(events.SiteMappingChanged); ok {
			resolver.InvalidateHosts(ctx, ev.Hosts...)
			// change storefront-client-auth (design D10): this event is also
			// what a shop status change publishes, and the cached publishable
			// key entries embed that status — so a disabled shop's keys stop
			// serving at the same moment its domains do.
			for _, shopID := range ev.ShopIDs {
				resolver.InvalidateShopKeys(ctx, shopID)
			}
		}
	})
	// change storefront-client-auth: key/origin mutations drop their own cache
	// entries so revocation and registration take effect immediately.
	dispatcher.Subscribe(func(ctx context.Context, e events.Event) {
		if ev, ok := e.(events.StorefrontClientChanged); ok {
			resolver.InvalidateKeys(ctx, ev.Keys...)
			resolver.InvalidateOrigins(ctx, ev.Origins...)
		}
	})

	deps.TenantMW = httpapi.NewTenantMiddleware(resolver)
	deps.CORSOrigins = resolver
	deps.Sites = &httpapi.SitesHandler{Client: client, Dispatcher: dispatcher, Log: a.log, Authz: authz}
	deps.ClientAuth = &httpapi.ClientAuthHandler{Client: client, Dispatcher: dispatcher, Authz: authz, Log: a.log}

	// CMS engine + render pipeline (M5).
	renderCache := render.NewCache(rdb, a.log)
	invalidator := &render.Invalidator{Cache: renderCache, Client: client, Log: a.log}
	dispatcher.Subscribe(invalidator.Handle)

	// member-tiers-and-points (change member-tiers-and-points, design D1):
	// the dispatcher's first non-cache subscriber — reacts to
	// events.OrderPaymentSucceeded/OrderReturned published by payment/
	// shipping below. Needs *ent.Client only; it never imports order/
	// payment/shipping.
	pointsService := &points.Service{Client: client, Log: a.log}
	dispatcher.Subscribe(pointsService.Handle)

	cmsService := &cms.Service{Client: client, Dispatcher: dispatcher}
	deps.Themes = &httpapi.ThemesHandler{Client: client, Service: cmsService, Authz: authz, Log: a.log}
	deps.Shops = &httpapi.ShopsHandler{Client: client, Service: cmsService, Authz: authz, Log: a.log}
	deps.Pages = &httpapi.PagesHandler{Client: client, Service: cmsService, Issuer: issuer, Cfg: cfg, Authz: authz, Log: a.log}

	// Product catalog (change product-catalog): no cache/events involved
	// (design D8 — the public endpoint reads the DB directly), so the
	// service only needs the ent client.
	catalogService := &catalog.Service{Client: client}
	deps.Categories = &httpapi.CategoriesHandler{Client: client, Service: catalogService, Authz: authz, Log: a.log}
	deps.Products = &httpapi.ProductsHandler{Client: client, Service: catalogService, Authz: authz, Log: a.log}

	// Shopping cart (change shopping-cart): member self-service, no RBAC/
	// events involved (design D9 — no ConflictError, no cache to invalidate).
	cartService := &cart.Service{Client: client}
	deps.Cart = &httpapi.CartHandler{Client: client, Service: cartService, Log: a.log}

	// Order notification emails (change order-notification-email): a second
	// non-cache subscriber, alongside points. It reacts to events.OrderPlaced
	// (published by order.Checkout below) and events.OrderPaymentSucceeded
	// (published by payment), and — like points — imports none of them.
	//
	// The Sender is where "are notifications on" is decided (design D10): no
	// SMTP credentials means DiscardSender, which makes Handle a no-op and
	// leaves this deployment behaving byte for byte as it did before this change.
	// **The Subscribe call below is unconditional on purpose** — hanging it off
	// a config check would make the subscriber lock (wiring check ⑦, an equality
	// comparison) depend on the environment of whoever runs the tests.
	var mailSender notify.Sender = notify.DiscardSender{}
	if cfg.SMTP.Enabled() {
		mailSender = &notify.SMTPSender{
			Host: cfg.SMTP.Host, Port: cfg.SMTP.Port,
			Username: cfg.SMTP.Username, Password: cfg.SMTP.Password,
			From: cfg.SMTP.From, FromName: cfg.SMTP.FromName,
			Timeout: cfg.SMTP.Timeout,
		}
	} else {
		a.log.Info("SMTP credentials not configured — order notification emails are disabled")
	}
	notifier := &notify.Service{
		Client: client, Sender: mailSender, Log: a.log,
		Workers: cfg.Notify.Workers, QueueSize: cfg.Notify.QueueSize, Timeout: cfg.SMTP.Timeout,
	}
	notifier.Start()
	// Drains whatever the workers already accepted; a no-op when disabled.
	a.closers = append(a.closers, notifier.Close)
	dispatcher.Subscribe(notifier.Handle)

	// Order management (change order-management): checkout consumes the
	// active cart directly via ent (design D9); member routes are RBAC-free
	// like Cart, admin routes reuse the same authz engine as Products.
	//
	// change order-notification-email: order.Service becomes a publisher for the
	// first time (events.OrderPlaced, from inside Checkout after its transaction
	// commits — design D15). It must be THE dispatcher, not a new one: a
	// publisher holding its own instance produces exactly the symptom of nobody
	// being subscribed, which is why wiring check ⑦ asserts the single instance.
	orderService := &order.Service{Client: client, Dispatcher: dispatcher}

	// Checkout idempotency (change checkout-idempotency): one service shared by
	// both order-placing endpoints, so there is exactly one implementation of the
	// duplicate-submission guard — the same discipline that keeps stock deduction
	// to one implementation.
	//
	// NOTE: nothing in `go test ./...` reaches this file (every integration test
	// builds its own router), so forgetting to attach idempotencyService to a
	// handler below would leave the whole suite green while the real server
	// silently ignored Idempotency-Key. e2e/commerce-path.mjs asserts the replay
	// for exactly this reason.
	idempotencyService := &idempotency.Service{Client: client, TTL: cfg.CheckoutIdempotencyTTL}
	deps.Orders = &httpapi.OrderHandler{
		Client: client, Service: orderService, Authz: authz, Log: a.log,
		Idempotency: idempotencyService,
	}

	// Payment integration (change payment-integration): a provider registry
	// of one demo/reference provider today (design D1/D2/D9) — adding a
	// real provider later means constructing it here and adding it to this
	// map, nothing else changes. Member/admin routes reuse orderService as
	// the sole controlled entry point for advancing orders.payment_status
	// (design D6/D7); the webhook route needs no auth middleware of its own.
	//
	// change ecpay-payment-provider: the registry now has two entries, and that
	// really was all the wiring the abstraction needed — the interface itself
	// grew three capabilities (RedirectForm/WebhookAck/OutcomePending, see the
	// package doc), but httpapi's control flow and the webhook route did not
	// change shape. DefaultProvider stays "mock" unless configuration says
	// otherwise (design D13), so this line changes nothing for existing callers.
	//
	// change ecpay-per-shop-credentials: ecpay's registry entry is now a
	// ShopScopedProvider (ecpay.ShopResolver) instead of a fixed *ecpay.Provider
	// — Config.MerchantID/HashKey/HashIV came from ECPAY_* env vars before this
	// change, now from each shop's own encrypted row. CheckoutURL/ClientBackURL/
	// ReturnURL stay platform-wide (design D7): which 綠界 environment the whole
	// deployment talks to, appended per shop id at resolve time (design D2).
	credentialsBox, err := cryptbox.ParseKey(cfg.PaymentCredentialsEncryptionKey)
	if err != nil {
		return fmt.Errorf("wire: payment credentials encryption key: %w", err)
	}
	mockProvider := payment.NewMockProvider(cfg.PaymentMockWebhookSecret)
	ecpayResolver := &ecpay.ShopResolver{
		Client:        client,
		Box:           credentialsBox,
		CheckoutURL:   cfg.ECPay.CheckoutURL,
		ClientBackURL: cfg.ECPay.ClientBackURL,
		ReturnURLBase: cfg.ECPay.ReturnURL,
	}
	paymentService := &payment.Service{
		Client:     client,
		Orders:     orderService,
		Dispatcher: dispatcher,
		// The only consumer is the "paid, but the order was already cancelled"
		// warning (design D9) — a state only a human can resolve.
		Log: a.log,
		Providers: map[string]any{
			mockProvider.Name():  mockProvider,
			ecpayResolver.Name(): ecpayResolver,
		},
		DefaultProvider: cfg.PaymentDefaultProvider,
	}
	deps.Payments = &httpapi.PaymentHandler{Client: client, Service: paymentService, Authz: authz, Log: a.log}
	// The merchant-scoped credentials endpoint that lets a shop set its own
	// ecpay MerchantID/HashKey/HashIV/ChoosePayment (change
	// ecpay-per-shop-credentials, design D6).
	deps.PaymentCredentials = &httpapi.PaymentCredentialsHandler{
		Client: client, Authz: authz, Log: a.log, Box: credentialsBox,
	}

	// Shipping logistics (change shipping-logistics): shipping methods CRUD
	// + shipment records, no external carrier integration (design D9 — no
	// new config values needed, unlike payment's mock webhook secret).
	// Member/admin routes reuse orderService as the sole controlled entry
	// point for advancing orders.fulfillment_status (design D4/D6).
	shippingService := &shipping.Service{Client: client, Orders: orderService, Dispatcher: dispatcher}
	deps.Shipping = &httpapi.ShippingHandler{Client: client, Service: shippingService, Authz: authz, Log: a.log}

	// member-tiers-and-points HTTP layer (points.Service was already
	// constructed and subscribed above, alongside the render invalidator).
	deps.Points = &httpapi.PointsHandler{Client: client, Service: pointsService, Authz: authz, Log: a.log}

	// Customer roster (change member-directory): a read-only back-office view
	// over `members`. It owns no state and subscribes to no events; the whole
	// design content is which fields it emits (httpapi.memberJSON) and the fact
	// that every query carries shop_id explicitly, because back-office paths run
	// outside the tenant context (member-identity-per-shop design D4).
	deps.Members = &httpapi.MembersHandler{
		Client: client, Service: &member.Service{Client: client}, Authz: authz, Log: a.log,
	}

	// Guest checkout (change guest-checkout): a coordinator over the services
	// already constructed above — it creates no state of its own and, in
	// particular, calls orderService.Checkout rather than reimplementing it
	// (design D1). The handler additionally reuses paymentService and
	// shippingService so a guest can pay for and track the one order its access
	// token names.
	guestService := &guest.Service{Client: client, Cart: cartService, Orders: orderService}
	deps.Guest = &httpapi.GuestHandler{
		Client:      client,
		Service:     guestService,
		Orders:      orderService,
		Payments:    paymentService,
		Shipping:    shippingService,
		Issuer:      issuer,
		Cfg:         cfg,
		Log:         a.log,
		Idempotency: idempotencyService,
	}

	// Merchant reports (change merchant-reports): the platform's first aggregate
	// queries. A pure read path — it writes nothing, publishes no event and
	// subscribes to none, so wiring check ⑦'s expectedSubscribers is untouched
	// (design D9).
	//
	// DefaultTimezone is a plain string, so the nil-dependency audit (check ⑤)
	// cannot report it as unset. shoptz.Resolve rejects an empty value loudly
	// for exactly that reason — otherwise forgetting this line would silently
	// cut every unconfigured shop's days in UTC.
	deps.Reports = &httpapi.ReportsHandler{
		Client: client, Authz: authz, Log: a.log,
		Service: &report.Service{Client: client, DefaultTimezone: cfg.ReportDefaultTimezone},
	}
	// The merchant-scoped settings endpoint that owns shops.timezone. Kept
	// separate from ShopsHandler because that one is platform-scoped (design D7).
	deps.ShopSettings = &httpapi.ShopSettingsHandler{
		Client: client, Authz: authz, Log: a.log, DefaultTimezone: cfg.ReportDefaultTimezone,
	}

	deps.Render = &httpapi.RenderHandler{
		Resolver:  resolver,
		Assembler: &render.Assembler{Client: client},
		Cache:     renderCache,
		Issuer:    issuer,
		Engine:    engine,
		Log:       a.log,
	}

	return nil
}
