package provision

import (
	"context"
	"errors"
	"fmt"

	"ksdevworks/ecommerce/api/internal/account"
	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/ent/page"
	"ksdevworks/ecommerce/api/internal/ent/publishablekey"
	"ksdevworks/ecommerce/api/internal/ent/role"
	"ksdevworks/ecommerce/api/internal/ent/roleuser"
	"ksdevworks/ecommerce/api/internal/ent/shippingmethod"
	"ksdevworks/ecommerce/api/internal/ent/shop"
	"ksdevworks/ecommerce/api/internal/ent/shoppaymentprovidercredential"
	"ksdevworks/ecommerce/api/internal/ent/user"
)

// ErrShopNotFound is returned by SetupStatus for an unknown shop.
var ErrShopNotFound = errors.New("provision: shop not found")

// SetupStatus is the derived "is this shop ready" checklist. Nothing here is
// stored: every flag is an existence query on the facts themselves, so it can
// never drift from them (design D8).
type SetupStatus struct {
	OwnerAssigned         bool
	OwnerPasswordSet      bool
	HomePublished         bool
	ShippingMethodActive  bool
	PaymentCredentialsSet bool
	PublishableKeyActive  bool
}

// SetupStatus derives the checklist for shopID. Back-office queries run
// without tenant context, so every tenant-owned query names shop_id itself.
func (s *Service) SetupStatus(ctx context.Context, shopID int) (*SetupStatus, error) {
	c := s.Client
	exists, err := c.Shop.Query().Where(shop.IDEQ(shopID)).Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("provision: shop: %w", err)
	}
	if !exists {
		return nil, ErrShopNotFound
	}
	st := &SetupStatus{}
	check := func(dst *bool, q interface {
		Exist(context.Context) (bool, error)
	}) error {
		v, err := q.Exist(ctx)
		if err != nil {
			return fmt.Errorf("provision: setup status: %w", err)
		}
		*dst = v
		return nil
	}
	ownerRole := roleuser.And(roleuser.ShopIDEQ(shopID), roleuser.HasRoleWith(role.NameEQ(account.MerchantOwnerRole)))
	steps := []struct {
		dst *bool
		q   interface {
			Exist(context.Context) (bool, error)
		}
	}{
		{&st.OwnerAssigned, c.RoleUser.Query().Where(ownerRole)},
		{&st.OwnerPasswordSet, c.User.Query().Where(user.PasswordHashNotNil(), user.HasRoleUsersWith(ownerRole))},
		{&st.HomePublished, c.Page.Query().Where(page.ShopIDEQ(shopID), page.SlugEQ("home"), page.StatusEQ(1))},
		{&st.ShippingMethodActive, c.ShippingMethod.Query().Where(shippingmethod.ShopIDEQ(shopID), shippingmethod.IsActiveEQ(true))},
		{&st.PaymentCredentialsSet, c.ShopPaymentProviderCredential.Query().Where(shoppaymentprovidercredential.ShopIDEQ(shopID))},
		{&st.PublishableKeyActive, c.PublishableKey.Query().Where(publishablekey.ShopIDEQ(shopID), publishablekey.RevokedAtIsNil())},
	}
	for _, step := range steps {
		if err := check(step.dst, step.q); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func normalizedEmail(raw string) string {
	e, _ := auth.NormalizeEmail(raw)
	return e
}
