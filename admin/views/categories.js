// 商品分類 CRUD（change admin-commerce-catalog, design D9）。
//
// 用 `?flat=1` 的平坦清單而不是樹狀元件——那個參數當初就是為了 admin 而存在的
// （api/internal/httpapi/categories.go 的註解明說 "offered for admin UIs that
// prefer to build their own tree view"）。父子關係以「父分類」欄與編輯時的
// <select> 表達；排序是既有的 position 數字欄位。拖拉排序留給日後。
import { api } from "../lib/api.js";
import { $, el, fillSelect, rows } from "../lib/dom.js";
import { can, whenCan } from "../lib/rbac.js";

export function createCategoriesView(ctx) {
  const state = { list: [], editing: null };

  /** 給其他視圖（商品編輯器）共用的平坦分類清單。 */
  async function fetchFlat() {
    const data = await api("GET", `/admin/shops/${ctx.getShopID()}/categories?flat=1`);
    return data.categories || [];
  }

  function nameOf(id) {
    const found = state.list.find((c) => c.id === id);
    return found ? found.name : `#${id}`;
  }

  async function load() {
    const err = $("#categories-error");
    err.hidden = true;
    $("#new-category-btn").hidden = !can("category.create");
    try {
      state.list = await fetchFlat();
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
      return;
    }
    rows($("#categories-tbody"), state.list, (c) => el("tr", {},
      el("td", {}, c.id),
      el("td", {}, c.name),
      el("td", {}, c.slug),
      el("td", {}, c.parent_id ? nameOf(c.parent_id) : "—"),
      el("td", {}, c.position ?? 0),
      el("td", {},
        whenCan("category.edit", () => el("button", { onclick: () => openDialog(c) }, "編輯")),
        whenCan("category.edit", () => el("button", { class: "category-order-btn", onclick: () => openOrder(c) }, "商品排序")),
        whenCan("category.delete", () =>
          el("button", { class: "danger", onclick: () => remove(c) }, "刪除")))),
    { empty: "尚未建立任何分類。", colspan: 6 });
  }

  function openDialog(category) {
    state.editing = category ?? null;
    $("#category-dialog-title").textContent = category ? `編輯分類 #${category.id}` : "新增分類";
    $("#category-name").value = category?.name ?? "";
    $("#category-slug").value = category?.slug ?? "";
    $("#category-position").value = category?.position ?? 0;
    // 父分類選單排除自己（明顯的自我成環）；真正的防環由伺服器 422 守著。
    fillSelect(
      $("#category-parent"),
      [{ value: "", label: "（根分類）" }].concat(
        state.list.filter((c) => !category || c.id !== category.id)
          .map((c) => ({ value: c.id, label: `${c.name}（${c.slug}）` }))),
      category?.parent_id ?? "");
    $("#category-error").hidden = true;
    $("#category-dialog").showModal();
  }

  async function submit(e) {
    e.preventDefault();
    const box = $("#category-error");
    box.hidden = true;
    const parentRaw = $("#category-parent").value;
    const body = {
      name: $("#category-name").value.trim(),
      slug: $("#category-slug").value.trim(),
      position: parseInt($("#category-position").value, 10) || 0,
    };
    try {
      if (state.editing) {
        // parent_id 省略＝不變；clear_parent＝移到根（服務層的三態語意）。
        if (parentRaw === "") body.clear_parent = true;
        else body.parent_id = Number(parentRaw);
        await api("PUT", `/admin/shops/${ctx.getShopID()}/categories/${state.editing.id}`, body);
      } else {
        if (parentRaw !== "") body.parent_id = Number(parentRaw);
        await api("POST", `/admin/shops/${ctx.getShopID()}/categories`, body);
      }
      $("#category-dialog").close();
      await load();
    } catch (err) {
      box.textContent = err.message +
        (err.details?.length ? `（${err.details.map((d) => `${d.pointer} ${d.message}`).join("；")}）` : "");
      box.hidden = false;
    }
  }

  async function remove(category) {
    if (!confirm(`確定刪除分類「${category.name}」？`)) return;
    const err = $("#categories-error");
    err.hidden = true;
    try {
      await api("DELETE", `/admin/shops/${ctx.getShopID()}/categories/${category.id}`);
      await load();
    } catch (e) {
      // 409：仍有子分類或仍有商品。伺服器的訊息本來就寫成給人看的，直接顯示。
      err.textContent = e.message;
      err.hidden = false;
    }
  }

  // ── 分類內商品排序（design D6）：載入直接成員、上下移動、送出完整排列 ──
  const orderState = { category: null, items: [] };

  function renderOrder() {
    const list = $("#category-order-list");
    list.replaceChildren();
    if (orderState.items.length === 0) {
      list.append(el("li", { class: "hint" }, "此分類目前沒有直接關聯的商品。"));
    }
    orderState.items.forEach((it, i) => {
      const move = (delta) => {
        const j = i + delta;
        [orderState.items[i], orderState.items[j]] = [orderState.items[j], orderState.items[i]];
        renderOrder();
      };
      // el() 對非 null 值一律 setAttribute，disabled:false 仍會停用——故先建再設屬性。
      const up = el("button", { type: "button", class: "order-up", onclick: () => move(-1) }, "↑");
      const down = el("button", { type: "button", class: "order-down", onclick: () => move(1) }, "↓");
      up.disabled = i === 0;
      down.disabled = i === orderState.items.length - 1;
      list.append(el("li", { "data-product-id": it.product_id },
        el("span", {}, it.title),
        it.status === 1 ? "" : el("span", { class: "badge" }, "草稿"),
        up, down));
    });
    $("#category-order-save").disabled = orderState.items.length === 0;
  }

  async function openOrder(category) {
    orderState.category = category;
    $("#category-order-title").textContent = `商品排序：${category.name}`;
    const box = $("#category-order-error");
    box.hidden = true;
    try {
      const data = await api("GET", `/admin/shops/${ctx.getShopID()}/categories/${category.id}/product-order`);
      orderState.items = data.products || [];
    } catch (e) {
      box.textContent = e.message;
      box.hidden = false;
      orderState.items = [];
    }
    renderOrder();
    $("#category-order-dialog").showModal();
  }

  async function submitOrder(e) {
    e.preventDefault();
    const box = $("#category-order-error");
    box.hidden = true;
    try {
      await api("PUT", `/admin/shops/${ctx.getShopID()}/categories/${orderState.category.id}/product-order`,
        { product_ids: orderState.items.map((it) => it.product_id) });
      $("#category-order-dialog").close();
    } catch (err) {
      box.textContent = err.message;
      box.hidden = false;
    }
  }

  $("#category-order-cancel").addEventListener("click", () => $("#category-order-dialog").close());
  $("#category-order-form").addEventListener("submit", submitOrder);

  $("#new-category-btn").addEventListener("click", () => openDialog(null));
  $("#category-cancel").addEventListener("click", () => $("#category-dialog").close());
  $("#category-form").addEventListener("submit", submit);

  return { load, fetchFlat };
}
