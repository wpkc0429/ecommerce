// 商品分類 (flat list, create/edit dialog, per-category product order dialog, delete). Permission checks, validation, the
// delete confirmation and the order freshness check live in the core actions.
export default async function (root, ctx) {
  const { h, $, rows, fillSelect } = ctx.ui;
  const t = ctx.t;
  const state = { list: [], editing: null };
  const orderState = { category: null, items: [] };

  const newBtn = h("button", { id: "new-category-btn", class: "primary", onclick: () => openDialog(null) }, t("categories.new"));
  const err = h("p", { id: "categories-error", class: "error", hidden: true });
  const tbody = h("tbody", { id: "categories-tbody" });

  const nameInput = h("input", { type: "text", id: "category-name", required: true });
  const slugInput = h("input", { type: "text", id: "category-slug", required: true, pattern: "[a-z0-9-]+" });
  const parentSelect = h("select", { id: "category-parent" });
  const positionInput = h("input", { type: "number", id: "category-position", step: "1", value: "0" });
  const dialogTitle = h("h3", { id: "category-dialog-title" }, t("categories.dialog.titleNew"));
  const dialogError = h("p", { id: "category-error", class: "error", hidden: true });
  const dialog = h("dialog", { id: "category-dialog" },
    h("form", { id: "category-form", method: "dialog", onsubmit: submit },
      dialogTitle,
      h("label", {}, t("categories.dialog.name"), " ", nameInput),
      h("label", {}, t("categories.dialog.slug"), " ", slugInput),
      h("label", {}, t("categories.dialog.parent"), " ", parentSelect),
      h("label", {}, t("categories.dialog.position"), " ", positionInput),
      dialogError,
      h("div", { class: "dialog-actions" },
        h("button", { type: "button", id: "category-cancel", onclick: () => dialog.close() }, t("categories.dialog.cancel")),
        h("button", { type: "submit", class: "primary" }, t("categories.dialog.save")))));

  const orderTitle = h("h3", { id: "category-order-title" }, t("categories.order.titleDefault"));
  const orderList = h("ol", { id: "category-order-list", class: "order-list" });
  const orderError = h("p", { id: "category-order-error", class: "error", hidden: true });
  const orderSave = h("button", { type: "submit", id: "category-order-save", class: "primary" }, t("categories.order.save"));
  const orderDialog = h("dialog", { id: "category-order-dialog" },
    h("form", { id: "category-order-form", method: "dialog", onsubmit: submitOrder },
      orderTitle,
      h("p", { class: "hint" }, t("categories.order.hint")),
      orderList,
      orderError,
      h("div", { class: "dialog-actions" },
        h("button", { type: "button", id: "category-order-cancel", onclick: () => orderDialog.close() }, t("categories.order.cancel")),
        orderSave)));

  const panel = h("div", { id: "tab-categories", class: "tab-panel" },
    h("div", { class: "toolbar" }, h("h2", {}, t("categories.title")), newBtn),
    err,
    h("table", { class: "table" },
      h("thead", {}, h("tr", {},
        h("th", {}, t("categories.col.id")), h("th", {}, t("categories.col.name")), h("th", {}, t("categories.col.slug")),
        h("th", {}, t("categories.col.parent")), h("th", {}, t("categories.col.position")), h("th", {}, t("categories.col.actions")))),
      tbody),
    dialog,
    orderDialog);
  root.append(panel);

  const nameOf = (id) => {
    const found = state.list.find((c) => c.id === id);
    return found ? found.name : `#${id}`;
  };
  const detailsOf = (e) => (e && e.details && e.details.length
    ? ctx.t("categories.errDetail", { message: e.message, details: e.details.map((d) => `${d.pointer} ${d.message}`).join("；") })
    : null);

  async function load() {
    err.hidden = true;
    newBtn.hidden = !ctx.can("category.create");
    try {
      state.list = await ctx.data.listCategories();
    } catch (e) {
      if (!ctx.alive()) return;
      err.textContent = ctx.errorMessage(e);
      err.hidden = false;
      return;
    }
    if (!ctx.alive()) return;
    rows(tbody, state.list, (c) => h("tr", {},
      h("td", {}, c.id),
      h("td", {}, c.name),
      h("td", {}, c.slug),
      h("td", {}, c.parent_id ? nameOf(c.parent_id) : t("categories.rootDash")),
      h("td", {}, c.position ?? 0),
      h("td", {},
        ctx.can("category.edit") ? h("button", { onclick: () => openDialog(c) }, t("categories.edit")) : "",
        ctx.can("category.edit") ? h("button", { class: "category-order-btn", onclick: () => openOrder(c) }, t("categories.order")) : "",
        ctx.can("category.delete") ? h("button", { class: "danger", onclick: () => remove(c) }, t("categories.delete")) : "")),
    { empty: t("categories.empty"), colspan: 6 });
  }

  function openDialog(category) {
    state.editing = category ?? null;
    dialogTitle.textContent = category ? t("categories.dialog.titleEdit", { id: category.id }) : t("categories.dialog.titleNew");
    nameInput.value = category?.name ?? "";
    slugInput.value = category?.slug ?? "";
    positionInput.value = category?.position ?? 0;
    // The parent menu leaves out the category itself; real cycle protection is the server's 422.
    fillSelect(
      parentSelect,
      [{ value: "", label: t("categories.dialog.root") }].concat(
        state.list.filter((c) => !category || c.id !== category.id)
          .map((c) => ({ value: c.id, label: t("categories.dialog.parentOption", { name: c.name, slug: c.slug }) }))),
      category?.parent_id ?? "");
    dialogError.hidden = true;
    dialog.showModal();
  }

  async function submit(e) {
    e.preventDefault();
    dialogError.hidden = true;
    try {
      await ctx.actions.saveCategory(state.editing ? state.editing.id : null, {
        name: nameInput.value, slug: slugInput.value, position: positionInput.value, parentId: parentSelect.value,
      });
      dialog.close();
      await load();
    } catch (e2) {
      dialogError.textContent = detailsOf(e2) ?? ctx.errorMessage(e2);
      dialogError.hidden = false;
    }
  }

  async function remove(category) {
    try {
      const r = await ctx.actions.deleteCategory(category.id);
      if (r && r.cancelled) return;
      err.hidden = true;
      await load();
    } catch (e) {
      // 409: still has child categories or products. The server's message is written for people, shown as is.
      err.textContent = ctx.errorMessage(e);
      err.hidden = false;
    }
  }

  // ── product order inside one category: load direct members, move up/down, submit the full arrangement ──
  function renderOrder() {
    orderList.replaceChildren();
    if (orderState.items.length === 0) {
      orderList.append(h("li", { class: "hint" }, t("categories.order.none")));
    }
    orderState.items.forEach((it, i) => {
      const move = (delta) => {
        const j = i + delta;
        [orderState.items[i], orderState.items[j]] = [orderState.items[j], orderState.items[i]];
        renderOrder();
      };
      const up = h("button", { type: "button", class: "order-up", disabled: i === 0, onclick: () => move(-1) }, t("categories.order.up"));
      const down = h("button", { type: "button", class: "order-down", disabled: i === orderState.items.length - 1, onclick: () => move(1) }, t("categories.order.down"));
      orderList.append(h("li", { "data-product-id": it.product_id },
        h("span", {}, it.title),
        it.status === 1 ? "" : h("span", { class: "badge" }, t("categories.order.draft")),
        up, down));
    });
    orderSave.disabled = orderState.items.length === 0;
  }

  async function openOrder(category) {
    orderState.category = category;
    orderTitle.textContent = t("categories.order.title", { name: category.name });
    orderError.hidden = true;
    try {
      orderState.items = await ctx.data.categoryProductOrder(category.id);
    } catch (e) {
      orderError.textContent = ctx.errorMessage(e);
      orderError.hidden = false;
      orderState.items = [];
    }
    renderOrder();
    orderDialog.showModal();
  }

  async function submitOrder(e) {
    e.preventDefault();
    orderError.hidden = true;
    try {
      await ctx.actions.saveCategoryProductOrder(orderState.category.id, orderState.items.map((it) => it.product_id));
      orderDialog.close();
    } catch (e2) {
      orderError.textContent = ctx.errorMessage(e2);
      orderError.hidden = false;
    }
  }

  await load();
}
