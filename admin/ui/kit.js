// UI kit: the safe DOM builder and the shared components (buttons, fields, dialogs, toasts,
// responsive lists, empty/loading/error states). Presentation primitives only: no API paths here.
//
// h() is the ONLY way design code builds DOM, so it refuses what could run code or leak data:
// raw HTML (innerHTML/outerHTML/srcdoc), string event handlers, script-like tags, form actions and
// URLs that are not same-origin / relative / an allowed image origin. Refusals are dropped with a warning.
import { t } from "../lib/design.js";

const PROPS = new Set(["value", "checked", "disabled", "hidden", "selected", "indeterminate", "textContent"]);
const BLOCKED_TAGS = new Set(["script", "iframe", "frame", "frameset", "object", "embed", "base", "meta", "link", "style", "applet", "portal", "noscript"]);
const BLOCKED_ATTRS = new Set(["innerhtml", "outerhtml", "srcdoc", "action", "formaction", "ping", "http-equiv", "is", "xlink:href"]);
const URL_ATTRS = new Set(["src", "href", "poster", "srcset", "data", "cite"]);
const allowedOrigins = new Set();

/** The core calls this with the media public base and branding logo hosts so <img> may point at them. */
export function allowImageOrigin(url) {
  try { allowedOrigins.add(new URL(url).origin); } catch { /* not a URL */ }
}
const refuse = (what) => console.warn(`[design] ui: refused ${what}`);

function urlOK(v) {
  const s = String(v).trim();
  if (/^(#|\/(?!\/)|\.{1,2}\/|\?)/.test(s)) return true;
  if (/^(mailto:|tel:|blob:)/i.test(s)) return true;
  if (/^data:image\/(png|jpe?g|webp|gif|avif|svg\+xml)[;,]/i.test(s)) return true;
  if (/^[a-z][a-z0-9+.-]*:/i.test(s) || s.startsWith("//")) {
    try { const u = new URL(s, location.href); return /^https?:$/.test(u.protocol) && (u.origin === location.origin || allowedOrigins.has(u.origin)); } catch { return false; }
  }
  return true; // plain relative path
}

export function h(tag, attrs, ...kids) {
  const name = String(tag).toLowerCase();
  if (BLOCKED_TAGS.has(name)) { refuse(`<${name}> element`); return document.createComment(`blocked <${name}>`); }
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    const lk = k.toLowerCase();
    if (lk.startsWith("on")) {
      if (typeof v === "function") el.addEventListener(lk.slice(2), v); else refuse(`string event handler "${k}"`);
      continue;
    }
    if (BLOCKED_ATTRS.has(lk)) { refuse(`attribute "${k}"`); continue; }
    if (URL_ATTRS.has(lk)) {
      const parts = lk === "srcset" ? String(v).split(",").map((p) => p.trim().split(/\s+/)[0]) : [String(v)];
      if (!parts.every(urlOK)) { refuse(`${k}="${String(v).slice(0, 60)}"`); continue; }
    }
    if (k === "class") el.className = v;
    else if (k === "style") {
      if (v && typeof v === "object") { for (const [sk, sv] of Object.entries(v)) { if (sk.startsWith("--")) el.style.setProperty(sk, String(sv)); else el.style[sk] = sv; } }
      else refuse("string style attribute (use an object)");
    }
    else if (PROPS.has(k)) { el[k] = v; if (k === "value" && name === "input") el.setAttribute("value", String(v)); }
    else el.setAttribute(k, v === true ? "" : String(v));
  }
  append(el, kids);
  return el;
}

const SVG_NS = "http://www.w3.org/2000/svg";
const SVG_TAGS = new Set(["svg", "g", "path", "rect", "line", "circle", "ellipse", "polyline", "polygon", "text", "tspan", "title", "defs", "linearGradient", "stop"]);
/** Safe SVG element builder (allowlisted tags; no handlers, links or style strings). */
export function svg(tag, attrs = {}, text) {
  if (!SVG_TAGS.has(tag)) { refuse(`svg <${tag}>`); return document.createComment("blocked svg"); }
  const el = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    const lk = k.toLowerCase();
    if (lk.startsWith("on") || lk === "href" || lk === "xlink:href" || lk === "style") { refuse(`svg attribute "${k}"`); continue; }
    el.setAttribute(k, String(v));
  }
  if (text !== undefined) el.textContent = text;
  return el;
}

export function append(el, kids) {
  for (const k of kids.flat(Infinity)) {
    if (k === null || k === undefined || k === false) continue;
    el.append(k instanceof Node ? k : document.createTextNode(String(k)));
  }
  return el;
}
export function clear(el) { if (el) el.replaceChildren(); return el; }
export const $ = (sel, root = document) => root.querySelector(sel);
export const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

// ── Branding (data from the session -> CSS variables; the core applies it) ─────────────────────
const HEX = /^#[0-9a-fA-F]{6}$/;
export function applyBranding(branding) {
  const root = document.documentElement;
  const b = branding || {};
  // Unset = remove the override so styles.css defaults apply.
  for (const [prop, val] of [["--brand-primary", b.primary_color], ["--brand-accent", b.accent_color]]) {
    if (HEX.test(val || "")) root.style.setProperty(prop, val);
    else root.style.removeProperty(prop);
  }
  const logoOK = /^https?:\/\//.test(b.logo_url || "");
  if (logoOK) allowImageOrigin(b.logo_url);
  for (const img of $$(".brand-logo")) {
    img.hidden = !logoOK;
    if (logoOK) img.src = b.logo_url; else img.removeAttribute("src");
  }
}

