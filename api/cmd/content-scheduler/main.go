// Command content-scheduler publishes the scheduled content documents that are
// due (change headless-content-store slice 3, design D11).
//
//	go run ./cmd/content-scheduler            # one run, then exit (cron / Task Scheduler)
//	go run ./cmd/content-scheduler -every 1m  # keep running, one run per interval
//
// It is the cron-friendly twin of the optional in-process ticker of the API
// server (CONTENT_SCHEDULER_ENABLED=1). Both call content.Scheduler.RunOnce, so
// they can run side by side: a Postgres advisory lock makes the second one skip
// and every schedule is claimed with a conditional UPDATE besides.
//
// Publishing goes through content.Service.Publish, exactly like a manual
// publish: revisions, audit, render-cache invalidation (needs Redis, hence the
// up-front ping: stale cached content is worse than a late publish, and an
// unclaimed schedule is simply tried again next run) and the publish hook (the
// run ends by sending the hook calls it accumulated). Granularity is one
// minute: run it every minute.
//
// Output: counts only. Exit status is non-zero when the run itself could not be
// done (database or Redis unreachable); a schedule that failed or was missed is
// recorded in the data and the API, not in the exit status.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/content"
	"ksdevworks/ecommerce/api/internal/content/hook"
	"ksdevworks/ecommerce/api/internal/cryptbox"
	"ksdevworks/ecommerce/api/internal/database"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/render"
)

func main() {
	every := flag.Duration("every", 0, "keep running with this interval (default: one run, then exit)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, db, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer func() { _ = client.Close() }()

	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB,
		DialTimeout: 2 * time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, MaxRetries: 1,
	})
	defer func() { _ = rdb.Close() }()
	if err := rdb.Ping(ctx).Err(); err != nil {
		// Publishing without invalidating the render cache would serve stale
		// content for up to 24 h; nothing is claimed, the next run tries again.
		log.Fatalf("redis is unreachable (%s): refusing to publish without cache invalidation", cfg.RedisAddr)
	}

	box, err := cryptbox.ParseKey(cfg.PaymentCredentialsEncryptionKey)
	if err != nil {
		log.Fatalf("encryption key: %v", err)
	}
	dispatcher := events.NewDispatcher(logger)
	dispatcher.Subscribe((&render.Invalidator{Cache: render.NewCache(rdb, logger), Log: logger}).Handle)
	notifier := &hook.Notifier{
		Client: client, Box: box, Log: logger,
		Sender: &hook.Sender{Policy: policyOf(cfg)},
		Cfg:    hookConfigOf(cfg),
	}
	notifier.SetPolicy(policyOf(cfg))
	dispatcher.Subscribe(notifier.Handle)

	cmsService := &cms.Service{Client: client, Dispatcher: dispatcher}
	if cfg.Media.Enabled() {
		cmsService.RestrictImageDomain(append([]string{cfg.Media.PublicBaseURL}, cfg.Media.ExtraAllowedOrigins...)...)
	}
	docs := &content.Service{Client: client, Dispatcher: dispatcher, Gate: cmsService, Log: logger}
	o := cfg.ContentOps
	sched := &content.Scheduler{
		Client: client, DB: db, Docs: docs, Clock: content.DBClock{DB: db},
		Authorize: content.PublishAuthorizer(rbac.NewEngine(client, rdb, logger)),
		Cfg: content.ScheduleConfig{
			MinLead: o.ScheduleMinLead, MaxAhead: o.ScheduleMaxAhead, Grace: o.ScheduleGrace,
			RunningTimeout: 10 * time.Minute, MaxAttempts: 5, BatchSize: 100,
		},
		Log: logger,
	}

	run := func() {
		rep, err := sched.RunOnce(ctx)
		if err != nil {
			log.Printf("run failed: %v", err)
			os.Exit(1)
		}
		// Send the hook calls this run accumulated (the one-shot process has no
		// background flusher). The minimum interval and the daily cap still apply;
		// what they hold back stays pending for the API server's flusher.
		notifier.DrainInline(ctx)
		hr, herr := notifier.Tick(ctx, true)
		if rep.Skipped {
			fmt.Println("skipped: another scheduler is running")
			return
		}
		fmt.Printf("due=%d published=%d already_published=%d failed=%d missed=%d superseded=%d cancelled=%d retry=%d hook_sent=%d hook_failed=%d hook_deferred=%d\n",
			rep.Due, rep.Published, rep.AlreadyPublished, rep.Failed, rep.Missed, rep.Superseded, rep.Cancelled, rep.Retry,
			hr.Sent, hr.Failed, hr.Deferred)
		if herr != nil {
			log.Printf("hook flush failed")
		}
	}
	if *every <= 0 {
		run()
		return
	}
	for {
		run()
		select {
		case <-ctx.Done():
			return
		case <-time.After(*every):
		}
	}
}

func policyOf(cfg *config.Config) hook.Policy {
	return hook.Policy{ExtraHosts: cfg.ContentOps.HookExtraHosts, DevInsecure: cfg.ContentOps.HookDevInsecure}
}

func hookConfigOf(cfg *config.Config) hook.Config {
	c := hook.DefaultConfig()
	o := cfg.ContentOps
	c.Debounce, c.MaxWait, c.MinInterval = o.HookDebounce, o.HookMaxWait, o.HookMinInterval
	c.DailyCap, c.QueueSize, c.FlushInterval = o.HookDailyCap, o.HookQueueSize, o.HookFlushInterval
	return c
}
