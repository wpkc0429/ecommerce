package testutil

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/jackc/pgx/v5/stdlib"

	"ksdevworks/ecommerce/api/internal/config"
)

// The destructive migrations of change retire-hosted-storefront-and-themes
// (stage 5) applied to a database that holds old data: a themed shop with a
// site binding, a published page, and a granted theme.* permission. The test
// runs on a throwaway database of its own, never on the shared test database
// and never on a real one.
func TestThemeAndSiteMigrationsOnPopulatedDatabase(t *testing.T) {
	RequireIntegration(t)
	if _, err := config.Load(); err != nil {
		t.Fatalf("config: %v", err)
	}
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Fatal("TEST_DATABASE_URL is required")
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	withDB := func(name string) string {
		c := *u
		c.Path = "/" + name
		return c.String()
	}
	const scratch = "ecommerce_migtest"
	admin, err := sql.Open("pgx", withDB("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	if _, err := admin.Exec("DROP DATABASE IF EXISTS " + scratch); err != nil {
		t.Fatalf("drop scratch db: %v", err)
	}
	if _, err := admin.Exec("CREATE DATABASE " + scratch); err != nil {
		t.Fatalf("create scratch db: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE IF EXISTS " + scratch) })

	dir := filepath.ToSlash(migrationsDir(t))
	m, err := migrate.New("file://"+dir, withDB(scratch))
	if err != nil {
		t.Fatalf("migrate init: %v", err)
	}
	defer func() { _, _ = m.Close() }()

	// Up to the last migration before content_schemas, then plant old themed data.
	const beforeSchemas = 20261124142354
	if err := m.Migrate(beforeSchemas); err != nil {
		t.Fatalf("migrate to %d: %v", beforeSchemas, err)
	}
	db, err := sql.Open("pgx", withDB(scratch))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO themes (created_at, updated_at, code, name, layout_key, config_schema) VALUES (now(), now(), 'starter', 'Starter', 'starter/main', '{"type":"object"}')`)
	mustExec(`INSERT INTO theme_pages (created_at, updated_at, type_key, component_key, page_schema, theme_id) VALUES (now(), now(), 'home', 'starter/home', '{"type":"object"}', 1)`)
	mustExec(`INSERT INTO shops (created_at, updated_at, name, status, content_json, meta, theme_id) VALUES (now(), now(), 'Old Shop', 1, '{"header":{"site_title":"keep me"}}', '{}', 1)`)

	// The content_schemas migration backfills the themed shop with its theme's schemas.
	const before = 20261124155743
	if err := m.Migrate(before); err != nil {
		t.Fatalf("migrate to %d: %v", before, err)
	}
	var schemaDoc string
	if err := db.QueryRow(`SELECT schema_json::text FROM content_schemas WHERE shop_id = 1 AND version = 1 AND source = 'theme_backfill'`).Scan(&schemaDoc); err != nil {
		t.Fatalf("backfilled schema missing: %v", err)
	}
	if !strings.Contains(schemaDoc, `"home"`) || !strings.Contains(schemaDoc, `"global"`) {
		t.Fatalf("backfilled schema looks wrong: %s", schemaDoc)
	}

	// Plant the rest of the old data, then apply the removal.
	mustExec(`INSERT INTO sites (created_at, updated_at, domain, meta) VALUES (now(), now(), 'old.localhost', '{}')`)
	mustExec(`INSERT INTO site_shop (is_primary, created_at, site_id, shop_id) VALUES (true, now(), 1, 1)`)
	mustExec(`INSERT INTO pages (created_at, updated_at, shop_id, type_key, title, slug, status, content_json, meta) VALUES (now(), now(), 1, 'home', 'Home', 'home', 0, '{"sections":[]}', '{}')`)
	mustExec(`INSERT INTO permissions (name) VALUES ('theme.view'), ('shop.manage_domains'), ('page.view')`)
	mustExec(`INSERT INTO roles (created_at, updated_at, name, scope) VALUES (now(), now(), 'editor', 'merchant')`)
	mustExec(`INSERT INTO role_permission (role_id, permission_id) SELECT 1, id FROM permissions`)

	// Apply the removal.
	// Up to the removal itself, not "latest": later migrations (for example the
	// content.* permission copy of headless-content-store) are not part of what
	// this test pins, and the refusal check below needs the removal to be the
	// current version.
	const removal = 20261124173059
	if err := m.Migrate(removal); err != nil {
		t.Fatalf("migrate up over old data: %v", err)
	}

	scalar := func(q string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	for _, tbl := range []string{"themes", "theme_pages", "sites", "site_shop"} {
		if scalar(`SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename='`+tbl+`'`) != 0 {
			t.Errorf("table %s still exists", tbl)
		}
	}
	if scalar(`SELECT count(*) FROM information_schema.columns WHERE table_name='shops' AND column_name='theme_id'`) != 0 {
		t.Error("shops.theme_id still exists")
	}
	// Kept: the shop (with its global content), the page, the page.* permission and its grant.
	if scalar(`SELECT count(*) FROM shops WHERE name='Old Shop' AND content_json->'header'->>'site_title'='keep me'`) != 1 {
		t.Error("the shop or its content_json was lost")
	}
	if scalar(`SELECT count(*) FROM pages WHERE slug='home'`) != 1 {
		t.Error("the page was lost")
	}
	if scalar(`SELECT count(*) FROM permissions WHERE name='page.view'`) != 1 ||
		scalar(`SELECT count(*) FROM role_permission rp JOIN permissions p ON p.id=rp.permission_id WHERE p.name='page.view'`) != 1 {
		t.Error("page.view or its grant was lost")
	}
	// Removed: the theme.*/shop.manage_domains nodes and their grants.
	if scalar(`SELECT count(*) FROM permissions WHERE name IN ('theme.view','theme.manage','shop.manage_domains')`) != 0 {
		t.Error("removed permission nodes survive")
	}
	if scalar(`SELECT count(*) FROM role_permission`) != 1 {
		t.Error("grants of removed nodes survive")
	}

	// The down file of the last migration refuses to roll back (the rollback
	// story is the pre-upgrade backup).
	if err := m.Steps(-1); err == nil || !strings.Contains(err.Error(), "refusing to roll back") {
		t.Fatalf("the down migration must be refused, got %v", err)
	}
	if scalar(`SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename='themes'`) != 0 {
		t.Error("a refused rollback must not recreate anything")
	}
}
