// 訂單詳情裡的「出貨」區塊（change partial-shipment）。
//
// **為什麼是獨立檔案**：tripwire #5（單一 views/*.js 超過約 400 行）在本 change
// 響了——orders.js 起點 375 行，而出貨區塊從「一筆出貨的五個欄位」長成「N 筆出貨
// 各自帶裝載品項與終態按鈕，外加一份品項層級的建立表單」。design D11 事先定調過
// 這件事：#5 的意思是「這個檔案該拆了」，不是「該換框架了」，而且拆的方式是**檔案
// 切分，不得新增 lib/ 抽象**。所以這裡沒有任何新的共用詞彙，用的全是既有的
// el()／clear()／repeater()／can()。
//
// **顯示用的 badge／formatTime 由呼叫端傳進來**，不從 lib/ 拿也不再抄一份：它們是
// 訂單詳情這一頁的格式，這個模組只是那一頁的一段。（順帶記下一筆真的欠著的債：
// formatTime 今天在 views/ 裡有四份拷貝——members、orders、points、
// storefront-clients——那早就過了 tripwire #2 的門檻，該進 lib/。本 change 刻意
// 不順手做，因為那樣 #4′ 量到的就不是本 change 自己的壓力了。留給下一個 admin
// change。）
//
// 三件事在這個檔案裡是不可協商的：
//
//  1. **巢狀表單走 repeater()，每列的 read() 閉包在自己的 input 上**
//     （admin-commerce-catalog design D2）。出貨品項正是「N 列 × 每列同名子欄位」
//     的形狀，lib/schemaform.js 那條用 label 文字回頭找 DOM 的收集路徑在這裡會
//     **靜默配錯值**——把 A 品項的數量送成 B 品項的。
//  2. **「已出貨數」不在瀏覽器裡算**。它是伺服器上的 order_items.shipped_quantity
//     （design D3），隨 GET /orders/{id} 一起回來。這裡唯一算的是
//     `未出 = 訂購 − 已出`，那是**一列之內的純函式**。送出後整份重讀
//     （loadDetail），所以畫面永遠是伺服器狀態的一次性投影，沒有需要同步的第二份
//     真相——這正是 tripwire #1 的結論，見 design D10。
//  3. **「出貨全部未出品項」不送 items**。省略 items 的意思由伺服器定義（design
//     D5），前端刻意不把它展開成一份自己算出來的完整清單：那會讓權威從伺服器搬到
//     瀏覽器，而兩邊對「還剩幾件」的看法一旦分歧，多出來的那件會被真的寄出去。
import { api } from "../lib/api.js";
import { el, clear, repeater } from "../lib/dom.js";
import { can } from "../lib/rbac.js";

const SHIPMENT_STATUS = { 1: "已出貨", 2: "已送達", 3: "已退貨" };
const SHIPMENT_RETURNED = 3;
const SHIPMENT_SHIPPED = 1;

const remainingOf = (it) => Number(it.quantity) - Number(it.shipped_quantity ?? 0);

const kv = (k, v) => el("div", { class: "kv" },
  el("span", { class: "kv-k" }, k), el("span", { class: "kv-v" }, v));

/**
 * 畫出一筆訂單的出貨區塊。
 *
 * deps：{ shopPath, reload, showError, badge, formatTime }
 *   - reload()：重新載入整份訂單詳情（送出後唯一的更新策略）
 *   - showError(msg)：把訊息寫到訂單詳情的錯誤列
 */
export async function renderOrderShipments(host, order, deps) {
  clear(host);
  if (!can("shipment.view") && !can("shipment.create")) {
    host.append(el("p", { class: "hint" }, "你沒有檢視出貨紀錄的權限。"));
    return;
  }

  let shipments = [];
  if (can("shipment.view")) {
    try {
      const data = await api("GET", deps.shopPath(`/orders/${order.id}/shipments`));
      shipments = data.shipments || [];
    } catch (e) {
      host.append(el("p", { class: "error" }, e.message));
      return;
    }
  }

  // shipment_items 只帶 order_item_id、不帶商品文案（伺服器刻意不做第三份品項
  // 序列化）。名稱與 SKU 由訂單自己的品項補上，查表而不是再打一次 API。
  const byID = new Map((order.items || []).map((it) => [it.id, it]));

  renderParcels(host, order, shipments, deps, byID);
  renderCreateForm(host, order, shipments, deps);
}

