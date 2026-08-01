package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/member"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// memberDirEnv exercises change member-directory: the merchant back-office
// customer roster. The scenarios that matter here are not CRUD — the endpoints
// are read-only — they are the DISCLOSURE boundary (which fields leave the
// server), the AUTHORIZATION boundary (member.view), and the TENANT boundary
// (403 vs 404 are different answers to different questions).
type memberDirEnv struct {
	client *ent.Client
	rdb    *redis.Client
	router http.Handler
	issuer *auth.TokenIssuer

	shopA, shopB int

	// Shop A customers, chosen to cover every disclosure case in one fixture:
	registeredA int // has a password → registered: true
	guestA      int // password_hash IS NULL → registered: false (guest checkout)
	tieredA     int // holds a member tier → level must be eager-loaded
	memberB     int // shop B, same email as registeredA: per-shop identity

	tierA int

	ownerA  int // holds member.view
	viewerA int // shop A user WITHOUT member.view
	ownerB  int // holds member.view on shop B only
}

// knownHash is a real bcrypt hash so the leak assertions have something with a
// recognizable prefix to look for. If password_hash ever reached a response,
// grepping for "$2a$"/"$2b$" finds it even if someone renamed the JSON key.
const memberDirPassword = "roster-fixture-password"

func newMemberDirEnv(t *testing.T) *memberDirEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	e := &memberDirEnv{client: client, rdb: rdb}

	nodes := []string{"member.view", "order.view"}
	permIDs := map[string]int{}
	for _, p := range nodes {
		permIDs[p] = client.Permission.Create().SetName(p).SaveX(ctx).ID
	}
	ownerRole := client.Role.Create().SetName("merchant_owner").SetScope("merchant").SaveX(ctx)
	for _, p := range nodes {
		client.RolePermission.Create().SetRoleID(ownerRole.ID).SetPermissionID(permIDs[p]).SaveX(ctx)
	}
	// A role that can enter the shop and read orders but holds no member.view.
	// This is the shape that proves the node actually guards something: without
	// it the endpoints would still be reachable by anyone in the shop.
	viewerRole := client.Role.Create().SetName("viewer_no_member_view").SetScope("merchant").SaveX(ctx)
	client.RolePermission.Create().SetRoleID(viewerRole.ID).SetPermissionID(permIDs["order.view"]).SaveX(ctx)

	e.shopA = client.Shop.Create().SetName("Roster A").SetStatus(1).SaveX(ctx).ID
	e.shopB = client.Shop.Create().SetName("Roster B").SetStatus(1).SaveX(ctx).ID

	memberHash, err := auth.HashPassword(memberDirPassword)
	if err != nil {
		t.Fatal(err)
	}
	e.registeredA = client.Member.Create().
		SetShopID(e.shopA).SetEmail("Alice.Chen@example.com").SetPasswordHash(memberHash).
		SetStatus(1).SaveX(ctx).ID
	// A passwordless identity is exactly what guest checkout creates
	// (guest-checkout design D10: "無法登入" is expressed by password_hash being
	// NULL, never by status).
	e.guestA = client.Member.Create().
		SetShopID(e.shopA).SetEmail("walkin-guest@example.com").SetStatus(1).SaveX(ctx).ID

	e.tierA = client.MemberTier.Create().
		SetShopID(e.shopA).SetName("金卡會員").SetMinPoints(100).SetDiscountPercent(10).SaveX(ctx).ID
	e.tieredA = client.Member.Create().
		SetShopID(e.shopA).SetEmail("vip@example.com").SetPasswordHash(memberHash).
		SetStatus(1).SetPoints(120).SetLevelID(e.tierA).SaveX(ctx).ID

	// Same email at another shop: two independent identities
	// (member-identity-per-shop design D1). The roster must never surface it.
	e.memberB = client.Member.Create().
		SetShopID(e.shopB).SetEmail("Alice.Chen@example.com").SetPasswordHash(memberHash).
		SetStatus(1).SaveX(ctx).ID

	adminHash, _ := auth.HashPassword("password-123")
	e.ownerA = client.User.Create().SetEmail("roster-owner-a@t.dev").SetPasswordHash(adminHash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.ownerA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.ownerA).SetRoleID(ownerRole.ID).SetShopID(e.shopA).SaveX(ctx)

	e.viewerA = client.User.Create().SetEmail("roster-viewer-a@t.dev").SetPasswordHash(adminHash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopA).SetUserID(e.viewerA).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.viewerA).SetRoleID(viewerRole.ID).SetShopID(e.shopA).SaveX(ctx)

	e.ownerB = client.User.Create().SetEmail("roster-owner-b@t.dev").SetPasswordHash(adminHash).SaveX(ctx).ID
	client.ShopUser.Create().SetShopID(e.shopB).SetUserID(e.ownerB).SaveX(ctx)
	client.RoleUser.Create().SetUserID(e.ownerB).SetRoleID(ownerRole.ID).SetShopID(e.shopB).SaveX(ctx)

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
		Members: &httpapi.MembersHandler{
			Client: client, Service: &member.Service{Client: client}, Authz: authz, Log: log,
		},
	})
	return e
}

