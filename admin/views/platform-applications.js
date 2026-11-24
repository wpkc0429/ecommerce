// 平台視圖「開店申請」（change platform-website）：審核官網送來的商家申請。
// 平台層，不依賴目前選的商家。
//
// 規則全在伺服器（internal/application）：狀態轉移、核准即開店的單一交易、權限組合。
// 這個視圖只負責呈現與送出——不推算任何狀態。核准後 owner 的密碼設定連結只顯示
// 一次（showLinkOnce，不寫入任何儲存）；它不存在於任何 outbox 訊息裡。
import { api } from "../lib/api.js";
import { $, el, rows, renderPager, clear } from "../lib/dom.js";
import { canPlatform } from "../lib/rbac.js";
import { formatTime } from "../lib/format.js";
import { errText, showLinkOnce } from "./platform-accounts.js";

const PAGE_SIZE = 20;

const STATUS = {
  pending_verification: ["待驗證信箱", "warn"],
  pending_review: ["待審核", "warn"],
  approved: ["已核准", "ok"],
  rejected: ["已拒絕", ""],
  expired: ["已過期", ""],
  withdrawn: ["已撤回", ""],
};
const FLAGS = {
  filled_too_fast: "填表過快",
  email_has_account: "已有後台帳號",
  manually_verified: "人工確認聯絡方式",
};
const EVENTS = {
  submitted: "送出申請", verification_sent: "產生驗證信", verified: "信箱已驗證", verified_manually: "人工確認聯絡方式",
  approved: "核准並開店", approve_failed: "核准失敗（已回滾）", rejected: "拒絕", message_marked_sent: "標記訊息已寄出",
  expired: "過期", purged: "個資已清除",
};
const MSG_STATUS = { pending_manual: "待人工寄出", queued: "排隊寄送中", sent: "已寄出", failed: "寄送失敗" };

const badge = (status) => {
  const [label, cls] = STATUS[status] || [status, ""];
  return el("span", { class: `badge ${cls}`.trim() }, label);
};

/** 導覽列上的待處理徽章：待審核申請數 ＋ 待人工寄出的訊息數。 */
export async function refreshApplicationsBadge() {
  const tab = document.querySelector('[data-tab="platform-applications"]');
  if (!tab) return;
  tab.querySelector(".nav-count")?.remove();
  try {
    const d = await api("GET", "/admin/shop-applications?page_size=1");
    const n = (d.counts.pending_review || 0) + (d.pending_messages || 0);
    if (n > 0) tab.append(el("span", { class: "badge warn nav-count" }, String(n)));
  } catch { /* 徽章只是便利，不報錯 */ }
}

