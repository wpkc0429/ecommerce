// JSON Schema 驅動的表單（原 app.js task 9.2，逐字搬移）。
//
// **只給 CMS 用。** 商品／SKU／分類刻意不走這裡（change
// admin-commerce-catalog, design D3）：CMS 之所以需要 schema 驅動，是因為頁型
// 的形狀在撰寫 admin 時未知（由主題作者定義，執行期才收到 JSON Schema）；商品
// 的形狀是 catalog.ProductInput 這個 Go struct 與 openapi.yaml 的請求 schema，
// 撰寫時完全已知且穩定。把已知形狀塞進 schema 驅動表單，等於憑空多維護一份會
// 與 OpenAPI 漂移的 schema，並沿用下方 collect 路徑（用 label 文字回頭找 DOM）
// ——那條路徑在 N 列 × 每列 M 個同名子欄位的巢狀下會靜默配錯值。
//
// 同理，x-editor-order 這個關鍵字（change cms-editor-field-order）存在的原因是
// schema 存在 Postgres jsonb 而 jsonb 不保留鍵序；手寫表單的欄位順序就是原始碼
// 的順序，那個問題在固定形狀表單上根本不存在。
import { el } from "./dom.js";
import { ApiError } from "./api.js";

export function resolveRef(node, root) {
  let cur = node;
  for (let i = 0; i < 8 && cur && cur.$ref && cur.$ref.startsWith("#/"); i++) {
    let target = root;
    for (const seg of cur.$ref.slice(2).split("/")) {
      target = target?.[seg.replaceAll("~1", "/").replaceAll("~0", "~")];
    }
    if (!target) return cur;
    cur = target;
  }
  return cur;
}

// orderedKeys(props, root)：依 x-editor-order 排序一個 properties 物件的鍵
// （design cms-editor-field-order；與 api/internal/cms/fieldorder.go 的
// FieldOrder 規則一致，各自實作以配合 admin 無建置的 vanilla JS 限制）——
// jsonb 不保留物件鍵順序，schema 撰寫順序無法單靠 Object.entries() 還原，
// 需要顯式標註。已標註者依整數升冪排前（同值以鍵名字母序打破平手），未標
// 註者排在最後、彼此依鍵名字母序排列（決定性 fallback，取代目前不可預期
// 的順序）。
export function orderedKeys(props, root) {
  const keys = Object.keys(props || {});
  const orderOf = (k) => {
    const ps = resolveRef(props[k], root);
    const v = ps?.["x-editor-order"];
    return typeof v === "number" && Number.isInteger(v) ? v : null;
  };
  return keys.sort((a, b) => {
    const oa = orderOf(a), ob = orderOf(b);
    if (oa !== null && ob !== null) return oa - ob || a.localeCompare(b);
    if (oa !== null) return -1;
    if (ob !== null) return 1;
    return a.localeCompare(b);
  });
}

function sectionBranches(items, root) {
  const resolved = resolveRef(items || {}, root);
  const oneOf = resolved?.oneOf;
  if (!Array.isArray(oneOf) || oneOf.length === 0) return null;
  const out = [];
  for (const b of oneOf) {
    const bm = resolveRef(b, root);
    const c = bm?.properties?.type?.const;
    if (!c) return null;
    out.push({ type: c, schema: bm });
  }
  return out;
}

export function buildForm(schema, value, container, root, pointer = "") {
  container.textContent = "";
  const props = resolveRef(schema, root)?.properties || {};
  for (const key of orderedKeys(props, root)) {
    const ps = resolveRef(props[key], root);
    container.append(buildField(key, ps, value?.[key], root, `${pointer}/${key}`));
  }
}

