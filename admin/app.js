// Ecommerce Admin — 薄殼 SPA 的 shell（change admin-commerce-catalog, design D1）。
//
// 這個檔案只做四件事：登入/登出、商家選擇、視圖註冊表、路由。所有畫面邏輯
// 住在 views/*.js，共用詞彙住在 lib/*.js——全部是**瀏覽器原生 ES module**，
// 沒有打包器、沒有轉譯器、沒有任何建置步驟。「無建置步驟」不等於「一個檔案」。
//
// 新增一個視圖 = 在 index.html 加一個 .tab-panel 容器 + 在下方 views 註冊表加
// 一列。第 12 個分頁的成本與第 4 個相同。
//
// change admin-commerce-operations（design D5）把分頁切換換成 hash 路由：
// showTab() 只負責寫 hash，真正的畫面切換發生在單一的路由處理器裡。兩份真相
// （「現在在哪個分頁」與「網址說在哪個分頁」）會立刻漂移，所以只留一份。
import { api, tokens, API_BASE, onUnauthorized } from "./lib/api.js";
import { $, el } from "./lib/dom.js";
import { loadPermissions, can, permissionError } from "./lib/rbac.js";
import { parseHash, navigate, onRoute } from "./lib/router.js";
import { createPagesView } from "./views/pages.js";
import { createContentView } from "./views/content.js";
import { createCategoriesView } from "./views/categories.js";
import { createProductsView } from "./views/products.js";
import { createOrdersView } from "./views/orders.js";
import { createShippingMethodsView } from "./views/shipping-methods.js";
import { createMemberTiersView } from "./views/member-tiers.js";
import { createPointsView } from "./views/points.js";
import { createStorefrontClientsView } from "./views/storefront-clients.js";

const state = { me: null, shopID: null };

const ctx = {
  getShopID: () => state.shopID,
  showTab,
  showPanel,
};

// 視圖註冊表。
//   nav:false       → 不出現在導覽（編輯器由其列表視圖開啟）
//   transient:true  → 沒有自己的載入器，內容由開啟它的視圖預先填好；因此**開機時
//                     不得路由過去**（重新整理會停在一個空畫面）。導覽期間照常。
//   perm            → 進入該視圖所需的權限節點（design D5：UI 依實際權限決定
//                     可見性，但伺服器端永遠是唯一權威）
//   perms           → 任一成立即可見（用於一個分頁承載兩種權限層級的情形）
const views = {
  pages: { label: "頁面", panel: "tab-pages", nav: true, perm: "page.view", view: createPagesView(ctx) },
  editor: { panel: "tab-editor", nav: false, transient: true },
  content: { label: "全域內容", panel: "tab-content", nav: true, perm: "shop.view", view: createContentView(ctx) },
  orders: { label: "訂單", panel: "tab-orders", nav: true, perm: "order.view", view: createOrdersView(ctx) },
  products: { label: "商品", panel: "tab-products", nav: true, perm: "product.view", view: createProductsView(ctx) },
  "product-editor": { panel: "tab-product-editor", nav: false, transient: true },
  categories: { label: "分類", panel: "tab-categories", nav: true, perm: "category.view", view: createCategoriesView(ctx) },
  "shipping-methods": {
    label: "運送方式", panel: "tab-shipping-methods", nav: true,
    perm: "shipping_method.view", view: createShippingMethodsView(ctx),
  },
  "member-tiers": {
    label: "會員等級", panel: "tab-member-tiers", nav: true,
    perm: "member_tier.view", view: createMemberTiersView(ctx),
  },
  points: { label: "會員點數", panel: "tab-points", nav: true, perm: "point.view", view: createPointsView(ctx) },
  // 前端接入：金鑰是商家層、origin 是平台層（design D6）。分頁只要其中一邊看得到
  // 就該出現，區塊各自再判一次。
  "storefront-clients": {
    label: "前端接入", panel: "tab-storefront-clients", nav: true,
    perms: ["publishable_key.view", "shop.manage_client_origins"],
    view: createStorefrontClientsView(ctx),
  },
};

function allowed(def) {
  if (!def) return false;
  if (def.perms) return def.perms.some((p) => can(p));
  return !def.perm || can(def.perm);
}

function show(viewID) {
  document.querySelectorAll(".view").forEach((v) => (v.hidden = v.id !== viewID));
}

function showPanel(panelID) {
  document.querySelectorAll(".tab-panel").forEach((p) => (p.hidden = p.id !== panelID));
}

/** 導航。hash 沒變時瀏覽器不發 hashchange，所以自己渲染一次。 */
function showTab(name, ...params) {
  if (!navigate(name, ...params)) renderRoute({ name, params });
}

/** 唯一的畫面切換入口——hashchange 與「hash 未變」的直接呼叫都走這裡。 */
function renderRoute({ name, params }) {
  const def = views[name];
  if (!def || !allowed(def)) {
    // 過期的書籤、無權限的連結、拼錯的 hash——一律安靜地回落到一個進得去的
    // 畫面。一個過期的書籤不該長得像故障（spec admin-operations-ui）。
    const first = firstAllowedView();
    if (first && first !== name) showTab(first);
    return;
  }
  showPanel(def.panel);
  document.querySelectorAll(".tab").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
  def.view?.load?.(...params);
}

function firstAllowedView() {
  for (const [name, def] of Object.entries(views)) {
    if (def.nav && allowed(def)) return name;
  }
  return null;
}

function renderNav() {
  const nav = $("#tabs");
  nav.textContent = "";
  for (const [name, def] of Object.entries(views)) {
    if (!def.nav || !allowed(def)) continue;
    nav.append(el("button", { class: "tab", "data-tab": name, onclick: () => showTab(name) }, def.label));
  }
  return firstAllowedView();
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
//
// honorHash 只在開機時為真：那是深層連結唯一該被採信的時刻。切換商家時參數
// （訂單編號、會員編號）屬於**上一家店**，帶著它們過去只會得到一個 404 畫面，
// 所以只保留視圖名稱。
async function refreshShopContext(honorHash) {
  await loadPermissions(state.shopID);
  const first = renderNav();
  const notice = $("#no-access");
  if (!first) {
    showPanel(null);
    notice.textContent = permissionError()
      ? `無法取得你在此商家的權限（${permissionError().message}）。請確認商家編號，或重新登入。`
      : "你在這個商家沒有任何可用的功能。";
    notice.hidden = false;
    return;
  }
  notice.hidden = true;
  const route = parseHash();
  const def = views[route.name];
  if (honorHash && def && allowed(def) && !def.transient) {
    showTab(route.name, ...route.params);
    return;
  }
  if (!honorHash && def && allowed(def) && !def.transient) {
    showTab(route.name); // 同一個視圖，換一家店，不帶參數
    return;
  }
  showTab(first);
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
  return refreshShopContext(true);
}

onRoute(renderRoute);

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
  refreshShopContext(false);
});
$("#shop-input").addEventListener("change", (e) => {
  state.shopID = Number(e.target.value);
  localStorage.setItem("shopID", String(state.shopID));
  refreshShopContext(false);
});

boot();
