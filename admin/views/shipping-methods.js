// 運送方式 CRUD（change admin-commerce-operations）。
//
// flat_rate 是最小貨幣單位整數（openapi.yaml 的 MinorUnitAmount），與商品價格
// 同一條規則：**顯示走 formatMoney、輸入是整數、不縮放**（design D10 / spec
// admin-catalog-ui 的金額規則）。TWD 的 150 就是 NT$150。
//
// 結帳時 order.Service 會把選定方式的 flat_rate 快照成 orders.shipping_fee
// （change order-extras），所以這個畫面上的數字直接決定顧客付多少錢。
import { api, ApiError } from "../lib/api.js";
import { $, el, rows } from "../lib/dom.js";
import { formatMoney, parseAmount } from "../lib/money.js";
import { can, whenCan } from "../lib/rbac.js";

export function createShippingMethodsView(ctx) {
  const state = { list: [], editing: null };

  async function load() {
    const err = $("#shipping-methods-error");
    err.hidden = true;
    $("#new-shipping-method-btn").hidden = !can("shipping_method.create");
    try {
      const data = await api("GET", `/admin/shops/${ctx.getShopID()}/shipping-methods`);
      state.list = data.shipping_methods || [];
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
      return;
    }
    rows($("#shipping-methods-tbody"), state.list, (m) => el("tr", { "data-name": m.name },
      el("td", {}, m.id),
      el("td", {}, m.name),
      el("td", {}, m.carrier),
      // 不縮放：150 就是 NT$150。
      el("td", { class: "money" }, formatMoney(m.flat_rate, "TWD")),
      el("td", {}, m.is_active
        ? el("span", { class: "badge ok" }, "啟用中")
        : el("span", { class: "badge" }, "已停用")),
      el("td", {},
        whenCan("shipping_method.edit", () => el("button", { onclick: () => openDialog(m) }, "編輯")),
        whenCan("shipping_method.delete", () =>
          el("button", { class: "danger", onclick: () => remove(m) }, "刪除")))),
    { empty: "尚未建立任何運送方式。顧客結帳時將沒有運費選項。", colspan: 6 });
  }

  function openDialog(method) {
    state.editing = method ?? null;
    $("#shipping-method-dialog-title").textContent =
      method ? `編輯運送方式 #${method.id}` : "新增運送方式";
    $("#shipping-method-name").value = method?.name ?? "";
    $("#shipping-method-carrier").value = method?.carrier ?? "";
    $("#shipping-method-rate").value = method?.flat_rate ?? 0;
    $("#shipping-method-active").checked = method?.is_active ?? true;
    $("#shipping-method-error").hidden = true;
    syncPreview();
    $("#shipping-method-dialog").showModal();
  }

  function syncPreview() {
    $("#shipping-method-rate-preview").textContent =
      formatMoney(parseAmount($("#shipping-method-rate")), "TWD");
  }

  async function submit(e) {
    e.preventDefault();
    const box = $("#shipping-method-error");
    box.hidden = true;
    const body = {
      name: $("#shipping-method-name").value.trim(),
      carrier: $("#shipping-method-carrier").value.trim(),
      // 整數原封送出，不乘不除。
      flat_rate: parseAmount($("#shipping-method-rate")),
      is_active: $("#shipping-method-active").checked,
    };
    try {
      const base = `/admin/shops/${ctx.getShopID()}/shipping-methods`;
      if (state.editing) await api("PUT", `${base}/${state.editing.id}`, body);
      else await api("POST", base, body);
      $("#shipping-method-dialog").close();
      await load();
    } catch (err) {
      box.textContent = err.message +
        (err instanceof ApiError && err.details.length
          ? `（${err.details.map((d) => `${d.pointer} ${d.message}`).join("；")}）` : "");
      box.hidden = false;
    }
  }

  async function remove(method) {
    if (!confirm(`確定刪除運送方式「${method.name}」？已使用它的既有訂單不受影響（運費與名稱在結帳時已快照）。`)) return;
    const err = $("#shipping-methods-error");
    err.hidden = true;
    try {
      await api("DELETE", `/admin/shops/${ctx.getShopID()}/shipping-methods/${method.id}`);
      await load();
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
    }
  }

  $("#new-shipping-method-btn").addEventListener("click", () => openDialog(null));
  $("#shipping-method-cancel").addEventListener("click", () => $("#shipping-method-dialog").close());
  $("#shipping-method-form").addEventListener("submit", submit);
  $("#shipping-method-rate").addEventListener("input", syncPreview);

  return { load };
}
