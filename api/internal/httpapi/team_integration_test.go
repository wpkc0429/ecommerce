package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/ent/admincredentialtoken"
	"ksdevworks/ecommerce/api/internal/ent/roleuser"
	"ksdevworks/ecommerce/api/internal/ent/shopuser"
	"ksdevworks/ecommerce/api/internal/ent/user"
)

// Change merchant-staff-role: owner-facing invitations, acceptance and
// membership disable. Test values only; nothing here is a real address or
// credential.

const teamPassword = "team-test-password-1"

// mkMember creates an active account with a password, a membership of the
// shop and the named merchant role there. Returns the user id.
func (e *acctEnv) mkMember(t *testing.T, email string, shopID int, roleName string) int {
	t.Helper()
	ctx := context.Background()
	hash, err := auth.HashPassword(teamPassword)
	if err != nil {
		t.Fatal(err)
	}
	u := e.client.User.Create().SetEmail(email).SetPasswordHash(hash).SetStatus(1).SaveX(ctx)
	e.client.ShopUser.Create().SetShopID(shopID).SetUserID(u.ID).SaveX(ctx)
	e.client.RoleUser.Create().SetUserID(u.ID).SetRoleID(e.roleID(t, roleName)).SetShopID(shopID).SaveX(ctx)
	return u.ID
}

func (e *acctEnv) invitePath(shopID int, suffix string) string {
	return fmt.Sprintf("/api/v1/admin/shops/%d/invitations%s", shopID, suffix)
}

func inviteToken(t *testing.T, u string) string {
	t.Helper()
	prefix := acctPublicURL + "/#/accept-invite/"
	if !strings.HasPrefix(u, prefix) {
		t.Fatalf("invite link %q does not start with %q", u, prefix)
	}
	return strings.TrimPrefix(u, prefix)
}

