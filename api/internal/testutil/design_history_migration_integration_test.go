package testutil

import "testing"

const designTriggersMig = 20261125160000

// The design-history migrations are pure additions and reversible: up, down,
// up again on a throwaway database (change design-history tasks 1.2 and 1.3).
func TestDesignHistoryMigrationsAreReversible(t *testing.T) {
	db, m := scratchMigration(t, "ecommerce_migtest_design", designTriggersMig)
	tables := []string{"design_requests", "design_request_events", "design_screenshots", "design_service_credentials", "design_site_state", "design_untracked_changes"}
	count := func(q string) int { return scalarInt(t, db, q) }
	present := func() {
		t.Helper()
		for _, tbl := range tables {
			if count(`SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename='`+tbl+`'`) != 1 {
				t.Fatalf("table %s missing", tbl)
			}
		}
		if n := count(`SELECT count(*) FROM pg_trigger WHERE NOT tgisinternal AND tgname LIKE 'design_%'`); n != 4 {
			t.Fatalf("%d design triggers, want 4", n)
		}
		// The one-active-request unique index and the composite same-shop FKs exist.
		if count(`SELECT count(*) FROM pg_indexes WHERE indexname='design_requests_one_active'`) != 1 {
			t.Fatal("partial unique index missing")
		}
		if n := count(`SELECT count(*) FROM pg_constraint WHERE contype='f' AND conname LIKE 'design_%'`); n != 4 {
			t.Fatalf("%d design foreign keys, want 4", n)
		}
	}
	present()

	const cols = `(id, shop_id, site, kind, status, requested_by_user_id, prompt_sha256, base_sha, head_sha, branch, rev, created_at, updated_at, prompt_flags, diff_summary`
	// The composite FK really is same-shop: a revert may not point at another shop's request.
	exec(t, db, `INSERT INTO design_requests `+cols+`)
		VALUES ('00000000-0000-0000-0000-000000000001', 1, 'admin', 'change', 'published', 1, repeat('a',64), repeat('b',40), repeat('b',40), 'ai/shop-1/00000000-0000-0000-0000-000000000001', 1, now(), now(), '[]', '{}')`)
	if _, err := db.Exec(`INSERT INTO design_requests ` + cols + `, restores_request_id)
		VALUES ('00000000-0000-0000-0000-000000000002', 2, 'admin', 'revert', 'drafting', 1, repeat('a',64), repeat('b',40), repeat('b',40), 'ai/shop-2/00000000-0000-0000-0000-000000000002', 1, now(), now(), '[]', '{}', '00000000-0000-0000-0000-000000000001')`); err == nil {
		t.Fatal("a revert in shop 2 pointed at shop 1's request")
	}
	// kind and restores_request_id must agree.
	if _, err := db.Exec(`INSERT INTO design_requests ` + cols + `)
		VALUES ('00000000-0000-0000-0000-000000000003', 1, 'admin', 'revert', 'failed', 1, repeat('a',64), repeat('b',40), repeat('b',40), 'ai/shop-1/00000000-0000-0000-0000-000000000003', 1, now(), now(), '[]', '{}')`); err == nil {
		t.Fatal("a revert without a target was accepted")
	}
	// At most one non-terminal request per (shop, site).
	insert := func(id, status string) error {
		_, err := db.Exec(`INSERT INTO design_requests `+cols+`)
			VALUES ($1, 1, 'storefront', 'change', $2, 1, repeat('a',64), repeat('b',40), repeat('b',40), 'ai/shop-1/' || $3::text, 1, now(), now(), '[]', '{}')`, id, status, id)
		return err
	}
	if err := insert("00000000-0000-0000-0000-000000000010", "drafting"); err != nil {
		t.Fatal(err)
	}
	if err := insert("00000000-0000-0000-0000-000000000011", "previewing"); err == nil {
		t.Fatal("two active requests on one site were accepted")
	}
	if err := insert("00000000-0000-0000-0000-000000000012", "failed"); err != nil {
		t.Fatalf("a finished request must not collide: %v", err)
	}

	// down, down: everything is gone (triggers first, then tables).
	if err := m.Steps(-2); err != nil {
		t.Fatalf("down: %v", err)
	}
	for _, tbl := range tables {
		if count(`SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename='`+tbl+`'`) != 0 {
			t.Fatalf("table %s survived down", tbl)
		}
	}
	if n := count(`SELECT count(*) FROM pg_proc WHERE proname LIKE 'design_%'`); n != 0 {
		t.Fatalf("%d design functions survived down", n)
	}
	// ... and up again.
	if err := m.Steps(2); err != nil {
		t.Fatalf("up again: %v", err)
	}
	present()
}