function buildField(key, ps, value, root, pointer) {
  const type = ps.type;
  const editor = ps["x-editor"];
  const label = ps.title || key;

  if (type === "object" || (ps.properties && !type)) {
    const fieldset = el("fieldset", { class: "obj", "data-pointer": pointer }, el("legend", {}, label));
    const props = ps.properties || {};
    for (const k of orderedKeys(props, root)) {
      const child = resolveRef(props[k], root);
      fieldset.append(buildField(k, child, value?.[k], root, `${pointer}/${k}`));
    }
    return fieldset;
  }

  if (type === "array") {
    const branches = sectionBranches(ps.items, root);
    if (branches) return buildSectionsField(label, ps, value, root, pointer, branches);
    return buildListField(label, ps, value, root, pointer);
  }

  // 葉節點控件：依 x-editor 對應（design D6 — 同一份 schema 驅動驗證與編輯器）。
  const wrap = el("label", { class: "field", "data-pointer": pointer });
  wrap.append(el("span", { class: "field-label" }, label));
  let input;
  if (type === "boolean") {
    input = el("input", { type: "checkbox" });
    input.checked = Boolean(value ?? ps.default);
  } else if (type === "number" || type === "integer") {
    input = el("input", { type: "number", value: value ?? ps.default ?? "" });
  } else if (editor === "color") {
    input = el("input", { type: "color", value: value ?? ps.default ?? "#000000" });
  } else if (editor === "richtext") {
    input = el("textarea", { rows: 6 });
    input.value = value ?? ps.default ?? "";
  } else if (editor === "image") {
    input = el("input", { type: "url", placeholder: "https://…", value: value ?? ps.default ?? "" });
  } else {
    input = el("input", { type: "text", value: value ?? ps.default ?? "" });
  }
  input.dataset.pointer = pointer;
  input.dataset.jsonType = type || "string";
  wrap.append(input, el("span", { class: "field-error" }));
  if (editor === "richtext") {
    // change cms-rich-text-sanitization：伺服器在儲存時依 allowlist 改寫 HTML，
    // 儲存後重新載入即看到實際存下的值。
    wrap.append(el("span", { class: "hint" },
      "支援段落、h2–h4、粗斜體、清單、引言、連結、https 圖片與表格；其他標籤（含 h1）、樣式與腳本會在儲存時移除。"));
  }
  return wrap;
}

// 一般陣列（如導覽列）：逐列編輯 + 新增/刪除。
function buildListField(label, ps, value, root, pointer) {
  const items = resolveRef(ps.items || {}, root);
  const box = el("fieldset", { class: "list", "data-pointer": pointer, "data-kind": "list" },
    el("legend", {}, label));
  const rows = el("div", { class: "list-rows" });
  box.append(rows);

  const addRow = (itemValue) => {
    const row = el("div", { class: "list-row" });
    const body = el("div", { class: "list-row-body" });
    if (items.properties) {
      for (const k of orderedKeys(items.properties, root)) {
        const child = resolveRef(items.properties[k], root);
        body.append(buildField(k, child, itemValue?.[k], root, ""));
      }
    } else {
      body.append(buildField("值", items, itemValue, root, ""));
    }
    row.append(body, el("button", { type: "button", class: "danger", onclick: () => row.remove() }, "刪除"));
    rows.append(row);
  };
  (Array.isArray(value) ? value : []).forEach(addRow);
  box.append(el("button", { type: "button", onclick: () => addRow(undefined) }, "＋ 新增一列"));
  box._collect = () =>
    [...rows.children].map((row) => {
      if (items.properties) {
        const obj = {};
        for (const [k] of Object.entries(items.properties)) {
          const field = [...row.querySelectorAll(".field")].find(
            (f) => f.querySelector(".field-label").textContent === (items.properties[k].title || k));
          if (field) obj[k] = readInput(field.querySelector("input,textarea"));
        }
        return obj;
      }
      return readInput(row.querySelector("input,textarea"));
    });
  return box;
}

