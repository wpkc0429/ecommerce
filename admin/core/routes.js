// The routes the CORE defines (from lib/meta/*): ids, hash names and the permission nodes each needs. The design layer
// only chooses how a route LOOKS (design/routes.js maps route ids to view modules); it cannot add routes, change a
// name or loosen a permission. A route whose permissions are not granted never mounts its view (core/app gate).
import { ROUTE_DEFS, NAV_DEFS } from "../lib/meta/index.js";
import { can, canPlatform } from "../lib/rbac.js";

export const ROUTES = ROUTE_DEFS;
export const byName = (name) => ROUTES.find((r) => r.name === name);

/** The same rule the old app used: platformPerm -> platform node; permAny -> any; perm -> all. */
export function allowed(def) {
  if (!def) return false;
  if (def.platformPerm) return canPlatform(def.platformPerm);
  if (def.permAny) return def.permAny.some((p) => can(p));
  return (def.perm || []).every((p) => can(p));
}

/** Navigation entries the person may see. */
export const visibleNav = () => NAV_DEFS.filter((n) => allowed(n));
export const NAV = NAV_DEFS;
