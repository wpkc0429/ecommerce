package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/account"
	"ksdevworks/ecommerce/api/internal/application"
	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cart"
	"ksdevworks/ecommerce/api/internal/catalog"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/content"
	"ksdevworks/ecommerce/api/internal/content/hook"
	"ksdevworks/ecommerce/api/internal/cryptbox"
	"ksdevworks/ecommerce/api/internal/database"
	"ksdevworks/ecommerce/api/internal/design"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/guest"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/idempotency"
	"ksdevworks/ecommerce/api/internal/media"
	"ksdevworks/ecommerce/api/internal/member"
	"ksdevworks/ecommerce/api/internal/notify"
	"ksdevworks/ecommerce/api/internal/order"
	"ksdevworks/ecommerce/api/internal/payment"
	"ksdevworks/ecommerce/api/internal/payment/ecpay"
	"ksdevworks/ecommerce/api/internal/points"
	"ksdevworks/ecommerce/api/internal/provision"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/render"
	"ksdevworks/ecommerce/api/internal/report"
	"ksdevworks/ecommerce/api/internal/shipping"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/turnstile"
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
		Password:     cfg.RedisPassword,
		DB:           cfg.RedisDB,
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
	deps.AdminPasswordSetupRateLimit = httpapi.NewAdminPasswordSetupRateLimit(limiter, cfg.RateLimit)

	engine := rbac.NewEngine(client, rdb, a.log)
	authz := &httpapi.AuthzMW{Engine: engine}
	// change admin-api-public-exposure D6: the lockout key HMAC is derived from
	// the admin JWT secret under its own label, so no new secret has to be
	// managed (and rotating the JWT secret merely resets the failure counters).
	lockoutKey := hmac.New(sha256.New, []byte(cfg.AdminJWTSecret))
	_, _ = lockoutKey.Write([]byte("login-lockout-key-v1"))
	deps.AdminAuth = &httpapi.AdminAuthHandler{
		Client: client, Issuer: issuer, Refresh: refresh, Engine: engine, Log: a.log,
		RefreshTTL: cfg.RefreshTokenTTL,
		Limiter:    limiter, LockoutFailures: cfg.RateLimit.LoginAccountFailures,
		LockoutWindow: cfg.RateLimit.LoginAccountWindow, LockoutSecret: lockoutKey.Sum(nil),
	}

	// Back-office account provisioning (change platform-account-provisioning).
	// Until the VPS move there is no outbound mail for admin accounts, so links
	// are always delivered manually (design D4).
	if cfg.Env == config.EnvProduction && cfg.AdminPublicURL == "http://localhost:3001" {
		a.log.Warn("ADMIN_PUBLIC_URL is the localhost default - password setup links will point at localhost; set it to the shared merchant admin origin")
	}
	accountService := &account.Service{
		Client: client, Engine: engine, Refresh: refresh, Delivery: account.ManualDelivery{},
		PublicURL: cfg.AdminPublicURL, SetupTTL: cfg.AdminSetupTokenTTL, ResetTTL: cfg.AdminResetTokenTTL,
		InviteTTL: cfg.AdminInviteTokenTTL,
	}
	deps.Accounts = &httpapi.AccountsHandler{Service: accountService, Authz: authz, Log: a.log}
	deps.Team = &httpapi.TeamHandler{Service: accountService, Engine: engine, Authz: authz, Log: a.log}
	deps.AdminInviteRateLimit = httpapi.NewAdminInviteRateLimit(limiter, cfg.RateLimit)
	deps.Roles = &httpapi.RolesHandler{Client: client, Engine: engine, Authz: authz, Log: a.log}

	// Domain events → cache invalidation (design D8: 失效集中於領域事件層).
	dispatcher := events.NewDispatcher(a.log)

	resolver := tenant.NewResolver(client, rdb, a.log)
	dispatcher.Subscribe(func(ctx context.Context, e events.Event) {
		if ev, ok := e.(events.ShopStatusChanged); ok {
			// change storefront-client-auth (design D10): the cached publishable
			// key entries embed the shop's status — so a disabled shop's keys
			// stop serving at the same moment its status changes.
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

	// change admin-api-public-exposure: Cloudflare Access JWT validation
	// (D2), admin host binding + PLATFORM_ADMIN_HOSTS (D4/D10). All three
	// middlewares are always mounted and pass through when unconfigured.
	var cfVerifier *httpapi.CFAccessVerifier
	if cfg.CFAccess.Enabled() {
		cfVerifier = httpapi.NewCFAccessVerifier(cfg.CFAccess, a.log)
	} else if cfg.Env == config.EnvProduction {
		a.log.Warn("CF_ACCESS_TEAM_DOMAIN / CF_ACCESS_AUD not set - the back-office API does not verify Cloudflare Access tokens; set them once an Access application fronts the admin domains")
	}
	deps.AdminAccessMW = httpapi.NewCFAccessMiddleware(cfVerifier, a.log)
	deps.AdminBindEmailMW = func(next http.Handler) http.Handler { return next }
	if cfg.CFAccess.Enabled() && cfg.CFAccess.BindEmail {
		deps.AdminBindEmailMW = httpapi.NewCFAccessBindEmailMiddleware(client, a.log)
	}
	if len(cfg.PlatformAdminHosts) == 0 && cfg.Env == config.EnvProduction {
		a.log.Warn("PLATFORM_ADMIN_HOSTS not set - platform-level back-office APIs are callable from any host")
	}
	deps.AdminHostMW = httpapi.NewAdminHostBindingMiddleware(resolver, cfg.PlatformAdminHosts)
	deps.Branding = &httpapi.BrandingHandler{Client: client, Registry: resolver, Authz: authz, Log: a.log}
	deps.ClientAuth = &httpapi.ClientAuthHandler{Client: client, Dispatcher: dispatcher, Authz: authz, Log: a.log}

	// CMS engine + render pipeline (M5).
	renderCache := render.NewCache(rdb, a.log)
	invalidator := &render.Invalidator{Cache: renderCache, Log: a.log}
	dispatcher.Subscribe(invalidator.Handle)

	// member-tiers-and-points (change member-tiers-and-points, design D1):
	// the dispatcher's first non-cache subscriber — reacts to
	// events.OrderPaymentSucceeded/OrderReturned published by payment/
	// shipping below. Needs *ent.Client only; it never imports order/
	// payment/shipping.
	pointsService := &points.Service{Client: client, Log: a.log}
	dispatcher.Subscribe(pointsService.Handle)

	cmsService := &cms.Service{Client: client, Dispatcher: dispatcher}
	// change product-images-r2 (design D7): with media configured, CMS image
	// fields must be empty or on the image domain, and rich text <img> outside it
	// is removed. Unconfigured = rule off (nothing could satisfy it).
	if cfg.Media.Enabled() {
		cmsService.RestrictImageDomain(append([]string{cfg.Media.PublicBaseURL}, cfg.Media.ExtraAllowedOrigins...)...)
	}
	deps.Shops = &httpapi.ShopsHandler{Client: client, Service: cmsService, Authz: authz, Log: a.log}
	deps.Provisioning = &httpapi.ProvisioningHandler{
		Service: &provision.Service{Client: client, CMS: cmsService, Accounts: accountService, Dispatcher: dispatcher},
		Engine:  engine, Authz: authz, Log: a.log,
	}
	deps.ContentSchema = &httpapi.ContentSchemaHandler{
		Service: &content.SchemaService{Client: client, Dispatcher: dispatcher, Log: a.log},
		Authz:   authz, Limiter: limiter, Log: a.log,
	}

	contentService := &content.Service{Client: client, Dispatcher: dispatcher, Gate: cmsService, Log: a.log}
	// Scheduled publishing (design D11): the scheduler publishes through the
	// SAME contentService.Publish path as a manual publish, so revisions, audit,
	// cache invalidation and the publish hook all behave identically. The clock
	// is the database's now(); the in-process ticker is off unless
	// CONTENT_SCHEDULER_ENABLED=1 (cmd/content-scheduler is the cron route).
	contentScheduler := &content.Scheduler{
		Client: client, DB: db, Docs: contentService, Clock: content.DBClock{DB: db},
		Authorize: content.PublishAuthorizer(engine), Cfg: scheduleConfig(cfg), Log: a.log,
	}
	if cfg.ContentOps.ScheduleEnabled {
		loopCtx, stop := context.WithCancel(context.Background())
		go contentScheduler.Loop(loopCtx, cfg.ContentOps.ScheduleInterval)
		a.closers = append(a.closers, func() error { stop(); return nil })
		a.log.Info("content scheduler ticker enabled", "interval", cfg.ContentOps.ScheduleInterval)
	}
	deps.ContentSchedules = &httpapi.ContentSchedulesHandler{Scheduler: contentScheduler, Authz: authz, Limiter: limiter, Log: a.log}
	deps.ContentDocuments = &httpapi.ContentDocumentsHandler{
		Service: contentService, Schedules: contentScheduler,
		Issuer: issuer, Authz: authz, Limiter: limiter, PreviewTTL: cfg.ContentPreviewTTL, Log: a.log,
	}
	deps.ContentPublic = &httpapi.ContentPublicHandler{
		Reader: &content.Reader{Client: client}, Cache: renderCache, Issuer: issuer, Engine: engine,
		MaxAge: cfg.ContentPublicMaxAge, Log: a.log,
	}
	deps.ContentPublicRateLimit = httpapi.NewContentPublicRateLimit(limiter, cfg.ContentPublicRateLimit, cfg.ContentPublicRateWindow)

	// Design history (change design-history, slice 1). The record is the
	// platform's; the AI edit service reaches it only through the machine route
	// family behind its own credential (no approval route exists there).
	// Screenshots live in a SECOND, private bucket with its own credentials (the
	// same S3 client code as media, different bucket/keys/config). Unconfigured:
	// Store stays nil, upload/read/submit answer 503 design_storage_not_configured.
	var designStore design.ObjectStore
	if cfg.Design.Enabled() {
		designStore = media.NewS3Store(config.MediaConfig{
			Endpoint: cfg.Design.R2Endpoint, AccessKeyID: cfg.Design.R2AccessKeyID,
			SecretAccessKey: cfg.Design.R2SecretKey, Bucket: cfg.Design.R2Bucket,
		})
	} else {
		designStore = media.NullStore{} // never nil (wiring audit); ScreenshotsEnabled=false gates it
		a.log.Info("design screenshot storage not configured - requests cannot be submitted for approval")
	}
	designService := &design.Service{
		Store:  designStore,
		Client: client, Authz: design.RBACAuthorizer{Engine: engine}, Log: a.log, Now: time.Now,
		Cfg: design.Config{
			ApprovalTTL: cfg.Design.ApprovalTTL, MinPreviewRemaining: cfg.Design.MinPreviewRemaining,
			DailyRequestCap: cfg.Design.DailyRequestCap, ClaimLease: cfg.Design.ClaimLease,
			ApprovalPendingTimeout: cfg.Design.ApprovalPendingTimeout,
			ScreenshotsEnabled:     cfg.Design.Enabled(),
			ShopQuotaBytes:         cfg.Design.ShopQuotaBytes, ScreenshotURLTTL: cfg.Design.ScreenshotTTL,
			ProcessConcurrency: cfg.Media.ProcessConcurrency, MaxScreenshotAttempts: cfg.Design.MaxScreenshotAttempts,
		},
	}
	designCreds := &design.Credentials{Client: client, Now: time.Now}
	deps.Design = &httpapi.DesignAdminHandler{
		Service: designService, Client: client, Authz: authz, Limiter: limiter,
		DefaultTZ: cfg.ReportDefaultTimezone, Log: a.log,
	}
	deps.DesignService = &httpapi.DesignServiceHandler{Service: designService, Log: a.log}
	deps.DesignCredentials = &httpapi.DesignCredentialsHandler{Credentials: designCreds, Authz: authz, Log: a.log}
	deps.DesignServiceAuthMW = httpapi.NewDesignServiceAuth(designCreds, limiter, a.log)
	deps.DesignServiceRateLimit = httpapi.NewDesignServiceRateLimit(limiter)

	deps.Pages = &httpapi.PagesHandler{Client: client, Service: cmsService, Issuer: issuer, Cfg: cfg, Authz: authz, Log: a.log}

	// Product catalog (change product-catalog): no cache/events involved
	// (design D8 — the public endpoint reads the DB directly), so the
	// service only needs the ent client.
	catalogService := &catalog.Service{Client: client}

	// Media (change product-images-r2). The Service is constructed whether or
	// not storage is configured (design D2): disabled → NullStore, every media
	// route still mounts and answers 503, so the route table and the wiring
	// audit (checks 5 and 6) do not depend on environment variables.
	var mediaStore media.ObjectStore
	if cfg.Media.Enabled() {
		mediaStore = media.NewS3Store(cfg.Media)
	} else {
		a.log.Info("media storage not configured - image upload routes answer 503 media_not_configured")
	}
	mediaService := media.NewService(client, cfg.Media, mediaStore, a.log)
	if err := mediaService.WarmUp(); err != nil {
		return fmt.Errorf("wire: media encoder warm-up: %w", err)
	}
	deps.Media = &httpapi.MediaHandler{Client: client, Service: mediaService, Authz: authz, Log: a.log}

	deps.Categories = &httpapi.CategoriesHandler{Client: client, Service: catalogService, Authz: authz, Log: a.log}
	deps.Products = &httpapi.ProductsHandler{Client: client, Service: catalogService, Media: mediaService, Authz: authz, Log: a.log}

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

	// Merchant applications (change platform-website). The public intake is
	// unauthenticated and sits outside every tenant/admin middleware; the review
	// side is platform-level behind RBAC. Both are always constructed (the route
	// table never depends on settings): the Turnstile verifier is the real one
	// when a secret is configured, the no-network dev verifier otherwise (config
	// refuses a production boot without a secret), and the Notifier is the SMTP
	// one only when SMTP credentials exist — the outbox is the always-available
	// fallback an operator works by hand.
	var turnstileVerifier turnstile.Verifier = turnstile.DevVerifier{}
	if cfg.Turnstile.SecretKey != "" {
		turnstileVerifier = turnstile.NewHTTPVerifier(cfg.Turnstile.SecretKey, cfg.Turnstile.VerifyURL)
	} else {
		a.log.Warn("TURNSTILE_SECRET_KEY not set - accepting only Cloudflare's dummy test token (development/test only)")
	}
	var appNotifier application.Notifier = application.OutboxNotifier{}
	if cfg.SMTP.Enabled() {
		smtpNotifier := &application.SMTPNotifier{
			Client: client, Sender: mailSender, Log: a.log, Timeout: cfg.SMTP.Timeout,
		}
		smtpNotifier.Start()
		a.closers = append(a.closers, func() error { smtpNotifier.Close(); return nil })
		appNotifier = smtpNotifier
	} else {
		a.log.Info("SMTP not configured - merchant application messages go to the manual outbox (platform admin)")
	}
	ipKey := hmac.New(sha256.New, []byte(cfg.AdminJWTSecret))
	_, _ = ipKey.Write([]byte("application-ip-hmac-v1"))
	applicationService := &application.Service{
		Client: client, Notifier: appNotifier, Log: a.log, Now: time.Now,
		Provision: deps.Provisioning.Service,
		Cfg: application.Config{
			WebsiteURL: cfg.WebsiteURL, ConsentVersion: cfg.Application.ConsentVersion,
			ReapplyCooldown: cfg.Application.ReapplyCooldown, DailyCap: cfg.Application.DailyCap,
			RejectedRetention: cfg.Application.RejectedRetention, ApprovedRetention: cfg.Application.ApprovedRetention,
			IPKey: ipKey.Sum(nil),
		},
	}
	deps.ApplicationsPublic = &httpapi.ApplicationsPublicHandler{
		Service: applicationService, Turnstile: turnstileVerifier, Limiter: limiter,
		EmailLimit: cfg.RateLimit.ApplicationEmailLimit, EmailWindow: cfg.RateLimit.ApplicationEmailWindow, Log: a.log,
	}
	deps.ApplicationRateLimit = httpapi.NewApplicationRateLimit(limiter, cfg.RateLimit)
	deps.ApplicationVerifyRateLimit = httpapi.NewApplicationVerifyRateLimit(limiter, cfg.RateLimit)
	deps.Applications = &httpapi.ApplicationsHandler{Service: applicationService, Engine: engine, Authz: authz, Log: a.log}

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

	// Publish hook (change headless-content-store slice 3, design D7). The
	// notifier is a dispatcher subscriber that ONLY enqueues (no I/O on the
	// publishing request); its writer and flusher goroutines are started here
	// and drained on Close. The subscription is unconditional (wiring check 7);
	// a shop without a hook costs one cheap indexed lookup per publish.
	contentHookNotifier := &hook.Notifier{
		Client: client, Box: credentialsBox, Log: a.log,
		Sender: &hook.Sender{Policy: hookPolicy(cfg)},
		Cfg:    hookConfig(cfg),
	}
	contentHookNotifier.SetPolicy(hookPolicy(cfg))
	contentHookNotifier.Start()
	a.closers = append(a.closers, contentHookNotifier.Close)
	dispatcher.Subscribe(contentHookNotifier.Handle)
	deps.ContentHook = &httpapi.ContentHookHandler{Notifier: contentHookNotifier, Authz: authz, Limiter: limiter, Log: a.log}

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
		ImagePrefixes: cmsService.ImagePrefixes,
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

// hookPolicy is the outbound policy of the publish hook: the built-in
// Cloudflare deploy-hook host plus the operator's CONTENT_HOOK_ALLOWED_HOSTS.
func hookPolicy(cfg *config.Config) hook.Policy {
	return hook.Policy{ExtraHosts: cfg.ContentOps.HookExtraHosts, DevInsecure: cfg.ContentOps.HookDevInsecure}
}

func hookConfig(cfg *config.Config) hook.Config {
	c := hook.DefaultConfig()
	o := cfg.ContentOps
	c.Debounce, c.MaxWait, c.MinInterval = o.HookDebounce, o.HookMaxWait, o.HookMinInterval
	c.DailyCap, c.QueueSize, c.FlushInterval = o.HookDailyCap, o.HookQueueSize, o.HookFlushInterval
	return c
}

func scheduleConfig(cfg *config.Config) content.ScheduleConfig {
	c := content.DefaultScheduleConfig()
	o := cfg.ContentOps
	c.MinLead, c.MaxAhead, c.Grace = o.ScheduleMinLead, o.ScheduleMaxAhead, o.ScheduleGrace
	return c
}
