package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// expectedUnguardedAdminRoutes is the complete list of authenticated admin
// routes that carry NO RBAC guard (change platform-account-provisioning,
// design D5). Disabling an account takes effect through the RBAC snapshot, so
// a route outside every guard is a route where a disabled account's old access
// token still works. Adding a route here is a decision a human has to make:
// either guard it, or accept (and say why) that a disabled account may call it.
//
//	GET /api/v1/admin/me            reads users.status itself and answers 401
//	GET /api/v1/admin/media/config  upload limits only, nothing tenant-specific
//	POST /api/v1/admin/invitations/accept  the caller is not yet a member of what
//	                                the invitation grants; the token plus the
//	                                e-mail equality check are the authority, and
//	                                the service refuses a disabled account
//	                                (change merchant-staff-role)
//	GET /api/v1/admin/branding      unauthenticated by design: the shared admin SPA
//	                                needs the shop's name, logo and colors on the
//	                                login page, resolved from the registered admin
//	                                host (change admin-api-public-exposure D8)
var expectedUnguardedAdminRoutes = []string{
	"GET /api/v1/admin/branding",
	"GET /api/v1/admin/me",
	"GET /api/v1/admin/media/config",
	"POST /api/v1/admin/invitations/accept",
}

var chiParam = regexp.MustCompile(`\{[^}]+\}`)

// TestAuthenticatedAdminRoutesWithoutRBACGuard probes every /api/v1/admin route
// of the real router with a valid access token of an account that has no role
// and no membership anywhere. A guarded route answers 403; anything else is
// unguarded and must be on the expected list.
func TestAuthenticatedAdminRoutesWithoutRBACGuard(t *testing.T) {
	testutil.RequireIntegration(t)
	client := testutil.OpenDB(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	cfg.DatabaseURL = os.Getenv("TEST_DATABASE_URL")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(context.Background(), cfg, log)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(a.Close)

	ctx := context.Background()
	u := client.User.Create().SetEmail("no-roles@t.dev").SaveX(ctx)
	issuer, err := auth.NewTokenIssuer(cfg.AdminJWTSecret, cfg.MemberJWTSecret, cfg.JWTIssuer, cfg.AccessTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := issuer.IssueAdmin(u.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	var unguarded []string
	walk := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/")
		if !strings.HasPrefix(route, "/api/v1/admin/") || strings.HasPrefix(route, "/api/v1/admin/auth/") {
			return nil
		}
		path := chiParam.ReplaceAllString(route, "1")
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		a.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			unguarded = append(unguarded, method+" "+route)
		}
		return nil
	}
	if err := chi.Walk(a.Router().(chi.Routes), walk); err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(unguarded)
	want := append([]string(nil), expectedUnguardedAdminRoutes...)
	sort.Strings(want)
	if strings.Join(unguarded, "\n") != strings.Join(want, "\n") {
		t.Fatalf("authenticated admin routes without an RBAC guard changed.\n"+
			"A disabled account's already-issued access token still works on every one of them;\n"+
			"guard the new route, or add it to expectedUnguardedAdminRoutes with a reason.\n  want:\n    %s\n  got:\n    %s",
			strings.Join(want, "\n    "), strings.Join(unguarded, "\n    "))
	}
}
