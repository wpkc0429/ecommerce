package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/account"
	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/admincredentialtoken"
	"ksdevworks/ecommerce/api/internal/ent/permission"
	"ksdevworks/ecommerce/api/internal/ent/role"
	"ksdevworks/ecommerce/api/internal/ent/shop"
	"ksdevworks/ecommerce/api/internal/ent/user"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/provision"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/seed"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// Values below are throwaway test constants, generated for this file only.
const (
	acctAdminEmail    = "root-admin@t.dev"
	acctAdminPassword = "seed-admin-pw-for-tests"
	acctOwnerPassword = "correct-horse-battery"
	acctPublicURL     = "http://admin.test"
)

type acctEnv struct {
	client  *ent.Client
	router  http.Handler
	issuer  *auth.TokenIssuer
	refresh *auth.RefreshService
	svc     *account.Service
	adminID int
	shopA   int
}

func newAcctEnv(t *testing.T) *acctEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	if err := seed.Run(ctx, client, seed.Options{AdminEmail: acctAdminEmail, AdminPassword: acctAdminPassword}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e := &acctEnv{client: client}
	e.adminID = client.User.Query().Where(user.EmailEQ(acctAdminEmail)).OnlyX(ctx).ID
	e.shopA = client.Shop.Create().SetName("Shop A").SetStatus(1).SaveX(ctx).ID

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	e.issuer = issuer
	e.refresh = auth.NewRefreshService(client, 30*24*time.Hour)
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	engine := rbac.NewEngine(client, rdb, log)
	authz := &httpapi.AuthzMW{Engine: engine}
	dispatcher := events.NewDispatcher(log)
	e.svc = &account.Service{
		Client: client, Engine: engine, Refresh: e.refresh, Delivery: account.ManualDelivery{},
		PublicURL: acctPublicURL, SetupTTL: 72 * time.Hour, ResetTTL: 24 * time.Hour,
	}
	cmsService := &cms.Service{Client: client, Dispatcher: dispatcher}
	limiter := ratelimit.New(rdb, log)
	e.router = httpapi.New(httpapi.Deps{
		Cfg:       &config.Config{},
		Log:       log,
		AdminMW:   httpapi.NewAdminMiddleware(issuer),
		AdminAuth: &httpapi.AdminAuthHandler{Client: client, Issuer: issuer, Refresh: e.refresh, Engine: engine, Log: log},
		Roles:     &httpapi.RolesHandler{Client: client, Engine: engine, Authz: authz, Log: log},
		Sites:     &httpapi.SitesHandler{Client: client, Dispatcher: dispatcher, Log: log, Authz: authz},
		Accounts:  &httpapi.AccountsHandler{Service: e.svc, Authz: authz, Log: log},
		Provisioning: &httpapi.ProvisioningHandler{
			Service: &provision.Service{Client: client, CMS: cmsService, Accounts: e.svc, Dispatcher: dispatcher},
			Engine:  engine, Authz: authz, Log: log,
		},
		AdminPasswordSetupRateLimit: httpapi.NewAdminPasswordSetupRateLimit(limiter, config.RateLimitConfig{
			LoginBroadLimit: 1000, LoginBroadWindow: time.Minute,
		}),
	})
	return e
}

func (e *acctEnv) do(t *testing.T, method, path, token string, body any) (int, map[string]any, string) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	raw := rec.Body.String()
	var parsed map[string]any
	_ = json.Unmarshal([]byte(raw), &parsed)
	return rec.Code, parsed, raw
}

