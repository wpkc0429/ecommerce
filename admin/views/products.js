// 商品列表與編輯器，含巢狀 SKU（change admin-commerce-catalog, design D2/D4/D6/D8）。
//
// 三件事在這個檔案裡是不可協商的：
//
//  1. **金額絕不縮放**（design D4）。本檔案不存在 `/100` 或 `*100`；顯示一律走
//     lib/money.js，輸入一律是整數。
//  2. **儲存時必送完整的 skus 與 category_ids**（design D8）。伺服器的巢狀
//     upsert 語意是「帶 id 的更新、不帶 id 的新建、**既有但未出現在酬載中的
//     直接刪除**」，category_ids 同樣是全量取代。漏送一列＝靜默刪除一個 SKU
//     連同它的價格與庫存。因此只有一顆儲存鍵，不提供「只存基本資料」。
//  3. **狀態住在 closure**（design D2）。每一列 SKU 自己持有 read()，收集時
//     不做任何 DOM 查詢——見 lib/dom.js 的 repeater。
import { api } from "../lib/api.js";
import { $, el, clear, fillSelect, repeater } from "../lib/dom.js";
import { formatMoney, parseAmount, priceRange } from "../lib/money.js";
import { showValidationErrors } from "../lib/schemaform.js";
import { can, whenCan, readOnlyUnless } from "../lib/rbac.js";

const PAGE_SIZE = 20;

