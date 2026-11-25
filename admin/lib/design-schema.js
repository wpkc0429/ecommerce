// The contract between the locked core and the editable design layer, as PLAIN DATA plus tiny pure
// validators. Both the browser loader (lib/design.js) and the guard (scripts/check-design.mjs) import
// this file, so what the guard accepts is exactly what the loader accepts. It imports only the pure-data route metadata (lib/meta).
//
// Anything the design layer is allowed to name is listed here. Anything not listed is rejected.
import { ROUTE_DEFS, WARNED_IDS } from "./meta/index.js";

/** Route ids the core defines (core/routes.js owns patterns, permissions and data needs). */
export const ROUTE_IDS = ["shell", "notFound", "noPermission", "bootError", ...ROUTE_DEFS.map((r) => r.id)];

/** Icon names the core can draw (ui/icons.js keeps this list in sync; the guard test checks it). */
export const ICON_NAMES = ["logout", "back", "plus", "close"];

export const LIMITS = {
  copyValueMax: 600,        // characters per copy string
  copyKeyMax: 80,
  routePathMax: 120,
  assetMaxBytes: 512 * 1024,
  cssMaxBytes: 256 * 1024,
  jsFileMaxBytes: 128 * 1024,
};

export const ASSET_EXTENSIONS = ["svg", "png", "jpg", "jpeg", "webp", "gif", "ico", "avif", "woff2"];
export const ASSET_TEXT_EXTENSIONS = ["txt", "md"];

export const COPY_KEY_RE = /^[a-z][A-Za-z0-9]*(\.[A-Za-z0-9_-]+)*$/;
const ROUTE_PATH_RE = /^views\/[a-z0-9][a-z0-9_-]*(\/[a-z0-9][a-z0-9_-]*)*\.js$/;
const PLACEHOLDER_RE = /\{([A-Za-z][A-Za-z0-9]*)\}/g;

export const placeholdersOf = (s) => [...String(s).matchAll(PLACEHOLDER_RE)].map((m) => m[1]);

/**
 * Confirmation ids (the `<id>` in confirm.<id>.*) of IRREVERSIBLE actions. Their warning text is FIXED and core-owned
 * (core/warnings.js): it has no copy key, so copy.js can only supply the title, the lead-in `text` and the button labels.
 * Single source of truth: core/warnings.js imports this list.
 */
export const WARNED_CONFIRM_IDS = WARNED_IDS;

const CONFIRM_KEY_RE = /^confirm\.([A-Za-z0-9]+)\.(.+)$/;
const FIXED_WARNING_MSG = "the warning of an irreversible confirmation is FIXED by the core (core/warnings.js) and cannot be defined, reworded or removed in copy; edit only title, text and the button labels";

/** A reason string when a copy key tries to define / replace a fixed confirmation warning (a VIOLATION), else null. */
export function fixedWarningViolation(key) {
  const m = CONFIRM_KEY_RE.exec(key);
  if (!m || !/warn/i.test(m[2])) return null;
  return `copy: "${key}" ignored: ${FIXED_WARNING_MSG}`;
}

/**
 * A note when a copy key is the RETIRED `confirm.<id>.message[Sub]` of an irreversible confirm (v0.1.0 name). It is ignored (the
 * default lead-in and the core's fixed warning are shown) but it is only a NOTE, not a violation, so a merchant repo made from
 * v0.1.0 can still sync (the design-layer action is to rename it), else null.
 */
export function retiredCopyKey(key) {
  const m = CONFIRM_KEY_RE.exec(key);
  if (!m || !WARNED_CONFIRM_IDS.includes(m[1]) || !/^message/.test(m[2])) return null;
  return `copy: "${key}" is retired and ignored: it was split into ${"confirm." + m[1] + "." + m[2].replace(/^message/, "text")} (an editable lead-in) plus a FIXED core warning; rename the key and drop the warning part (the core shows it anyway)`;
}

/** Problems in the built-in defaults themselves (a defaults file must define neither a warning key nor a retired key). */
export const defaultsProblems = (defaults) => Object.keys(defaults || {}).map((k) => fixedWarningViolation(k) || retiredCopyKey(k)).filter(Boolean);

/**
 * Validates a copy object against the built-in defaults. Returns {copy, warnings}: `copy` holds only the
 * entries that are acceptable (known key, string, within the length cap, no unknown placeholders); every
 * other entry is dropped, so that key falls back to the default string.
 */
export function validateCopy(copy, defaults) {
  const warnings = [];
  const retired = []; // notes, not violations: retired keys that are ignored
  const out = {};
  if (!copy || typeof copy !== "object" || Array.isArray(copy)) {
    return { copy: out, warnings: ["copy: must be a plain object of key -> string; using the built-in copy"], retired };
  }
  for (const [k, v] of Object.entries(copy)) {
    if (k.length > LIMITS.copyKeyMax || !COPY_KEY_RE.test(k)) { warnings.push(`copy: bad key "${k.slice(0, 40)}" ignored`); continue; }
    const fixed = fixedWarningViolation(k);
    if (fixed) { warnings.push(fixed); continue; }
    const old = retiredCopyKey(k);
    if (old) { retired.push(old); continue; }
    if (!(k in defaults)) { warnings.push(`copy: unknown key "${k}" ignored`); continue; }
    if (typeof v !== "string") { warnings.push(`copy: "${k}" must be a string; default used`); continue; }
    if (v.length > LIMITS.copyValueMax) { warnings.push(`copy: "${k}" is longer than ${LIMITS.copyValueMax} characters; default used`); continue; }
    const allowed = new Set(placeholdersOf(defaults[k]));
    const extra = placeholdersOf(v).filter((p) => !allowed.has(p));
    if (extra.length) { warnings.push(`copy: "${k}" uses unknown placeholder(s) {${extra.join("}, {")}}; default used`); continue; }
    out[k] = v;
  }
  return { copy: out, warnings, retired };
}

/** Validates the route registry (route id -> module path under design/). Same return shape. */
export function validateRoutes(routes) {
  const warnings = [];
  const out = {};
  if (!routes || typeof routes !== "object" || Array.isArray(routes)) {
    return { routes: out, warnings: ["routes: must be a plain object of route id -> module path; using the built-in views"] };
  }
  for (const [id, path] of Object.entries(routes)) {
    if (!ROUTE_IDS.includes(id)) { warnings.push(`routes: unknown route id "${id}" ignored`); continue; }
    if (typeof path !== "string" || path.length > LIMITS.routePathMax || !ROUTE_PATH_RE.test(path)) {
      warnings.push(`routes: "${id}" must be a path like "views/${id}.js"; built-in view used`);
      continue;
    }
    out[id] = path;
  }
  return { routes: out, warnings };
}

export const ROUTE_PATH_PATTERN = ROUTE_PATH_RE;
