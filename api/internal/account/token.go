package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/admincredentialtoken"
	"ksdevworks/ecommerce/api/internal/ent/user"
)

// Token kinds (admin_credential_tokens.kind).
const (
	KindSetup = "setup" // first password of a newly provisioned account
	KindReset = "reset" // new password for an existing account
)

// Issued is a freshly minted credential token. Token is the plaintext: it
// exists only here and in the link built from it, never in the database.
type Issued struct {
	Kind   string
	UserID int
	// ShopID is the shop the link belongs to (invitations, a new shop's owner
	// setup), 0 for none. It selects the link base (change
	// admin-api-public-exposure, design D9).
	ShopID    int
	Email     string
	Token     string
	ExpiresAt time.Time
}

// NewToken returns a 32-byte crypto/rand token (base64url) and its SHA-256 hex.
func NewToken() (plain, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("account: entropy: %w", err)
	}
	plain = base64.RawURLEncoding.EncodeToString(buf)
	return plain, HashToken(plain), nil
}

// HashToken is the stored form of a token.
func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func (s *Service) ttl(kind string) time.Duration {
	if kind == KindReset {
		return s.ResetTTL
	}
	return s.SetupTTL
}

// issueTx mints a token of the given kind for u inside tx. Older unused tokens
// of the same kind are retired first (used_at set), so a re-issued link never
// leaves two live keys. The user row is locked so two concurrent issues
// serialize instead of both surviving.
func (s *Service) issueTx(ctx context.Context, tx *ent.Tx, u *ent.User, kind string, createdBy *int) (*Issued, error) {
	if _, err := tx.User.Query().Where(user.IDEQ(u.ID)).ForUpdate().Only(ctx); err != nil {
		return nil, fmt.Errorf("account: lock user: %w", err)
	}
	now := time.Now()
	if _, err := tx.AdminCredentialToken.Update().
		Where(
			admincredentialtoken.UserIDEQ(u.ID),
			admincredentialtoken.KindEQ(kind),
			admincredentialtoken.UsedAtIsNil(),
		).
		SetUsedAt(now).
		Save(ctx); err != nil {
		return nil, fmt.Errorf("account: retire old tokens: %w", err)
	}
	plain, hash, err := NewToken()
	if err != nil {
		return nil, err
	}
	expires := now.Add(s.ttl(kind))
	create := tx.AdminCredentialToken.Create().
		SetUserID(u.ID).
		SetKind(kind).
		SetTokenHash(hash).
		SetExpiresAt(expires)
	if createdBy != nil {
		create.SetCreatedBy(*createdBy)
	}
	if _, err := create.Save(ctx); err != nil {
		return nil, fmt.Errorf("account: store token: %w", err)
	}
	return &Issued{Kind: kind, UserID: u.ID, Email: u.Email, Token: plain, ExpiresAt: expires}, nil
}

// consumeTx atomically spends a token: the conditional UPDATE flips used_at
// exactly once, so of any number of concurrent consumers one succeeds.
// Unknown, expired and already-used tokens are indistinguishable
// (ErrTokenInvalid).
//
// kinds restricts what may be spent here: the password-setup endpoint passes
// setup/reset and the invitation endpoints pass invite, so a token minted for
// one purpose can never be spent through the other's endpoint.
func consumeTx(ctx context.Context, tx *ent.Tx, plain string, kinds ...string) (*ent.AdminCredentialToken, error) {
	if plain == "" {
		return nil, ErrTokenInvalid
	}
	hash := HashToken(plain)
	n, err := tx.AdminCredentialToken.Update().
		Where(
			admincredentialtoken.TokenHashEQ(hash),
			admincredentialtoken.KindIn(kinds...),
			admincredentialtoken.UsedAtIsNil(),
			admincredentialtoken.ExpiresAtGT(time.Now()),
		).
		SetUsedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("account: consume token: %w", err)
	}
	if n == 0 {
		return nil, ErrTokenInvalid
	}
	row, err := tx.AdminCredentialToken.Query().Where(admincredentialtoken.TokenHashEQ(hash)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("account: reload token: %w", err)
	}
	return row, nil
}
