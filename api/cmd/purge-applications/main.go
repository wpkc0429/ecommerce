// Command purge-applications applies the retention policy of merchant
// applications (change platform-website, design D8). Idempotent.
//
//	go run ./cmd/purge-applications                    # dry run (default): report only
//	go run ./cmd/purge-applications -apply             # write
//	go run ./cmd/purge-applications -email a@b.test    # dry run of a privacy request
//	go run ./cmd/purge-applications -email a@b.test -apply
//
// Retention (see docs/deploy/platform-website-api-notes.md): applications whose
// e-mail was never verified are deleted after 7 days; rejected/expired/withdrawn
// ones have their personal data cleared after APPLICATION_REJECTED_RETENTION
// (and never before the re-application cooldown ends); approved ones after
// APPLICATION_APPROVED_RETENTION. Undecided (pending_review) applications are
// never touched. There is no scheduler in this repository: run it daily from
// the operator's own cron / task scheduler.
//
// Output is application ids only - never an e-mail, name or phone.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"

	"ksdevworks/ecommerce/api/internal/application"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/database"
)

func main() {
	apply := flag.Bool("apply", false, "write changes (default is a dry run)")
	email := flag.String("email", "", "privacy request: remove this person's applications regardless of retention")
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

	svc := &application.Service{
		Client: client, Notifier: application.OutboxNotifier{}, Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		Cfg: application.Config{
			ReapplyCooldown:   cfg.Application.ReapplyCooldown,
			RejectedRetention: cfg.Application.RejectedRetention,
			ApprovedRetention: cfg.Application.ApprovedRetention,
		},
	}
	var rep *application.PurgeReport
	if *email != "" {
		rep, err = svc.PurgeEmail(ctx, *email, *apply)
	} else {
		rep, err = svc.Purge(ctx, *apply)
	}
	if rep != nil {
		verb := map[bool]string{true: "", false: "would "}[*apply]
		for _, id := range rep.Deleted {
			fmt.Printf("%sdelete application id=%d\n", verb, id)
		}
		for _, id := range rep.Blanked {
			fmt.Printf("%sclear personal data of application id=%d\n", verb, id)
		}
		fmt.Printf("%d to delete, %d to clear", len(rep.Deleted), len(rep.Blanked))
		if !*apply {
			fmt.Print(" (dry run; pass -apply to write)")
		}
		fmt.Println()
	}
	if err != nil {
		log.Fatalf("purge: %v", err)
	}
}