// invite creates an invitation and returns (status, body, token-or-"").
func (e *acctEnv) invite(t *testing.T, bearer string, shopID int, email string, roleID int) (int, map[string]any, string) {
	t.Helper()
	code, body, _ := e.do(t, "POST", e.invitePath(shopID, ""), bearer, map[string]any{"email": email, "role_id": roleID})
	tok := ""
	if u, ok := body["invite_url"].(string); ok {
		tok = inviteToken(t, u)
	}
	return code, body, tok
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Scenario: 邀請店員 / 不能邀請 owner / editor 無權邀請 / 回應不透露帳號是否存在.
func TestTeamInviteCreateRules(t *testing.T) {
	e := newAcctEnv(t)
	ctx := context.Background()
	shopB := e.client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(ctx).ID
	owner := e.mkMember(t, "owner-a@t.dev", e.shopA, "merchant_owner")
	editor := e.mkMember(t, "editor-a@t.dev", e.shopA, "editor")
	other := e.mkMember(t, "elsewhere@t.dev", shopB, "editor") // has an account, elsewhere
	_ = other
	ownerTok := e.tokenFor(t, owner)

	// Invite a clerk: 201, a link, and NO account yet.
	code, body, tok := e.invite(t, ownerTok, e.shopA, "Clerk@Bouquet.test", e.roleID(t, "staff"))
	if code != http.StatusCreated || tok == "" || body["delivery"] != "manual" || body["expires_at"] == nil || body["invitation_id"] == nil {
		t.Fatalf("invite: %d %+v", code, body)
	}
	if e.client.User.Query().Where(user.EmailEQ("clerk@bouquet.test")).ExistX(ctx) {
		t.Fatal("inviting must not create a users row")
	}
	// 7 days.
	row := e.client.AdminCredentialToken.Query().Where(admincredentialtoken.KindEQ("invite")).OnlyX(ctx)
	if d := time.Until(row.ExpiresAt); d < 7*24*time.Hour-time.Minute || d > 7*24*time.Hour+time.Minute {
		t.Fatalf("invite validity = %v, want 7 days", d)
	}

	// Owner role cannot be invited by a shop; unknown / platform role neither.
	if c, _, _ := e.invite(t, ownerTok, e.shopA, "boss@bouquet.test", e.roleID(t, "merchant_owner")); c != http.StatusUnprocessableEntity {
		t.Fatalf("invite merchant_owner: want 422, got %d", c)
	}
	if c, _, _ := e.invite(t, ownerTok, e.shopA, "boss@bouquet.test", e.roleID(t, "super_admin")); c != http.StatusUnprocessableEntity {
		t.Fatalf("invite platform role: want 422, got %d", c)
	}
	if c, _, _ := e.invite(t, ownerTok, e.shopA, "not-an-email", e.roleID(t, "staff")); c != http.StatusUnprocessableEntity {
		t.Fatalf("invite bad email: want 422, got %d", c)
	}
	// An editor has no user.manage_members.
	if c, _, _ := e.invite(t, e.tokenFor(t, editor), e.shopA, "x@bouquet.test", e.roleID(t, "staff")); c != http.StatusForbidden {
		t.Fatalf("editor invite: want 403, got %d", c)
	}
	// Another shop's owner cannot invite into shop A.
	ownerB := e.mkMember(t, "owner-b@t.dev", shopB, "merchant_owner")
	if c, _, _ := e.invite(t, e.tokenFor(t, ownerB), e.shopA, "x@bouquet.test", e.roleID(t, "staff")); c != http.StatusForbidden {
		t.Fatalf("cross-shop invite: want 403, got %d", c)
	}
	// An active member of this shop: 409.
	if c, _, _ := e.invite(t, ownerTok, e.shopA, "editor-a@t.dev", e.roleID(t, "staff")); c != http.StatusConflict {
		t.Fatalf("invite existing member: want 409, got %d", c)
	}

	// The response must not say whether the e-mail has an account (design D6).
	_, fresh, _ := e.invite(t, ownerTok, e.shopA, "brand-new@bouquet.test", e.roleID(t, "staff"))
	c2, known, _ := e.invite(t, ownerTok, e.shopA, "elsewhere@t.dev", e.roleID(t, "staff"))
	if c2 != http.StatusCreated {
		t.Fatalf("invite known account: %d %+v", c2, known)
	}
	if strings.Join(keysOf(fresh), ",") != strings.Join(keysOf(known), ",") {
		t.Fatalf("response keys differ between a new and a known e-mail: %v vs %v", keysOf(fresh), keysOf(known))
	}
}

// Scenario: 同店同 email 新邀請使舊的失效 / 列表不含 token / 撤回後連結失效.
func TestTeamInviteSupersedeListRevoke(t *testing.T) {
	e := newAcctEnv(t)
	owner := e.mkMember(t, "owner-a@t.dev", e.shopA, "merchant_owner")
	ownerTok := e.tokenFor(t, owner)
	staff := e.roleID(t, "staff")

	_, first, tok1 := e.invite(t, ownerTok, e.shopA, "clerk@bouquet.test", staff)
	_, second, tok2 := e.invite(t, ownerTok, e.shopA, "clerk@bouquet.test", staff)
	if tok1 == tok2 {
		t.Fatal("a new invitation must carry a new token")
	}
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/auth/invitations/inspect", "", map[string]string{"token": tok1}); c != http.StatusNotFound {
		t.Fatalf("superseded invitation: inspect want 404, got %d", c)
	}
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/auth/invitations/inspect", "", map[string]string{"token": tok2}); c != http.StatusOK {
		t.Fatalf("live invitation: inspect want 200, got %d", c)
	}

	// Only the live one is listed, with no token or link.
	code, list, raw := e.do(t, "GET", e.invitePath(e.shopA, ""), ownerTok, nil)
	items, _ := list["invitations"].([]any)
	if code != http.StatusOK || len(items) != 1 {
		t.Fatalf("list: %d %s", code, raw)
	}
	if strings.Contains(raw, tok2) || strings.Contains(raw, "invite_url") || strings.Contains(raw, "token") {
		t.Fatalf("listing leaks a credential: %s", raw)
	}
	_ = first

	// Another shop cannot revoke it (404: not pending in THAT shop).
	shopB := e.client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(context.Background()).ID
	ownerB := e.mkMember(t, "owner-b@t.dev", shopB, "merchant_owner")
	id := int(second["invitation_id"].(float64))
	if c, _, _ := e.do(t, "DELETE", e.invitePath(shopB, fmt.Sprintf("/%d", id)), e.tokenFor(t, ownerB), nil); c != http.StatusNotFound {
		t.Fatalf("cross-shop revoke: want 404, got %d", c)
	}
	if c, _, _ := e.do(t, "DELETE", e.invitePath(e.shopA, fmt.Sprintf("/%d", id)), ownerTok, nil); c != http.StatusNoContent {
		t.Fatalf("revoke: want 204, got %d", c)
	}
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/auth/invitations/inspect", "", map[string]string{"token": tok2}); c != http.StatusNotFound {
		t.Fatalf("revoked invitation: inspect want 404, got %d", c)
	}
	if c, _, _ := e.do(t, "DELETE", e.invitePath(e.shopA, fmt.Sprintf("/%d", id)), ownerTok, nil); c != http.StatusNotFound {
		t.Fatalf("second revoke: want 404, got %d", c)
	}
	_, list2, _ := e.do(t, "GET", e.invitePath(e.shopA, ""), ownerTok, nil)
	if n := len(list2["invitations"].([]any)); n != 0 {
		t.Fatalf("revoked invitation still listed (%d)", n)
	}
}

