// Errors raised by the core's action functions (permission, state rules, validation, declined confirmations).
// `message` is already localized (copy lookup happens where the error is raised).
import { t } from "../lib/design.js";

export class ActionError extends Error {
  /**
   * kind: "permission" | "state" | "invalid" | "limit" | "stale" (412: someone else saved first, reload) | "internal" (428 and other programming errors)
   * fields: {pointer: message} for form errors; reason: a short machine code ("cancelled", "returned", ...).
   */
  constructor(kind, message, { fields, reason, status } = {}) {
    super(message);
    this.name = "ActionError";
    this.isActionError = true;
    this.kind = kind;
    this.fields = fields || null;
    this.reason = reason || "";
    this.status = status ?? ({ permission: 403, state: 409, invalid: 422, limit: 409, stale: 412, internal: 500 }[kind] || 400);
  }
}

export const denied = () => new ActionError("permission", t("err.403"), { reason: "permission" });
export const invalid = (fields, message) => new ActionError("invalid", message || Object.values(fields)[0] || t("err.422"), { fields });
export const blocked = (reason, message) => new ActionError("state", message, { reason });
