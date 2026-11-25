// Aggregates the per-group metadata (pure data, no imports of browser APIs): routes, navigation and the FIXED
// warnings of irreversible confirmations. Both the browser (core/routes.js, core/warnings.js) and the guard
// scripts (Node) import this. Adding a group = one line here + core/groups/<g>.js (core change, human reviewed).
import auth from "./auth.js";
import cms from "./cms.js";
import catalog from "./catalog.js";
import orders from "./orders.js";
import members from "./members.js";
import settings from "./settings.js";
import platform from "./platform.js";
import content from "./content.js";

export const GROUPS = { auth, cms, catalog, orders, members, settings, platform, content };

const all = Object.values(GROUPS);
/** Route definitions: {id, name (the #/name hash segment), perm: [all of], permAny: [any of], platformPerm, nav, transient, public} */
export const ROUTE_DEFS = all.flatMap((g) => g.routes || []);
/** Navigation entries {key (= route name), copy (copy key of the label), order, perm, permAny, platformPerm} sorted by order. */
export const NAV_DEFS = all.flatMap((g) => g.nav || []).sort((a, b) => a.order - b.order);
/** {confirmId: fixed warning text}. */
export const WARNINGS = Object.assign({}, ...all.map((g) => g.warned || {}));
export const WARNED_IDS = Object.keys(WARNINGS);
