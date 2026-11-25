package design

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"net"
	"net/netip"
	"strings"
	"time"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/designservicecredential"
	"ksdevworks/ecommerce/api/internal/ent/shop"
)

// Credential lifetimes (design D8).
const (
	KeyPrefix            = "dsk_"
	DefaultCredentialTTL = 90 * 24 * time.Hour
	MaxCredentialTTL     = 365 * 24 * time.Hour // the table CHECK allows 366 days
	RotationOverlap      = 7 * 24 * time.Hour
	lastUsedThrottle     = time.Minute
)

// Credentials issues, authenticates, rotates and revokes service credentials.
// A credential is bound to one shop, cannot approve anything and is not a user.
type Credentials struct {
	Client *ent.Client
	Now    func() time.Time
}

func (c *Credentials) now() time.Time {
	if c.Now != nil {
		return Micros(c.Now())
	}
	return Micros(time.Now())
}

// HashKey is the SHA-256 (hex) under which a key is stored and looked up.
func HashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// IssueInput describes a credential to issue.
type IssueInput struct {
	ShopID      int
	Name        string
	CIDRs       []string
	TTL         time.Duration
	CreatedByID int
}

// Issued is the one moment the plaintext key exists.
type Issued struct {
	Credential *ent.DesignServiceCredential
	Key        string
}

// ParseCIDRs validates a CIDR allow-list.
func ParseCIDRs(in []string) ([]string, error) {
	if len(in) > 20 {
		return nil, ErrInvalid("at most 20 CIDR ranges")
	}
	out := make([]string, 0, len(in))
	for _, c := range in {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			return nil, ErrInvalid("allowed_cidrs must be CIDR ranges such as 203.0.113.0/24")
		}
		out = append(out, p.Masked().String())
	}
	return out, nil
}

// Issue creates a credential and returns its key, which is never stored.
func (c *Credentials) Issue(ctx context.Context, in IssueInput) (*Issued, error) {
	name := strings.TrimSpace(SanitizeText(in.Name))
	if name == "" || len([]rune(name)) > 100 {
		return nil, ErrInvalid("name is required (at most 100 characters)")
	}
	ttl := in.TTL
	if ttl == 0 {
		ttl = DefaultCredentialTTL
	}
	if ttl < time.Hour || ttl > MaxCredentialTTL {
		return nil, ErrInvalid("expiry must be between 1 hour and 365 days")
	}
	cidrs, err := ParseCIDRs(in.CIDRs)
	if err != nil {
		return nil, err
	}
	ok, err := c.Client.Shop.Query().Where(shop.IDEQ(in.ShopID)).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInvalid("unknown shop")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	key := KeyPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
	now := c.now()
	row, err := c.Client.DesignServiceCredential.Create().
		SetShopID(in.ShopID).SetName(name).SetKeyPrefix(key[:8]).SetKeyHash(HashKey(key)).
		SetAllowedCidrs(cidrs).SetCreatedByUserID(in.CreatedByID).SetCreatedAt(now).SetExpiresAt(now.Add(ttl)).Save(ctx)
	if err != nil {
		return nil, err
	}
	return &Issued{Credential: row, Key: key}, nil
}

// List returns a shop's credentials (never the key).
func (c *Credentials) List(ctx context.Context, shopID int) ([]*ent.DesignServiceCredential, error) {
	return c.Client.DesignServiceCredential.Query().
		Where(designservicecredential.ShopIDEQ(shopID)).Order(ent.Desc(designservicecredential.FieldID)).All(ctx)
}

// Revoke takes effect on the very next request: authentication reads the row
// every time and caches nothing.
func (c *Credentials) Revoke(ctx context.Context, id int) (*ent.DesignServiceCredential, error) {
	n, err := c.Client.DesignServiceCredential.Update().
		Where(designservicecredential.IDEQ(id), designservicecredential.RevokedAtIsNil()).SetRevokedAt(c.now()).Save(ctx)
	if err != nil {
		return nil, err
	}
	row, qerr := c.Client.DesignServiceCredential.Get(ctx, id)
	if ent.IsNotFound(qerr) {
		return nil, ErrNotFound()
	}
	if qerr != nil {
		return nil, qerr
	}
	if n == 0 {
		return nil, conflict("already_revoked", "this credential is already revoked")
	}
	return row, nil
}

// Rotate issues a replacement with the same name, shop and CIDRs and shortens
// the old credential to the 7-day overlap so the service can switch over.
func (c *Credentials) Rotate(ctx context.Context, id int, createdBy int) (*Issued, error) {
	old, err := c.Client.DesignServiceCredential.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound()
	}
	if err != nil {
		return nil, err
	}
	if old.RevokedAt != nil {
		return nil, conflict("already_revoked", "a revoked credential cannot be rotated")
	}
	issued, err := c.Issue(ctx, IssueInput{ShopID: old.ShopID, Name: old.Name, CIDRs: old.AllowedCidrs, CreatedByID: createdBy})
	if err != nil {
		return nil, err
	}
	end := c.now().Add(RotationOverlap)
	if end.Before(old.ExpiresAt) {
		if _, err := c.Client.DesignServiceCredential.UpdateOneID(old.ID).SetExpiresAt(end).Save(ctx); err != nil {
			return nil, err
		}
	}
	return issued, nil
}

// Authenticate resolves a presented key. Every failure is the same
// (ok=false): unknown, expired, revoked and out-of-range callers learn nothing.
func (c *Credentials) Authenticate(ctx context.Context, key string, clientIP netip.Addr) (*ent.DesignServiceCredential, bool, error) {
	if !strings.HasPrefix(key, KeyPrefix) || len(key) > 128 {
		return nil, false, nil
	}
	row, err := c.Client.DesignServiceCredential.Query().Where(designservicecredential.KeyHashEQ(HashKey(key))).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	now := c.now()
	if row.RevokedAt != nil || !now.Before(row.ExpiresAt) {
		return nil, false, nil
	}
	if len(row.AllowedCidrs) > 0 && !cidrAllows(row.AllowedCidrs, clientIP) {
		return nil, false, nil
	}
	// last_used_at is written at most once a minute, not on every request.
	if row.LastUsedAt == nil || now.Sub(*row.LastUsedAt) >= lastUsedThrottle {
		_, _ = c.Client.DesignServiceCredential.Update().
			Where(designservicecredential.IDEQ(row.ID),
				designservicecredential.Or(designservicecredential.LastUsedAtIsNil(), designservicecredential.LastUsedAtLT(now.Add(-lastUsedThrottle)))).
			SetLastUsedAt(now).Save(ctx)
	}
	return row, true, nil
}

func cidrAllows(cidrs []string, ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	for _, c := range cidrs {
		if p, err := netip.ParsePrefix(c); err == nil && p.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientAddr parses an address string such as "203.0.113.5" or "203.0.113.5:44321".
func ClientAddr(s string) netip.Addr {
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	a, _ := netip.ParseAddr(s)
	return a
}
