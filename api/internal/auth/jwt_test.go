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

// Token isolation matrix (spec authentication/Token isolation): member tokens
// must never verify as admin, admin tokens never as member, and member tokens
// never across shops.
func TestTokenIsolationMatrix(t *testing.T) {
	ti := newTestIssuer(t)
	adminTok, _ := ti.IssueAdmin(1, nil)
	memberTokShopA, _ := ti.IssueMember(9, 1)

	if _, err := ti.VerifyMember(adminTok, 1); err == nil {
		t.Fatal("admin token accepted as member token")
	}
	if _, _, err := ti.VerifyAdmin(memberTokShopA); err == nil {
		t.Fatal("member token accepted as admin token")
	}
	if _, err := ti.VerifyMember(memberTokShopA, 2); err == nil {
		t.Fatal("shop A member token accepted in shop B context")
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
