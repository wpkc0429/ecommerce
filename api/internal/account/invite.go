package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/admincredentialtoken"
	"ksdevworks/ecommerce/api/internal/ent/role"
	"ksdevworks/ecommerce/api/internal/ent/roleuser"
	"ksdevworks/ecommerce/api/internal/ent/shop"
	"ksdevworks/ecommerce/api/internal/ent/shopuser"
	"ksdevworks/ecommerce/api/internal/ent/user"
	"ksdevworks/ecommerce/api/internal/rbac"
)

// Shop invitations (change merchant-staff-role, design D5-D7).
//
// An invitation is an admin_credential_tokens row of kind "invite". Inviting
// never creates an account, and accepting never sets a password for an account
// that already exists: a manual-delivery link is held by the inviter, so a
// link that could set the password of an existing (even half-provisioned)
// account would let any owner take it over.

// KindInvite is the credential token kind of a shop invitation.
const KindInvite = "invite"

var (
	// ErrAlreadyMember: the email already has a membership (active or
	// disabled) in this shop.
	ErrAlreadyMember = errors.New("account: already a member of this shop")
	// ErrAccountExists: the unauthenticated accept path refuses an email that
	// has any back-office account; that person must sign in first.
	ErrAccountExists = errors.New("account: an account with this email already exists; sign in first")
	// ErrNotDelegable: a merchant-level caller touched something only the
	// platform may (a non-assignable role, or a member holding one).
	ErrNotDelegable = errors.New("account: not delegable by a merchant-level caller")
	// ErrWrongAccount: an authenticated accept by someone the invitation was
	// not issued to.
	ErrWrongAccount = errors.New("account: this invitation was issued to a different email")
)

// InviteURL builds {base}/#/accept-invite/{token}; the token sits in the
// fragment for the same reason as the password-setup link.
func InviteURL(base, token string) string {
	return base + "/#/accept-invite/" + token
}

// IsPlatformCaller reports whether the user holds a platform-scope role
// (design D4: platform callers are not bound by role assignability).
func IsPlatformCaller(ctx context.Context, engine *rbac.Engine, userID int) (bool, error) {
	snap, err := engine.Snapshot(ctx, userID, rbac.PlatformCtx)
	if err != nil {
		return false, fmt.Errorf("account: platform snapshot: %w", err)
	}
	return snap.IsPlatformAdmin(), nil
}

// HoldsNonAssignableRole reports whether the user holds, in this shop, any
// role with merchant_assignable = false (for example another owner). A
// merchant-level caller may not manage such a member (design D4/D8).
func HoldsNonAssignableRole(ctx context.Context, client *ent.Client, shopID, userID int) (bool, error) {
	n, err := client.RoleUser.Query().
		Where(
			roleuser.UserIDEQ(userID), roleuser.ShopIDEQ(shopID),
			roleuser.HasRoleWith(role.MerchantAssignableEQ(false)),
		).
		Count(ctx)
	if err != nil {
		return false, fmt.Errorf("account: check delegable roles: %w", err)
	}
	return n > 0, nil
}

// InviteInput is the input of Invite. Platform callers may invite to any
// merchant-scope role.
type InviteInput struct {
	ShopID     int
	RoleID     int
	Email      string
	CreatedBy  int
	ByPlatform bool
}

// Invited is a created invitation.
type Invited struct {
	ID       int
	Email    string
	Delivery Delivery
}

// Invite issues a shop invitation: valid for InviteTTL (7 days), one live
// invitation per (shop, email) — a new one retires the old. It creates no user
// row.
func (s *Service) Invite(ctx context.Context, in InviteInput) (*Invited, error) {
	email, ok := auth.NormalizeEmail(in.Email)
	if !ok || len(email) > 150 {
		return nil, invalid("/email", "must be a valid email address")
	}
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("account: begin tx: %w", err)
	}
	inv, is, err := s.inviteTx(ctx, tx, in, email)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("account: commit: %w", err)
	}
	d, err := s.Deliver(ctx, is)
	if err != nil {
		return nil, err
	}
	inv.Delivery = d
	return inv, nil
}