function renderParcels(host, order, shipments, deps, byID) {
  const list = el("div", { id: "order-shipments-list" });
  host.append(list);
  if (shipments.length === 0) {
    list.append(el("p", { class: "hint", id: "order-shipments-empty" }, "尚未出貨。"));
    return;
  }
  list.append(el("p", { class: "hint" },
    `這筆訂單共有 ${shipments.length} 筆出貨紀錄（最新的在最前面）。`));
  // 只有第一張（最新那一筆）帶那組沿用下來的 id。id 必須唯一，而多筆出貨是本
  // change 才出現的形狀；每一張都另外帶 data-shipment-id 與穩定的 class。
  shipments.forEach((sh, i) => list.append(parcelCard(order, sh, deps, byID, i === 0)));
}

function parcelCard(order, sh, deps, byID, primary) {
  const { badge, formatTime } = deps;

  const contents = el("ul", { class: "parcel-contents" });
  const items = sh.items || [];
  if (items.length === 0) {
    contents.append(el("li", { class: "empty" }, "（這筆出貨沒有品項紀錄）"));
  }
  for (const si of items) {
    const line = byID.get(si.order_item_id);
    contents.append(el("li", { class: "parcel-line" },
      el("span", { class: "parcel-line-title" }, line ? line.product_title : `品項 #${si.order_item_id}`),
      el("span", { class: "mono" }, line ? line.sku_code : ""),
      el("span", { class: "parcel-line-qty" }, `× ${si.quantity}`)));
  }

  const card = el("div", { class: "parcel", "data-shipment-id": String(sh.id) },
    el("div", primary ? { class: "detail-grid", id: "shipment-detail" } : { class: "detail-grid" },
      kv("出貨編號", `#${sh.id}`),
      kv("出貨狀態", badge(SHIPMENT_STATUS, sh.status, [1, 2])),
      kv("物流商", sh.carrier),
      kv("追蹤號", el("span", { class: "mono" }, sh.tracking_number || "—")),
      kv("出貨時間", formatTime(sh.shipped_at)),
      kv("送達時間", formatTime(sh.delivered_at))),
    el("h4", {}, "這箱裝了什麼"),
    contents);

  if (sh.status !== SHIPMENT_SHIPPED) {
    // 已送達／已退貨都是終態（伺服器的 CAS 只在 shipped 成立）。不畫任何按鈕。
    card.append(el("p", primary ? { class: "hint", id: "shipment-terminal-note" } : { class: "hint" },
      "此出貨已進入終態，狀態不可再變更。"));
    return card;
  }
  if (!can("shipment.update")) return card;
  card.append(el("div", { class: "toolbar-actions" },
    el("button", {
      ...(primary ? { id: "shipment-delivered-btn" } : {}),
      class: "primary shipment-delivered", onclick: () => advance(order, sh, "delivered", deps),
    }, "標記已送達"),
    // 退貨另需 shipment.return（change merchant-staff-role）：店員有 shipment.update
    // 但沒有它，所以按鈕依它決定可見——否則畫一顆按下去必回 403 的危險按鈕。
    can("shipment.return") && el("button", {
      ...(primary ? { id: "shipment-returned-btn" } : {}),
      class: "danger shipment-returned", onclick: () => advance(order, sh, "returned", deps),
    }, "標記退貨")));
  return card;
}

async function advance(order, sh, target, deps) {
  // 退貨會經 events.OrderReturned 讓 points 扣回這筆訂單已核發的點數
  // （change member-tiers-and-points design D5）。那不是一個標籤，說清楚。
  //
  // change partial-shipment 讓這句話多了兩個後果，兩個都得講：扣回的是**整筆訂單**
  // 的點數（就算這箱只裝了其中一部分），而且訂單自此**不接受任何新的出貨**
  // （design D7）——商家最可能踩到的就是「退一箱、剩下的照出」，而伺服器會回 409。
  const confirmText = target === "returned"
    ? `確定將出貨 #${sh.id} 標記為退貨？此訂單已核發的點數會被全額扣回（即使這箱只裝了部分品項），`
      + "且此訂單之後無法再建立任何出貨。退貨是終態，無法復原。"
    : `確定將出貨 #${sh.id} 標記為已送達？已送達是終態，無法復原。`;
  if (!confirm(confirmText)) return;
  try {
    await api("PUT", deps.shopPath(`/orders/${order.id}/shipments/${sh.id}`), { status: target });
    await deps.reload();
  } catch (e) {
    deps.showError(e.message);
  }
}

