// 商品列表與編輯器，含巢狀 SKU. The list (#tab-products) and the editor (#tab-product-editor) are two panels of this one
// view; opening/closing the editor only toggles which one is visible, as the old panels did.
// The save action always receives the COMPLETE sku list (existing rows keep their id; rows the person removed are listed in
// removedSkuIds) and the core re-checks that against the server before it sends anything. Money is never scaled here.
import { productImagesSection } from "../parts/catalog-product-images.js";

export default async function (root, ctx) {
  const { h, clear, fillSelect, repeater, rows } = ctx.ui;
  const t = ctx.t;
  const state = {
    page: 1, total: 0, categories: [], editing: null, skus: null, fields: null, categoryBoxes: [], removedSkuIds: [],
  };
  let loadSeq = 0;
  const statusLabel = (s) => (s === 1 ? t("products.status.published") : t("products.status.draft"));

  // ── list panel ──
  const qInput = h("input", { type: "search", id: "product-q", placeholder: t("products.search.placeholder"),
    onkeydown: (e) => { if (e.key === "Enter") { e.preventDefault(); state.page = 1; load(); } } });
  const catFilter = h("select", { id: "product-category", onchange: () => { state.page = 1; load(); } });
  const statusFilter = h("select", { id: "product-status", onchange: () => { state.page = 1; load(); } },
    h("option", { value: "" }, t("products.filter.allStatus")),
    h("option", { value: "0" }, t("products.status.draft")),
    h("option", { value: "1" }, t("products.status.published")));
  const newBtn = h("button", { id: "new-product-btn", class: "primary", onclick: () => openEditor(null) }, t("products.new"));
  const listErr = h("p", { id: "products-error", class: "error", hidden: true });
  const tbody = h("tbody", { id: "products-tbody" });
  const prev = h("button", { id: "products-prev", onclick: () => { if (state.page > 1) { state.page--; load(); } } }, t("products.prev"));
  const next = h("button", { id: "products-next", onclick: () => { state.page++; load(); } }, t("products.next"));
  const pageInfo = h("span", { id: "products-pageinfo" });

  const listPanel = h("div", { id: "tab-products", class: "tab-panel" },
    h("div", { class: "toolbar" },
      h("h2", {}, t("products.title")),
      h("div", { class: "filters" }, qInput, catFilter, statusFilter,
        h("button", { id: "product-search", onclick: () => { state.page = 1; load(); } }, t("products.search"))),
      newBtn),
    listErr,
    h("table", { class: "table" },
      h("thead", {}, h("tr", {},
        h("th", {}, t("products.col.id")), h("th", {}, t("products.col.title")), h("th", {}, t("products.col.slug")),
        h("th", {}, t("products.col.category")), h("th", {}, t("products.col.skuCount")),
        h("th", {}, t("products.col.price")), h("th", {}, t("products.col.stock")), h("th", {}, t("products.col.status")),
        h("th", {}, t("products.col.actions")))),
      tbody),
    h("div", { class: "pager" }, prev, pageInfo, next));

  // ── editor panel ──
  const editorTitle = h("h2", { id: "product-editor-title" }, t("productEditor.titleDefault"));
  const editorStatus = h("p", { id: "product-editor-status", class: "hint" });
  const editorErr = h("p", { id: "product-editor-error", class: "error", hidden: true });
  const form = h("form", { id: "product-form", class: "schema-form wide" });
  const saveBtn = h("button", { id: "product-save", class: "primary", onclick: save }, t("productEditor.save"));
  const deleteBtn = h("button", { id: "product-delete", class: "danger", onclick: removeFromEditor }, t("productEditor.delete"));
  const editorPanel = h("div", { id: "tab-product-editor", class: "tab-panel", hidden: true },
    h("div", { class: "toolbar" },
      editorTitle,
      h("div", { class: "toolbar-actions" },
        h("button", { id: "product-back", onclick: backToList }, t("productEditor.back")), saveBtn, deleteBtn)),
    editorStatus, editorErr, form);
  root.append(listPanel, editorPanel);

  const categoryName = (id) => {
    const c = state.categories.find((x) => x.id === id);
    return c ? c.name : `#${id}`;
  };

  // ── list ──
  async function load() {
    const seq = ++loadSeq;
    listErr.hidden = true;
    newBtn.hidden = !ctx.can("product.create");
    state.categories = await ctx.data.categoryChoices();
    if (seq !== loadSeq || !ctx.alive()) return;

    catFilter.hidden = state.categories.length === 0;
    if (!catFilter.hidden) {
      fillSelect(catFilter, [{ value: "", label: t("products.filter.allCategories") }]
        .concat(state.categories.map((c) => ({ value: c.id, label: c.name }))), catFilter.value);
    }

    let data;
    try {
      data = await ctx.data.listProducts({ page: state.page, q: qInput.value, categoryId: catFilter.value, status: statusFilter.value });
    } catch (e) {
      if (seq !== loadSeq || !ctx.alive()) return;
      listErr.textContent = ctx.errorMessage(e);
      listErr.hidden = false;
      return;
    }
    if (seq !== loadSeq || !ctx.alive()) return;
    state.total = data.total ?? 0;
    state.page = data.page ?? 1;

    rows(tbody, data.products, (p) => {
      const skus = p.skus || [];
      const stock = skus.reduce((sum, s) => sum + (Number(s.stock_qty) || 0), 0);
      return h("tr", { "data-slug": p.slug },
        h("td", {}, p.id),
        h("td", {}, p.title,
          p.subtitle ? h("div", { class: "hint product-subtitle" }, p.subtitle) : ""),
        h("td", { class: "mono" }, p.slug),
        h("td", {}, (p.category_ids || []).map(categoryName).join(t("products.categoryJoin")) || "—"),
        h("td", {}, skus.length),
        h("td", { class: "money" }, ctx.format.priceRange(skus)),
        h("td", {}, stock),
        h("td", {}, p.status === 1
          ? h("span", { class: "badge ok" }, t("products.status.published"))
          : h("span", { class: "badge" }, t("products.status.draft"))),
        h("td", {},
          h("button", { onclick: () => openEditor(p.id) }, ctx.can("product.edit") ? t("products.edit") : t("products.view")),
          ctx.can("product.delete") ? h("button", { class: "danger", onclick: () => remove(p) }, t("products.delete")) : ""));
    }, {
      empty: state.total === 0 && !qInput.value.trim() ? t("products.empty.none") : t("products.empty.noMatch"),
      colspan: 9,
    });

    const pages = Math.max(1, Math.ceil((state.total || 0) / (data.pageSize || 20)));
    pageInfo.textContent = t("common.pageInfo", { page: state.page, pages, total: state.total || 0 });
    prev.disabled = state.page <= 1;
    next.disabled = state.page >= pages;
  }

  async function remove(p) {
    try {
      const r = await ctx.actions.deleteProduct(p.id);
      if (r && r.cancelled) return;
      listErr.hidden = true;
      await load();
    } catch (e) {
      listErr.textContent = ctx.errorMessage(e);
      listErr.hidden = false;
    }
  }

  async function removeFromEditor() {
    if (!state.editing) return;
    try { await ctx.actions.deleteProduct(state.editing.id); } catch { /* the list reload below clears its banner, as before */ }
    backToList();
  }

  function backToList() {
    editorPanel.hidden = true;
    listPanel.hidden = false;
    load();
  }

  // ── one SKU row. read() closes over the row's own inputs: no DOM queries when collecting. ──
  function skuRow(sku, removeRow) {
    const code = h("input", { type: "text", class: "sku-code", Value: sku?.sku_code ?? "", placeholder: t("productEditor.sku.codePlaceholder") });
    const price = h("input", { type: "number", class: "sku-price", step: "1", min: "0", Value: sku?.price_amount ?? 0 });
    const currency = h("input", { type: "text", class: "sku-currency", maxlength: "3", Value: sku?.currency ?? "TWD" });
    const stock = h("input", { type: "number", class: "sku-stock", step: "1", min: "0", Value: sku?.stock_qty ?? 0 });
    const active = h("input", { type: "checkbox", class: "sku-active", checked: sku?.is_active ?? true });

    const preview = h("span", { class: "money-preview" });
    const syncPreview = () => {
      preview.textContent = ctx.format.money(ctx.format.parseAmount(price), currency.value.trim().toUpperCase() || "TWD");
    };
    price.addEventListener("input", syncPreview);
    currency.addEventListener("input", syncPreview);
    syncPreview();

    // options is a jsonb object (e.g. {"尺寸":"M"}); the second nesting level is the same repeater.
    const optionsHost = h("div", { class: "kv-rows" });
    const options = repeater(optionsHost, optionRow);
    options.reset(Object.entries(sku?.options ?? {}).map(([key, value]) => ({ key, value })));

    const node = h("tr", { class: "sku-row" },
      h("td", {}, code),
      h("td", {}, optionsHost,
        h("button", { type: "button", class: "tiny", onclick: () => options.add() }, t("productEditor.sku.addOption"))),
      h("td", {}, price, preview),
      h("td", {}, currency),
      h("td", {}, stock),
      h("td", { class: "center" }, active),
      h("td", {}, h("button", { type: "button", class: "danger sku-remove", onclick: () => {
        if (sku?.id !== undefined && sku?.id !== null) state.removedSkuIds.push(sku.id);
        removeRow();
      } }, t("productEditor.sku.remove"))));

    return {
      node,
      read() {
        const out = {
          sku_code: code.value,
          options: Object.fromEntries(options.read().filter((o) => o.key !== "").map((o) => [o.key, o.value])),
          price: price.value,
          currency: currency.value,
          stock: stock.value,
          is_active: active.checked,
        };
        // Existing rows always carry their id back, else the server would delete them as "absent".
        if (sku?.id !== undefined && sku?.id !== null) out.id = sku.id;
        return out;
      },
    };
  }

  function optionRow(pair, removeRow) {
    const original = pair?.value;
    const key = h("input", { type: "text", class: "kv-key", Value: pair?.key ?? "", placeholder: t("productEditor.option.keyPlaceholder") });
    const value = h("input", { type: "text", class: "kv-value", Value: original ?? "", placeholder: t("productEditor.option.valuePlaceholder") });
    return {
      node: h("div", { class: "kv-row" },
        key, h("span", { class: "kv-eq" }, "="), value,
        h("button", { type: "button", class: "danger tiny", onclick: removeRow }, "×")),
      read: () => ({
        key: key.value.trim(),
        // An untouched value goes back as it was, which keeps non-string jsonb types (number/boolean).
        value: original !== undefined && value.value === String(original) ? original : value.value,
      }),
    };
  }

  // ── editor ──
  function buildEditor(product) {
    clear(form);
    state.removedSkuIds = [];
    const editable = ctx.can("product.edit") || (product === null && ctx.can("product.create"));

    const field = (label, input, pointer, hint) => {
      return h("label", { class: "field", "data-pointer": pointer },
        h("span", { class: "field-label" }, label),
        input,
        hint ? h("span", { class: "hint" }, hint) : "",
        h("span", { class: "field-error" }));
    };

    const title = h("input", { type: "text", id: "product-title", "data-pointer": "/title", Value: product?.title ?? "" });
    const subtitle = h("input", { type: "text", id: "product-subtitle", "data-pointer": "/subtitle", maxlength: "200", Value: product?.subtitle ?? "" });
    const slug = h("input", { type: "text", id: "product-slug", "data-pointer": "/slug", Value: product?.slug ?? "", placeholder: t("productEditor.field.slugPlaceholder") });
    const description = h("textarea", { id: "product-description", "data-pointer": "/description", rows: "4", value: product?.description ?? "" });
    const status = h("select", { id: "product-status-select", "data-pointer": "/status" });
    fillSelect(status, [{ value: 0, label: t("products.status.draft") }, { value: 1, label: t("products.status.published") }], product?.status ?? 0);

    form.append(h("fieldset", { class: "obj" }, h("legend", {}, t("productEditor.basics")),
      field(t("productEditor.field.title"), title, "/title"),
      field(t("productEditor.field.subtitle"), subtitle, "/subtitle", t("productEditor.field.subtitleHint")),
      field(t("productEditor.field.slug"), slug, "/slug", t("productEditor.field.slugHint")),
      field(t("productEditor.field.description"), description, "/description"),
      field(t("productEditor.field.status"), status, "/status", t("productEditor.field.statusHint"))));

    // Category links: full replacement. What is ticked is the complete set after saving.
    state.categoryBoxes = [];
    if (state.categories.length > 0) {
      const selected = new Set(product?.category_ids ?? []);
      const boxes = h("div", { class: "checkbox-grid" });
      for (const c of state.categories) {
        const box = h("input", { type: "checkbox", class: "category-box", value: String(c.id), checked: selected.has(c.id) });
        state.categoryBoxes.push({ id: c.id, box });
        boxes.append(h("label", { class: "checkbox" }, box, h("span", {}, c.name)));
      }
      form.append(h("fieldset", { class: "obj", "data-pointer": "/category_ids" },
        h("legend", {}, t("productEditor.categories")), boxes,
        h("span", { class: "hint" }, t("productEditor.categoriesHint")),
        h("span", { class: "field-error" })));
    }

    // SKU: the nested repeater.
    const skuBody = h("tbody", { id: "sku-tbody" });
    const skuTable = h("table", { class: "table sku-table" },
      h("thead", {}, h("tr", {},
        h("th", {}, t("productEditor.sku.code")), h("th", {}, t("productEditor.sku.options")), h("th", {}, t("productEditor.sku.price")),
        h("th", {}, t("productEditor.sku.currency")), h("th", {}, t("productEditor.sku.stock")), h("th", {}, t("productEditor.sku.active")), h("th", {}, ""))),
      skuBody);
    state.skus = repeater(skuBody, skuRow);
    state.skus.reset(product?.skus ?? []);
    form.append(h("fieldset", { class: "obj", "data-pointer": "/skus" },
      h("legend", {}, t("productEditor.sku")),
      skuTable,
      h("button", { type: "button", id: "add-sku-btn", onclick: () => state.skus.add() }, t("productEditor.sku.add")),
      h("span", { class: "hint" }, t("productEditor.sku.hint")),
      h("span", { class: "field-error" })));

    // Images are a sub-resource with their own actions: not part of collectBody() and not of the save button.
    form.append(productImagesSection(ctx, product));

    state.fields = { title, subtitle, slug, description, status };
    if (!editable) ctx.ui.$$("input, select, textarea, button", form).forEach((n) => { n.disabled = true; });
    saveBtn.hidden = !editable;
    deleteBtn.hidden = product === null || !ctx.can("product.delete");
  }

  async function openEditor(productID) {
    try {
      state.categories = await ctx.data.categoryChoices();
      const product = productID === null ? null : await ctx.data.getProduct(productID);
      if (!ctx.alive()) return;
      state.editing = product;
      listPanel.hidden = true;
      editorPanel.hidden = false;
      editorTitle.textContent = product ? t("productEditor.titleEdit", { title: product.title }) : t("productEditor.titleNew");
      editorStatus.textContent = product ? statusLabel(product.status) : t("productEditor.statusNew");
      editorErr.hidden = true;
      buildEditor(product);
    } catch (e) {
      listErr.textContent = ctx.errorMessage(e);
      listErr.hidden = false;
    }
  }

  function collectBody() {
    const { title, subtitle, slug, description, status } = state.fields;
    const body = {
      title: title.value, subtitle: subtitle.value, slug: slug.value, description: description.value, status: Number(status.value),
      skus: state.skus.read(), // complete list, always
      removedSkuIds: state.removedSkuIds.slice(),
    };
    if (state.categories.length > 0) {
      body.category_ids = state.categoryBoxes.filter((c) => c.box.checked).map((c) => c.id);
    }
    return body;
  }

  // 422 details (or the core's field errors) -> mark the matching fields.
  function showValidationErrors(err) {
    ctx.ui.$$(".invalid", form).forEach((n) => n.classList.remove("invalid"));
    ctx.ui.$$(".field-error", form).forEach((n) => { n.textContent = ""; });
    editorErr.textContent = ctx.errorMessage(err);
    editorErr.hidden = false;
    const entries = err && err.details && err.details.length
      ? err.details.map((d) => [d.pointer, d.message])
      : Object.entries(ctx.fieldErrors(err));
    for (const [pointer, message] of entries) {
      let target = null;
      try { target = ctx.ui.$(`[data-pointer="${pointer}"]`, form) || ctx.ui.$(`[data-pointer^="${pointer}"]`, form); } catch { target = null; }
      if (target) {
        const holder = target.closest(".field") || target;
        holder.classList.add("invalid");
        const slot = ctx.ui.$(".field-error", holder);
        if (slot) slot.textContent = message;
      }
    }
  }

  async function save() {
    editorErr.hidden = true;
    const body = collectBody();
    try {
      const saved = await ctx.actions.saveProduct(state.editing ? state.editing.id : null, body);
      state.editing = saved;
      // Redraw: newly created SKUs only get their id now; without this the next save would treat them as new rows again.
      buildEditor(saved);
      editorTitle.textContent = t("productEditor.titleEdit", { title: saved.title });
      editorStatus.textContent = t("productEditor.savedStatus", { status: statusLabel(saved.status) });
    } catch (err) {
      showValidationErrors(err);
    }
  }

  await load();
}