func (e *acctEnv) tokenFor(t *testing.T, userID int) string {
	t.Helper()
	tok, err := e.issuer.IssueAdmin(userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (e *acctEnv) roleID(t *testing.T, name string) int {
	t.Helper()
	return e.client.Role.Query().Where(role.NameEQ(name)).OnlyX(context.Background()).ID
}

// tokenFromURL extracts the token from {PublicURL}/#/set-password/{token}.
func tokenFromURL(t *testing.T, u string) string {
	t.Helper()
	prefix := acctPublicURL + "/#/set-password/"
	if !strings.HasPrefix(u, prefix) {
		t.Fatalf("link %q does not start with %q", u, prefix)
	}
	return strings.TrimPrefix(u, prefix)
}

func (e *acctEnv) login(t *testing.T, email, password string) (int, map[string]any) {
	t.Helper()
	code, body, _ := e.do(t, "POST", "/api/v1/admin/auth/login", "", map[string]string{"email": email, "password": password})
	return code, body
}

// createOwner creates an account as the super admin and returns (user id, token).
func (e *acctEnv) createOwner(t *testing.T, email string) (int, string) {
	t.Helper()
	code, body, raw := e.do(t, "POST", "/api/v1/admin/accounts", e.tokenFor(t, e.adminID), map[string]any{
		"email": email, "shop_id": e.shopA, "role_id": e.roleID(t, "merchant_owner"),
	})
	if code != http.StatusCreated {
		t.Fatalf("create account: %d %s", code, raw)
	}
	return int(body["user_id"].(float64)), tokenFromURL(t, body["setup_url"].(string))
}

func (e *acctEnv) setPassword(t *testing.T, token, password string) int {
	t.Helper()
	code, _, _ := e.do(t, "POST", "/api/v1/admin/auth/password-setup", "", map[string]string{"token": token, "password": password})
	return code
}

// Scenario: 建立帳號並取得設定連結 / 設定密碼後可登入 / token 只能用一次 /
// 不接受管理員代填密碼 (spec account-provisioning).
func TestAccountCreateSetupAndSingleUse(t *testing.T) {
	e := newAcctEnv(t)
	admin := e.tokenFor(t, e.adminID)

	code, body, raw := e.do(t, "POST", "/api/v1/admin/accounts", admin, map[string]any{
		"email": "Owner@Bouquet.test", "shop_id": e.shopA, "role_id": e.roleID(t, "merchant_owner"),
		"password": "admin-chosen-password-1", // must be ignored
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, raw)
	}
	if body["delivery"] != "manual" || body["expires_at"] == nil {
		t.Fatalf("delivery fields: %s", raw)
	}
	uid := int(body["user_id"].(float64))
	token := tokenFromURL(t, body["setup_url"].(string))

	// The account exists without a password; neither guess nor the ignored
	// admin-supplied value logs in.
	u := e.client.User.GetX(context.Background(), uid)
	if u.PasswordHash != nil {
		t.Fatal("password_hash must be NULL for a provisioned account")
	}
	for _, pw := range []string{"admin-chosen-password-1", "anything-at-all-123"} {
		if c, _ := e.login(t, "owner@bouquet.test", pw); c != http.StatusUnauthorized {
			t.Fatalf("login before setup with %q: %d", pw, c)
		}
	}
	// Unset-password failure is byte-identical to an unknown account.
	_, _, rawNoPw := e.do(t, "POST", "/api/v1/admin/auth/login", "", map[string]string{"email": "owner@bouquet.test", "password": "x"})
	_, _, rawUnknown := e.do(t, "POST", "/api/v1/admin/auth/login", "", map[string]string{"email": "nobody@bouquet.test", "password": "x"})
	if rawNoPw != rawUnknown {
		t.Fatalf("unset-password login differs from unknown account:\n%s\n%s", rawNoPw, rawUnknown)
	}

	// Only the hash is stored.
	if n := e.client.AdminCredentialToken.Query().Where(admincredentialtoken.TokenHashEQ(token)).CountX(context.Background()); n != 0 {
		t.Fatal("plaintext token found in the database")
	}

	code, insp, _ := e.do(t, "POST", "/api/v1/admin/auth/password-setup/inspect", "", map[string]string{"token": token})
	if code != 200 || insp["email"] != "owner@bouquet.test" || insp["kind"] != "setup" {
		t.Fatalf("inspect: %d %v", code, insp)
	}

	// Too short: 422 and the link is NOT burned.
	if c := e.setPassword(t, token, "short-pw"); c != http.StatusUnprocessableEntity {
		t.Fatalf("short password: %d", c)
	}
	if c := e.setPassword(t, token, acctOwnerPassword); c != http.StatusNoContent {
		t.Fatalf("set password: %d", c)
	}
	if c, _ := e.login(t, "owner@bouquet.test", acctOwnerPassword); c != http.StatusOK {
		t.Fatalf("login after setup: %d", c)
	}
	// Second use and inspect: the same uniform 404.
	if c := e.setPassword(t, token, "another-long-password-2"); c != http.StatusNotFound {
		t.Fatalf("reuse: %d", c)
	}
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/auth/password-setup/inspect", "", map[string]string{"token": token}); c != http.StatusNotFound {
		t.Fatalf("inspect used token: %d", c)
	}
	if c, _ := e.login(t, "owner@bouquet.test", "another-long-password-2"); c != http.StatusUnauthorized {
		t.Fatalf("password changed by a reused token: %d", c)
	}
}

// Scenario: email 已存在 / 商家角色呼叫被拒 / 詳情不含密碼雜湊.
func TestAccountCreateConflictAuthzAndNoHashLeak(t *testing.T) {
	e := newAcctEnv(t)
	admin := e.tokenFor(t, e.adminID)
	ownerID, setup := e.createOwner(t, "owner@bouquet.test")
	if c := e.setPassword(t, setup, acctOwnerPassword); c != http.StatusNoContent {
		t.Fatalf("set password: %d", c)
	}

	if c, _, _ := e.do(t, "POST", "/api/v1/admin/accounts", admin, map[string]any{"email": "OWNER@bouquet.test"}); c != http.StatusConflict {
		t.Fatalf("duplicate email: %d", c)
	}
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/accounts", admin, map[string]any{"email": "not-an-email"}); c != http.StatusUnprocessableEntity {
		t.Fatalf("bad email: %d", c)
	}

	ownerTok := e.tokenFor(t, ownerID)
	for _, p := range [][2]string{
		{"POST", "/api/v1/admin/accounts"}, {"GET", "/api/v1/admin/accounts"},
		{"POST", "/api/v1/admin/shop-provisioning"}, {"GET", "/api/v1/admin/accounts/1"},
	} {
		if c, _, _ := e.do(t, p[0], p[1], ownerTok, map[string]any{"email": "x@t.dev"}); c != http.StatusForbidden {
			t.Fatalf("merchant owner %s %s: %d", p[0], p[1], c)
		}
	}

	code, _, raw := e.do(t, "GET", "/api/v1/admin/accounts/"+itoa(ownerID), admin, nil)
	if code != 200 {
		t.Fatalf("detail: %d %s", code, raw)
	}
	for _, bad := range []string{"password_hash", "$argon2", "$2a$", "$2b$"} {
		if strings.Contains(raw, bad) {
			t.Fatalf("detail leaks %q: %s", bad, raw)
		}
	}
	if !strings.Contains(raw, `"has_password":true`) || !strings.Contains(raw, `"merchant_owner"`) {
		t.Fatalf("detail content: %s", raw)
	}
	_, _, listRaw := e.do(t, "GET", "/api/v1/admin/accounts?q=bouquet", admin, nil)
	if strings.Contains(listRaw, "password_hash") || strings.Contains(listRaw, "$argon2") || !strings.Contains(listRaw, "owner@bouquet.test") {
		t.Fatalf("list: %s", listRaw)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// Scenario: 重發後舊連結失效 / 重設密碼踢掉既有 session / 過期連結.
func TestPasswordResetSupersedeSessionsAndExpiry(t *testing.T) {
	e := newAcctEnv(t)
	admin := e.tokenFor(t, e.adminID)
	ownerID, setup := e.createOwner(t, "owner@bouquet.test")
	if c := e.setPassword(t, setup, acctOwnerPassword); c != http.StatusNoContent {
		t.Fatalf("set password: %d", c)
	}
	_, login := e.login(t, "owner@bouquet.test", acctOwnerPassword)
	oldRefresh := login["refresh_token"].(string)

	reset := func() string {
		code, body, raw := e.do(t, "POST", "/api/v1/admin/accounts/"+itoa(ownerID)+"/password-reset", admin, nil)
		if code != 200 || body["delivery"] != "manual" || body["setup_url"] != nil {
			t.Fatalf("reset: %d %s", code, raw)
		}
		return tokenFromURL(t, body["reset_url"].(string))
	}
	first, second := reset(), reset()

	if c := e.setPassword(t, first, "brand-new-password-1"); c != http.StatusNotFound {
		t.Fatalf("superseded link: %d", c)
	}
	if c := e.setPassword(t, second, "brand-new-password-2"); c != http.StatusNoContent {
		t.Fatalf("latest link: %d", c)
	}
	// The refresh token from before the reset no longer exchanges.
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/auth/refresh", "", map[string]string{"refresh_token": oldRefresh}); c != http.StatusUnauthorized {
		t.Fatalf("refresh after reset: %d", c)
	}
	if c, _ := e.login(t, "owner@bouquet.test", "brand-new-password-2"); c != http.StatusOK {
		t.Fatalf("login with new password: %d", c)
	}

	// Expired link: 404 with the same shape as any other invalid one.
	third := reset()
	e.client.AdminCredentialToken.Update().
		Where(admincredentialtoken.TokenHashEQ(account.HashToken(third))).
		SetExpiresAt(time.Now().Add(-time.Minute)).ExecX(context.Background())
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/auth/password-setup/inspect", "", map[string]string{"token": third}); c != http.StatusNotFound {
		t.Fatalf("expired inspect: %d", c)
	}
	if c := e.setPassword(t, third, "brand-new-password-3"); c != http.StatusNotFound {
		t.Fatalf("expired set: %d", c)
	}
}

