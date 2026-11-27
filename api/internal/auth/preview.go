package auth

import (
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Read-only preview sessions (change preview-readonly-session).
//
// A login or refresh issued from a configured PREVIEW origin yields a session
// whose access token carries scope=preview_ro and is bound to that exact
// origin. It uses its own audience ("admin-preview", admin secret), so
// VerifyAdmin - and with it every route that only knows ordinary admin tokens
// - rejects it by audience: a read-only token can never be mistaken for a
// normal one. The refresh token is an opaque value with a "pv1.<origin tag>."
// prefix; only the SHA-256 of the whole string is stored, so the prefix cannot
// be edited by a client, and no schema change is needed.

const (
	// ScopePreviewRO is the value of the `scope` claim of a read-only preview session.
	ScopePreviewRO = "preview_ro"
	// AdminPreviewAudience is the audience of preview_ro access tokens.
	AdminPreviewAudience = "admin-preview"
)

// AdminPreviewClaims are the claims of a read-only preview access token.
type AdminPreviewClaims struct {
	SIDs   []int  `json:"sids"`
	Scope  string `json:"scope"`
	Origin string `json:"pvo"` // the canonical preview origin the session is bound to
	jwt.RegisteredClaims
}

// IssueAdminPreview signs a preview_ro access token bound to origin.
func (ti *TokenIssuer) IssueAdminPreview(userID int, sids []int, origin string, ttl time.Duration) (string, error) {
	now := ti.now()
	if ttl > ti.accessTTL {
		ttl = ti.accessTTL
	}
	claims := AdminPreviewClaims{
		SIDs: sids, Scope: ScopePreviewRO, Origin: origin,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.Itoa(userID),
			Issuer:    ti.issuer,
			Audience:  jwt.ClaimStrings{AdminPreviewAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(ti.adminSecret)
}

// VerifyAdminPreview validates a preview_ro access token.
func (ti *TokenIssuer) VerifyAdminPreview(token string) (*AdminPreviewClaims, int, error) {
	claims := &AdminPreviewClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return ti.adminSecret, nil
	},
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(ti.issuer),
		jwt.WithAudience(AdminPreviewAudience),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !parsed.Valid || claims.Scope != ScopePreviewRO || claims.Origin == "" {
		return nil, 0, ErrInvalidToken
	}
	uid, err := strconv.Atoi(claims.Subject)
	if err != nil || uid <= 0 {
		return nil, 0, ErrInvalidToken
	}
	return claims, uid, nil
}
