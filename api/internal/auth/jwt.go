package auth

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Two fully isolated JWT systems (design D9): admin tokens (aud=admin) and
// member tokens (aud=shop:{id}) are signed with different secrets, so a token
// from one system can never validate in the other.

const (
	AdminAudience    = "admin"
	adminIssuerSfx   = ""
	memberAudPrefix  = "shop:"
	minSecretEntropy = 8 // sanity floor; production secrets are validated in config
)

// ErrInvalidToken is returned for any token that fails verification.
// Callers translate it to a uniform 401 — no reason leakage.
var ErrInvalidToken = errors.New("auth: invalid token")

// TokenIssuer signs and verifies both token families.
type TokenIssuer struct {
	adminSecret  []byte
	memberSecret []byte
	issuer       string
	accessTTL    time.Duration
	now          func() time.Time // injectable for tests
}

// NewTokenIssuer builds a TokenIssuer. Secrets must differ (enforced in
// config for production; double-checked here).
func NewTokenIssuer(adminSecret, memberSecret, issuer string, accessTTL time.Duration) (*TokenIssuer, error) {
	if len(adminSecret) < minSecretEntropy || len(memberSecret) < minSecretEntropy {
		return nil, fmt.Errorf("auth: JWT secrets too short")
	}
	if adminSecret == memberSecret {
		return nil, fmt.Errorf("auth: admin and member JWT secrets must differ (design D9)")
	}
	return &TokenIssuer{
		adminSecret:  []byte(adminSecret),
		memberSecret: []byte(memberSecret),
		issuer:       issuer,
		accessTTL:    accessTTL,
		now:          time.Now,
	}, nil
}

// AccessTTL exposes the access-token lifetime (for expires_in responses).
func (ti *TokenIssuer) AccessTTL() time.Duration { return ti.accessTTL }

// AdminClaims are the claims of a back-office access token: sub = user id,
// sids = shop membership hints for the UI only (never used for authorization
// — design D9 keeps permissions out of the JWT).
type AdminClaims struct {
	SIDs []int `json:"sids"`
	jwt.RegisteredClaims
}

// MemberAud renders the audience of a member token for a shop.
func MemberAud(shopID int) string { return memberAudPrefix + strconv.Itoa(shopID) }

// IssueAdmin signs an access token for a back-office user.
func (ti *TokenIssuer) IssueAdmin(userID int, sids []int) (string, error) {
	now := ti.now()
	claims := AdminClaims{
		SIDs: sids,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.Itoa(userID),
			Issuer:    ti.issuer,
			Audience:  jwt.ClaimStrings{AdminAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ti.accessTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(ti.adminSecret)
}

// VerifyAdmin validates an admin access token and returns (userID, sids).
func (ti *TokenIssuer) VerifyAdmin(token string) (int, []int, error) {
	claims := &AdminClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return ti.adminSecret, nil
	},
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(ti.issuer),
		jwt.WithAudience(AdminAudience),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !parsed.Valid {
		return 0, nil, ErrInvalidToken
	}
	uid, err := strconv.Atoi(claims.Subject)
	if err != nil || uid <= 0 {
		return 0, nil, ErrInvalidToken
	}
	return uid, claims.SIDs, nil
}

// PreviewClaims bind a short-lived preview token to (user, shop, slug)
// (task 7.4). Signed with the admin secret, aud=preview — unusable as an
// access token anywhere else.
type PreviewClaims struct {
	ShopID int    `json:"shop_id"`
	Slug   string `json:"slug"`
	jwt.RegisteredClaims
}

const previewAudience = "preview"

// IssuePreview signs a preview token for a page working copy.
func (ti *TokenIssuer) IssuePreview(userID, shopID int, slug string, ttl time.Duration) (string, error) {
	now := ti.now()
	claims := PreviewClaims{
		ShopID: shopID,
		Slug:   slug,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.Itoa(userID),
			Issuer:    ti.issuer,
			Audience:  jwt.ClaimStrings{previewAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(ti.adminSecret)
}

// VerifyPreview validates a preview token → (userID, shopID, slug).
func (ti *TokenIssuer) VerifyPreview(token string) (int, int, string, error) {
	claims := &PreviewClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return ti.adminSecret, nil
	},
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(ti.issuer),
		jwt.WithAudience(previewAudience),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !parsed.Valid {
		return 0, 0, "", ErrInvalidToken
	}
	uid, err := strconv.Atoi(claims.Subject)
	if err != nil || uid <= 0 || claims.ShopID <= 0 {
		return 0, 0, "", ErrInvalidToken
	}
	return uid, claims.ShopID, claims.Slug, nil
}

// IssueMember signs an access token for a member bound to one shop.
func (ti *TokenIssuer) IssueMember(memberID, shopID int) (string, error) {
	now := ti.now()
	claims := jwt.RegisteredClaims{
		Subject:   strconv.Itoa(memberID),
		Issuer:    ti.issuer,
		Audience:  jwt.ClaimStrings{MemberAud(shopID)},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ti.accessTTL)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(ti.memberSecret)
}

// VerifyMember validates a member access token against the expected shop
// context; any audience mismatch (cross-shop reuse) fails.
func (ti *TokenIssuer) VerifyMember(token string, shopID int) (int, error) {
	claims := &jwt.RegisteredClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return ti.memberSecret, nil
	},
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(ti.issuer),
		jwt.WithAudience(MemberAud(shopID)),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !parsed.Valid {
		return 0, ErrInvalidToken
	}
	mid, err := strconv.Atoi(claims.Subject)
	if err != nil || mid <= 0 {
		return 0, ErrInvalidToken
	}
	return mid, nil
}