// ── Dialogs (native <dialog>) ──────────────────────────────────────────
/** Promise<boolean>. The core's confirmation primitive: action functions call it themselves; screens cannot skip it.
 *  `warning` is the core's FIXED text (core/warnings.js), always rendered in its own element. `detail` is plain text computed by the core. */
export function confirmDialog({ title, message, warning = "", detail = "", confirmText = t("common.confirm"), cancelText = t("common.cancel"), danger = false }) {
  return new Promise((resolve) => {
    let answer = false;
    const dialog = h("dialog", { class: "confirm-dialog", "data-testid": "confirm-dialog" });
    const yes = h("button", { type: "button", class: danger ? "danger" : "primary", "data-testid": "confirm-yes", onclick: () => { answer = true; dialog.close(); } }, confirmText);
    const no = h("button", { type: "button", "data-testid": "confirm-no", onclick: () => dialog.close() }, cancelText);
    dialog.append(
      h("h3", {}, title),
      message ? h("p", { class: "dlg__msg" }, message) : "",
      warning ? h("p", { class: "dlg__warning", "data-testid": "confirm-warning" }, warning) : "",
      detail ? h("pre", { class: "dlg__detail", "data-testid": "confirm-detail" }, detail) : "",
      h("div", { class: "dialog-actions" }, no, yes));
    dialog.addEventListener("close", () => { dialog.remove(); resolve(answer); });
    document.body.append(dialog);
    dialog.showModal();
  });
}

// ── Presentation helpers carried over from the pre-split admin (lib/dom.js) ─────────────────────
/** Renders `list` as <tr> rows into host (with an empty-state row). */
export function rows(host, list, render, { empty = t("common.empty"), colspan = 1 } = {}) {
  clear(host);
  const items = Array.isArray(list) ? list : [];
  if (items.length === 0) {
    host.append(h("tr", {}, h("td", { colspan: String(colspan), class: "empty" }, empty)));
    return host;
  }
  for (const item of items) host.append(render(item));
  return host;
}

/** Pager text and prev/next disabled state for the id group `#{prefix}-pageinfo|prev|next`. Returns the page count. */
export function renderPager(prefix, page, total, pageSize) {
  const pages = Math.max(1, Math.ceil((total || 0) / pageSize));
  const info = $(`#${prefix}-pageinfo`);
  if (info) info.textContent = t("common.pageInfo", { page, pages, total: total || 0 });
  const prev = $(`#${prefix}-prev`);
  if (prev) prev.disabled = page <= 1;
  const next = $(`#${prefix}-next`);
  if (next) next.disabled = page >= pages;
  return pages;
}

/** Fills a <select> and selects `value`. */
export function fillSelect(select, options, value) {
  clear(select);
  for (const o of options) select.append(h("option", { value: String(o.value) }, o.label));
  select.value = value === null || value === undefined ? "" : String(value);
  return select;
}

/** Repeatable rows: renderRow(value, remove) -> {node, read}. read() closes over the row's own inputs (no DOM queries). */
export function repeater(host, renderRow) {
  const entries = [];
  function add(value) {
    let entry;
    const remove = () => { entry.node.remove(); const i = entries.indexOf(entry); if (i >= 0) entries.splice(i, 1); };
    entry = renderRow(value, remove);
    entries.push(entry);
    host.append(entry.node);
    return entry;
  }
  return {
    add,
    reset(values) { clear(host); entries.length = 0; (Array.isArray(values) ? values : []).forEach((v) => add(v)); },
    read() { return entries.map((e) => e.read()); },
    count() { return entries.length; },
  };
}

/** Copies text; resolves false when the browser refuses. */
export async function copyText(text) {
  try { await navigator.clipboard.writeText(text); return true; } catch { /* fall through */ }
  try {
    const ta = h("textarea", {}); ta.value = text; document.body.append(ta); ta.select();
    const ok = document.execCommand("copy"); ta.remove(); return ok;
  } catch { return false; }
}

// ── Error text (wording lives in design/copy.js) ───────────────────────
/** Per-field messages from a 422 (or a core validation error): {"/title": "...", ...}. */
export function fieldErrors(err) {
  if (err && err.fields && typeof err.fields === "object") return { ...err.fields };
  const out = {};
  for (const d of err?.details || []) {
    const ptr = String(d.pointer || "");
    const text = d.message || t("err.invalid");
    out[ptr] = out[ptr] ? `${out[ptr]}；${text}` : text;
  }
  return out;
}

/** `over` maps a status to a context-specific message (e.g. {409: "..."}) that replaces the generic one. */
export function errorMessage(err, over = {}) {
  if (err && err.isActionError) return err.message;
  if (!err || typeof err.status !== "number") return err && err.message ? err.message : t("err.network");
  if (over[err.status]) return over[err.status];
  return err.message || t("err.generic");
}
