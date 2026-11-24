package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"ksdevworks/ecommerce/api/internal/account"
	"ksdevworks/ecommerce/api/internal/auth"
	"ksdevworks/ecommerce/api/internal/ent"
	"ksdevworks/ecommerce/api/internal/ent/permission"
	"ksdevworks/ecommerce/api/internal/ent/role"
	"ksdevworks/ecommerce/api/internal/ent/rolepermission"
	"ksdevworks/ecommerce/api/internal/ent/roleuser"
	"ksdevworks/ecommerce/api/internal/ent/shopuser"
	"ksdevworks/ecommerce/api/internal/httpx"
	"ksdevworks/ecommerce/api/internal/rbac"
)

// RolesHandler implements role management (task 4.4): merchant-scope
// assignment limited to the caller's shop, platform-scope role and permission
// catalog management limited to platform admins.
type RolesHandler struct {
	Client *ent.Client
	Engine *rbac.Engine
	Authz  *AuthzMW
	Log    *slog.Logger
}

// MountShop registers routes under /admin/shops/{shopID} (admin-authenticated).
func (h *RolesHandler) MountShop(r chi.Router) {
	// Self-information: no permission node, only the cross-shop guard
	// (change admin-commerce-catalog, design D5).
	r.With(h.Authz.RequireShopAccess()).Get("/me", h.meInShop)
	r.With(h.Authz.RequireShopPermission("user.view")).Get("/roles", h.listMerchantRoles)
	r.With(h.Authz.RequireShopPermission("user.view")).Get("/users", h.listShopUsers)
	r.With(h.Authz.RequireShopPermission("user.manage_roles")).Post("/users/{userID}/roles", h.assignShopRole)
	r.With(h.Authz.RequireShopPermission("user.manage_roles")).Delete("/users/{userID}/roles/{roleID}", h.removeShopRole)
}

// MountPlatform registers platform-level routes under /admin.
func (h *RolesHandler) MountPlatform(r chi.Router) {
	guard := h.Authz.RequirePlatformPermission("user.manage_roles")
	r.With(guard).Get("/permissions", h.listPermissions)
	r.With(guard).Post("/permissions", h.createPermission)
	r.With(guard).Get("/roles", h.listAllRoles)
	r.With(guard).Post("/roles", h.createRole)
	r.With(guard).Put("/roles/{roleID}/permissions", h.setRolePermissions)
	r.With(guard).Post("/users/{userID}/roles", h.assignPlatformRole)
	r.With(guard).Delete("/users/{userID}/roles/{roleID}", h.removePlatformRole)
}

func intParam(r *http.Request, name string) (int, bool) {
	v, err := strconv.Atoi(chi.URLParam(r, name))
	return v, err == nil && v > 0
}

// ── merchant scope ────────────────────────────────────────────────

// meInShop returns the caller's own effective permission nodes for the shop in
// the URL (change admin-commerce-catalog, design D5) — the answer to "what may
// I do here?", which the admin SPA needs before it can decide which buttons to
// render at all.
//
// The node list is derived from the permissions catalog and run through
// Snapshot.Allows, so it is the *decided* set (a user-level denial override
// removes a node its role grants), not the raw union of role permissions.
// Deriving it from the catalog rather than a hardcoded list means a new
// permission node is covered here the day it is seeded.
func (h *RolesHandler) meInShop(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.AdminFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return
	}
	shopID, ok := shopIDParam(r)
	if !ok {
		httpx.NotFound(w)
		return
	}
	snap, err := h.Engine.Snapshot(r.Context(), id.UserID, shopID)
	if err != nil {
		h.Log.Error("load authorization snapshot", "err", err)
		httpx.Internal(w)
		return
	}
	names, err := h.Client.Permission.Query().Order(permission.ByName()).Select(permission.FieldName).Strings(r.Context())
	if err != nil {
		h.Log.Error("load permission catalog", "err", err)
		httpx.Internal(w)
		return
	}
	granted := make([]string, 0, len(names))
	for _, name := range names {
		if snap.Allows(name) {
			granted = append(granted, name)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"shop_id":     shopID,
		"user_id":     id.UserID,
		"permissions": granted,
	})
}