// Scenario: 新人接受邀請 — and the clerk then sees exactly the staff nodes.
func TestTeamAcceptAnonymousNewPerson(t *testing.T) {
	e := newAcctEnv(t)
	ctx := context.Background()
	owner := e.mkMember(t, "owner-a@t.dev", e.shopA, "merchant_owner")
	_, _, tok := e.invite(t, e.tokenFor(t, owner), e.shopA, "clerk@bouquet.test", e.roleID(t, "staff"))

	code, insp, raw := e.do(t, "POST", "/api/v1/admin/auth/invitations/inspect", "", map[string]string{"token": tok})
	if code != http.StatusOK || insp["shop_name"] != "Shop A" || insp["role_name"] != "staff" ||
		insp["email"] != "clerk@bouquet.test" || insp["requires_login"] != false {
		t.Fatalf("inspect: %d %s", code, raw)
	}

	// A too-short password is refused and does NOT burn the link.
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/auth/invitations/accept", "", map[string]string{"token": tok, "password": "short"}); c != http.StatusUnprocessableEntity {
		t.Fatalf("short password: want 422, got %d", c)
	}
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/auth/invitations/accept", "", map[string]string{"token": tok, "password": teamPassword}); c != http.StatusNoContent {
		t.Fatalf("accept: want 204, got %d", c)
	}
	// Single use.
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/auth/invitations/accept", "", map[string]string{"token": tok, "password": teamPassword}); c != http.StatusNotFound {
		t.Fatalf("reuse: want 404, got %d", c)
	}

	c, login := e.login(t, "clerk@bouquet.test", teamPassword)
	if c != http.StatusOK {
		t.Fatalf("login after accept: %d", c)
	}
	u := e.client.User.Query().Where(user.EmailEQ("clerk@bouquet.test")).OnlyX(ctx)
	access := login["access_token"].(string)

	code, me, raw := e.do(t, "GET", fmt.Sprintf("/api/v1/admin/shops/%d/me", e.shopA), access, nil)
	if code != http.StatusOK {
		t.Fatalf("/me: %d %s", code, raw)
	}
	var got []string
	for _, p := range me["permissions"].([]any) {
		got = append(got, p.(string))
	}
	sort.Strings(got)
	want := []string{"order.view", "payment.view", "shipment.create", "shipment.update", "shipment.view"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("staff effective permissions = %v, want %v", got, want)
	}
	if !e.client.RoleUser.Query().Where(roleuser.UserIDEQ(u.ID), roleuser.ShopIDEQ(e.shopA)).ExistX(ctx) {
		t.Fatal("role assignment missing")
	}
	// The inspect result after use: gone.
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/auth/invitations/inspect", "", map[string]string{"token": tok}); c != http.StatusNotFound {
		t.Fatalf("inspect after accept: want 404, got %d", c)
	}
}

