// Package account implements back-office account provisioning (change
// platform-account-provisioning): creating accounts with no password, one-time
// password setup/reset links, shop membership and roles, and disabling.
//
// Passwords are only ever set by their owner through a link (design D1); no
// method here accepts a password from an administrator.
package account

import (
	"context"
	"errors"
	"fmt"
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
	"ksdevworks/ecommerce/api/internal/ent/userpermission"
	"ksdevworks/ecommerce/api/internal/rbac"
)

// Password length bounds, in characters.
const (
	MinPasswordLen = 12
	maxPasswordLen = 256
)

// Seeded role names this package relies on.
const (
	SuperAdminRole    = "super_admin"
	MerchantOwnerRole = "merchant_owner"
)

var (
	ErrNotFound       = errors.New("account: not found")
	ErrEmailTaken     = errors.New("account: email already exists")
	ErrSelfDisable    = errors.New("account: cannot disable your own account")
	ErrLastSuperAdmin = errors.New("account: cannot remove the last active super_admin")
	// ErrTokenInvalid covers unknown, expired and already-used tokens alike.
	ErrTokenInvalid = errors.New("account: invalid or expired token")
)

// ValidationError is a 422-shaped input problem.
type ValidationError struct {
	Pointer string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func invalid(pointer, msg string) error { return &ValidationError{Pointer: pointer, Message: msg} }

// Service holds the account operations. Engine is told about every change that
// affects authorization so cached snapshots die immediately.
type Service struct {
	Client   *ent.Client
	Engine   *rbac.Engine
	Refresh  *auth.RefreshService
	Delivery LinkDelivery

	// PublicURL is Config.AdminPublicURL (link base); SetupTTL/ResetTTL bound
	// the one-time links.
	PublicURL string
	SetupTTL  time.Duration
	ResetTTL  time.Duration
}

// Invalidate drops cached authorization snapshots of the given users. Call it
// after the transaction that changed them has committed.
func (s *Service) Invalidate(ctx context.Context, userIDs ...int) {
	s.Engine.InvalidateUsers(ctx, userIDs...)
}

// Deliver turns an issued token into a link and hands it to the configured
// LinkDelivery. Call it after commit.
func (s *Service) Deliver(ctx context.Context, is *Issued) (Delivery, error) {
	return s.Delivery.Deliver(ctx, LinkMessage{
		Kind:      is.Kind,
		Email:     is.Email,
		URL:       SetPasswordURL(s.PublicURL, is.Token),
		ExpiresAt: is.ExpiresAt,
	})
}

// ── create ────────────────────────────────────────────────────────

// CreateInput is the input of CreateAccount. ShopID and RoleID must be given
// together (or not at all).
type CreateInput struct {
	Email     string
	ShopID    *int
	RoleID    *int
	CreatedBy *int
}

// CreateAccount creates a password-less account, optionally makes it a member
// of a shop with a merchant-scope role, and issues its setup token.
func (s *Service) CreateAccount(ctx context.Context, in CreateInput) (*ent.User, Delivery, error) {
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, Delivery{}, fmt.Errorf("account: begin tx: %w", err)
	}
	u, is, err := s.CreateAccountTx(ctx, tx, in)
	if err != nil {
		_ = tx.Rollback()
		return nil, Delivery{}, err
	}
	if err := tx.Commit(); err != nil {
		return nil, Delivery{}, fmt.Errorf("account: commit: %w", err)
	}
	s.Invalidate(ctx, u.ID)
	d, err := s.Deliver(ctx, is)
	if err != nil {
		return nil, Delivery{}, err
	}
	return u, d, nil
}

// CreateAccountTx does CreateAccount's writes on a caller-owned transaction.
// The caller commits, then calls Invalidate and Deliver.
func (s *Service) CreateAccountTx(ctx context.Context, tx *ent.Tx, in CreateInput) (*ent.User, *Issued, error) {
	email, ok := auth.NormalizeEmail(in.Email)
	if !ok || len(email) > 150 {
		return nil, nil, invalid("/email", "must be a valid email address")
	}
	if (in.ShopID == nil) != (in.RoleID == nil) {
		return nil, nil, invalid("/role_id", "shop_id and role_id must be given together")
	}
	taken, err := tx.User.Query().Where(user.EmailEQ(email)).Exist(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("account: check email: %w", err)
	}
	if taken {
		return nil, nil, ErrEmailTaken
	}
	u, err := tx.User.Create().SetEmail(email).SetStatus(1).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, nil, ErrEmailTaken
		}
		return nil, nil, fmt.Errorf("account: create user: %w", err)
	}
	if in.ShopID != nil {
		if err := s.AddMembershipTx(ctx, tx, u.ID, *in.ShopID, *in.RoleID); err != nil {
			return nil, nil, err
		}
	}
	is, err := s.issueTx(ctx, tx, u, KindSetup, in.CreatedBy)
	if err != nil {
		return nil, nil, err
	}
	return u, is, nil
}

