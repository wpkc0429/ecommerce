package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/userrefreshtoken"
	"ksdevworks/ecommerce/api/internal/previeworigin"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// Read-only preview sessions against the REAL wired application (change
// preview-readonly-session): real router, real middleware stack, real
// database. The route list comes from the router, so a route added later is
// covered automatically.

const (
	pvOrigin    = "https://p-3f9a.preview.example.test"
	pvOtherOrig = "https://other.preview.example.test"
	pvNormal    = "https://admin.merchant.example.test"
	pvPassword  = "correct horse battery staple 1"
)

var pvSeq atomic.Int64

type pvEnv struct {
	t      *testing.T
	a      *App
	client *ent.Client
	cfg    *config.Config
	logbuf *bytes.Buffer
	issuer *auth.TokenIssuer
	user   *ent.User
	email  string
	shopID int
}

func newPVEnv(t *testing.T) *pvEnv {
	t.Helper()
	testutil.RequireIntegration(t)
	client := testutil.OpenDB(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	cfg.DatabaseURL = os.Getenv("TEST_DATABASE_URL")
	set, err := previeworigin.Parse([]string{"https://*.preview.example.test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AdminPreview = config.AdminPreviewConfig{Set: set, Origins: []string{"https://*.preview.example.test"}, SessionTTL: 20 * time.Minute}
	// A private Redis database, flushed around each test: the RBAC snapshot cache
	// is keyed by user id and ids restart after the test DB is truncated, so a
	// shared database would leak one test's memberships into the next one's.
	cfg.RedisDB = pvRedisDB
	flushPVRedis(t, cfg)
	t.Cleanup(func() { flushPVRedis(t, cfg) })
	cfg.RateLimit.LoginBroadLimit, cfg.RateLimit.LoginTightLimit = 100000, 100000
	cfg.RateLimit.RefreshLimit = 100000
	cfg.RateLimit.LoginAccountFailures = 100000
	buf := &bytes.Buffer{}
	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(&syncWriter{buf: buf}, nil)))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(a.Close)

	ctx := context.Background()
	hash, err := auth.HashPassword(pvPassword)
	if err != nil {
		t.Fatal(err)
	}
	email := fmt.Sprintf("pv-%d-%d@example.test", time.Now().UnixNano(), pvSeq.Add(1))
	u := client.User.Create().SetEmail(email).SetPasswordHash(hash).SetStatus(1).SaveX(ctx)
	shop := client.Shop.Create().SetName("Preview shop").SaveX(ctx)
	client.ShopUser.Create().SetShopID(shop.ID).SetUserID(u.ID).SaveX(ctx)
	issuer, err := auth.NewTokenIssuer(cfg.AdminJWTSecret, cfg.MemberJWTSecret, cfg.JWTIssuer, cfg.AccessTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	return &pvEnv{t: t, a: a, client: client, cfg: cfg, logbuf: buf, issuer: issuer, user: u, email: email, shopID: shop.ID}
}

const pvRedisDB = 14

func flushPVRedis(t *testing.T, cfg *config.Config) {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: pvRedisDB})
	defer func() { _ = rdb.Close() }()
	if err := rdb.FlushDB(context.Background()).Err(); err != nil {
		t.Logf("flush redis db %d: %v", pvRedisDB, err)
	}
}

type syncWriter struct{ buf *bytes.Buffer }

var pvLogMu = make(chan struct{}, 1)

func (w *syncWriter) Write(p []byte) (int, error) {
	pvLogMu <- struct{}{}
	defer func() { <-pvLogMu }()
	return w.buf.Write(p)
}

func (e *pvEnv) logs() string {
	pvLogMu <- struct{}{}
	defer func() { <-pvLogMu }()
	return e.logbuf.String()
}

type pvReq struct {
	method, path, origin, token string
	body                        any
	headers                     map[string]string
	cookies                     []*http.Cookie
}

