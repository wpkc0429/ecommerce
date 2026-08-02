// 營運報表（change merchant-reports）。
//
// 三件事在這個檔案裡是不可協商的：
//
//  1. **期間篩選一律送給伺服器**，與訂單列表同一條規則（design D3）。這裡連
//     「在前端過濾」的選項都不存在——回應是彙總值，不是可過濾的列。
//  2. **口徑必須在畫面上明示**，而且它是 index.html 裡的靜態散文，不是這裡的
//     字串。這一頁最容易的失敗方式不是壞掉，是給出一個看起來權威的錯數字，而
//     商家分不出來。所以畫面同時呈現**對帳分割**（計入／已取消／未付款／已退貨），
//     四者相加等於期間內全部訂單——那是他能自行驗證這份報表的唯一方式。
//  3. **筆數與數量絕不經過 formatMoney**（design D10 的同一條規則）。一筆訂單
//     丟進 formatMoney 會得到「NT$1」——數字對、單位錯、看起來完全合理。
//     金額走 formatMoney，其餘走 formatCount。
//
// **多幣別不在這裡被加總**，而且不是靠自律：回應以 currency 為最外層維度，
// 這一頁只是照著畫，沒有任何一行程式碼有機會把兩個幣別加起來。
import { api } from "../lib/api.js";
import { $, el, clear, rows } from "../lib/dom.js";
import { formatMoney } from "../lib/money.js";
import { can } from "../lib/rbac.js";

/** 筆數／數量。**不是金額**，所以不帶貨幣符號、不經過 formatMoney。 */
const groups = new Intl.NumberFormat("zh-Hant-TW");
function formatCount(n, unit) {
  const v = Number(n);
  return Number.isFinite(v) ? `${groups.format(v)} ${unit}` : "—";
}

