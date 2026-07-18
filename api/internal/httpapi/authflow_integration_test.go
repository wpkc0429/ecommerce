package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/member"
	"ksdevworks/ecommerce/api/internal/ent/memberrefreshtoken"
	"ksdevworks/ecommerce/api/internal/ent/shopmember"
	"ksdevworks/ecommerce/api/internal/ent/userrefreshtoken"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/tenant"
	"ksdevworks/ecommerce/api/internal/testutil"
)

type authEnv struct {
	client  *ent.Client
	router  http.Handler
	shopA   int
	shopB   int
	refresh *auth.RefreshService
}

// fakeTenantMW resolves X-Site-Domain via a static host→shop map, standing in
// for the real resolver (task 5.1) so auth flows are testable in isolation.
func fakeTenantMW(hosts map[string]int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := strings.ToLower(r.Header.Get("X-Site-Domain"))
			shopID, ok := hosts[host]
			if !ok {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(tenant.WithShopID(r.Context(), shopID)))
		})
	}
}

func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	ctx := context.Background()

	shopA := client.Shop.Create().SetName("Shop A").SaveX(ctx)
	shopB := client.Shop.Create().SetName("Shop B").SaveX(ctx)

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	refresh := auth.NewRefreshService(client, 30*24*time.Hour)
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	router := httpapi.New(httpapi.Deps{
		Cfg:        &config.Config{},
		Log:        log,
		AdminAuth:  &httpapi.AdminAuthHandler{Client: client, Issuer: issuer, Refresh: refresh, Log: log},
		MemberAuth: &httpapi.MemberAuthHandler{Client: client, Issuer: issuer, Refresh: refresh, Log: log},
		AdminMW:    httpapi.NewAdminMiddleware(issuer),
		MemberMW:   httpapi.NewMemberMiddleware(issuer),
		TenantMW: fakeTenantMW(map[string]int{
			"shop-a.test": shopA.ID,
			"shop-b.test": shopB.ID,
		}),
	})
	return &authEnv{client: client, router: router, shopA: shopA.ID, shopB: shopB.ID, refresh: refresh}
}

func (e *authEnv) do(t *testing.T, method, path string, headers map[string]string, body any) (int, map[string]any, string) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	raw := rec.Body.String()
	var parsed map[string]any
	_ = json.Unmarshal([]byte(raw), &parsed)
	return rec.Code, parsed, raw
}

func (e *authEnv) createUser(t *testing.T, email, password string, status int16) *ent.User {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	return e.client.User.Create().SetEmail(email).SetPasswordHash(hash).SetStatus(status).SaveX(context.Background())
}

func TestAdminLoginAndUniformFailures(t *testing.T) {
	e := newAuthEnv(t)
	e.createUser(t, "ok@test.dev", "correct-password", 1)
	e.createUser(t, "disabled@test.dev", "correct-password", 0)

	// Success.
	code, body, _ := e.do(t, "POST", "/api/v1/admin/auth/login", nil,
		map[string]string{"email": "OK@test.dev", "password": "correct-password"})
	if code != 200 || body["access_token"] == "" || body["refresh_token"] == "" {
		t.Fatalf("login: code=%d body=%v", code, body)
	}
	// Access token works against a protected endpoint.
	code, me, _ := e.do(t, "GET", "/api/v1/admin/me",
		map[string]string{"Authorization": "Bearer " + body["access_token"].(string)}, nil)
	if code != 200 || me["email"] != "ok@test.dev" {
		t.Fatalf("me: code=%d body=%v", code, me)
	}

	// Spec: 登入失敗不洩漏原因 — identical 401 body for wrong password,
	// unknown account, and disabled account.
	c1, _, r1 := e.do(t, "POST", "/api/v1/admin/auth/login", nil,
		map[string]string{"email": "ok@test.dev", "password": "wrong"})
	c2, _, r2 := e.do(t, "POST", "/api/v1/admin/auth/login", nil,
		map[string]string{"email": "no-such@test.dev", "password": "whatever"})
	c3, _, r3 := e.do(t, "POST", "/api/v1/admin/auth/login", nil,
		map[string]string{"email": "disabled@test.dev", "password": "correct-password"})
	if c1 != 401 || c2 != 401 || c3 != 401 {
		t.Fatalf("failure codes: %d %d %d", c1, c2, c3)
	}
	if r1 != r2 || r2 != r3 {
		t.Fatalf("failure bodies differ:\n%s\n%s\n%s", r1, r2, r3)
	}
}

