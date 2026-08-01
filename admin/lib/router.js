// 雜湊路由（change admin-commerce-operations, design D5）。
//
// 形狀只有一種：`#/<view>[/<param>…]`。`#/orders`、`#/orders/1234`、`#/points/57`。
//
// **為什麼是 hash 而不是 History API**：後台是靜態檔案由 nginx 服務
// （admin/Dockerfile）。History API 的深層連結需要伺服器把任意路徑 rewrite 回
// index.html，那是一個部署設定變更；hash 不需要任何伺服器配合。這與「維持無建置
// 步驟、同一份靜態資源服務任何環境」是同一個取捨方向。
//
// **為什麼現在才做**（admin-commerce-catalog 刻意延後）：訂單詳情與會員點數是
// 帶參數的視圖。沒有路由的話，「從訂單詳情跳到這位顧客的點數」得靠兩個模組互相
// 持有狀態或由 shell 轉交參數——那已經是一個沒有名字的路由器了。順帶得到商家真正
// 會用的能力：把 #/orders/1234 貼給同事。
//
// **不做**：巢狀路由、把篩選條件寫進 query string、路由守衛。篩選不入 hash 是刻意
// 的——它會讓「貼給同事」變成貼一串連自己都看不懂的參數，而深層連結要解的問題是
// 「這一筆」，不是「這個查詢」。

/** 解析目前（或給定的）hash 為 { name, params }。空 hash → name 為 ""。 */
export function parseHash(hash = window.location.hash) {
  const raw = String(hash || "").replace(/^#\/?/, "");
  const parts = raw.split("/").filter((s) => s !== "");
  return {
    name: parts.length > 0 ? decodeURIComponent(parts[0]) : "",
    params: parts.slice(1).map(decodeURIComponent),
  };
}

/** 組出一個視圖的 hash 字串；params 會被 encode。 */
export function hashFor(name, ...params) {
  return `#/${[name, ...params].map((p) => encodeURIComponent(String(p))).join("/")}`;
}

/**
 * 導航到某個視圖。回傳 true 表示 hash 真的變了（因此 hashchange 會觸發、由
 * onRoute 的處理器負責畫面）；回傳 false 表示 hash 相同——瀏覽器不會發事件，
 * 呼叫端必須自己渲染一次，否則點同一個分頁第二次會沒有反應。
 */
export function navigate(name, ...params) {
  const target = hashFor(name, ...params);
  if (window.location.hash === target) return false;
  window.location.hash = target;
  return true;
}

/** 註冊路由處理器，並立即以目前 hash 呼叫一次是**呼叫端**的責任（開機時序）。 */
export function onRoute(handler) {
  window.addEventListener("hashchange", () => handler(parseHash()));
}