// Scenario: 邀請不能接管未完成設定的帳號. The inviter holds the link (manual
// delivery), so a link that could set the password of a password-less account
// would hand that account to the inviter.
func TestTeamInviteCannotTakeOverUnfinishedAccount(t *testing.T) {
	e := newAcctEnv(t)
	ctx := context.Background()
	owner := e.mkMember(t, "owner-a@t.dev", e.shopA, "merchant_owner")

	// The platform provisions x@t.test (no password yet) elsewhere.
	shopB := e.client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(ctx).ID
	code, body, raw := e.do(t, "POST", "/api/v1/admin/accounts", e.tokenFor(t, e.adminID), map[string]any{
		"email": "x@t.test", "shop_id": shopB, "role_id": e.roleID(t, "merchant_owner"),
	})
	if code != http.StatusCreated {
		t.Fatalf("provision: %d %s", code, raw)
	}
	victim := int(body["user_id"].(float64))

	_, _, tok := e.invite(t, e.tokenFor(t, owner), e.shopA, "x@t.test", e.roleID(t, "staff"))
	if tok == "" {
		t.Fatal("inviting an existing e-mail must still succeed (same response)")
	}
	_, insp, _ := e.do(t, "POST", "/api/v1/admin/auth/invitations/inspect", "", map[string]string{"token": tok})
	if insp["requires_login"] != true {
		t.Fatalf("inspect must say requires_login for an existing account: %+v", insp)
	}
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/auth/invitations/accept", "", map[string]string{"token": tok, "password": "attacker-chosen-pw-1"}); c != http.StatusConflict {
		t.Fatalf("takeover attempt: want 409, got %d", c)
	}
	u := e.client.User.GetX(ctx, victim)
	if u.PasswordHash != nil {
		t.Fatal("password_hash must remain NULL")
	}
	if e.client.ShopUser.Query().Where(shopuser.UserIDEQ(victim), shopuser.ShopIDEQ(e.shopA)).ExistX(ctx) {
		t.Fatal("no membership may have been created")
	}
	// A setup/reset token cannot be spent through the invitation endpoint, nor
	// an invitation through the password-setup endpoint.
	setupTok := tokenFromURL(t, body["setup_url"].(string))
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/auth/invitations/accept", "", map[string]string{"token": setupTok, "password": teamPassword}); c != http.StatusNotFound {
		t.Fatalf("setup token via invitation endpoint: want 404, got %d", c)
	}
	if c := e.setPassword(t, tok, teamPassword); c != http.StatusNotFound {
		t.Fatalf("invitation token via password-setup endpoint: want 404, got %d", c)
	}
	if e.client.User.GetX(ctx, victim).PasswordHash != nil {
		t.Fatal("password_hash must still be NULL")
	}
}

// Scenario: 既有帳號登入後接受 / 他人無法代為接受.
func TestTeamAcceptAuthenticated(t *testing.T) {
	e := newAcctEnv(t)
	ctx := context.Background()
	shopB := e.client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(ctx).ID
	owner := e.mkMember(t, "owner-a@t.dev", e.shopA, "merchant_owner")
	a := e.mkMember(t, "alice@t.dev", shopB, "editor")
	b := e.mkMember(t, "bob@t.dev", shopB, "editor")
	hashBefore := *e.client.User.GetX(ctx, a).PasswordHash

	_, _, tok := e.invite(t, e.tokenFor(t, owner), e.shopA, "alice@t.dev", e.roleID(t, "staff"))

	// Bob cannot accept Alice's invitation, and that does not burn it.
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/invitations/accept", e.tokenFor(t, b), map[string]string{"token": tok}); c != http.StatusForbidden {
		t.Fatalf("other account: want 403, got %d", c)
	}
	// No token at all: unauthenticated.
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/invitations/accept", "", map[string]string{"token": tok}); c != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: want 401, got %d", c)
	}
	// Garbage token: the same 404 as any invalid token.
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/invitations/accept", e.tokenFor(t, a), map[string]string{"token": "nope"}); c != http.StatusNotFound {
		t.Fatalf("garbage token: want 404, got %d", c)
	}
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/invitations/accept", e.tokenFor(t, a), map[string]string{"token": tok}); c != http.StatusNoContent {
		t.Fatalf("alice accept: want 204, got %d", c)
	}
	if !e.client.ShopUser.Query().Where(shopuser.UserIDEQ(a), shopuser.ShopIDEQ(e.shopA), shopuser.StatusEQ(1)).ExistX(ctx) ||
		!e.client.RoleUser.Query().Where(roleuser.UserIDEQ(a), roleuser.ShopIDEQ(e.shopA)).ExistX(ctx) {
		t.Fatal("alice must be an active staff member of shop A")
	}
	if *e.client.User.GetX(ctx, a).PasswordHash != hashBefore {
		t.Fatal("an existing account's password must not change")
	}
	// Her other shop is untouched.
	if !e.client.RoleUser.Query().Where(roleuser.UserIDEQ(a), roleuser.ShopIDEQ(shopB)).ExistX(ctx) {
		t.Fatal("shop B role was disturbed")
	}
	// Accepting again: the token is spent.
	if c, _, _ := e.do(t, "POST", "/api/v1/admin/invitations/accept", e.tokenFor(t, a), map[string]string{"token": tok}); c != http.StatusNotFound {
		t.Fatalf("reuse: want 404, got %d", c)
	}
}