// Scenario: 並發消費恰好一次成功 (design D2).
func TestSetupTokenConcurrentConsumeExactlyOnce(t *testing.T) {
	e := newAcctEnv(t)
	_, setup := e.createOwner(t, "owner@bouquet.test")

	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = e.setPassword(t, setup, "concurrent-password-"+itoa(i))
		}()
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		switch c {
		case http.StatusNoContent:
			ok++
		case http.StatusNotFound:
		default:
			t.Fatalf("unexpected status %d in %v", c, codes)
		}
	}
	if ok != 1 {
		t.Fatalf("want exactly one success, got %d in %v", ok, codes)
	}
}

// Scenario: 停用後既有 access token 立即失效 / refresh 失效 / me 回 401 /
// 重新啟用後恢復 / 停用的超級管理員失去平台權限 (spec authentication + rbac).
func TestDisableTakesEffectOnIssuedAccessTokens(t *testing.T) {
	e := newAcctEnv(t)
	admin := e.tokenFor(t, e.adminID)
	ownerID, setup := e.createOwner(t, "owner@bouquet.test")
	e.setPassword(t, setup, acctOwnerPassword)
	_, login := e.login(t, "owner@bouquet.test", acctOwnerPassword)
	access, refresh := login["access_token"].(string), login["refresh_token"].(string)
	usersPath := "/api/v1/admin/shops/" + itoa(e.shopA) + "/users"

	if c, _, _ := e.do(t, "GET", usersPath, access, nil); c != 200 {
		t.Fatalf("owner before disable: %d", c)
	}
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/accounts/"+itoa(ownerID)+"/disable", admin, nil); c != http.StatusNoContent {
		t.Fatalf("disable: %d", c)
	}
	if c, _, _ := e.do(t, "GET", usersPath, access, nil); c != http.StatusForbidden {
		t.Fatalf("disabled owner with issued token: %d", c)
	}
	if c, _, _ := e.do(t, "GET", "/api/v1/admin/me", access, nil); c != http.StatusUnauthorized {
		t.Fatalf("/me for disabled: %d", c)
	}
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/auth/refresh", "", map[string]string{"refresh_token": refresh}); c != http.StatusUnauthorized {
		t.Fatalf("refresh for disabled: %d", c)
	}
	if c, _ := e.login(t, "owner@bouquet.test", acctOwnerPassword); c != http.StatusUnauthorized {
		t.Fatalf("login for disabled: %d", c)
	}

	if c, _, _ := e.do(t, "POST", "/api/v1/admin/accounts/"+itoa(ownerID)+"/enable", admin, nil); c != http.StatusNoContent {
		t.Fatalf("enable: %d", c)
	}
	if c, _, _ := e.do(t, "GET", usersPath, access, nil); c != 200 {
		t.Fatalf("owner after re-enable: %d", c)
	}
	if c, _ := e.login(t, "owner@bouquet.test", acctOwnerPassword); c != http.StatusOK {
		t.Fatalf("login after re-enable: %d", c)
	}
}