// ── membership ────────────────────────────────────────────────────

// AddMembership makes the user a member of the shop and grants the
// merchant-scope role. Existing membership or role are kept (idempotent).
func (s *Service) AddMembership(ctx context.Context, userID, shopID, roleID int) error {
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("account: begin tx: %w", err)
	}
	if err := s.AddMembershipTx(ctx, tx, userID, shopID, roleID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("account: commit: %w", err)
	}
	s.Invalidate(ctx, userID)
	return nil
}

// AddMembershipTx is AddMembership on a caller-owned transaction; the caller
// commits and calls Invalidate. The user row is locked, so concurrent
// membership changes of one user serialize.
func (s *Service) AddMembershipTx(ctx context.Context, tx *ent.Tx, userID, shopID, roleID int) error {
	if _, err := lockUser(ctx, tx, userID); err != nil {
		return err
	}
	rl, err := tx.Role.Get(ctx, roleID)
	if err != nil || rl.Scope != "merchant" {
		return invalid("/role_id", "role must exist and have merchant scope")
	}
	exists, err := tx.Shop.Query().Where(shop.IDEQ(shopID)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("account: check shop: %w", err)
	}
	if !exists {
		return invalid("/shop_id", "unknown shop")
	}
	member, err := tx.ShopUser.Query().Where(shopuser.ShopIDEQ(shopID), shopuser.UserIDEQ(userID)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("account: check membership: %w", err)
	}
	if !member {
		if _, err := tx.ShopUser.Create().SetShopID(shopID).SetUserID(userID).Save(ctx); err != nil {
			return fmt.Errorf("account: add membership: %w", err)
		}
	}
	has, err := tx.RoleUser.Query().
		Where(roleuser.UserIDEQ(userID), roleuser.RoleIDEQ(roleID), roleuser.ShopIDEQ(shopID)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("account: check role: %w", err)
	}
	if !has {
		if _, err := tx.RoleUser.Create().SetUserID(userID).SetRoleID(roleID).SetShopID(shopID).Save(ctx); err != nil {
			return fmt.Errorf("account: assign role: %w", err)
		}
	}
	return nil
}

// RemoveMembership removes the user from the shop together with every role
// assignment and personal permission override scoped to that shop.
func (s *Service) RemoveMembership(ctx context.Context, userID, shopID int) error {
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("account: begin tx: %w", err)
	}
	if err := removeMembershipTx(ctx, tx, userID, shopID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("account: commit: %w", err)
	}
	s.Invalidate(ctx, userID)
	return nil
}

