// DOM 詞彙。刻意維持在下限：每加一個 helper 都要能指出它替換掉了哪一段重複
// 程式碼（change admin-commerce-catalog, design D1）。沒有虛擬 DOM、沒有
// observable、沒有模板語言、沒有路由器。

export const $ = (sel) => document.querySelector(sel);

export function clear(node) {
  if (node) node.textContent = "";
  return node;
}

export function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else if (v !== undefined && v !== null) node.setAttribute(k, v);
  }
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return node;
}

/**
 * repeater：可重複的「列」集合，本專案處理巢狀表單的唯一機制
 * （change admin-commerce-catalog, design D2）。
 *
 * renderRow(value, remove) 必須回傳 `{ node, read }`：
 *   - node：要插進 host 的元素
 *   - read()：**閉包在該列自己的 input 參考上**的讀值函式
 *
 * 關鍵性質：collect 時走 entries 陣列呼叫每一列的 read()，**一次 DOM 查詢都
 * 不做**。這與 lib/schemaform.js 的 collect 路徑（用 label 文字回頭找 DOM
 * 節點）刻意相反——後者在「N 列 × 每列 M 個同名子欄位」的巢狀下會把值配錯，
 * 而且錯得靜默。狀態住在 closure，DOM 只是它的投影。
 *
 * 因為 repeater 對「列裡面有什麼」一無所知，巢狀第二層用的是同一個函式：
 * 一個 SKU 列的 renderRow 內部可以再開一個 repeater 管它的 options。
 */
export function repeater(host, renderRow) {
  const entries = [];

  function add(value) {
    let entry;
    const remove = () => {
      entry.node.remove();
      const i = entries.indexOf(entry);
      if (i >= 0) entries.splice(i, 1);
    };
    entry = renderRow(value, remove);
    entries.push(entry);
    host.append(entry.node);
    return entry;
  }

  return {
    add,
    reset(values) {
      clear(host);
      entries.length = 0;
      (Array.isArray(values) ? values : []).forEach((v) => add(v));
    },
    read() { return entries.map((e) => e.read()); },
    count() { return entries.length; },
  };
}

/**
 * rows：把一份資料畫成 <tbody> 的列（change admin-commerce-operations, design D2）。
 *
 * 加進 lib/ 的理由是數出來的，不是感覺：本專案有 8 個列表視圖（分類、商品、
 * 訂單、運送方式、會員等級、點數流水帳、publishable key、allowed origin），
 * 每一個都在重複「清空 → 沒資料就放一列空狀態 → 有資料就逐列 append」。
 * admin-commerce-catalog design D1 的推翻條件 #2 說第三次重複要先問「能不能加進
 * lib/」，而這個東西不需要生命週期也不需要反應性——它是純函式，輸入資料、
 * 輸出 DOM。所以答案是能，而且它不是框架的訊號。
 *
 * **刻意不是 `table()`**：一個接管整張表的 helper 得同時擁有表頭、對齊與每一格的
 * 產生方式，於是每一格從「一行 el("td", …)」變成 config 物件裡的 render 欄位。
 * 表頭在本專案是 index.html 的靜態 <thead>，本來就不重複；重複的只有這個迴圈。
 *
 * 空狀態被包進來的理由：它是最容易漏的一段，而漏掉的樣子是「一張看起來還在
 * 載入的空表格」——商家分不出「沒有訂單」與「壞了」。放進來之後，忘記寫空狀態
 * 這個失敗模式從結構上消失。
 */
export function rows(host, list, render, { empty = "沒有資料。", colspan = 1 } = {}) {
  clear(host);
  const items = Array.isArray(list) ? list : [];
  if (items.length === 0) {
    host.append(el("tr", {}, el("td", { colspan: String(colspan), class: "empty" }, empty)));
    return host;
  }
  for (const item of items) host.append(render(item));
  return host;
}

/**
 * renderPager：分頁列的文字與上下頁 disabled 狀態（同上，3 個呼叫端）。
 * prefix 是 index.html 裡那組 id 的共同前綴：`#{prefix}-pageinfo` /
 * `#{prefix}-prev` / `#{prefix}-next`。回傳總頁數，供呼叫端夾住頁碼。
 */
export function renderPager(prefix, page, total, pageSize) {
  const pages = Math.max(1, Math.ceil((total || 0) / pageSize));
  const info = $(`#${prefix}-pageinfo`);
  if (info) info.textContent = `第 ${page} / ${pages} 頁 ｜ 共 ${total || 0} 筆`;
  const prev = $(`#${prefix}-prev`);
  if (prev) prev.disabled = page <= 1;
  const next = $(`#${prefix}-next`);
  if (next) next.disabled = page >= pages;
  return pages;
}

/** 讓 <select> 顯示 options 並選中 value（設 attribute 是不夠的）。 */
export function fillSelect(select, options, value) {
  clear(select);
  for (const o of options) select.append(el("option", { value: String(o.value) }, o.label));
  select.value = value === null || value === undefined ? "" : String(value);
  return select;
}
