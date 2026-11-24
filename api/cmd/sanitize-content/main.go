// Command sanitize-content re-sanitizes every stored CMS rich text field
// against the shop's current theme schema (change cms-rich-text-sanitization,
// design D7). Idempotent: a second -apply run reports zero changes.
//
//	go run ./cmd/sanitize-content          # dry run (default): report only, writes nothing
//	go run ./cmd/sanitize-content -apply   # write sanitized values + invalidate render cache
//
// Output is ids and verdicts only — never content (it may hold merchant data).
// Rows it cannot check (shop without theme, page type not in the current
// theme) are reported as skipped; the write path will sanitize them on their
// next save, and the storefront renderer sanitizes them on every render.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/database"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/render"
)

func main() {
	apply := flag.Bool("apply", false, "write sanitized values (default is a dry run)")
	verbose := flag.Bool("v", false, "also list unchanged rows")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	ctx := context.Background()
	client, _, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer func() { _ = client.Close() }()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	var dispatcher *events.Dispatcher
	if *apply {
		// Same Redis settings as the server (internal/app/wire.go): the render
		// cache is the only thing a changed published_json can leave stale.
		rdb := redis.NewClient(&redis.Options{
			Addr:         cfg.RedisAddr,
			Password:     cfg.RedisPassword,
			DB:           cfg.RedisDB,
			DialTimeout:  2 * time.Second,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
			MaxRetries:   1,
		})
		defer func() { _ = rdb.Close() }()
		if err := rdb.Ping(ctx).Err(); err != nil {
			logger.Warn("redis unreachable — rows will still be written, but cached renders may stay stale until their TTL", "err", err)
		}
		dispatcher = events.NewDispatcher(logger)
		inv := &render.Invalidator{Cache: render.NewCache(rdb, logger), Client: client, Log: logger}
		dispatcher.Subscribe(inv.Handle)
	}

	svc := &cms.Service{Client: client, Dispatcher: dispatcher}
	sum, err := svc.BackfillRichText(ctx, *apply, func(it cms.BackfillItem) {
		switch {
		case it.Skipped != "":
			fmt.Printf("skip     %-4s id=%d shop=%d %s: %s\n", it.Kind, it.ID, it.ShopID, it.Field, it.Skipped)
		case it.Changed:
			fmt.Printf("changed  %-4s id=%d shop=%d %s\n", it.Kind, it.ID, it.ShopID, it.Field)
		case *verbose:
			fmt.Printf("ok       %-4s id=%d shop=%d %s\n", it.Kind, it.ID, it.ShopID, it.Field)
		}
	})
	if err != nil {
		log.Fatalf("backfill: %v", err)
	}
	mode := "dry run (nothing written; re-run with -apply)"
	if *apply {
		mode = "applied"
	}
	fmt.Printf("%s: checked=%d changed=%d skipped=%d\n", mode, sum.Checked, sum.Changed, sum.Skipped)
}
