// 平台視圖「帳號」（change platform-account-provisioning）。
//
// 平台層：不依賴目前選的商家（shopID）。可見性來源是 /admin/me 的
// platform_permissions（體驗層；伺服器的 RequirePlatformPermission 才是權威）。
//
// 會產生連結的操作在 delivery = "manual" 時把連結顯示**一次**（showLinkOnce）。
// 連結只存在於那個容器的 DOM 裡：重新載入、換頁、再次操作都會清掉它，**不寫入任何
// 持久儲存**；要再給一次只能重新產生。
import { api, ApiError } from "../lib/api.js";
import { $, el, rows, renderPager, clear } from "../lib/dom.js";
import { canPlatform } from "../lib/rbac.js";

const PAGE_SIZE = 20;

export function errText(err) {
  return err.message +
    (err instanceof ApiError && err.details.length
      ? `（${err.details.map((d) => `${d.pointer} ${d.message}`).join("；")}）` : "");
}

/** 把一次性連結顯示在 host 內（只此一次）；delivery 為 email 時不會有連結可顯示。 */
export function showLinkOnce(host, { title, link }) {
  clear(host);
  host.hidden = false;
  host.append(el("h3", {}, title));
  const url = link.setup_url || link.reset_url;
  if (link.delivery !== "manual" || !url) {
    host.append(el("p", {}, "連結已由系統寄給本人，這裡不會顯示。"));
    return;
  }
  const hours = Math.max(1, Math.round((new Date(link.expires_at) - Date.now()) / 3600000));
  const input = el("input", { type: "text", readonly: "", value: url, class: "link-once" });
  const copy = el("button", { type: "button" }, "複製連結");
  copy.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(url);
      copy.textContent = "已複製";
    } catch {
      input.select();
    }
  });
  host.append(
    el("p", { class: "hint" },
      `此連結只顯示這一次，約 ${hours} 小時後失效，使用一次即作廢。請以安全管道交給本人；離開此畫面後無法再取回（只能重新產生）。`),
    input, copy);
}

