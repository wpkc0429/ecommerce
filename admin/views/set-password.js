// 密碼設定頁（change platform-account-provisioning）。
//
// 連結形如 `#/set-password/{token}`，是一份給**所有** admin 前端的契約（見
// docs/admin-frontend-integration.md）。此頁無需登入：先以 inspect 端點顯示帳號
// email，再讓使用者輸入兩次新密碼；成功後**不**直接登入，導回登入頁並預填 email。
//
// token 只存在於這個函式的 closure 與網址的 hash（fragment 不會被送到任何伺服器）。
// **絕不寫入 localStorage 或任何持久儲存。**
import { API_BASE } from "../lib/api.js";
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

export function createSetPasswordView({ toLogin }) {
  let token = "";
  let email = "";

  function invalid() {
    $("#sp-form").hidden = true;
    $("#sp-invalid").hidden = false;
  }

  async function load(tok) {
    token = tok || "";
    email = "";
    $("#sp-invalid").hidden = true;
    $("#sp-error").hidden = true;
    $("#sp-form").hidden = true;
    $("#sp-password").value = "";
    $("#sp-password2").value = "";
    if (!token) return invalid();
    try {
      const res = await post("/auth/password-setup/inspect", { token });
      if (!res.ok) return invalid();
      email = res.data.email;
      $("#sp-email").textContent = email;
      $("#sp-form").hidden = false;
    } catch {
      invalid();
    }
  }

  function showError(msg) {
    const box = $("#sp-error");
    box.textContent = msg;
    box.hidden = false;
  }

  $("#sp-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    $("#sp-error").hidden = true;
    const pw = $("#sp-password").value;
    if (pw !== $("#sp-password2").value) return showError("兩次輸入的密碼不一致。");
    if ([...pw].length < 12) return showError("密碼至少需要 12 個字元。");
    try {
      const res = await post("/auth/password-setup", { token, password: pw });
      if (res.status === 404) return invalid();
      if (!res.ok) return showError(res.data?.error?.message || `設定失敗（HTTP ${res.status}）。`);
    } catch {
      return showError("無法連線到伺服器，請稍後再試。");
    }
    const done = email;
    token = "";
    $("#sp-password").value = "";
    $("#sp-password2").value = "";
    toLogin(done);
  });

  $("#sp-to-login").addEventListener("click", (e) => {
    e.preventDefault();
    toLogin("");
  });

  return { load };
}
