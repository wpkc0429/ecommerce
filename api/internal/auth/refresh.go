package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/member"
	"ksdevworks/ecommerce/api/internal/ent/memberrefreshtoken"
	"ksdevworks/ecommerce/api/internal/ent/user"
	"ksdevworks/ecommerce/api/internal/ent/userrefreshtoken"
)

// Refresh tokens are opaque random values; only their SHA-256 is stored.
// Every use rotates the token (design D9); reuse of a rotated token revokes
// the whole rotation chain (replay detection).

// ErrRefreshInvalid covers unknown/expired/revoked-owner tokens; callers
// answer 401 without detail.
var ErrRefreshInvalid = errors.New("auth: invalid refresh token")

// RefreshService manages both refresh-token families.
type RefreshService struct {
	client *ent.Client
	ttl    time.Duration
	now    func() time.Time
}

func NewRefreshService(client *ent.Client, ttl time.Duration) *RefreshService {
	return &RefreshService{client: client, ttl: ttl, now: time.Now}
}

func newOpaqueToken() (plain, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("auth: entropy: %w", err)
	}
	plain = base64.RawURLEncoding.EncodeToString(buf)
	return plain, hashToken(plain), nil
}

func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// ── back-office users ─────────────────────────────────────────────

// IssueUser creates a fresh refresh token for a user (login).
func (s *RefreshService) IssueUser(ctx context.Context, userID int) (string, error) {
	plain, hash, err := newOpaqueToken()
	if err != nil {
		return "", err
	}
	_, err = s.client.UserRefreshToken.Create().
		SetUserID(userID).
		SetTokenHash(hash).
		SetExpiresAt(s.now().Add(s.ttl)).
		Save(ctx)
	if err != nil {
		return "", fmt.Errorf("auth: store refresh token: %w", err)
	}
	return plain, nil
}

// RotateUser exchanges a valid refresh token for a new one. Reuse of an
// already-rotated token revokes its entire chain and fails.
func (s *RefreshService) RotateUser(ctx context.Context, plain string) (int, string, error) {
	row, err := s.client.UserRefreshToken.Query().
		Where(userrefreshtoken.TokenHashEQ(hashToken(plain))).
		Only(ctx)
	if err != nil {
		return 0, "", ErrRefreshInvalid
	}
	now := s.now()
	if row.RevokedAt != nil {
		// Replay of a rotated/revoked token → kill the whole chain.
		_ = s.revokeUserChain(ctx, row)
		return 0, "", ErrRefreshInvalid
	}
	if now.After(row.ExpiresAt) {
		return 0, "", ErrRefreshInvalid
	}
	// Disabled accounts cannot refresh (spec authentication/Disabled account rejection).
	owner, err := s.client.User.Query().Where(user.IDEQ(row.UserID), user.StatusEQ(1)).Only(ctx)
	if err != nil {
		return 0, "", ErrRefreshInvalid
	}

	newPlain, newHash, err := newOpaqueToken()
	if err != nil {
		return 0, "", err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return 0, "", err
	}
	// Guard against concurrent rotation of the same token: only one rotation
	// may flip revoked_at from NULL.
	n, err := tx.UserRefreshToken.Update().
		Where(userrefreshtoken.IDEQ(row.ID), userrefreshtoken.RevokedAtIsNil()).
		SetRevokedAt(now).
		Save(ctx)
	if err == nil && n == 0 {
		err = ErrRefreshInvalid
	}
	if err == nil {
		_, err = tx.UserRefreshToken.Create().
			SetUserID(owner.ID).
			SetTokenHash(newHash).
			SetExpiresAt(now.Add(s.ttl)).
			SetRotatedFrom(row.ID).
			Save(ctx)
	}
	if err != nil {
		_ = tx.Rollback()
		return 0, "", err
	}
	if err := tx.Commit(); err != nil {
		return 0, "", err
	}
	return owner.ID, newPlain, nil
}

// RevokeUser revokes the presented token (logout). Unknown tokens are a no-op.
func (s *RefreshService) RevokeUser(ctx context.Context, plain string) error {
	_, err := s.client.UserRefreshToken.Update().
		Where(userrefreshtoken.TokenHashEQ(hashToken(plain)), userrefreshtoken.RevokedAtIsNil()).
		SetRevokedAt(s.now()).
		Save(ctx)
	return err
}

