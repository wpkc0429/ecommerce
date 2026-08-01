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

/** 讓 <select> 顯示 options 並選中 value（設 attribute 是不夠的）。 */
export function fillSelect(select, options, value) {
  clear(select);
  for (const o of options) select.append(el("option", { value: String(o.value) }, o.label));
  select.value = value === null || value === undefined ? "" : String(value);
  return select;
}