function renderCreateForm(host, order, shipments, deps) {
  if (order.status === 1) {
    host.append(el("p", { class: "hint" }, "訂單已取消，無法出貨。"));
    return;
  }
  if (shipments.some((s) => s.status === SHIPMENT_RETURNED)) {
    host.append(el("p", { class: "hint", id: "order-shipment-returned-note" },
      "此訂單已有退貨紀錄，無法再建立出貨。未出的品項請另開新訂單處理。"));
    return;
  }
  const outstanding = (order.items || []).filter((it) => remainingOf(it) > 0);
  if (outstanding.length === 0) {
    host.append(el("p", { class: "hint", id: "order-shipment-complete-note" },
      "這筆訂單的品項已全部出貨。"));
    return;
  }
  if (!can("shipment.create")) {
    host.append(el("p", { class: "hint" }, "尚有未出貨的品項，但你沒有建立出貨的權限。"));
    return;
  }

  const carrier = el("input", { type: "text", id: "shipment-carrier", placeholder: "黑貓宅急便" });
  const tracking = el("input", { type: "text", id: "shipment-tracking", placeholder: "追蹤號（選填）" });
  const lines = el("div", { class: "shipment-lines", id: "shipment-lines" });

  // 每一列的 read() 閉包在該列自己的 qty input 上；collect 走 entries 陣列，
  // **一次 DOM 查詢都不做**（design D2）。
  const rep = repeater(lines, (it) => {
    const remaining = remainingOf(it);
    const qty = el("input", {
      type: "number", min: "0", max: String(remaining), value: String(remaining),
      class: "shipment-line-qty", "data-order-item-id": String(it.id),
    });
    return {
      node: el("div", { class: "shipment-line" },
        el("span", { class: "shipment-line-title" }, it.product_title),
        el("span", { class: "mono" }, it.sku_code),
        el("span", { class: "shipment-line-remaining" }, `未出 ${remaining} ／ 訂購 ${it.quantity}`),
        el("label", { class: "field inline" },
          el("span", { class: "field-label" }, "本次出貨"), qty)),
      // remaining/title ride along so the submit handler can refuse an
      // over-quantity without going back to the DOM for either of them.
      read: () => ({
        order_item_id: it.id, quantity: Number(qty.value),
        remaining, title: it.product_title,
      }),
    };
  });
  rep.reset(outstanding);

  async function send(body) {
    try {
      await api("POST", deps.shopPath(`/orders/${order.id}/shipments`), body);
      await deps.reload();
    } catch (e) {
      deps.showError(e.message);
    }
  }
  const base = () => {
    const b = { carrier: carrier.value.trim() };
    const t = tracking.value.trim();
    if (t) b.tracking_number = t;
    return b;
  };

  host.append(el("div", { class: "shipment-form" },
    el("label", { class: "field" }, el("span", { class: "field-label" }, "物流商"), carrier),
    el("label", { class: "field" }, el("span", { class: "field-label" }, "追蹤號"), tracking),
    el("h4", {}, "本次出貨的品項"),
    lines,
    el("div", { class: "toolbar-actions" },
      // items 省略＝伺服器出貨全部未出品項。這顆按鈕的請求主體與 partial-shipment
      // 之前**逐位元組相同**。
      el("button", { id: "create-shipment-btn", class: "primary", onclick: () => send(base()) },
        "出貨全部未出品項"),
      el("button", {
        id: "create-partial-shipment-btn",
        onclick: () => {
          const lines = rep.read();
          // `max` on a number input is advisory — it does not stop a typed or
          // pasted value. The server's conditional UPDATE is the real arbiter
          // (and answers 422), but bouncing it here names the offending row,
          // which a generic 422 cannot.
          const over = lines.find((l) => l.quantity > l.remaining);
          if (over) {
            deps.showError(`「${over.title}」只剩 ${over.remaining} 件未出貨，不能出 ${over.quantity} 件。`);
            return;
          }
          const picked = lines.filter((l) => l.quantity > 0)
            .map((l) => ({ order_item_id: l.order_item_id, quantity: l.quantity }));
          if (picked.length === 0) {
            deps.showError("請至少為一個品項指定大於 0 的出貨數量。");
            return;
          }
          send({ ...base(), items: picked });
        },
      }, "依上方數量出貨")),
    el("p", { class: "hint" },
      "「出貨全部未出品項」不需要逐項確認數量——一次出完仍然是一個操作。"
      + "未出的數量出完之前，訂單的出貨狀態是「部分出貨」；全部出完後才會變成「已出貨」。"
      + "已標記退貨的訂單無法再建立出貨。")));
}