func TestAdminRefreshRotationAndReplay(t *testing.T) {
	e := newAuthEnv(t)
	e.createUser(t, "u@test.dev", "password-123", 1)
	_, login, _ := e.do(t, "POST", "/api/v1/admin/auth/login", nil,
		map[string]string{"email": "u@test.dev", "password": "password-123"})
	r0 := login["refresh_token"].(string)

	// Rotate: r0 → r1.
	code, rot1, _ := e.do(t, "POST", "/api/v1/admin/auth/refresh", nil, map[string]string{"refresh_token": r0})
	if code != 200 {
		t.Fatalf("refresh: %d", code)
	}
	r1 := rot1["refresh_token"].(string)
	if r1 == r0 {
		t.Fatal("refresh token not rotated")
	}

	// Old token no longer works…
	code, _, _ = e.do(t, "POST", "/api/v1/admin/auth/refresh", nil, map[string]string{"refresh_token": r0})
	if code != 401 {
		t.Fatalf("replayed old refresh: want 401, got %d", code)
	}
	// …and the replay revoked the whole chain: r1 is dead too (spec 重放偵測).
	code, _, _ = e.do(t, "POST", "/api/v1/admin/auth/refresh", nil, map[string]string{"refresh_token": r1})
	if code != 401 {
		t.Fatalf("chain not revoked after replay: r1 got %d", code)
	}
	n := e.client.UserRefreshToken.Query().Where(userrefreshtoken.RevokedAtIsNil()).CountX(context.Background())
	if n != 0 {
		t.Fatalf("%d tokens still active after chain revocation", n)
	}
}

func TestAdminLogoutAndDisabledRefresh(t *testing.T) {
	e := newAuthEnv(t)
	u := e.createUser(t, "u@test.dev", "password-123", 1)
	_, login, _ := e.do(t, "POST", "/api/v1/admin/auth/login", nil,
		map[string]string{"email": "u@test.dev", "password": "password-123"})
	r0 := login["refresh_token"].(string)

	// Logout revokes the presented token.
	code, _, _ := e.do(t, "POST", "/api/v1/admin/auth/logout", nil, map[string]string{"refresh_token": r0})
	if code != 204 {
		t.Fatalf("logout: %d", code)
	}
	code, _, _ = e.do(t, "POST", "/api/v1/admin/auth/refresh", nil, map[string]string{"refresh_token": r0})
	if code != 401 {
		t.Fatalf("refresh after logout: want 401, got %d", code)
	}

	// Spec: 停用後 refresh 失效.
	_, login2, _ := e.do(t, "POST", "/api/v1/admin/auth/login", nil,
		map[string]string{"email": "u@test.dev", "password": "password-123"})
	u.Update().SetStatus(0).ExecX(context.Background())
	code, _, _ = e.do(t, "POST", "/api/v1/admin/auth/refresh", nil,
		map[string]string{"refresh_token": login2["refresh_token"].(string)})
	if code != 401 {
		t.Fatalf("disabled account refresh: want 401, got %d", code)
	}
}

func TestMemberRegistrationIsomorphism(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()
	hostA := map[string]string{"X-Site-Domain": "shop-a.test"}
	hostB := map[string]string{"X-Site-Domain": "shop-b.test"}

	// Scenario: 全新會員註冊 (shop A).
	codeA, bodyA, _ := e.do(t, "POST", "/api/v1/shop/auth/register", hostA,
		map[string]string{"email": "buyer@test.dev", "password": "password-123"})
	if codeA != 200 || bodyA["access_token"] == "" {
		t.Fatalf("register A: %d %v", codeA, bodyA)
	}
	m := e.client.Member.Query().Where(member.EmailEQ("buyer@test.dev")).OnlyX(ctx)
	if m.PasswordHash == nil || !strings.HasPrefix(*m.PasswordHash, "$argon2id$") {
		t.Fatal("member password not stored as $argon2id$ hash")
	}

	// Scenario: 既有平台身分於第二家店註冊 — same response shape, no new member.
	codeB, bodyB, _ := e.do(t, "POST", "/api/v1/shop/auth/register", hostB,
		map[string]string{"email": "buyer@test.dev", "password": "password-123"})
	if codeB != codeA {
		t.Fatalf("isomorphism broken: codes %d vs %d", codeA, codeB)
	}
	keysOf := func(m map[string]any) string {
		ks := make([]string, 0, len(m))
		for k := range m {
			ks = append(ks, k)
		}
		slices.Sort(ks) // map iteration order is randomized; compare a canonical shape
		return strings.Join(ks, ",")
	}
	if keysOf(bodyA) != keysOf(bodyB) {
		t.Fatalf("isomorphism broken: shapes %q vs %q", keysOf(bodyA), keysOf(bodyB))
	}
	if n := e.client.Member.Query().CountX(ctx); n != 1 {
		t.Fatalf("member rows: %d, want 1", n)
	}
	if n := e.client.ShopMember.Query().CountX(ctx); n != 2 {
		t.Fatalf("shop_member rows: %d, want 2", n)
	}

	// Wrong password on existing identity → generic failure, no membership.
	e.client.ShopMember.Delete().Where(shopmember.ShopIDEQ(e.shopB)).ExecX(ctx)
	code, body, _ := e.do(t, "POST", "/api/v1/shop/auth/register", hostB,
		map[string]string{"email": "buyer@test.dev", "password": "wrong-password"})
	if code != 422 {
		t.Fatalf("register with wrong password: want 422, got %d", code)
	}
	if msg := body["error"].(map[string]any)["message"].(string); strings.Contains(strings.ToLower(msg), "exist") {
		t.Fatalf("error message leaks existence: %q", msg)
	}
	if n := e.client.ShopMember.Query().Where(shopmember.ShopIDEQ(e.shopB)).CountX(ctx); n != 0 {
		t.Fatal("failed registration must not create membership")
	}
}