func (s *Service) inviteTx(ctx context.Context, tx *ent.Tx, in InviteInput, email string) (*Invited, *Issued, error) {
	rl, err := tx.Role.Get(ctx, in.RoleID)
	if err != nil || rl.Scope != "merchant" {
		return nil, nil, invalid("/role_id", "role must exist and have merchant scope")
	}
	if !in.ByPlatform && !rl.MerchantAssignable {
		return nil, nil, invalid("/role_id", "this role cannot be granted by a shop")
	}
	// Serialize concurrent invitations of one shop (and prove the shop exists).
	if _, err := tx.Shop.Query().Where(shop.IDEQ(in.ShopID)).ForUpdate().Only(ctx); err != nil {
		if ent.IsNotFound(err) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("account: lock shop: %w", err)
	}
	member, err := tx.ShopUser.Query().
		Where(shopuser.ShopIDEQ(in.ShopID), shopuser.HasUserWith(user.EmailEQ(email))).
		Exist(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("account: check membership: %w", err)
	}
	if member {
		return nil, nil, ErrAlreadyMember
	}
	now := time.Now()
	if _, err := tx.AdminCredentialToken.Update().
		Where(
			admincredentialtoken.KindEQ(KindInvite),
			admincredentialtoken.ShopIDEQ(in.ShopID),
			admincredentialtoken.EmailEQ(email),
			admincredentialtoken.UsedAtIsNil(),
		).
		SetUsedAt(now).
		Save(ctx); err != nil {
		return nil, nil, fmt.Errorf("account: supersede invitations: %w", err)
	}
	plain, hash, err := NewToken()
	if err != nil {
		return nil, nil, err
	}
	expires := now.Add(s.InviteTTL)
	row, err := tx.AdminCredentialToken.Create().
		SetKind(KindInvite).
		SetTokenHash(hash).
		SetEmail(email).
		SetShopID(in.ShopID).
		SetRoleID(in.RoleID).
		SetCreatedBy(in.CreatedBy).
		SetExpiresAt(expires).
		Save(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("account: store invitation: %w", err)
	}
	return &Invited{ID: row.ID, Email: email}, &Issued{Kind: KindInvite, ShopID: in.ShopID, Email: email, Token: plain, ExpiresAt: expires}, nil
}

// Invitation statuses reported by ListInvitations (change
// order-and-invitation-polish, design D2). Accepted, revoked and superseded
// invitations all carry used_at and are not listed at all.
const (
	InvitationPending = "pending"
	InvitationExpired = "expired"
)

// Invitation is an outstanding invitation as listed to the shop's owner. It
// never carries the token or the link.
type Invitation struct {
	ID        int
	Email     string
	RoleID    int
	RoleName  string
	Status    string // InvitationPending or InvitationExpired
	ExpiresAt time.Time
	CreatedAt time.Time
}

// ListInvitations returns the shop's unused, unrevoked invitations — pending
// and expired — newest first. Expired ones stay listed so the owner can see
// and revoke them (RevokeInvitation accepts any unused invitation).
func (s *Service) ListInvitations(ctx context.Context, shopID int) ([]Invitation, error) {
	now := time.Now()
	rows, err := s.Client.AdminCredentialToken.Query().
		Where(
			admincredentialtoken.KindEQ(KindInvite),
			admincredentialtoken.ShopIDEQ(shopID),
			admincredentialtoken.UsedAtIsNil(),
		).
		WithRole().
		Order(ent.Desc(admincredentialtoken.FieldCreatedAt), ent.Desc(admincredentialtoken.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("account: list invitations: %w", err)
	}
	out := make([]Invitation, 0, len(rows))
	for _, r := range rows {
		inv := Invitation{ID: r.ID, Status: InvitationPending, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt}
		if !r.ExpiresAt.After(now) {
			inv.Status = InvitationExpired
		}
		if r.Email != nil {
			inv.Email = *r.Email
		}
		if r.RoleID != nil {
			inv.RoleID = *r.RoleID
		}
		if r.Edges.Role != nil {
			inv.RoleName = r.Edges.Role.Name
		}
		out = append(out, inv)
	}
	return out, nil
}

// RevokeInvitation retires an unused invitation of this shop, pending or
// expired; its link is invalid afterwards and it leaves the list. ErrNotFound if
// it is already used or revoked, or belongs to another shop.
func (s *Service) RevokeInvitation(ctx context.Context, shopID, id int) error {
	n, err := s.Client.AdminCredentialToken.Update().
		Where(
			admincredentialtoken.IDEQ(id),
			admincredentialtoken.KindEQ(KindInvite),
			admincredentialtoken.ShopIDEQ(shopID),
			admincredentialtoken.UsedAtIsNil(),
		).
		SetUsedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("account: revoke invitation: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// InvitationInspection is what the holder of an invitation link may learn.
type InvitationInspection struct {
	ShopName      string
	RoleName      string
	Email         string
	RequiresLogin bool
	ExpiresAt     time.Time
}

// InspectInvitation reports a live invitation without consuming it.
// RequiresLogin is true when the email already has a back-office account — the
// page must branch on it, so (design D6) the link holder learns that bit.
func (s *Service) InspectInvitation(ctx context.Context, plain string) (*InvitationInspection, error) {
	if plain == "" {
		return nil, ErrTokenInvalid
	}
	row, err := s.Client.AdminCredentialToken.Query().
		Where(
			admincredentialtoken.TokenHashEQ(HashToken(plain)),
			admincredentialtoken.KindEQ(KindInvite),
			admincredentialtoken.UsedAtIsNil(),
			admincredentialtoken.ExpiresAtGT(time.Now()),
		).
		WithRole().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrTokenInvalid
		}
		return nil, fmt.Errorf("account: inspect invitation: %w", err)
	}
	if row.Email == nil || row.ShopID == nil || row.Edges.Role == nil {
		return nil, ErrTokenInvalid
	}
	sh, err := s.Client.Shop.Get(ctx, *row.ShopID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrTokenInvalid
		}
		return nil, fmt.Errorf("account: inspect shop: %w", err)
	}
	exists, err := s.Client.User.Query().Where(user.EmailEQ(*row.Email)).Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("account: inspect account: %w", err)
	}
	return &InvitationInspection{
		ShopName: sh.Name, RoleName: row.Edges.Role.Name, Email: *row.Email,
		RequiresLogin: exists, ExpiresAt: row.ExpiresAt,
	}, nil
}

