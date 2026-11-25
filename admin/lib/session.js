// Session state (CORE): who is logged in, which shop is selected, permissions and branding. Tokens live in lib/api.js
// (memory / cookie); nothing here is ever exposed to the design layer except through core/context.js getters.
import { api, tokens, canRefresh, refreshTokens, logoutRequest } from "./api.js";
import { loadPermissions, setPlatformPermissions } from "./rbac.js";
import { loadMediaConfig, setShopIDSource } from "./upload.js";
import { loadShopBranding } from "./branding.js";

const badgeListeners = new Set();
export const onBadge = (fn) => { badgeListeners.add(fn); return () => badgeListeners.delete(fn); };
export const emitBadge = () => badgeListeners.forEach((fn) => { try { fn(); } catch { /* convenience only */ } });
export const session = { me: null, shopID: null };
const listeners = new Set();
export const onShopChanged = (fn) => { listeners.add(fn); return () => listeners.delete(fn); };

setShopIDSource(() => session.shopID);

export const shopIds = () => (session.me?.sids || []).slice();
export const hasShopList = () => shopIds().length > 0;

/** Restores a session after a reload (refresh cookie / token) and loads /admin/me. Returns true when logged in. */
export async function restoreSession() {
  if (!tokens.access && canRefresh()) {
    try { await refreshTokens(); } catch { tokens.clear(); }
  }
  if (!tokens.access) return false;
  try { session.me = await api("GET", "/admin/me"); return true; } catch { tokens.clear(); return false; }
}

/** After a successful login(): fetch /admin/me. */
export async function loadMe() { session.me = await api("GET", "/admin/me"); return session.me; }

/** Picks the shop, loads its permissions, media config and branding. preferShop wins when the account has it. */
export async function enterShop(preferShop) {
  setPlatformPermissions(session.me.platform_permissions);
  const sids = session.me.sids || [];
  if (sids.length > 0) session.shopID = sids.includes(preferShop) ? preferShop : sids[0];
  else session.shopID = Number(localStorage.getItem("shopID") || 1);
  await loadShopContext();
}

export async function loadShopContext() {
  await loadPermissions(session.shopID);
  loadShopBranding(session.shopID); // not awaited: branding is only appearance
  await loadMediaConfig();
}

/** Switches the active shop (selector or manual id) and tells the app to re-render the current screen. */
export async function selectShop(id) {
  const n = Number(id);
  if (!Number.isInteger(n) || n < 1) return;
  session.shopID = n;
  if (!hasShopList()) { try { localStorage.setItem("shopID", String(n)); } catch { /* storage unavailable */ } }
  await loadShopContext();
  listeners.forEach((fn) => fn(n));
}

export function clearSession() {
  logoutRequest();
  tokens.clear();
  session.me = null;
  session.shopID = null;
  setPlatformPermissions([]);
}
