// Shared helpers of the members group (CORE): the current shop's API path and the 404 wording.
import { session } from "../../lib/session.js";
import { ApiError } from "../../lib/api.js";
import { ActionError } from "../errors.js";
import { t } from "../../lib/design.js";

/** `/admin/shops/{current shop}{suffix}`: the design layer never sees a shop id. */
export const shopPath = (suffix) => `/admin/shops/${session.shopID}${suffix}`;

/** 404 on a member id = the id is not in this shop (identity is per shop): say so instead of a bare "not found". */
export function memberNotFound(e, id) {
  if (e && e.status === 404) return new ActionError("state", t("members.notFound", { id }), { reason: "notFound" });
  return e;
}

/** An API validation error as the old forms showed it: "message（/pointer message；...）". */
export function withDetails(err) {
  if (err instanceof ApiError && err.details.length) {
    return new ActionError("invalid", `${err.message}（${err.details.map((d) => `${d.pointer} ${d.message}`).join("；")}）`);
  }
  return err;
}
