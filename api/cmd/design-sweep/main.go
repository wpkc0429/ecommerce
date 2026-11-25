// Command design-sweep keeps the design history honest (change design-history,
// design D18/D19/D3). Idempotent.
//
//	go run ./cmd/design-sweep                 # dry run (default): what would be failed
//	go run ./cmd/design-sweep -apply          # fail stuck requests (system events)
//	go run ./cmd/design-sweep -verify         # recompute every request's event hash chain
//	go run ./cmd/design-sweep -orphans        # report objects without rows and rows without objects
//	go run ./cmd/design-sweep -shop 3         # restrict to one shop
//
// The sweep fails a request whose worker lost its claim lease (service_lost),
// one that waited for approval too long (approval_timeout) and one that was
// approved but never published in time (approval_expired). Each is an event
// with actor_type "system". -verify exits with status 1 when a chain does not
// verify. There is no scheduler in this repository: run the sweep from the
// operator's own cron or task scheduler (docs/deploy/design-history-operations.md).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/database"
	"ksdevworks/ecommerce/api/internal/design"
)

func main() {
	apply := flag.Bool("apply", false, "fail stuck requests (default is a dry run)")
	verify := flag.Bool("verify", false, "recompute the event hash chain of every request")
	orphans := flag.Bool("orphans", false, "report orphaned screenshot objects")
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

	svc := &design.Service{Client: client, Cfg: design.Config{
		ApprovalTTL: cfg.Design.ApprovalTTL, MinPreviewRemaining: cfg.Design.MinPreviewRemaining,
		DailyRequestCap: cfg.Design.DailyRequestCap, ClaimLease: cfg.Design.ClaimLease,
		ApprovalPendingTimeout: cfg.Design.ApprovalPendingTimeout,
	}}

	if *verify {
		rep, err := svc.VerifyChains(ctx, *shop)
		if err != nil {
			log.Fatalf("verify: %v", err)
		}
		fmt.Printf("chains verified: %d\n", rep.Verified)
		for _, b := range rep.Broken {
			fmt.Printf("BROKEN request %s at event %d: %s\n", b.RequestID, b.Seq, b.Reason)
		}
		if len(rep.Broken) > 0 {
			os.Exit(1)
		}
		return
	}
	if *orphans {
		fmt.Println("orphan report: screenshot storage is not part of this slice (slice 2); nothing to compare yet")
		return
	}

	rep, err := svc.Sweep(ctx, *shop, *apply)
	if err != nil {
		log.Fatalf("sweep: %v", err)
	}
	word := "would fail"
	if *apply {
		word = "failed"
	}
	fmt.Printf("%s %d request(s): service_lost (claim lease expired)\n", word, rep.LeasesLost)
	fmt.Printf("%s %d request(s): approval_timeout (nobody approved in time)\n", word, rep.ApprovalTimeouts)
	fmt.Printf("%s %d request(s): approval_expired (approved but not published in time)\n", word, rep.ApprovalExpired)
	if !*apply {
		fmt.Println("dry run; pass -apply to write")
	}
}
