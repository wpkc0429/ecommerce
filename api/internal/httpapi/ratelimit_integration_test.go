package httpapi_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/httpapi"
	"ksdevworks/ecommerce/api/internal/ratelimit"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// rlEnv is a self-contained fixture (separate from authEnv in
// authflow_integration_test.go) so its tiny, fast-expiring thresholds never
// leak into the other auth-flow assertions.
type rlEnv struct {
	client *ent.Client
	router http.Handler
	shopA  int
	shopB  int
}

// rlThresholds keeps windows short (milliseconds) so window-expiry tests run
// fast; the exact numbers don't matter, only the shape of the algorithm.
func rlThresholds() config.RateLimitConfig {
	const window = 300 * time.Millisecond
	return config.RateLimitConfig{
		LoginBroadLimit: 10, LoginBroadWindow: window,
		LoginTightLimit: 2, LoginTightWindow: window,
		RegisterBroadLimit: 10, RegisterBroadWindow: window,
		RegisterTightLimit: 2, RegisterTightWindow: window,
		RefreshLimit: 2, RefreshWindow: window,
	}
}

func newRLEnv(t *testing.T) *rlEnv {
	t.Helper()
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := t.Context()

	shopA := client.Shop.Create().SetName("Shop A").SaveX(ctx)
	shopB := client.Shop.Create().SetName("Shop B").SaveX(ctx)

	issuer, err := auth.NewTokenIssuer("test-admin-secret", "test-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	refresh := auth.NewRefreshService(client, 30*24*time.Hour)
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	limiter := ratelimit.New(rdb, log)
	cfg := rlThresholds()

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
		AdminLoginRateLimit:     httpapi.NewAdminLoginRateLimit(limiter, cfg),
		AdminRefreshRateLimit:   httpapi.NewAdminRefreshRateLimit(limiter, cfg),
		MemberLoginRateLimit:    httpapi.NewMemberLoginRateLimit(limiter, cfg),
		MemberRegisterRateLimit: httpapi.NewMemberRegisterRateLimit(limiter, cfg),
		MemberRefreshRateLimit:  httpapi.NewMemberRefreshRateLimit(limiter, cfg),
	})
	return &rlEnv{client: client, router: router, shopA: shopA.ID, shopB: shopB.ID}
}

// do issues one request from the given source IP (RemoteAddr), returning the
// status code, decoded body, and raw Retry-After header value.
func (e *rlEnv) do(t *testing.T, method, path, remoteAddr string, headers map[string]string, body any) (int, map[string]any, string) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var parsed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, parsed, rec.Header().Get("Retry-After")
}

func (e *rlEnv) createUser(t *testing.T, email, password string) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	e.client.User.Create().SetEmail(email).SetPasswordHash(hash).SetStatus(1).SaveX(t.Context())
}

// Scenario (authentication/Admin login and token issuance): 正常流量放行 —
// attempts under the tight (IP+email) threshold are never rate limited.
func TestRateLimitAllowsNormalTraffic(t *testing.T) {
	e := newRLEnv(t)
	e.createUser(t, "ok@test.dev", "correct-password")

	for i := 0; i < 2; i++ { // tight limit is 2 — both must go through to the handler
		code, _, _ := e.do(t, "POST", "/api/v1/admin/auth/login", "203.0.113.1:5001", nil,
			map[string]string{"email": "ok@test.dev", "password": "correct-password"})
		if code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d: unexpected 429 under threshold", i)
		}
	}
}

// Scenario (authentication/Admin login and token issuance): 單一帳號超限 — once
// the IP+email tight rule is exhausted, further attempts get 429 with
// Retry-After, and the request never reaches the login logic (a *correct*
// password after the limit is still rejected as 429, not 200).
func TestRateLimitBlocksAdminLoginOverThreshold(t *testing.T) {
	e := newRLEnv(t)
	e.createUser(t, "ok@test.dev", "correct-password")

	for i := 0; i < 2; i++ {
		code, _, _ := e.do(t, "POST", "/api/v1/admin/auth/login", "203.0.113.2:5001", nil,
			map[string]string{"email": "ok@test.dev", "password": "wrong"})
		if code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401 (under threshold), got %d", i, code)
		}
	}
	code, body, retryAfter := e.do(t, "POST", "/api/v1/admin/auth/login", "203.0.113.2:5001", nil,
		map[string]string{"email": "ok@test.dev", "password": "correct-password"})
	if code != http.StatusTooManyRequests {
		t.Fatalf("3rd attempt: want 429, got %d", code)
	}
	if retryAfter == "" {
		t.Fatal("429 response missing Retry-After header")
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok || errObj["code"] != "rate_limited" {
		t.Fatalf("unexpected error body: %v", body)
	}
}