export function createPlatformApplicationsView() {
  const state = { page: 1, status: "", q: "", detailID: null, detail: null };

  async function load() {
    clear($("#platform-application-result")).hidden = true;
    clear($("#platform-application-detail")).hidden = true;
    state.detailID = null;
    await loadList();
  }

  async function loadList() {
    const err = $("#platform-applications-error");
    err.hidden = true;
    let data;
    try {
      const qs = new URLSearchParams({ page: state.page, page_size: PAGE_SIZE });
      if (state.status) qs.set("status", state.status);
      if (state.q) qs.set("q", state.q);
      data = await api("GET", `/admin/shop-applications?${qs}`);
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
      return;
    }
    renderPager("platform-applications", state.page, data.total, PAGE_SIZE);
    $("#platform-applications-summary").textContent =
      `待審核 ${data.counts.pending_review || 0} 筆　待驗證信箱 ${data.counts.pending_verification || 0} 筆　` +
      `待人工寄出的訊息 ${data.pending_messages} 則`;
    rows($("#platform-applications-tbody"), data.items, (a) => el("tr", { "data-id": a.id, "data-email": a.email },
      el("td", {}, a.id),
      el("td", {}, badge(a.status), " ",
        a.status === "pending_verification" || a.email_verified ? "" : el("span", { class: "badge" }, "未驗證")),
      el("td", {}, a.shop_name || "（已清除）"),
      el("td", {}, a.contact_name || "", el("br"), el("small", {}, a.email)),
      el("td", {}, Object.keys(a.risk_flags).filter((k) => a.risk_flags[k]).map((k) => FLAGS[k] || k).join("、")),
      el("td", {}, formatTime(a.created_at)),
      el("td", {}, el("button", { "data-act": "open", onclick: () => openDetail(a.id) }, "檢視"))),
    { empty: "沒有符合的申請。", colspan: 7 });
    refreshApplicationsBadge();
  }

  async function openDetail(id) {
    state.detailID = id;
    const host = $("#platform-application-detail");
    clear(host).hidden = false;
    let d;
    try {
      d = await api("GET", `/admin/shop-applications/${id}`);
    } catch (e) {
      host.append(el("p", { class: "error" }, e.message));
      return;
    }
    state.detail = d;
    const status = el("p", { class: "error", hidden: "" });
    const act = async (fn) => {
      status.hidden = true;
      try { await fn(); } catch (e) { status.textContent = errText(e); status.hidden = false; }
    };
    const review = canPlatform("shop_application.review");
    const open = d.status === "pending_verification" || d.status === "pending_review";

    host.append(
      el("h3", {}, `申請 #${d.id}　`, badge(d.status)),
      ...[
        ["店名", d.shop_name],
        ["聯絡人", d.contact_name],
        ["Email", `${d.email}${d.email_verified ? "（已驗證）" : "（未驗證）"}`],
        ["電話", d.phone || "—"],
        ["業務摘要", d.business_summary],
        ["同意版本", `${d.consent_version}（${formatTime(d.consented_at)}）`],
        ["風險旗標", Object.keys(d.risk_flags).filter((k) => d.risk_flags[k]).map((k) => FLAGS[k] || k).join("、") || "—"],
        ...(d.decision_reason ? [["決定理由（申請者可見）", d.decision_reason]] : []),
        ...(d.internal_note ? [["內部備註", d.internal_note]] : []),
        ...(d.provisioned_shop_id ? [["已開店", `商家 #${d.provisioned_shop_id}`]] : []),
      ].map(([k, v]) => el("div", { class: "kv" }, el("span", { class: "kv-k" }, k), el("span", { class: "kv-v" }, v))),
      d.email_has_account && open
        ? el("p", { class: "error" }, "此 email 已是後台帳號。核准時該帳號只會被加入為新商家的 owner，密碼不會改動，需要明確確認。") : "",
      status);

    if (review && open) {
      const bar = el("div", { class: "toolbar" });
      if (d.status === "pending_verification") {
        bar.append(el("button", { "data-act": "verify-manually", onclick: () => act(async () => {
          if (!confirm("確定已透過其他管道確認申請者的聯絡方式？此動作會記入稽核。")) return;
          await api("POST", `/admin/shop-applications/${d.id}/verify-manually`);
          await refresh();
        }) }, "人工確認聯絡方式"));
      }
      if (d.status === "pending_review" && d.email_verified) {
        bar.append(el("button", { class: "primary", "data-act": "approve", onclick: () => openApprove(d) }, "核准並開店"));
      }
      bar.append(el("button", { class: "danger", "data-act": "reject", onclick: () => openReject(d) }, "拒絕"));
      host.append(bar);
    }

    host.append(el("h4", {}, "通知訊息（outbox）"));
    if (!d.messages.length) host.append(el("p", { class: "hint" }, "沒有訊息。"));
    for (const m of d.messages) {
      const copy = el("button", { type: "button" }, "複製內文");
      copy.addEventListener("click", async () => {
        try { await navigator.clipboard.writeText(m.body_text); copy.textContent = "已複製"; } catch { /* 手動選取 */ }
      });
      host.append(el("div", { class: "card", "data-message": m.id },
        el("p", {}, el("strong", {}, m.subject), "　→ ", m.recipient_email || "（已清除）",
          "　", el("span", { class: m.status === "sent" ? "badge ok" : "badge warn" }, MSG_STATUS[m.status] || m.status)),
        m.body_text ? el("pre", {}, m.body_text) : el("p", { class: "hint" }, "（內文已清除）"),
        m.error_reason ? el("p", { class: "error" }, m.error_reason) : "",
        m.body_text ? copy : "",
        review && m.status !== "sent"
          ? el("button", { "data-act": "mark-sent", onclick: () => act(async () => {
            await api("POST", `/admin/shop-applications/${d.id}/messages/${m.id}/mark-sent`);
            await refresh();
          }) }, "標記為已寄出") : ""));
    }

    host.append(el("h4", {}, "稽核時間線"));
    const tbody = el("tbody", {});
    host.append(el("table", { class: "table" },
      el("thead", {}, el("tr", {}, el("th", {}, "時間"), el("th", {}, "事件"), el("th", {}, "操作者"))), tbody));
    rows(tbody, d.events, (ev) => el("tr", { "data-event": ev.event },
      el("td", {}, formatTime(ev.created_at)), el("td", {}, EVENTS[ev.event] || ev.event),
      el("td", {}, ev.actor_user_id ? `帳號 #${ev.actor_user_id}` : "系統／申請者")),
    { empty: "沒有事件。", colspan: 3 });
  }

  async function refresh() {
    await openDetail(state.detailID);
    await loadList();
  }

  function openApprove(d) {
    $("#platform-application-approve-name").value = d.shop_name;
    $("#platform-application-approve-status").value = "2";
    $("#platform-application-approve-confirm").checked = false;
    $("#platform-application-approve-existing").hidden = !d.email_has_account;
    $("#platform-application-approve-error").hidden = true;
    $("#platform-application-approve-dialog").showModal();
  }

  async function submitApprove(e) {
    e.preventDefault();
    const box = $("#platform-application-approve-error");
    box.hidden = true;
    const d = state.detail;
    const body = {
      shop_name: $("#platform-application-approve-name").value.trim(),
      status: Number($("#platform-application-approve-status").value),
      confirm_existing_account: $("#platform-application-approve-confirm").checked,
    };
    let res;
    try {
      res = await api("POST", `/admin/shop-applications/${d.id}/approve`, body);
    } catch (err) {
      box.textContent = errText(err);
      box.hidden = false;
      return;
    }
    $("#platform-application-approve-dialog").close();
    const host = clear($("#platform-application-result"));
    host.hidden = false;
    host.append(el("h3", {}, `已核准並開店：${res.shop.name}（#${res.shop.id}）`));
    const linkHost = el("div", { class: "card" });
    host.append(linkHost);
    if (res.owner.created) {
      showLinkOnce(linkHost, { title: `${res.owner.email} 的密碼設定連結（owner）`, link: res.owner });
    } else {
      linkHost.append(el("p", {}, `${res.owner.email} 是既有帳號，已加入為 owner，密碼不變。`));
    }
    host.append(el("p", { class: "hint" },
      "商家目前是「審核中」（店面不可達）。後續上線步驟（publishable key、內容 schema、origin、金流憑證）請依 merchant-onboarding 流程進行。"));
    await refresh();
  }

  function openReject(d) {
    $("#platform-application-reject-reason").value = "";
    $("#platform-application-reject-note").value = "";
    $("#platform-application-reject-error").hidden = true;
    $("#platform-application-reject-dialog").dataset.id = String(d.id);
    $("#platform-application-reject-dialog").showModal();
  }

  async function submitReject(e) {
    e.preventDefault();
    const box = $("#platform-application-reject-error");
    box.hidden = true;
    const id = Number($("#platform-application-reject-dialog").dataset.id);
    try {
      await api("POST", `/admin/shop-applications/${id}/reject`, {
        decision_reason: $("#platform-application-reject-reason").value.trim(),
        internal_note: $("#platform-application-reject-note").value.trim(),
      });
    } catch (err) {
      box.textContent = errText(err);
      box.hidden = false;
      return;
    }
    $("#platform-application-reject-dialog").close();
    await refresh();
  }

  $("#platform-application-approve-form").addEventListener("submit", submitApprove);
  $("#platform-application-approve-cancel").addEventListener("click", () => $("#platform-application-approve-dialog").close());
  $("#platform-application-reject-form").addEventListener("submit", submitReject);
  $("#platform-application-reject-cancel").addEventListener("click", () => $("#platform-application-reject-dialog").close());
  $("#platform-applications-filter").addEventListener("change", (e) => { state.status = e.target.value; state.page = 1; loadList(); });
  $("#platform-applications-search").addEventListener("click", () => {
    state.q = $("#platform-applications-q").value.trim();
    state.page = 1;
    loadList();
  });
  $("#platform-applications-prev").addEventListener("click", () => { state.page = Math.max(1, state.page - 1); loadList(); });
  $("#platform-applications-next").addEventListener("click", () => { state.page += 1; loadList(); });

  return { load };
}