func (e *pvEnv) do(r pvReq) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd *bytes.Reader
	if r.body != nil {
		b, _ := json.Marshal(r.body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader([]byte("{}"))
	}
	req := httptest.NewRequest(r.method, r.path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", 1+pvSeq.Add(1)%250)
	if r.origin != "" {
		req.Header.Set("Origin", r.origin)
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	for _, c := range r.cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.a.Router().ServeHTTP(rec, req)
	return rec
}

type pvTokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Scope   string `json:"scope"`
	Expires int    `json:"expires_in"`
	Type    string `json:"token_type"`
}

func (e *pvEnv) login(origin string, extra map[string]any) (*httptest.ResponseRecorder, pvTokens) {
	body := map[string]any{"email": e.email, "password": pvPassword}
	for k, v := range extra {
		body[k] = v
	}
	rec := e.do(pvReq{method: "POST", path: "/api/v1/admin/auth/login", origin: origin, body: body})
	var tk pvTokens
	_ = json.Unmarshal(rec.Body.Bytes(), &tk)
	return rec, tk
}

func isDenied(rec *httptest.ResponseRecorder) bool {
	return rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), `"preview_read_only"`)
}

func TestPreviewLoginIssuesReadOnlySession(t *testing.T) {
	e := newPVEnv(t)
	rec, tk := e.login(pvOrigin, nil)
	if rec.Code != 200 {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	if tk.Scope != "preview_ro" || tk.Access == "" || tk.Refresh == "" || tk.Type != "Bearer" {
		t.Fatalf("response: %+v", tk)
	}
	if !auth.IsPreviewRefresh(tk.Refresh) || auth.PreviewRefreshTag(tk.Refresh) != auth.OriginTag(pvOrigin) {
		t.Fatal("refresh token must carry the preview prefix bound to the origin")
	}
	claims, uid, err := e.issuer.VerifyAdminPreview(tk.Access)
	if err != nil || uid != e.user.ID || claims.Scope != "preview_ro" || claims.Origin != pvOrigin {
		t.Fatalf("claims %+v %d %v", claims, uid, err)
	}
	// short, bounded lifetime: never past the configured session TTL
	if tk.Expires <= 0 || tk.Expires > int((20*time.Minute).Seconds()) || tk.Expires > 900 {
		t.Fatalf("expires_in %d", tk.Expires)
	}
	row := e.client.UserRefreshToken.Query().Where(userrefreshtoken.UserIDEQ(e.user.ID)).OnlyX(context.Background())
	if d := time.Until(row.ExpiresAt); d <= 0 || d > 20*time.Minute+5*time.Second {
		t.Fatalf("refresh lifetime %v", d)
	}

	// A normal login is untouched: no scope key at all.
	rec, ntk := e.login(pvNormal, nil)
	if rec.Code != 200 || ntk.Scope != "" || strings.Contains(rec.Body.String(), `"scope"`) {
		t.Fatalf("normal login: %d %s", rec.Code, rec.Body)
	}
	if _, _, err := e.issuer.VerifyAdmin(ntk.Access); err != nil {
		t.Fatal("normal login must yield an ordinary admin token")
	}
	if auth.IsPreviewRefresh(ntk.Refresh) {
		t.Fatal("normal refresh token must not carry the preview prefix")
	}
	// ... and so is a login with no Origin at all (server-to-server, curl).
	if rec, ntk2 := e.login("", nil); rec.Code != 200 || ntk2.Scope != "" {
		t.Fatalf("no-origin login: %d %s", rec.Code, rec.Body)
	}
	// Lookalikes of the preview pattern are NOT preview origins: they get normal sessions
	// only because they are not preview origins, and never a read-only one by accident.
	for _, o := range []string{"https://evil-preview.example.test", "https://a.b.preview.example.test", "https://preview.example.test"} {
		if rec, tk := e.login(o, nil); rec.Code != 200 || tk.Scope != "" {
			t.Errorf("%s: %d scope %q", o, rec.Code, tk.Scope)
		}
	}
}

func TestPreviewSessionReadsWorkWritesAreRefused(t *testing.T) {
	e := newPVEnv(t)
	_, tk := e.login(pvOrigin, nil)
	shop := fmt.Sprintf("/api/v1/admin/shops/%d", e.shopID)

	for _, p := range []string{"/api/v1/admin/me", shop + "/me"} {
		if rec := e.do(pvReq{method: "GET", path: p, origin: pvOrigin, token: tk.Access}); rec.Code != 200 {
			t.Errorf("GET %s with a preview session: %d %s", p, rec.Code, rec.Body)
		}
	}
	// Same-origin GET through the preview host's proxy: no Origin header, Host identifies it.
	req := httptest.NewRequest("GET", "/api/v1/admin/me", http.NoBody)
	req.Host = "p-3f9a.preview.example.test"
	req.Header.Set("Authorization", "Bearer "+tk.Access)
	rec := httptest.NewRecorder()
	e.a.Router().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("same-origin GET (Host only): %d", rec.Code)
	}
	for _, w := range []pvReq{
		{method: "PATCH", path: "/api/v1/admin/me", body: map[string]any{"display_name": "x"}},
		{method: "POST", path: shop + "/products", body: map[string]any{}},
		{method: "PUT", path: shop + "/settings", body: map[string]any{}},
		{method: "DELETE", path: shop + "/products/1"},
	} {
		w.origin, w.token = pvOrigin, tk.Access
		if rec := e.do(w); !isDenied(rec) {
			t.Errorf("%s %s: %d %s", w.method, w.path, rec.Code, rec.Body)
		}
	}
	if got := e.client.User.GetX(context.Background(), e.user.ID).DisplayName; got != nil {
		t.Fatal("a refused write must not have changed anything")
	}
	// Denylisted GET: sensitive output.
	if rec := e.do(pvReq{method: "GET", path: shop + "/pages/1/preview-token", origin: pvOrigin, token: tk.Access}); !isDenied(rec) {
		t.Errorf("preview-token GET: %d", rec.Code)
	}
}

