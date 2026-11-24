// 平台視圖「商家」（change platform-account-provisioning）：商家列表、開店精靈、
// 開店檢查清單。平台層，不依賴目前選的商家。
//
// 精靈只是 POST /admin/shop-provisioning 的一個表單——所有規則（單一交易、權限
// 組合、整筆回滾）都在伺服器的 provision.Service。檢查清單每一項都由伺服器依當下
// 資料推導（GET /admin/shops/{id}/setup-status），瀏覽器不儲存、不推算任何進度。
import { api } from "../lib/api.js";
import { $, el, rows, renderPager, clear } from "../lib/dom.js";
import { canPlatform } from "../lib/rbac.js";
import { errText, showLinkOnce } from "./platform-accounts.js";

const PAGE_SIZE = 20;

const CHECKS = [
  ["owner_assigned", "已指派 owner"],
  ["owner_password_set", "owner 已設定密碼"],
  ["home_published", "首頁已發佈"],
  ["shipping_method_active", "至少一個啟用中的運送方式"],
  ["payment_credentials_set", "已設定金流憑證"],
  ["publishable_key_active", "有啟用中的 publishable key"],
];

export function createPlatformShopsView() {
  const state = { page: 1 };

  async function load() {
    $("#platform-shop-result").hidden = true;
    $("#platform-shop-checklist").hidden = true;
    await loadList();
  }

  async function loadList() {
    const err = $("#platform-shops-error");
    err.hidden = true;
    $("#platform-shop-new-btn").hidden = !canPlatform("shop.create");
    let data;
    try {
      data = await api("GET", `/admin/shops?page=${state.page}&page_size=${PAGE_SIZE}`);
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
      return;
    }
    renderPager("platform-shops", data.page, data.total, PAGE_SIZE);
    rows($("#platform-shops-tbody"), data.shops, (s) => el("tr", { "data-name": s.name },
      el("td", {}, s.id),
      el("td", {}, s.name),
      el("td", {}, ({ 0: "停用", 1: "啟用", 2: "審核中" })[s.status] ?? s.status),
      el("td", {}, canPlatform("shop.view")
        ? el("button", { onclick: () => showChecklist(s.id, s.name) }, "檢查清單") : "")),
    { empty: "尚無商家。", colspan: 4 });
  }

  async function showChecklist(shopID, name, host = $("#platform-shop-checklist")) {
    clear(host).hidden = false;
    host.append(el("h3", {}, `開店檢查清單：${name}（#${shopID}）`));
    try {
      const st = await api("GET", `/admin/shops/${shopID}/setup-status`);
      const list = el("ul", { class: "checklist" });
      for (const [key, label] of CHECKS) {
        list.append(el("li", { "data-check": key, "data-done": String(st[key]) },
          st[key] ? el("span", { class: "badge ok" }, "完成") : el("span", { class: "badge warn" }, "待處理"),
          " ", label));
      }
      host.append(list);
    } catch (e) {
      host.append(el("p", { class: "error" }, e.message));
    }
  }

  function openWizard() {
    for (const id of ["name", "origin", "owner"]) $(`#platform-shop-${id}`).value = "";
    $("#platform-shop-error").hidden = true;
    $("#platform-shop-dialog").showModal();
  }

  async function submitWizard(e) {
    e.preventDefault();
    const box = $("#platform-shop-error");
    box.hidden = true;
    const body = { name: $("#platform-shop-name").value.trim(), owner_email: $("#platform-shop-owner").value.trim() };
    const origin = $("#platform-shop-origin").value.trim();
    if (origin) body.storefront_origins = [origin];
    let res;
    try {
      res = await api("POST", "/admin/shop-provisioning", body);
    } catch (err) {
      box.textContent = errText(err);
      box.hidden = false;
      return;
    }
    $("#platform-shop-dialog").close();
    await loadList();
    const host = clear($("#platform-shop-result"));
    host.hidden = false;
    host.append(el("h3", {}, `已開店：${res.shop.name}（#${res.shop.id}）`));
    const linkHost = el("div", { class: "card" });
    host.append(linkHost);
    if (res.owner.created) {
      showLinkOnce(linkHost, { title: `${res.owner.email} 的密碼設定連結（owner）`, link: res.owner });
    } else {
      linkHost.append(el("p", {}, `${res.owner.email} 是既有帳號，已加入為 owner，密碼不變。`));
    }
    const checklist = el("div", { class: "card" });
    host.append(checklist);
    await showChecklist(res.shop.id, res.shop.name, checklist);
  }

  $("#platform-shop-new-btn").addEventListener("click", openWizard);
  $("#platform-shop-cancel").addEventListener("click", () => $("#platform-shop-dialog").close());
  $("#platform-shop-form").addEventListener("submit", submitWizard);
  $("#platform-shops-prev").addEventListener("click", () => { state.page = Math.max(1, state.page - 1); loadList(); });
  $("#platform-shops-next").addEventListener("click", () => { state.page += 1; loadList(); });

  return { load };
}