// Scenario: 停用的超級管理員失去平台權限.
func TestDisabledSuperAdminLosesPlatformPermissions(t *testing.T) {
	e := newAcctEnv(t)
	admin := e.tokenFor(t, e.adminID)
	second, setup := e.createOwner(t, "second-admin@t.dev")
	e.setPassword(t, setup, acctOwnerPassword)
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/users/"+itoa(second)+"/roles", admin, map[string]int{"role_id": e.roleID(t, "super_admin")}); c != 200 {
		t.Fatalf("assign super_admin: %d", c)
	}
	secondTok := e.tokenFor(t, second)
	if c, _, _ := e.do(t, "GET", "/api/v1/admin/accounts", secondTok, nil); c != 200 {
		t.Fatalf("second super_admin list: %d", c)
	}
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/accounts/"+itoa(second)+"/disable", admin, nil); c != http.StatusNoContent {
		t.Fatalf("disable second: %d", c)
	}
	if c, _, _ := e.do(t, "GET", "/api/v1/admin/accounts", secondTok, nil); c != http.StatusForbidden {
		t.Fatalf("disabled super_admin: %d", c)
	}
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/shops", secondTok, map[string]string{"name": "x"}); c == 200 || c == 201 {
		t.Fatalf("disabled super_admin created a shop: %d", c)
	}
}

