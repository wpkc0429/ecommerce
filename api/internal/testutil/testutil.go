// Package testutil provides integration-test infrastructure: a migrated,
// truncated test database and a flushed Redis handle. Tests calling these
// helpers skip unless INTEGRATION=1 (docker compose infra required); run them
// with `make test-int`.
package testutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/database"
	"ksdevworks/ecommerce/api/internal/ent"
)

// RequireIntegration skips t unless INTEGRATION=1.
func RequireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("INTEGRATION") != "1" {
		t.Skip("integration test: set INTEGRATION=1 and run `make test-int`")
	}
}

// OpenDB returns an ent client on TEST_DATABASE_URL with migrations applied
// and all tables truncated. Integration tests must not run in parallel across
// packages against the shared database (Makefile uses `go test -p 1`).
func OpenDB(t *testing.T) *ent.Client {
	t.Helper()
	RequireIntegration(t)

	if _, err := config.Load(); err != nil { // pulls .env for TEST_DATABASE_URL
		t.Fatalf("config: %v", err)
	}
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL is required for integration tests")
	}

	m, err := migrate.New("file://"+migrationsDir(t), url)
	if err != nil {
		t.Fatalf("migrate init: %v", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate up: %v", err)
	}
	_, _ = m.Close()

	client, db, err := database.Open(context.Background(), url)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// Wipe all application tables between tests.
	rows, err := db.Query(`SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		tables = append(tables, fmt.Sprintf("%q", name))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}
	if len(tables) > 0 {
		if _, err := db.Exec("TRUNCATE " + strings.Join(tables, ", ") + " RESTART IDENTITY CASCADE"); err != nil {
			t.Fatalf("truncate: %v", err)
		}
	}
	return client
}

// OpenRedis returns a client on logical DB 15 (flushed) so tests never clash
// with the local development cache.
func OpenRedis(t *testing.T) *redis.Client {
	t.Helper()
	RequireIntegration(t)

	if _, err := config.Load(); err != nil {
		t.Fatalf("config: %v", err)
	}
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	if err := rdb.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("redis flush: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func migrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate migrations dir")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}
