// Auth flows (CORE, locked): login, logout, password setup and invitation acceptance.
// The one-time link token (#/set-password/{token}, #/accept-invite/{token}) lives ONLY in this closure and in the URL
// fragment; it is never exposed to the design layer and never written to any storage. Every method returns plain data
// ({status, message}...) so a screen can render the result; screens never see tokens, URLs or the raw API.
// Logic ported 1:1 from the pre-split views/set-password.js and views/accept-invite.js.
import { API_BASE, api, ApiError, tokens, refreshTokens, login } from "../lib/api.js";
import { t } from "../lib/design.js";

async function post(path, body) {
  const res = await fetch(`${API_BASE}/api/v1/admin${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { /* non-JSON response */ }
  return { status: res.status, ok: res.ok, data };
}

/** Validation shared by both "choose a password" forms; returns a message or "". */
function passwordProblem(pw, pw2) {
  if (pw !== pw2) return t("auth.passwordMismatch");
  if ([...pw].length < 12) return t("auth.passwordTooShort");
  return "";
}

/** hooks = {onLoggedIn(), onLogout(), goLogin(email, notice), afterJoin(prevSids)} supplied by app.js. */
export function makeAuth({ linkToken, hooks }) {
  let token = linkToken || "";
  let email = "";   // password-setup: the account being set up
  let info = null;  // invitation: the inspected invitation

  const passwordSetup = {
    /** -> {valid:false} | {valid:true, email} */
    async inspect() {
      email = "";
      if (!token) return { valid: false };
      try {
        const res = await post("/auth/password-setup/inspect", { token });
        if (!res.ok) return { valid: false };
        email = res.data.email;
        return { valid: true, email };
      } catch {
        return { valid: false };
      }
    },
    /** -> {status:"done"} | {status:"invalid"} | {status:"error", message} */
    async submit(password, confirm) {
      const problem = passwordProblem(password, confirm);
      if (problem) return { status: "error", message: problem };
      try {
        const res = await post("/auth/password-setup", { token, password });
        if (res.status === 404) return { status: "invalid" };
        if (!res.ok) return { status: "error", message: res.data?.error?.message || t("setPassword.failed", { status: res.status }) };
      } catch {
        return { status: "error", message: t("err.network") };
      }
      const done = email;
      token = "";
      hooks.goLogin(done, "");
      return { status: "done" };
    },
  };

  // Accept with the account that is currently signed in. Rotates the tokens so the shop selector includes the new shop.
  async function acceptAsCurrent() {
    try {
      const before = (await api("GET", "/admin/me")).sids || [];
      try {
        await api("POST", "/admin/invitations/accept", { token });
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return { status: "invalid" };
        if (e instanceof ApiError && e.status === 403) return { status: "error", message: t("acceptInvite.notForYou") };
        if (e instanceof ApiError && e.status === 409) return { status: "error", message: t("acceptInvite.alreadyMember") };
        return { status: "error", message: e.message };
      }
      token = "";
      await refreshTokens();
      await hooks.afterJoin(before);
      return { status: "done" };
    } catch {
      return { status: "error", message: t("err.network") };
    }
  }

  const invitation = {
    /** -> {status:"invalid"} | {status:"new"|"login"|"switch", shop, role, email, currentEmail?} */
    async inspect() {
      info = null;
      if (!token) return { status: "invalid" };
      try {
        const res = await post("/auth/invitations/inspect", { token });
        if (!res.ok) return { status: "invalid" };
        info = res.data;
      } catch {
        return { status: "invalid" };
      }
      const base = { shop: info.shop_name, role: info.role_name, email: info.email };
      if (!info.requires_login) return { status: "new", ...base };
      // Existing account: when the signed-in account is that account, accept directly; otherwise ask for a login.
      let me = null;
      if (tokens.access) {
        try { me = await api("GET", "/admin/me"); } catch { me = null; }
      }
      if (me && me.email.toLowerCase() === info.email.toLowerCase()) return { status: "switch", ...base, currentEmail: me.email };
      return { status: "login", ...base };
    },
    /** New account: -> {status:"done"} | {status:"invalid"} | {status:"retry"} (re-inspect) | {status:"error", message} */
    async acceptNew(password, confirm) {
      const problem = passwordProblem(password, confirm);
      if (problem) return { status: "error", message: problem };
      try {
        const res = await post("/auth/invitations/accept", { token, password });
        if (res.status === 404) return { status: "invalid" };
        // Race: the email got an account after we inspected: the screen re-inspects and takes the login branch.
        if (res.status === 409) return { status: "retry" };
        if (!res.ok) return { status: "error", message: res.data?.error?.message || t("acceptInvite.failed", { status: res.status }) };
      } catch {
        return { status: "error", message: t("err.network") };
      }
      const done = info.email;
      token = "";
      hooks.goLogin(done, "");
      return { status: "done" };
    },
    /** Existing account, not signed in: sign in with the invited email, then accept. Same result shapes as acceptAsCurrent. */
    async loginAndJoin(password) {
      try {
        await login(info.email, password);
      } catch {
        return { status: "error", message: t("acceptInvite.loginFailed") };
      }
      return acceptAsCurrent();
    },
    /** Existing account, already signed in as the invited email. */
    acceptAsCurrent,
  };

  return {
    /** Throws when the credentials (or the follow-up session load) fail; the screen shows the old error. */
    async login(emailArg, password) {
      await login(emailArg, password);
      await hooks.onLoggedIn();
    },
    logout: () => hooks.onLogout(),
    /** "back to login" links on the public screens. */
    goLogin: (emailArg = "") => hooks.goLogin(emailArg, ""),
    passwordSetup,
    invitation,
  };
}