// Scenario: 停用自己被拒 / 最後一位 super_admin 不可被停用或移除角色，並發下成立.
func TestLastSuperAdminProtections(t *testing.T) {
	e := newAcctEnv(t)
	admin := e.tokenFor(t, e.adminID)
	superRole := e.roleID(t, "super_admin")

	if c, _, _ := e.do(t, "POST", "/api/v1/admin/accounts/"+itoa(e.adminID)+"/disable", admin, nil); c != http.StatusConflict {
		t.Fatalf("disable self: %d", c)
	}
	// Sole super_admin: removing the role is refused too.
	if c, _, _ := e.do(t, "DELETE", "/api/v1/admin/users/"+itoa(e.adminID)+"/roles/"+itoa(superRole), admin, nil); c != http.StatusConflict {
		t.Fatalf("remove last super_admin role: %d", c)
	}

	second, setup := e.createOwner(t, "second-admin@t.dev")
	e.setPassword(t, setup, acctOwnerPassword)
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/users/"+itoa(second)+"/roles", admin, map[string]int{"role_id": superRole}); c != 200 {
		t.Fatalf("assign: %d", c)
	}
	secondTok := e.tokenFor(t, second)

	// Two admins disabling each other at once: never both succeed.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	pairs := [][3]any{{admin, second}, {secondTok, e.adminID}}
	for i := range pairs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _, _ = e.do(t, "POST", "/api/v1/admin/accounts/"+itoa(pairs[i][1].(int))+"/disable", pairs[i][0].(string), nil)
		}()
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == http.StatusNoContent {
			ok++
		} else if c != http.StatusConflict && c != http.StatusForbidden {
			t.Fatalf("unexpected status in %v", codes)
		}
	}
	if ok != 1 {
		t.Fatalf("exactly one mutual disable may succeed, got %v", codes)
	}
	active := e.client.User.Query().Where(user.StatusEQ(1), user.IDIn(e.adminID, second)).CountX(context.Background())
	if active != 1 {
		t.Fatalf("want exactly one active super_admin left, got %d", active)
	}
}

