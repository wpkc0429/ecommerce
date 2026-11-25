import { can, canPlatform } from "../lib/rbac.js";
import { denied } from "./errors.js";

/** Throws a 403-style ActionError unless every shop-level node is granted. The check happens BEFORE any request. */
export function need(...nodes) {
  if (!nodes.every((n) => can(n))) throw denied();
}
/** Same, but at least one of the nodes must be granted. */
export function needAny(...nodes) {
  if (!nodes.some((n) => can(n))) throw denied();
}
/** Platform-level nodes (GET /admin/me platform_permissions). */
export function needPlatform(...nodes) {
  if (!nodes.every((n) => canPlatform(n))) throw denied();
}
