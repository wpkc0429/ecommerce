// Pure logic for the schema-driven content form: walking the platform's restricted JSON Schema (format 2, see
// ecommerce docs/content-schema.md), defaults, and the VALIDATION the core enforces before anything is sent.
// No DOM, no network: testable under node (scripts/test-content.mjs) and shared by ui/content-form.js (which
// builds the controls) and core/content-actions.js (which validates again before every save).
//
// The server stays the authority (422 with JSON Pointers); this module makes the same checks early, in the core,
// so a design layer can neither skip a required field nor submit a value the form would have refused.
import { t } from "./design.js";

export const EDITOR_KINDS = ["text", "textarea", "richtext", "image", "link", "color", "select", "list", "sections"];
export const MAX_URL = 2048;

// ── Schema walking ─────────────────────────────────────────────────────
/** Follows a local `#/$defs/<name>` $ref (the only kind the platform allows). `root` is the type's schema node. */
export function deref(node, root) {
  let n = node;
  for (let i = 0; i < 8 && n && typeof n.$ref === "string"; i++) {
    const m = /^#\/\$defs\/([A-Za-z0-9_-]+)$/.exec(n.$ref);
    const next = m && root && root.$defs ? root.$defs[m[1]] : null;
    if (!next) return n;
    n = { ...next, ...Object.fromEntries(Object.entries(n).filter(([k]) => k !== "$ref")) };
  }
  return n;
}

/** The field kind: object | array-list | array-sections | boolean | integer | number | an x-editor string kind. */
export function kindOf(node, root) {
  const n = deref(node, root) || {};
  switch (n.type) {
    case "object": return "object";
    case "array": return n["x-editor"] === "sections" || (n.items && deref(n.items, root)?.oneOf) ? "sections" : "list";
    case "boolean": return "boolean";
    case "integer": return "integer";
    case "number": return "number";
    default: {
      const e = n["x-editor"];
      if (e === "select" || (Array.isArray(n.enum) && !e)) return "select";
      return ["textarea", "richtext", "image", "link", "color"].includes(e) ? e : "text";
    }
  }
}

/** Properties of an object node in editor order (x-editor-order, then name). */
export function orderedProps(node, root) {
  const n = deref(node, root) || {};
  return Object.entries(n.properties || {})
    .map(([key, sub]) => [key, deref(sub, root)])
    .sort((a, b) => (Number(a[1]["x-editor-order"] ?? 1e6) - Number(b[1]["x-editor-order"] ?? 1e6)) || a[0].localeCompare(b[0]));
}

export const isRequired = (node, key, root) => (deref(node, root)?.required || []).includes(key);

/** The branches of a block union: [{type, node}] (`properties.type.const` names each branch). */
export function sectionBranches(arrayNode, root) {
  const items = deref(deref(arrayNode, root)?.items, root) || {};
  return (items.oneOf || []).map((b) => {
    const n = deref(b, root);
    return { type: n?.properties?.type?.const, node: n };
  }).filter((b) => typeof b.type === "string");
}

export function defaultValue(node, root) {
  const n = deref(node, root) || {};
  if (n.default !== undefined) return JSON.parse(JSON.stringify(n.default));
  switch (n.type) {
    case "object": return Object.fromEntries(Object.entries(n.properties || {}).map(([k, s]) => [k, defaultValue(s, root)]));
    case "array": return [];
    case "boolean": return false;
    case "integer": case "number": return 0;
    default: return "";
  }
}

/** The value at a JSON Pointer ("/a/0/b") or undefined. */
export function getAt(value, pointer) {
  let cur = value;
  for (const seg of splitPointer(pointer)) {
    if (cur === null || typeof cur !== "object") return undefined;
    cur = cur[seg];
  }
  return cur;
}
export function splitPointer(p) { return String(p || "").split("/").slice(1).map((s) => s.replace(/~1/g, "/").replace(/~0/g, "~")); }
export const joinPointer = (base, seg) => `${base}/${String(seg).replace(/~/g, "~0").replace(/\//g, "~1")}`;

/** The schema node a pointer points at (`root` = the type's schema), or null. Array indexes resolve to `items`. */
export function nodeAt(root, pointer) {
  let n = deref(root, root);
  for (const seg of splitPointer(pointer)) {
    if (!n) return null;
    if (n.type === "array") {
      const items = deref(n.items, root);
      if (items && items.oneOf) return null; // block unions are resolved per block by the form
      n = items;
    } else n = deref(n.properties?.[seg], root);
  }
  return n || null;
}

