// 訂單列表與詳情，含出貨（change admin-commerce-operations, design D3/D8/D9/D10）。
//
// 四件事在這個檔案裡是不可協商的：
//
//  1. **篩選一律送給伺服器**（design D3）。列表是分頁的，在前端過濾只看得到當頁，
//     會對「符合條件但在第 3 頁」的訂單回報不存在——那是一束沒送到的花。
//  2. **金額要以組合式呈現**（design D10）。change order-extras 把 total_amount
//     的語意從「品項小計」改成「應付總額」，change tier-discount-application 又在
//     中間插入了折抵，這個畫面是唯一能讓商家察覺這兩件事的地方，所以是
//     「小計 －折扣 ＋ 運費 ＝ 應付總額」，不是四個並列的數字。
//  3. **出貨是整筆訂單、至多一筆**（design D1/D9）。shipments 沒有品項欄位，
//     order_id 上有唯一索引。所以這裡沒有品項勾選、沒有數量、沒有「已出貨 3 件
//     待出貨 2 件」——那個概念在系統裡沒有對應的事實。
//  4. **狀態只經既有端點**（design D8）。付款由 provider webhook 推進，後台沒有
//     手動標記付款的路徑，這裡也不發明一個；出貨的兩個終態按鈕只在 shipped 時
//     出現，因為伺服器的 CAS 只在 shipped 成立，一顆保證 409 的按鈕比沒有更糟。
import { api } from "../lib/api.js";
import { $, el, clear, rows, renderPager } from "../lib/dom.js";
import { formatMoney, formatPercent } from "../lib/money.js";
import { can, whenCan } from "../lib/rbac.js";

const PAGE_SIZE = 20;

const ORDER_STATUS = { 0: "處理中", 1: "已取消" };
// payment_status / fulfillment_status 的列舉分別屬於 payment / shipping
// （order-management design D5）。這裡只是顯示用的標籤，不認得的值原樣顯示編號，
// 不假裝知道它是什麼。
const PAYMENT_STATUS = { 0: "未付款", 1: "已付款", 2: "已退款", 3: "部分退款" };
const FULFILLMENT_STATUS = { 0: "未出貨", 1: "已出貨", 2: "已送達", 3: "已退貨" };
const SHIPMENT_STATUS = { 1: "已出貨", 2: "已送達", 3: "已退貨" };
const PAYMENT_RECORD_STATUS = { 0: "處理中", 1: "成功", 2: "失敗", 3: "已退款" };

function label(map, v) {
  return map[v] ?? `#${v}`;
}

function badge(map, v, okValues) {
  const cls = okValues.includes(v) ? "badge ok" : "badge";
  return el("span", { class: cls }, label(map, v));
}

function formatTime(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? String(iso) : d.toLocaleString("zh-Hant-TW");
}

