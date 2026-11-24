// Package provision is the one implementation of "open a new shop" (change
// platform-account-provisioning, design D8): shop + home page, theme, domain
// binding, owner account and storefront origins, all in one transaction. The
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
	"ksdevworks/ecommerce/api/internal/ent/site"
	"ksdevworks/ecommerce/api/internal/ent/user"
	"ksdevworks/ecommerce/api/internal/events"
	"ksdevworks/ecommerce/api/internal/tenant"
)

// ErrConflict marks a uniqueness/binding violation (domain, prefix, origin).
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

// Input is a shop provisioning request. OwnerEmail is required; Domain is
// optional (PathPrefix only applies with it).
type Input struct {
	Name              string
	ThemeID           *int
	Status            *int16
	Domain            string
	PathPrefix        string
	StorefrontOrigins []string
	OwnerEmail        string
	ActorID           int
}

// Binding is the domain binding that was created.
type Binding struct {
	SiteID     int
	Domain     string
	PathPrefix *string
	IsPrimary  bool
	SiteIsNew  bool
}

// Result is the outcome of ProvisionShop.
type Result struct {
	Shop       *ent.Shop
	Binding    *Binding
	Origins    []string
	OwnerID    int
	OwnerEmail string
	// Created reports whether the owner account was created by this call.
	Created bool
	// Delivery is set only when Created.
	Delivery *account.Delivery
}

// ProvisionShop does everything in one transaction; any failure rolls back
// the whole thing, so there is never a shop without an owner.
func (s *Service) ProvisionShop(ctx context.Context, in Input) (*Result, error) {
	// Validate the cheap, purely local inputs before opening a transaction.
	domain := tenant.NormalizeHost(in.Domain)
	var prefix *string
	if domain != "" {
		if !tenant.ValidDomain(domain) {
			return nil, &account.ValidationError{Pointer: "/domain", Message: "must be a lowercase hostname"}
		}
		if p := tenant.NormalizePathPrefix(in.PathPrefix); p != "" {
			if !tenant.ValidPathPrefix(p) {
				return nil, &account.ValidationError{Pointer: "/path_prefix", Message: "must match ^/[a-z0-9-]+$ segments"}
			}
			prefix = &p
		}
	} else if in.PathPrefix != "" {
		return nil, &account.ValidationError{Pointer: "/path_prefix", Message: "requires domain"}
	}
	origins := make([]string, 0, len(in.StorefrontOrigins))
	for i, raw := range in.StorefrontOrigins {
		o := tenant.NormalizeOrigin(raw)
		if o == "" {
			return nil, &account.ValidationError{
				Pointer: fmt.Sprintf("/storefront_origins/%d", i),
				Message: "must be a scheme://host[:port] http(s) origin without path or wildcard",
			}
		}
		origins = append(origins, o)
	}

	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("provision: begin tx: %w", err)
	}
	res, issued, err := s.provisionTx(ctx, tx, in, domain, prefix, origins)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("provision: commit: %w", err)
	}

	// After commit: cache invalidation through events, then link delivery.
	s.Accounts.Invalidate(ctx, res.OwnerID)
	if res.Binding != nil {
		s.Dispatcher.Publish(ctx, events.ShopStatusChanged{Hosts: []string{res.Binding.Domain}, ShopIDs: []int{res.Shop.ID}})
	}
	if len(res.Origins) > 0 {
		s.Dispatcher.Publish(ctx, events.StorefrontClientChanged{ShopID: res.Shop.ID, Origins: res.Origins})
	}
	if issued != nil {
		d, err := s.Accounts.Deliver(ctx, issued)
		if err != nil {
			return nil, err
		}
		res.Delivery = &d
	}
	return res, nil
}

func (s *Service) provisionTx(ctx context.Context, tx *ent.Tx, in Input, domain string, prefix *string, origins []string) (*Result, *account.Issued, error) {
	res := &Result{}

	shop, err := s.CMS.CreateShopTx(ctx, tx, cms.ShopInput{Name: in.Name, ThemeID: in.ThemeID, Status: in.Status})
	if err != nil {
		return nil, nil, err
	}
	res.Shop = shop

	if domain != "" {
		b, err := bindDomain(ctx, tx, shop.ID, domain, prefix)
		if err != nil {
			return nil, nil, err
		}
		res.Binding = b
	}

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

// bindDomain finds or creates the site and binds it to the shop as primary.
func bindDomain(ctx context.Context, tx *ent.Tx, shopID int, domain string, prefix *string) (*Binding, error) {
	b := &Binding{Domain: domain, PathPrefix: prefix, IsPrimary: true}
	st, err := tx.Site.Query().Where(site.DomainEQ(domain)).Only(ctx)
	switch {
	case err == nil:
	case ent.IsNotFound(err):
		st, err = tx.Site.Create().SetDomain(domain).Save(ctx)
		if err != nil {
			if ent.IsConstraintError(err) {
				return nil, &ConflictError{Pointer: "/domain", Message: "domain already exists"}
			}
			return nil, fmt.Errorf("provision: create site: %w", err)
		}
		b.SiteIsNew = true
	default:
		return nil, fmt.Errorf("provision: look up site: %w", err)
	}
	b.SiteID = st.ID
	create := tx.SiteShop.Create().SetSiteID(st.ID).SetShopID(shopID).SetIsPrimary(true)
	if prefix != nil {
		create.SetPathPrefix(*prefix)
	}
	if _, err := create.Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, &ConflictError{Pointer: "/domain", Message: "domain and path_prefix are already bound to another shop"}
		}
		return nil, fmt.Errorf("provision: bind site: %w", err)
	}
	return b, nil
}
