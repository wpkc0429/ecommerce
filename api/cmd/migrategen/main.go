// Command migrategen generates versioned SQL migrations from the ent schema
// using the Atlas diff engine (design D11), formatted as golang-migrate
// up/down pairs in /api/migrations.
//
// Flow: wipe the dev database → replay existing migration files onto it →
// diff against the desired ent schema → write NNN_<name>.up.sql/.down.sql.
//
// Caveat: handwritten migrations carry constraints ent cannot express
// (UNIQUE NULLS NOT DISTINCT — see 20260718000002). The diff engine does not
// know about them; if a future diff proposes dropping those constraints,
// remove the offending statements from the generated file by hand.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"os"

	"ariga.io/atlas/sql/sqltool"
	"entgo.io/ent/dialect"
	entschema "entgo.io/ent/dialect/sql/schema"
	_ "github.com/lib/pq"

	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent/migrate"
)

func main() {
	name := flag.String("name", "", "migration name (snake_case)")
	flag.Parse()
	if *name == "" {
		log.Fatal("usage: migrategen -name <migration_name>")
	}

	// Loads .env for DEV_DATABASE_URL.
	if _, err := config.Load(); err != nil {
		log.Fatalf("config: %v", err)
	}
	devURL := os.Getenv("DEV_DATABASE_URL")
	if devURL == "" {
		log.Fatal("DEV_DATABASE_URL is required (Atlas dev database for replay+diff)")
	}

	// The dev database must be empty before replay.
	db, err := sql.Open("postgres", devURL)
	if err != nil {
		log.Fatalf("open dev db: %v", err)
	}
	if _, err := db.Exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public;"); err != nil {
		log.Fatalf("reset dev db: %v", err)
	}
	_ = db.Close()

	dir, err := sqltool.NewGolangMigrateDir("migrations")
	if err != nil {
		log.Fatalf("open migrations dir: %v", err)
	}

	opts := []entschema.MigrateOption{
		entschema.WithDir(dir),
		entschema.WithMigrationMode(entschema.ModeReplay),
		entschema.WithDialect(dialect.Postgres),
		entschema.WithFormatter(sqltool.GolangMigrateFormatter),
		entschema.WithDropColumn(true),
		entschema.WithDropIndex(true),
	}
	if err := migrate.NamedDiff(context.Background(), devURL, *name, opts...); err != nil {
		log.Fatalf("generate migration: %v", err)
	}
	log.Printf("migration %q written to /api/migrations", *name)
}
