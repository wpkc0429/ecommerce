// Command purge-design-history applies the retention policy of the design
// history (change design-history, design D21). Idempotent.
//
//	go run ./cmd/purge-design-history                    # dry run (default): counts only
//	go run ./cmd/purge-design-history -apply             # purge
//	go run ./cmd/purge-design-history -shop 3            # restrict to one shop
//	go run ./cmd/purge-design-history -export-shop 3     # JSON of one shop's history (no URLs)
//	go run ./cmd/purge-design-history -all-for-shop 3 -apply   # delete one shop's whole history
//
// Retention (all configurable, see internal/config/design.go): prompt text is
// blanked after 365 days (its SHA-256 stays), screenshots of published requests
// go after 365 days and of the others after 30, finished requests with their
// events after 730 days, untracked changes after 730 days, and revoked or
// expired service credentials after 90 days. A request that is still in
// progress is never touched. There is no scheduler in this repository: run it
// daily from the operator's own cron or task scheduler after one dry run (see
// docs/deploy/design-history-operations.md).
//
// The history holds no personal data beyond user ids, but the output still
// shows only counts and time ranges.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/database"
	"ksdevworks/ecommerce/api/internal/design"
	"ksdevworks/ecommerce/api/internal/media"
)

func main() {
	apply := flag.Bool("apply", false, "purge (default is a dry run)")
	shop := flag.Int("shop", 0, "only this shop id (default: all shops)")
	exportShop := flag.Int("export-shop", 0, "print one shop's whole design history as JSON and exit")
	allForShop := flag.Int("all-for-shop", 0, "delete one shop's whole design history (dry run unless -apply)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	ctx := context.Background()
	client, db, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer func() { _ = client.Close() }()
	p := &design.Purger{Client: client, DB: db}
	if cfg.Design.Enabled() {
		p.Store = design.DeleterOf(media.NewS3Store(config.MediaConfig{
			Endpoint: cfg.Design.R2Endpoint, AccessKeyID: cfg.Design.R2AccessKeyID,
			SecretAccessKey: cfg.Design.R2SecretKey, Bucket: cfg.Design.R2Bucket,
		}))
	}

	switch {
	case *exportShop > 0:
		ex, err := p.ExportShop(ctx, *exportShop)
		if err != nil {
			log.Fatalf("export: %v", err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(ex); err != nil {
			log.Fatalf("export: %v", err)
		}
	case *allForShop > 0:
		rep, err := p.AllForShop(ctx, *allForShop, *apply)
		if rep != nil {
			fmt.Printf("shop %d: %d requests, %d untracked changes %s\n", *allForShop, rep.Requests, rep.Untracked, verb(*apply))
			printObjects(rep, *apply)
		}
		if err != nil {
			log.Fatalf("purge: %v", err)
		}
		dryNote(*apply)
	default:
		pc := design.PurgeConfig{
			PromptRetention: cfg.Design.PromptRetention, ScreenshotRetention: cfg.Design.ScreenshotRetention,
			ScreenshotRetentionUnpublished: cfg.Design.ScreenshotRetentionUnpublished, AuditRetention: cfg.Design.AuditRetention,
			UntrackedRetention: cfg.Design.UntrackedRetention, CredentialRetention: cfg.Design.CredentialRetention, Shop: *shop,
		}
		rep, err := p.Purge(ctx, pc, *apply)
		if rep != nil {
			printReport(db, rep, *apply)
		}
		if err != nil {
			log.Fatalf("purge: %v", err)
		}
		dryNote(*apply)
	}
}

func verb(apply bool) string {
	if apply {
		return "deleted"
	}
	return "to delete"
}

func dryNote(apply bool) {
	if !apply {
		fmt.Println("dry run; pass -apply to purge")
	}
}

func printObjects(rep *design.PurgeReport, apply bool) {
	if apply {
		fmt.Printf("  objects: %d deleted, %d not deleted (no store configured or a delete failed; orphans are listed by design-sweep -orphans)\n", rep.ObjectsDeleted, rep.ObjectsFailed)
	}
}

func printReport(_ *sql.DB, rep *design.PurgeReport, apply bool) {
	v := verb(apply)
	fmt.Printf("prompt text blanked: %d\n", rep.Prompts)
	fmt.Printf("screenshots %s: %d\n", map[bool]string{true: "purged", false: "to purge"}[apply], rep.Screenshots)
	fmt.Printf("finished requests (with events) %s: %d", v, rep.Requests)
	if rep.OldestRequest != nil && rep.NewestRequest != nil {
		fmt.Printf(" (created %s to %s)", rep.OldestRequest.UTC().Format(time.DateOnly), rep.NewestRequest.UTC().Format(time.DateOnly))
	}
	fmt.Println()
	fmt.Printf("untracked changes %s: %d\n", v, rep.Untracked)
	fmt.Printf("service credentials %s: %d\n", v, rep.Credentials)
	printObjects(rep, apply)
}