func TestPreviewTokenConfusionBothDirections(t *testing.T) {
	e := newPVEnv(t)
	_, ro := e.login(pvOrigin, nil)
	_, normal := e.login(pvNormal, nil)
	me := "/api/v1/admin/me"
	shop := fmt.Sprintf("/api/v1/admin/shops/%d/me", e.shopID)

	// normal token from a preview origin: refused (reads and writes)
	for _, p := range []string{me, shop} {
		if rec := e.do(pvReq{method: "GET", path: p, origin: pvOrigin, token: normal.Access}); !isDenied(rec) {
			t.Errorf("normal token GET %s from preview origin: %d", p, rec.Code)
		}
	}
	if rec := e.do(pvReq{method: "PATCH", path: me, origin: pvOrigin, token: normal.Access, body: map[string]any{"display_name": "x"}}); !isDenied(rec) {
		t.Errorf("normal token PATCH from preview origin: %d", rec.Code)
	}
	// ... but the normal token still works from its normal origin and without one
	for _, o := range []string{pvNormal, ""} {
		if rec := e.do(pvReq{method: "GET", path: me, origin: o, token: normal.Access}); rec.Code != 200 {
			t.Errorf("normal token from %q: %d", o, rec.Code)
		}
	}
	// ro token from a normal admin origin / no origin / another preview origin: 401
	for _, o := range []string{pvNormal, "", pvOtherOrig, "https://evil-preview.example.test"} {
		for _, m := range []string{"GET", "PATCH"} {
			rec := e.do(pvReq{method: m, path: me, origin: o, token: ro.Access})
			// another preview origin's writes are refused by the guard already; everything else is a 401
			if o == pvOtherOrig && m == "PATCH" {
				if !isDenied(rec) {
					t.Errorf("ro token PATCH from %q: %d", o, rec.Code)
				}
				continue
			}
			if rec.Code != 401 {
				t.Errorf("ro token %s %s from %q: %d", m, me, o, rec.Code)
			}
		}
	}
	// a token bound to an origin the operator no longer lists is dead
	stale, _ := e.issuer.IssueAdminPreview(e.user.ID, nil, "https://gone.example.test", time.Minute)
	if rec := e.do(pvReq{method: "GET", path: me, origin: "https://gone.example.test", token: stale}); rec.Code != 401 {
		t.Errorf("token for an unlisted origin: %d", rec.Code)
	}
	// expired access token
	old, _ := e.issuer.IssueAdminPreview(e.user.ID, nil, pvOrigin, -time.Minute)
	if rec := e.do(pvReq{method: "GET", path: me, origin: pvOrigin, token: old}); rec.Code != 401 {
		t.Errorf("expired token: %d", rec.Code)
	}
	// ro tokens mean nothing outside the admin family
	if rec := e.do(pvReq{method: "GET", path: "/api/v1/shop/me", origin: pvOrigin, token: ro.Access}); rec.Code == 200 {
		t.Errorf("ro token on a member route: %d", rec.Code)
	}
}

