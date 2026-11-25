// App bootstrap (CORE, locked): boot, the hash router, the session lifecycle and the permission gate.
// Every screen, including the shell (topbar + navigation) and the login / set-password / accept-invite screens, is a
// design-layer view mounted through core/host.js with a ctx from core/context.js. This file decides WHICH screen shows and
// WHETHER the person may see it; the design layer decides only how it looks. Structure adapted from merchant-template
// v0.3.0 app.js (docs/DESIGN-LAYER-PROVENANCE.md); routing keeps this admin's `#/<view>[/<param>]` hash router.
import { api, onUnauthorized } from "./lib/api.js";
import { parseHash, navigate, onRoute } from "./lib/router.js";
import { session, restoreSession, enterShop, loadMe, clearSession, onShopChanged, onBadge } from "./lib/session.js";
import { can, permissionError } from "./lib/rbac.js";
import { loadHostBranding } from "./lib/branding.js";
import { mediaConfig } from "./lib/upload.js";
import { loadDesign, t } from "./lib/design.js";
import { h, clear, applyBranding, allowImageOrigin, errorMessage } from "./ui/kit.js";
import { byName, allowed, visibleNav } from "./core/routes.js";
import { makeContext } from "./core/context.js";
import { makeAuth } from "./core/auth.js";
import { mountView } from "./core/host.js";

const root = document.getElementById("app");
let shell = null;           // {main, setActive, setNoAccess}
let viewToken = 0;          // invalidates in-flight view renders on navigation
let bootState = "pending";  // pending | in | out | error

const fragment = () => document.createDocumentFragment();
const nav = () => visibleNav().map((n) => ({ key: n.key, copy: n.copy }));
const firstAllowed = () => visibleNav()[0]?.key || "";

// ── Auth hooks handed to the screens through ctx.auth ──────────────────
const hooks = {
  onLoggedIn: async () => { await enterApp(); },
  onLogout: () => doLogout(),
  goLogin: (email, notice) => { history.replaceState(null, "", window.location.pathname + window.location.search); showLogin({ email, notice }); },
  afterJoin: async (prevSids) => {
    history.replaceState(null, "", window.location.pathname + window.location.search);
    await loadMe();
    const added = (session.me.sids || []).find((id) => !(prevSids || []).includes(id));
    await enterApp(added);
  },
};
const authFor = (linkToken) => makeAuth({ linkToken, hooks });

// ── Boot ───────────────────────────────────────────────────────────────
async function boot() {
  await loadDesign();
  onUnauthorized(() => doLogout());
  onShopChanged(() => { const r = parseHash(); renderRoute({ name: r.name, params: [] }); });
  onRoute(renderRoute);
  onBadge(() => { if (shell && shell.refreshBadge) shell.refreshBadge(); });
  loadHostBranding(); // the login page also carries the shop's branding (resolved from the admin host)
  const r = parseHash();
  if (r.name === "set-password" || r.name === "accept-invite") return renderRoute(r);
  try {
    if (await restoreSession()) { await enterApp(undefined, true); return; }
  } catch (e) { return showBootError(e); }
  bootState = "out";
  showLogin({});
}

async function showBootError(e) {
  bootState = "error";
  const { root: r } = await mountView("bootError", fragment, () => makeContext({ routeId: "bootError", state: { error: errorMessage(e) } }));
  root.replaceChildren(r);
}

async function enterApp(preferShop, honorHash = false) {
  if (!session.me) await loadMe();
  await enterShop(preferShop);
  const base = mediaConfig().public_base_url;
  if (base) allowImageOrigin(base);
  bootState = "in";
  await buildShell();
  const first = firstAllowed();
  if (!first) {
    shell.setNoAccess(permissionError()
      ? t("shell.noAccessError", { message: permissionError().message })
      : t("shell.noAccess"));
    shell.main.replaceChildren();
    return;
  }
  shell.setNoAccess("");
  const r = parseHash();
  const def = byName(r.name);
  if (def && !def.public && allowed(def) && !def.transient) {
    // Only a boot honours deep-link parameters; after login the person lands on the named view.
    return renderRoute(honorHash ? r : { name: r.name, params: [] });
  }
  navigate(first) || renderRoute({ name: first, params: [] });
}

// ── Routing ────────────────────────────────────────────────────────────
async function renderRoute({ name, params = [] }) {
  const token = ++viewToken;
  if (name === "set-password" || name === "accept-invite") {
    teardownShell();
    const def = byName(name);
    const { root: r } = await mountView(def.id, fragment, () => makeContext({ routeId: def.id, auth: authFor(params[0] || "") }));
    if (token !== viewToken) return;
    root.replaceChildren(r);
    return;
  }
  if (!session.me) { if (bootState !== "pending") showLogin({}); return; }
  const def = byName(name);
  if (!def || def.public || !allowed(def)) {
    // Stale bookmarks, links without permission and typos all fall back quietly to a view the person can open.
    const first = firstAllowed();
    if (first && first !== name) { if (!navigate(first)) renderRoute({ name: first, params: [] }); }
    return;
  }
  if (!shell) await buildShell();
  if (token !== viewToken) return;
  shell.setActive(def.nav || def.name);
  window.scrollTo(0, 0);
  const ctxFor = () => makeContext({ routeId: def.id, params, alive: () => token === viewToken, nav: nav(), auth: authFor(null) });
  try {
    const { root: view } = await mountView(def.id, fragment, ctxFor);
    if (token !== viewToken) return;
    shell.main.replaceChildren(view);
  } catch (e) {
    if (token !== viewToken) return;
    shell.main.replaceChildren(h("p", { class: "error" }, errorMessage(e)));
  }
}

// ── Login / shell ──────────────────────────────────────────────────────
async function showLogin({ email = "", notice = "" }) {
  const token = ++viewToken;
  teardownShell();
  bootState = "out";
  applyBranding(null);
  const { root: r } = await mountView("login", fragment, () => makeContext({ routeId: "login", state: { email, notice }, auth: authFor(null) }));
  if (token !== viewToken) return;
  root.replaceChildren(r);
  loadHostBranding();
}

function teardownShell() { shell = null; }

async function buildShell() {
  const { root: r, result } = await mountView("shell", fragment,
    () => makeContext({ routeId: "shell", nav: nav(), auth: authFor(null) }),
    { validate: (res, frag) => Boolean(res && res.main instanceof HTMLElement && typeof res.setActive === "function" && typeof res.setNoAccess === "function" && frag.contains(res.main)) });
  root.replaceChildren(r);
  shell = { main: result.main, setActive: result.setActive, setNoAccess: result.setNoAccess, refreshBadge: typeof result.refreshBadge === "function" ? result.refreshBadge : null };
}

async function doLogout() {
  document.querySelectorAll("dialog").forEach((d) => d.close());
  clearSession();
  viewToken++;
  showLogin({ notice: "" });
}

boot();
