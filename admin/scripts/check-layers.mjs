#!/usr/bin/env node
// Layer guard. Run: node scripts/check-layers.mjs [--root <admin dir>]   (also run by scripts/check-design.mjs)
//   lib/        = API / session / RBAC / design-loader layer (CORE). May not import ui/, core/, design/, design-defaults/ or
//                 app.js, and must not build UI (DOM). Two files legitimately touch the DOM: see LIB_DOM_ALLOW.
//   ui/         = UI primitives (CORE). No fetch / XHR / storage / API paths / tokens; may not import core/; may import only a
//                 short list of lib modules (UI_LIB_ALLOW).
//   core/, app.js = the ctx facade, actions, auth and bootstrap (CORE). No raw fetch / XHR / storage / `/api/v1` / tokens
//                 (all of it goes through lib/api.js `api()`), except the precise snippets in NET_ALLOW; never a static
//                 import of design/ or design-defaults/ (only lib/design.js loads them, dynamically).
// The allowlists match exact code snippets (not whole files), so any NEW use in the same file still fails.
import { readFileSync, existsSync, readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));

// Comments out of the way (cheap and good enough here: none of the checked files has "//" inside a checked string).
const strip = (s) => s.replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, " ")).replace(/(^|[^:\\"'`])\/\/[^\n]*/g, "$1");

const walk = (root, dir) => {
  const abs = path.join(root, dir);
  if (!existsSync(abs)) return [];
  return readdirSync(abs, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? walk(root, `${dir}/${e.name}`) : e.name.endsWith(".js") && !e.name.includes(".min.") ? [`${dir}/${e.name}`] : []));
};

// lib/ may not touch the DOM, except these exact snippets.
const LIB_DOM_ALLOW = {
  // applies the shop brand to :root CSS variables and to <img class="brand-logo">
  "lib/branding.js": [/document\.documentElement/g, /document\.querySelectorAll\(\s*"\.brand-logo"\s*\)/g],
  // HEIC -> JPEG conversion: an offscreen <canvas>, and loading the vendored heic2any <script> on demand
  "lib/upload.js": [/document\.createElement\(\s*"canvas"\s*\)/g, /document\.createElement\(\s*"script"\s*\)/g, /document\.head\.append\(\s*s\s*\)/g],
};
// ui/ may import these lib modules only (design.js: t(); upload.js: mediaEnabled for the upload button; money/format: pure).
const UI_LIB_ALLOW = new Set(["design.js", "upload.js", "money.js", "format.js", "content-schema.js"]);
// core/, app.js: raw network/storage snippets that are legitimate (each justified).
const NET_ALLOW = {
  // one-time link-token flows (password setup / invitation accept) are UNAUTHENTICATED POSTs that must not carry or touch the session tokens
  "core/auth.js": [/fetch\(\s*`\$\{API_BASE\}\/api\/v1\/admin\$\{path\}`/g],
  // opens the render-preview URL (a navigation, not a request) with the server-issued short-lived preview token
  "core/groups/cms.js": [/window\.open\(\s*`\$\{API_BASE\}\/api\/v1\/render\/preview\?token=/g],
};
// Token / base-URL identifiers of lib/api.js: who may even name them.
const TOKEN_ALLOW = { tokens: ["core/auth.js"], refreshTokens: ["core/auth.js"], logoutRequest: [], COOKIE_MODE: [], API_BASE: ["core/auth.js", "core/groups/cms.js"] };
const NET = /\bfetch\s*\(|XMLHttpRequest|\/api\/v1|\blocalStorage\b|\bsessionStorage\b|document\.cookie|\bWebSocket\b|\bsendBeacon\b|\bEventSource\b|\bindexedDB\b/;

const lineOf = (src, i) => src.slice(0, i).split("\n").length;
const withoutAllowed = (src, regs = []) => regs.reduce((s, re) => s.replace(re, (m) => " ".repeat(m.length)), src);

export function layerChecks(root) {
  const out = [];
  const bad = (rule, file, msg, line = 0) => out.push({ rule: `layers/${rule}`, file, line, msg });
  const read = (f) => strip(readFileSync(path.join(root, f), "utf8"));
  const importsOf = (src) => [...src.matchAll(/(?:\bfrom|\bimport)\s*\(?\s*["']([^"']+)["']/g)].map((m) => ({ spec: m[1], index: m.index }));

  for (const f of [...walk(root, "lib"), ...walk(root, "lib/meta")].filter((x, i, a) => a.indexOf(x) === i)) {
    const src = read(f);
    for (const im of importsOf(src)) {
      const target = path.posix.normalize(path.posix.join(path.posix.dirname(f), im.spec));
      if (/^(ui|core|design|design-defaults)\//.test(target) || target === "app.js") bad("lib-import", f, `lib must not import ${target}`, lineOf(src, im.index));
    }
    const s = withoutAllowed(src, LIB_DOM_ALLOW[f]);
    for (const m of s.matchAll(/\bh\(\s*["'`]|innerHTML|outerHTML|insertAdjacentHTML|\.append\(|appendChild|createElement|\bdocument\b|\bdocument\./g)) bad("lib-dom", f, `lib must not build or touch the DOM (${m[0].trim()}); allowed exceptions are listed in scripts/check-layers.mjs`, lineOf(s, m.index));
  }

  const checkCommon = (f, src) => {
    const s = withoutAllowed(src, NET_ALLOW[f]);
    for (const m of s.matchAll(new RegExp(NET.source, "g"))) bad("net", f, `no raw network / storage / API path outside lib/ (found "${m[0].trim()}"): use lib/api.js api()`, lineOf(s, m.index));
    for (const [name, files] of Object.entries(TOKEN_ALLOW)) {
      if (files.includes(f)) continue;
      for (const m of s.matchAll(new RegExp(`\\b${name}\\b`, "g"))) bad("tokens", f, `"${name}" (lib/api.js token/base-URL internals) may not be used here`, lineOf(s, m.index));
    }
    for (const im of importsOf(src)) {
      if (/(^|\/)(design|design-defaults)\//.test(im.spec) && !/^\.\.?\/(\.\.\/)*lib\//.test(im.spec)) bad("design-import", f, `the core never imports the design layer statically (${im.spec}); lib/design.js loads it dynamically`, lineOf(src, im.index));
    }
  };

  for (const f of walk(root, "ui")) {
    const src = read(f);
    checkCommon(f, src);
    for (const im of importsOf(src)) {
      if (!im.spec.startsWith(".")) continue;
      const target = path.posix.normalize(path.posix.join(path.posix.dirname(f), im.spec));
      if (/^core\//.test(target) || target === "app.js") bad("ui-import", f, `ui must not import ${target}`, lineOf(src, im.index));
      else if (/^lib\//.test(target) && !UI_LIB_ALLOW.has(target.slice(4))) {
        // lib/api.js is allowed only for the ApiError class
        const stmt = src.slice(Math.max(0, src.lastIndexOf("import", im.index)), im.index + im.spec.length + 2);
        if (!(target === "lib/api.js" && /^\s*import\s*\{\s*ApiError\s*\}/.test(stmt))) bad("ui-import", f, `ui may import only lib/{${[...UI_LIB_ALLOW].join(",")}} (and ApiError from lib/api.js), not ${target}`, lineOf(src, im.index));
      }
    }
  }
  for (const f of [...walk(root, "core"), ...(existsSync(path.join(root, "app.js")) ? ["app.js"] : [])]) checkCommon(f, read(f));
  return out;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const i = process.argv.indexOf("--root");
  const root = path.resolve(i >= 0 ? process.argv[i + 1] : path.join(here, ".."));
  const out = layerChecks(root);
  if (out.length) {
    for (const x of out) console.error(`  x [${x.rule}] ${x.file}${x.line ? `:${x.line}` : ""}  ${x.msg}`);
    console.error(`${out.length} layer violation(s)`);
    process.exit(1);
  }
  console.log("layers ok");
}