func TestPreviewRefreshKeepsScopeAndNeverUpgrades(t *testing.T) {
	e := newPVEnv(t)
	rec, ro := e.login(pvOrigin, nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	first := e.client.UserRefreshToken.Query().Where(userrefreshtoken.UserIDEQ(e.user.ID)).OnlyX(context.Background())

	// allowlisted: refresh of the same scope from the same origin
	rec = e.do(pvReq{method: "POST", path: "/api/v1/admin/auth/refresh", origin: pvOrigin, body: map[string]any{"refresh_token": ro.Refresh}})
	if rec.Code != 200 {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body)
	}
	var next pvTokens
	_ = json.Unmarshal(rec.Body.Bytes(), &next)
	if next.Scope != "preview_ro" || !auth.IsPreviewRefresh(next.Refresh) || next.Refresh == ro.Refresh {
		t.Fatalf("refresh result %+v", next)
	}
	if _, _, err := e.issuer.VerifyAdminPreview(next.Access); err != nil {
		t.Fatal("refresh must yield a preview token again")
	}
	if rec := e.do(pvReq{method: "GET", path: "/api/v1/admin/me", origin: pvOrigin, token: next.Access}); rec.Code != 200 {
		t.Fatalf("refreshed token: %d", rec.Code)
	}
	// the absolute session end is NOT extended by refreshing
	rows := e.client.UserRefreshToken.Query().Where(userrefreshtoken.UserIDEQ(e.user.ID)).AllX(context.Background())
	if len(rows) != 2 {
		t.Fatalf("rows %d", len(rows))
	}
	for _, r := range rows {
		if !r.ExpiresAt.Equal(first.ExpiresAt) {
			t.Fatalf("session end moved: %v vs %v", r.ExpiresAt, first.ExpiresAt)
		}
	}
	// rotation: the old token is dead (and replay kills the chain)
	if rec := e.do(pvReq{method: "POST", path: "/api/v1/admin/auth/refresh", origin: pvOrigin, body: map[string]any{"refresh_token": ro.Refresh}}); rec.Code != 401 {
		t.Fatalf("replay: %d", rec.Code)
	}

	// no upgrade: a preview refresh token from a normal origin / no origin / another preview origin
	_, fresh := e.login(pvOrigin, nil)
	for _, o := range []string{pvNormal, "", pvOtherOrig} {
		if rec := e.do(pvReq{method: "POST", path: "/api/v1/admin/auth/refresh", origin: o, body: map[string]any{"refresh_token": fresh.Refresh}}); rec.Code != 401 {
			t.Errorf("preview refresh token from %q: %d %s", o, rec.Code, rec.Body)
		}
	}
	// ... still valid from the right origin (the refusals above revoked nothing)
	if rec := e.do(pvReq{method: "POST", path: "/api/v1/admin/auth/refresh", origin: pvOrigin, body: map[string]any{"refresh_token": fresh.Refresh}}); rec.Code != 200 {
		t.Fatalf("preview refresh after refusals: %d", rec.Code)
	}
	// a normal refresh token cannot be used from a preview origin to mint any session
	_, normal := e.login(pvNormal, nil)
	if rec := e.do(pvReq{method: "POST", path: "/api/v1/admin/auth/refresh", origin: pvOrigin, body: map[string]any{"refresh_token": normal.Refresh}}); rec.Code != 401 {
		t.Fatalf("normal refresh token from a preview origin: %d", rec.Code)
	}
	if rec := e.do(pvReq{method: "POST", path: "/api/v1/admin/auth/refresh", origin: pvNormal, body: map[string]any{"refresh_token": normal.Refresh}}); rec.Code != 200 {
		t.Fatalf("normal refresh must still work: %d", rec.Code)
	}
	// a forged "pv1." token with another origin's tag fails (hash lookup)
	forged := "pv1." + auth.OriginTag(pvOrigin) + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if rec := e.do(pvReq{method: "POST", path: "/api/v1/admin/auth/refresh", origin: pvOrigin, body: map[string]any{"refresh_token": forged}}); rec.Code != 401 {
		t.Fatalf("forged: %d", rec.Code)
	}
	// ending the session: logout works from the preview origin and kills the token
	_, s := e.login(pvOrigin, nil)
	if rec := e.do(pvReq{method: "POST", path: "/api/v1/admin/auth/logout", origin: pvOrigin, body: map[string]any{"refresh_token": s.Refresh}}); rec.Code != 204 {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := e.do(pvReq{method: "POST", path: "/api/v1/admin/auth/refresh", origin: pvOrigin, body: map[string]any{"refresh_token": s.Refresh}}); rec.Code != 401 {
		t.Fatalf("refresh after logout: %d", rec.Code)
	}
}

func TestPreviewSessionExpiry(t *testing.T) {
	e := newPVEnv(t)
	_, ro := e.login(pvOrigin, nil)
	e.client.UserRefreshToken.Update().Where(userrefreshtoken.UserIDEQ(e.user.ID)).
		SetExpiresAt(time.Now().Add(-time.Second)).ExecX(context.Background())
	if rec := e.do(pvReq{method: "POST", path: "/api/v1/admin/auth/refresh", origin: pvOrigin, body: map[string]any{"refresh_token": ro.Refresh}}); rec.Code != 401 {
		t.Fatalf("expired session refresh: %d", rec.Code)
	}
	// near the end the access token is shortened to the remaining time
	_, ro2 := e.login(pvOrigin, nil)
	e.client.UserRefreshToken.Update().Where(userrefreshtoken.UserIDEQ(e.user.ID), userrefreshtoken.RevokedAtIsNil()).
		SetExpiresAt(time.Now().Add(30 * time.Second)).ExecX(context.Background())
	rec := e.do(pvReq{method: "POST", path: "/api/v1/admin/auth/refresh", origin: pvOrigin, body: map[string]any{"refresh_token": ro2.Refresh}})
	var tk pvTokens
	_ = json.Unmarshal(rec.Body.Bytes(), &tk)
	if rec.Code != 200 || tk.Expires > 30 || tk.Expires <= 0 {
		t.Fatalf("end-of-session refresh: %d %+v", rec.Code, tk)
	}
}

func TestPreviewCookieModeAttributesAndIsolation(t *testing.T) {
	e := newPVEnv(t)
	rec, tk := e.login(pvOrigin, map[string]any{"refresh_transport": "cookie"})
	if rec.Code != 200 || tk.Refresh != "" || tk.Scope != "preview_ro" {
		t.Fatalf("cookie login: %d %s", rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies %v", cookies)
	}
	c := cookies[0]
	if c.Name != "__Secure-admin_preview_refresh" || c.Name == "__Secure-admin_refresh" {
		t.Fatalf("cookie name %q must differ from the normal refresh cookie", c.Name)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/api/v1/admin/auth" || c.Domain != "" {
		t.Fatalf("cookie attributes: %+v (host-only, HttpOnly, Secure, SameSite=Strict, narrow Path)", c)
	}
	if c.MaxAge <= 0 || c.MaxAge > int((20*time.Minute).Seconds()) {
		t.Fatalf("cookie Max-Age %d must be the (short) session lifetime", c.MaxAge)
	}
	hdr := strings.Join(rec.Result().Header.Values("Set-Cookie"), ";")
	if strings.Contains(strings.ToLower(hdr), "domain=") {
		t.Fatalf("a Domain attribute would widen the cookie scope: %s", hdr)
	}
	cookieHdr := map[string]string{"X-Admin-Refresh": "1"}
	ref := func(origin string, ck *http.Cookie, hdrs map[string]string) *httptest.ResponseRecorder {
		return e.do(pvReq{method: "POST", path: "/api/v1/admin/auth/refresh", origin: origin, headers: hdrs, cookies: []*http.Cookie{ck}})
	}
	// The preview cookie is only honored for preview origins ...
	for _, o := range []string{pvNormal, "", pvOtherOrig} {
		if r := ref(o, c, cookieHdr); r.Code != 401 {
			t.Errorf("preview cookie from %q: %d", o, r.Code)
		}
	}
	// ... and needs the CSRF header even there.
	if r := ref(pvOrigin, c, nil); r.Code != 401 {
		t.Errorf("preview cookie without X-Admin-Refresh: %d", r.Code)
	}
	r := ref(pvOrigin, c, cookieHdr)
	if r.Code != 200 {
		t.Fatalf("preview cookie refresh: %d %s", r.Code, r.Body)
	}
	if got := r.Result().Cookies(); len(got) != 1 || got[0].Name != c.Name || got[0].Value == c.Value {
		t.Fatalf("rotated cookie %+v", got)
	}
	if strings.Contains(r.Body.String(), "refresh_token") {
		t.Fatal("cookie mode must not put the refresh token in the body")
	}
	// A normal-session cookie is not honored from a preview origin, and cannot be smuggled into the preview cookie.
	nrec, _ := e.login(pvNormal, map[string]any{"refresh_transport": "cookie"})
	nc := nrec.Result().Cookies()[0]
	if nc.Name != "__Secure-admin_refresh" {
		t.Fatalf("normal cookie %q", nc.Name)
	}
	if r := ref(pvOrigin, nc, cookieHdr); r.Code != 401 {
		t.Errorf("normal cookie from a preview origin: %d", r.Code)
	}
	smuggled := &http.Cookie{Name: c.Name, Value: nc.Value}
	if r := ref(pvOrigin, smuggled, cookieHdr); r.Code != 401 {
		t.Errorf("normal token in the preview cookie: %d", r.Code)
	}
	// logout clears the preview cookie (and only that one)
	lr := e.do(pvReq{method: "POST", path: "/api/v1/admin/auth/logout", origin: pvOrigin, headers: cookieHdr, cookies: []*http.Cookie{r.Result().Cookies()[0]}})
	if lr.Code != 204 {
		t.Fatalf("logout: %d", lr.Code)
	}
	if got := lr.Result().Cookies(); len(got) != 1 || got[0].Name != c.Name || got[0].MaxAge != -1 {
		t.Fatalf("logout cookie %+v", got)
	}
}

// Every route of the real router, tried from a preview origin with a valid
// preview session. Nothing is hand-listed.
func TestPreviewSessionEveryRouteOfTheRealRouter(t *testing.T) {
	e := newPVEnv(t)
	_, ro := e.login(pvOrigin, nil)
	_, normal := e.login(pvNormal, nil)
	allowed := map[string]bool{
		"POST /api/v1/admin/auth/login": true, "POST /api/v1/admin/auth/refresh": true, "POST /api/v1/admin/auth/logout": true,
	}
	var nonSafe, reads, deniedGets int
	walk := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/")
		path := chiParam.ReplaceAllString(route, "1")
		key := method + " " + route
		safe := method == "GET" || method == "HEAD" || method == "OPTIONS"
		if !safe {
			nonSafe++
			if !allowed[key] {
				if rec := e.do(pvReq{method: method, path: path, origin: pvOrigin, token: ro.Access}); !isDenied(rec) {
					t.Errorf("%s with a preview session: %d %s", key, rec.Code, rec.Body)
				}
				// no origin and a normal origin: a preview token is never accepted (never 2xx)
				for _, o := range []string{"", pvNormal} {
					if !strings.HasPrefix(route, "/api/v1/admin/") {
						break
					}
					if rec := e.do(pvReq{method: method, path: path, origin: o, token: ro.Access}); rec.Code < 400 {
						t.Errorf("%s with a preview token from %q was accepted: %d", key, o, rec.Code)
					}
				}
			}
			return nil
		}
		if method == "OPTIONS" || !strings.HasPrefix(route, "/api/v1/admin") {
			return nil
		}
		reads++
		denyListed := false
		for _, d := range []string{"preview-token", "/screenshots", "/ecpay-credentials", "/content/publish-hook", "/design-service/", "/accounts", "/shop-applications"} {
			if strings.Contains(route, d) {
				denyListed = true
			}
		}
		rec := e.do(pvReq{method: method, path: path, origin: pvOrigin, token: ro.Access})
		if denyListed {
			deniedGets++
			if !isDenied(rec) {
				t.Errorf("%s must be denied for a preview session: %d", key, rec.Code)
			}
		} else if isDenied(rec) {
			t.Errorf("%s must stay readable in a preview session: %d %s", key, rec.Code, rec.Body)
		}
		// the ordinary admin token is never accepted from a preview origin on an authenticated route
		if rec := e.do(pvReq{method: method, path: path, origin: pvOrigin, token: normal.Access}); rec.Code == 200 {
			t.Errorf("%s: an ordinary admin token answered 200 from a preview origin", key)
		}
		return nil
	}
	if err := chi.Walk(e.a.Router().(chi.Routes), walk); err != nil {
		t.Fatal(err)
	}
	if nonSafe < 100 || reads < 50 || deniedGets < 8 {
		t.Fatalf("route walk looks broken: nonSafe=%d reads=%d deniedGets=%d", nonSafe, reads, deniedGets)
	}
	t.Logf("verified %d non-safe routes, %d admin reads (%d denylisted)", nonSafe, reads, deniedGets)
}

func TestPreviewSessionAuditLogHasNoPII(t *testing.T) {
	e := newPVEnv(t)
	_, ro := e.login(pvOrigin, nil)
	e.do(pvReq{method: "POST", path: "/api/v1/admin/auth/refresh", origin: pvOrigin, body: map[string]any{"refresh_token": ro.Refresh}})
	e.do(pvReq{method: "DELETE", path: "/api/v1/admin/shops/1/products/1", origin: pvOrigin, token: ro.Access})
	logs := e.logs()
	for _, want := range []string{"admin_preview_session", "event=login", "event=refresh", "origin_tag=" + auth.OriginTag(pvOrigin),
		fmt.Sprintf("user_id=%d", e.user.ID), "admin_preview_denied"} {
		if !strings.Contains(logs, want) {
			t.Errorf("audit log lacks %q", want)
		}
	}
	for _, secret := range []string{e.email, pvPassword, ro.Access, ro.Refresh, "p-3f9a.preview.example.test"} {
		if strings.Contains(logs, secret) {
			t.Errorf("audit log must not contain %q", secret)
		}
	}
}

func TestPreviewFeatureOffMeansNoReadOnlySessions(t *testing.T) {
	testutil.RequireIntegration(t)
	e := newPVEnv(t)
	// A second app on the same DB with the feature configured OFF.
	cfg := *e.cfg
	cfg.AdminPreview = config.AdminPreviewConfig{SessionTTL: 20 * time.Minute}
	off, err := New(context.Background(), &cfg, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(off.Close)
	post := func(a *App, origin string) (*httptest.ResponseRecorder, pvTokens) {
		b, _ := json.Marshal(map[string]any{"email": e.email, "password": pvPassword})
		req := httptest.NewRequest("POST", "/api/v1/admin/auth/login", bytes.NewReader(b))
		req.Header.Set("Origin", origin)
		req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", 1+pvSeq.Add(1)%250)
		rec := httptest.NewRecorder()
		a.Router().ServeHTTP(rec, req)
		var tk pvTokens
		_ = json.Unmarshal(rec.Body.Bytes(), &tk)
		return rec, tk
	}
	rec, tk := post(off, pvOrigin)
	if rec.Code != 200 || tk.Scope != "" {
		t.Fatalf("feature off: login from a would-be preview origin yields a normal session: %d %+v", rec.Code, tk)
	}
	// and a preview token minted elsewhere is simply invalid here
	_, ro := e.login(pvOrigin, nil)
	req := httptest.NewRequest("GET", "/api/v1/admin/me", http.NoBody)
	req.Header.Set("Origin", pvOrigin)
	req.Header.Set("Authorization", "Bearer "+ro.Access)
	r2 := httptest.NewRecorder()
	off.Router().ServeHTTP(r2, req)
	if r2.Code != 401 {
		t.Fatalf("feature off: preview token accepted: %d", r2.Code)
	}
}
