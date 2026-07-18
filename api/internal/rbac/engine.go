package rbac

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/permission"
	"ksdevworks/ecommerce/api/internal/ent/role"
	"ksdevworks/ecommerce/api/internal/ent/rolepermission"
	"ksdevworks/ecommerce/api/internal/ent/roleuser"
	"ksdevworks/ecommerce/api/internal/ent/shopuser"
	"ksdevworks/ecommerce/api/internal/ent/userpermission"
)

// PlatformCtx is the shop ctx value for platform-level (no shop) decisions.
const PlatformCtx = 0

const cacheTTL = 10 * time.Minute

// Engine loads and caches authorization snapshots. Redis is optional (nil →
// DB-only, and any Redis failure degrades to DB — cache never blocks authz).
type Engine struct {
	client *ent.Client
	redis  *redis.Client
	log    *slog.Logger
}

func NewEngine(client *ent.Client, rdb *redis.Client, log *slog.Logger) *Engine {
	return &Engine{client: client, redis: rdb, log: log}
}

func cacheKey(userID, shopID int) string {
	return fmt.Sprintf("authz:user:%d:shop:%d", userID, shopID)
}

// Snapshot returns the (possibly cached) authorization snapshot for
// (user, shop ctx). shopID = PlatformCtx for platform-level decisions.
func (e *Engine) Snapshot(ctx context.Context, userID, shopID int) (*Snapshot, error) {
	key := cacheKey(userID, shopID)
	if e.redis != nil {
		if raw, err := e.redis.Get(ctx, key).Bytes(); err == nil {
			var snap Snapshot
			if json.Unmarshal(raw, &snap) == nil {
				return &snap, nil
			}
		}
	}
	snap, err := e.load(ctx, userID, shopID)
	if err != nil {
		return nil, err
	}
	if e.redis != nil {
		if raw, err := json.Marshal(snap); err == nil {
			if err := e.redis.Set(ctx, key, raw, cacheTTL).Err(); err != nil {
				e.log.Warn("authz cache set failed", "err", err)
			}
		}
	}
	return snap, nil
}

// Allows runs the three-tier decision with caching.
func (e *Engine) Allows(ctx context.Context, userID, shopID int, perm string) (bool, error) {
	snap, err := e.Snapshot(ctx, userID, shopID)
	if err != nil {
		return false, err
	}
	return snap.Allows(perm), nil
}

// load builds a snapshot from the database (design D4).
func (e *Engine) load(ctx context.Context, userID, shopID int) (*Snapshot, error) {
	snap := &Snapshot{
		ShopOverrides:     map[string]bool{},
		PlatformOverrides: map[string]bool{},
		RolePerms:         map[string]bool{},
	}

	// Tier 1 inputs: per-user overrides at shop ctx and platform level.
	ovQuery := e.client.UserPermission.Query().Where(userpermission.UserIDEQ(userID))
	if shopID != PlatformCtx {
		ovQuery = ovQuery.Where(userpermission.Or(
			userpermission.ShopIDEQ(shopID),
			userpermission.ShopIDIsNil(),
		))
	} else {
		ovQuery = ovQuery.Where(userpermission.ShopIDIsNil())
	}
	overrides, err := ovQuery.WithPermission().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("rbac: load overrides: %w", err)
	}
	for _, ov := range overrides {
		permName := ""
		if ov.Edges.Permission != nil {
			permName = ov.Edges.Permission.Name
		} else if p, perr := e.client.Permission.Query().Where(permission.IDEQ(ov.PermissionID)).Only(ctx); perr == nil {
			permName = p.Name
		}
		if permName == "" {
			continue
		}
		if ov.ShopID != nil {
			snap.ShopOverrides[permName] = ov.IsGranted
		} else {
			snap.PlatformOverrides[permName] = ov.IsGranted
		}
	}

	// Tier 2 inputs: roles at shop ctx plus platform level.
	ruQuery := e.client.RoleUser.Query().Where(roleuser.UserIDEQ(userID))
	if shopID != PlatformCtx {
		ruQuery = ruQuery.Where(roleuser.Or(
			roleuser.ShopIDEQ(shopID),
			roleuser.ShopIDIsNil(),
		))
	} else {
		ruQuery = ruQuery.Where(roleuser.ShopIDIsNil())
	}
	assignments, err := ruQuery.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("rbac: load role assignments: %w", err)
	}
	roleIDs := make([]int, 0, len(assignments))
	platformRoleIDs := make([]int, 0, len(assignments))
	for _, a := range assignments {
		roleIDs = append(roleIDs, a.RoleID)
		if a.ShopID == nil {
			platformRoleIDs = append(platformRoleIDs, a.RoleID)
		}
	}
	if len(platformRoleIDs) > 0 {
		// Defense in depth: only scope='platform' roles count as platform.
		n, err := e.client.Role.Query().
			Where(role.IDIn(platformRoleIDs...), role.ScopeEQ("platform")).
			Count(ctx)
		if err != nil {
			return nil, fmt.Errorf("rbac: check platform roles: %w", err)
		}
		snap.HasPlatformRole = n > 0
	}
	if len(roleIDs) > 0 {
		names, err := e.client.RolePermission.Query().
			Where(rolepermission.RoleIDIn(roleIDs...)).
			QueryPermission().
			Select(permission.FieldName).
			Strings(ctx)
		if err != nil {
			return nil, fmt.Errorf("rbac: load role permissions: %w", err)
		}
		for _, n := range names {
			snap.RolePerms[n] = true
		}
	}

	// Cross-shop guard input.
	if shopID != PlatformCtx {
		isMember, err := e.client.ShopUser.Query().
			Where(shopuser.ShopIDEQ(shopID), shopuser.UserIDEQ(userID)).
			Exist(ctx)
		if err != nil {
			return nil, fmt.Errorf("rbac: check membership: %w", err)
		}
		snap.IsShopMember = isMember
	}
	return snap, nil
}

// ── invalidation (spec rbac/Authorization cache invalidation) ────

// InvalidateUsers deletes every cached snapshot of the given users so the
// next request re-reads fresh data.
func (e *Engine) InvalidateUsers(ctx context.Context, userIDs ...int) {
	if e.redis == nil {
		return
	}
	for _, uid := range userIDs {
		pattern := fmt.Sprintf("authz:user:%d:shop:*", uid)
		iter := e.redis.Scan(ctx, 0, pattern, 200).Iterator()
		var keys []string
		for iter.Next(ctx) {
			keys = append(keys, iter.Val())
		}
		if err := iter.Err(); err != nil {
			e.log.Warn("authz invalidation scan failed", "user", uid, "err", err)
			continue
		}
		if len(keys) > 0 {
			if err := e.redis.Del(ctx, keys...).Err(); err != nil {
				e.log.Warn("authz invalidation del failed", "user", uid, "err", err)
			}
		}
	}
}

// InvalidateRoleMembers invalidates every user holding the given role
// (role_permission changes affect them all).
func (e *Engine) InvalidateRoleMembers(ctx context.Context, roleID int) {
	if e.redis == nil {
		return
	}
	uids, err := e.client.RoleUser.Query().
		Where(roleuser.RoleIDEQ(roleID)).
		Select(roleuser.FieldUserID).
		Ints(ctx)
	if err != nil {
		e.log.Warn("authz role invalidation query failed", "role", roleID, "err", err)
		return
	}
	seen := map[int]bool{}
	unique := uids[:0]
	for _, id := range uids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	e.InvalidateUsers(ctx, unique...)
}
