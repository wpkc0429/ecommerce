// Ecommerce Admin — 薄殼 SPA (design D1, tasks 9.1–9.3).
// 無建置步驟的 ES-module 應用：登入/token 管理（自動 refresh 輪替）、
// JSON Schema 驅動的表單（依 x-editor 渲染控件）、422 錯誤定位、
// 頁面發佈/下架/預覽、商家全域內容編輯。

const API_BASE = localStorage.getItem("apiBase") || "http://localhost:8080";
const WEB_BASE = localStorage.getItem("webBase") || "http://localhost:3000";

// ── token 管理（task 9.1）───────────────────────────────────────────
const tokens = {
  get access() { return localStorage.getItem("accessToken") || ""; },
  get refresh() { return localStorage.getItem("refreshToken") || ""; },
  set(pair) {
    localStorage.setItem("accessToken", pair.access_token);
    localStorage.setItem("refreshToken", pair.refresh_token);
  },
  clear() {
    localStorage.removeItem("accessToken");
    localStorage.removeItem("refreshToken");
  },
};

let refreshing = null;

async function refreshTokens() {
  // 合併併發 refresh：refresh token 每次使用即輪替，重複使用會撤銷整鏈。
  if (!refreshing) {
    refreshing = (async () => {
      const res = await fetch(`${API_BASE}/api/v1/admin/auth/refresh`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ refresh_token: tokens.refresh }),
      });
      if (!res.ok) throw new Error("refresh failed");
      tokens.set(await res.json());
    })().finally(() => { refreshing = null; });
  }
  return refreshing;
}

// api(): 帶 access token 呼叫；401 時自動 refresh 一次再重試。
async function api(method, path, body) {
  const doFetch = () =>
    fetch(`${API_BASE}/api/v1${path}`, {
      method,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${tokens.access}`,
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  let res = await doFetch();
  if (res.status === 401 && tokens.refresh) {
    try {
      await refreshTokens();
      res = await doFetch();
    } catch {
      logout();
      throw new ApiError(401, { error: { message: "session expired" } });
    }
  }
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) throw new ApiError(res.status, data);
  return data;
}

class ApiError extends Error {
  constructor(status, body) {
    super(body?.error?.message || `HTTP ${status}`);
    this.status = status;
    this.body = body;
  }
  get details() { return this.body?.error?.details || []; }
}

// ── 全域狀態 ───────────────────────────────────────────────────────
const state = {
  me: null,
  shopID: null,
  pages: [],
  editingPage: null, // { id, schema, meta }
  contentSchema: null,
};

const $ = (sel) => document.querySelector(sel);

function show(viewID) {
  document.querySelectorAll(".view").forEach((v) => (v.hidden = v.id !== viewID));
}

function switchTab(tab) {
  document.querySelectorAll(".tab").forEach((b) => b.classList.toggle("active", b.dataset.tab === tab));
  $("#tab-pages").hidden = tab !== "pages";
  $("#tab-editor").hidden = tab !== "editor";
  $("#tab-content").hidden = tab !== "content";
  if (tab === "pages") loadPages();
  if (tab === "content") loadShopContent();
}

function logout() {
  if (tokens.refresh) {
    fetch(`${API_BASE}/api/v1/admin/auth/logout`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refresh_token: tokens.refresh }),
    }).catch(() => {});
  }
  tokens.clear();
  show("view-login");
}

// ── Schema 驅動表單（task 9.2）──────────────────────────────────────
// buildForm(schema, value, container, pointer)：依 JSON Schema 產生控件，
// data-pointer 屬性對應 422 錯誤的 JSON Pointer 定位。
function resolveRef(node, root) {
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
function orderedKeys(props, root) {
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

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else if (v !== undefined && v !== null) node.setAttribute(k, v);
  }
  for (const c of children) {
    node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return node;
}

function buildForm(schema, value, container, root, pointer = "") {
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

function collectForm(form) {
  const obj = {};
  for (const child of form.children) {
    const key = keyOf(child);
    if (key !== undefined) obj[key] = collectNode(child);
  }
  return obj;
}

// 422 詳情 → 對應欄位標紅（task 9.2）。
function showValidationErrors(form, errBox, err) {
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

// ── 登入與初始化 ───────────────────────────────────────────────────
async function boot() {
  if (tokens.access) {
    try {
      state.me = await api("GET", "/admin/me");
      enterMain();
      return;
    } catch { tokens.clear(); }
  }
  show("view-login");
}

$("#login-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  $("#login-error").hidden = true;
  try {
    const res = await fetch(`${API_BASE}/api/v1/admin/auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: $("#login-email").value, password: $("#login-password").value }),
    });
    if (!res.ok) throw new Error("bad credentials");
    tokens.set(await res.json());
    state.me = await api("GET", "/admin/me");
    enterMain();
  } catch {
    $("#login-error").hidden = false;
  }
});

