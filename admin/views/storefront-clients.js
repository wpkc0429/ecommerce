// 前端接入設定：publishable key 與 allowed origin
// （change admin-commerce-operations, design D6）。
//
// 兩個區塊放在同一個分頁是因為它們回答同一個問題（「我自己的前端怎麼接上來」），
// 但**權限層級不同**：
//
//   publishable key  → 商家層 publishable_key.*（金鑰值是隨機的，命名空間不競爭，
//                      而且輪替是商家自己的營運節奏，必須自助）
//   allowed origin   → **平台層** shop.manage_client_origins（origin 是全域競爭
//                      的命名空間，和綁定網域同性質；讓商家自行登記等於讓他claim
//                      別人的 origin 並把任意站點加進平台的 CORS 允許集）
//
// 可見性的來源仍然只有 lib/rbac.js 的 can()——不需要第二個來源。理由是查證過的：
// rbac.Engine 在商家上下文取角色時是 `shop_id = ? OR shop_id IS NULL`，平台層角色
// 的權限會被聯集進商家層快照，所以 /shops/{id}/me 對平台管理員會回
// shop.manage_client_origins，對商家 owner 不會（design D6，有整合測試守著）。
import { api, ApiError } from "../lib/api.js";
import { $, el, rows } from "../lib/dom.js";
import { can } from "../lib/rbac.js";

export function createStorefrontClientsView(ctx) {
  async function load() {
    const keysVisible = can("publishable_key.view");
    $("#publishable-keys-block").hidden = !keysVisible;
    $("#new-publishable-key-btn").hidden = !can("publishable_key.create");

    const originsVisible = can("shop.manage_client_origins");
    $("#allowed-origins-block").hidden = !originsVisible;

    await Promise.all([
      keysVisible ? loadKeys() : Promise.resolve(),
      originsVisible ? loadOrigins() : Promise.resolve(),
    ]);
  }

  // ── publishable keys ────────────────────────────────────────────────
  async function loadKeys() {
    const err = $("#publishable-keys-error");
    err.hidden = true;
    let data;
    try {
      data = await api("GET", `/admin/shops/${ctx.getShopID()}/publishable-keys`);
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
      return;
    }
    rows($("#publishable-keys-tbody"), data.keys, (k) => el("tr", { "data-key": k.key },
      el("td", {}, k.id),
      // 完整顯示，不遮蔽：它是公開識別碼不是憑證（design storefront-client-auth
      // D1），遮起來只會擋住正在接前端的人，保護不到任何東西。
      el("td", { class: "mono key-value" }, k.key),
      el("td", {}, k.label || "—"),
      el("td", {}, formatTime(k.created_at)),
      el("td", {}, k.revoked_at
        ? el("span", { class: "badge" }, `已撤銷 ${formatTime(k.revoked_at)}`)
        : el("span", { class: "badge ok" }, "使用中")),
      el("td", {}, !k.revoked_at && can("publishable_key.revoke")
        ? el("button", { class: "danger", onclick: () => revoke(k) }, "撤銷")
        : "")),
    { empty: "尚未產生任何 publishable key。自帶前端需要一把才能呼叫 /api/v1/shop/*。", colspan: 6 });
  }

  async function createKey() {
    const err = $("#publishable-keys-error");
    err.hidden = true;
    const label = prompt("這把金鑰的用途標籤（選填，例如「正式站前端」）：", "") ?? "";
    try {
      await api("POST", `/admin/shops/${ctx.getShopID()}/publishable-keys`, { label });
      await loadKeys();
    } catch (e) {
      err.textContent = e.message +
        (e instanceof ApiError && e.details.length
          ? `（${e.details.map((d) => `${d.pointer} ${d.message}`).join("；")}）` : "");
      err.hidden = false;
    }
  }

  async function revoke(key) {
    if (!confirm(`確定撤銷金鑰 ${key.key}？撤銷即時生效，使用它的前端會立刻收到 404。`)) return;
    const err = $("#publishable-keys-error");
    err.hidden = true;
    try {
      await api("DELETE", `/admin/shops/${ctx.getShopID()}/publishable-keys/${key.id}`);
      await loadKeys();
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
    }
  }

  // ── allowed origins（平台層）─────────────────────────────────────────
  async function loadOrigins() {
    const err = $("#allowed-origins-error");
    err.hidden = true;
    let data;
    try {
      data = await api("GET", `/admin/shops/${ctx.getShopID()}/allowed-origins`);
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
      return;
    }
    rows($("#allowed-origins-tbody"), data.origins, (o) => el("tr", { "data-origin": o.origin },
      el("td", {}, o.id),
      el("td", { class: "mono" }, o.origin),
      el("td", {}, formatTime(o.created_at)),
      el("td", {}, el("button", { class: "danger", onclick: () => removeOrigin(o) }, "移除"))),
    { empty: "尚未登記任何 origin。瀏覽器端的自帶前端會被 CORS 擋下。", colspan: 4 });
  }

  async function addOrigin(e) {
    e.preventDefault();
    const err = $("#allowed-origins-error");
    err.hidden = true;
    const origin = $("#allowed-origin-input").value.trim();
    try {
      await api("POST", `/admin/shops/${ctx.getShopID()}/allowed-origins`, { origin });
      $("#allowed-origin-input").value = "";
      await loadOrigins();
    } catch (ex) {
      err.textContent = ex.message +
        (ex instanceof ApiError && ex.details.length
          ? `（${ex.details.map((d) => `${d.pointer} ${d.message}`).join("；")}）` : "");
      err.hidden = false;
    }
  }

  async function removeOrigin(o) {
    if (!confirm(`確定移除 origin ${o.origin}？該來源的瀏覽器請求會立刻被 CORS 擋下。`)) return;
    const err = $("#allowed-origins-error");
    err.hidden = true;
    try {
      await api("DELETE", `/admin/shops/${ctx.getShopID()}/allowed-origins/${o.id}`);
      await loadOrigins();
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
    }
  }

  $("#new-publishable-key-btn").addEventListener("click", createKey);
  $("#allowed-origin-form").addEventListener("submit", addOrigin);

  return { load };
}

function formatTime(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? String(iso) : d.toLocaleString("zh-Hant-TW");
}
