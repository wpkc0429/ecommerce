package app

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/catalog"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/database"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/render"
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

	// Auth endpoint rate limiting (design auth-rate-limiting): reuses the
	// same Redis client as the render/authz caches — rate limiting is
	// defense-in-depth, not a new infra dependency.
	limiter := ratelimit.New(rdb, a.log)
	deps.AdminLoginRateLimit = httpapi.NewAdminLoginRateLimit(limiter, cfg.RateLimit)
	deps.AdminRefreshRateLimit = httpapi.NewAdminRefreshRateLimit(limiter, cfg.RateLimit)
	deps.MemberLoginRateLimit = httpapi.NewMemberLoginRateLimit(limiter, cfg.RateLimit)
	deps.MemberRegisterRateLimit = httpapi.NewMemberRegisterRateLimit(limiter, cfg.RateLimit)
	deps.MemberRefreshRateLimit = httpapi.NewMemberRefreshRateLimit(limiter, cfg.RateLimit)

	engine := rbac.NewEngine(client, rdb, a.log)
	authz := &httpapi.AuthzMW{Engine: engine}
	deps.Roles = &httpapi.RolesHandler{Client: client, Engine: engine, Authz: authz, Log: a.log}

	// Domain events → cache invalidation (design D8: 失效集中於領域事件層).
	dispatcher := events.NewDispatcher(a.log)

	resolver := tenant.NewResolver(client, rdb, a.log)
	dispatcher.Subscribe(func(ctx context.Context, e events.Event) {
		if ev, ok := e.(events.SiteMappingChanged); ok {
			resolver.InvalidateHosts(ctx, ev.Hosts...)
		}
	})

	deps.TenantMW = httpapi.NewTenantMiddleware(resolver)
	deps.Sites = &httpapi.SitesHandler{Client: client, Dispatcher: dispatcher, Log: a.log, Authz: authz}

	// CMS engine + render pipeline (M5).
	renderCache := render.NewCache(rdb, a.log)
	invalidator := &render.Invalidator{Cache: renderCache, Client: client, Log: a.log}
	dispatcher.Subscribe(invalidator.Handle)

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
