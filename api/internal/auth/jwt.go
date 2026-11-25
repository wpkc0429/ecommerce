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
//
// Each system carries a second, narrower token family that reuses its secret
// but takes a disjoint audience, so the jwt library rejects cross-family use
// during verification rather than leaving it to a handler-side check:
//
//	admin secret  → aud=admin           back-office access token
//	              → aud=preview         page preview (short-lived, one page)
//	member secret → aud=shop:{id}       member access token
//	              → aud=order:{id}      order access token (change
//	                                    guest-checkout: short-lived, one order,
//	                                    not a session — no refresh, no history)
//
// Both narrow families bind their target into the claims (preview: shop+slug;
// order access: the order id in `sub`) and their shop into the audience, so
// cross-shop reuse fails at audience validation.

const (
	AdminAudience    = "admin"
	adminIssuerSfx   = ""
	memberAudPrefix  = "shop:"
	orderAudPrefix   = "order:"
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

// OrderAccessClaims bind a short-lived order access token to exactly one order
// (change guest-checkout, design D3): sub = order id, aud = order:{shop_id}.
// Signed with the member secret because this is a storefront customer-facing
// token, but its audience makes it unusable as a member access token (and vice
// versa) — VerifyMember demands aud=shop:{id}.
//
// This is deliberately NOT a session: there is no refresh counterpart, it
// cannot list order history, cannot touch the identity's credentials, points or
// any other order. ShopID is redundant with the audience and kept as defense in
// depth plus log readability.
type OrderAccessClaims struct {
	ShopID int `json:"shop_id"`
	jwt.RegisteredClaims
}

// OrderAud renders the audience of an order access token for a shop.
func OrderAud(shopID int) string { return orderAudPrefix + strconv.Itoa(shopID) }

// IssueOrderAccess signs an order access token naming exactly one order. ttl is
// supplied by the caller (config.OrderAccessTokenTTL) rather than reusing
// accessTTL — this token's lifetime is a product decision about how long a
// guest's confirmation page stays usable, not a session length (mirrors
// IssuePreview's parameterized ttl).
func (ti *TokenIssuer) IssueOrderAccess(orderID, shopID int, ttl time.Duration) (string, error) {
	now := ti.now()
	claims := OrderAccessClaims{
		ShopID: shopID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.Itoa(orderID),
			Issuer:    ti.issuer,
			Audience:  jwt.ClaimStrings{OrderAud(shopID)},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(ti.memberSecret)
}

// VerifyOrderAccess validates an order access token against the expected shop
// context and returns the order id it names. Any audience mismatch (cross-shop
// reuse, or a member/admin/preview token) fails.
func (ti *TokenIssuer) VerifyOrderAccess(token string, shopID int) (int, error) {
	claims := &OrderAccessClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return ti.memberSecret, nil
	},
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(ti.issuer),
		jwt.WithAudience(OrderAud(shopID)),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !parsed.Valid {
		return 0, ErrInvalidToken
	}
	oid, err := strconv.Atoi(claims.Subject)
	if err != nil || oid <= 0 || claims.ShopID != shopID {
		return 0, ErrInvalidToken
	}
	return oid, nil
}

// ContentPreviewClaims bind a short-lived content preview token to (user, shop,
// scope, key) (change headless-content-store slice 2, design D2). Signed with
// the admin secret but with its own audience, so it is unusable as an access
// token or as a page preview token, and vice versa.
type ContentPreviewClaims struct {
	ShopID int    `json:"shop_id"`
	Scope  string `json:"scope"`
	Key    string `json:"key,omitempty"`
	jwt.RegisteredClaims
}

const contentPreviewAudience = "content-preview"

// Scopes of a content preview token.
const (
	PreviewScopeDocument = "document"
	PreviewScopeShop     = "shop"
)

// IssueContentPreview signs a content preview token. scope "document" is bound
// to one key; scope "shop" previews every draft of the shop.
func (ti *TokenIssuer) IssueContentPreview(userID, shopID int, scope, key string, ttl time.Duration) (string, error) {
	now := ti.now()
	claims := ContentPreviewClaims{
		ShopID: shopID, Scope: scope, Key: key,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.Itoa(userID),
			Issuer:    ti.issuer,
			Audience:  jwt.ClaimStrings{contentPreviewAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(ti.adminSecret)
}

// VerifyContentPreview validates a content preview token.
func (ti *TokenIssuer) VerifyContentPreview(token string) (*ContentPreviewClaims, int, error) {
	claims := &ContentPreviewClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return ti.adminSecret, nil
	},
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(ti.issuer),
		jwt.WithAudience(contentPreviewAudience),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !parsed.Valid {
		return nil, 0, ErrInvalidToken
	}
	uid, err := strconv.Atoi(claims.Subject)
	if err != nil || uid <= 0 || claims.ShopID <= 0 ||
		(claims.Scope != PreviewScopeDocument && claims.Scope != PreviewScopeShop) ||
		(claims.Scope == PreviewScopeDocument && claims.Key == "") {
		return nil, 0, ErrInvalidToken
	}
	return claims, uid, nil
}