function iso(d) {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

// `key` 是給 E2E 定址用的 data 屬性而不是 id：多幣別時同一組欄位會出現多次，
// 而重複的 id 是一個 HTML 層級的錯誤。定址走 [data-currency=…] [data-k=…]。
const kv = (label, value, key) => el("div", { class: "kv", "data-k": key },
  el("span", { class: "kv-k" }, label), el("span", { class: "kv-v" }, value));

export function createReportsView(ctx) {
  const shopPath = (suffix) => `/admin/shops/${ctx.getShopID()}${suffix}`;

  function showError(msg) {
    const err = $("#report-error");
    err.textContent = msg;
    err.hidden = false;
  }

  // ── 期間 ────────────────────────────────────────────────────────────
  //
  // 預設是本月一日到今天。**期間刻意不進 hash**（design D5：深層連結要解的問題
  // 是「這一個畫面」，不是「這一個查詢」）。
  function ensureDefaults() {
    const from = $("#report-from");
    const to = $("#report-to");
    if (from.value && to.value) return;
    const now = new Date();
    from.value = iso(new Date(now.getFullYear(), now.getMonth(), 1));
    to.value = iso(now);
  }

  function query() {
    const params = new URLSearchParams({ from: $("#report-from").value, to: $("#report-to").value });
    return params;
  }

  // ── 時區 ────────────────────────────────────────────────────────────
  function renderTimezone(tz) {
    const fallback = tz?.source === "default";
    // 「他選了 Asia/Taipei」與「他什麼都沒選、平台預設剛好是 Asia/Taipei」算出來
    // 的數字一模一樣，但只有後者該讓一個開在東京的商家停下來。所以回落一定要
    // 說出來，不能只顯示名字。
    $("#report-timezone").textContent = fallback
      ? `${tz?.name ?? "—"}（平台預設，這家商家尚未設定）`
      : `${tz?.name ?? "—"}（商家設定）`;
    const form = $("#report-timezone-form");
    form.hidden = !can("shop.update");
    $("#report-timezone-note").hidden = form.hidden;
    if (!form.hidden && !fallback) $("#report-timezone-input").value = tz?.name ?? "";
  }

  async function saveTimezone(value) {
    $("#report-error").hidden = true;
    const label = value === null ? "清除時區設定" : `將時區設為 ${value}`;
    if (!confirm(`${label}？變更會改變所有歷史報表的日界線——同一段期間的數字會跟著改變。`)) return;
    try {
      await api("PUT", shopPath("/settings"), { timezone: value });
    } catch (e) {
      showError(e.message);
      return;
    }
    if (value === null) $("#report-timezone-input").value = "";
    await load();
  }

  // ── 一個幣別的區塊 ──────────────────────────────────────────────────
  function amountComposition(s, currency) {
    const discounted = Number(s.discount_amount) > 0;
    const part = (label, value, cls) => el("span", { class: cls ? `amount-part ${cls}` : "amount-part" },
      el("span", { class: "amount-label" }, label),
      el("span", { class: "money" }, formatMoney(value, currency)));
    // 組合式，不是五個並列的數字（design D3 項 5）：`total_amount` 的語意被改過
    // 一次、組成被改過一次，而這是商家唯一能察覺的地方。折抵列只在真的折了錢時
    // 出現——每期都掛一個「− NT$0」會教會商家忽略那一列。
    return el("div", { class: "amount-composition" },
      part("品項小計", s.subtotal_amount),
      discounted ? el("span", { class: "amount-op" }, "－") : "",
      discounted ? part("等級折扣", s.discount_amount) : "",
      discounted ? el("span", { class: "amount-op" }, "＝") : "",
      discounted ? part("商品淨額", s.net_amount) : "",
      el("span", { class: "amount-op" }, "＋"),
      part("運費", s.shipping_fee),
      el("span", { class: "amount-op" }, "＝"),
      part("請款總額", s.total_amount, "total"));
  }

  function reconciliation(s, currency) {
    const total = Number(s.order_count) + Number(s.returned_order_count)
      + Number(s.cancelled_order_count) + Number(s.unpaid_order_count);
    return el("div", { class: "detail-grid" },
      kv("計入的訂單", formatCount(s.order_count, "筆"), "counted"),
      kv("已退貨（不計入）",
        `${formatCount(s.returned_order_count, "筆")} ／ ${formatMoney(s.returned_total_amount, currency)}`, "returned"),
      kv("已取消（不計入）", formatCount(s.cancelled_order_count, "筆"), "cancelled"),
      kv("未付款（不計入）", formatCount(s.unpaid_order_count, "筆"), "unpaid"),
      kv("期間內建立的訂單合計", formatCount(total, "筆"), "period-total"));
  }

  function table(headers, tbody) {
    return el("table", { class: "table" },
      el("thead", {}, el("tr", {}, ...headers.map((h) => el("th", {}, h)))),
      tbody);
  }

  function bucketsTable(c) {
    const body = el("tbody");
    rows(body, c.buckets, (b) => el("tr", { "data-period": b.period },
      el("td", {}, b.period),
      el("td", {}, formatCount(b.order_count, "筆")),
      el("td", { class: "money" }, formatMoney(b.subtotal_amount, c.currency)),
      el("td", { class: "money" }, formatMoney(b.discount_amount, c.currency)),
      el("td", { class: "money" }, formatMoney(b.shipping_fee, c.currency)),
      el("td", { class: "money" }, formatMoney(b.total_amount, c.currency)),
      el("td", {}, formatCount(b.returned_order_count, "筆"))),
    { empty: "這個期間沒有計入的訂單。", colspan: 7 });
    return table(["期間", "訂單", "品項小計", "折扣", "運費", "請款總額", "退貨"], body);
  }

  function productsTable(c) {
    const body = el("tbody");
    rows(body, c?.items, (it) => el("tr", { "data-sku": it.sku_code },
      el("td", {}, it.product_title),
      el("td", { class: "mono" }, it.sku_code),
      el("td", {}, formatCount(it.quantity, "件")),
      el("td", { class: "money" }, formatMoney(it.gross_amount, c.currency))),
    { empty: "這個期間沒有售出的商品。", colspan: 4 });
    return table(["商品", "SKU", "售出數量", "銷售金額（折扣前）"], body);
  }

  function currencyBlock(c, productsByCurrency) {
    return el("div", { class: "detail-section", "data-currency": c.currency },
      el("h3", {}, `營業額（${c.currency}）`),
      amountComposition(c.summary, c.currency),
      reconciliation(c.summary, c.currency),
      el("h4", {}, "期間明細"),
      bucketsTable(c),
      el("h4", {}, "商品銷售排行"),
      productsTable(productsByCurrency[c.currency] ?? { currency: c.currency, items: [] }));
  }

  // ── 載入 ────────────────────────────────────────────────────────────
  async function load() {
    ensureDefaults();
    $("#report-error").hidden = true;
    const host = clear($("#report-currencies"));

    const params = query();
    params.set("group_by", $("#report-group-by").value);
    let sales;
    let products = { currencies: [] };
    try {
      sales = await api("GET", shopPath(`/reports/sales?${params.toString()}`));
      products = await api("GET", shopPath(`/reports/products?${query().toString()}`));
    } catch (e) {
      showError(e.message);
      return;
    }
    renderTimezone(sales.timezone);

    const byCurrency = {};
    for (const c of products.currencies ?? []) byCurrency[c.currency] = c;

    const list = sales.currencies ?? [];
    if (list.length === 0) {
      host.append(el("p", { class: "hint", id: "report-empty" }, "這個期間沒有任何訂單。"));
      return;
    }
    // 多幣別：逐塊呈現，各自完整。**這一頁沒有任何地方把它們加起來**，因為回應
    // 裡根本沒有那個數字（design D13）。
    if (list.length > 1) {
      host.append(el("p", { class: "hint", id: "report-multi-currency" },
        "這個期間有多種幣別的訂單，因此以下逐一分開呈現。跨幣別的總額不存在，也不會被計算。"));
    }
    for (const c of list) host.append(currencyBlock(c, byCurrency));
  }

  // ── 事件接線 ────────────────────────────────────────────────────────
  $("#report-search").addEventListener("click", load);
  $("#report-group-by").addEventListener("change", load);
  $("#report-timezone-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const v = $("#report-timezone-input").value.trim();
    if (v) saveTimezone(v);
  });
  $("#report-timezone-clear").addEventListener("click", () => saveTimezone(null));

  return { load };
}
