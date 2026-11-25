package content

import (
	"context"

	"ksdevworks/ecommerce/api/internal/rbac"
)

// PublishAuthorizer answers, at fire time, whether the person who scheduled a
// publish may still publish in the shop: still a member (not disabled, not
// removed) and still holding content.publish. A schedule must not outlive the
// permission of the person who made it.
func PublishAuthorizer(e *rbac.Engine) func(ctx context.Context, userID, shopID int) bool {
	return func(ctx context.Context, userID, shopID int) bool {
		if userID <= 0 {
			return false
		}
		snap, err := e.Snapshot(ctx, userID, shopID)
		return err == nil && snap.CanAccessShop() && snap.Allows("content.publish")
	}
}
