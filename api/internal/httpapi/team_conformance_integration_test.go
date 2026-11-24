package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/account"
	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent/role"
	"ksdevworks/ecommerce/api/internal/ent/user"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/seed"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// TestOpenAPITeamResponsesMatchImplementation checks real responses of the
// invitation endpoints against the contract's schemas (check 3, change
// merchant-staff-role).
func TestOpenAPITeamResponsesMatchImplementation(t *testing.T) {
	testutil.RequireIntegration(t)
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	if err := seed.Run(ctx, client, seed.Options{AdminEmail: "conf-root2@t.dev", AdminPassword: "conf-root-pw-for-tests"}); err != nil {
		t.Fatal(err)
	}
	adminID := client.User.Query().Where(user.EmailEQ("conf-root2@t.dev")).OnlyX(ctx).ID
	shopID := client.Shop.Create().SetName("Conf Team Shop").SaveX(ctx).ID
	staffRole := client.Role.Query().Where(role.NameEQ("staff")).OnlyX(ctx).ID
	editorRole := client.Role.Query().Where(role.NameEQ("editor")).OnlyX(ctx).ID

	issuer, err := auth.NewTokenIssuer("conf-admin-secret", "conf-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := rbac.NewEngine(client, rdb, log)
	authz := &AuthzMW{Engine: engine}
	svc := &account.Service{
		Client: client, Engine: engine, Refresh: auth.NewRefreshService(client, time.Hour),
		Delivery: account.ManualDelivery{}, PublicURL: "http://admin.test",
		SetupTTL: 72 * time.Hour, ResetTTL: 24 * time.Hour, InviteTTL: 7 * 24 * time.Hour,
	}
	router := New(Deps{
		Cfg: &config.Config{}, Log: log,
		AdminMW: NewAdminMiddleware(issuer),
		Roles:   &RolesHandler{Client: client, Engine: engine, Authz: authz, Log: log},
		Team:    &TeamHandler{Service: svc, Engine: engine, Authz: authz, Log: log},
	})
	tok, err := issuer.IssueAdmin(adminID, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(bearer, method, path string, body any) (int, []byte) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	v := newContractValidator(t)
	base := "/api/v1/admin/shops/" + strconv.Itoa(shopID)

	code, body := call(tok, http.MethodPost, base+"/invitations", map[string]any{"email": "conf-clerk@t.dev", "role_id": staffRole})
	requireStatus(t, code, http.StatusCreated, body)
	v.assertResponse(t, "/api/v1/admin/shops/{shopID}/invitations", http.MethodPost, http.StatusCreated, body)
	var created struct {
		ID  int    `json:"invitation_id"`
		URL string `json:"invite_url"`
	}
	decodeInto(t, body, &created)
	inviteToken := strings.TrimPrefix(created.URL, "http://admin.test/#/accept-invite/")

	code, body = call(tok, http.MethodGet, base+"/invitations", nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/admin/shops/{shopID}/invitations", http.MethodGet, http.StatusOK, body)

	code, body = call("", http.MethodPost, "/api/v1/admin/auth/invitations/inspect", map[string]any{"token": inviteToken})
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/admin/auth/invitations/inspect", http.MethodPost, http.StatusOK, body)

	code, body = call("", http.MethodPost, "/api/v1/admin/auth/invitations/accept", map[string]any{"token": inviteToken, "password": "conf-clerk-password-1"})
	requireStatus(t, code, http.StatusNoContent, body)

	// A second invitation, revoked.
	code, body = call(tok, http.MethodPost, base+"/invitations", map[string]any{"email": "conf-other@t.dev", "role_id": editorRole})
	requireStatus(t, code, http.StatusCreated, body)
	decodeInto(t, body, &created)
	code, body = call(tok, http.MethodDelete, base+"/invitations/"+strconv.Itoa(created.ID), nil)
	requireStatus(t, code, http.StatusNoContent, body)

	// The clerk accepted: disable / enable their membership.
	clerkID := client.User.Query().Where(user.EmailEQ("conf-clerk@t.dev")).OnlyX(ctx).ID
	code, body = call(tok, http.MethodPost, base+"/users/"+strconv.Itoa(clerkID)+"/disable", nil)
	requireStatus(t, code, http.StatusNoContent, body)
	code, body = call(tok, http.MethodPost, base+"/users/"+strconv.Itoa(clerkID)+"/enable", nil)
	requireStatus(t, code, http.StatusNoContent, body)

	// The member list and role list carry the new fields.
	code, body = call(tok, http.MethodGet, base+"/users", nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/admin/shops/{shopID}/users", http.MethodGet, http.StatusOK, body)
	code, body = call(tok, http.MethodGet, base+"/roles", nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/admin/shops/{shopID}/roles", http.MethodGet, http.StatusOK, body)
	code, body = call(tok, http.MethodGet, "/api/v1/admin/roles", nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/admin/roles", http.MethodGet, http.StatusOK, body)
}