export function createOrdersView(ctx) {
  const state = { page: 1, total: 0, order: null };

  const shopPath = (suffix) => `/admin/shops/${ctx.getShopID()}${suffix}`;

  // ── 列表 ────────────────────────────────────────────────────────────
  function query() {
    const params = new URLSearchParams({ page: String(state.page), page_size: String(PAGE_SIZE) });
    const put = (key, sel) => {
      const v = $(sel).value;
      if (v !== "") params.set(key, v);
    };
    put("status", "#order-status");
    put("payment_status", "#order-payment-status");
    put("fulfillment_status", "#order-fulfillment-status");
    put("delivery_from", "#order-delivery-from");
    put("delivery_to", "#order-delivery-to");
    const q = $("#order-q").value.trim();
    if (q) params.set("q", q);
    return params.toString();
  }

  function syncDateNote() {
    // 未指定配送日的訂單在帶任一日期界限時一律被排除（NULL 比較為假）。這是正確
    // 語意，但它看起來像「訂單不見了」，所以必須說出來（spec admin-operations-ui）。
    const dated = $("#order-delivery-from").value !== "" || $("#order-delivery-to").value !== "";
    $("#orders-date-note").hidden = !dated;
  }

  async function loadList() {
    const err = $("#orders-error");
    err.hidden = true;
    syncDateNote();
    let data;
    try {
      data = await api("GET", shopPath(`/orders?${query()}`));
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
      return;
    }
    state.total = data.total ?? 0;
    state.page = data.page ?? 1;

    rows($("#orders-tbody"), data.orders, (o) => {
      const addr = o.shipping_address || {};
      return el("tr", { "data-order-id": String(o.id) },
        el("td", {}, o.id),
        el("td", {}, formatTime(o.created_at)),
        el("td", {}, addr.recipient_name || "—"),
        el("td", {}, o.requested_delivery_date || "—"),
        el("td", { class: "money" }, formatMoney(o.total_amount, o.currency)),
        el("td", {}, badge(ORDER_STATUS, o.status, [0])),
        el("td", {}, badge(PAYMENT_STATUS, o.payment_status, [1])),
        el("td", {}, badge(FULFILLMENT_STATUS, o.fulfillment_status, [1, 2])),
        el("td", {}, el("button", { onclick: () => ctx.showTab("orders", o.id) }, "詳情")));
    }, { empty: "沒有符合條件的訂單。", colspan: 9 });

    renderPager("orders", state.page, state.total, PAGE_SIZE);
  }

  // ── 詳情 ────────────────────────────────────────────────────────────
  async function loadDetail(orderID) {
    ctx.showPanel("tab-order-detail");
    const err = $("#order-detail-error");
    err.hidden = true;
    $("#order-detail-title").textContent = `訂單 #${orderID}`;
    let order;
    try {
      order = await api("GET", shopPath(`/orders/${orderID}`));
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
      clear($("#order-summary"));
      clear($("#order-items-tbody"));
      clear($("#order-shipment"));
      clear($("#order-payments"));
      return;
    }
    state.order = order;
    renderSummary(order);
    renderItems(order);
    await renderShipment(order);
    await renderPayments(order);

    $("#order-cancel-btn").hidden = !can("order.cancel") || order.status !== 0;
    $("#order-points-btn").hidden = !can("point.view");
  }

  function renderSummary(o) {
    const addr = o.shipping_address || {};
    const host = clear($("#order-summary"));

    // 金額組合式，不是並列的數字（design D10）。折抵列**只在真的折了錢時出現**
    // （change tier-discount-application, design D13）：每一筆訂單都掛一個「− NT$0」
    // 會教會商家忽略那一列，而它恰恰是最需要被看見的一列。
    // 折抵是金額走 formatMoney、折扣率是百分比走 formatPercent——`10` 同時可以是
    // 10% 與 NT$10，兩者看起來都合理，所以絕不互換。
    const discounted = Number(o.discount_amount) > 0;
    host.append(el("div", { class: "amount-composition", id: "order-amounts" },
      el("span", { class: "amount-part" },
        el("span", { class: "amount-label" }, "品項小計"),
        el("span", { class: "money", id: "order-subtotal" }, formatMoney(o.subtotal_amount, o.currency))),
      discounted ? el("span", { class: "amount-op" }, "－") : "",
      discounted ? el("span", { class: "amount-part" },
        el("span", { class: "amount-label" },
          `等級折扣（${o.member_tier_name ?? "—"} ${formatPercent(o.discount_percent ?? 0)}）`),
        el("span", { class: "money", id: "order-discount" }, formatMoney(o.discount_amount, o.currency))) : "",
      el("span", { class: "amount-op" }, "＋"),
      el("span", { class: "amount-part" },
        el("span", { class: "amount-label" }, `運費${o.shipping_method_name ? `（${o.shipping_method_name}）` : ""}`),
        el("span", { class: "money", id: "order-shipping-fee" }, formatMoney(o.shipping_fee, o.currency))),
      el("span", { class: "amount-op" }, "＝"),
      el("span", { class: "amount-part total" },
        el("span", { class: "amount-label" }, "應付總額"),
        el("span", { class: "money", id: "order-total" }, formatMoney(o.total_amount, o.currency)))));

    const row = (k, v) => el("div", { class: "kv" },
      el("span", { class: "kv-k" }, k), el("span", { class: "kv-v" }, v));

    host.append(el("div", { class: "detail-grid" },
      row("訂單狀態", badge(ORDER_STATUS, o.status, [0])),
      row("付款狀態", badge(PAYMENT_STATUS, o.payment_status, [1])),
      row("出貨狀態", badge(FULFILLMENT_STATUS, o.fulfillment_status, [1, 2])),
      row("建立時間", formatTime(o.created_at)),
      row("指定配送日", o.requested_delivery_date || "（未指定）"),
      row("運送方式", o.shipping_method_name || "（未選）"),
      row("收件人", addr.recipient_name || "—"),
      row("電話", addr.phone || "—"),
      row("地址", [addr.postal_code, addr.city, addr.line1, addr.line2].filter(Boolean).join(" ") || "—"),
      // change member-directory：顧客現在有自己的畫面，所以這條連結指向那裡而不是
      // 直接跳進點數（點數仍有工具列上的專屬按鈕）。
      row("顧客", el("a", { href: `#/members/${o.member_id}` }, `顧客 #${o.member_id}`)),
      o.cancelled_at ? row("取消時間", formatTime(o.cancelled_at)) : ""));
  }

  function renderItems(o) {
    rows($("#order-items-tbody"), o.items, (it) => el("tr", { "data-sku": it.sku_code },
      el("td", {}, it.product_title),
      el("td", { class: "mono" }, it.sku_code),
      el("td", { class: "money" }, formatMoney(it.price_amount, it.currency)),
      el("td", {}, it.quantity),
      el("td", { class: "money" }, formatMoney(it.line_total, it.currency)),
      // 卡片訊息掛在**品項**上（change order-extras）：同一個 SKU 兩張不同卡片是
      // 兩列，所以它必須顯示在列上，不是訂單層級。
      el("td", { class: "gift-message" }, it.gift_message || "—")),
    { empty: "這筆訂單沒有品項（不該發生）。", colspan: 6 });
  }

  // 出貨區塊的三態（design D9）。
  async function renderShipment(o) {
    const host = clear($("#order-shipment"));
    if (!can("shipment.view") && !can("shipment.create")) {
      host.append(el("p", { class: "hint" }, "你沒有檢視出貨紀錄的權限。"));
      return;
    }
    let shipments = [];
    if (can("shipment.view")) {
      try {
        const data = await api("GET", shopPath(`/orders/${o.id}/shipments`));
        shipments = data.shipments || [];
      } catch (e) {
        host.append(el("p", { class: "error" }, e.message));
        return;
      }
    }
    const shipment = shipments[0] ?? null;

    if (!shipment) {
      if (o.status === 1) {
        host.append(el("p", { class: "hint" }, "訂單已取消，無法出貨。"));
        return;
      }
      if (!can("shipment.create")) {
        host.append(el("p", { class: "hint" }, "尚未出貨。"));
        return;
      }
      const carrier = el("input", { type: "text", id: "shipment-carrier", placeholder: "黑貓宅急便" });
      const tracking = el("input", { type: "text", id: "shipment-tracking", placeholder: "追蹤號（選填）" });
      host.append(el("div", { class: "shipment-form" },
        el("label", { class: "field" }, el("span", { class: "field-label" }, "物流商"), carrier),
        el("label", { class: "field" }, el("span", { class: "field-label" }, "追蹤號"), tracking),
        el("button", {
          id: "create-shipment-btn", class: "primary",
          onclick: () => createShipment(o, carrier.value.trim(), tracking.value.trim()),
        }, "將整筆訂單標記為已出貨"),
        // 沒有品項勾選、沒有數量——這不是省略，是系統沒有部分出貨這回事。
        el("p", { class: "hint" },
          "出貨的粒度是整筆訂單：一筆訂單至多一筆出貨紀錄，送出後整筆訂單即為已出貨。")));
      return;
    }

    const terminal = shipment.status !== 1;
    host.append(el("div", { class: "detail-grid", id: "shipment-detail" },
      el("div", { class: "kv" }, el("span", { class: "kv-k" }, "出貨狀態"),
        el("span", { class: "kv-v" }, badge(SHIPMENT_STATUS, shipment.status, [1, 2]))),
      el("div", { class: "kv" }, el("span", { class: "kv-k" }, "物流商"),
        el("span", { class: "kv-v" }, shipment.carrier)),
      el("div", { class: "kv" }, el("span", { class: "kv-k" }, "追蹤號"),
        el("span", { class: "kv-v mono" }, shipment.tracking_number || "—")),
      el("div", { class: "kv" }, el("span", { class: "kv-k" }, "出貨時間"),
        el("span", { class: "kv-v" }, formatTime(shipment.shipped_at))),
      el("div", { class: "kv" }, el("span", { class: "kv-k" }, "送達時間"),
        el("span", { class: "kv-v" }, formatTime(shipment.delivered_at)))));

    if (terminal) {
      // 已送達／已退貨都是終態（伺服器的 CAS 只在 shipped 成立）。不畫任何按鈕。
      host.append(el("p", { class: "hint", id: "shipment-terminal-note" },
        "此出貨已進入終態，狀態不可再變更。"));
      return;
    }
    if (!can("shipment.update")) return;
    host.append(el("div", { class: "toolbar-actions" },
      el("button", { id: "shipment-delivered-btn", class: "primary", onclick: () => advance(o, shipment, "delivered") }, "標記已送達"),
      el("button", { id: "shipment-returned-btn", class: "danger", onclick: () => advance(o, shipment, "returned") }, "標記退貨")));
  }

  async function createShipment(o, carrier, tracking) {
    const err = $("#order-detail-error");
    err.hidden = true;
    try {
      await api("POST", shopPath(`/orders/${o.id}/shipments`),
        tracking ? { carrier, tracking_number: tracking } : { carrier });
      await loadDetail(o.id);
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
    }
  }

  async function advance(o, shipment, target) {
    // 退貨會經 events.OrderReturned 讓 points 扣回這筆訂單已核發的點數
    // （change member-tiers-and-points design D5）。那不是一個標籤，說清楚。
    const confirmText = target === "returned"
      ? "確定標記為退貨？此訂單已核發的點數會被扣回，且退貨是終態，無法復原。"
      : "確定標記為已送達？已送達是終態，無法復原。";
    if (!confirm(confirmText)) return;
    const err = $("#order-detail-error");
    err.hidden = true;
    try {
      await api("PUT", shopPath(`/orders/${o.id}/shipments/${shipment.id}`), { status: target });
      await loadDetail(o.id);
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
    }
  }

  async function renderPayments(o) {
    const host = clear($("#order-payments"));
    if (!can("payment.view")) {
      host.append(el("p", { class: "hint" }, "你沒有檢視付款紀錄的權限。"));
      return;
    }
    let list = [];
    try {
      const data = await api("GET", shopPath(`/orders/${o.id}/payments`));
      list = data.payments || [];
    } catch (e) {
      host.append(el("p", { class: "error" }, e.message));
      return;
    }
    const body = el("tbody", { id: "order-payments-tbody" });
    host.append(el("table", { class: "table" },
      el("thead", {}, el("tr", {},
        el("th", {}, "ID"), el("th", {}, "provider"), el("th", {}, "provider reference"),
        el("th", {}, "金額"), el("th", {}, "狀態"), el("th", {}, "時間"))),
      body));
    rows(body, list, (p) => el("tr", {},
      el("td", {}, p.id),
      el("td", {}, p.provider),
      el("td", { class: "mono" }, p.provider_reference),
      el("td", { class: "money" }, formatMoney(p.amount, p.currency)),
      el("td", {}, badge(PAYMENT_RECORD_STATUS, p.status, [1])),
      el("td", {}, formatTime(p.created_at))),
    { empty: "尚無付款紀錄。", colspan: 6 });
    // 沒有「標記為已付款」按鈕，而且不會有：付款狀態由 provider webhook 推進
    // （design D8）。
    host.append(el("p", { class: "hint" },
      "付款狀態由金流服務的回呼推進，後台不提供手動變更。"));
  }

  async function cancel() {
    const o = state.order;
    if (!o) return;
    if (!confirm(`確定取消訂單 #${o.id}？庫存會歸還，已核發的點數會被扣回。已付款或已出貨的訂單無法取消。`)) return;
    const err = $("#order-detail-error");
    err.hidden = true;
    try {
      await api("POST", shopPath(`/orders/${o.id}/cancel`));
      await loadDetail(o.id);
    } catch (e) {
      // 409（已付款／已出貨）的訊息本來就寫成給人看的，原樣顯示，不自行預判。
      err.textContent = e.message;
      err.hidden = false;
    }
  }

  // ── 路由入口 ────────────────────────────────────────────────────────
  //
  // load() 是同一個視圖的兩個面貌：#/orders 是列表，#/orders/1234 是詳情。
  function load(orderID) {
    const id = Number(orderID);
    if (Number.isFinite(id) && id > 0) return loadDetail(id);
    state.order = null;
    return loadList();
  }

  // ── 事件接線 ────────────────────────────────────────────────────────
  const refilter = () => { state.page = 1; loadList(); };
  $("#order-search").addEventListener("click", refilter);
  $("#order-q").addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.preventDefault(); refilter(); }
  });
  for (const sel of ["#order-status", "#order-payment-status", "#order-fulfillment-status",
    "#order-delivery-from", "#order-delivery-to"]) {
    $(sel).addEventListener("change", refilter);
  }
  $("#orders-prev").addEventListener("click", () => { if (state.page > 1) { state.page--; loadList(); } });
  $("#orders-next").addEventListener("click", () => { state.page++; loadList(); });
  $("#order-back").addEventListener("click", () => ctx.showTab("orders"));
  $("#order-cancel-btn").addEventListener("click", cancel);
  $("#order-points-btn").addEventListener("click", () => {
    if (state.order) ctx.showTab("points", state.order.member_id);
  });

  return { load };
}