func removeMembershipTx(ctx context.Context, tx *ent.Tx, userID, shopID int) error {
	if _, err := lockUser(ctx, tx, userID); err != nil {
		return err
	}
	if _, err := tx.RoleUser.Delete().
		Where(roleuser.UserIDEQ(userID), roleuser.ShopIDEQ(shopID)).Exec(ctx); err != nil {
		return fmt.Errorf("account: remove roles: %w", err)
	}
	if _, err := tx.UserPermission.Delete().
		Where(userpermission.UserIDEQ(userID), userpermission.ShopIDEQ(shopID)).Exec(ctx); err != nil {
		return fmt.Errorf("account: remove overrides: %w", err)
	}
	n, err := tx.ShopUser.Delete().Where(shopuser.UserIDEQ(userID), shopuser.ShopIDEQ(shopID)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("account: remove membership: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ── status ────────────────────────────────────────────────────────

// Disable sets status 0, revokes every refresh token and clears the
// authorization cache, so the account's existing access tokens are refused at
// the next request (RBAC snapshot, design D5). It refuses to disable the actor
// themself, and the last active super_admin (checked under row locks, so two
// concurrent disables cannot both pass).
func (s *Service) Disable(ctx context.Context, actorID, targetID int) error {
	if actorID == targetID {
		return ErrSelfDisable
	}
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("account: begin tx: %w", err)
	}
	if err := s.disableTx(ctx, tx, targetID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("account: commit: %w", err)
	}
	s.Invalidate(ctx, targetID)
	return nil
}

func (s *Service) disableTx(ctx context.Context, tx *ent.Tx, targetID int) error {
	sa, err := LockSuperAdmins(ctx, tx)
	if err != nil {
		return err
	}
	u, err := lockUser(ctx, tx, targetID)
	if err != nil {
		return err
	}
	if sa.WouldLoseLast(targetID) {
		return ErrLastSuperAdmin
	}
	if err := tx.User.UpdateOneID(u.ID).SetStatus(0).Exec(ctx); err != nil {
		return fmt.Errorf("account: disable: %w", err)
	}
	return s.Refresh.RevokeAllUser(ctx, tx, u.ID)
}

// Enable sets status 1 and clears the authorization cache.
func (s *Service) Enable(ctx context.Context, targetID int) error {
	n, err := s.Client.User.Update().Where(user.IDEQ(targetID)).SetStatus(1).Save(ctx)
	if err != nil {
		return fmt.Errorf("account: enable: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	s.Invalidate(ctx, targetID)
	return nil
}

// ── password links ────────────────────────────────────────────────

// IssueReset mints a reset link for an existing account (any status); older
// unused reset links of that account stop working.
func (s *Service) IssueReset(ctx context.Context, actorID, targetID int) (Delivery, error) {
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return Delivery{}, fmt.Errorf("account: begin tx: %w", err)
	}
	u, err := lockUser(ctx, tx, targetID)
	if err != nil {
		_ = tx.Rollback()
		return Delivery{}, err
	}
	is, err := s.issueTx(ctx, tx, u, KindReset, &actorID)
	if err != nil {
		_ = tx.Rollback()
		return Delivery{}, err
	}
	if err := tx.Commit(); err != nil {
		return Delivery{}, fmt.Errorf("account: commit: %w", err)
	}
	return s.Deliver(ctx, is)
}

// Inspection is what an unauthenticated holder of a link may learn about it.
type Inspection struct {
	Email     string
	Kind      string
	ExpiresAt time.Time
}

// Inspect reports the account a live token belongs to without consuming it.
func (s *Service) Inspect(ctx context.Context, plain string) (*Inspection, error) {
	if plain == "" {
		return nil, ErrTokenInvalid
	}
	row, err := s.Client.AdminCredentialToken.Query().
		Where(
			admincredentialtoken.TokenHashEQ(HashToken(plain)),
			admincredentialtoken.UsedAtIsNil(),
			admincredentialtoken.ExpiresAtGT(time.Now()),
		).
		WithUser().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrTokenInvalid
		}
		return nil, fmt.Errorf("account: inspect token: %w", err)
	}
	return &Inspection{Email: row.Edges.User.Email, Kind: row.Kind, ExpiresAt: row.ExpiresAt}, nil
}

// CompletePasswordSetup spends the token, stores the Argon2id hash of the new
// password and revokes every refresh token of the account — all in one
// transaction. The password length is checked first, so a too-short password
// never burns the link and never reveals whether the token was valid.
func (s *Service) CompletePasswordSetup(ctx context.Context, plainToken, password string) error {
	n := utf8.RuneCountInString(password)
	if n < MinPasswordLen {
		return invalid("/password", fmt.Sprintf("must be at least %d characters", MinPasswordLen))
	}
	if n > maxPasswordLen {
		return invalid("/password", fmt.Sprintf("must be at most %d characters", maxPasswordLen))
	}
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("account: begin tx: %w", err)
	}
	row, err := consumeTx(ctx, tx, plainToken)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.User.UpdateOneID(row.UserID).SetPasswordHash(hash).Exec(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("account: store password: %w", err)
	}
	if err := s.Refresh.RevokeAllUser(ctx, tx, row.UserID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("account: commit: %w", err)
	}
	return nil
}

// ── helpers ───────────────────────────────────────────────────────

// lockUser reads the user row FOR UPDATE.
func lockUser(ctx context.Context, tx *ent.Tx, id int) (*ent.User, error) {
	u, err := tx.User.Query().Where(user.IDEQ(id)).ForUpdate().Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("account: lock user: %w", err)
	}
	return u, nil
}

// SuperAdmins is the set of accounts holding the platform super_admin role,
// read under row locks by LockSuperAdmins.
type SuperAdmins struct {
	active map[int]bool
}

// LockSuperAdmins locks (in id order, so concurrent callers cannot deadlock)
// every user holding the platform super_admin role and returns who is still
// active. A concurrent change that commits first is visible after the lock is
// granted, so the "last super_admin" checks hold under concurrency.
func LockSuperAdmins(ctx context.Context, tx *ent.Tx) (*SuperAdmins, error) {
	ids, err := tx.RoleUser.Query().
		Where(
			roleuser.ShopIDIsNil(),
			roleuser.HasRoleWith(role.NameEQ(SuperAdminRole), role.ScopeEQ("platform")),
		).
		Select(roleuser.FieldUserID).
		Ints(ctx)
	if err != nil {
		return nil, fmt.Errorf("account: list super_admins: %w", err)
	}
	out := &SuperAdmins{active: map[int]bool{}}
	if len(ids) == 0 {
		return out, nil
	}
	users, err := tx.User.Query().Where(user.IDIn(ids...)).Order(user.ByID()).ForUpdate().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("account: lock super_admins: %w", err)
	}
	for _, u := range users {
		if u.Status == 1 {
			out.active[u.ID] = true
		}
	}
	return out, nil
}

// WouldLoseLast reports whether taking super_admin standing away from userID
// (disabling the account, or removing the role) would leave no active
// super_admin.
func (sa *SuperAdmins) WouldLoseLast(userID int) bool {
	return sa.active[userID] && len(sa.active) <= 1
}