func (h *RolesHandler) listMerchantRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.Client.Role.Query().
		Where(role.ScopeEQ("merchant")).
		WithPermissions().
		All(r.Context())
	if err != nil {
		h.Log.Error("list merchant roles", "err", err)
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"roles": rolesJSON(roles)})
}

func (h *RolesHandler) listShopUsers(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	ctx := r.Context()
	memberships, err := h.Client.ShopUser.Query().
		Where(shopuser.ShopIDEQ(shopID)).
		WithUser().
		All(ctx)
	if err != nil {
		h.Log.Error("list shop users", "err", err)
		httpx.Internal(w)
		return
	}
	out := make([]map[string]any, 0, len(memberships))
	for _, m := range memberships {
		entry := map[string]any{"user_id": m.UserID, "membership_status": m.Status}
		if m.Edges.User != nil {
			entry["email"] = m.Edges.User.Email
			entry["status"] = m.Edges.User.Status
		}
		assignments, err := h.Client.RoleUser.Query().
			Where(roleuser.UserIDEQ(m.UserID), roleuser.ShopIDEQ(shopID)).
			WithRole().
			All(ctx)
		if err == nil {
			var rr []map[string]any
			for _, a := range assignments {
				if a.Edges.Role != nil {
					rr = append(rr, map[string]any{
						"id": a.RoleID, "name": a.Edges.Role.Name, "merchant_assignable": a.Edges.Role.MerchantAssignable,
					})
				}
			}
			entry["roles"] = rr
		}
		out = append(out, entry)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"users": out})
}

