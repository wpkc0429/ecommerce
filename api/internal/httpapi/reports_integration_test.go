package httpapi_test

// change merchant-reports, HTTP layer.
//
// The counting criteria themselves are proved in internal/report's own
// integration tests (against real orders, with real numbers). This file covers
// what only the router can answer: the AUTHORIZATION boundary (report.view),
// the TENANT boundary (403 vs 404 are answers to two different questions), the
// 422 surface, and the settings endpoint's promise that it touches nothing but
// one column.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	entshop "ksdevworks/ecommerce/api/internal/ent/shop"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/report"
	"ksdevworks/ecommerce/api/internal/testutil"
)

const reportsDefaultTZ = "Asia/Taipei"

type reportsEnv struct {
	client *ent.Client
	rdb    *redis.Client
	router http.Handler
	issuer *auth.TokenIssuer

	shopA, shopB int

	ownerA  int // report.view + shop.update on shop A
	viewerA int // shop A, order.view only — no report.view, no shop.update
	ownerB  int // report.view + shop.update on shop B only
}

func newReportsEnv(t *testing.T) *reportsEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &reportsEnv{client: client, rdb: rdb}

	nodes := []string{"report.view", "shop.update", "order.view"}
	permIDs := map[string]int{}
	for _, p := range nodes {
		permIDs[p] = client.Permission.Create().SetName(p).SaveX(ctx).ID
	}
	ownerRole := client.Role.Create().SetName("merchant_owner").SetScope("merchant").SaveX(ctx)
	for _, p := range []string{"report.view", "shop.update", "order.view"} {
		client.RolePermission.Create().SetRoleID(ownerRole.ID).SetPermissionID(permIDs[p]).SaveX(ctx)
	}
	// Can enter the shop and read orders, but holds neither new-node access.
	// This is the shape that proves report.view guards something: without such
	// a role, "everyone in the shop can reach it" would look identical.
	viewerRole := client.Role.Create().SetName("viewer_no_reports").SetScope("merchant").SaveX(ctx)
	client.RolePermission.Create().SetRoleID(viewerRole.ID).SetPermissionID(permIDs["order.view"]).SaveX(ctx)

	e.shopA = client.Shop.Create().SetName("Reports A").SetStatus(1).SaveX(ctx).ID
	e.shopB = client.Shop.Create().SetName("Reports B").SetStatus(1).SaveX(ctx).ID

	adminHash, _ := auth.HashPassword("password-123")
	mk := func(email string, shopID, roleID int) int {
		id := client.User.Create().SetEmail(email).SetPasswordHash(adminHash).SaveX(ctx).ID
		client.ShopUser.Create().SetShopID(shopID).SetUserID(id).SaveX(ctx)
		client.RoleUser.Create().SetUserID(id).SetRoleID(roleID).SetShopID(shopID).SaveX(ctx)
		return id
	}
	e.ownerA = mk("reports-owner-a@t.dev", e.shopA, ownerRole.ID)
	e.viewerA = mk("reports-viewer-a@t.dev", e.shopA, viewerRole.ID)
	e.ownerB = mk("reports-owner-b@t.dev", e.shopB, ownerRole.ID)

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	log := slog.New(slog.DiscardHandler)
	engine := rbac.NewEngine(client, rdb, log)
	authz := &httpapi.AuthzMW{Engine: engine}

	e.router = httpapi.New(httpapi.Deps{
		Cfg:     &config.Config{},
		Log:     log,
		AdminMW: httpapi.NewAdminMiddleware(issuer),
		Reports: &httpapi.ReportsHandler{
			Client: client, Authz: authz, Log: log,
			Service: &report.Service{Client: client, DefaultTimezone: reportsDefaultTZ},
		},
		ShopSettings: &httpapi.ShopSettingsHandler{
			Client: client, Authz: authz, Log: log, DefaultTimezone: reportsDefaultTZ,
		},
	})
	return e
}