$("#logout-btn").addEventListener("click", logout);

function enterMain() {
  show("view-main");
  $("#whoami").textContent = state.me.email;
  const sel = $("#shop-select");
  sel.textContent = "";
  const sids = state.me.sids || [];
  if (sids.length > 0) {
    for (const id of sids) sel.append(el("option", { value: id }, `Shop #${id}`));
    state.shopID = sids[0];
    sel.hidden = false;
    $("#shop-input").hidden = true;
  } else {
    // 平台管理員無 sids 提示 → 手動輸入目標 shop。
    sel.hidden = true;
    const input = $("#shop-input");
    input.hidden = false;
    state.shopID = Number(localStorage.getItem("shopID") || 1);
    input.value = state.shopID;
  }
  switchTab("pages");
}

$("#shop-select").addEventListener("change", (e) => {
  state.shopID = Number(e.target.value);
  switchTab("pages");
});
$("#shop-input").addEventListener("change", (e) => {
  state.shopID = Number(e.target.value);
  localStorage.setItem("shopID", String(state.shopID));
  switchTab("pages");
});
document.querySelectorAll(".tab").forEach((b) =>
  b.addEventListener("click", () => switchTab(b.dataset.tab)));

// ── 頁面清單與動作（task 9.2/9.3）──────────────────────────────────
async function loadPages() {
  const data = await api("GET", `/admin/shops/${state.shopID}/pages`);
  state.pages = data.pages;
  const tbody = $("#pages-tbody");
  tbody.textContent = "";
  for (const p of data.pages) {
    const status = p.status === 1
      ? el("span", { class: "badge ok" }, "已發佈")
      : el("span", { class: "badge" }, "草稿");
    const row = el("tr", {},
      el("td", {}, p.id),
      el("td", {}, p.title || "—"),
      el("td", {}, p.slug),
      el("td", {}, p.incompatible
        ? el("span", { class: "badge warn", title: "目前主題不支援此頁型" }, `${p.type_key} ⚠ 不相容`)
        : p.type_key),
      el("td", {}, status),
      el("td", {},
        el("button", { onclick: () => openEditor(p.id) }, "編輯"),
        el("button", { onclick: () => previewPage(p.id) }, "預覽"),
        p.status === 1
          ? el("button", { onclick: () => pageAction(p.id, "unpublish") }, "下架")
          : el("button", { class: "publish", onclick: () => pageAction(p.id, "publish") }, "發佈"),
        p.slug === "home" ? "" :
          el("button", { class: "danger", onclick: () => deletePage(p.id) }, "刪除")));
    tbody.append(row);
  }
}

async function pageAction(id, action) {
  try {
    await api("POST", `/admin/shops/${state.shopID}/pages/${id}/${action}`);
    await loadPages();
  } catch (err) { alert(err.message); }
}

async function deletePage(id) {
  if (!confirm("確定刪除此頁面？")) return;
  try {
    await api("DELETE", `/admin/shops/${state.shopID}/pages/${id}`);
    await loadPages();
  } catch (err) { alert(err.message); }
}

async function previewPage(id) {
  try {
    const data = await api("GET", `/admin/shops/${state.shopID}/pages/${id}/preview-token`);
    window.open(`${WEB_BASE}/preview?token=${encodeURIComponent(data.preview_token)}`, "_blank");
  } catch (err) { alert(err.message); }
}