// sections 陣列（x-editor: sections）：依區塊型別 schema 動態組合（design D6）。
function buildSectionsField(label, ps, value, root, pointer, branches) {
  const box = el("fieldset", { class: "sections", "data-pointer": pointer, "data-kind": "sections" },
    el("legend", {}, label));
  const rows = el("div", { class: "section-rows" });
  box.append(rows);

  const addSection = (item) => {
    const branch = branches.find((b) => b.type === item?.type);
    if (!branch) return;
    const row = el("div", { class: "section-row" });
    const head = el("div", { class: "section-head" },
      el("strong", {}, branch.type),
      el("span", { class: "section-actions" },
        el("button", { type: "button", onclick: () => row.previousElementSibling?.before(row) }, "↑"),
        el("button", { type: "button", onclick: () => row.nextElementSibling?.after(row) }, "↓"),
        el("button", { type: "button", class: "danger", onclick: () => row.remove() }, "刪除")));
    const body = el("div", { class: "section-body" });
    const branchProps = branch.schema.properties || {};
    for (const k of orderedKeys(branchProps, root)) {
      if (k === "type") continue;
      const child = resolveRef(branchProps[k], root);
      body.append(buildField(k, child, item?.[k], root, ""));
    }
    row._type = branch.type;
    row._schema = branch.schema;
    row.append(head, body);
    rows.append(row);
  };

  (Array.isArray(value) ? value : []).forEach(addSection);

  const picker = el("div", { class: "section-picker" });
  for (const b of branches) {
    picker.append(el("button", { type: "button", onclick: () => addSection({ type: b.type }) }, `＋ ${b.type}`));
  }
  box.append(picker);
  box._collect = () =>
    [...rows.children].map((row) => {
      const obj = { type: row._type };
      for (const [k] of Object.entries(row._schema.properties || {})) {
        if (k === "type") continue;
        const field = [...row.querySelectorAll(":scope > .section-body > .field, :scope > .section-body > fieldset")]
          .find((f) => (f.querySelector(".field-label,legend")?.textContent) === (row._schema.properties[k].title || k));
        if (field) obj[k] = collectNode(field);
      }
      return obj;
    });
  return box;
}

function readInput(input) {
  if (!input) return undefined;
  if (input.type === "checkbox") return input.checked;
  if (input.dataset.jsonType === "number") return input.value === "" ? 0 : Number(input.value);
  if (input.dataset.jsonType === "integer") return input.value === "" ? 0 : parseInt(input.value, 10);
  return input.value;
}

function collectNode(node) {
  if (node._collect) return node._collect();
  if (node.classList.contains("field")) return readInput(node.querySelector("input,textarea"));
  if (node.tagName === "FIELDSET") {
    const obj = {};
    for (const child of node.children) {
      if (child.tagName === "LEGEND") continue;
      const key = keyOf(child);
      if (key !== undefined) obj[key] = collectNode(child);
    }
    return obj;
  }
  return undefined;
}

function keyOf(node) {
  const pointer = node.dataset?.pointer ?? node.querySelector?.("[data-pointer]")?.dataset?.pointer;
  if (!pointer) return undefined;
  const segs = pointer.split("/");
  return segs[segs.length - 1] || undefined;
}

export function collectForm(form) {
  const obj = {};
  for (const child of form.children) {
    const key = keyOf(child);
    if (key !== undefined) obj[key] = collectNode(child);
  }
  return obj;
}

// 422 詳情 → 對應欄位標紅（task 9.2）。對固定形狀表單同樣適用：它依賴的是
// 伺服器回的 JSON Pointer 與 DOM 上的 data-pointer，與 schema 無關。
export function showValidationErrors(form, errBox, err) {
  form.querySelectorAll(".invalid").forEach((n) => n.classList.remove("invalid"));
  form.querySelectorAll(".field-error").forEach((n) => (n.textContent = ""));
  if (!(err instanceof ApiError)) {
    errBox.textContent = String(err.message || err);
    errBox.hidden = false;
    return;
  }
  errBox.textContent = err.message;
  errBox.hidden = false;
  for (const d of err.details) {
    const target = form.querySelector(`[data-pointer="${d.pointer}"]`) ||
      form.querySelector(`[data-pointer^="${d.pointer}"]`);
    if (target) {
      const holder = target.closest(".field") || target;
      holder.classList.add("invalid");
      const slot = holder.querySelector(".field-error");
      if (slot) slot.textContent = d.message;
    }
  }
}
