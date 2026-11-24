// 呼叫者在當前商家的有效權限（change admin-commerce-catalog, design D5）。
//
// 來源是 GET /api/v1/admin/shops/{shopID}/me——它回的是三層判定**之後**的節點
// 集合（個人覆蓋優先於角色聯集），不是角色權限的原始聯集。
//
// **這是體驗層，不是安全邊界。** 伺服器端的 RequireShopPermission 永遠是唯一
// 權威；這裡的判斷只決定「要不要畫這顆按鈕」。權限集合過期最壞的後果，是使用
// 者看到一顆按下去回 403 的按鈕——不會造成越權。
import { api } from "./api.js";

let permissions = new Set();
let loadError = null;

/** 登入後與**每次切換商家後**都必須呼叫——權限依商家而異。 */
export async function loadPermissions(shopID) {
  permissions = new Set();
  loadError = null;
  try {
    const data = await api("GET", `/admin/shops/${shopID}/me`);
    permissions = new Set(data.permissions || []);
  } catch (err) {
    // 失敗一律視為「沒有權限」（fail closed）。shell 會據此顯示說明，
    // 而不是留下一個沒有任何解釋的空白後台。
    loadError = err;
  }
  return permissions;
}

export function can(node) { return permissions.has(node); }

// 平台層權限（change platform-account-provisioning, design D9）：來源是
// GET /admin/me 的 platform_permissions，與商家無關。同樣只是體驗層。
let platformPermissions = new Set();
export function setPlatformPermissions(list) { platformPermissions = new Set(list || []); }
export function canPlatform(node) { return platformPermissions.has(node); }

export function permissionError() { return loadError; }

/** 有權限才產生這個節點；沒有就回傳 ""（el() 會忽略空字串以外的 falsy，
 *  空字串則變成一個無害的空文字節點——與原 app.js 的既有慣用法一致）。 */
export function whenCan(node, factory) { return can(node) ? factory() : ""; }

/** 無 `node` 權限時把整個容器內的輸入與按鈕設為 disabled（唯讀模式）。 */
export function readOnlyUnless(node, container) {
  if (can(node) || !container) return false;
  container.querySelectorAll("input, select, textarea, button").forEach((n) => (n.disabled = true));
  return true;
}
