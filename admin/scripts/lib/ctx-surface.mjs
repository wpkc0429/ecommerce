// Builds the REAL ctx in Node (browser globals stubbed) and returns its public surface as
// { "ctx": [names], "ctx.ui": [names], "ctx.ui.schemaForm": [names], ... }. Used by check-design.mjs and
// gen-public-api.mjs to prove lib/public-api.json is exactly what core/context.js exposes.
// Plain objects are walked recursively; functions are leaves (their properties are not part of the surface).
import { pathToFileURL } from "node:url";
import { resolve } from "node:path";

function storage() {
  const m = new Map();
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => void m.set(k, String(v)), removeItem: (k) => void m.delete(k), clear: () => m.clear(), key: (i) => [...m.keys()][i] ?? null, get length() { return m.size; } };
}
function fakeEl() {
  const el = { style: {}, dataset: {}, classList: { add() {}, remove() {}, toggle() {}, contains: () => false }, children: [], append() {}, appendChild() {}, setAttribute() {}, removeAttribute() {}, addEventListener() {}, removeEventListener() {}, querySelector: () => null, querySelectorAll: () => [], getContext: () => null };
  return el;
}

/** Installs minimal browser globals, replacing Node built-ins (e.g. its experimental localStorage) so the run is hermetic. */
export function stubBrowser() {
  const g = globalThis;
  const def = (k, v) => Object.defineProperty(g, k, { value: v, writable: true, configurable: true, enumerable: true });
  const location = { hostname: "localhost", origin: "http://localhost:3001", hash: "", href: "http://localhost:3001/", search: "", pathname: "/", reload() {} };
  const history = { replaceState() {}, pushState() {} };
  const document = { createElement: () => fakeEl(), createElementNS: () => fakeEl(), createTextNode: (t) => ({ textContent: t }), createDocumentFragment: () => fakeEl(), documentElement: fakeEl(), body: fakeEl(), head: fakeEl(), querySelector: () => null, querySelectorAll: () => [], addEventListener() {}, getElementById: () => null };
  const local = storage(), session = storage();
  def("location", location); def("localStorage", local); def("sessionStorage", session); def("history", history); def("document", document);
  def("window", { location, history, localStorage: local, sessionStorage: session, document, scrollTo() {}, addEventListener() {}, removeEventListener() {}, matchMedia: () => ({ matches: false, addEventListener() {} }) });
  def("navigator", { clipboard: { writeText: async () => {} }, userAgent: "node" });
}

export async function realSurface(root) {
  stubBrowser();
  const url = (p) => pathToFileURL(resolve(root, p)).href;
  const { makeContext } = await import(url("core/context.js"));
  const { makeAuth } = await import(url("core/auth.js"));
  const noop = () => {};
  const auth = makeAuth({ linkToken: null, hooks: { onLoggedIn: noop, onLogout: noop, goLogin: noop, afterJoin: noop } });
  const ctx = makeContext({ routeId: "home", nav: [], auth });
  const out = {};
  const walk = (obj, p) => {
    out[p] = Object.keys(obj).sort();
    if (p === "ctx.route") return; // params / state are plain data with free keys
    for (const k of out[p]) {
      const v = obj[k];
      if (v && typeof v === "object" && !Array.isArray(v)) walk(v, `${p}.${k}`);
    }
  };
  walk(ctx, "ctx");
  return out;
}
