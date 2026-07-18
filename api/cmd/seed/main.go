// Command seed provisions baseline data (task 2.4). Idempotent.
//
//	go run ./cmd/seed         # catalog + roles + super admin + starter theme
//	go run ./cmd/seed -demo   # additionally: demo shop on demo.localhost
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/database"
	"ksdevworks/ecommerce/api/internal/seed"
)

func main() {
	demo := flag.Bool("demo", false, "also create a demo shop bound to demo.localhost")
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

	opts := seed.Options{
		AdminEmail:    os.Getenv("SEED_ADMIN_EMAIL"),
		AdminPassword: os.Getenv("SEED_ADMIN_PASSWORD"),
		Demo:          *demo,
	}
	if err := seed.Run(ctx, client, opts); err != nil {
		log.Fatalf("seed: %v", err)
	}
	log.Printf("seed complete (demo=%v)", *demo)
}
