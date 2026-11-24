// 邀請接受頁（change merchant-staff-role）。
//
// 連結形如 `#/accept-invite/{token}`，是給**所有** admin 前端的契約（見
// docs/admin-frontend-integration.md）。此頁無需登入：先以 inspect 端點顯示商家、
// 角色與受邀 email，再依 `requires_login` 分流——
//   false → 該 email 還沒有後台帳號：設定密碼並接受（成功後導回登入頁）。
//   true  → 該 email 已有帳號：**邀請連結永遠不能替既有帳號設定密碼**，所以只能
//           以那個帳號登入後接受；接受成功後換發 token 並切到該商家。
//
// token 只存在於這個函式的 closure 與網址的 hash。**絕不寫入任何持久儲存。**
import { API_BASE, api, ApiError, tokens, refreshTokens } from "../lib/api.js";
import { $ } from "../lib/dom.js";

async function post(path, body) {
  const res = await fetch(`${API_BASE}/api/v1/admin${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { /* 非 JSON 回應 */ }
  return { status: res.status, ok: res.ok, data };
}

export function createAcceptInviteView({ toLogin, toMain }) {
  let token = "";
  let info = null;

  const sections = ["#ai-invalid", "#ai-new-form", "#ai-login-form", "#ai-switch", "#ai-info"];
  function only(...ids) {
    for (const s of sections) $(s).hidden = !ids.includes(s);
  }
  function showError(msg) {
    const box = $("#ai-error");
    box.textContent = msg || "";
    box.hidden = !msg;
  }
  function invalid() { only("#ai-invalid"); }

  async function load(tok) {
    token = tok || "";
    info = null;
    showError("");
    only();
    for (const id of ["#ai-password", "#ai-password2", "#ai-login-password"]) $(id).value = "";
    if (!token) return invalid();
    try {
      const res = await post("/auth/invitations/inspect", { token });
      if (!res.ok) return invalid();
      info = res.data;
    } catch {
      return invalid();
    }
    $("#ai-shop").textContent = info.shop_name;
    $("#ai-role").textContent = info.role_name;
    $("#ai-email").textContent = info.email;
    if (!info.requires_login) return only("#ai-info", "#ai-new-form");

    // 已有帳號：若目前已登入的正是那個帳號，直接接受；否則要求登入。
    let me = null;
    if (tokens.access) {
      try { me = await api("GET", "/admin/me"); } catch { me = null; }
    }
    if (me && me.email.toLowerCase() === info.email.toLowerCase()) {
      $("#ai-switch-email").textContent = me.email;
      return only("#ai-info", "#ai-switch");
    }
    $("#ai-login-email").textContent = info.email;
    only("#ai-info", "#ai-login-form");
  }

  // 以已登入的（受邀）帳號接受。成功後換發 token，讓商家選單包含新商家。
  async function acceptAsCurrent() {
    const before = (await api("GET", "/admin/me")).sids || [];
    try {
      await api("POST", "/admin/invitations/accept", { token });
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return invalid();
      if (e instanceof ApiError && e.status === 403) return showError("這份邀請不是發給目前登入的帳號。");
      if (e instanceof ApiError && e.status === 409) return showError("這個帳號已經是該商家的成員。");
      return showError(e.message);
    }
    token = "";
    await refreshTokens();
    await toMain(before);
  }

  $("#ai-new-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    showError("");
    const pw = $("#ai-password").value;
    if (pw !== $("#ai-password2").value) return showError("兩次輸入的密碼不一致。");
    if ([...pw].length < 12) return showError("密碼至少需要 12 個字元。");
    try {
      const res = await post("/auth/invitations/accept", { token, password: pw });
      if (res.status === 404) return invalid();
      if (res.status === 409) {
        // 競態：這個 email 在我們檢視之後被建立了帳號——改走登入分支。
        return load(token);
      }
      if (!res.ok) return showError(res.data?.error?.message || `接受失敗（HTTP ${res.status}）。`);
    } catch {
      return showError("無法連線到伺服器，請稍後再試。");
    }
    const done = info.email;
    token = "";
    for (const id of ["#ai-password", "#ai-password2"]) $(id).value = "";
    toLogin(done);
  });

  $("#ai-login-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    showError("");
    try {
      const res = await post("/auth/login", { email: info.email, password: $("#ai-login-password").value });
      if (!res.ok) return showError("登入失敗，請確認密碼。");
      tokens.set(res.data);
      $("#ai-login-password").value = "";
      await acceptAsCurrent();
    } catch {
      showError("無法連線到伺服器，請稍後再試。");
    }
  });

  $("#ai-switch-btn").addEventListener("click", async () => {
    showError("");
    try { await acceptAsCurrent(); } catch { showError("無法連線到伺服器，請稍後再試。"); }
  });

  $("#ai-to-login").addEventListener("click", (e) => {
    e.preventDefault();
    toLogin("");
  });

  return { load };
}
