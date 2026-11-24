// HTTP 層：token 管理（自動 refresh 輪替）、api() 包裝、統一錯誤型別。
// 自原 app.js 逐字搬移（change admin-commerce-catalog, design D1）。
//
// API_BASE / WEB_BASE 刻意是**執行期**設定（localStorage），不是建置期常數
// ——同一份靜態資源因此可以服務任何環境，這正是 admin 不引入打包器的其中一個
// 理由（spec admin-catalog-ui：後台前端維持無建置步驟的靜態交付形式）。
export const API_BASE = localStorage.getItem("apiBase") || "http://localhost:8080";
export const WEB_BASE = localStorage.getItem("webBase") || "http://localhost:3000";

export const tokens = {
  get access() { return localStorage.getItem("accessToken") || ""; },
  get refresh() { return localStorage.getItem("refreshToken") || ""; },
  set(pair) {
    localStorage.setItem("accessToken", pair.access_token);
    localStorage.setItem("refreshToken", pair.refresh_token);
  },
  clear() {
    localStorage.removeItem("accessToken");
    localStorage.removeItem("refreshToken");
  },
};

// onUnauthorized：refresh 失敗時要做什麼（登出並回登入畫面）由 shell 註冊。
// 這裡不直接 import app.js 的 logout()，否則 lib 會反向依賴 shell，形成循環。
let unauthorizedHandler = () => {};
export function onUnauthorized(fn) { unauthorizedHandler = fn; }

let refreshing = null;

export async function refreshTokens() {
  // 合併併發 refresh：refresh token 每次使用即輪替，重複使用會撤銷整鏈。
  if (!refreshing) {
    refreshing = (async () => {
      const res = await fetch(`${API_BASE}/api/v1/admin/auth/refresh`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ refresh_token: tokens.refresh }),
      });
      if (!res.ok) throw new Error("refresh failed");
      tokens.set(await res.json());
    })().finally(() => { refreshing = null; });
  }
  return refreshing;
}

// api(): 帶 access token 呼叫；401 時自動 refresh 一次再重試。
export async function api(method, path, body) {
  const doFetch = () =>
    fetch(`${API_BASE}/api/v1${path}`, {
      method,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${tokens.access}`,
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  let res = await doFetch();
  if (res.status === 401 && tokens.refresh) {
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
