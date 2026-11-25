// The ONE loader for the design layer (design/). Core code reads copy and screens only through here.
//
//   design/copy.js    -> export default { "key": "text with {placeholders}" }   (validated per entry)
//   design/routes.js  -> export default { routeId: "views/xyz.js" }             (validated per entry)
//   design/views/*.js -> export default function (root, ctx)                    (loaded lazily)
//   design/tokens.css, design/theme.css are plain <link> tags in index.html (CSS cannot break behaviour).
//
// Everything is validated against lib/design-schema.js and falls back, per entry, to the pristine
// built-in copy in design-defaults/. A broken design logs a console warning and NEVER blocks the app.
import { validateCopy, validateRoutes } from "./design-schema.js";

const DESIGN = new URL("../design/", import.meta.url).href;
const DEFAULTS = new URL("../design-defaults/", import.meta.url).href;

let defaultCopy = {};
let copy = {};
let designRoutes = {};
let defaultRoutes = {};
const missingWarned = new Set();

export const warn = (msg, err) => console.warn(`[design] ${msg}`, ...(err ? [err] : []));

/** Loads copy and the route registry. Never throws. */
export async function loadDesign() {
  try {
    defaultCopy = (await import(`${DEFAULTS}copy.js`)).default || {};
    defaultRoutes = (await import(`${DEFAULTS}routes.js`)).default || {};
  } catch (e) {
    warn("built-in design defaults failed to load", e);
  }
  try {
    const mod = await import(`${DESIGN}copy.js`);
    const r = validateCopy(mod.default, defaultCopy);
    copy = r.copy;
    r.warnings.forEach((w) => warn(w));
    (r.retired || []).forEach((w) => warn(w));
  } catch (e) { warn("design/copy.js could not be loaded; using the built-in copy", e); copy = {}; }
  try {
    const mod = await import(`${DESIGN}routes.js`);
    const r = validateRoutes(mod.default);
    designRoutes = r.routes;
    r.warnings.forEach((w) => warn(w));
  } catch (e) { warn("design/routes.js could not be loaded; using the built-in views", e); designRoutes = {}; }
}

/** Copy lookup: design copy, then the built-in copy, then the key itself. {name} placeholders are filled from params. */
export function t(key, params) {
  let s = copy[key];
  if (s === undefined) s = defaultCopy[key];
  if (s === undefined) {
    if (!missingWarned.has(key)) { missingWarned.add(key); warn(`missing copy key "${key}"`); }
    return key;
  }
  if (!params) return s;
  return s.replace(/\{([A-Za-z][A-Za-z0-9]*)\}/g, (m, name) => (name in params ? String(params[name]) : m));
}

/** Resolves a route's view function from the design layer, or null (caller then uses the default). */
export async function designView(id) {
  const path = designRoutes[id];
  if (!path) return null;
  try {
    const mod = await import(`${DESIGN}${path}`);
    if (typeof mod.default !== "function") throw new Error("the module has no default export function");
    return mod.default;
  } catch (e) { warn(`design view "${id}" (${path}) could not be loaded; using the built-in view`, e); return null; }
}

/** The pristine built-in view for a route (kept in the core, covered by the hash manifest). */
export async function defaultView(id) {
  const path = defaultRoutes[id];
  if (!path) throw new Error(`no built-in view for route "${id}"`);
  return (await import(`${DEFAULTS}${path}`)).default;
}

/** Everything the design layer exposes, for diagnostics and e2e. */
export const designInfo = () => ({ routes: { ...designRoutes }, copyKeys: Object.keys(copy).length });
