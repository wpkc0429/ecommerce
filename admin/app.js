// Ecommerce Admin — 薄殼 SPA 的 shell（change admin-commerce-catalog, design D1）。
//
// 這個檔案只做四件事：登入/登出、商家選擇、視圖註冊表、分頁切換。所有畫面邏輯
// 住在 views/*.js，共用詞彙住在 lib/*.js——全部是**瀏覽器原生 ES module**，
// 沒有打包器、沒有轉譯器、沒有任何建置步驟。「無建置步驟」不等於「一個檔案」。
//
// 新增一個視圖 = 在 index.html 加一個 .tab-panel 容器 + 在下方 views 註冊表加
// 一列。第 8 個分頁的成本與第 4 個相同（原本的 switchTab 是三行硬編碼的
// `$("#tab-x").hidden = ...`，那才是撐不到 8 個的東西）。
import { api, tokens, API_BASE, onUnauthorized } from "./lib/api.js";
import { $, el } from "./lib/dom.js";
import { loadPermissions, can, permissionError } from "./lib/rbac.js";
import { createPagesView } from "./views/pages.js";
import { createContentView } from "./views/content.js";
import { createCategoriesView } from "./views/categories.js";
import { createProductsView } from "./views/products.js";

const state = { me: null, shopID: null };

const ctx = {
  getShopID: () => state.shopID,
  showTab,
};

// 視圖註冊表。`nav` 為 false 者不出現在導覽（編輯器由其列表視圖開啟）；
// `perm` 是進入該視圖所需的權限節點（design D5：UI 依實際權限決定可見性，
// 但伺服器端永遠是唯一權威）。
const views = {
  pages: { label: "頁面", panel: "tab-pages", nav: true, perm: "page.view", view: createPagesView(ctx) },
  editor: { panel: "tab-editor", nav: false },
  content: { label: "全域內容", panel: "tab-content", nav: true, perm: "shop.view", view: createContentView(ctx) },
  products: { label: "商品", panel: "tab-products", nav: true, perm: "product.view", view: createProductsView(ctx) },
  "product-editor": { panel: "tab-product-editor", nav: false },
  categories: { label: "分類", panel: "tab-categories", nav: true, perm: "category.view", view: createCategoriesView(ctx) },
};

function show(viewID) {
  document.querySelectorAll(".view").forEach((v) => (v.hidden = v.id !== viewID));
}

function showTab(name) {
  const def = views[name];
  if (!def) return;
  document.querySelectorAll(".tab-panel").forEach((p) => (p.hidden = p.id !== def.panel));
  document.querySelectorAll(".tab").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
  def.view?.load?.();
}

function renderNav() {
  const nav = $("#tabs");
  nav.textContent = "";
  let first = null;
  for (const [name, def] of Object.entries(views)) {
    if (!def.nav || (def.perm && !can(def.perm))) continue;
    nav.append(el("button", { class: "tab", "data-tab": name, onclick: () => showTab(name) }, def.label));
    if (!first) first = name;
  }
  return first;
}

function logout() {
  if (tokens.refresh) {
    fetch(`${API_BASE}/api/v1/admin/auth/logout`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refresh_token: tokens.refresh }),
    }).catch(() => {});
  }
  tokens.clear();
  show("view-login");
}
onUnauthorized(logout);

// 切換商家後權限必須重新判定——權限依商家而異（spec admin-catalog-ui）。
async function refreshShopContext() {
  await loadPermissions(state.shopID);
  const first = renderNav();
  const notice = $("#no-access");
  if (first) {
    notice.hidden = true;
    showTab(first);
    return;
  }
  document.querySelectorAll(".tab-panel").forEach((p) => (p.hidden = true));
  notice.textContent = permissionError()
    ? `無法取得你在此商家的權限（${permissionError().message}）。請確認商家編號，或重新登入。`
    : "你在這個商家沒有任何可用的功能。";
  notice.hidden = false;
}

function enterMain() {
  show("view-main");
  $("#whoami").textContent = state.me.email;
  const sel = $("#shop-select");
  sel.textContent = "";
  const sids = state.me.sids || [];
  if (sids.length > 0) {
    for (const id of sids) sel.append(el("option", { value: id }, `Shop #${id}`));
    state.shopID = sids[0];
    sel.hidden = false;
    $("#shop-input").hidden = true;
  } else {
    // 平台管理員無 sids 提示 → 手動輸入目標 shop。
    sel.hidden = true;
    const input = $("#shop-input");
    input.hidden = false;
    state.shopID = Number(localStorage.getItem("shopID") || 1);
    input.value = state.shopID;
  }
  return refreshShopContext();
}

async function boot() {
  if (tokens.access) {
    try {
      state.me = await api("GET", "/admin/me");
      await enterMain();
      return;
    } catch { tokens.clear(); }
  }
  show("view-login");
}

$("#login-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  $("#login-error").hidden = true;
  try {
    const res = await fetch(`${API_BASE}/api/v1/admin/auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: $("#login-email").value, password: $("#login-password").value }),
    });
    if (!res.ok) throw new Error("bad credentials");
    tokens.set(await res.json());
    state.me = await api("GET", "/admin/me");
    await enterMain();
  } catch {
    $("#login-error").hidden = false;
  }
});

$("#logout-btn").addEventListener("click", logout);

$("#shop-select").addEventListener("change", (e) => {
  state.shopID = Number(e.target.value);
  refreshShopContext();
});
$("#shop-input").addEventListener("change", (e) => {
  state.shopID = Number(e.target.value);
  localStorage.setItem("shopID", String(state.shopID));
  refreshShopContext();
});

boot();