// Scenario: 移除成員資格 (spec account-provisioning).
func TestRemoveMembershipRevokesShopAccess(t *testing.T) {
	e := newAcctEnv(t)
	admin := e.tokenFor(t, e.adminID)
	ownerID, setup := e.createOwner(t, "owner@bouquet.test")
	e.setPassword(t, setup, acctOwnerPassword)
	tok := e.tokenFor(t, ownerID)
	usersPath := "/api/v1/admin/shops/" + itoa(e.shopA) + "/users"

	if c, _, _ := e.do(t, "GET", usersPath, tok, nil); c != 200 {
		t.Fatalf("before: %d", c)
	}
	if c, _, _ := e.do(t, "DELETE", "/api/v1/admin/accounts/"+itoa(ownerID)+"/memberships/"+itoa(e.shopA), admin, nil); c != http.StatusNoContent {
		t.Fatalf("remove: %d", c)
	}
	if c, _, _ := e.do(t, "GET", usersPath, tok, nil); c != http.StatusForbidden {
		t.Fatalf("after removal: %d", c)
	}
	if c, _, _ := e.do(t, "DELETE", "/api/v1/admin/accounts/"+itoa(ownerID)+"/memberships/"+itoa(e.shopA), admin, nil); c != http.StatusNotFound {
		t.Fatalf("remove twice: %d", c)
	}
	// Re-adding restores access; a platform-scope role is rejected as 422.
	ownerRole := e.roleID(t, "merchant_owner")
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/accounts/"+itoa(ownerID)+"/memberships", admin, map[string]int{"shop_id": e.shopA, "role_id": ownerRole}); c != 200 {
		t.Fatalf("re-add: %d", c)
	}
	if c, _, _ := e.do(t, "GET", usersPath, tok, nil); c != 200 {
		t.Fatalf("after re-add: %d", c)
	}
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/accounts/"+itoa(ownerID)+"/memberships", admin, map[string]int{"shop_id": e.shopA, "role_id": e.roleID(t, "super_admin")}); c != http.StatusUnprocessableEntity {
		t.Fatalf("platform role as membership: %d", c)
	}
}

// Scenario: 商家 owner 沒有平台權限 / 超級管理員的平台權限.
func TestMePlatformPermissions(t *testing.T) {
	e := newAcctEnv(t)
	ownerID, setup := e.createOwner(t, "owner@bouquet.test")
	e.setPassword(t, setup, acctOwnerPassword)

	_, me, raw := e.do(t, "GET", "/api/v1/admin/me", e.tokenFor(t, e.adminID), nil)
	perms, _ := me["platform_permissions"].([]any)
	has := map[string]bool{}
	for _, p := range perms {
		has[p.(string)] = true
	}
	if !has["account.manage"] || !has["shop.create"] || !has["account.view"] {
		t.Fatalf("super admin platform_permissions: %s", raw)
	}
	_, me, raw = e.do(t, "GET", "/api/v1/admin/me", e.tokenFor(t, ownerID), nil)
	if p, ok := me["platform_permissions"].([]any); !ok || len(p) != 0 {
		t.Fatalf("owner platform_permissions must be []: %s", raw)
	}
}