// Scenario: 消費是原子的 — N concurrent acceptances of one invitation produce
// exactly one account and exactly one success.
func TestTeamConcurrentAcceptExactlyOnce(t *testing.T) {
	e := newAcctEnv(t)
	ctx := context.Background()
	owner := e.mkMember(t, "owner-a@t.dev", e.shopA, "merchant_owner")
	_, _, tok := e.invite(t, e.tokenFor(t, owner), e.shopA, "racer@bouquet.test", e.roleID(t, "staff"))

	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, _, _ := e.do(t, "POST", "/api/v1/admin/auth/invitations/accept", "", map[string]string{"token": tok, "password": teamPassword})
			codes[i] = c
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == http.StatusNoContent {
			ok++
		} else if c != http.StatusNotFound && c != http.StatusConflict {
			t.Fatalf("unexpected status %d in %v", c, codes)
		}
	}
	if ok != 1 {
		t.Fatalf("successes = %d, want exactly 1 (%v)", ok, codes)
	}
	if n := e.client.User.Query().Where(user.EmailEQ("racer@bouquet.test")).CountX(ctx); n != 1 {
		t.Fatalf("accounts created = %d, want 1", n)
	}
	if n := e.client.RoleUser.Query().Where(roleuser.ShopIDEQ(e.shopA), roleuser.HasUserWith(user.EmailEQ("racer@bouquet.test"))).CountX(ctx); n != 1 {
		t.Fatalf("role assignments = %d, want 1", n)
	}
}