func (e *memberDirEnv) call(t *testing.T, userID int, path string) (int, string) {
	t.Helper()
	tok, err := e.issuer.IssueAdmin(userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", path, bytes.NewReader(nil))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (e *memberDirEnv) callJSON(t *testing.T, userID int, path string) (int, map[string]any, string) {
	t.Helper()
	code, raw := e.call(t, userID, path)
	var parsed map[string]any
	_ = json.Unmarshal([]byte(raw), &parsed)
	return code, parsed, raw
}

func (e *memberDirEnv) rosterPath(shopID int, suffix string) string {
	return fmt.Sprintf("/api/v1/admin/shops/%d/members%s", shopID, suffix)
}

// memberIDs extracts the id list from a roster page, preserving order.
func memberIDs(t *testing.T, page map[string]any) []int {
	t.Helper()
	raw, _ := page["members"].([]any)
	out := make([]int, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("member element is not an object: %#v", v)
		}
		out = append(out, int(m["id"].(float64)))
	}
	return out
}

func containsID(list []int, want int) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// Scenario: 列出商家的顧客 / 名冊僅回傳本商家顧客
// (spec member-directory/商家可瀏覽並搜尋自己的顧客名冊, 顧客名冊查詢受租戶隔離約束).
func TestMemberRosterListsOnlyThisShopsCustomers(t *testing.T) {
	e := newMemberDirEnv(t)

	code, page, _ := e.callJSON(t, e.ownerA, e.rosterPath(e.shopA, ""))
	if code != http.StatusOK {
		t.Fatalf("list: want 200, got %d", code)
	}
	ids := memberIDs(t, page)
	for _, want := range []int{e.registeredA, e.guestA, e.tieredA} {
		if !containsID(ids, want) {
			t.Fatalf("shop A roster must contain member %d: %v", want, ids)
		}
	}
	// Shop B's identity shares Alice's email; the roster must not reach it even
	// though a substring search for her email would match both rows if the
	// shop_id predicate were missing.
	if containsID(ids, e.memberB) {
		t.Fatalf("shop A roster leaked shop B's member %d: %v", e.memberB, ids)
	}
	if got := int(page["total"].(float64)); got != 3 {
		t.Fatalf("total: want 3, got %d", got)
	}
	if got := int(page["page"].(float64)); got != 1 {
		t.Fatalf("page: want 1, got %d", got)
	}
}

// Scenario: 以 email 的一部分搜尋 / 關鍵字為數字時亦比對顧客識別碼.
func TestMemberRosterSearch(t *testing.T) {
	e := newMemberDirEnv(t)

	// Case-insensitive substring: the fixture email is "Alice.Chen@example.com".
	_, page, _ := e.callJSON(t, e.ownerA, e.rosterPath(e.shopA, "?q=alice"))
	ids := memberIDs(t, page)
	if len(ids) != 1 || ids[0] != e.registeredA {
		t.Fatalf("q=alice should match exactly the registered customer: %v", ids)
	}

	// A numeric keyword additionally matches the member id — a merchant reads
	// "會員 #57" off an order detail and pastes 57 in here.
	_, page, _ = e.callJSON(t, e.ownerA, e.rosterPath(e.shopA, fmt.Sprintf("?q=%d", e.guestA)))
	ids = memberIDs(t, page)
	if !containsID(ids, e.guestA) {
		t.Fatalf("numeric q must match the member id: %v", ids)
	}

	// A numeric keyword that names another shop's member matches nothing here:
	// the shop predicate still applies.
	_, page, _ = e.callJSON(t, e.ownerA, e.rosterPath(e.shopA, fmt.Sprintf("?q=%d", e.memberB)))
	if got := int(page["total"].(float64)); got != 0 {
		t.Fatalf("searching another shop's member id must match nothing, got %d", got)
	}

	_, page, _ = e.callJSON(t, e.ownerA, e.rosterPath(e.shopA, "?q=nobody-by-this-name"))
	if got := int(page["total"].(float64)); got != 0 {
		t.Fatalf("no-match search: want total 0, got %d", got)
	}
}