// Scenario: 一次開好一家店 / owner 設密碼後檢查項改變 / 既有帳號成為 owner /
// 網域衝突整筆回滾 / 權限組合.
func TestShopProvisioning(t *testing.T) {
	e := newAcctEnv(t)
	ctx := context.Background()
	admin := e.tokenFor(t, e.adminID)

	code, body, raw := e.do(t, "POST", "/api/v1/admin/shop-provisioning", admin, map[string]any{
		"name": "Bouquet", "domain": "bouquet.test", "owner_email": "Owner@Bouquet.test",
		"storefront_origins": []string{"https://bouquet.test"},
	})
	if code != http.StatusCreated {
		t.Fatalf("provision: %d %s", code, raw)
	}
	shopID := int(body["shop"].(map[string]any)["id"].(float64))
	owner := body["owner"].(map[string]any)
	if owner["created"] != true || owner["delivery"] != "manual" || owner["setup_url"] == nil {
		t.Fatalf("owner: %s", raw)
	}
	if b := body["binding"].(map[string]any); b["domain"] != "bouquet.test" || b["is_primary"] != true {
		t.Fatalf("binding: %s", raw)
	}
	ownerID := int(owner["user_id"].(float64))
	if !e.client.ShopUser.Query().Where().ExistX(ctx) {
		t.Fatal("no membership rows")
	}
	if n := e.client.RoleUser.Query().CountX(ctx); n < 2 { // super_admin + merchant_owner
		t.Fatalf("role assignments: %d", n)
	}

	statusPath := "/api/v1/admin/shops/" + itoa(shopID) + "/setup-status"
	_, st, raw := e.do(t, "GET", statusPath, admin, nil)
	if st["owner_assigned"] != true || st["domain_bound"] != true || st["owner_password_set"] != false ||
		st["home_published"] != false || st["theme_applied"] != false {
		t.Fatalf("status before: %s", raw)
	}
	e.setPassword(t, tokenFromURL(t, owner["setup_url"].(string)), acctOwnerPassword)
	_, st, raw = e.do(t, "GET", statusPath, admin, nil)
	if st["owner_password_set"] != true {
		t.Fatalf("status after password: %s", raw)
	}
	if c, _, _ := e.do(t, "GET", "/api/v1/admin/shops/99999/setup-status", admin, nil); c != http.StatusNotFound {
		t.Fatalf("setup-status unknown shop: %d", c)
	}
	if c, _, _ := e.do(t, "GET", statusPath, e.tokenFor(t, ownerID), nil); c != http.StatusForbidden {
		t.Fatalf("setup-status as merchant owner: %d", c)
	}

	// Existing account becomes owner of a second shop; password untouched.
	before := *e.client.User.GetX(ctx, ownerID).PasswordHash
	code, body, raw = e.do(t, "POST", "/api/v1/admin/shop-provisioning", admin, map[string]any{
		"name": "Second", "owner_email": "owner@bouquet.test",
	})
	if code != http.StatusCreated {
		t.Fatalf("provision existing owner: %d %s", code, raw)
	}
	owner = body["owner"].(map[string]any)
	if owner["created"] != false || owner["setup_url"] != nil || owner["delivery"] != nil {
		t.Fatalf("existing owner response: %s", raw)
	}
	if after := *e.client.User.GetX(ctx, ownerID).PasswordHash; after != before {
		t.Fatal("existing owner's password changed")
	}

	// Domain conflict: whole request rolls back (no shop, no account).
	shopsBefore, usersBefore := e.client.Shop.Query().CountX(ctx), e.client.User.Query().CountX(ctx)
	code, _, raw = e.do(t, "POST", "/api/v1/admin/shop-provisioning", admin, map[string]any{
		"name": "Clash", "domain": "bouquet.test", "owner_email": "clash-owner@bouquet.test",
	})
	if code != http.StatusConflict {
		t.Fatalf("domain conflict: %d %s", code, raw)
	}
	if e.client.Shop.Query().CountX(ctx) != shopsBefore || e.client.User.Query().CountX(ctx) != usersBefore {
		t.Fatal("conflicting provisioning left residue")
	}
	if e.client.Shop.Query().Where(shop.NameEQ("Clash")).ExistX(ctx) {
		t.Fatal("Clash shop survived")
	}
	// Origin collision rolls back too.
	code, _, _ = e.do(t, "POST", "/api/v1/admin/shop-provisioning", admin, map[string]any{
		"name": "Clash2", "owner_email": "clash2@bouquet.test", "storefront_origins": []string{"https://bouquet.test"},
	})
	if code != http.StatusConflict || e.client.Shop.Query().Where(shop.NameEQ("Clash2")).ExistX(ctx) {
		t.Fatalf("origin conflict: %d", code)
	}
	// Validation.
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/shop-provisioning", admin, map[string]any{"name": "NoOwner"}); c != http.StatusUnprocessableEntity {
		t.Fatalf("missing owner: %d", c)
	}
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/shop-provisioning", admin, map[string]any{"name": "", "owner_email": "a@b.test"}); c != http.StatusUnprocessableEntity {
		t.Fatalf("missing name: %d", c)
	}
}