// Scenario: 停用店員 / 不影響他店 / 啟用 / 不能停用自己 / 不能停用另一位 owner /
// sids 只含啟用中的成員資格. The cut-off is the very next request: the access
// token is untouched.
func TestTeamDisableAndEnableMembership(t *testing.T) {
	e := newAcctEnv(t)
	ctx := context.Background()
	shopB := e.client.Shop.Create().SetName("Shop B").SetStatus(1).SaveX(ctx).ID
	owner := e.mkMember(t, "owner-a@t.dev", e.shopA, "merchant_owner")
	owner2 := e.mkMember(t, "owner-a2@t.dev", e.shopA, "merchant_owner")
	clerk := e.mkMember(t, "clerk@t.dev", e.shopA, "staff")
	e.client.ShopUser.Create().SetShopID(shopB).SetUserID(clerk).SaveX(ctx) // also in shop B
	e.client.RoleUser.Create().SetUserID(clerk).SetRoleID(e.roleID(t, "editor")).SetShopID(shopB).SaveX(ctx)

	ownerTok, clerkTok := e.tokenFor(t, owner), e.tokenFor(t, clerk)
	meA := fmt.Sprintf("/api/v1/admin/shops/%d/me", e.shopA)
	meB := fmt.Sprintf("/api/v1/admin/shops/%d/me", shopB)
	disable := fmt.Sprintf("/api/v1/admin/shops/%d/users/%d/disable", e.shopA, clerk)
	enable := fmt.Sprintf("/api/v1/admin/shops/%d/users/%d/enable", e.shopA, clerk)

	if c, _, _ := e.do(t, "GET", meA, clerkTok, nil); c != http.StatusOK {
		t.Fatalf("clerk before disable: %d", c)
	}
	// An editor cannot disable anyone.
	editor := e.mkMember(t, "editor-a@t.dev", e.shopA, "editor")
	if c, _, _ := e.do(t, "POST", disable, e.tokenFor(t, editor), nil); c != http.StatusForbidden {
		t.Fatalf("editor disable: want 403, got %d", c)
	}

	if c, _, _ := e.do(t, "POST", disable, ownerTok, nil); c != http.StatusNoContent {
		t.Fatalf("disable: want 204, got %d", c)
	}
	// Same access token, next request: shop A is closed, shop B still open.
	if c, _, _ := e.do(t, "GET", meA, clerkTok, nil); c != http.StatusForbidden {
		t.Fatalf("clerk after disable: want 403, got %d", c)
	}
	if c, _, _ := e.do(t, "GET", meB, clerkTok, nil); c != http.StatusOK {
		t.Fatalf("clerk in shop B after shop A disabled: want 200, got %d", c)
	}
	// The account itself is untouched.
	if e.client.User.GetX(ctx, clerk).Status != 1 {
		t.Fatal("users.status must not change")
	}
	// A fresh login lists only the active membership.
	c, login := e.login(t, "clerk@t.dev", teamPassword)
	if c != http.StatusOK {
		t.Fatalf("login: %d", c)
	}
	_, me, _ := e.do(t, "GET", "/api/v1/admin/me", login["access_token"].(string), nil)
	sids := me["sids"].([]any)
	if len(sids) != 1 || int(sids[0].(float64)) != shopB {
		t.Fatalf("sids = %v, want only shop B", sids)
	}

	if c, _, _ := e.do(t, "POST", enable, ownerTok, nil); c != http.StatusNoContent {
		t.Fatalf("enable: want 204, got %d", c)
	}
	if c, _, _ := e.do(t, "GET", meA, clerkTok, nil); c != http.StatusOK {
		t.Fatalf("clerk after enable: want 200, got %d", c)
	}

	// Self-protection and owner protection.
	if c, _, _ := e.do(t, "POST", fmt.Sprintf("/api/v1/admin/shops/%d/users/%d/disable", e.shopA, owner), ownerTok, nil); c != http.StatusConflict {
		t.Fatalf("disable self: want 409, got %d", c)
	}
	if c, _, _ := e.do(t, "POST", fmt.Sprintf("/api/v1/admin/shops/%d/users/%d/disable", e.shopA, owner2), ownerTok, nil); c != http.StatusForbidden {
		t.Fatalf("disable another owner: want 403, got %d", c)
	}
	if e.client.ShopUser.Query().Where(shopuser.UserIDEQ(owner2), shopuser.StatusEQ(0)).ExistX(ctx) {
		t.Fatal("the other owner's membership changed")
	}
	// A non-member target is 404, and another shop's owner has no reach.
	if c, _, _ := e.do(t, "POST", fmt.Sprintf("/api/v1/admin/shops/%d/users/%d/disable", e.shopA, e.adminID), ownerTok, nil); c != http.StatusNotFound {
		t.Fatalf("non-member target: want 404, got %d", c)
	}
	ownerB := e.mkMember(t, "owner-b@t.dev", shopB, "merchant_owner")
	if c, _, _ := e.do(t, "POST", disable, e.tokenFor(t, ownerB), nil); c != http.StatusForbidden {
		t.Fatalf("cross-shop disable: want 403, got %d", c)
	}
}

// A disabled membership cannot be revived by accepting an invitation as that
// same person: the invite endpoint refuses an existing membership (409).
func TestTeamInviteRefusesExistingDisabledMembership(t *testing.T) {
	e := newAcctEnv(t)
	owner := e.mkMember(t, "owner-a@t.dev", e.shopA, "merchant_owner")
	clerk := e.mkMember(t, "clerk@t.dev", e.shopA, "staff")
	ownerTok := e.tokenFor(t, owner)
	if c, _, _ := e.do(t, "POST", fmt.Sprintf("/api/v1/admin/shops/%d/users/%d/disable", e.shopA, clerk), ownerTok, nil); c != http.StatusNoContent {
		t.Fatalf("disable: %d", c)
	}
	if c, _, _ := e.invite(t, ownerTok, e.shopA, "clerk@t.dev", e.roleID(t, "staff")); c != http.StatusConflict {
		t.Fatalf("invite a disabled member: want 409, got %d", c)
	}
}

