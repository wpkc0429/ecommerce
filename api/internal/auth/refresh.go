package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
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
// already-rotated token revokes its entire chain and fails. A read-only
// preview token is refused here: it can never become a normal session.
func (s *RefreshService) RotateUser(ctx context.Context, plain string) (int, string, error) {
	if IsPreviewRefresh(plain) {
		return 0, "", ErrRefreshInvalid
	}
	uid, newPlain, _, err := s.rotateUser(ctx, plain, "")
	return uid, newPlain, err
}

// RotatePreviewUser rotates a read-only preview refresh token. The token must
// carry the preview prefix AND the tag of the caller's preview origin; the new
// token keeps the prefix and tag and the SAME absolute expiry (a preview
// session is never extended by refreshing). It returns the session end.
func (s *RefreshService) RotatePreviewUser(ctx context.Context, plain, originTag string) (int, string, time.Time, error) {
	if !IsPreviewRefresh(plain) || PreviewRefreshTag(plain) != originTag {
		return 0, "", time.Time{}, ErrRefreshInvalid
	}
	return s.rotateUser(ctx, plain, originTag)
}

// rotateUser rotates one token. previewTag "" = normal session.
func (s *RefreshService) rotateUser(ctx context.Context, plain, previewTag string) (int, string, time.Time, error) {
	row, err := s.client.UserRefreshToken.Query().
		Where(userrefreshtoken.TokenHashEQ(hashToken(plain))).
		Only(ctx)
	if err != nil {
		return 0, "", time.Time{}, ErrRefreshInvalid
	}
	now := s.now()
	if row.RevokedAt != nil {
		// Replay of a rotated/revoked token → kill the whole chain.
		_ = s.revokeUserChain(ctx, row)
		return 0, "", time.Time{}, ErrRefreshInvalid
	}
	if now.After(row.ExpiresAt) {
		return 0, "", time.Time{}, ErrRefreshInvalid
	}
	// Disabled accounts cannot refresh (spec authentication/Disabled account rejection).
	owner, err := s.client.User.Query().Where(user.IDEQ(row.UserID), user.StatusEQ(1)).Only(ctx)
	if err != nil {
		return 0, "", time.Time{}, ErrRefreshInvalid
	}

	newPlain, newHash, err := newOpaqueToken()
	if err != nil {
		return 0, "", time.Time{}, err
	}
	expires := now.Add(s.ttl)
	if previewTag != "" {
		newPlain, newHash = previewPrefix(previewTag)+newPlain, hashToken(previewPrefix(previewTag)+newPlain)
		expires = row.ExpiresAt // absolute session end: never extended
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return 0, "", time.Time{}, err
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
			SetExpiresAt(expires).
			SetRotatedFrom(row.ID).
			Save(ctx)
	}
	if err != nil {
		_ = tx.Rollback()
		return 0, "", time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return 0, "", time.Time{}, err
	}
	return owner.ID, newPlain, expires, nil
}

// ── read-only preview refresh tokens ──────────────────────────────

const previewRefreshMarker = "pv1."

func previewPrefix(tag string) string { return previewRefreshMarker + tag + "." }

// IsPreviewRefresh reports whether a refresh token is a read-only preview one.
func IsPreviewRefresh(plain string) bool { return strings.HasPrefix(plain, previewRefreshMarker) }

// PreviewRefreshTag extracts the origin tag a preview refresh token is bound to.
func PreviewRefreshTag(plain string) string {
	rest, ok := strings.CutPrefix(plain, previewRefreshMarker)
	if !ok {
		return ""
	}
	tag, _, _ := strings.Cut(rest, ".")
	return tag
}

// OriginTag is the short, non-reversible tag of a canonical preview origin
// that is embedded in preview refresh tokens and written to audit logs (the
// origin itself is never logged).
func OriginTag(origin string) string {
	sum := sha256.Sum256([]byte("preview-origin-v1:" + origin))
	return hex.EncodeToString(sum[:8])
}

// IssuePreviewUser creates a read-only preview refresh token bound to the
// origin tag, expiring after ttl (the whole session lifetime).
func (s *RefreshService) IssuePreviewUser(ctx context.Context, userID int, originTag string, ttl time.Duration) (string, time.Time, error) {
	rnd, _, err := newOpaqueToken()
	if err != nil {
		return "", time.Time{}, err
	}
	plain := previewPrefix(originTag) + rnd
	expires := s.now().Add(ttl)
	if _, err := s.client.UserRefreshToken.Create().
		SetUserID(userID).
		SetTokenHash(hashToken(plain)).
		SetExpiresAt(expires).
		Save(ctx); err != nil {
		return "", time.Time{}, fmt.Errorf("auth: store preview refresh token: %w", err)
	}
	return plain, expires, nil
}


// RevokeUser revokes the presented token (logout). Unknown tokens are a no-op.
func (s *RefreshService) RevokeUser(ctx context.Context, plain string) error {
	_, err := s.client.UserRefreshToken.Update().
		Where(userrefreshtoken.TokenHashEQ(hashToken(plain)), userrefreshtoken.RevokedAtIsNil()).
		SetRevokedAt(s.now()).
		Save(ctx)
	return err
}

// RevokeAllUser revokes every live refresh token of a user inside tx — the
// "kick all sessions" step of disabling an account and of completing a
// password setup/reset (change platform-account-provisioning, design D2/D5).
// Already-revoked rows are left alone, so calling it twice is harmless.
func (s *RefreshService) RevokeAllUser(ctx context.Context, tx *ent.Tx, userID int) error {
	_, err := tx.UserRefreshToken.Update().
		Where(userrefreshtoken.UserIDEQ(userID), userrefreshtoken.RevokedAtIsNil()).
		SetRevokedAt(s.now()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("auth: revoke all refresh tokens: %w", err)
	}
	return nil
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
	// shop_id is explicit rather than left to the tenant interceptor (change
	// member-identity-per-shop design D4): a token whose member belongs to a
	// different shop can never be rotated here, whatever context this runs
	// in. row.ShopID was already matched against shopID above.
	owner, err := s.client.Member.Query().
		Where(member.IDEQ(row.MemberID), member.ShopIDEQ(shopID), member.StatusEQ(1)).
		Only(ctx)
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
