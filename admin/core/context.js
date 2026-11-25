// Builds the `ctx` object every design view receives. It exposes EXACTLY what lib/public-api.json lists
// (scripts/check-design.mjs compares the two) and nothing else: no tokens, no network, no storage, no lib/ modules.
// The object is deeply frozen. Data and actions come from the core's groups (core/groups/*.js); every action checks
// permissions itself and every irreversible action asks its own FIXED-warning confirmation.
// Pattern adapted from merchant-template v0.3.0 core/context.js (docs/DESIGN-LAYER-PROVENANCE.md).
import { session, hasShopList, shopIds, selectShop } from "../lib/session.js";
import { can, canAny, canPlatform } from "../lib/rbac.js";
import { navigate as routerNavigate } from "../lib/router.js";
import { t } from "../lib/design.js";
import * as kit from "../ui/kit.js";
import * as money from "../lib/money.js";
import { formatTime } from "../lib/format.js";
import { groups } from "./groups/index.js";

const UI = {
  h: kit.h, svg: kit.svg, append: kit.append, clear: kit.clear, $: kit.$, $$: kit.$$,
  confirmDialog: kit.confirmDialog, rows: kit.rows, renderPager: kit.renderPager, fillSelect: kit.fillSelect,
  repeater: kit.repeater, copyText: kit.copyText,
};
const FORMAT = {
  money: money.formatMoney, points: money.formatPoints, pointsDelta: money.formatPointsDelta, percent: money.formatPercent,
  parseAmount: money.parseAmount, priceRange: money.priceRange, time: formatTime,
};
const merge = (key) => Object.assign({}, ...Object.values(groups).map((g) => g[key] || {}));
Object.assign(UI, merge("ui"));
Object.assign(FORMAT, merge("format"));
const ASSET_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,80}$/;

const freeze = (o) => { for (const v of Object.values(o)) if (v && typeof v === "object" && !Object.isFrozen(v)) freeze(v); return Object.freeze(o); };
const STATIC = freeze({ ui: UI, format: FORMAT, data: merge("data"), logic: merge("logic"), enums: merge("enums"), actions: merge("actions") });

/**
 * routeId/params(array of path segments)/query: from the router. state: small plain data the shell passes to a screen.
 * nav: the permission-filtered destinations [{key, copy}]. auth: from core/auth.js. alive: view-token check.
 */
export function makeContext({ routeId, params = [], state = {}, alive = () => true, nav = [], auth }) {
  const ctx = {
    route: { id: routeId, params: [...params], state: { ...state } },
    alive,
    t,
    can: (n) => can(n), canAny, canPlatform,
    navigate(name, ...segs) {
      if (!/^[a-z][a-z0-9-]*$/.test(String(name))) { console.warn(`[design] navigate refused: ${name}`); return false; }
      return routerNavigate(name, ...segs);
    },
    scrollTop: () => window.scrollTo(0, 0),
    asset(name) {
      if (!ASSET_RE.test(String(name))) { console.warn(`[design] asset name refused: ${name}`); return ""; }
      return new URL(`../design/assets/${name}`, import.meta.url).href;
    },
    errorMessage: kit.errorMessage,
    fieldErrors: kit.fieldErrors,
    session: Object.freeze({
      get userEmail() { return session.me ? session.me.email : ""; },
      get shopId() { return session.shopID || 0; },
      get shopIds() { return shopIds(); },
      get hasShopList() { return hasShopList(); },
      selectShop: (id) => selectShop(id),
    }),
    nav: Object.freeze({ items: Object.freeze(nav.map((n) => Object.freeze({ key: n.key, copy: n.copy }))) }),
    auth: auth ? freeze(auth) : undefined,
    ...STATIC,
  };
  return Object.freeze(ctx);
}
