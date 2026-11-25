#!/usr/bin/env node
/* ============================================================
   check-design.mjs: the single guard for the platform admin's design layer. No dependencies (Node 18+).
   Adapted from merchant-template/admin/scripts/check-design.mjs for THIS layout (admin/ is a monorepo subfolder,
   copy is split into design-defaults/copy/*.js, the CSP lives in nginx.conf).

   node scripts/check-design.mjs --base <git ref>   everything, incl. the scope check of `git diff --name-only <ref>`
                                                    (+ untracked files). Only admin/design/** may differ.
   node scripts/check-design.mjs --files a b c      everything, scope check on this list (paths relative to admin/)
   node scripts/check-design.mjs                    everything except the scope check
   node scripts/check-design.mjs --update           rewrite scripts/core.manifest.json (HUMAN ONLY, after a reviewed core change)

   Options: --root <dir> (check another checkout of admin/), --json (machine-readable report), --no-layers.

   Exit codes: 0 every check passed / 1 at least one violation / 2 usage or environment error (nothing was judged).

   Checks: scope, design scan (files/js/css/assets), schema (copy.js, routes.js), defaults (design-defaults scans + every
   route has a built-in view), surface (lib/public-api.json == real ctx; design code uses only listed names), copy keys
   (used == defined), fixed warnings, deploy (nginx.conf CSP + index.html), core manifest, layers, and a NOTE-only
   drift report (design/ vs design-defaults/).
   ============================================================ */
import { readFileSync, readdirSync, lstatSync, existsSync, writeFileSync } from "node:fs";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { fileURLToPath, pathToFileURL } from "node:url";
import path from "node:path";
import { scanCode, scanDataJs, scanCss, scanAsset, scopeViolations } from "./lib/design-scan.mjs";
import { realSurface } from "./lib/ctx-surface.mjs";
import { layerChecks } from "./check-layers.mjs";
import { LIMITS, validateCopy, validateRoutes, defaultsProblems, fixedWarningViolation, ROUTE_IDS, ASSET_EXTENSIONS } from "../lib/design-schema.js";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const v = (rule, msg, file, line = 0) => ({ rule, msg, file, line });

// ───────────── file walking ─────────────
export function walk(root, dir) {
  const abs = path.join(root, dir);
  if (!existsSync(abs)) return [];
  return readdirSync(abs, { withFileTypes: true }).flatMap((e) => {
    const rel = dir === "." ? e.name : path.posix.join(dir, e.name);
    return e.isDirectory() ? walk(root, rel) : [rel];
  });
}

// ───────────── locked core: the hash manifest ─────────────
// DENY BY DEFAULT: every file under admin/ is locked EXCEPT the design layer and things that are not part of the
// shipped core. So a new file anywhere (a new lib/, a new top-level script, a new config) shows up as "added".
export const MANIFEST_FILE = "scripts/core.manifest.json";
const UNLOCKED_PREFIXES = ["design/", "docs/", "e2e/", "node_modules/", ".git/"];
const TEXT_EXT = new Set([".js", ".mjs", ".css", ".html", ".json", ".md", ".txt", ".svg", ".conf"]);
const TEXT_NAMES = new Set(["Dockerfile", ".dockerignore"]);

export function lockedList(root) {
  return walk(root, ".").filter((f) => f !== MANIFEST_FILE && !UNLOCKED_PREFIXES.some((p) => f.startsWith(p))).sort();
}
function hashFile(root, rel) {
  let buf = readFileSync(path.join(root, rel));
  if (TEXT_EXT.has(path.extname(rel).toLowerCase()) || TEXT_NAMES.has(path.basename(rel))) buf = Buffer.from(buf.toString("utf8").replace(/\r\n/g, "\n"));
  return createHash("sha256").update(buf).digest("hex");
}
export function computeManifest(root) {
  const m = {};
  for (const f of lockedList(root)) m[f] = hashFile(root, f);
  return m;
}
export function manifestChecks(root, { update = false } = {}) {
  const file = path.join(root, MANIFEST_FILE);
  const now = computeManifest(root);
  if (update) { writeFileSync(file, JSON.stringify(now, null, 2) + "\n"); return { out: [], wrote: Object.keys(now).length }; }
  if (!existsSync(file)) return { out: [v("manifest/missing", `${MANIFEST_FILE} is missing (a human runs: node scripts/check-design.mjs --update)`, MANIFEST_FILE)] };
  let saved;
  try { saved = JSON.parse(readFileSync(file, "utf8")); } catch (e) { return { out: [v("manifest/missing", `${MANIFEST_FILE} is not valid JSON: ${e.message}`, MANIFEST_FILE)] }; }
  const out = [];
  for (const f of new Set([...Object.keys(saved), ...Object.keys(now)])) {
    if (saved[f] !== now[f]) out.push(v("manifest/changed", `locked core file ${saved[f] ? (now[f] ? "modified" : "removed") : "added"}: ${f}`, f));
  }
  return { out };
}