// Scenario (rbac MODIFIED): 商家管理員不能授予 owner / 平台管理員可授予 owner /
// owner 改另一位 owner 的角色 403 / 既有 editor 指派仍可用.
func TestTeamRoleDelegationRules(t *testing.T) {
	e := newAcctEnv(t)
	ctx := context.Background()
	owner := e.mkMember(t, "owner-a@t.dev", e.shopA, "merchant_owner")
	owner2 := e.mkMember(t, "owner-a2@t.dev", e.shopA, "merchant_owner")
	clerk := e.mkMember(t, "clerk@t.dev", e.shopA, "staff")
	ownerTok := e.tokenFor(t, owner)
	rolesPath := func(uid int) string { return fmt.Sprintf("/api/v1/admin/shops/%d/users/%d/roles", e.shopA, uid) }

	// owner -> editor: still allowed.
	if c, _, raw := e.do(t, "POST", rolesPath(clerk), ownerTok, map[string]int{"role_id": e.roleID(t, "editor")}); c != http.StatusOK {
		t.Fatalf("owner assigns editor: want 200, got %d %s", c, raw)
	}
	// owner -> merchant_owner: refused, nothing assigned.
	if c, _, _ := e.do(t, "POST", rolesPath(clerk), ownerTok, map[string]int{"role_id": e.roleID(t, "merchant_owner")}); c != http.StatusForbidden {
		t.Fatalf("owner assigns merchant_owner: want 403, got %d", c)
	}
	if e.client.RoleUser.Query().Where(roleuser.UserIDEQ(clerk), roleuser.RoleIDEQ(e.roleID(t, "merchant_owner"))).ExistX(ctx) {
		t.Fatal("merchant_owner was assigned despite the 403")
	}
	// owner touching another owner: 403 both ways.
	if c, _, _ := e.do(t, "POST", rolesPath(owner2), ownerTok, map[string]int{"role_id": e.roleID(t, "staff")}); c != http.StatusForbidden {
		t.Fatalf("owner assigns a role to another owner: want 403, got %d", c)
	}
	if c, _, _ := e.do(t, "DELETE", fmt.Sprintf("%s/%d", rolesPath(owner2), e.roleID(t, "merchant_owner")), ownerTok, nil); c != http.StatusForbidden {
		t.Fatalf("owner removes another owner's role: want 403, got %d", c)
	}
	// owner cannot strip their own role.
	if c, _, _ := e.do(t, "DELETE", fmt.Sprintf("%s/%d", rolesPath(owner), e.roleID(t, "merchant_owner")), ownerTok, nil); c != http.StatusConflict {
		t.Fatalf("owner removes own role: want 409, got %d", c)
	}
	// owner removes a staff/editor role: fine.
	if c, _, _ := e.do(t, "DELETE", fmt.Sprintf("%s/%d", rolesPath(clerk), e.roleID(t, "editor")), ownerTok, nil); c != http.StatusNoContent {
		t.Fatalf("owner removes editor: want 204, got %d", c)
	}
	// The platform may grant merchant_owner (and it is applied).
	if c, _, raw := e.do(t, "POST", rolesPath(clerk), e.tokenFor(t, e.adminID), map[string]int{"role_id": e.roleID(t, "merchant_owner")}); c != http.StatusOK {
		t.Fatalf("platform assigns merchant_owner: want 200, got %d %s", c, raw)
	}
	// ... and a former clerk holding owner can no longer be managed by owners.
	if c, _, _ := e.do(t, "POST", fmt.Sprintf("/api/v1/admin/shops/%d/users/%d/disable", e.shopA, clerk), ownerTok, nil); c != http.StatusForbidden {
		t.Fatalf("owner disables a promoted owner: want 403, got %d", c)
	}
}

// The role catalog tells the UI which roles a shop may hand out.
func TestTeamRoleListExposesAssignable(t *testing.T) {
	e := newAcctEnv(t)
	owner := e.mkMember(t, "owner-a@t.dev", e.shopA, "merchant_owner")
	_, body, raw := e.do(t, "GET", fmt.Sprintf("/api/v1/admin/shops/%d/roles", e.shopA), e.tokenFor(t, owner), nil)
	got := map[string]bool{}
	for _, r := range body["roles"].([]any) {
		m := r.(map[string]any)
		v, ok := m["merchant_assignable"].(bool)
		if !ok {
			t.Fatalf("role without merchant_assignable: %s", raw)
		}
		got[m["name"].(string)] = v
	}
	if got["merchant_owner"] || !got["editor"] || !got["staff"] {
		t.Fatalf("assignable flags = %v", got)
	}
}