// Scenario: 名冊帶出點數與等級.
func TestMemberRosterCarriesPointsAndTier(t *testing.T) {
	e := newMemberDirEnv(t)

	code, m, _ := e.callJSON(t, e.ownerA, e.rosterPath(e.shopA, fmt.Sprintf("/%d", e.tieredA)))
	if code != http.StatusOK {
		t.Fatalf("get: want 200, got %d", code)
	}
	if got := int(m["points"].(float64)); got != 120 {
		t.Fatalf("points: want 120, got %d", got)
	}
	level, ok := m["level"].(map[string]any)
	if !ok {
		t.Fatalf("level must be eager-loaded for a tiered customer: %#v", m["level"])
	}
	if level["name"] != "金卡會員" {
		t.Fatalf("level name: %#v", level["name"])
	}
	// A customer with no tier reports level_id/level as null rather than
	// omitting them — the same shape as MemberPoints.
	_, plain, _ := e.callJSON(t, e.ownerA, e.rosterPath(e.shopA, fmt.Sprintf("/%d", e.guestA)))
	if plain["level"] != nil || plain["level_id"] != nil {
		t.Fatalf("untiered customer must report null level/level_id: %#v %#v", plain["level"], plain["level_id"])
	}
}

// Scenario: 訪客結帳建立的身分被標示為未註冊 / 認領後的身分被標示為已註冊
// (spec member-directory/訪客身分在名冊上與已註冊顧客可分辨).
func TestMemberRosterDistinguishesGuestIdentities(t *testing.T) {
	e := newMemberDirEnv(t)

	_, guest, _ := e.callJSON(t, e.ownerA, e.rosterPath(e.shopA, fmt.Sprintf("/%d", e.guestA)))
	if guest["registered"] != false {
		t.Fatalf("a passwordless identity must report registered=false: %#v", guest["registered"])
	}
	_, registered, _ := e.callJSON(t, e.ownerA, e.rosterPath(e.shopA, fmt.Sprintf("/%d", e.registeredA)))
	if registered["registered"] != true {
		t.Fatalf("a password-holding identity must report registered=true: %#v", registered["registered"])
	}

	// Claiming (guest-checkout design D5) sets the password on the SAME row, so
	// the flag flips without any id changing.
	hash, _ := auth.HashPassword("claimed-now")
	e.client.Member.UpdateOneID(e.guestA).SetPasswordHash(hash).SaveX(context.Background())
	_, claimed, _ := e.callJSON(t, e.ownerA, e.rosterPath(e.shopA, fmt.Sprintf("/%d", e.guestA)))
	if claimed["registered"] != true {
		t.Fatalf("a claimed identity must report registered=true: %#v", claimed["registered"])
	}
	if int(claimed["id"].(float64)) != e.guestA {
		t.Fatalf("claiming must not change the member id")
	}
}

// Scenario: 密碼雜湊不出現在任何顧客回應中 / 顧客電話不由名冊揭露
// (spec member-directory/顧客名冊的揭露欄位有明確界線).
//
// The assertions are on the RAW RESPONSE BYTES on purpose (design D7). Two
// layers already stop the hash — ent's Sensitive() puts `json:"-"` on the field
// and every serializer here is a hand-written map — but both are one future line
// away from being undone (someone writing `WriteJSON(w, 200, m)`), and neither
// undoing would fail a test that merely checks the response parses. Grepping the
// bytes for the bcrypt prefix also survives someone renaming the JSON key.
func TestMemberRosterNeverLeaksPasswordHashOrPhone(t *testing.T) {
	e := newMemberDirEnv(t)

	// A phone on the row, so "phone is absent" is proven rather than vacuous:
	// nothing in production writes members.phone today (design D3), so without
	// this the assertion would pass on a NULL column no matter what the
	// serializer did.
	e.client.Member.UpdateOneID(e.registeredA).SetPhone("0912345678").SaveX(context.Background())

	paths := []string{
		e.rosterPath(e.shopA, ""),
		e.rosterPath(e.shopA, fmt.Sprintf("/%d", e.registeredA)),
	}
	for _, p := range paths {
		code, raw := e.call(t, e.ownerA, p)
		if code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d", p, code)
		}
		if !strings.Contains(raw, "Alice.Chen@example.com") {
			t.Fatalf("%s: the assertion is only meaningful if the payload really is this customer: %s", p, raw)
		}
		for _, forbidden := range []string{"password_hash", "$2a$", "$2b$", "$2y$"} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("%s: response leaked %q: %s", p, forbidden, raw)
			}
		}
		if strings.Contains(raw, "phone") || strings.Contains(raw, "0912345678") {
			t.Fatalf("%s: the roster must not disclose members.phone (design D3): %s", p, raw)
		}
	}
}

