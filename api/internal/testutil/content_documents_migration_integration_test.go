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

const (
	beforeContentDocuments = 20261124200226
	contentDocumentsMig    = 20261125051318
	dataMarker             = "-- ── Data migration"
)

// scratchMigration opens a throwaway database migrated to version `to`.
func scratchMigration(t *testing.T, name string, to uint) (*sql.DB, *migrate.Migrate) {
	t.Helper()
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
	withDB := func(n string) string {
		c := *u
		c.Path = "/" + n
		return c.String()
	}
	admin, err := sql.Open("pgx", withDB("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err := admin.Exec("DROP DATABASE IF EXISTS " + name); err != nil {
		t.Fatalf("drop scratch db: %v", err)
	}
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create scratch db: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE IF EXISTS " + name) })
	m, err := migrate.New("file://"+filepath.ToSlash(migrationsDir(t)), withDB(name))
	if err != nil {
		t.Fatalf("migrate init: %v", err)
	}
	t.Cleanup(func() { _, _ = m.Close() })
	if err := m.Migrate(to); err != nil {
		t.Fatalf("migrate to %d: %v", to, err)
	}
	db, err := sql.Open("pgx", withDB(name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, m
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func scalarInt(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// The data migration of headless-content-store slice 2 on a database that
// holds slice-1 data: pages become documents, shops.content_json becomes the
// "global" document, slice 1's interim storage moves into content_revisions
// and content_audit_log, and re-running the data part changes nothing.
func TestContentDocumentsBackfill(t *testing.T) {
	db, m := scratchMigration(t, "ecommerce_migtest_docs", beforeContentDocuments)

	exec(t, db, `INSERT INTO shops (created_at, updated_at, name, status, content_json, meta) VALUES
	  (now(), now(), 'Shop A', 1, '{"header":{"site_title":"A"}}', '{}'),
	  (now(), now(), 'Shop B', 1, '{}', '{}')`)
	exec(t, db, `INSERT INTO content_schemas (shop_id, version, schema_json, sha256, source, created_by_user_id, snapshot_json, created_at) VALUES
	  (1, 1, '{"format":1,"types":{"home":{"type":"object"}},"global":{"type":"object"}}', 'a', 'theme_backfill', NULL, NULL, now()),
	  (1, 2, '{"format":1,"types":{"home":{"type":"object"}},"global":{"type":"object"}}', 'b', 'registered', 7,
	     '{"documents":[{"kind":"page","key":"about","status":"needs_attention","draft":{"old":"draft"},"published":{"old":"pub"}}]}', now())`)
	exec(t, db, `INSERT INTO pages (created_at, updated_at, shop_id, type_key, title, slug, status, content_json, published_json, meta, needs_attention) VALUES
	  (now(), now(), 1, 'home', 'Home', 'home', 1, '{"sections":[1]}', '{"sections":[1]}', '{}', false),
	  (now(), now(), 1, 'home', 'About', 'about', 0, '{"a":2}', '{"a":1}', '{"seo":"x"}', true),
	  (now(), now(), 1, 'home', 'Draft only', 'draft-only', 0, '{"d":1}', NULL, '{}', false),
	  (now(), now(), 2, 'home', 'Home', 'home', 0, '{"sections":[]}', NULL, '{}', false)`)

	if err := m.Migrate(contentDocumentsMig); err != nil {
		t.Fatalf("migrate up over slice-1 data: %v", err)
	}

	if n := scalarInt(t, db, `SELECT count(*) FROM content_documents`); n != 6 {
		t.Fatalf("documents = %d, want 6 (4 pages + 2 global)", n)
	}
	// slug -> key identity; draft/published/meta/needs_attention copied.
	var status, publishedRev, draftRev int
	var draft, published, meta string
	var attention bool
	if err := db.QueryRow(`SELECT status, published_rev, draft_rev, draft_json::text, COALESCE(published_json::text, ''), meta::text, needs_attention
	  FROM content_documents WHERE shop_id = 1 AND key = 'about'`).Scan(&status, &publishedRev, &draftRev, &draft, &published, &meta, &attention); err != nil {
		t.Fatal(err)
	}
	if status != 0 || publishedRev != 1 || draftRev != 1 || !strings.Contains(draft, `"a": 2`) ||
		!strings.Contains(published, `"a": 1`) || !strings.Contains(meta, "seo") || !attention {
		t.Fatalf("about not mapped as expected: %d %d %d %s %s %s %v", status, publishedRev, draftRev, draft, published, meta, attention)
	}
	if n := scalarInt(t, db, `SELECT published_rev FROM content_documents WHERE shop_id = 1 AND key = 'draft-only'`); n != 0 {
		t.Fatalf("draft-only published_rev = %d, want 0", n)
	}
	// shops.content_json -> global, live (status 1, published = draft).
	if err := db.QueryRow(`SELECT status, draft_json::text, published_json::text, type_key FROM content_documents WHERE shop_id = 1 AND key = 'global'`).
		Scan(&status, &draft, &published, &meta); err != nil || status != 1 || draft != published || meta != "global" || !strings.Contains(draft, "site_title") {
		t.Fatalf("global not mapped: %d %s %s %s err=%v", status, draft, published, meta, err)
	}
	if n := scalarInt(t, db, `SELECT schema_version FROM content_documents WHERE shop_id = 1 AND key = 'home'`); n != 2 {
		t.Fatalf("home schema_version = %d, want the active version 2", n)
	}
	// one publish revision per published document: home + global of shop A,
	// about (published_json exists), global of shop B.
	if n := scalarInt(t, db, `SELECT count(*) FROM content_revisions WHERE kind = 'publish'`); n != 4 {
		t.Fatalf("publish revisions = %d, want 4", n)
	}
	// slice-1 snapshot moved to a schema_snapshot revision of "about".
	var snapDraft, snapPub string
	var snapVersion int
	if err := db.QueryRow(`SELECT r.json::text, COALESCE(r.published_json::text, ''), r.schema_version
	  FROM content_revisions r JOIN content_documents d ON d.id = r.document_id
	  WHERE d.shop_id = 1 AND d.key = 'about' AND r.kind = 'schema_snapshot'`).Scan(&snapDraft, &snapPub, &snapVersion); err != nil {
		t.Fatalf("schema_snapshot revision missing: %v", err)
	}
	if !strings.Contains(snapDraft, "draft") || !strings.Contains(snapPub, "pub") || snapVersion != 2 {
		t.Fatalf("snapshot content wrong: %s %s %d", snapDraft, snapPub, snapVersion)
	}
	// created_by_user_id -> audit row.
	if n := scalarInt(t, db, `SELECT count(*) FROM content_audit_log WHERE action = 'schema.register' AND actor_user_id = 7 AND shop_id = 1`); n != 1 {
		t.Fatalf("schema.register audit rows = %d, want 1", n)
	}

	// Idempotent: run the data part again.
	raw, err := os.ReadFile(filepath.Join(migrationsDir(t), "20261125051318_add_content_documents.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	i := strings.Index(text, dataMarker)
	if i < 0 {
		t.Fatal("data migration marker not found")
	}
	exec(t, db, text[i:])
	for q, want := range map[string]int{
		`SELECT count(*) FROM content_documents`:                      6,
		`SELECT count(*) FROM content_revisions`:                      5,
		`SELECT count(*) FROM content_audit_log`:                      1,
	} {
		if n := scalarInt(t, db, q); n != want {
			t.Fatalf("after re-running the data migration: %s = %d, want %d", q, n, want)
		}
	}

	// Down removes the three tables and nothing else; pages and shops keep the data.
	if err := m.Migrate(beforeContentDocuments); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	if n := scalarInt(t, db, `SELECT count(*) FROM pages`); n != 4 {
		t.Fatalf("pages after down = %d, want 4", n)
	}
	if n := scalarInt(t, db, `SELECT count(*) FROM information_schema.tables WHERE table_name IN ('content_documents','content_revisions','content_audit_log')`); n != 0 {
		t.Fatalf("tables left after down: %d", n)
	}
}

// A page whose slug cannot be a content key makes the migration fail and name
// it, instead of silently dropping or renaming it.
func TestContentDocumentsBackfillRefusesBadSlugs(t *testing.T) {
	db, m := scratchMigration(t, "ecommerce_migtest_docs_bad", beforeContentDocuments)
	exec(t, db, `INSERT INTO shops (created_at, updated_at, name, status, content_json, meta) VALUES (now(), now(), 'Shop', 1, '{}', '{}')`)
	exec(t, db, `INSERT INTO pages (created_at, updated_at, shop_id, type_key, title, slug, status, content_json, meta) VALUES
	  (now(), now(), 1, 'home', 'Bad', '-leading-hyphen', 0, '{}', '{}'),
	  (now(), now(), 1, 'home', 'Global', 'global', 0, '{}', '{}')`)
	err := m.Migrate(contentDocumentsMig)
	if err == nil {
		t.Fatal("migration must fail on pages that cannot be content keys")
	}
	if !strings.Contains(err.Error(), "-leading-hyphen") || !strings.Contains(err.Error(), "global") {
		t.Fatalf("the error must list the offending slugs: %v", err)
	}
}
