// Command purge-content-audit applies the retention policy of the content
// store (change headless-content-store slice 3, design D12). Idempotent.
//
//	go run ./cmd/purge-content-audit                 # dry run (default): counts only
//	go run ./cmd/purge-content-audit -apply          # delete
//	go run ./cmd/purge-content-audit -shop 3         # restrict to one shop
//
// Retention: content_audit_log rows older than CONTENT_AUDIT_RETENTION (180
// days) and finished schedule rows (done, failed, missed, cancelled,
// superseded) older than CONTENT_SCHEDULE_RETENTION (90 days). Pending and
// running schedules are never touched. There is no scheduler in this
// repository: run it daily from the operator's own cron / task scheduler (see
// docs/deploy/content-store-operations.md), after one dry run.
//
// The audit log stores ids, counts and result categories only (no content, no
// URLs, no e-mail addresses), but the output still shows nothing but counts and
// time ranges.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"sort"
	"time"

	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/content"
	"ksdevworks/ecommerce/api/internal/database"
)

func main() {
	apply := flag.Bool("apply", false, "delete (default is a dry run)")
	shop := flag.Int("shop", 0, "only this shop id (default: all shops)")
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

	rep, err := content.Purge(ctx, client, time.Now(), content.PurgeConfig{
		AuditRetention:    cfg.ContentOps.AuditRetention,
		ScheduleRetention: cfg.ContentOps.ScheduleRetention,
		Shop:              *shop,
	}, *apply)
	if rep != nil {
		printReport(rep, *apply)
	}
	if err != nil {
		log.Fatalf("purge: %v", err)
	}
}

func printReport(rep *content.PurgeReport, apply bool) {
	verb := map[bool]string{true: "", false: "would "}[apply]
	done := map[bool]string{true: "deleted", false: "to delete"}[apply]
	fmt.Printf("audit log: cutoff %s\n", rep.AuditCutoff.UTC().Format(time.RFC3339))
	actions := make([]string, 0, len(rep.AuditByAction))
	for a := range rep.AuditByAction {
		actions = append(actions, a)
	}
	sort.Strings(actions)
	for _, a := range actions {
		fmt.Printf("  %sdelete %d x %s\n", verb, rep.AuditByAction[a], a)
	}
	fmt.Printf("  %d audit rows %s", rep.AuditRows, done)
	if rep.AuditOldest != nil && rep.AuditNewest != nil {
		fmt.Printf(" (from %s to %s)", rep.AuditOldest.UTC().Format("2006-01-02"), rep.AuditNewest.UTC().Format("2006-01-02"))
	}
	fmt.Println()
	fmt.Printf("finished schedules: cutoff %s\n", rep.ScheduleCutoff.UTC().Format(time.RFC3339))
	fmt.Printf("  %d schedule rows %s", rep.ScheduleRows, done)
	if rep.ScheduleOldest != nil && rep.ScheduleNewest != nil {
		fmt.Printf(" (publish_at from %s to %s)", rep.ScheduleOldest.UTC().Format("2006-01-02"), rep.ScheduleNewest.UTC().Format("2006-01-02"))
	}
	fmt.Println()
	if !apply {
		fmt.Println("dry run; pass -apply to delete")
	}
}
