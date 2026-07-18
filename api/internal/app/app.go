// Package app bootstraps the API service: infrastructure clients, domain
// services, and the HTTP router. cmd/server stays a thin entrypoint.
package app

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/httpapi"
)

// App owns the long-lived resources of the service.
type App struct {
	cfg    *config.Config
	log    *slog.Logger
	router http.Handler
	redis  *redis.Client

	closers []func() error
}

// New wires the application graph. Infrastructure pieces are attached as the
// implementation grows; a missing DATABASE_URL keeps the service bootable with
// /healthz only (useful for the M0 skeleton and smoke checks).
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	a := &App{cfg: cfg, log: log}

	deps := httpapi.Deps{
		Cfg: cfg,
		Log: log,
	}

	if cfg.DatabaseURL != "" {
		if err := a.wire(ctx, &deps); err != nil {
			a.Close()
			return nil, err
		}
	} else {
		log.Warn("DATABASE_URL not set — booting with /healthz only")
	}

	a.router = httpapi.New(deps)
	return a, nil
}

// Router returns the assembled HTTP handler.
func (a *App) Router() http.Handler { return a.router }

// Close releases all resources in reverse acquisition order.
func (a *App) Close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		if err := a.closers[i](); err != nil {
			a.log.Error("close resource", "err", err)
		}
	}
}