/** Leaf-or-container pointers that MUST be rendered: every `required` property, recursively through required objects. */
export function requiredPointers(root) {
  const out = [];
  (function walk(node, ptr) {
    const n = deref(node, root);
    if (!n || n.type !== "object") return;
    for (const key of n.required || []) {
      const sub = deref(n.properties?.[key], root);
      if (!sub) continue;
      const p = joinPointer(ptr, key);
      out.push(p);
      if (sub.type === "object") walk(sub, p);
    }
  })(root, "");
  return out;
}

// ── Value checks the platform repeats on write ────────────────────────
const CONTROL = /[\u0000-\u001f\u007f]/;
/** link: https:, http:, mailto:, tel:, or a site-relative path starting with / or # (not //host). "" is allowed (empty link). */
export function linkOK(v) {
  const s = String(v);
  if (s === "") return true;
  if (s.length > MAX_URL || CONTROL.test(s) || /\s/.test(s)) return false;
  if (s.startsWith("//")) return false;
  if (s.startsWith("/") || s.startsWith("#")) return true;
  return /^(https?:\/\/[^/\s]|mailto:[^\s]|tel:[^\s])/i.test(s);
}

/** image: "" or an https URL under the configured image domain (prefix ends with "/"). Unconfigured: any https URL. */
export function imageOK(v, prefix = "") {
  const s = String(v);
  if (s === "") return true;
  if (s.length > MAX_URL || CONTROL.test(s) || /\s/.test(s)) return false;
  if (prefix) return s.startsWith(prefix.endsWith("/") ? prefix : `${prefix}/`);
  return /^https:\/\/[^/\s]/i.test(s);
}

export const colorOK = (v) => v === "" || /^#[0-9a-fA-F]{6}$/.test(String(v));
const len = (s) => [...String(s)].length; // JSON Schema counts code points

function reOK(pattern, s) {
  try { return new RegExp(pattern, "u").test(s); } catch { return true; } // an RE2 pattern JS cannot parse: the server decides
}

/**
 * Validates `value` against `node`; returns {pointer: message} (empty when fine). `env`: {root, imagePrefix}.
 * Messages come from copy (cf.err.*). Unknown keys in an object are ignored here (the form never sends them).
 */
export function validate(node, value, env = {}, ptr = "") {
  const root = env.root || node;
  const n = deref(node, root) || {};
  const errs = {};
  const put = (msg, p = ptr) => { if (!(p in errs)) errs[p] = msg; };
  const kind = kindOf(n, root);

  if (value === undefined || value === null) { put(t("cf.err.required")); return errs; }

  if (kind === "object") {
    if (typeof value !== "object" || Array.isArray(value)) { put(t("cf.err.invalid")); return errs; }
    for (const [key, sub] of orderedProps(n, root)) {
      const p = joinPointer(ptr, key);
      if (!(key in value)) { if (isRequired(n, key, root)) put(t("cf.err.required"), p); continue; }
      Object.assign(errs, validate(sub, value[key], env, p));
    }
    return errs;
  }
  if (kind === "list" || kind === "sections") {
    if (!Array.isArray(value)) { put(t("cf.err.invalid")); return errs; }
    if (n.minItems !== undefined && value.length < n.minItems) put(t("cf.err.minItems", { n: n.minItems }));
    if (n.maxItems !== undefined && value.length > n.maxItems) put(t("cf.err.maxItems", { n: n.maxItems }));
    const items = deref(n.items, root) || {};
    value.forEach((item, i) => {
      const p = joinPointer(ptr, i);
      if (kind === "sections") {
        const br = sectionBranches(n, root).find((b) => b.type === item?.type);
        if (!br) { put(t("cf.err.blockType"), p); return; }
        Object.assign(errs, validate(br.node, item, env, p));
      } else Object.assign(errs, validate(items, item, env, p));
    });
    return errs;
  }
  if (kind === "boolean") { if (typeof value !== "boolean") put(t("cf.err.invalid")); return errs; }
  if (kind === "integer" || kind === "number") {
    if (typeof value !== "number" || !Number.isFinite(value)) { put(t("cf.err.number")); return errs; }
    if (kind === "integer" && !Number.isInteger(value)) put(t("cf.err.integer"));
    if (n.minimum !== undefined && value < n.minimum) put(t("cf.err.min", { n: n.minimum }));
    if (n.maximum !== undefined && value > n.maximum) put(t("cf.err.max", { n: n.maximum }));
    return errs;
  }
  // strings
  if (typeof value !== "string") { put(t("cf.err.invalid")); return errs; }
  if (n.const !== undefined && value !== n.const) put(t("cf.err.invalid"));
  if (Array.isArray(n.enum) && !n.enum.includes(value)) put(t("cf.err.enum"));
  if (n.minLength !== undefined && len(value) < n.minLength) put(n.minLength === 1 ? t("cf.err.required") : t("cf.err.minLength", { n: n.minLength }));
  if (n.maxLength !== undefined && len(value) > n.maxLength) put(t("cf.err.maxLength", { n: n.maxLength }));
  if (n.pattern && value !== "" && !reOK(n.pattern, value)) put(t("cf.err.pattern"));
  if (n.format === "date" && value !== "" && !/^\d{4}-\d{2}-\d{2}$/.test(value)) put(t("cf.err.date"));
  if (kind === "link" && !linkOK(value)) put(t("cf.err.link"));
  if (kind === "image" && !imageOK(value, env.imagePrefix || "")) put(t("cf.err.image"));
  if (kind === "color" && !colorOK(value)) put(t("cf.err.color"));
  return errs;
}