// Accepted is the outcome of an accepted invitation.
type Accepted struct {
	ShopID int
	UserID int
}

// AcceptAnonymous accepts an invitation with a new password. It succeeds only
// when the invited email has NO back-office account (any user row, with or
// without a password, makes it ErrAccountExists and changes nothing): the
// account, the membership and the role are created in one transaction together
// with the token consumption. The password length is checked first so a too
// short password never burns the link.
func (s *Service) AcceptAnonymous(ctx context.Context, plain, password string) (*Accepted, error) {
	n := utf8.RuneCountInString(password)
	if n < MinPasswordLen {
		return nil, invalid("/password", fmt.Sprintf("must be at least %d characters", MinPasswordLen))
	}
	if n > maxPasswordLen {
		return nil, invalid("/password", fmt.Sprintf("must be at most %d characters", maxPasswordLen))
	}
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("account: begin tx: %w", err)
	}
	row, err := consumeTx(ctx, tx, plain, KindInvite)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if row.Email == nil || row.ShopID == nil || row.RoleID == nil {
		_ = tx.Rollback()
		return nil, ErrTokenInvalid
	}
	taken, err := tx.User.Query().Where(user.EmailEQ(*row.Email)).Exist(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("account: check email: %w", err)
	}
	if taken {
		_ = tx.Rollback() // also un-consumes the token
		return nil, ErrAccountExists
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	u, err := tx.User.Create().SetEmail(*row.Email).SetPasswordHash(hash).SetStatus(1).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		if ent.IsConstraintError(err) {
			return nil, ErrAccountExists
		}
		return nil, fmt.Errorf("account: create user: %w", err)
	}
	if err := s.AddMembershipTx(ctx, tx, u.ID, *row.ShopID, *row.RoleID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("account: commit: %w", err)
	}
	s.Invalidate(ctx, u.ID)
	return &Accepted{ShopID: *row.ShopID, UserID: u.ID}, nil
}

// AcceptAuthenticated accepts an invitation as the signed-in account. The
// invitation's email must equal the caller's (ErrWrongAccount otherwise, and
// the invitation stays usable by its rightful owner). The caller's password is
// not touched.
func (s *Service) AcceptAuthenticated(ctx context.Context, userID int, plain string) (*Accepted, error) {
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("account: begin tx: %w", err)
	}
	u, err := lockUser(ctx, tx, userID)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	row, err := consumeTx(ctx, tx, plain, KindInvite)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if row.Email == nil || row.ShopID == nil || row.RoleID == nil {
		_ = tx.Rollback()
		return nil, ErrTokenInvalid
	}
	if u.Status != 1 || !strings.EqualFold(*row.Email, u.Email) {
		_ = tx.Rollback()
		return nil, ErrWrongAccount
	}
	member, err := tx.ShopUser.Query().
		Where(shopuser.ShopIDEQ(*row.ShopID), shopuser.UserIDEQ(u.ID)).Exist(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("account: check membership: %w", err)
	}
	if member {
		_ = tx.Rollback()
		return nil, ErrAlreadyMember
	}
	if err := s.AddMembershipTx(ctx, tx, u.ID, *row.ShopID, *row.RoleID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("account: commit: %w", err)
	}
	s.Invalidate(ctx, u.ID)
	return &Accepted{ShopID: *row.ShopID, UserID: u.ID}, nil
}
