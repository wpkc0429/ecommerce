package account

import (
	"context"
	"errors"
	"fmt"

	"ksdevworks/ecommerce/api/internal/ent/shopuser"
)

// ErrSelfMembership: an actor may not disable their own membership.
var ErrSelfMembership = errors.New("account: cannot change your own membership")

// SetMembershipEnabled disables (enabled=false) or enables a user's
// membership of one shop (change merchant-staff-role, design D3/D8). It
// changes shop_user.status only: the account (users.status), its other shops
// and its refresh tokens are untouched, so a shop owner can never lock a
// person out of somewhere else.
//
// A merchant-level actor (byPlatform=false) may not act on themself
// (ErrSelfMembership) nor on a member holding a non-assignable role such as
// another owner (ErrNotDelegable). The target's cached authorization snapshot
// is dropped, so the very next request is judged against the new state.
func (s *Service) SetMembershipEnabled(ctx context.Context, actorID, shopID, targetID int, byPlatform, enabled bool) error {
	if actorID == targetID {
		return ErrSelfMembership
	}
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("account: begin tx: %w", err)
	}
	if _, err := lockUser(ctx, tx, targetID); err != nil {
		_ = tx.Rollback()
		return err
	}
	exists, err := tx.ShopUser.Query().Where(shopuser.ShopIDEQ(shopID), shopuser.UserIDEQ(targetID)).Exist(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("account: check membership: %w", err)
	}
	if !exists {
		_ = tx.Rollback()
		return ErrNotFound
	}
	if !byPlatform {
		blocked, err := HoldsNonAssignableRole(ctx, tx.Client(), shopID, targetID)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if blocked {
			_ = tx.Rollback()
			return ErrNotDelegable
		}
	}
	status := int16(0)
	if enabled {
		status = 1
	}
	if _, err := tx.ShopUser.Update().
		Where(shopuser.ShopIDEQ(shopID), shopuser.UserIDEQ(targetID)).
		SetStatus(status).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("account: set membership status: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("account: commit: %w", err)
	}
	s.Invalidate(ctx, targetID)
	return nil
}
