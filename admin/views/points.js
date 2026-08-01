// 會員點數：餘額、等級、流水帳、手動調整（change admin-commerce-operations）。
//
// **這個視圖以 member id 定址，因為平台沒有任何列出會員的端點**（design D7）。
// 進來的路徑有三條：訂單詳情的「檢視此顧客點數」、深層連結 #/points/{memberID}、
// 以及畫面上的手動輸入框。做一份顧客名冊不是加一個 GET /members——它要分頁、
// 搜尋，還要一份 PII 揭露決策與一個今天不存在的權限節點，那是獨立 change 的體量。
//
// 金額在這個畫面上一個都沒有：points / points_delta 是「點」（design D10）。
import { api } from "../lib/api.js";
import { $, el, rows, renderPager } from "../lib/dom.js";
import { formatPoints, formatPointsDelta } from "../lib/money.js";
import { can } from "../lib/rbac.js";

const PAGE_SIZE = 20;

// point_transactions.kind（api/internal/points）：0 訂單核發、1 訂單扣回、
// 2 商家手動調整。
const KIND_LABELS = { 0: "訂單核發", 1: "退貨扣回", 2: "手動調整" };

export function createPointsView(ctx) {
  const state = { memberID: null, page: 1, total: 0 };

  function shopPath(suffix) {
    return `/admin/shops/${ctx.getShopID()}/members/${state.memberID}${suffix}`;
  }

  /** load(memberID)：memberID 由路由帶入（#/points/57），沒有就等使用者輸入。 */
  async function load(memberID) {
    const err = $("#points-error");
    err.hidden = true;
    const next = Number(memberID);
    if (Number.isFinite(next) && next > 0 && next !== state.memberID) {
      state.memberID = next;
      state.page = 1;
    }
    $("#points-member-input").value = state.memberID ?? "";
    $("#points-adjust-box").hidden = !can("point.adjust") || !state.memberID;
    const body = $("#points-body");
    if (!state.memberID) {
      body.hidden = true;
      $("#points-hint").hidden = false;
      return;
    }
    $("#points-hint").hidden = true;
    body.hidden = false;
    await Promise.all([loadBalance(), loadLedger()]);
  }

  async function loadBalance() {
    const err = $("#points-error");
    try {
      const data = await api("GET", shopPath("/points"));
      $("#points-balance").textContent = formatPoints(data.points ?? 0);
      $("#points-level").textContent = data.level ? data.level.name : "（尚無等級）";
      $("#points-member-label").textContent = `會員 #${data.member_id}`;
    } catch (e) {
      // 404 = 這個 member id 不在這家店（也可能根本不存在）。說清楚，不要只丟
      // 一句 not found 讓商家以為系統壞了。
      err.textContent = e.status === 404
        ? `這家商家沒有編號 ${state.memberID} 的顧客。顧客身分以商家為界（change member-identity-per-shop）。`
        : e.message;
      err.hidden = false;
      $("#points-balance").textContent = "—";
      $("#points-level").textContent = "—";
    }
  }

  async function loadLedger() {
    let data;
    try {
      data = await api("GET",
        shopPath(`/points/transactions?page=${state.page}&page_size=${PAGE_SIZE}`));
    } catch {
      rows($("#points-tbody"), [], () => null, { empty: "無法載入流水帳。", colspan: 5 });
      return;
    }
    state.total = data.total ?? 0;
    state.page = data.page ?? 1;
    rows($("#points-tbody"), data.transactions, (t) => el("tr", { "data-kind": String(t.kind) },
      el("td", {}, t.id),
      el("td", { class: t.points_delta < 0 ? "points negative" : "points" },
        formatPointsDelta(t.points_delta)),
      el("td", {}, KIND_LABELS[t.kind] ?? `#${t.kind}`),
      el("td", {}, t.order_id
        ? el("a", { href: `#/orders/${t.order_id}` }, `訂單 #${t.order_id}`)
        : (t.reason || "—")),
      el("td", {}, formatTime(t.created_at))),
    { empty: "這位顧客還沒有任何點數紀錄。", colspan: 5 });
    renderPager("points", state.page, state.total, PAGE_SIZE);
  }

  async function adjust(e) {
    e.preventDefault();
    const err = $("#points-adjust-error");
    err.hidden = true;
    const delta = parseInt($("#points-adjust-delta").value, 10);
    const reason = $("#points-adjust-reason").value.trim();
    if (!Number.isFinite(delta) || delta === 0) {
      err.textContent = "異動點數必須是非零整數。";
      err.hidden = false;
      return;
    }
    if (reason === "") {
      err.textContent = "請填寫調整理由——流水帳是唯讀的稽核軌跡，理由是它唯一的解釋。";
      err.hidden = false;
      return;
    }
    try {
      await api("POST", shopPath("/points/adjust"), { points_delta: delta, reason });
      $("#points-adjust-delta").value = "";
      $("#points-adjust-reason").value = "";
      state.page = 1;
      await Promise.all([loadBalance(), loadLedger()]);
    } catch (ex) {
      err.textContent = ex.message;
      err.hidden = false;
    }
  }

  $("#points-member-go").addEventListener("click", () => {
    const id = parseInt($("#points-member-input").value, 10);
    if (Number.isFinite(id) && id > 0) ctx.showTab("points", id);
  });
  $("#points-member-input").addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.preventDefault(); $("#points-member-go").click(); }
  });
  $("#points-prev").addEventListener("click", () => {
    if (state.page > 1) { state.page--; loadLedger(); }
  });
  $("#points-next").addEventListener("click", () => { state.page++; loadLedger(); });
  $("#points-adjust-form").addEventListener("submit", adjust);

  return { load };
}

function formatTime(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? String(iso) : d.toLocaleString("zh-Hant-TW");
}
