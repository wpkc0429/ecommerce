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
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/config"
	"ksdevworks/ecommerce/api/internal/ent/role"
	"ksdevworks/ecommerce/api/internal/ent/user"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/provision"
	"ksdevworks/ecommerce/api/internal/rbac"
	"ksdevworks/ecommerce/api/internal/seed"
	"ksdevworks/ecommerce/api/internal/testutil"
)

// TestOpenAPIAccountResponsesMatchImplementation checks real responses of the
// account provisioning endpoints against the contract's schemas (check 3,
// change platform-account-provisioning).
func TestOpenAPIAccountResponsesMatchImplementation(t *testing.T) {
	testutil.RequireIntegration(t)
	client := testutil.OpenDB(t)
	rdb := testutil.OpenRedis(t)
	ctx := context.Background()
	if err := seed.Run(ctx, client, seed.Options{AdminEmail: "conf-root@t.dev", AdminPassword: "conf-root-pw-for-tests"}); err != nil {
		t.Fatal(err)
	}
	adminID := client.User.Query().Where(user.EmailEQ("conf-root@t.dev")).OnlyX(ctx).ID
	shopID := client.Shop.Create().SetName("Conf Shop").SaveX(ctx).ID
	ownerRole := client.Role.Query().Where(role.NameEQ("merchant_owner")).OnlyX(ctx).ID

	issuer, err := auth.NewTokenIssuer("conf-admin-secret", "conf-member-secret", "test", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := rbac.NewEngine(client, rdb, log)
	authz := &AuthzMW{Engine: engine}
	dispatcher := events.NewDispatcher(log)
	svc := &account.Service{
		Client: client, Engine: engine, Refresh: auth.NewRefreshService(client, time.Hour),
		Delivery: account.ManualDelivery{}, PublicURL: "http://admin.test",
		SetupTTL: 72 * time.Hour, ResetTTL: 24 * time.Hour,
	}
	router := New(Deps{
		Cfg: &config.Config{}, Log: log,
		AdminMW:   NewAdminMiddleware(issuer),
		AdminAuth: &AdminAuthHandler{Client: client, Issuer: issuer, Refresh: svc.Refresh, Engine: engine, Log: log},
		Accounts:  &AccountsHandler{Service: svc, Authz: authz, Log: log},
		Provisioning: &ProvisioningHandler{
			Service: &provision.Service{
				Client: client, CMS: &cms.Service{Client: client, Dispatcher: dispatcher}, Accounts: svc, Dispatcher: dispatcher,
			},
			Engine: engine, Authz: authz, Log: log,
		},
	})
	tok, err := issuer.IssueAdmin(adminID, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any) (int, []byte) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	v := newContractValidator(t)

	code, body := call(http.MethodPost, "/api/v1/admin/accounts", map[string]any{
		"email": "conf-owner@t.dev", "shop_id": shopID, "role_id": ownerRole,
	})
	requireStatus(t, code, http.StatusCreated, body)
	v.assertResponse(t, "/api/v1/admin/accounts", http.MethodPost, http.StatusCreated, body)
	var created struct {
		UserID   int    `json:"user_id"`
		SetupURL string `json:"setup_url"`
	}
	decodeInto(t, body, &created)
	uid := strconv.Itoa(created.UserID)
	setupToken := strings.TrimPrefix(created.SetupURL, "http://admin.test/#/set-password/")

	code, body = call(http.MethodGet, "/api/v1/admin/accounts?q=conf", nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/admin/accounts", http.MethodGet, http.StatusOK, body)

	code, body = call(http.MethodGet, "/api/v1/admin/accounts/"+uid, nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/admin/accounts/{userID}", http.MethodGet, http.StatusOK, body)

	code, body = call(http.MethodPost, "/api/v1/admin/accounts/"+uid+"/password-reset", nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/admin/accounts/{userID}/password-reset", http.MethodPost, http.StatusOK, body)

	code, body = call(http.MethodPost, "/api/v1/admin/accounts/"+uid+"/memberships", map[string]any{"shop_id": shopID, "role_id": ownerRole})
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/admin/accounts/{userID}/memberships", http.MethodPost, http.StatusOK, body)

	code, body = call(http.MethodPost, "/api/v1/admin/auth/password-setup/inspect", map[string]any{"token": setupToken})
	// The reset issued above does not touch the setup token (different kind).
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/admin/auth/password-setup/inspect", http.MethodPost, http.StatusOK, body)

	code, body = call(http.MethodPost, "/api/v1/admin/shop-provisioning", map[string]any{
		"name": "Conf Provisioned", "owner_email": "conf-owner2@t.dev",
		"storefront_origins": []string{"https://conf-prov.test"},
	})
	requireStatus(t, code, http.StatusCreated, body)
	v.assertResponse(t, "/api/v1/admin/shop-provisioning", http.MethodPost, http.StatusCreated, body)
	var prov struct {
		Shop struct {
			ID int `json:"id"`
		} `json:"shop"`
	}
	decodeInto(t, body, &prov)

	code, body = call(http.MethodPost, "/api/v1/admin/shop-provisioning", map[string]any{
		"name": "Conf Existing Owner", "owner_email": "conf-owner@t.dev",
	})
	requireStatus(t, code, http.StatusCreated, body)
	v.assertResponse(t, "/api/v1/admin/shop-provisioning", http.MethodPost, http.StatusCreated, body)

	code, body = call(http.MethodGet, "/api/v1/admin/shops/"+strconv.Itoa(prov.Shop.ID)+"/setup-status", nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/admin/shops/{shopID}/setup-status", http.MethodGet, http.StatusOK, body)

	code, body = call(http.MethodGet, "/api/v1/admin/me", nil)
	requireStatus(t, code, http.StatusOK, body)
	v.assertResponse(t, "/api/v1/admin/me", http.MethodGet, http.StatusOK, body)
	if !strings.Contains(string(body), `"platform_permissions":["`) {
		t.Fatalf("super admin /me should list platform permissions: %s", truncate(body))
	}
}