// Scenario: 不具權限者無法取得顧客名冊
// (spec member-directory/顧客名冊由 `member.view` 授權).
func TestMemberRosterRequiresMemberViewPermission(t *testing.T) {
	e := newMemberDirEnv(t)

	// viewerA can enter the shop and read orders; that must not be enough.
	if code, _ := e.call(t, e.viewerA, e.rosterPath(e.shopA, "")); code != http.StatusForbidden {
		t.Fatalf("list without member.view: want 403, got %d", code)
	}
	code, _ := e.call(t, e.viewerA, e.rosterPath(e.shopA, fmt.Sprintf("/%d", e.registeredA)))
	if code != http.StatusForbidden {
		t.Fatalf("get without member.view: want 403, got %d", code)
	}
}

// Scenario: 跨商家請求顧客名冊被拒 / 指定他店顧客識別碼回 404
// (spec member-directory/顧客名冊查詢受租戶隔離約束).
//
// The two cases answer different questions and therefore get different codes
// (design D8), and conflating them is the mistake this test exists to prevent.
func TestMemberRosterCrossShopIsolation(t *testing.T) {
	e := newMemberDirEnv(t)

	// (1) No access to that shop at all → the cross-shop guard, before any
	// handler runs.
	if code, _ := e.call(t, e.ownerA, e.rosterPath(e.shopB, "")); code != http.StatusForbidden {
		t.Fatalf("cross-shop list: want 403, got %d", code)
	}
	code, _ := e.call(t, e.ownerA, e.rosterPath(e.shopB, fmt.Sprintf("/%d", e.memberB)))
	if code != http.StatusForbidden {
		t.Fatalf("cross-shop get: want 403, got %d", code)
	}

	// (2) Access to the shop, but the member id belongs to another shop → the
	// explicit shop_id predicate misses. 404, and nothing of that customer in
	// the body: saying "does not exist" leaks less than confirming the id lives
	// somewhere.
	code, raw := e.call(t, e.ownerB, e.rosterPath(e.shopB, fmt.Sprintf("/%d", e.registeredA)))
	if code != http.StatusNotFound {
		t.Fatalf("foreign member id within an accessible shop: want 404, got %d: %s", code, raw)
	}
	if strings.Contains(raw, "Alice.Chen@example.com") {
		t.Fatalf("404 body must not disclose the foreign customer: %s", raw)
	}
}

// Scenario: 單頁筆數受上限約束 (spec member-directory).
func TestMemberRosterPageSizeIsCapped(t *testing.T) {
	e := newMemberDirEnv(t)
	ctx := context.Background()
	for i := range 4 {
		e.client.Member.Create().
			SetShopID(e.shopA).SetEmail(fmt.Sprintf("bulk-%d@example.com", i)).SetStatus(1).SaveX(ctx)
	}

	_, page, _ := e.callJSON(t, e.ownerA, e.rosterPath(e.shopA, "?page_size=5000"))
	if got := int(page["page_size"].(float64)); got != 100 {
		t.Fatalf("page_size must be capped at 100, got %d", got)
	}

	// 7 customers at 3 per page: 3 / 3 / 1.
	_, first, _ := e.callJSON(t, e.ownerA, e.rosterPath(e.shopA, "?page=1&page_size=3"))
	_, second, _ := e.callJSON(t, e.ownerA, e.rosterPath(e.shopA, "?page=2&page_size=3"))
	_, third, _ := e.callJSON(t, e.ownerA, e.rosterPath(e.shopA, "?page=3&page_size=3"))
	firstIDs, secondIDs, thirdIDs := memberIDs(t, first), memberIDs(t, second), memberIDs(t, third)
	if len(firstIDs) != 3 || len(secondIDs) != 3 || len(thirdIDs) != 1 {
		t.Fatalf("pagination split: page1=%v page2=%v page3=%v (7 customers total)", firstIDs, secondIDs, thirdIDs)
	}
	if got := int(first["total"].(float64)); got != 7 {
		t.Fatalf("total must count the whole set, not the page: %d", got)
	}
	secondIDs = append(secondIDs, thirdIDs...)
	for _, id := range firstIDs {
		if containsID(secondIDs, id) {
			t.Fatalf("pages must not overlap: %v / %v", firstIDs, secondIDs)
		}
	}
}