// ───────────── scope ─────────────
/**
 * Paths changed against `base`, relative to `root` (the admin dir). root may be a subfolder of the git work tree
 * (this repo keeps the admin under admin/): anything changed OUTSIDE root comes back as "../x", which the scope check
 * rejects, so an admin edit can never touch api/, docs/, .github/, e2e/ ... of the repo.
 */
export function changedFiles(root, base) {
  const run = (cwd, ...args) => execFileSync("git", args, { cwd, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
  const top = run(root, "rev-parse", "--show-toplevel").trim();
  const prefix = path.relative(top, path.resolve(root)).split(path.sep).join("/"); // "" when root is the repo root
  const fromTop = (l) => (prefix ? path.posix.relative(prefix, l) : l);
  const names = new Set();
  const lines = [
    ...run(root, "diff", "--name-only", "--no-renames", base).split(/\r?\n/), // repo-root relative names
    ...run(top, "ls-files", "--others", "--exclude-standard", "--full-name").split(/\r?\n/),
  ];
  for (const l of lines) if (l.trim()) names.add(fromTop(l.trim()));
  return [...names];
}

// ───────────── design scan ─────────────
const NAME_RE = /^[A-Za-z0-9._-]+$/;
/**
 * Inventory + scans of one design tree (dirName "design" or "design-defaults"). Returns violations.
 * design-defaults has the same shape plus copy/ fragments and a copy.js that aggregates them (imports + spread);
 * that aggregator is scanned as code instead of as a pure data file.
 */
export function scanTree(root, dirName, api) {
  const out = [];
  const defaults = dirName === "design-defaults";
  const files = walk(root, dirName);
  if (files.length > 300) out.push(v("files/count", `${files.length} files (cap 300)`, dirName));
  let total = 0;
  for (const rel of files) {
    const abs = path.join(root, rel);
    const st = lstatSync(abs);
    total += st.size;
    if (st.isSymbolicLink()) { out.push(v("files/symlink", "symbolic links are not allowed", rel)); continue; }
    const segs = rel.split("/");
    if (segs.length > 7 || !segs.every((s) => NAME_RE.test(s))) { out.push(v("files/name", "file names may use only letters, digits, . _ - and at most 6 folder levels", rel)); continue; }
    const ext = path.extname(rel).toLowerCase();
    if (rel.startsWith(`${dirName}/assets/`)) {
      if (["README.md", "README.txt"].includes(segs[segs.length - 1])) continue;
      out.push(...scanAsset(readFileSync(abs), rel));
    } else if (ext === ".js") {
      if (st.size > LIMITS.jsFileMaxBytes) { out.push(v("js/size", `larger than ${LIMITS.jsFileMaxBytes / 1024} KiB`, rel)); continue; }
      const src = readFileSync(abs, "utf8");
      const isCopyFragment = defaults && segs[1] === "copy" && segs.length === 3;
      const isAggregator = defaults && rel === "design-defaults/copy.js";
      if (rel === `${dirName}/routes.js` || isCopyFragment || (!defaults && rel === "design/copy.js")) out.push(...scanDataJs(src, rel));
      else if (isAggregator) out.push(...scanCode(src, rel, { api, designRelPath: rel }));
      else if (segs.length === 2) out.push(v("files/location", "only copy.js and routes.js may sit directly in design/; put code in design/views/ or design/parts/", rel));
      else out.push(...scanCode(src, rel, { api, designRelPath: rel }));
    } else if (ext === ".css") {
      if (st.size > LIMITS.cssMaxBytes) { out.push(v("css/size", `larger than ${LIMITS.cssMaxBytes / 1024} KiB`, rel)); continue; }
      out.push(...scanCss(readFileSync(abs, "utf8"), rel, rel));
    } else if (ext === ".md" || ext === ".txt") {
      if (st.size > 64 * 1024) out.push(v("files/size", "documentation files are capped at 64 KiB", rel));
    } else {
      out.push(v("files/type", `${ext || "(no extension)"} is not an allowed design file (js, css, md, txt; images and fonts only under ${dirName}/assets/: ${ASSET_EXTENSIONS.join(", ")})`, rel));
    }
  }
  if (total > 4 * 1024 * 1024) out.push(v("files/size", `${(total / 1048576).toFixed(1)} MiB in total (cap 4 MiB)`, dirName));
  return out;
}

/** Non-failing notes (printed by main). Reset per run. */
export const NOTES = [];
const imp = async (root, rel) => (await import(`${pathToFileURL(path.join(root, rel)).href}?t=${Date.now()}${Math.random()}`)).default;

// ───────────── schema: copy.js + routes.js ─────────────
export async function schemaChecks(root, dataClean = {}) {
  const out = [];
  let defaults = {};
  try { defaults = await imp(root, "design-defaults/copy.js"); } catch (e) { out.push(v("schema/defaults", `design-defaults/copy.js cannot be loaded: ${e.message}`, "design-defaults/copy.js")); }
  for (const msg of defaultsProblems(defaults)) out.push(v("schema/defaults", `design-defaults/copy.js: ${msg}`, "design-defaults/copy.js"));
  if (!existsSync(path.join(root, "design/copy.js"))) out.push(v("schema/copy", "design/copy.js is missing", "design/copy.js"));
  else if (dataClean["design/copy.js"] !== false) {
    try {
      const { warnings, retired } = validateCopy(await imp(root, "design/copy.js"), defaults);
      for (const w of warnings) out.push(v("schema/copy", w, "design/copy.js"));
      for (const w of retired) NOTES.push(`note (not a failure) design/copy.js: ${w}`);
    } catch (e) { out.push(v("schema/copy", `design/copy.js cannot be loaded: ${e.message}`, "design/copy.js")); }
  }
  if (!existsSync(path.join(root, "design/routes.js"))) out.push(v("schema/routes", "design/routes.js is missing", "design/routes.js"));
  else if (dataClean["design/routes.js"] !== false) {
    try {
      const { routes: ok, warnings } = validateRoutes(await imp(root, "design/routes.js"));
      for (const w of warnings) out.push(v("schema/routes", w, "design/routes.js"));
      for (const [id, p] of Object.entries(ok)) {
        const f = `design/${p}`;
        if (!existsSync(path.join(root, f))) { out.push(v("schema/routes", `${id} -> ${p}: the file does not exist`, "design/routes.js")); continue; }
        if (!/export\s+default\s+(async\s+)?function|export\s*\{[^}]*\bas\s+default\b|export\s+default\s+(async\s+)?\(/.test(readFileSync(path.join(root, f), "utf8"))) out.push(v("schema/routes", `${id} -> ${p}: no \`export default function\``, f));
      }
    } catch (e) { out.push(v("schema/routes", `design/routes.js cannot be loaded: ${e.message}`, "design/routes.js")); }
  }
  return out;
}

// ───────────── surface: lib/public-api.json == real ctx ─────────────
export function readApi(root) {
  const api = JSON.parse(readFileSync(path.join(root, "lib/public-api.json"), "utf8"));
  return Object.fromEntries(Object.entries(api).filter(([k]) => k !== "_comment"));
}
export async function ctxChecks(root) {
  const out = [];
  const declared = readApi(root);
  const real = await realSurface(root);
  const F = "lib/public-api.json";
  for (const k of new Set([...Object.keys(real), ...Object.keys(declared)])) {
    const a = real[k], b = declared[k];
    if (!a) { out.push(v("ctx/manifest", `${k} is listed in ${F} but does not exist on the real ctx`, F)); continue; }
    if (!b) { out.push(v("ctx/manifest", `${k} exists on the real ctx but is missing from ${F}`, F)); continue; }
    for (const n of a) if (!b.includes(n)) out.push(v("ctx/manifest", `${k}.${n} exists on the real ctx but is not listed in ${F}`, F));
    for (const n of b) if (!a.includes(n)) out.push(v("ctx/manifest", `${k}.${n} is listed in ${F} but does not exist on the real ctx`, F));
  }
  return { out, api: declared };
}

// ───────────── defaults: scans + every route id has a built-in view ─────────────
export async function defaultsChecks(root, api) {
  const out = scanTree(root, "design-defaults", api).map((x) => ({ ...x, rule: `defaults:${x.rule}` }));
  if (!existsSync(path.join(root, "design-defaults/routes.js"))) return [...out, v("defaults/routes", "design-defaults/routes.js is missing", "design-defaults/routes.js")];
  const routes = await imp(root, "design-defaults/routes.js");
  for (const id of ROUTE_IDS) {
    if (!routes[id]) out.push(v("defaults/routes", `no built-in view for route id "${id}"`, "design-defaults/routes.js"));
    else if (!existsSync(path.join(root, "design-defaults", routes[id]))) out.push(v("defaults/routes", `${id} -> ${routes[id]} does not exist`, "design-defaults/routes.js"));
  }
  return out;
}

// ───────────── copy keys: every literal t("key") of the core / default views exists in the defaults ─────────────
export async function copyChecks(root) {
  const out = [];
  const defaults = await imp(root, "design-defaults/copy.js");
  const files = ["app.js", ...["lib", "ui", "core", "design-defaults/views", "design-defaults/parts"].flatMap((d) => walk(root, d))].filter((f) => f.endsWith(".js"));
  for (const f of files) {
    const src = readFileSync(path.join(root, f), "utf8");
    for (const m of src.matchAll(/\b(?:ctx\.)?t\(\s*"([^"]+)"/g)) if (!(m[1] in defaults)) out.push(v("copy/missing", `copy key "${m[1]}" is used but not defined in design-defaults/copy.js`, f, src.slice(0, m.index).split("\n").length));
    for (const m of src.matchAll(/\b(?:ctx\.)?t\(\s*`([^`$]*)\$\{/g)) if (!Object.keys(defaults).some((k) => k.startsWith(m[1]))) out.push(v("copy/missing", `no copy keys start with "${m[1]}" (used with a computed suffix)`, f));
  }
  return out;
}

// ───────────── fixed warnings ─────────────
/**
 * The warning of an irreversible confirmation is core-owned (lib/meta `warned`, rendered via core/warnings.js). So:
 *  - no copy file (design/ or design-defaults/) may define a confirm.*warn* key;
 *  - every warned id has confirm.<id>.title / .text / .yes in BOTH copy files (design/copy.js is the editable full copy);
 *  - every warned id has a non-trivial fixed warning text in lib/meta.
 */
export async function warningChecks(root) {
  const out = [];
  const { WARNINGS, WARNED_IDS } = await import(`${pathToFileURL(path.join(root, "lib/meta/index.js")).href}?t=${Math.random()}`);
  const load = async (rel) => { try { return await imp(root, rel); } catch (e) { out.push(v("warnings/load", `${rel} cannot be loaded: ${e.message}`, rel)); return null; } };
  const copies = { "design-defaults/copy.js": await load("design-defaults/copy.js"), "design/copy.js": await load("design/copy.js") };
  for (const [file, copy] of Object.entries(copies)) {
    if (!copy) continue;
    for (const k of Object.keys(copy)) {
      const w = fixedWarningViolation(k);
      if (w) out.push(v("warnings/copy-key", w, file));
      else if (/^confirm\./.test(k) && /warn/i.test(k)) out.push(v("warnings/copy-key", `"${k}": no confirm.* copy key may name a warning`, file));
    }
    for (const id of WARNED_IDS) for (const part of ["title", "text", "yes"]) {
      if (!(`confirm.${id}.${part}` in copy)) out.push(v("warnings/copy-missing", `confirm.${id}.${part} is missing (confirm "${id}" has a fixed warning, so it needs title, text and yes in the copy)`, file));
    }
  }
  for (const id of WARNED_IDS) if (typeof WARNINGS[id] !== "string" || WARNINGS[id].trim().length < 6) out.push(v("warnings/empty", `the fixed warning of confirm "${id}" is empty or too short`, "lib/meta"));
  if (!WARNED_IDS.length) out.push(v("warnings/empty", "lib/meta defines no fixed warnings at all", "lib/meta"));
  return out;
}

// ───────────── deploy: nginx.conf CSP + Dockerfile + index.html ─────────────
/**
 * A strict CSP is a deployment requirement. admin/nginx.conf must send, with `always`:
 *   default-src 'self'; script-src 'self' [+ 'wasm-unsafe-eval' for the vendored HEIC decoder] (no unsafe-inline /
 *   unsafe-eval / wildcard / remote host); style-src 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none';
 *   form-action present. index.html must carry no inline script / style / handler attribute.
 */
export function deployChecks(root) {
  const out = [];
  const read = (rel) => (existsSync(path.join(root, rel)) ? readFileSync(path.join(root, rel), "utf8") : null);
  const nginx = read("nginx.conf");
  const N = "nginx.conf";
  const bad = (msg) => out.push(v("deploy/csp-weak", msg, N));
  if (nginx === null) out.push(v("deploy/csp-missing", "nginx.conf is missing: the admin hosting config must carry the strict CSP", N));
  else {
    const live = nginx.split("\n").filter((l) => !/^\s*#/.test(l)).join("\n");
    const m = /add_header\s+Content-Security-Policy\s+"([^"]+)"\s*(always)?\s*;/i.exec(live);
    if (!m) out.push(v("deploy/csp-missing", "nginx.conf has no (uncommented) `add_header Content-Security-Policy \"...\" always;`", N));
    else {
      if (!m[2]) out.push(v("deploy/csp-always", "the CSP add_header must end with `always` so error responses carry it too", N));
      const dirs = new Map();
      for (const part of m[1].split(";")) { const [n, ...rest] = part.trim().split(/\s+/); if (n) dirs.set(n.toLowerCase(), rest); }
      const exact = (n, want) => { const got = dirs.get(n); if (!got || got.join(" ") !== want) bad(`CSP ${n} must be exactly ${want}${got ? ` (found: ${got.join(" ")})` : " (missing)"}`); };
      if (!dirs.has("default-src")) bad("CSP has no default-src"); else exact("default-src", "'self'");
      const script = dirs.get("script-src");
      if (!script) bad("CSP has no script-src");
      else {
        if (!script.includes("'self'")) bad("CSP script-src must contain 'self'");
        const extra = script.filter((x) => x !== "'self'" && x !== "'wasm-unsafe-eval'");
        if (extra.length) bad(`CSP script-src may contain only 'self' and 'wasm-unsafe-eval' (found also: ${extra.join(" ")})`);
      }
      exact("style-src", "'self'");
      exact("frame-ancestors", "'none'");
      exact("base-uri", "'none'");
      exact("object-src", "'none'");
      if (!dirs.has("form-action")) bad("CSP has no form-action");
      for (const [n, vals] of dirs) {
        const w = vals.find((x) => /^'unsafe-(inline|eval|hashes)'$/i.test(x) || x === "*" || (/^https?:\/?\/?\*?$/.test(x) && !(n === "img-src" && x === "https:")));
        if (w) bad(`CSP ${n} must not contain ${w}`);
      }
    }
  }
  const docker = read("Dockerfile");
  if (docker === null) out.push(v("deploy/csp-dockerfile", "Dockerfile is missing", "Dockerfile"));
  else {
    const live = docker.split("\n").filter((l) => !/^\s*#/.test(l)).join("\n");
    if (!/COPY\s+nginx\.conf\s+\/etc\/nginx\/conf\.d\/default\.conf/.test(live)) out.push(v("deploy/csp-dockerfile", "Dockerfile must install nginx.conf as /etc/nginx/conf.d/default.conf (the CSP is not optional)", "Dockerfile"));
    if (/\b(sed|truncate|echo|printf|cat)\b[^\n]*(default\.conf|nginx\.conf|Content-Security)/i.test(live) || /\brm\b[^\n]*default\.conf/i.test(live)) out.push(v("deploy/csp-dockerfile", "Dockerfile must not remove or edit the CSP / nginx config after installing it", "Dockerfile"));
  }
  out.push(...indexHtmlChecks(root));
  return out;
}
export function indexHtmlChecks(root) {
  const f = "index.html";
  if (!existsSync(path.join(root, f))) return [v("deploy/index-html", "index.html is missing", f)];
  const src = readFileSync(path.join(root, f), "utf8").replace(/<!--[\s\S]*?-->/g, (c) => c.replace(/[^\n]/g, " "));
  const out = [];
  const lineOf = (i) => src.slice(0, i).split("\n").length;
  for (const m of src.matchAll(/<script\b([^>]*)>/gi)) {
    if (!/\bsrc\s*=/i.test(m[1])) out.push(v("deploy/index-html", "inline <script> (script-src 'self' forbids it)", f, lineOf(m.index)));
    else if (/\bsrc\s*=\s*["']?(https?:)?\/\//i.test(m[1])) out.push(v("deploy/index-html", "external <script src> (script-src 'self')", f, lineOf(m.index)));
  }
  for (const m of src.matchAll(/<style\b/gi)) out.push(v("deploy/index-html", "inline <style> block (style-src 'self' forbids it)", f, lineOf(m.index)));
  for (const m of src.matchAll(/<[a-z][^>]*>/gi)) {
    const tag = m[0];
    if (/\son[a-z]+\s*=/i.test(tag)) out.push(v("deploy/index-html", `inline event-handler attribute in ${tag.slice(0, 60)}`, f, lineOf(m.index)));
    if (/\sstyle\s*=/i.test(tag)) out.push(v("deploy/index-html", `inline style attribute in ${tag.slice(0, 60)} (style-src 'self' forbids it)`, f, lineOf(m.index)));
    if (/\s(href|src)\s*=\s*["']?\s*javascript:/i.test(tag)) out.push(v("deploy/index-html", "javascript: URL", f, lineOf(m.index)));
    if (/^<link\b/i.test(tag) && /\bhref\s*=\s*["']?(https?:)?\/\//i.test(tag)) out.push(v("deploy/index-html", `external <link href> (style-src 'self'): ${tag.slice(0, 80)}`, f, lineOf(m.index)));
  }
  return out;
}

// ───────────── drift (NOTE only): design/ vs design-defaults/ ─────────────
export async function driftNotes(root) {
  const notes = [];
  const norm = (rel) => readFileSync(path.join(root, rel), "utf8").replace(/\r\n/g, "\n");
  for (const sub of ["views", "parts"]) {
    const d = walk(root, `design-defaults/${sub}`).map((f) => f.slice("design-defaults/".length));
    const c = walk(root, `design/${sub}`).map((f) => f.slice("design/".length));
    for (const f of d) {
      if (!c.includes(f)) notes.push(`note (not a failure) drift: design/${f} does not exist (the built-in one is used if routed)`);
      else if (norm(`design-defaults/${f}`) !== norm(`design/${f}`)) notes.push(`note (not a failure) drift: design/${f} differs from design-defaults/${f}`);
    }
    for (const f of c) if (!d.includes(f)) notes.push(`note (not a failure) drift: design/${f} has no counterpart in design-defaults/`);
  }
  try {
    const defaults = await imp(root, "design-defaults/copy.js");
    const copy = await imp(root, "design/copy.js");
    const changed = Object.keys(copy).filter((k) => k in defaults && copy[k] !== defaults[k]).length;
    const missing = Object.keys(defaults).filter((k) => !(k in copy)).length;
    if (changed) notes.push(`note (not a failure) drift: ${changed} copy value(s) in design/copy.js differ from design-defaults/copy.js`);
    if (missing) notes.push(`note (not a failure) drift: ${missing} default copy key(s) are not defined in design/copy.js (the defaults are used)`);
  } catch { /* reported by the schema check */ }
  return notes;
}

// ───────────── run everything ─────────────
export async function runAll(root, { changed = null, layers = true } = {}) {
  NOTES.length = 0;
  const steps = [];
  const violations = [];
  const step = (name, list, note = "") => { steps.push({ name, count: list.length, note }); violations.push(...list); };
  const guarded = async (name, fn) => { try { return await fn(); } catch (e) { return [v("core/load", `${name}: ${String(e.message).split("\n")[0]}`, "(core)")]; } };

  if (changed) step("scope (only design/** may change)", scopeViolations(changed), `${changed.length} changed file(s)`);
  else steps.push({ name: "scope", count: 0, note: "skipped (give --base <ref> or --files ...)" });

  let api;
  try { api = readApi(root); } catch (e) { return { fatal: `cannot read lib/public-api.json: ${e.message}` }; }

  const treeV = scanTree(root, "design", api);
  step("design scan (files, js, css, assets)", treeV);
  const dataClean = {};
  for (const f of ["design/copy.js", "design/routes.js"]) dataClean[f] = !treeV.some((x) => x.file === f);
  step("schema (copy.js, routes.js)", await schemaChecks(root, dataClean));
  step("surface (lib/public-api.json == real ctx)", await guarded("ctx", async () => (await ctxChecks(root)).out));
  step("defaults (design-defaults scans, a built-in view per route)", await guarded("defaults", () => defaultsChecks(root, api)));
  step("copy keys (used == defined)", await guarded("copy", () => copyChecks(root)));
  step("fixed warnings", await guarded("warnings", () => warningChecks(root)));
  step("deploy (nginx.conf CSP, Dockerfile, index.html)", deployChecks(root));
  step("core manifest (scripts/core.manifest.json)", manifestChecks(root).out);
  if (layers) step("layers (lib / ui / core / app.js boundaries)", layerChecks(root));
  try { NOTES.push(...(await driftNotes(root))); } catch { /* notes only */ }
  return { steps, violations, notes: [...NOTES] };
}

async function main() {
  const argv = process.argv.slice(2);
  const flag = (n) => argv.includes(n);
  const val = (n) => { const i = argv.indexOf(n); return i >= 0 ? argv[i + 1] : null; };
  if (flag("--help") || flag("-h")) { console.log(readFileSync(fileURLToPath(import.meta.url), "utf8").split("\n").slice(1, 20).join("\n")); return 0; }
  const root = path.resolve(val("--root") || path.join(HERE, ".."));
  if (!existsSync(path.join(root, "design")) || !existsSync(path.join(root, "lib"))) { console.error(`${root}: not an admin checkout (no design/ or lib/)`); return 2; }

  if (flag("--update")) {
    const r = manifestChecks(root, { update: true });
    console.log(`wrote ${MANIFEST_FILE} (${r.wrote} locked files). Review the core diff first: this blesses it.`);
    return 0;
  }

  let changed = null;
  if (val("--base")) {
    try { changed = changedFiles(root, val("--base")); } catch (e) { console.error(`cannot read the diff against "${val("--base")}": ${(e.stderr || e.message).toString().trim()}`); return 2; }
  } else if (flag("--files")) {
    const i = argv.indexOf("--files");
    changed = argv.slice(i + 1).filter((a) => !a.startsWith("--"));
  }
  const r = await runAll(root, { changed, layers: !flag("--no-layers") });
  if (r.fatal) { console.error(r.fatal); return 2; }
  const { steps, violations, notes } = r;
  if (flag("--json")) { console.log(JSON.stringify({ ok: violations.length === 0, steps, violations, notes }, null, 2)); return violations.length ? 1 : 0; }
  for (const s of steps) console.log(`${s.count ? "x " : "ok"}  ${s.name}${s.note ? `  (${s.note})` : ""}${s.count ? `  ${s.count} violation(s)` : ""}`);
  for (const n of notes) console.log(n);
  if (violations.length) {
    console.error("\nDESIGN CHECK FAILED");
    for (const x of violations) console.error(`  x [${x.rule}] ${x.file}${x.line ? `:${x.line}` : ""}  ${x.msg}`);
    return 1;
  }
  console.log("\ndesign check OK");
  return 0;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().then((code) => process.exit(code), (e) => { console.error(e); process.exit(2); });
}