// revokeUserChain revokes every token linked to row through rotated_from
// lineage (ancestors and descendants).
func (s *RefreshService) revokeUserChain(ctx context.Context, row *ent.UserRefreshToken) error {
	ids := map[int]bool{row.ID: true}
	// Ancestors.
	cur := row
	for cur.RotatedFrom != nil {
		parent, err := s.client.UserRefreshToken.Get(ctx, *cur.RotatedFrom)
		if err != nil || ids[parent.ID] {
			break
		}
		ids[parent.ID] = true
		cur = parent
	}
	// Descendants (breadth-first).
	frontier := keys(ids)
	for len(frontier) > 0 {
		children, err := s.client.UserRefreshToken.Query().
			Where(userrefreshtoken.RotatedFromIn(frontier...)).
			All(ctx)
		if err != nil {
			return err
		}
		frontier = frontier[:0]
		for _, c := range children {
			if !ids[c.ID] {
				ids[c.ID] = true
				frontier = append(frontier, c.ID)
			}
		}
	}
	_, err := s.client.UserRefreshToken.Update().
		Where(userrefreshtoken.IDIn(keys(ids)...), userrefreshtoken.RevokedAtIsNil()).
		SetRevokedAt(s.now()).
		Save(ctx)
	return err
}

// ── storefront members (shop-bound) ───────────────────────────────

// IssueMember creates a refresh token bound to (member, shop).
func (s *RefreshService) IssueMember(ctx context.Context, memberID, shopID int) (string, error) {
	plain, hash, err := newOpaqueToken()
	if err != nil {
		return "", err
	}
	_, err = s.client.MemberRefreshToken.Create().
		SetMemberID(memberID).
		SetShopID(shopID).
		SetTokenHash(hash).
		SetExpiresAt(s.now().Add(s.ttl)).
		Save(ctx)
	if err != nil {
		return "", fmt.Errorf("auth: store member refresh token: %w", err)
	}
	return plain, nil
}

// RotateMember rotates a member refresh token within the shop context it was
// issued for; presenting it under another shop fails.
func (s *RefreshService) RotateMember(ctx context.Context, plain string, shopID int) (int, string, error) {
	row, err := s.client.MemberRefreshToken.Query().
		Where(memberrefreshtoken.TokenHashEQ(hashToken(plain))).
		Only(ctx)
	if err != nil || row.ShopID != shopID {
		return 0, "", ErrRefreshInvalid
	}
	now := s.now()
	if row.RevokedAt != nil {
		_ = s.revokeMemberChain(ctx, row)
		return 0, "", ErrRefreshInvalid
	}
	if now.After(row.ExpiresAt) {
		return 0, "", ErrRefreshInvalid
	}
	owner, err := s.client.Member.Query().Where(member.IDEQ(row.MemberID), member.StatusEQ(1)).Only(ctx)
	if err != nil {
		return 0, "", ErrRefreshInvalid
	}

	newPlain, newHash, err := newOpaqueToken()
	if err != nil {
		return 0, "", err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return 0, "", err
	}
	n, err := tx.MemberRefreshToken.Update().
		Where(memberrefreshtoken.IDEQ(row.ID), memberrefreshtoken.RevokedAtIsNil()).
		SetRevokedAt(now).
		Save(ctx)
	if err == nil && n == 0 {
		err = ErrRefreshInvalid
	}
	if err == nil {
		_, err = tx.MemberRefreshToken.Create().
			SetMemberID(owner.ID).
			SetShopID(shopID).
			SetTokenHash(newHash).
			SetExpiresAt(now.Add(s.ttl)).
			SetRotatedFrom(row.ID).
			Save(ctx)
	}
	if err != nil {
		_ = tx.Rollback()
		return 0, "", err
	}
	if err := tx.Commit(); err != nil {
		return 0, "", err
	}
	return owner.ID, newPlain, nil
}

// RevokeMember revokes the presented member token (logout).
func (s *RefreshService) RevokeMember(ctx context.Context, plain string, shopID int) error {
	_, err := s.client.MemberRefreshToken.Update().
		Where(
			memberrefreshtoken.TokenHashEQ(hashToken(plain)),
			memberrefreshtoken.ShopIDEQ(shopID),
			memberrefreshtoken.RevokedAtIsNil(),
		).
		SetRevokedAt(s.now()).
		Save(ctx)
	return err
}

func (s *RefreshService) revokeMemberChain(ctx context.Context, row *ent.MemberRefreshToken) error {
	ids := map[int]bool{row.ID: true}
	cur := row
	for cur.RotatedFrom != nil {
		parent, err := s.client.MemberRefreshToken.Get(ctx, *cur.RotatedFrom)
		if err != nil || ids[parent.ID] {
			break
		}
		ids[parent.ID] = true
		cur = parent
	}
	frontier := keys(ids)
	for len(frontier) > 0 {
		children, err := s.client.MemberRefreshToken.Query().
			Where(memberrefreshtoken.RotatedFromIn(frontier...)).
			All(ctx)
		if err != nil {
			return err
		}
		frontier = frontier[:0]
		for _, c := range children {
			if !ids[c.ID] {
				ids[c.ID] = true
				frontier = append(frontier, c.ID)
			}
		}
	}
	_, err := s.client.MemberRefreshToken.Update().
		Where(memberrefreshtoken.IDIn(keys(ids)...), memberrefreshtoken.RevokedAtIsNil()).
		SetRevokedAt(s.now()).
		Save(ctx)
	return err
}

func keys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