// assignShopRole assigns a merchant-scope role to a member of this shop.
func (h *RolesHandler) assignShopRole(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	targetID, ok := intParam(r, "userID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var req struct {
		RoleID int `json:"role_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()

	rl, err := h.Client.Role.Get(ctx, req.RoleID)
	if err != nil || rl.Scope != "merchant" {
		httpx.Unprocessable(w, "role must exist and have merchant scope", nil)
		return
	}
	// Delegation rules (change merchant-staff-role, design D4/D8): a
	// merchant-level caller may only grant merchant_assignable roles, may not
	// touch a member who holds a non-assignable one (another owner), and may
	// not change their own roles. merchant_owner is the platform's to grant.
	if !h.delegationAllowed(w, r, shopID, targetID, rl.MerchantAssignable) {
		return
	}
	isMember, err := h.Client.ShopUser.Query().
		Where(shopuser.ShopIDEQ(shopID), shopuser.UserIDEQ(targetID)).
		Exist(ctx)
	if err != nil {
		httpx.Internal(w)
		return
	}
	if !isMember {
		httpx.Unprocessable(w, "target user is not a member of this shop", nil)
		return
	}
	exists, err := h.Client.RoleUser.Query().
		Where(roleuser.UserIDEQ(targetID), roleuser.RoleIDEQ(req.RoleID), roleuser.ShopIDEQ(shopID)).
		Exist(ctx)
	if err != nil {
		httpx.Internal(w)
		return
	}
	if !exists {
		if _, err := h.Client.RoleUser.Create().
			SetUserID(targetID).SetRoleID(req.RoleID).SetShopID(shopID).
			Save(ctx); err != nil {
			h.Log.Error("assign shop role", "err", err)
			httpx.Internal(w)
			return
		}
	}
	// 撤權/授權立即生效 (spec rbac/Authorization cache invalidation).
	h.Engine.InvalidateUsers(ctx, targetID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user_id": targetID, "role_id": req.RoleID, "shop_id": shopID})
}

// delegationAllowed applies the merchant-level delegation rules and writes the
// refusal itself (409 self, 403 otherwise) when they are not met. Platform
// administrators are not bound by them (design D4). roleAssignable is the
// assignability of the role being granted (true when removing: what a removal
// may touch is decided by what the target holds).
func (h *RolesHandler) delegationAllowed(w http.ResponseWriter, r *http.Request, shopID, targetID int, roleAssignable bool) bool {
	actor, ok := auth.AdminFrom(r.Context())
	if !ok {
		httpx.Unauthorized(w)
		return false
	}
	byPlatform, err := account.IsPlatformCaller(r.Context(), h.Engine, actor.UserID)
	if err != nil {
		h.Log.Error("delegation: platform check", "err", err)
		httpx.Internal(w)
		return false
	}
	if byPlatform {
		return true
	}
	if actor.UserID == targetID {
		httpx.Conflict(w, "you cannot change your own roles")
		return false
	}
	if !roleAssignable {
		httpx.Forbidden(w)
		return false
	}
	blocked, err := account.HoldsNonAssignableRole(r.Context(), h.Client, shopID, targetID)
	if err != nil {
		h.Log.Error("delegation: target roles", "err", err)
		httpx.Internal(w)
		return false
	}
	if blocked {
		httpx.Forbidden(w)
		return false
	}
	return true
}

func (h *RolesHandler) removeShopRole(w http.ResponseWriter, r *http.Request) {
	shopID, _ := shopIDParam(r)
	targetID, ok1 := intParam(r, "userID")
	roleID, ok2 := intParam(r, "roleID")
	if !ok1 || !ok2 {
		httpx.NotFound(w)
		return
	}
	ctx := r.Context()
	if !h.delegationAllowed(w, r, shopID, targetID, true) {
		return
	}
	n, err := h.Client.RoleUser.Delete().
		Where(roleuser.UserIDEQ(targetID), roleuser.RoleIDEQ(roleID), roleuser.ShopIDEQ(shopID)).
		Exec(ctx)
	if err != nil {
		httpx.Internal(w)
		return
	}
	if n == 0 {
		httpx.NotFound(w)
		return
	}
	h.Engine.InvalidateUsers(ctx, targetID)
	w.WriteHeader(http.StatusNoContent)
}

// ── platform scope ────────────────────────────────────────────────

func (h *RolesHandler) listPermissions(w http.ResponseWriter, r *http.Request) {
	perms, err := h.Client.Permission.Query().Order(permission.ByName()).All(r.Context())
	if err != nil {
		httpx.Internal(w)
		return
	}
	out := make([]map[string]any, 0, len(perms))
	for _, p := range perms {
		out = append(out, map[string]any{"id": p.ID, "name": p.Name, "description": p.Description})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"permissions": out})
}

func (h *RolesHandler) createPermission(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		httpx.Unprocessable(w, "name is required", nil)
		return
	}
	p, err := h.Client.Permission.Create().SetName(req.Name).SetDescription(req.Description).Save(r.Context())
	if ent.IsConstraintError(err) {
		httpx.Unprocessable(w, "permission name already exists", nil)
		return
	}
	if err != nil {
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"id": p.ID, "name": p.Name})
}

func (h *RolesHandler) listAllRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.Client.Role.Query().WithPermissions().Order(role.ByID()).All(r.Context())
	if err != nil {
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"roles": rolesJSON(roles)})
}

func (h *RolesHandler) createRole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" || (req.Scope != "platform" && req.Scope != "merchant") {
		httpx.Unprocessable(w, "name required; scope must be platform or merchant", nil)
		return
	}
	rl, err := h.Client.Role.Create().SetName(req.Name).SetScope(req.Scope).Save(r.Context())
	if ent.IsConstraintError(err) {
		httpx.Unprocessable(w, "role already exists", nil)
		return
	}
	if err != nil {
		httpx.Internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"id": rl.ID, "name": rl.Name, "scope": rl.Scope})
}

// setRolePermissions replaces the permission set of a role.
func (h *RolesHandler) setRolePermissions(w http.ResponseWriter, r *http.Request) {
	roleID, ok := intParam(r, "roleID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var req struct {
		PermissionIDs []int `json:"permission_ids"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	if _, err := h.Client.Role.Get(ctx, roleID); err != nil {
		httpx.NotFound(w)
		return
	}
	valid, err := h.Client.Permission.Query().Where(permission.IDIn(req.PermissionIDs...)).Count(ctx)
	if err != nil {
		httpx.Internal(w)
		return
	}
	if valid != len(req.PermissionIDs) {
		httpx.Unprocessable(w, "unknown permission id in set", nil)
		return
	}

	tx, err := h.Client.Tx(ctx)
	if err != nil {
		httpx.Internal(w)
		return
	}
	_, err = tx.RolePermission.Delete().Where(rolepermission.RoleIDEQ(roleID)).Exec(ctx)
	if err == nil {
		for _, pid := range req.PermissionIDs {
			if _, err = tx.RolePermission.Create().SetRoleID(roleID).SetPermissionID(pid).Save(ctx); err != nil {
				break
			}
		}
	}
	if err != nil {
		_ = tx.Rollback()
		httpx.Internal(w)
		return
	}
	if err := tx.Commit(); err != nil {
		httpx.Internal(w)
		return
	}
	// Everyone holding this role sees the change on their next request.
	h.Engine.InvalidateRoleMembers(ctx, roleID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"role_id": roleID, "permission_ids": req.PermissionIDs})
}