export function createProductsView(ctx) {
  const state = {
    page: 1,
    total: 0,
    categories: [],   // 平坦分類（可能因缺 category.view 而為空）
    editing: null,    // 目前編輯中的商品（新建時為 null）
    skus: null,       // 目前編輯器的 SKU repeater
    fields: null,     // 目前編輯器的欄位參考
    categoryBoxes: [], // 分類 checkbox 的參考
  };

  // ── 分類（篩選用 + 編輯器的關聯勾選）────────────────────────────
  async function ensureCategories() {
    if (!can("category.view")) { state.categories = []; return; }
    try {
      const data = await api("GET", `/admin/shops/${ctx.getShopID()}/categories?flat=1`);
      state.categories = data.categories || [];
    } catch {
      state.categories = [];
    }
  }

  function categoryName(id) {
    const c = state.categories.find((x) => x.id === id);
    return c ? c.name : `#${id}`;
  }

  // ── 列表 ────────────────────────────────────────────────────────
  function query() {
    const params = new URLSearchParams({ page: String(state.page), page_size: String(PAGE_SIZE) });
    const q = $("#product-q").value.trim();
    if (q) params.set("q", q);
    const cat = $("#product-category").value;
    if (cat) params.set("category_id", cat);
    const status = $("#product-status").value;
    if (status !== "") params.set("status", status);
    return params.toString();
  }

  async function load() {
    const err = $("#products-error");
    err.hidden = true;
    $("#new-product-btn").hidden = !can("product.create");
    await ensureCategories();

    const filter = $("#product-category");
    filter.hidden = state.categories.length === 0;
    if (!filter.hidden) {
      const keep = filter.value;
      fillSelect(filter, [{ value: "", label: "全部分類" }]
        .concat(state.categories.map((c) => ({ value: c.id, label: c.name }))), keep);
    }

    let data;
    try {
      data = await api("GET", `/admin/shops/${ctx.getShopID()}/products?${query()}`);
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
      return;
    }
    state.total = data.total ?? 0;
    state.page = data.page ?? 1;

    const tbody = clear($("#products-tbody"));
    if ((data.products || []).length === 0) {
      tbody.append(el("tr", {}, el("td", { colspan: "9", class: "empty" },
        state.total === 0 && !$("#product-q").value.trim() ? "尚未建立任何商品。" : "沒有符合條件的商品。")));
    }
    for (const p of data.products || []) {
      const skus = p.skus || [];
      const stock = skus.reduce((sum, s) => sum + (Number(s.stock_qty) || 0), 0);
      tbody.append(el("tr", { "data-slug": p.slug },
        el("td", {}, p.id),
        el("td", {}, p.title),
        el("td", { class: "mono" }, p.slug),
        el("td", {}, (p.category_ids || []).map(categoryName).join("、") || "—"),
        el("td", {}, skus.length),
        // 金額：原值 + 千分位，不縮放（design D4）
        el("td", { class: "money" }, priceRange(skus)),
        el("td", {}, stock),
        el("td", {}, p.status === 1
          ? el("span", { class: "badge ok" }, "已發佈")
          : el("span", { class: "badge" }, "草稿")),
        el("td", {},
          el("button", { onclick: () => openEditor(p.id) }, can("product.edit") ? "編輯" : "檢視"),
          whenCan("product.delete", () =>
            el("button", { class: "danger", onclick: () => remove(p) }, "刪除")))));
    }

    const pages = Math.max(1, Math.ceil(state.total / PAGE_SIZE));
    $("#products-pageinfo").textContent = `第 ${state.page} / ${pages} 頁 ｜ 共 ${state.total} 筆`;
    $("#products-prev").disabled = state.page <= 1;
    $("#products-next").disabled = state.page >= pages;
  }

  async function remove(p) {
    if (!confirm(`確定刪除商品「${p.title}」？其 SKU 會一併刪除。`)) return;
    const err = $("#products-error");
    err.hidden = true;
    try {
      await api("DELETE", `/admin/shops/${ctx.getShopID()}/products/${p.id}`);
      await load();
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
    }
  }

  // ── SKU 的一列（design D2）─────────────────────────────────────
  //
  // renderRow 回傳 { node, read }；read() 閉包在這一列自己的 input 參考上。
  // 收集時走 repeater 的 entries 陣列呼叫每一列的 read()，一次 DOM 查詢都不做。
  function skuRow(sku, remove) {
    const code = el("input", { type: "text", class: "sku-code", value: sku?.sku_code ?? "", placeholder: "SKU-001" });
    // 金額輸入：step=1、min=0，收到的整數原封送出（design D4）。
    const price = el("input", { type: "number", class: "sku-price", step: "1", min: "0", value: sku?.price_amount ?? 0 });
    const currency = el("input", { type: "text", class: "sku-currency", maxlength: "3", value: sku?.currency ?? "TWD" });
    const stock = el("input", { type: "number", class: "sku-stock", step: "1", min: "0", value: sku?.stock_qty ?? 0 });
    const active = el("input", { type: "checkbox", class: "sku-active" });
    active.checked = sku?.is_active ?? true;

    const preview = el("span", { class: "money-preview" });
    const syncPreview = () => {
      preview.textContent = formatMoney(parseAmount(price), currency.value.trim().toUpperCase() || "TWD");
    };
    price.addEventListener("input", syncPreview);
    currency.addEventListener("input", syncPreview);
    syncPreview();

    // options 是一個 jsonb 物件（如 {"尺寸":"M"}）。巢狀第二層用的是同一個
    // repeater——它對「列裡面有什麼」一無所知，所以巢狀不需要新機制。
    const optionsHost = el("div", { class: "kv-rows" });
    const options = repeater(optionsHost, optionRow);
    options.reset(Object.entries(sku?.options ?? {}).map(([key, value]) => ({ key, value })));

    const node = el("tr", { class: "sku-row" },
      el("td", {}, code),
      el("td", {}, optionsHost,
        el("button", { type: "button", class: "tiny", onclick: () => options.add() }, "＋ 選項")),
      el("td", {}, price, preview),
      el("td", {}, currency),
      el("td", {}, stock),
      el("td", { class: "center" }, active),
      el("td", {}, el("button", { type: "button", class: "danger sku-remove", onclick: remove }, "移除")));

    return {
      node,
      read() {
        const out = {
          sku_code: code.value.trim(),
          options: Object.fromEntries(
            options.read().filter((o) => o.key !== "").map((o) => [o.key, o.value])),
          price_amount: parseAmount(price), // 整數，不縮放
          currency: currency.value.trim().toUpperCase() || "TWD",
          stock_qty: parseAmount(stock),
          is_active: active.checked,
        };
        // 帶 id → 更新既有 SKU；不帶 → 新建。既有列一律把 id 帶回去，
        // 否則伺服器會把它當成「未出現」而刪除（design D8）。
        if (sku?.id !== undefined && sku?.id !== null) out.id = sku.id;
        return out;
      },
    };
  }

  function optionRow(pair, remove) {
    const original = pair?.value;
    const key = el("input", { type: "text", class: "kv-key", value: pair?.key ?? "", placeholder: "尺寸" });
    const value = el("input", { type: "text", class: "kv-value", value: original ?? "", placeholder: "M" });
    return {
      node: el("div", { class: "kv-row" },
        key, el("span", { class: "kv-eq" }, "="), value,
        el("button", { type: "button", class: "danger tiny", onclick: remove }, "×")),
      read: () => ({
        key: key.value.trim(),
        // 未被改動的值原樣送回，保住 jsonb 裡非字串的型別（數字/布林）；
        // 改動過的值就是使用者輸入的字串。
        value: original !== undefined && value.value === String(original) ? original : value.value,
      }),
    };
  }

  // ── 編輯器 ──────────────────────────────────────────────────────
  function buildEditor(product) {
    const form = clear($("#product-form"));
    const editable = can("product.edit") || (product === null && can("product.create"));

    const field = (label, input, pointer, hint) => {
      input.dataset.pointer = pointer;
      return el("label", { class: "field", "data-pointer": pointer },
        el("span", { class: "field-label" }, label),
        input,
        hint ? el("span", { class: "hint" }, hint) : "",
        el("span", { class: "field-error" }));
    };

    const title = el("input", { type: "text", id: "product-title", value: product?.title ?? "" });
    const slug = el("input", { type: "text", id: "product-slug", value: product?.slug ?? "", placeholder: "my-product" });
    const description = el("textarea", { id: "product-description", rows: "4" });
    description.value = product?.description ?? "";
    const status = el("select", { id: "product-status-select" });
    fillSelect(status, [{ value: 0, label: "草稿" }, { value: 1, label: "已發佈" }], product?.status ?? 0);

    const basics = el("fieldset", { class: "obj" }, el("legend", {}, "基本資料"),
      field("標題", title, "/title"),
      field("Slug", slug, "/slug", "小寫英數與連字號，商家內唯一"),
      field("描述", description, "/description"),
      field("狀態", status, "/status", "「已發佈」後才會出現在前台公開端點"));
    form.append(basics);

    // 分類關聯：全量取代語意——勾選的即是儲存後的完整集合。
    state.categoryBoxes = [];
    if (state.categories.length > 0) {
      const selected = new Set(product?.category_ids ?? []);
      const boxes = el("div", { class: "checkbox-grid" });
      for (const c of state.categories) {
        const box = el("input", { type: "checkbox", class: "category-box", value: String(c.id) });
        box.checked = selected.has(c.id);
        state.categoryBoxes.push({ id: c.id, box });
        boxes.append(el("label", { class: "checkbox" }, box, el("span", {}, c.name)));
      }
      form.append(el("fieldset", { class: "obj", "data-pointer": "/category_ids" },
        el("legend", {}, "分類"), boxes,
        el("span", { class: "hint" }, "儲存時以勾選結果全量取代既有關聯"),
        el("span", { class: "field-error" })));
    }

    // SKU：巢狀 repeater。
    const skuBody = el("tbody", { id: "sku-tbody" });
    const skuTable = el("table", { class: "table sku-table" },
      el("thead", {}, el("tr", {},
        el("th", {}, "SKU 代碼"), el("th", {}, "選項"), el("th", {}, "價格（最小貨幣單位整數）"),
        el("th", {}, "幣別"), el("th", {}, "庫存"), el("th", {}, "啟用"), el("th", {}, ""))),
      skuBody);
    state.skus = repeater(skuBody, skuRow);
    state.skus.reset(product?.skus ?? []);
    form.append(el("fieldset", { class: "obj", "data-pointer": "/skus" },
      el("legend", {}, "SKU"),
      skuTable,
      el("button", { type: "button", id: "add-sku-btn", onclick: () => state.skus.add() }, "＋ 新增 SKU"),
      el("span", { class: "hint" },
        "價格是最小貨幣單位的整數：TWD 的 3400 就是 NT$3,400。儲存時會送出上表的完整內容，被移除的列即為刪除。"),
      el("span", { class: "field-error" })));

    state.fields = { title, slug, description, status };
    if (!editable) readOnlyUnless("product.edit", form);
    $("#product-save").hidden = !editable;
    $("#product-delete").hidden = product === null || !can("product.delete");
  }

  async function openEditor(productID) {
    await ensureCategories();
    const product = productID === null
      ? null
      : await api("GET", `/admin/shops/${ctx.getShopID()}/products/${productID}`);
    state.editing = product;
    ctx.showTab("product-editor");
    $("#product-editor-title").textContent = product ? `編輯商品：${product.title}` : "新增商品";
    $("#product-editor-status").textContent = product
      ? (product.status === 1 ? "已發佈" : "草稿")
      : "尚未建立";
    $("#product-editor-error").hidden = true;
    buildEditor(product);
  }

  function collectBody() {
    const { title, slug, description, status } = state.fields;
    const body = {
      title: title.value.trim(),
      slug: slug.value.trim(),
      description: description.value,
      status: Number(status.value),
      // 全量送出（design D8）：漏送即刪除。
      skus: state.skus.read(),
    };
    if (state.categories.length > 0) {
      body.category_ids = state.categoryBoxes.filter((c) => c.box.checked).map((c) => c.id);
    }
    return body;
  }

  async function save() {
    const form = $("#product-form");
    const errBox = $("#product-editor-error");
    errBox.hidden = true;
    const body = collectBody();
    try {
      const saved = state.editing
        ? await api("PUT", `/admin/shops/${ctx.getShopID()}/products/${state.editing.id}`, body)
        : await api("POST", `/admin/shops/${ctx.getShopID()}/products`, body);
      state.editing = saved;
      // 重畫編輯器：新建的 SKU 這時才拿到 id，不重畫就會在下次儲存被當成新列。
      buildEditor(saved);
      $("#product-editor-title").textContent = `編輯商品：${saved.title}`;
      $("#product-editor-status").textContent =
        `已儲存 ✓ ｜ ${saved.status === 1 ? "已發佈" : "草稿"}`;
    } catch (err) {
      showValidationErrors(form, errBox, err);
    }
  }

  // ── 事件接線 ────────────────────────────────────────────────────
  $("#new-product-btn").addEventListener("click", () => openEditor(null));
  $("#product-search").addEventListener("click", () => { state.page = 1; load(); });
  $("#product-q").addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.preventDefault(); state.page = 1; load(); }
  });
  $("#product-category").addEventListener("change", () => { state.page = 1; load(); });
  $("#product-status").addEventListener("change", () => { state.page = 1; load(); });
  $("#products-prev").addEventListener("click", () => { if (state.page > 1) { state.page--; load(); } });
  $("#products-next").addEventListener("click", () => { state.page++; load(); });
  $("#product-back").addEventListener("click", () => ctx.showTab("products"));
  $("#product-save").addEventListener("click", save);
  $("#product-delete").addEventListener("click", async () => {
    if (!state.editing) return;
    await remove(state.editing);
    ctx.showTab("products");
  });

  return { load };
}
