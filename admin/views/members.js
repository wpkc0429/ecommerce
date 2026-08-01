// 顧客名冊與顧客詳情（change member-directory）。
//
// 這個視圖填掉了 `admin-commerce-operations` design D7 明確留下的缺口：在它之前，
// 商家只能從某一張訂單反查到一位顧客，或直接知道 member id。
//
// 四件事在這個檔案裡是不可協商的：
//
//  1. **點數不在這裡改。** 餘額與等級是唯讀的，調整表單與流水帳留在 views/points.js
//     （design D9）。理由有兩層：抽象上，「餘額＋等級＋調整表單＋流水帳」複製到這裡
//     會是同一個小元件的第三次重寫（tripwire #2），而它有表單、有錯誤處理、有調整後的
//     重新載入，不是 rows() 抽得掉的重複；產品上，調整之後要同步的是餘額、等級、
//     流水帳三處（tripwire #1 描述的正是這個），而點數只該有一條寫入路徑與一條稽核軌跡。
//     **所以這裡只有一顆連過去的按鈕。**
//
//  2. **點數走 formatPoints，永遠不走 formatMoney**（design D10 of admin-commerce-operations）。
//     核發是 floor(基準/100)，所以 3,400 的訂單發 34 點——把 34 丟進 formatMoney 會得到
//     「NT$34」，數字對、單位錯、看起來完全合理，而且沒有任何東西會噴錯。同一個畫面上
//     訂單金額（NT$）與點數（點）並排出現，這條規則在這裡特別容易被違反。
//
//  3. **訪客身分要如實呈現。** password_hash IS NULL 的身分是訪客結帳自動建立的
//     （change guest-checkout）：這些人從未主動註冊、從未同意成為任何名單的一員、而且
//     **無法登入**（Login 的 password_hash == nil 分支直接 401）。把他們標成「會員」會讓
//     商家對他們說錯話，也會讓這份名冊被當成行銷名單。API 的 `registered` 是這件事唯一的
//     來源，而它是 password_hash 的布林投影——hash 本身不會出現在任何回應裡。
//
//  4. **搜尋送給伺服器。** 列表是分頁的，在前端過濾只看得到當頁（design D6 of
//     admin-commerce-catalog 的同一條論證）。
//
// 訂單區塊用的是既有的訂單列表端點加上 `member_id`（design D2），不是顧客端點自帶的
// 訂單陣列——訂單的形狀在三個 change 裡動過三次，兩份序列化遲早會分歧。
import { api } from "../lib/api.js";
import { $, el, clear, rows, renderPager } from "../lib/dom.js";
import { formatMoney, formatPoints } from "../lib/money.js";
import { can } from "../lib/rbac.js";

const PAGE_SIZE = 20;
const RECENT_ORDERS = 10;

const MEMBER_STATUS = { 0: "已停用", 1: "啟用中" };
const PAYMENT_STATUS = { 0: "未付款", 1: "已付款", 2: "已退款", 3: "部分退款" };
const FULFILLMENT_STATUS = { 0: "未出貨", 1: "已出貨", 2: "已送達", 3: "已退貨" };

function label(map, v) {
  return map[v] ?? `#${v}`;
}

function formatTime(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? String(iso) : d.toLocaleString("zh-Hant-TW");
}

/** 註冊狀態徽章。訪客不是「比較差的會員」，是一個沒有同意過任何事的人。 */
function identityBadge(m) {
  return m.registered
    ? el("span", { class: "badge ok", "data-identity": "registered" }, "已註冊")
    : el("span", { class: "badge", "data-identity": "guest" }, "訪客身分");
}

