// Package provision is the one implementation of "open a new shop" (change
// platform-account-provisioning, design D8): shop + home page, owner account
// and storefront origins, all in one transaction. The
// HTTP wizard calls it today; the future "shop application approved" flow calls
// the same method.
package provision

import (
	"context"
	"errors"
	"fmt"

	"ksdevworks/ecommerce/api/internal/account"
	"ksdevworks/ecommerce/api/internal/cms"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/role"
	"ksdevworks/ecommerce/api/internal/ent/user"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// ErrConflict marks a uniqueness violation (origin, owner e-mail).
var ErrConflict = errors.New("provision: conflict")

// ConflictError carries which input collided.
type ConflictError struct {
	Pointer string
	Message string
}

func (e *ConflictError) Error() string { return e.Message }
func (e *ConflictError) Unwrap() error { return ErrConflict }

// Service provisions shops.
type Service struct {
	Client     *ent.Client
	CMS        *cms.Service
	Accounts   *account.Service
	Dispatcher *events.Dispatcher
}

// Input is a shop provisioning request. OwnerEmail is required.
type Input struct {
	Name              string
	Status            *int16
	StorefrontOrigins []string
	OwnerEmail        string
	ActorID           int
}

// Result is the outcome of ProvisionShop.
type Result struct {
	Shop       *ent.Shop
	Origins    []string
	OwnerID    int
	OwnerEmail string
	// Created reports whether the owner account was created by this call.
	Created bool
	// Delivery is set only when Created.
	Delivery *account.Delivery
}

// ProvisionShop does everything in one transaction; any failure rolls back
// the whole thing, so there is never a shop without an owner. It is
// ProvisionShopTx + commit + AfterCommit; callers that must make other writes
// atomic with the shop (shop application approval, change platform-website
// D9) call those pieces themselves.
func (s *Service) ProvisionShop(ctx context.Context, in Input) (*Result, error) {
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("provision: begin tx: %w", err)
	}
	res, issued, err := s.ProvisionShopTx(ctx, tx, in)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("provision: commit: %w", err)
	}
	if err := s.AfterCommit(ctx, res, issued); err != nil {
		return nil, err
	}
	return res, nil
}

// ProvisionShopTx creates the shop, owner and origins inside the caller's
// transaction. The caller commits (or rolls back) and then MUST call
// AfterCommit with the returned values; nothing observable happens before that.
func (s *Service) ProvisionShopTx(ctx context.Context, tx *ent.Tx, in Input) (*Result, *account.Issued, error) {
	origins := make([]string, 0, len(in.StorefrontOrigins))
	for i, raw := range in.StorefrontOrigins {
		o := tenant.NormalizeOrigin(raw)
		if o == "" {
			return nil, nil, &account.ValidationError{
				Pointer: fmt.Sprintf("/storefront_origins/%d", i),
				Message: "must be a scheme://host[:port] http(s) origin without path or wildcard",
			}
		}
		origins = append(origins, o)
	}
	return s.provisionTx(ctx, tx, in, origins)
}

// AfterCommit runs the effects that must only happen once the transaction is
// durable: cache invalidation through events, then link delivery (which sets
// res.Delivery when a new owner account was created).
func (s *Service) AfterCommit(ctx context.Context, res *Result, issued *account.Issued) error {
	s.Accounts.Invalidate(ctx, res.OwnerID)
	if len(res.Origins) > 0 {
		s.Dispatcher.Publish(ctx, events.StorefrontClientChanged{ShopID: res.Shop.ID, Origins: res.Origins})
	}
	if issued != nil {
		d, err := s.Accounts.Deliver(ctx, issued)
		if err != nil {
			return err
		}
		res.Delivery = &d
	}
	return nil
}

func (s *Service) provisionTx(ctx context.Context, tx *ent.Tx, in Input, origins []string) (*Result, *account.Issued, error) {
	res := &Result{}

	shop, err := s.CMS.CreateShopTx(ctx, tx, cms.ShopInput{Name: in.Name, Status: in.Status})
	if err != nil {
		return nil, nil, err
	}
	res.Shop = shop

	ownerRole, err := tx.Role.Query().Where(role.NameEQ(account.MerchantOwnerRole), role.ScopeEQ("merchant")).Only(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("provision: merchant_owner role: %w", err)
	}
	actor := in.ActorID
	var issued *account.Issued
	existing, err := tx.User.Query().Where(user.EmailEQ(normalizedEmail(in.OwnerEmail))).Only(ctx)
	switch {
	case err == nil:
		if err := s.Accounts.AddMembershipTx(ctx, tx, existing.ID, shop.ID, ownerRole.ID); err != nil {
			return nil, nil, err
		}
		res.OwnerID, res.OwnerEmail = existing.ID, existing.Email
	case ent.IsNotFound(err):
		u, is, err := s.Accounts.CreateAccountTx(ctx, tx, account.CreateInput{
			Email: in.OwnerEmail, ShopID: &shop.ID, RoleID: &ownerRole.ID, CreatedBy: &actor,
		})
		if err != nil {
			return nil, nil, mapOwnerErr(err)
		}
		res.OwnerID, res.OwnerEmail, res.Created = u.ID, u.Email, true
		issued = is
	default:
		return nil, nil, fmt.Errorf("provision: look up owner: %w", err)
	}

	for _, o := range origins {
		if _, err := tx.ShopAllowedOrigin.Create().SetShopID(shop.ID).SetOrigin(o).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return nil, nil, &ConflictError{Pointer: "/storefront_origins", Message: "origin is already registered: " + o}
			}
			return nil, nil, fmt.Errorf("provision: register origin: %w", err)
		}
	}
	res.Origins = origins
	return res, issued, nil
}

func mapOwnerErr(err error) error {
	if errors.Is(err, account.ErrEmailTaken) {
		return &ConflictError{Pointer: "/owner_email", Message: "email already exists"}
	}
	return err
}
