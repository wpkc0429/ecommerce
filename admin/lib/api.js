// HTTP 層：token 管理（自動 refresh 輪替）、api() 包裝、統一錯誤型別。
// 自原 app.js 逐字搬移（change admin-commerce-catalog, design D1）。
//
// API_BASE / WEB_BASE 刻意是**執行期**設定（localStorage），不是建置期常數
// ——同一份靜態資源因此可以服務任何環境，這正是 admin 不引入打包器的其中一個
// 理由（spec admin-catalog-ui：後台前端維持無建置步驟的靜態交付形式）。
//
// change admin-api-public-exposure（design D1/D5）：
//   * 預設 API_BASE：頁面不在 localhost 時用 location.origin（正式拓撲＝同源：
//     Cloudflare tunnel 把 /api/v1/admin 轉給 API，其餘給本靜態站）；localhost
//     時維持 http://localhost:8080（本機開發是跨源）。localStorage 仍可覆寫。
//   * 同源 → refresh token 走 HttpOnly cookie（JS 讀不到），refresh／logout 帶
//     X-Admin-Refresh: 1；跨源 → 沿用 body 模式（SameSite=Strict 的 cookie 跨源
//     不會被帶上）。access token 只存在記憶體。
const isLocalHost = ["localhost", "127.0.0.1", "[::1]"].includes(location.hostname);
export const API_BASE = localStorage.getItem("apiBase") ||
  (isLocalHost ? "http://localhost:8080" : location.origin);
export const WEB_BASE = localStorage.getItem("webBase") || "http://localhost:3000";
export const COOKIE_MODE = API_BASE === location.origin;

let accessToken = "";
export const tokens = {
  get access() { return accessToken; },
  // cookie 模式下 refresh token 對 JS 不可見，這裡永遠是空字串。
  get refresh() { return COOKIE_MODE ? "" : (localStorage.getItem("refreshToken") || ""); },
  set(pair) {
    accessToken = pair.access_token;
    if (!COOKIE_MODE && pair.refresh_token) localStorage.setItem("refreshToken", pair.refresh_token);
  },
  clear() {
    accessToken = "";
    localStorage.removeItem("refreshToken");
    localStorage.removeItem("accessToken"); // 舊版殘留
  },
};

// 是否有機會換到新的 access token（cookie 模式無從得知 cookie 是否存在，嘗試即可）。
export function canRefresh() { return COOKIE_MODE || !!tokens.refresh; }

// Access session 過期時，XHR 會被 Cloudflare 導向它的登入頁（跨網域重導 → fetch 丟出
// TypeError，或拿到 HTML）。整頁重新載入就會走 Access 登入。以 sessionStorage 防止
// 在 Access 真的壞掉時無限重載。
function reloadForAccess() {
  if (!COOKIE_MODE) return false;
  try {
    const last = Number(sessionStorage.getItem("accessReloadAt") || 0);
    if (Date.now() - last < 30000) return false;
    sessionStorage.setItem("accessReloadAt", String(Date.now()));
  } catch { /* sessionStorage 不可用就不防迴圈，仍重載一次 */ }
  location.reload();
  return true;
}

async function accessAwareFetch(url, init) {
  let res;
  try {
    res = await fetch(url, init);
  } catch (e) {
    if (reloadForAccess()) return new Promise(() => {});
    throw e;
  }
  const type = res.headers.get("content-type") || "";
  if (COOKIE_MODE && res.status !== 204 && type.includes("text/html") && reloadForAccess()) return new Promise(() => {});
  return res;
}

// 登入。同源 → cookie 模式。登入失敗時 throw。
export async function login(email, password) {
  const res = await accessAwareFetch(`${API_BASE}/api/v1/admin/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ email, password, ...(COOKIE_MODE ? { refresh_transport: "cookie" } : {}) }),
  });
  if (!res.ok) throw new Error(res.status === 429 ? "locked" : "bad credentials");
  tokens.set(await res.json());
}

// 登出：撤銷 refresh token（cookie 模式由伺服器清除 cookie）。
export function logoutRequest() {
  const init = { method: "POST", headers: { "Content-Type": "application/json" } };
  if (COOKIE_MODE) {
    init.headers["X-Admin-Refresh"] = "1";
    init.body = JSON.stringify({});
  } else if (tokens.refresh) {
    init.body = JSON.stringify({ refresh_token: tokens.refresh });
  } else {
    return Promise.resolve();
  }
  return fetch(`${API_BASE}/api/v1/admin/auth/logout`, init).catch(() => {});
}

// onUnauthorized：refresh 失敗時要做什麼（登出並回登入畫面）由 shell 註冊。
// 這裡不直接 import app.js 的 logout()，否則 lib 會反向依賴 shell，形成循環。
let unauthorizedHandler = () => {};
export function onUnauthorized(fn) { unauthorizedHandler = fn; }

let refreshing = null;

export async function refreshTokens() {
  // 合併併發 refresh：refresh token 每次使用即輪替，重複使用會撤銷整鏈。
  if (!refreshing) {
    refreshing = (async () => {
      const headers = { "Content-Type": "application/json" };
      let body;
      if (COOKIE_MODE) {
        headers["X-Admin-Refresh"] = "1";
        body = JSON.stringify({});
      } else {
        body = JSON.stringify({ refresh_token: tokens.refresh });
      }
      const res = await accessAwareFetch(`${API_BASE}/api/v1/admin/auth/refresh`, { method: "POST", headers, body });
      if (!res.ok) throw new Error("refresh failed");
      tokens.set(await res.json());
    })().finally(() => { refreshing = null; });
  }
  return refreshing;
}

// api(): 帶 access token 呼叫；401 時自動 refresh 一次再重試。
export async function api(method, path, body) {
  const doFetch = () =>
    accessAwareFetch(`${API_BASE}/api/v1${path}`, {
      method,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${tokens.access}`,
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  let res = await doFetch();
  if (res.status === 401 && canRefresh()) {
    try {
      await refreshTokens();
      res = await doFetch();
    } catch {
      unauthorizedHandler();
      throw new ApiError(401, { error: { message: "session expired" } });
    }
  }
  // 403 之後確認身分（change platform-account-provisioning）：被停用的帳號，其未過期的
  // access token 在伺服器端會被 RBAC 拒絕（403），而 /admin/me 對它回 401。這時它
  // 該回登入頁，而不是看著一堆 403 錯誤。refresh 已被撤銷，所以走既有的 401 路徑。
  if (res.status === 403 && path !== "/admin/me") {
    const probe = await fetch(`${API_BASE}/api/v1/admin/me`, { headers: { Authorization: `Bearer ${tokens.access}` } });
    if (probe.status === 401) {
      unauthorizedHandler();
      throw new ApiError(401, { error: { message: "account disabled or session expired" } });
    }
  }
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) throw new ApiError(res.status, data);
  return data;
}

export class ApiError extends Error {
  constructor(status, body) {
    super(body?.error?.message || `HTTP ${status}`);
    this.status = status;
    this.body = body;
  }
  get details() { return this.body?.error?.details || []; }
}
