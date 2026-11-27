package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPreviewTokenCarriesScopeAndOrigin(t *testing.T) {
	ti := newTestIssuer(t)
	tok, err := ti.IssueAdminPreview(7, []int{3}, "https://a.preview.example.test", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, uid, err := ti.VerifyAdminPreview(tok)
	if err != nil || uid != 7 || c.Scope != ScopePreviewRO || c.Origin != "https://a.preview.example.test" || len(c.SIDs) != 1 {
		t.Fatalf("claims %+v uid %d err %v", c, uid, err)
	}
	if exp := c.ExpiresAt.Sub(c.IssuedAt.Time); exp > 10*time.Minute+time.Second {
		t.Fatalf("lifetime %v", exp)
	}
}

func TestPreviewTokenLifetimeNeverExceedsAccessTTL(t *testing.T) {
	ti := newTestIssuer(t)
	tok, _ := ti.IssueAdminPreview(1, nil, "https://a.preview.example.test", 5*time.Hour)
	c, _, err := ti.VerifyAdminPreview(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.ExpiresAt.Sub(c.IssuedAt.Time) > 15*time.Minute {
		t.Fatal("preview token must be capped at the access TTL")
	}
}

// Token confusion at the signature/audience layer: neither class verifies as
// the other, and no other token family verifies as a preview token.
func TestPreviewAndNormalTokensDoNotCross(t *testing.T) {
	ti := newTestIssuer(t)
	ro, _ := ti.IssueAdminPreview(1, nil, "https://a.preview.example.test", time.Minute)
	if _, _, err := ti.VerifyAdmin(ro); err == nil {
		t.Fatal("a preview_ro token must NOT verify as an ordinary admin token")
	}
	normal, _ := ti.IssueAdmin(1, nil)
	if _, _, err := ti.VerifyAdminPreview(normal); err == nil {
		t.Fatal("an ordinary admin token must NOT verify as a preview token")
	}
	member, _ := ti.IssueMember(1, 1)
	if _, _, err := ti.VerifyAdminPreview(member); err == nil {
		t.Fatal("a member token must not verify as a preview token")
	}
	pg, _ := ti.IssuePreview(1, 1, "home", time.Minute)
	if _, _, err := ti.VerifyAdminPreview(pg); err == nil {
		t.Fatal("a page preview token must not verify as a preview session token")
	}
	if _, _, err := ti.VerifyAdminPreview(ro + "x"); err == nil {
		t.Fatal("tampered token must fail")
	}
	expired, _ := ti.IssueAdminPreview(1, nil, "https://a.preview.example.test", -time.Minute)
	if _, _, err := ti.VerifyAdminPreview(expired); err == nil {
		t.Fatal("expired token must fail")
	}
}

func TestOriginTagAndRefreshPrefix(t *testing.T) {
	a, b := OriginTag("https://a.preview.example.test"), OriginTag("https://b.preview.example.test")
	if a == b || len(a) != 16 || strings.Contains(a, "preview") {
		t.Fatalf("tags %q %q", a, b)
	}
	tok := previewPrefix(a) + "RANDOM"
	if !IsPreviewRefresh(tok) || PreviewRefreshTag(tok) != a {
		t.Fatal("prefix round trip")
	}
	plain, _, _ := newOpaqueToken()
	if IsPreviewRefresh(plain) {
		t.Fatal("a normal refresh token can never look like a preview one")
	}
	if PreviewRefreshTag(plain) != "" {
		t.Fatal("no tag on a normal token")
	}
}
