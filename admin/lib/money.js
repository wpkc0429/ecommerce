// 金額。這個檔案存在的唯一理由，是把「絕對不縮放」這件事變成一個地方
// （change admin-commerce-catalog, design D4；spec admin-catalog-ui）。
//
// **本檔案與整個 admin 都不存在 `/100` 或 `*100`。**
//
// 契約（api/openapi/openapi.yaml 的 MinorUnitAmount）：金額是最小貨幣單位的
// 整數，單位由**同一層的 currency** 決定。TWD 的最小貨幣單位是「元」，所以
// 3400 就是 NT$3,400。是否該除、除多少，取決於幣別；在只有 TWD 的系統上，
// 正確答案是「不除」。
//
// 為什麼不用 Intl 的 style:"currency"：它依 CLDR 自行決定小數位，
// format(3400) 會產出 "$3,400.00"。數值沒錯，但那個 ".00" 暗示了一個與本系統
// 相反的單位語意（「這是有小數的金額」），下一個讀到它的人就會去補除法。
// 用 decimal 樣式只求千分位、幣別符號自己加，語意就無從被誤解。
//
// 為什麼不自己長一張 ISO 4217 exponent 對照表：spec product-catalog 的
// design D1 已定案「若後續要支援非零小數貨幣，屆時建立一個後端 money
// package 集中處理，不要各 proposal 各自土法煉鋼」。前端自建對照表正是那句話
// 禁止的事。非 TWD 一律顯示成 "3,400 USD" 這種**不假裝知道小數位**的形式。
const groups = new Intl.NumberFormat("zh-Hant-TW");

export function formatMoney(amount, currency = "TWD") {
  const n = Number(amount);
  if (!Number.isFinite(n)) return "—";
  const s = groups.format(n); // 只加千分位；不縮放
  return currency === "TWD" ? `NT$${s}` : `${s} ${currency}`;
}

/**
 * 點數（change admin-commerce-operations, design D10）。
 *
 * **點數不是金額，絕不能經過 formatMoney。** 這條規則存在的理由是兩者之間有一個
 * 真實的換算：points.Service 以 floor(subtotal_amount / 100) 核發，所以一筆
 * 3,400 的訂單會核發 34 點。把 34 丟進 formatMoney 會得到「NT$34」——數字對、
 * 單位錯、而且看起來完全合理，沒有任何測試會噴錯。訂單詳情上這兩個數字會同時
 * 出現，所以它們在畫面上必須長得不一樣。
 */
export function formatPoints(n) {
  const v = Number(n);
  if (!Number.isFinite(v)) return "—";
  return `${groups.format(v)} 點`;
}

/** 帶正負號的點數異動量（流水帳用）：+34 點 / −34 點。 */
export function formatPointsDelta(n) {
  const v = Number(n);
  if (!Number.isFinite(v)) return "—";
  return `${v > 0 ? "+" : ""}${groups.format(v)} 點`;
}

/** 百分比欄位（member_tiers.discount_percent）。同樣不經過 formatMoney。 */
export function formatPercent(n) {
  const v = Number(n);
  if (!Number.isFinite(v)) return "—";
  return `${v}%`;
}

/** 讀一個整數輸入框（金額或數量）；空白/非法一律 0，永不回傳小數。 */
export function parseAmount(input) {
  const v = parseInt(input?.value ?? "", 10);
  return Number.isFinite(v) ? v : 0;
}

/** 一組 SKU 的價格區間，供列表用；混幣別時不假裝算得出一個區間。 */
export function priceRange(skus) {
  const list = Array.isArray(skus) ? skus : [];
  if (list.length === 0) return "—";
  const currencies = new Set(list.map((s) => s.currency || "TWD"));
  if (currencies.size > 1) return "多幣別";
  const currency = list[0].currency || "TWD";
  const amounts = list.map((s) => Number(s.price_amount) || 0);
  const min = Math.min(...amounts);
  const max = Math.max(...amounts);
  return min === max ? formatMoney(min, currency) : `${formatMoney(min, currency)} – ${formatMoney(max, currency)}`;
}