func (h *RolesHandler) assignPlatformRole(w http.ResponseWriter, r *http.Request) {
	targetID, ok := intParam(r, "userID")
	if !ok {
		httpx.NotFound(w)
		return
	}
	var req struct {
		RoleID int `json:"role_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	rl, err := h.Client.Role.Get(ctx, req.RoleID)
	if err != nil || rl.Scope != "platform" {
		httpx.Unprocessable(w, "role must exist and have platform scope", nil)
		return
	}
	if _, err := h.Client.User.Get(ctx, targetID); err != nil {
		httpx.Unprocessable(w, "target user does not exist", nil)
		return
	}
	exists, err := h.Client.RoleUser.Query().
		Where(roleuser.UserIDEQ(targetID), roleuser.RoleIDEQ(req.RoleID), roleuser.ShopIDIsNil()).
		Exist(ctx)
	if err != nil {
		httpx.Internal(w)
		return
	}
	if !exists {
		if _, err := h.Client.RoleUser.Create().SetUserID(targetID).SetRoleID(req.RoleID).Save(ctx); err != nil {
			httpx.Internal(w)
			return
		}
	}
	h.Engine.InvalidateUsers(ctx, targetID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user_id": targetID, "role_id": req.RoleID, "shop_id": nil})
}

func (h *RolesHandler) removePlatformRole(w http.ResponseWriter, r *http.Request) {
	targetID, ok1 := intParam(r, "userID")
	roleID, ok2 := intParam(r, "roleID")
	if !ok1 || !ok2 {
		httpx.NotFound(w)
		return
	}
	ctx := r.Context()
	rl, err := h.Client.Role.Get(ctx, roleID)
	if err != nil && !ent.IsNotFound(err) {
		httpx.Internal(w)
		return
	}
	tx, err := h.Client.Tx(ctx)
	if err != nil {
		httpx.Internal(w)
		return
	}
	// Removing super_admin must never leave the platform without an active
	// one (change platform-account-provisioning, design D6). The check runs
	// under row locks so two concurrent removals cannot both pass.
	if rl != nil && rl.Name == account.SuperAdminRole {
		sa, err := account.LockSuperAdmins(ctx, tx)
		if err != nil {
			_ = tx.Rollback()
			h.Log.Error("remove platform role: lock super_admins", "err", err)
			httpx.Internal(w)
			return
		}
		if sa.WouldLoseLast(targetID) {
			_ = tx.Rollback()
			httpx.Conflict(w, "this would leave no active super_admin")
			return
		}
	}
	n, err := tx.RoleUser.Delete().
		Where(roleuser.UserIDEQ(targetID), roleuser.RoleIDEQ(roleID), roleuser.ShopIDIsNil()).
		Exec(ctx)
	if err != nil {
		_ = tx.Rollback()
		httpx.Internal(w)
		return
	}
	if err := tx.Commit(); err != nil {
		httpx.Internal(w)
		return
	}
	if n == 0 {
		httpx.NotFound(w)
		return
	}
	h.Engine.InvalidateUsers(ctx, targetID)
	w.WriteHeader(http.StatusNoContent)
}

func rolesJSON(roles []*ent.Role) []map[string]any {
	out := make([]map[string]any, 0, len(roles))
	for _, rl := range roles {
		perms := make([]string, 0, len(rl.Edges.Permissions))
		for _, p := range rl.Edges.Permissions {
			perms = append(perms, p.Name)
		}
		out = append(out, map[string]any{
			"id": rl.ID, "name": rl.Name, "scope": rl.Scope, "merchant_assignable": rl.MerchantAssignable, "permissions": perms,
		})
	}
	return out
}