// ── Server error mapping ───────────────────────────────────────────────
/**
 * Maps a 422's details [{pointer, message}] to {pointer: message} relative to the content. The API points INTO the
 * content; a leading "/content" is tolerated when the field of that name does not exist. Unmappable entries
 * (pointer "" or unknown) are kept under "" so the screen can show them as a form-level message.
 */
export function mapServerDetails(details, schemaRoot) {
  const out = {};
  for (const d of Array.isArray(details) ? details : []) {
    let ptr = String(d?.pointer ?? "");
    if (schemaRoot && ptr.startsWith("/content") && !(schemaRoot.properties && "content" in schemaRoot.properties)) {
      const stripped = ptr.slice("/content".length);
      if (stripped === "" || stripped.startsWith("/")) ptr = stripped;
    }
    const msg = t("cf.err.server", { message: String(d?.message || "") });
    out[ptr] = out[ptr] ? `${out[ptr]}；${msg}` : msg;
  }
  return out;
}

/** Fills missing properties with defaults and drops undeclared keys, so the form always has exactly the schema's shape (the server hydrates the same way). */
export function hydrate(node, value, root = node) {
  const n = deref(node, root) || {};
  if (n.type === "object") {
    const src = value && typeof value === "object" && !Array.isArray(value) ? value : {};
    // Keys the schema does not declare are DROPPED (every object is additionalProperties:false, so the server would reject
    // them; a draft that predates the schema, e.g. a legacy "sections" key, would otherwise never be savable).
    const out = {};
    for (const [k, sub] of Object.entries(n.properties || {})) {
      if (!(k in src)) { if (isRequired(n, k, root) || deref(sub, root)?.default !== undefined || deref(sub, root)?.type === "object") out[k] = defaultValue(sub, root); }
      else out[k] = hydrate(sub, src[k], root);
    }
    return out;
  }
  if (n.type === "array" && Array.isArray(value)) {
    const items = deref(n.items, root);
    if (items && !items.oneOf && items.type === "object") return value.map((v) => hydrate(items, v, root));
    return value;
  }
  return value === undefined ? defaultValue(n, root) : value;
}

/** Plain-text lines for a dry-run report (rendered with textContent by the dialog). */
export function reportLines(report) {
  const docs = Array.isArray(report?.documents) ? report.documents : [];
  const lines = [];
  for (const d of docs) {
    const label = d.kind === "global" ? "global" : String(d.key);
    if (d.status === "ok" && !(d.removed_paths || []).length) continue;
    const status = { needs_attention: t("cf.report.needsAttention"), orphaned: t("cf.report.orphaned"), ok: t("cf.report.ok") }[d.status] || String(d.status);
    lines.push(`${label}: ${status}`);
    for (const p of d.removed_paths || []) lines.push(`  ${t("cf.report.removed")} ${p}`);
    for (const x of d.invalid || []) lines.push(`  ${t("cf.report.invalid")} ${x.pointer || "/"} ${x.message || ""}`.trimEnd());
  }
  return lines;
}
export const hasRemovals = (report) => (report?.documents || []).some((d) => (d.removed_paths || []).length || d.status === "orphaned");