// 新增頁面 dialog。
$("#new-page-btn").addEventListener("click", async () => {
  const types = await api("GET", `/admin/shops/${state.shopID}/page-types`);
  const sel = $("#new-page-type");
  sel.textContent = "";
  for (const t of types.page_types) sel.append(el("option", { value: t.type_key }, t.type_key));
  $("#new-page-error").hidden = true;
  $("#new-page-dialog").showModal();
});
$("#new-page-cancel").addEventListener("click", () => $("#new-page-dialog").close());
$("#new-page-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  try {
    const created = await api("POST", `/admin/shops/${state.shopID}/pages`, {
      type_key: $("#new-page-type").value,
      title: $("#new-page-title").value,
      slug: $("#new-page-slug").value,
    });
    $("#new-page-dialog").close();
    await loadPages();
    openEditor(created.id);
  } catch (err) {
    const box = $("#new-page-error");
    box.textContent = err.message + (err.details?.length ? `（${err.details.map((d) => d.pointer).join(", ")}）` : "");
    box.hidden = false;
  }
});

// ── 頁面編輯器 ─────────────────────────────────────────────────────
async function openEditor(pageID) {
  const p = await api("GET", `/admin/shops/${state.shopID}/pages/${pageID}`);
  state.editingPage = p;
  switchTab("editor");
  $("#editor-title").textContent = `編輯：${p.title || p.slug}`;
  $("#editor-status").textContent =
    (p.status === 1 ? "已發佈" : "草稿") + (p.incompatible ? " ｜ ⚠ 目前主題不支援此頁型" : "");
  $("#editor-error").hidden = true;
  const meta = p.meta || {};
  $("#seo-title").value = meta.seo_title || "";
  $("#seo-keywords").value = meta.seo_keywords || "";
  $("#seo-description").value = meta.seo_description || "";
  const form = $("#editor-form");
  if (p.page_schema) {
    buildForm(p.page_schema, p.content_json || {}, form, p.page_schema);
  } else {
    form.textContent = "此頁型不受目前主題支援，無法編輯內容。";
  }
}

$("#editor-back").addEventListener("click", () => switchTab("pages"));
$("#editor-save").addEventListener("click", async () => {
  const p = state.editingPage;
  const form = $("#editor-form");
  const errBox = $("#editor-error");
  errBox.hidden = true;
  try {
    await api("PUT", `/admin/shops/${state.shopID}/pages/${p.id}`, {
      content: collectForm(form),
      meta: {
        seo_title: $("#seo-title").value,
        seo_keywords: $("#seo-keywords").value,
        seo_description: $("#seo-description").value,
      },
    });
    $("#editor-status").textContent = "草稿已儲存 ✓（發佈後才會反映到前台）";
  } catch (err) {
    showValidationErrors(form, errBox, err);
  }
});
$("#editor-publish").addEventListener("click", async () => {
  const p = state.editingPage;
  try {
    await api("POST", `/admin/shops/${state.shopID}/pages/${p.id}/publish`);
    $("#editor-status").textContent = "已發佈 ✓";
  } catch (err) {
    showValidationErrors($("#editor-form"), $("#editor-error"), err);
  }
});
$("#editor-unpublish").addEventListener("click", async () => {
  const p = state.editingPage;
  try {
    await api("POST", `/admin/shops/${state.shopID}/pages/${p.id}/unpublish`);
    $("#editor-status").textContent = "已下架";
  } catch (err) { alert(err.message); }
});
$("#editor-preview").addEventListener("click", () => previewPage(state.editingPage.id));

// ── 商家全域內容（task 9.3）────────────────────────────────────────
async function loadShopContent() {
  const data = await api("GET", `/admin/shops/${state.shopID}/content`);
  state.contentSchema = data.config_schema;
  const form = $("#content-form");
  if (data.config_schema) {
    buildForm(data.config_schema, data.content || {}, form, data.config_schema);
  } else {
    form.textContent = "商家尚未套用主題。";
  }
}

$("#content-save").addEventListener("click", async () => {
  const form = $("#content-form");
  const errBox = $("#content-error");
  errBox.hidden = true;
  try {
    await api("PUT", `/admin/shops/${state.shopID}/content`, { content: collectForm(form) });
    alert("已儲存，前台立即生效。");
  } catch (err) {
    showValidationErrors(form, errBox, err);
  }
});

boot();
