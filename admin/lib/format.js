// 顯示格式。**不含金額、不含點數**——那些住在 lib/money.js，理由是它們之間
// 有一個真實的換算，混用會產出「數字對、單位錯」的畫面（design D10）。
//
// 本檔存在的理由是一筆記在帳上的舊債：`formatTime` 在 views/ 曾經有**四份逐字
// 相同的拷貝**（members／orders／points／storefront-clients），早就過了 tripwire
// #2 的「第三次重寫」門檻。change partial-shipment 的 design D11 明文記錄它、
// 刻意不順手還，理由是「做了之後 #4′ 量到的就不是本 change 自己的壓力」，
// 並指名留給下一個 admin change——也就是 change merchant-reports。
//
// **`label()`（兩份）刻意仍未收進來**：它與 formatTime 不同型。formatTime 是
// 純函式、零設定；`label(map, v)` 的兩個呼叫端各自帶著自己的列舉對照表
// （orders.js 四張、points.js 一張），搬進 lib/ 只會搬走三行，對照表仍留在視圖裡。
// 而且兩份還沒到 tripwire #2 的門檻。第三份出現時再說。

/** ISO 8601 字串 → 當地時間字串。空值顯示破折號，無法解析時原樣顯示。 */
export function formatTime(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? String(iso) : d.toLocaleString("zh-Hant-TW");
}
