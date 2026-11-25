// Schema-driven form builder (CORE, locked): builds DOM from a JSON schema, collects the values back and maps
// 422 pointers onto fields. Ported 1:1 from the pre-split lib/schemaform.js (only CMS pages / shop content use it;
// x-editor-order exists because jsonb does not keep key order).
// Exposed to views as ctx.ui.schemaForm = {build, collect, showValidationErrors}. Image upload is NOT done here: the
// view passes the core-bound upload action (ctx.actions.cmsUploadImage) in `opts.upload`; without it (or when media
// is not configured) no upload button is rendered, exactly like the old "media not configured" behaviour.
import { h } from "./kit.js";
import { t } from "../lib/design.js";
import { ApiError } from "../lib/api.js";
import { mediaEnabled } from "../lib/upload.js";

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

// x-editor-order ordering of a properties object (same rule as api/internal/cms/fieldorder.go FieldOrder).
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

/** Rebuilds `container` from schema + value. opts.upload: async (File) => url (the core-bound upload action). */
export function build(schema, value, container, opts = {}) {
  container.textContent = "";
  const root = schema;
  const props = resolveRef(schema, root)?.properties || {};
  for (const key of orderedKeys(props, root)) {
    const ps = resolveRef(props[key], root);
    container.append(buildField(key, ps, value?.[key], root, `/${key}`, opts));
  }
}

function buildField(key, ps, value, root, pointer, opts) {
  const type = ps.type;
  const editor = ps["x-editor"];
  const label = ps.title || key;

  if (type === "object" || (ps.properties && !type)) {
    const fieldset = h("fieldset", { class: "obj", "data-pointer": pointer }, h("legend", {}, label));
    const props = ps.properties || {};
    for (const k of orderedKeys(props, root)) {
      const child = resolveRef(props[k], root);
      fieldset.append(buildField(k, child, value?.[k], root, `${pointer}/${k}`, opts));
    }
    return fieldset;
  }

  if (type === "array") {
    const branches = sectionBranches(ps.items, root);
    if (branches) return buildSectionsField(label, ps, value, root, pointer, branches, opts);
    return buildListField(label, ps, value, root, pointer, opts);
  }

  const wrap = h("label", { class: "field", "data-pointer": pointer });
  wrap.append(h("span", { class: "field-label" }, label));
  let input;
  if (type === "boolean") {
    input = h("input", { type: "checkbox" });
    input.checked = Boolean(value ?? ps.default);
  } else if (type === "number" || type === "integer") {
    input = h("input", { type: "number", value: value ?? ps.default ?? "" });
  } else if (editor === "color") {
    input = h("input", { type: "color", value: value ?? ps.default ?? "#000000" });
  } else if (editor === "richtext") {
    input = h("textarea", { rows: 6 });
    input.value = value ?? ps.default ?? "";
  } else if (editor === "image") {
    input = h("input", { type: "url", placeholder: "https://…", value: value ?? ps.default ?? "" });
  } else {
    input = h("input", { type: "text", value: value ?? ps.default ?? "" });
  }
  input.dataset.pointer = pointer;
  input.dataset.jsonType = type || "string";
  wrap.append(input, h("span", { class: "field-error" }));
  if (editor === "image" && mediaEnabled() && typeof opts.upload === "function") {
    const status = h("span", { class: "hint" });
    const file = h("input", { type: "file", accept: "image/jpeg,image/png,image/webp,image/heic,image/heif,.heic,.heif" });
    file.addEventListener("change", async () => {
      const f = file.files?.[0];
      file.value = "";
      if (!f) return;
      status.textContent = t("cms.form.uploading");
      try {
        input.value = await opts.upload(f);
        status.textContent = t("cms.form.uploaded");
      } catch (e) {
        status.textContent = e.message;
      }
    });
    wrap.append(h("label", { class: "file-button" }, t("cms.form.uploadImage"), file), status);
  }
  if (editor === "richtext") {
    wrap.append(h("span", { class: "hint" }, t("cms.form.richtextHint")));
  }
  return wrap;
}

function buildListField(label, ps, value, root, pointer, opts) {
  const items = resolveRef(ps.items || {}, root);
  const box = h("fieldset", { class: "list", "data-pointer": pointer, "data-kind": "list" }, h("legend", {}, label));
  const rows = h("div", { class: "list-rows" });
  box.append(rows);

  const addRow = (itemValue) => {
    const row = h("div", { class: "list-row" });
    const body = h("div", { class: "list-row-body" });
    if (items.properties) {
      for (const k of orderedKeys(items.properties, root)) {
        const child = resolveRef(items.properties[k], root);
        body.append(buildField(k, child, itemValue?.[k], root, "", opts));
      }
    } else {
      body.append(buildField(t("cms.form.value"), items, itemValue, root, "", opts));
    }
    row.append(body, h("button", { type: "button", class: "danger", onclick: () => row.remove() }, t("cms.form.remove")));
    rows.append(row);
  };
  (Array.isArray(value) ? value : []).forEach(addRow);
  box.append(h("button", { type: "button", onclick: () => addRow(undefined) }, t("cms.form.addRow")));
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

function buildSectionsField(label, ps, value, root, pointer, branches, opts) {
  const box = h("fieldset", { class: "sections", "data-pointer": pointer, "data-kind": "sections" }, h("legend", {}, label));
  const rows = h("div", { class: "section-rows" });
  box.append(rows);

  const addSection = (item) => {
    const branch = branches.find((b) => b.type === item?.type);
    if (!branch) return;
    const row = h("div", { class: "section-row" });
    const head = h("div", { class: "section-head" },
      h("strong", {}, branch.type),
      h("span", { class: "section-actions" },
        h("button", { type: "button", onclick: () => row.previousElementSibling?.before(row) }, "↑"),
        h("button", { type: "button", onclick: () => row.nextElementSibling?.after(row) }, "↓"),
        h("button", { type: "button", class: "danger", onclick: () => row.remove() }, t("cms.form.remove"))));
    const body = h("div", { class: "section-body" });
    const branchProps = branch.schema.properties || {};
    for (const k of orderedKeys(branchProps, root)) {
      if (k === "type") continue;
      const child = resolveRef(branchProps[k], root);
      body.append(buildField(k, child, item?.[k], root, "", opts));
    }
    row._type = branch.type;
    row._schema = branch.schema;
    row.append(head, body);
    rows.append(row);
  };

  (Array.isArray(value) ? value : []).forEach(addSection);

  const picker = h("div", { class: "section-picker" });
  for (const b of branches) {
    picker.append(h("button", { type: "button", onclick: () => addSection({ type: b.type }) }, `＋ ${b.type}`));
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

export function collect(form) {
  const obj = {};
  for (const child of form.children) {
    const key = keyOf(child);
    if (key !== undefined) obj[key] = collectNode(child);
  }
  return obj;
}

/** 422 details (JSON Pointers) -> mark the matching fields; anything else just shows its message in errBox. */
export function showValidationErrors(form, errBox, err) {
  form.querySelectorAll(".invalid").forEach((n) => n.classList.remove("invalid"));
  form.querySelectorAll(".field-error").forEach((n) => (n.textContent = ""));
  const details = err instanceof ApiError ? err.details
    : (err && err.isActionError && err.fields ? Object.entries(err.fields).map(([pointer, message]) => ({ pointer, message })) : null);
  if (!details) {
    errBox.textContent = String((err && err.message) || err);
    errBox.hidden = false;
    return;
  }
  errBox.textContent = err.message;
  errBox.hidden = false;
  for (const d of details) {
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