func TestMemberLoginAutoJoinAndTokenIsolation(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()
	hostA := map[string]string{"X-Site-Domain": "shop-a.test"}
	hostB := map[string]string{"X-Site-Domain": "shop-b.test"}

	_, regA, _ := e.do(t, "POST", "/api/v1/shop/auth/register", hostA,
		map[string]string{"email": "buyer@test.dev", "password": "password-123"})
	memberAccessA := regA["access_token"].(string)

	// Login on shop B auto-creates the membership (design D9 首次互動).
	code, loginB, _ := e.do(t, "POST", "/api/v1/shop/auth/login", hostB,
		map[string]string{"email": "buyer@test.dev", "password": "password-123"})
	if code != 200 {
		t.Fatalf("login B: %d", code)
	}
	if n := e.client.ShopMember.Query().Where(shopmember.ShopIDEQ(e.shopB)).CountX(ctx); n != 1 {
		t.Fatal("login did not auto-join shop B")
	}

	e.createUser(t, "staff@test.dev", "password-123", 1)
	_, adminLogin, _ := e.do(t, "POST", "/api/v1/admin/auth/login", nil,
		map[string]string{"email": "staff@test.dev", "password": "password-123"})
	adminAccess := adminLogin["access_token"].(string)

	// ── Token isolation matrix over HTTP (task 3.6) ──
	cases := []struct {
		name    string
		path    string
		headers map[string]string
		want    int
	}{
		{"member token on admin API", "/api/v1/admin/me",
			map[string]string{"Authorization": "Bearer " + memberAccessA}, 401},
		{"admin token on member API", "/api/v1/shop/me",
			map[string]string{"Authorization": "Bearer " + adminAccess, "X-Site-Domain": "shop-a.test"}, 401},
		{"shop A member token on shop B", "/api/v1/shop/me",
			map[string]string{"Authorization": "Bearer " + memberAccessA, "X-Site-Domain": "shop-b.test"}, 401},
		{"shop A member token on shop A", "/api/v1/shop/me",
			map[string]string{"Authorization": "Bearer " + memberAccessA, "X-Site-Domain": "shop-a.test"}, 200},
		{"admin token on admin API", "/api/v1/admin/me",
			map[string]string{"Authorization": "Bearer " + adminAccess}, 200},
	}
	for _, tc := range cases {
		code, _, raw := e.do(t, "GET", tc.path, tc.headers, nil)
		if code != tc.want {
			t.Errorf("%s: got %d want %d (%s)", tc.name, code, tc.want, raw)
		}
	}

	// Member refresh tokens are shop-bound: A-issued refresh fails on B.
	refreshA := regA["refresh_token"].(string)
	code, _, _ = e.do(t, "POST", "/api/v1/shop/auth/refresh", hostB, map[string]string{"refresh_token": refreshA})
	if code != 401 {
		t.Fatalf("cross-shop member refresh: want 401, got %d", code)
	}
	code, _, _ = e.do(t, "POST", "/api/v1/shop/auth/refresh", hostA, map[string]string{"refresh_token": refreshA})
	if code != 200 {
		t.Fatalf("same-shop member refresh: want 200, got %d", code)
	}

	// Member refresh replay revokes the member chain too.
	code, _, _ = e.do(t, "POST", "/api/v1/shop/auth/refresh", hostA, map[string]string{"refresh_token": refreshA})
	if code != 401 {
		t.Fatalf("member replay: want 401, got %d", code)
	}
	active := e.client.MemberRefreshToken.Query().
		Where(memberrefreshtoken.ShopIDEQ(e.shopA), memberrefreshtoken.RevokedAtIsNil()).
		CountX(ctx)
	if active != 0 {
		t.Fatalf("member chain not fully revoked: %d active", active)
	}
	_ = loginB
}