// Scenario (authentication/Admin login and token issuance): 視窗過期後恢復.
func TestRateLimitRecoversAfterWindow(t *testing.T) {
	e := newRLEnv(t)
	e.createUser(t, "ok@test.dev", "correct-password")
	ip := "203.0.113.3:5001"

	for i := 0; i < 2; i++ {
		e.do(t, "POST", "/api/v1/admin/auth/login", ip, nil,
			map[string]string{"email": "ok@test.dev", "password": "wrong"})
	}
	if code, _, _ := e.do(t, "POST", "/api/v1/admin/auth/login", ip, nil,
		map[string]string{"email": "ok@test.dev", "password": "wrong"}); code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 before window elapses, got %d", code)
	}

	time.Sleep(400 * time.Millisecond) // window is 300ms

	code, _, _ := e.do(t, "POST", "/api/v1/admin/auth/login", ip, nil,
		map[string]string{"email": "ok@test.dev", "password": "correct-password"})
	if code != http.StatusOK {
		t.Fatalf("after window elapsed: want 200, got %d", code)
	}
}

// Scenario (authentication/Member registration and login in shop context):
// 跨商家流量互不干擾 — shop A exhausting its (shop+IP+email) quota must not
// affect shop B even with the identical IP and email.
func TestRateLimitShopScopeIsolation(t *testing.T) {
	e := newRLEnv(t)
	ip := "203.0.113.4:5001"
	hostA := map[string]string{"X-Site-Domain": "shop-a.test"}
	hostB := map[string]string{"X-Site-Domain": "shop-b.test"}
	creds := map[string]string{"email": "buyer@test.dev", "password": "password-123"}

	for i := 0; i < 2; i++ {
		code, _, _ := e.do(t, "POST", "/api/v1/shop/auth/register", ip, hostA, creds)
		if code != http.StatusOK {
			t.Fatalf("shop A attempt %d: want 200, got %d", i, code)
		}
	}
	code, _, _ := e.do(t, "POST", "/api/v1/shop/auth/register", ip, hostA, creds)
	if code != http.StatusTooManyRequests {
		t.Fatalf("shop A 3rd attempt: want 429, got %d", code)
	}

	// Shop B, same IP + email: independent quota, must still succeed.
	code, _, _ = e.do(t, "POST", "/api/v1/shop/auth/register", ip, hostB, creds)
	if code == http.StatusTooManyRequests {
		t.Fatal("shop B must not be blocked by shop A's exhausted quota")
	}
}

// Scenario (authentication/Admin login and token issuance): different source
// IPs never share a rate-limit bucket.
func TestRateLimitIPScopeIsolation(t *testing.T) {
	e := newRLEnv(t)
	e.createUser(t, "ok@test.dev", "correct-password")

	ipA := "203.0.113.5:5001"
	for i := 0; i < 2; i++ {
		e.do(t, "POST", "/api/v1/admin/auth/login", ipA, nil,
			map[string]string{"email": "ok@test.dev", "password": "wrong"})
	}
	if code, _, _ := e.do(t, "POST", "/api/v1/admin/auth/login", ipA, nil,
		map[string]string{"email": "ok@test.dev", "password": "wrong"}); code != http.StatusTooManyRequests {
		t.Fatalf("ipA 3rd attempt: want 429, got %d", code)
	}

	ipB := "203.0.113.6:5001"
	code, _, _ := e.do(t, "POST", "/api/v1/admin/auth/login", ipB, nil,
		map[string]string{"email": "ok@test.dev", "password": "correct-password"})
	if code != http.StatusOK {
		t.Fatalf("ipB must be unaffected by ipA's exhausted quota, got %d", code)
	}
}

// Scenario (authentication/Refresh token rotation and revocation): refresh
// 端點超限 — once the refresh rule is exhausted, further calls 429 and never
// reach the rotation logic (the still-valid token remains usable once the
// window clears).
func TestRateLimitBlocksRefreshOverThreshold(t *testing.T) {
	e := newRLEnv(t)
	e.createUser(t, "ok@test.dev", "correct-password")
	ip := "203.0.113.7:5001"

	_, login, _ := e.do(t, "POST", "/api/v1/admin/auth/login", ip, nil,
		map[string]string{"email": "ok@test.dev", "password": "correct-password"})
	refreshToken := login["refresh_token"].(string)

	// Exhaust the refresh limit (2) with intentionally-bad tokens — the
	// limiter must count every attempt at this route, not just successful
	// ones.
	for i := 0; i < 2; i++ {
		code, _, _ := e.do(t, "POST", "/api/v1/admin/auth/refresh", ip, nil,
			map[string]string{"refresh_token": "not-a-real-token"})
		if code != http.StatusUnauthorized {
			t.Fatalf("bad-token attempt %d: want 401 (under threshold), got %d", i, code)
		}
	}

	code, _, retryAfter := e.do(t, "POST", "/api/v1/admin/auth/refresh", ip, nil,
		map[string]string{"refresh_token": refreshToken})
	if code != http.StatusTooManyRequests {
		t.Fatalf("3rd refresh attempt (valid token): want 429, got %d", code)
	}
	if retryAfter == "" {
		t.Fatal("429 response missing Retry-After header")
	}

	time.Sleep(400 * time.Millisecond)

	code, rotated, _ := e.do(t, "POST", "/api/v1/admin/auth/refresh", ip, nil,
		map[string]string{"refresh_token": refreshToken})
	if code != http.StatusOK || rotated["refresh_token"] == "" {
		t.Fatalf("after window elapsed, the still-valid token must rotate successfully: code=%d body=%v", code, rotated)
	}
}
