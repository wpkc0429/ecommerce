// Package database opens the PostgreSQL connection shared by the server,
// CLI commands, and tests (pgx stdlib driver + ent client).
package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"entgo.io/ent/dialect"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// Open connects to PostgreSQL and returns an ent client plus the underlying
// *sql.DB (for pings and raw statements). Caller owns closing the client.
func Open(ctx context.Context, databaseURL string) (*ent.Client, *sql.DB, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("database: open: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("database: ping: %w", err)
	}

	drv := entsql.OpenDB(dialect.Postgres, db)
	client := ent.NewClient(ent.Driver(drv))
	// Tenant data isolation is non-negotiable for every client instance
	// (spec multi-tenancy/Tenant data isolation enforcement).
	tenant.Register(client)
	return client, db, nil
}
