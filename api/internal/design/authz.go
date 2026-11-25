package design

import (
	"context"

	"ksdevworks/ecommerce/api/internal/rbac"
)

// RBACAuthorizer answers requester checks with the shop's RBAC snapshot. It is
// stricter than the admin routes' guard: the requester must be an ACTIVE MEMBER
// of the shop (a platform role alone does not count), so a compromised service
// cannot hang a request on a person who is not part of the shop.
type RBACAuthorizer struct{ Engine *rbac.Engine }

// Allows implements Authorizer.
func (a RBACAuthorizer) Allows(ctx context.Context, userID, shopID int, node string) (bool, error) {
	snap, err := a.Engine.Snapshot(ctx, userID, shopID)
	if err != nil {
		return false, err
	}
	return !snap.Disabled && snap.IsShopMember && snap.Allows(node), nil
}
