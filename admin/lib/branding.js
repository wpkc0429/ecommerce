// 每商家的後台品牌（change admin-api-public-exposure, design D8）。
//
// 伺服器只提供資料（logo、兩個顏色）；這裡把它套成 CSS 變數與 <img>。自訂的後台
// 前端可以完全忽略這個端點——API 不依前端身分分支。
import { API_BASE, api } from "./api.js";

const root = document.documentElement;
const HEX = /^#[0-9a-fA-F]{6}$/;

// 未設定 ＝ 回到 styles.css 的預設值（移除覆寫，而不是寫入預設色）。
export function applyBranding(branding) {
  const b = branding || {};
  for (const [name, value] of [["--brand-primary", b.primary_color], ["--brand-accent", b.accent_color]]) {
    if (HEX.test(value || "")) root.style.setProperty(name, value);
    else root.style.removeProperty(name);
  }
  for (const img of document.querySelectorAll(".brand-logo")) {
    const ok = /^https?:\/\//.test(b.logo_url || "");
    img.hidden = !ok;
    if (ok) img.src = b.logo_url; else img.removeAttribute("src");
  }
}

// 登入前：伺服器以請求的 admin 主機解析商家。未登記的主機回 404 → 用平台預設。
export async function loadHostBranding() {
  try {
    const res = await fetch(`${API_BASE}/api/v1/admin/branding`);
    if (!res.ok) return applyBranding(null);
    applyBranding((await res.json()).branding);
  } catch {
    applyBranding(null);
  }
}

// 登入後（切換商家時）：成員可讀，不需權限節點。
export async function loadShopBranding(shopID) {
  try {
    applyBranding((await api("GET", `/admin/shops/${shopID}/branding`)).branding);
  } catch {
    applyBranding(null);
  }
}