func (e *reportsEnv) call(t *testing.T, userID int, method, path string, body any) (int, map[string]any, string) {
	t.Helper()
	tok, err := e.issuer.IssueAdmin(userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var rdr *bytes.Reader
	if body == nil {
		rdr = bytes.NewReader(nil)
	} else {
		raw, merr := json.Marshal(body)
		if merr != nil {
			t.Fatal(merr)
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	var parsed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, parsed, rec.Body.String()
}

func salesPath(shopID int, qs string) string {
	return fmt.Sprintf("/api/v1/admin/shops/%d/reports/sales%s", shopID, qs)
}

func productsPath(shopID int, qs string) string {
	return fmt.Sprintf("/api/v1/admin/shops/%d/reports/products%s", shopID, qs)
}

func settingsPath(shopID int) string {
	return fmt.Sprintf("/api/v1/admin/shops/%d/settings", shopID)
}

const julyQS = "?from=2026-07-01&to=2026-07-31"

// Scenario: 未持有報表權限者被拒絕 (spec merchant-reports/報表由單一權限節點守衛).
func TestReportsRequireReportView(t *testing.T) {
	e := newReportsEnv(t)
	for _, path := range []string{salesPath(e.shopA, julyQS), productsPath(e.shopA, julyQS)} {
		if code, _, body := e.call(t, e.viewerA, "GET", path, nil); code != http.StatusForbidden {
			t.Errorf("%s as a user without report.view: %d, want 403 (%s)", path, code, body)
		}
		if code, _, body := e.call(t, e.ownerA, "GET", path, nil); code != http.StatusOK {
			t.Errorf("%s as report.view holder: %d, want 200 (%s)", path, code, body)
		}
	}
}

// Scenario: 呼叫端無權存取該商家 → 403，且判定先於權限節點
// (spec merchant-reports/報表由單一權限節點守衛).
func TestReportsCrossShopIsForbidden(t *testing.T) {
	e := newReportsEnv(t)
	// ownerB holds report.view — on shop B. Naming shop A must not work.
	if code, _, body := e.call(t, e.ownerB, "GET", salesPath(e.shopA, julyQS), nil); code != http.StatusForbidden {
		t.Errorf("cross-shop report: %d, want 403 (%s)", code, body)
	}
	if code, _, body := e.call(t, e.ownerB, "PUT", settingsPath(e.shopA),
		map[string]any{"timezone": "UTC"}); code != http.StatusForbidden {
		t.Errorf("cross-shop settings write: %d, want 403 (%s)", code, body)
	}
}

// Scenario: 拒絕不合法的期間 (spec merchant-reports/期間以商家的當地日界定).
func TestReportsRejectBadPeriods(t *testing.T) {
	e := newReportsEnv(t)
	cases := []struct{ name, qs string }{
		{"no params at all", ""},
		{"missing to", "?from=2026-07-01"},
		{"missing from", "?to=2026-07-31"},
		{"unparseable from", "?from=07%2F01%2F2026&to=2026-07-31"},
		// A full timestamp is refused rather than silently truncated: accepting
		// it would invite an offset into a comparison that has none.
		{"timestamp instead of a day", "?from=2026-07-01T00:00:00Z&to=2026-07-31"},
		{"to before from", "?from=2026-07-31&to=2026-07-01"},
		{"period too long", "?from=2020-01-01&to=2026-12-31"},
		{"bad group_by", "?from=2026-07-01&to=2026-07-31&group_by=week"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, parsed, body := e.call(t, e.ownerA, "GET", salesPath(e.shopA, tc.qs), nil)
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d, want 422 (%s)", code, body)
			}
			// The envelope must carry a pointer so a client can say WHICH
			// parameter is wrong (design D12 error envelope).
			errObj, _ := parsed["error"].(map[string]any)
			details, _ := errObj["details"].([]any)
			if len(details) == 0 {
				t.Fatalf("422 carries no details: %s", body)
			}
			first, _ := details[0].(map[string]any)
			if first["pointer"] == "" || first["pointer"] == nil {
				t.Errorf("detail has no pointer: %s", body)
			}
		})
	}
}

// Scenario: 未設定時區的商家取得的生效時區標示為回落 (spec shop-timezone).
func TestReportReportsTimezoneFallback(t *testing.T) {
	e := newReportsEnv(t)
	code, parsed, body := e.call(t, e.ownerA, "GET", salesPath(e.shopA, julyQS), nil)
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	tz, _ := parsed["timezone"].(map[string]any)
	if tz["name"] != reportsDefaultTZ {
		t.Errorf("timezone.name = %v, want %s", tz["name"], reportsDefaultTZ)
	}
	if tz["source"] != "default" {
		t.Errorf("timezone.source = %v, want \"default\" — a shop that never configured a zone "+
			"must not be shown as having chosen one", tz["source"])
	}
	// An empty period still answers 200 with an empty currency list, not 404:
	// "you made no sales in July" is an answer, not a missing resource.
	if cs, ok := parsed["currencies"].([]any); !ok || len(cs) != 0 {
		t.Errorf("currencies = %#v, want an empty array", parsed["currencies"])
	}
}

// ── 商家設定 ──────────────────────────────────────────────────────────

// Scenario: 商家擁有者設定自己商家的時區 / 清除設定 (spec shop-timezone).
func TestUpdateShopTimezoneRoundTrip(t *testing.T) {
	e := newReportsEnv(t)

	code, parsed, body := e.call(t, e.ownerA, "PUT", settingsPath(e.shopA),
		map[string]any{"timezone": "America/New_York"})
	if code != http.StatusOK {
		t.Fatalf("set: %d (%s)", code, body)
	}
	if parsed["timezone"] != "America/New_York" {
		t.Errorf("timezone = %v, want America/New_York", parsed["timezone"])
	}
	eff, _ := parsed["effective_timezone"].(map[string]any)
	if eff["name"] != "America/New_York" || eff["source"] != "shop" {
		t.Errorf("effective_timezone = %#v, want {America/New_York shop}", eff)
	}

	// The report now cuts days in the shop's own zone, and says so.
	_, rep, _ := e.call(t, e.ownerA, "GET", salesPath(e.shopA, julyQS), nil)
	tz, _ := rep["timezone"].(map[string]any)
	if tz["name"] != "America/New_York" || tz["source"] != "shop" {
		t.Errorf("report timezone = %#v, want {America/New_York shop}", tz)
	}

	// null clears it — back to UNSET, not "set to the platform default value".
	// Those are two different facts and only null expresses the first
	// (design D1).
	code, parsed, body = e.call(t, e.ownerA, "PUT", settingsPath(e.shopA),
		map[string]any{"timezone": nil})
	if code != http.StatusOK {
		t.Fatalf("clear: %d (%s)", code, body)
	}
	if parsed["timezone"] != nil {
		t.Errorf("timezone after clearing = %v, want null", parsed["timezone"])
	}
	eff, _ = parsed["effective_timezone"].(map[string]any)
	if eff["source"] != "default" {
		t.Errorf("source after clearing = %v, want default", eff["source"])
	}
}

// Setting the zone to the SAME STRING as the platform default must still report
// source=shop. The arithmetic is identical; the sentence on screen must not be.
func TestSettingTheDefaultValueIsStillAChoice(t *testing.T) {
	e := newReportsEnv(t)
	_, parsed, _ := e.call(t, e.ownerA, "PUT", settingsPath(e.shopA),
		map[string]any{"timezone": reportsDefaultTZ})
	eff, _ := parsed["effective_timezone"].(map[string]any)
	if eff["source"] != "shop" {
		t.Errorf("source = %v, want shop — an explicit choice that happens to equal the default "+
			"is still a choice", eff["source"])
	}
}

// Scenario: 拒絕不合法的時區名稱 (spec shop-timezone).
func TestUpdateShopTimezoneRejectsInvalidNames(t *testing.T) {
	e := newReportsEnv(t)
	for _, bad := range []string{"Asia/Taipeh", "GMT+8", "+08:00", "Local", "../../etc/passwd"} {
		code, _, body := e.call(t, e.ownerA, "PUT", settingsPath(e.shopA), map[string]any{"timezone": bad})
		if code != http.StatusUnprocessableEntity {
			t.Errorf("timezone=%q: %d, want 422 (%s)", bad, code, body)
		}
	}
	// Nothing was stored.
	s := e.client.Shop.Query().Where(entshop.IDEQ(e.shopA)).OnlyX(context.Background())
	if s.Timezone != nil {
		t.Errorf("a rejected value was stored: %v", *s.Timezone)
	}
}

// Scenario: 更新時區不影響商家的其他欄位 (spec shop-timezone).
//
// The guarantee is structural (the UPDATE names one column), but it is the kind
// of thing a later "convenience" refactor breaks silently, so it is asserted on
// the stored row.
func TestUpdateShopTimezoneTouchesNothingElse(t *testing.T) {
	e := newReportsEnv(t)
	ctx := context.Background()
	before := e.client.Shop.Query().Where(entshop.IDEQ(e.shopA)).OnlyX(ctx)

	if code, _, body := e.call(t, e.ownerA, "PUT", settingsPath(e.shopA),
		map[string]any{"timezone": "UTC"}); code != http.StatusOK {
		t.Fatalf("update: %d (%s)", code, body)
	}
	after := e.client.Shop.Query().Where(entshop.IDEQ(e.shopA)).OnlyX(ctx)

	if after.Name != before.Name {
		t.Errorf("name changed: %q → %q", before.Name, after.Name)
	}
	if after.Status != before.Status {
		t.Errorf("status changed: %d → %d", before.Status, after.Status)
	}
	if string(after.ContentJSON) != string(before.ContentJSON) {
		t.Errorf("content_json changed: %s → %s", before.ContentJSON, after.ContentJSON)
	}
	if string(after.Meta) != string(before.Meta) {
		t.Errorf("meta changed: %s → %s", before.Meta, after.Meta)
	}
	if after.Timezone == nil || *after.Timezone != "UTC" {
		t.Errorf("timezone = %v, want UTC", after.Timezone)
	}
	// Another shop's row must be untouched — the UPDATE is shop-scoped.
	other := e.client.Shop.Query().Where(entshop.IDEQ(e.shopB)).OnlyX(ctx)
	if other.Timezone != nil {
		t.Errorf("shop B's timezone was written to: %v", *other.Timezone)
	}
}

// Scenario: 無設定權限者 (spec admin-operations-ui/時區設定入口位於報表視圖).
//
// The server half of "an editor sees the zone but cannot change it": the UI
// hides the form, and this is what makes hiding it more than cosmetic.
func TestUpdateShopSettingsRequiresShopUpdate(t *testing.T) {
	e := newReportsEnv(t)
	if code, _, body := e.call(t, e.viewerA, "PUT", settingsPath(e.shopA),
		map[string]any{"timezone": "UTC"}); code != http.StatusForbidden {
		t.Errorf("settings write without shop.update: %d, want 403 (%s)", code, body)
	}
}