// Scenario: 缺 shop.manage_domains 時帶 domain 回 403.
func TestShopProvisioningPermissionCombinations(t *testing.T) {
	e := newAcctEnv(t)
	ctx := context.Background()
	// A platform role holding only shop.create + account.manage.
	need := map[string]int{}
	for _, n := range []string{"shop.create", "account.manage"} {
		need[n] = e.client.Permission.Query().Where(permission.NameEQ(n)).OnlyX(ctx).ID
	}
	limited := e.client.Role.Create().SetName("provisioner").SetScope("platform").SaveX(ctx)
	for _, id := range need {
		e.client.RolePermission.Create().SetRoleID(limited.ID).SetPermissionID(id).SaveX(ctx)
	}
	hash, _ := auth.HashPassword(acctOwnerPassword)
	u := e.client.User.Create().SetEmail("provisioner@t.dev").SetPasswordHash(hash).SaveX(ctx)
	e.client.RoleUser.Create().SetUserID(u.ID).SetRoleID(limited.ID).SaveX(ctx)
	tok := e.tokenFor(t, u.ID)

	if c, _, raw := e.do(t, "POST", "/api/v1/admin/shop-provisioning", tok, map[string]any{
		"name": "P1", "domain": "p1.test", "owner_email": "p1@t.dev",
	}); c != http.StatusForbidden {
		t.Fatalf("domain without shop.manage_domains: %d %s", c, raw)
	}
	if c, _, raw := e.do(t, "POST", "/api/v1/admin/shop-provisioning", tok, map[string]any{
		"name": "P2", "owner_email": "p2@t.dev", "storefront_origins": []string{"https://p2.test"},
	}); c != http.StatusForbidden {
		t.Fatalf("origin without shop.manage_client_origins: %d %s", c, raw)
	}
	if c, _, raw := e.do(t, "POST", "/api/v1/admin/shop-provisioning", tok, map[string]any{
		"name": "P3", "owner_email": "p3@t.dev",
	}); c != http.StatusCreated {
		t.Fatalf("minimal provisioning: %d %s", c, raw)
	}
	if e.client.Shop.Query().Where(shop.NameEQ("P1")).ExistX(ctx) {
		t.Fatal("forbidden request created a shop")
	}
}

// Scenario: 兩個未認證端點以來源 IP 限流，超限 429 並帶 Retry-After.
func TestPasswordSetupRateLimited(t *testing.T) {
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	svc := &account.Service{
		Client: client, Engine: rbac.NewEngine(client, rdb, log),
		Refresh:  auth.NewRefreshService(client, time.Hour),
		Delivery: account.ManualDelivery{}, PublicURL: acctPublicURL,
		SetupTTL: time.Hour, ResetTTL: time.Hour,
	}
	limiter := ratelimit.New(rdb, log)
	router := httpapi.New(httpapi.Deps{
		Cfg: &config.Config{}, Log: log,
		Accounts: &httpapi.AccountsHandler{Service: svc, Authz: &httpapi.AuthzMW{}, Log: log},
		AdminPasswordSetupRateLimit: httpapi.NewAdminPasswordSetupRateLimit(limiter, config.RateLimitConfig{
			LoginBroadLimit: 3, LoginBroadWindow: time.Minute,
		}),
	})
	var last *httptest.ResponseRecorder
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/api/v1/admin/auth/password-setup/inspect", strings.NewReader(`{"token":"nope"}`))
		last = httptest.NewRecorder()
		router.ServeHTTP(last, req)
		if i < 3 && last.Code != http.StatusNotFound {
			t.Fatalf("request %d: %d", i, last.Code)
		}
	}
	if last.Code != http.StatusTooManyRequests || last.Header().Get("Retry-After") == "" {
		t.Fatalf("want 429 + Retry-After, got %d %v", last.Code, last.Header())
	}
}