export function createMembersView(ctx) {
  const state = { page: 1, total: 0, q: "", member: null };

  const shopPath = (suffix) => `/admin/shops/${ctx.getShopID()}${suffix}`;

  // ── 名冊 ────────────────────────────────────────────────────────────
  async function loadList() {
    const err = $("#members-error");
    err.hidden = true;
    const params = new URLSearchParams({ page: String(state.page), page_size: String(PAGE_SIZE) });
    if (state.q) params.set("q", state.q);

    let data;
    try {
      data = await api("GET", shopPath(`/members?${params}`));
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
      rows($("#members-tbody"), [], () => null, { empty: "無法載入顧客名冊。", colspan: 7 });
      return;
    }
    state.total = data.total ?? 0;
    state.page = data.page ?? 1;

    rows($("#members-tbody"), data.members, (m) => el("tr", { "data-member-id": String(m.id) },
      el("td", {}, m.id),
      el("td", { class: "member-email" }, m.email || "（無 email）"),
      el("td", {}, identityBadge(m)),
      // 點，不是錢。
      el("td", { class: "points" }, formatPoints(m.points ?? 0)),
      el("td", {}, m.level ? m.level.name : "—"),
      el("td", {}, formatTime(m.created_at)),
      el("td", {}, el("button", { onclick: () => ctx.showTab("members", m.id) }, "詳情"))),
    { empty: state.q ? "沒有符合條件的顧客。" : "這家商家還沒有任何顧客。", colspan: 7 });

    renderPager("members", state.page, state.total, PAGE_SIZE);
  }

  // ── 詳情 ────────────────────────────────────────────────────────────
  async function loadDetail(memberID) {
    ctx.showPanel("tab-member-detail");
    const err = $("#member-detail-error");
    err.hidden = true;
    $("#member-detail-title").textContent = `顧客 #${memberID}`;

    let m;
    try {
      m = await api("GET", shopPath(`/members/${memberID}`));
    } catch (e) {
      // 404 = 這個 id 不在這家店。顧客身分以商家為界，說清楚，不要只丟 not found
      // 讓商家以為系統壞了。
      err.textContent = e.status === 404
        ? `這家商家沒有編號 ${memberID} 的顧客。顧客身分以商家為界（change member-identity-per-shop）。`
        : e.message;
      err.hidden = false;
      clear($("#member-summary"));
      clear($("#member-orders-tbody"));
      $("#member-points-btn").hidden = true;
      return;
    }
    state.member = m;
    renderSummary(m);
    $("#member-points-btn").hidden = !can("point.view");
    await renderOrders(m);
  }

  function renderSummary(m) {
    const host = clear($("#member-summary"));
    const row = (k, v) => el("div", { class: "kv" },
      el("span", { class: "kv-k" }, k), el("span", { class: "kv-v" }, v));

    host.append(el("div", { class: "detail-grid" },
      row("顧客編號", `#${m.id}`),
      row("Email", el("span", { id: "member-email" }, m.email || "（無 email）")),
      row("身分", identityBadge(m)),
      row("帳號狀態", label(MEMBER_STATUS, m.status)),
      row("建立時間", formatTime(m.created_at))));

    if (!can("point.view")) return;
    // 唯讀。調整入口在 #/points/{id}，理由見檔頭第 1 點。
    host.append(el("div", { class: "detail-grid" },
      row("點數餘額", el("span", { class: "points", id: "member-points" }, formatPoints(m.points ?? 0))),
      row("目前等級", el("span", { id: "member-level" }, m.level ? m.level.name : "（尚無等級）"))));
    host.append(el("p", { class: "hint" },
      "點數的調整與流水帳在「會員點數」畫面上——這裡只顯示餘額，兩個畫面各自維護一份餘額會漂移。"));

    if (!m.registered) {
      host.append(el("p", { class: "hint", id: "member-guest-note" },
        "這是訪客結帳自動建立的身分：這位顧客沒有密碼因此無法登入，也從未同意成為會員。" +
        "若他想看自己的訂單，請他以同一個 email 註冊——先前的訂單與點數會自動出現。"));
    }
  }

  async function renderOrders(m) {
    const block = $("#member-orders-block");
    block.hidden = !can("order.view");
    if (!can("order.view")) return;

    let list = [];
    try {
      const data = await api("GET",
        shopPath(`/orders?member_id=${m.id}&page=1&page_size=${RECENT_ORDERS}`));
      list = data.orders || [];
    } catch (e) {
      rows($("#member-orders-tbody"), [], () => null, { empty: e.message, colspan: 7 });
      return;
    }
    rows($("#member-orders-tbody"), list, (o) => el("tr", { "data-order-id": String(o.id) },
      el("td", {}, o.id),
      el("td", {}, formatTime(o.created_at)),
      el("td", {}, o.requested_delivery_date || "—"),
      // 錢，不是點。
      el("td", { class: "money" }, formatMoney(o.total_amount, o.currency)),
      el("td", {}, label(PAYMENT_STATUS, o.payment_status)),
      el("td", {}, label(FULFILLMENT_STATUS, o.fulfillment_status)),
      el("td", {}, el("a", { href: `#/orders/${o.id}` }, "詳情"))),
    { empty: "這位顧客還沒有任何訂單。", colspan: 7 });
  }

  // ── 路由入口 ────────────────────────────────────────────────────────
  //
  // 同一個視圖的兩個面貌：#/members 是名冊，#/members/57 是詳情（與 orders 同型）。
  function load(memberID) {
    const id = Number(memberID);
    if (Number.isFinite(id) && id > 0) return loadDetail(id);
    state.member = null;
    return loadList();
  }

  // ── 事件接線 ────────────────────────────────────────────────────────
  const research = () => { state.q = $("#member-q").value.trim(); state.page = 1; loadList(); };
  $("#member-search").addEventListener("click", research);
  $("#member-q").addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.preventDefault(); research(); }
  });
  $("#members-prev").addEventListener("click", () => { if (state.page > 1) { state.page--; loadList(); } });
  $("#members-next").addEventListener("click", () => { state.page++; loadList(); });
  $("#member-back").addEventListener("click", () => ctx.showTab("members"));
  $("#member-points-btn").addEventListener("click", () => {
    if (state.member) ctx.showTab("points", state.member.id);
  });

  return { load };
}