export function createPlatformAccountsView() {
  const state = { page: 1, q: "", roles: null, detailID: null };

  async function merchantRoles() {
    if (state.roles) return state.roles;
    try {
      const data = await api("GET", "/admin/roles");
      state.roles = (data.roles || []).filter((r) => r.scope === "merchant");
    } catch {
      state.roles = [];
    }
    return state.roles;
  }

  function roleSelect(roles, withNone) {
    const sel = el("select", {});
    if (withNone) sel.append(el("option", { value: "" }, "（不指派）"));
    for (const r of roles) sel.append(el("option", { value: String(r.id) }, r.name));
    return sel;
  }

  async function load() {
    clear($("#platform-account-link")).hidden = true;
    clear($("#platform-account-detail")).hidden = true;
    state.detailID = null;
    await loadList();
  }

  async function loadList() {
    const err = $("#platform-accounts-error");
    err.hidden = true;
    $("#platform-account-new-btn").hidden = !canPlatform("account.manage");
    let data;
    try {
      const qs = new URLSearchParams({ page: state.page, page_size: PAGE_SIZE });
      if (state.q) qs.set("q", state.q);
      data = await api("GET", `/admin/accounts?${qs}`);
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
      return;
    }
    renderPager("platform-accounts", data.page, data.total, PAGE_SIZE);
    rows($("#platform-accounts-tbody"), data.accounts, (a) => el("tr", { "data-email": a.email },
      el("td", {}, a.id),
      el("td", {}, a.email),
      el("td", {}, a.status === 1
        ? el("span", { class: "badge ok" }, "啟用中")
        : el("span", { class: "badge warn" }, "已停用")),
      el("td", {}, a.has_password ? "已設定" : el("span", { class: "badge" }, "尚未設定")),
      el("td", {}, el("button", { onclick: () => openDetail(a.id) }, "詳情"))),
    { empty: "沒有符合的帳號。", colspan: 5 });
  }

  async function openDetail(id) {
    state.detailID = id;
    const host = $("#platform-account-detail");
    clear(host).hidden = false;
    let d;
    try {
      d = await api("GET", `/admin/accounts/${id}`);
    } catch (e) {
      host.append(el("p", { class: "error" }, e.message));
      return;
    }
    const manage = canPlatform("account.manage");
    const status = el("p", { class: "error", hidden: "" });
    const act = async (fn) => {
      status.hidden = true;
      try { await fn(); } catch (e) { status.textContent = errText(e); status.hidden = false; }
    };

    host.append(
      el("h3", {}, `帳號 #${d.id}　${d.email}`),
      el("p", {}, d.status === 1 ? "狀態：啟用中" : "狀態：已停用", "　｜　",
        d.has_password ? "密碼：已設定" : "密碼：尚未設定（無法登入）"),
      el("p", {}, "平台角色：", d.platform_roles.length ? d.platform_roles.map((r) => r.name).join("、") : "（無）"),
      status);

    if (manage) {
      host.append(el("div", { class: "toolbar" },
        d.status === 1
          ? el("button", { class: "danger", "data-act": "disable", onclick: () => act(async () => {
            if (!confirm(`確定停用 ${d.email}？其已登入的工作階段會立即失效。`)) return;
            await api("POST", `/admin/accounts/${d.id}/disable`);
            await refresh(d.id);
          }) }, "停用")
          : el("button", { "data-act": "enable", onclick: () => act(async () => {
            await api("POST", `/admin/accounts/${d.id}/enable`);
            await refresh(d.id);
          }) }, "重新啟用"),
        el("button", { "data-act": "reset", onclick: () => act(async () => {
          const link = await api("POST", `/admin/accounts/${d.id}/password-reset`);
          showLinkOnce($("#platform-account-link"), { title: `${d.email} 的密碼重設連結`, link });
        }) }, "產生密碼重設連結")));
    }

    host.append(el("h4", {}, "商家成員資格"));
    const tbody = el("tbody", {});
    host.append(el("table", { class: "table" },
      el("thead", {}, el("tr", {}, el("th", {}, "商家"), el("th", {}, "角色"), el("th", {}, "動作"))),
      tbody));
    rows(tbody, d.memberships, (m) => el("tr", {},
      el("td", {}, `#${m.shop_id} ${m.shop_name}`),
      el("td", {}, m.roles.map((r) => r.name).join("、") || "（無角色）"),
      el("td", {}, manage ? el("button", { class: "danger", onclick: () => act(async () => {
        if (!confirm(`確定將 ${d.email} 移出商家 #${m.shop_id}？該店的角色指派一併移除。`)) return;
        await api("DELETE", `/admin/accounts/${d.id}/memberships/${m.shop_id}`);
        await refresh(d.id);
      }) }, "移除") : "")),
    { empty: "尚未加入任何商家。", colspan: 3 });

    if (manage) {
      const roles = await merchantRoles();
      const shopInput = el("input", { type: "number", min: "1", placeholder: "商家 ID" });
      const sel = roleSelect(roles, false);
      host.append(el("div", { class: "inline-form" },
        el("label", { class: "inline" }, "商家 ID ", shopInput),
        el("label", { class: "inline" }, "角色 ", sel),
        el("button", { class: "primary", onclick: () => act(async () => {
          await api("POST", `/admin/accounts/${d.id}/memberships`,
            { shop_id: Number(shopInput.value), role_id: Number(sel.value) });
          await refresh(d.id);
        }) }, "加入商家")));
    }
  }

  async function refresh(id) {
    await loadList();
    await openDetail(id);
  }

  async function openCreate() {
    const roles = await merchantRoles();
    const sel = $("#platform-account-role");
    sel.replaceWith(Object.assign(roleSelect(roles, true), { id: "platform-account-role" }));
    $("#platform-account-email").value = "";
    $("#platform-account-shop").value = "";
    $("#platform-account-error").hidden = true;
    $("#platform-account-dialog").showModal();
  }

  async function submitCreate(e) {
    e.preventDefault();
    const box = $("#platform-account-error");
    box.hidden = true;
    const body = { email: $("#platform-account-email").value.trim() };
    const shop = $("#platform-account-shop").value;
    const role = $("#platform-account-role").value;
    if (shop || role) {
      body.shop_id = Number(shop);
      body.role_id = Number(role);
    }
    try {
      const created = await api("POST", "/admin/accounts", body);
      $("#platform-account-dialog").close();
      await loadList();
      showLinkOnce($("#platform-account-link"), { title: `${created.email} 的密碼設定連結`, link: created });
    } catch (err) {
      box.textContent = errText(err);
      box.hidden = false;
    }
  }

  $("#platform-account-new-btn").addEventListener("click", openCreate);
  $("#platform-account-cancel").addEventListener("click", () => $("#platform-account-dialog").close());
  $("#platform-account-form").addEventListener("submit", submitCreate);
  $("#platform-account-search").addEventListener("click", () => {
    state.q = $("#platform-account-q").value.trim();
    state.page = 1;
    loadList();
  });
  $("#platform-accounts-prev").addEventListener("click", () => { state.page = Math.max(1, state.page - 1); loadList(); });
  $("#platform-accounts-next").addEventListener("click", () => { state.page += 1; loadList(); });

  return { load };
}
