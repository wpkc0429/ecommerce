package auth

import (
	"testing"
	"time"
)

func newTestIssuer(t *testing.T) *TokenIssuer {
	t.Helper()
	ti, err := NewTokenIssuer("admin-secret-1", "member-secret-2", "test-issuer", 15*time.Minute)
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	return ti
}

func TestSameSecretRejected(t *testing.T) {
	if _, err := NewTokenIssuer("same-secret-x", "same-secret-x", "iss", time.Minute); err == nil {
		t.Fatal("identical admin/member secrets must be rejected (design D9)")
	}
}

func TestAdminTokenRoundTrip(t *testing.T) {
	ti := newTestIssuer(t)
	tok, err := ti.IssueAdmin(42, []int{1, 2})
	if err != nil {
		t.Fatalf("IssueAdmin: %v", err)
	}
	uid, sids, err := ti.VerifyAdmin(tok)
	if err != nil {
		t.Fatalf("VerifyAdmin: %v", err)
	}
	if uid != 42 || len(sids) != 2 {
		t.Fatalf("got uid=%d sids=%v", uid, sids)
	}
}

func TestMemberTokenRoundTrip(t *testing.T) {
	ti := newTestIssuer(t)
	tok, err := ti.IssueMember(7, 3)
	if err != nil {
		t.Fatalf("IssueMember: %v", err)
	}
	mid, err := ti.VerifyMember(tok, 3)
	if err != nil {
		t.Fatalf("VerifyMember: %v", err)
	}
	if mid != 7 {
		t.Fatalf("got mid=%d", mid)
	}
}

func TestOrderAccessTokenRoundTrip(t *testing.T) {
	ti := newTestIssuer(t)
	tok, err := ti.IssueOrderAccess(55, 3, time.Hour)
	if err != nil {
		t.Fatalf("IssueOrderAccess: %v", err)
	}
	oid, err := ti.VerifyOrderAccess(tok, 3)
	if err != nil {
		t.Fatalf("VerifyOrderAccess: %v", err)
	}
	if oid != 55 {
		t.Fatalf("got order id=%d, want 55", oid)
	}
}

// Token isolation matrix (spec authentication/Token isolation): every family
// must be inert in every other family's verification path, and the two
// shop-bound families must be inert across shops.
//
// change guest-checkout adds the fourth family (order access, aud=order:{shop}).
// It is signed with the *member* secret, so this matrix is the only thing
// standing between it and a member session — hence every cell is asserted, not
// just the interesting-looking ones.
func TestTokenIsolationMatrix(t *testing.T) {
	ti := newTestIssuer(t)
	adminTok, _ := ti.IssueAdmin(1, nil)
	previewTok, _ := ti.IssuePreview(1, 1, "home", time.Hour)
	memberTokShopA, _ := ti.IssueMember(9, 1)
	orderTokShopA, _ := ti.IssueOrderAccess(9, 1, time.Hour)

	if _, err := ti.VerifyMember(adminTok, 1); err == nil {
		t.Fatal("admin token accepted as member token")
	}
	if _, _, err := ti.VerifyAdmin(memberTokShopA); err == nil {
		t.Fatal("member token accepted as admin token")
	}
	if _, err := ti.VerifyMember(memberTokShopA, 2); err == nil {
		t.Fatal("shop A member token accepted in shop B context")
	}

	// The order access token must not be usable as anything else. Note the
	// subject collides with a valid member id on purpose: only the audience
	// separates them.
	if _, err := ti.VerifyMember(orderTokShopA, 1); err == nil {
		t.Fatal("order access token accepted as member token")
	}
	if _, _, err := ti.VerifyAdmin(orderTokShopA); err == nil {
		t.Fatal("order access token accepted as admin token")
	}
	if _, _, _, err := ti.VerifyPreview(orderTokShopA); err == nil {
		t.Fatal("order access token accepted as preview token")
	}

	// ...and nothing else may be used as one.
	if _, err := ti.VerifyOrderAccess(memberTokShopA, 1); err == nil {
		t.Fatal("member token accepted as order access token")
	}
	if _, err := ti.VerifyOrderAccess(adminTok, 1); err == nil {
		t.Fatal("admin token accepted as order access token")
	}
	if _, err := ti.VerifyOrderAccess(previewTok, 1); err == nil {
		t.Fatal("preview token accepted as order access token")
	}
	if _, err := ti.VerifyOrderAccess(orderTokShopA, 2); err == nil {
		t.Fatal("shop A order access token accepted in shop B context")
	}
}

func TestOrderAccessTokenExpires(t *testing.T) {
	ti := newTestIssuer(t)
	past := &TokenIssuer{
		adminSecret:  ti.adminSecret,
		memberSecret: ti.memberSecret,
		issuer:       ti.issuer,
		accessTTL:    ti.accessTTL,
		now:          func() time.Time { return time.Now().Add(-2 * time.Hour) },
	}
	expired, _ := past.IssueOrderAccess(5, 1, time.Hour)
	if _, err := ti.VerifyOrderAccess(expired, 1); err == nil {
		t.Fatal("expired order access token accepted")
	}
}

func TestOrderAccessTokenRejectsForeignSecret(t *testing.T) {
	ti := newTestIssuer(t)
	other, _ := NewTokenIssuer("attacker-admin", "attacker-member", "test-issuer", 15*time.Minute)
	forged, _ := other.IssueOrderAccess(5, 1, time.Hour)
	if _, err := ti.VerifyOrderAccess(forged, 1); err == nil {
		t.Fatal("order access token signed with a foreign secret accepted")
	}
}

func TestForgedAndExpiredTokens(t *testing.T) {
	ti := newTestIssuer(t)
	other, _ := NewTokenIssuer("attacker-admin", "attacker-member", "test-issuer", 15*time.Minute)

	forged, _ := other.IssueAdmin(1, nil)
	if _, _, err := ti.VerifyAdmin(forged); err == nil {
		t.Fatal("token signed with wrong secret accepted")
	}

	past := &TokenIssuer{
		adminSecret:  ti.adminSecret,
		memberSecret: ti.memberSecret,
		issuer:       ti.issuer,
		accessTTL:    time.Minute,
		now:          func() time.Time { return time.Now().Add(-time.Hour) },
	}
	expired, _ := past.IssueAdmin(1, nil)
	if _, _, err := ti.VerifyAdmin(expired); err == nil {
		t.Fatal("expired token accepted")
	}

	if _, _, err := ti.VerifyAdmin("not-a-jwt"); err == nil {
		t.Fatal("garbage accepted")
	}
}
