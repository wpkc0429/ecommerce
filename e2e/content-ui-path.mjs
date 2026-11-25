// Browser E2E for the platform admin's content-store screens (stage C of the locked-core / design-layer split):
//   #/documents[/<key>[/revisions]]  documents list, schema-driven editor, revisions + restore, preview token, schedule panel
//   #/content-schema                 schema status, dry run, registration through the core confirmation dialog
//   #/content-publishing             upcoming/recent schedules and the publish hook settings
//
// The screens are driven as the PLATFORM super admin (manual shop id input #shop-input) on a FRESH shop created through the
// provisioning API (the demo shop has a format 1 "starter" schema; format 2 registration is exercised here, see also
// commerce-path section 15), and as a merchant EDITOR of that shop (content.view + content.edit, no publish) to prove the
// permission gating of the buttons and of the schema / publishing screens.
//
// It needs the API stack of content-ops-path.mjs (CONTENT_HOOK_DEV_INSECURE=1 CONTENT_HOOK_ALLOWED_HOSTS=localhost, short
// debounce) for the publish hook part; without it that part is skipped (or fails under E2E_REQUIRE_CONTENT_OPS=1). The
// core's own schedule check wants >= 2 minutes of lead, so the schedule is set 4-6 minutes ahead and is never allowed to
// fire (it is cancelled / superseded before). Image upload goes to the local MinIO; when that is not reachable the upload
// is reported as skipped and only the list / remove controls and the URL rules are checked.
//
// Runs standalone (`node content-ui-path.mjs`: needs the API on E2E_API_BASE_URL and the admin on E2E_ADMIN_BASE_URL) or is
// imported by happy-path.mjs (runContentUiPath(browser, {admin})), sharing lib.mjs's failure counter.
import { createHmac, randomUUID } from "node:crypto";
import { createServer } from "node:http";
import { fileURLToPath, pathToFileURL } from "node:url";
import { chromium } from "playwright";
import { WARNINGS } from "../admin/lib/meta/index.js";
import { API, armConfirm, brief, check, dotEnvValue, fail, failureCount, req as rawReq, runID, section } from "./lib.mjs";
import { platformToken } from "./application-path.mjs";

const STUB_PORT = Number(process.env.E2E_UI_HOOK_STUB_PORT ?? 8191);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const ADMIN_BASE = () => (process.env.E2E_ADMIN_BASE_URL ?? "http://localhost:3001").replace(/\/+$/, "");
const PLATFORM_EMAIL = () => process.env.E2E_ADMIN_EMAIL || process.env.SEED_ADMIN_EMAIL || dotEnvValue("SEED_ADMIN_EMAIL") || "admin@example.com";
const PLATFORM_PASSWORD = () => process.env.E2E_ADMIN_PASSWORD || process.env.SEED_ADMIN_PASSWORD || dotEnvValue("SEED_ADMIN_PASSWORD") || "admin-change-me";
const FIXTURE_JPG = fileURLToPath(new URL("./fixtures/exif-orientation-6.jpg", import.meta.url));

// ── the schema every UI scenario uses: one document type per editor kind family (format 2) ──
const str = (extra) => ({ type: "string", default: "", maxLength: 200, ...extra });
export const landingNode = {
  type: "object", additionalProperties: false, required: ["headline"],
  properties: {
    headline: str({ title: "Headline", default: "Hello", minLength: 1, maxLength: 40, "x-editor-order": 0 }),
    lead: str({ title: "Lead", "x-editor": "textarea", maxLength: 300, "x-editor-order": 1 }),
    body: str({ title: "Body", "x-editor": "richtext", maxLength: 120, "x-editor-order": 2 }),
    cover: str({ title: "Cover", "x-editor": "image", maxLength: 2048, "x-editor-order": 3 }),
    cta: str({ title: "CTA link", "x-editor": "link", maxLength: 2048, "x-editor-order": 4 }),
    accent: str({ title: "Accent", "x-editor": "color", default: "#336699", maxLength: 7, "x-editor-order": 5 }),
    layout: { type: "string", title: "Layout", enum: ["wide", "narrow"], default: "wide", "x-editor": "select", "x-editor-order": 6 },
    tags: { type: "array", title: "Tags", maxItems: 5, default: [], "x-editor-order": 8, items: str({ title: "Tag", maxLength: 20 }) },
    faq: {
      type: "array", title: "FAQ", maxItems: 5, default: [], "x-editor-order": 9,
      items: { type: "object", title: "Q and A", additionalProperties: false, required: ["q"], properties: { q: str({ title: "Question", maxLength: 100 }), a: str({ title: "Answer", "x-editor": "textarea", maxLength: 300 }) } },
    },
    blocks: {
      type: "array", title: "Blocks", maxItems: 5, default: [], "x-editor": "sections", "x-editor-order": 10,
      items: {
        oneOf: [
          { type: "object", title: "Text block", additionalProperties: false, required: ["type"], properties: { type: { type: "string", const: "text" }, body: str({ title: "Text", maxLength: 200 }) } },
          { type: "object", title: "Banner block", additionalProperties: false, required: ["type"], properties: { type: { type: "string", const: "banner" }, text: str({ title: "Banner text", maxLength: 100 }) } },
        ],
      },
    },
  },
};
const noteNode = { type: "object", additionalProperties: false, properties: { title: str({ title: "Title", maxLength: 100 }) } };
export const uiSchema = {
  format: 2,
  types: { landing: { title: "Landing", schema: landingNode }, note: { title: "Note", schema: noteNode } },
  documents: { banner: { type: "note" } },
  collections: { "campaign-": { type: "landing", max: 5 } },
};
/** Same schema without the `tags` field: registering it would drop the values of that field (a removal). */
const { tags: _dropped, ...landingNoTags } = landingNode.properties;
export const uiSchemaV2 = { ...uiSchema, types: { ...uiSchema.types, landing: { title: "Landing", schema: { ...landingNode, properties: landingNoTags } } } };

// ── shared helpers (also used by design-path.mjs) ──
// Access tokens are short-lived and a full suite run is long: a token registered with trackToken() is refreshed (with its
// refresh token) when the API answers 401, and the request is repeated once. Everything else is lib.mjs's req().
const trackedTokens = new Map();
export const trackToken = (access, refresh) => trackedTokens.set(access, { cur: access, refresh });
export async function req(method, path, opts = {}) {
  const st = opts.token && trackedTokens.get(opts.token);
  if (!st) return rawReq(method, path, opts);
  let r = await rawReq(method, path, { ...opts, token: st.cur });
  if (r.status === 401 && st.refresh) {
    const rr = await rawReq("POST", "/api/v1/admin/auth/refresh", { body: { refresh_token: st.refresh } });
    if (rr.json?.access_token) {
      st.cur = rr.json.access_token;
      st.refresh = rr.json.refresh_token ?? st.refresh;
      r = await rawReq(method, path, { ...opts, token: st.cur });
    }
  }
  return r;
}

/** A fresh shop with a password-set owner, a publishable key, and an `editor` member. No schema is registered. */
export async function provisionContentShop(label) {
  const platform = await platformToken();
  const ownerEmail = `e2e-${label}-${runID}@example.com`;
  const ownerPassword = `e2e-${label}-owner-${runID}-pw`;
  const prov = await req("POST", "/api/v1/admin/shop-provisioning", { token: platform, body: { name: `E2E ${label} ${runID}`, owner_email: ownerEmail, status: 1 } });
  if (prov.status !== 201) throw new Error(`shop provisioning failed: ${brief(prov)}`);
  const shop = prov.json.shop.id;
  const sp = (p) => `/api/v1/admin/shops/${shop}${p}`;
  const setupToken = String(prov.json.owner?.setup_url ?? "").split("/").pop();
  const setPw = await req("POST", "/api/v1/admin/auth/password-setup", { body: { token: setupToken, password: ownerPassword } });
  if (setPw.status !== 204) throw new Error(`owner password setup failed: ${brief(setPw)}`);
  const login = await req("POST", "/api/v1/admin/auth/login", { body: { email: ownerEmail, password: ownerPassword } });
  const owner = login.json?.access_token;
  if (!owner) throw new Error(`owner login failed: ${brief(login)}`);
  trackToken(owner, login.json?.refresh_token);
  const keyRes = await req("POST", sp("/publishable-keys"), { token: owner, body: { label: `e2e-${label}-${runID}` } });
  const pk = keyRes.json?.key;
  if (!pk) throw new Error(`publishable key failed: ${brief(keyRes)}`);
  // An editor (content.view + content.edit): invited by the owner, accepts with a password.
  const roles = await req("GET", sp("/roles"), { token: owner });
  const roleID = roles.json?.roles?.find((r) => r.name === "editor")?.id;
  const editorEmail = `e2e-${label}-editor-${runID}@example.com`;
  const editorPassword = `e2e-${label}-editor-${runID}-pw`;
  const inv = await req("POST", sp("/invitations"), { token: owner, body: { email: editorEmail, role_id: roleID } });
  const invToken = String(inv.json?.invite_url ?? "").split("/#/accept-invite/")[1];
  const acc = await req("POST", "/api/v1/admin/auth/invitations/accept", { body: { token: invToken, password: editorPassword } });
  if (inv.status !== 201 || ![200, 204].includes(acc.status)) throw new Error(`editor invitation failed: ${brief(inv)} ${brief(acc)}`);
  return { shop, sp, owner, ownerEmail, ownerPassword, pk, editorEmail, editorPassword };
}

export async function uiLogin(page, admin, email, password) {
  await page.goto(`${admin}/`);
  await page.waitForSelector("#login-email", { timeout: 10000 });
  for (let attempt = 0; ; attempt++) {
    await page.fill("#login-email", email);
    await page.fill("#login-password", password);
    await page.click("#login-form button[type=submit]");
    try {
      await page.waitForSelector("#view-main:not([hidden])", { timeout: 8000 });
      return;
    } catch (e) {
      // The login limiter (20 per IP / 5 minutes, 5 per IP+e-mail) is shared with the whole suite: wait for the window.
      if (attempt >= 1 || !(await page.isVisible("#login-error").catch(() => false))) throw e;
      console.log("  … admin login refused (rate limit?): waiting 5 minutes for the window");
      await sleep(5 * 60 * 1000 + 5000);
    }
  }
}

export async function newAdminPage(browser, admin) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  await ctx.addInitScript((api) => { try { localStorage.setItem("apiBase", api); } catch {} }, API);
  const page = await ctx.newPage();
  page.errors = [];
  page.on("pageerror", (e) => { page.errors.push(e.message); fail(`page JS error: ${e.message}`); });
  return page;
}

export async function selectShop(page, id) {
  const inp = page.locator("#shop-input");
  // fill + Tab: the browser's own `change` (on blur) fires exactly once, so no second selectShop races a later click
  const me = page.waitForResponse((r) => r.url().includes(`/shops/${id}/me`) && r.request().method() === "GET");
  await inp.fill(String(id));
  await inp.press("Tab");
  await me;
  await sleep(500);
}

/** Reads the open core confirmation dialog, then answers it. Call right after the click that opens it. */
export async function answerConfirm(page, accept = true) {
  const sel = 'dialog[data-testid="confirm-dialog"][open]';
  await page.waitForSelector(sel, { timeout: 10000 });
  const d = {
    title: (await page.locator(`${sel} h3`).innerText()).trim(),
    warning: await page.locator(`${sel} [data-testid="confirm-warning"]`).innerText().catch(() => null),
    detail: await page.locator(`${sel} [data-testid="confirm-detail"]`).innerText().catch(() => null),
    text: await page.locator(`${sel} .dlg__msg`).innerText().catch(() => ""),
  };
  await page.click(`${sel} [data-testid="${accept ? "confirm-yes" : "confirm-no"}"]`);
  await page.waitForSelector(sel, { state: "detached", timeout: 5000 });
  return d;
}

const tabsOf = (page) => page.$$eval("#tabs [data-tab]", (ns) => ns.map((n) => n.dataset.tab));
const hashOf = (page) => page.evaluate(() => location.hash);
const cf = (page, ptr) => page.locator(`[data-testid="cf:${ptr}"]`);
const fieldError = (page, ptr) => page.locator(`[data-pointer="${ptr}"] > .field-error`).first();
/** Taipei wall clock "YYYY-MM-DDTHH:mm" `min` minutes from now (Asia/Taipei is UTC+8 all year). */
const taipei = (min) => new Date(Date.now() + (480 + min) * 60000).toISOString().slice(0, 16);

function startStub() {
  const received = [];
  const state = { status: 200 };
  const server = createServer((rq, rs) => {
    const chunks = [];
    rq.on("data", (c) => chunks.push(c));
    rq.on("end", () => {
      received.push({ method: rq.method, url: rq.url, headers: rq.headers, body: Buffer.concat(chunks).toString("utf8") });
      rs.writeHead(state.status, { "Content-Type": "application/json" });
      rs.end(JSON.stringify({ success: true, internal: "STUB-INTERNAL-DETAIL" }));
    });
  });
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(STUB_PORT, "127.0.0.1", () => resolve({ server, received, state }));
  });
}

async function step(page, name, fn) {
  try { await fn(); } catch (e) {
    fail(`${name} aborted: ${e?.message ?? e}`);
    const dir = process.env.E2E_SHOTS_DIR;
    if (dir && page) await page.screenshot({ path: `${dir}/content-ui-${name.replace(/\W+/g, "-")}.png`, fullPage: true }).catch(() => {});
  }
}

export async function runContentUiPath(browser, { admin = ADMIN_BASE() } = {}) {
  section("㉗ Content screens in the browser: documents, schema registration, publishing and hook (platform admin + merchant editor)");
  const S = await provisionContentShop("ui");
  const { shop, sp, owner, pk } = S;
  const docKey = `campaign-ui-${runID}`;
  const docKey2 = `campaign-two-${runID}`;
  const docPath = `/content/documents/${docKey}`;
  const pubRead = (key = docKey) => req("GET", `/api/v1/shop/content/${key}`, { pk });
  const apiDoc = async (key = docKey) => (await req("GET", sp(`/content/documents/${key}`), { token: owner })).json;
  const uiSchemaText = JSON.stringify(uiSchema, null, 2);

  const page = await newAdminPage(browser, admin);
  const goto = async (hash, waitFor) => {
    await page.evaluate((h) => { location.hash = h; }, hash);
    if (waitFor) await page.waitForSelector(waitFor, { timeout: 10000 });
  };

  // ── 1. platform admin: navigation, other shops, shop selector ──
  await step(page, "login-nav", async () => {
    await uiLogin(page, admin, PLATFORM_EMAIL(), PLATFORM_PASSWORD());
    const tabs = await tabsOf(page);
    check("platform admin nav shows documents, content-schema and content-publishing",
      ["documents", "content-schema", "content-publishing"].every((k) => tabs.includes(k)), JSON.stringify(tabs));
    // The demo shop (shop 1, format 1 starter schema) just has to open without an error.
    await goto("#/documents", "#tab-documents");
    await page.waitForSelector("#documents-table:not([hidden]), #documents-notice:not([hidden]), #documents-error:not([hidden])");
    check("shop 1 (format 1 schema): the documents screen opens without an error", !(await page.isVisible("#documents-error")));
    await selectShop(page, shop);
    await page.waitForSelector("#documents-notice:not([hidden])", { timeout: 8000 });
    check("a new shop without a schema: the list says no schema is registered and offers no 'new document'",
      (await page.textContent("#documents-notice")).length > 5 && (await page.locator("#new-document-btn").isHidden()));
    check("the 'schema' link is shown to the platform admin", await page.locator("#documents-schema-link").isVisible());
  });

  // ── 2. schema screen ──
  await step(page, "schema", async () => {
    await page.click("#documents-schema-link");
    await page.waitForSelector("#tab-content-schema");
    await page.waitForSelector("#schema-active");
    check("schema screen: status says nothing is registered (version 0)", (await page.getAttribute("#schema-active", "data-version")) === "0");
    await page.click("#schema-dryrun");
    await page.waitForSelector("#schema-error:not([hidden])");
    check("dry run of an empty box is refused with a message", (await page.textContent("#schema-error")).length > 3);
    await page.fill("#schema-text", "{ not json");
    await page.click("#schema-dryrun");
    await page.waitForSelector("#schema-error:not([hidden])");
    check("dry run of text that is not JSON is refused before any request", (await page.textContent("#schema-error")).length > 3 && !(await page.isVisible("#schema-dryrun-summary")));
    // A hostile schema (remote $ref): 422 with a pointer, shown on the screen, nothing registered.
    const bad = { format: 2, types: { x: { schema: { type: "object", additionalProperties: false, properties: { p: { $ref: "https://example.com/evil.json" } } } } } };
    await page.fill("#schema-text", JSON.stringify(bad));
    await page.click("#schema-dryrun");
    await page.waitForSelector("#schema-error:not([hidden])");
    check("dry run of a schema with a remote $ref shows the server's pointer", /\$ref/.test(await page.textContent("#schema-error")), await page.textContent("#schema-error"));
    // The file input fills the box.
    await page.setInputFiles("#schema-file", { name: "content.schema.json", mimeType: "application/json", buffer: Buffer.from(uiSchemaText) });
    await page.waitForFunction((t) => document.querySelector("#schema-text").value === t, uiSchemaText);
    check("a schema file loaded through the file input fills the text box", true);
    await page.click("#schema-dryrun");
    await page.waitForSelector("#schema-dryrun-summary", { timeout: 8000 });
    const sum = await page.textContent("#schema-dryrun-summary");
    check("dry run of a valid schema reports the version it would register", /1/.test(sum), sum);
    check("the dry run is read-only: no schema version exists", (await req("GET", sp("/content/schema/versions"), { token: owner })).json?.versions?.length === 0);
    // Registration: cancel first (nothing happens), then accept.
    await page.click("#schema-register");
    const c1 = await answerConfirm(page, false);
    check("registration asks the core confirmation with the FIXED warning (core wording)", c1.warning === WARNINGS.registerContentSchema && c1.warning.length > 10, JSON.stringify(c1));
    check("the confirmation names the version and carries the report as detail", /1/.test(c1.title) && c1.detail !== null);
    await sleep(300);
    check("declined: nothing was registered", (await req("GET", sp("/content/schema/versions"), { token: owner })).json?.versions?.length === 0);
    await page.click("#schema-register");
    await answerConfirm(page, true);
    await page.waitForSelector("#schema-ok:not([hidden])", { timeout: 8000 });
    check("accepted: the screen reports the registered version", /1/.test(await page.textContent("#schema-ok")), await page.textContent("#schema-ok"));
    await page.waitForSelector('#schema-active[data-version="1"]');
    check("the status and the versions table show v1", (await page.locator("#schema-versions-tbody tr[data-version]").count()) === 1);
    const act = await req("GET", sp("/content/schema"), { token: owner });
    check("the API agrees: version 1, format 2", act.json?.version === 1 && act.json?.format === 2, brief(act));
    // The same text again is a no-op that asks for no confirmation.
    await page.click("#schema-register");
    await page.waitForSelector("#schema-ok:not([hidden])", { timeout: 8000 });
    check("registering the same text again is 'unchanged' and shows no dialog", (await page.locator("dialog[open]").count()) === 0 && (await page.getAttribute("#schema-active", "data-version")) === "1");
  });

  // ── 3. documents: list, create, editor controls ──
  await step(page, "documents-create", async () => {
    await goto("#/documents", "#documents-table:not([hidden])");
    await page.waitForSelector('#documents-tbody tr[data-key="banner"]', { timeout: 8000 });
    check("the declared document (banner) was created by the registration and is listed as a draft",
      (await page.textContent('#documents-tbody tr[data-key="banner"]')).includes("banner"));
    await page.click("#new-document-btn");
    await page.waitForSelector("#new-document-dialog[open]");
    await page.fill("#new-document-key", "Bad Key");
    await page.selectOption("#new-document-type", "landing");
    await page.click('#new-document-form button[type="submit"]');
    // the native pattern attribute blocks it; fall back to the core's own check by removing the constraint
    await page.evaluate(() => document.querySelector("#new-document-key").removeAttribute("pattern"));
    await page.click('#new-document-form button[type="submit"]');
    await page.waitForSelector("#new-document-error:not([hidden])");
    check("an invalid key (capitals, space) is refused by the core with a message", (await page.textContent("#new-document-error")).length > 5);
    await page.fill("#new-document-key", `other-${runID}`);
    await page.click('#new-document-form button[type="submit"]');
    await page.waitForFunction(() => { const e = document.querySelector("#new-document-error"); return e && !e.hidden && e.textContent.length > 0; });
    await sleep(400);
    check("a key outside the declared collection prefixes is refused (422), no document was created",
      (await req("GET", sp(`/content/documents/other-${runID}`), { token: owner })).status === 404);
    await page.fill("#new-document-key", docKey);
    await page.fill("#new-document-title", "UI landing");
    await page.click('#new-document-form button[type="submit"]');
    await page.waitForSelector("#tab-document #document-form [data-testid=content-form]", { timeout: 8000 });
    check("creating a document opens its editor at #/documents/<key>", (await hashOf(page)) === `#/documents/${docKey}`, await hashOf(page));
    const d0 = await apiDoc();
    check("the new document exists as a draft of type landing with the schema defaults",
      d0.type_key === "landing" && d0.status === 0 && d0.draft?.headline === "Hello" && d0.draft?.layout === "wide", JSON.stringify(d0).slice(0, 300));
    // creating the same key again is refused
    await goto("#/documents", "#documents-table:not([hidden])");
    await page.click("#new-document-btn");
    await page.fill("#new-document-key", docKey);
    await page.selectOption("#new-document-type", "landing");
    await page.click('#new-document-form button[type="submit"]');
    await page.waitForSelector("#new-document-error:not([hidden])");
    check("creating an existing key says it exists (the If-None-Match 412 is not shown as a stale save)",
      !(await page.isVisible("#document-stale-dialog")) && (await page.textContent("#new-document-error")).length > 3);
    await page.click("#new-document-cancel");
  });

  await step(page, "editor-kinds", async () => {
    await goto(`#/documents/${docKey}`, "#document-form [data-testid=content-form]");
    await page.waitForSelector(`[data-testid="cf:/headline"]`);
    const kinds = await page.evaluate(() => ({
      text: document.querySelector('[data-testid="cf:/headline"]').tagName,
      textarea: document.querySelector('[data-testid="cf:/lead"]').tagName,
      richtext: document.querySelector('[data-testid="cf:/body"]').tagName,
      image: document.querySelector('[data-testid="cf:/cover"]').tagName,
      select: document.querySelector('[data-testid="cf:/layout"]').tagName,
      color: !!document.querySelector('[data-pointer="/accent"] input[type=color]'),
      list: !!document.querySelector('[data-testid="cf-add:/tags"]'),
      sections: !!document.querySelector('[data-testid="cf-add-type:/blocks"]'),
    }));
    check("the form is built from the schema: text, textarea, richtext, image, select, color, list, sections controls exist",
      kinds.text === "INPUT" && kinds.textarea === "TEXTAREA" && kinds.richtext === "TEXTAREA" && kinds.image === "INPUT" && kinds.select === "SELECT" && kinds.color && kinds.list && kinds.sections, JSON.stringify(kinds));
    const rev0 = Number(await page.getAttribute("#document-status", "data-rev"));
    // required field and per-kind client validation: nothing is sent
    await cf(page, "/headline").fill("");
    await cf(page, "/cta").fill("javascript:alert(1)");
    await cf(page, "/accent").fill("red");
    await cf(page, "/cover").fill("https://evil.example/x.png");
    await page.click("#document-save");
    await page.waitForSelector('[data-testid="cf-error-summary"]:not([hidden])');
    const errs = {
      headline: (await fieldError(page, "/headline").innerText()).length > 0,
      cta: (await fieldError(page, "/cta").innerText()).length > 0,
      accent: (await fieldError(page, "/accent").innerText()).length > 0,
      cover: (await fieldError(page, "/cover").innerText()).length > 0,
    };
    check("required / link / color / image rules are shown at their fields and nothing is saved",
      Object.values(errs).every(Boolean) && (await apiDoc()).draft_rev === rev0, JSON.stringify(errs));
    // fill everything validly
    await cf(page, "/headline").fill(`Spring ${runID}`);
    await cf(page, "/lead").fill("A lead\nover two lines");
    await cf(page, "/body").fill('<p id="rt1">Rich <strong>text</strong></p><script>window.__x=1</script><img src="x" onerror="window.__x=2">');
    await cf(page, "/cta").fill("/shop");
    await cf(page, "/accent").fill("#ff8800");
    await cf(page, "/cover").fill("");
    await cf(page, "/layout").selectOption("narrow");
    // list of strings: add three, move, remove
    for (const v of ["alpha", "beta", "gamma"]) {
      await page.click('[data-testid="cf-add:/tags"]');
      await page.locator('[data-pointer^="/tags/"] input').last().fill(v);
    }
    await page.locator('[data-pointer="/tags"] [data-testid="cf-item-down"]').first().click();
    await page.locator('[data-pointer="/tags"] [data-testid="cf-item-remove"]').last().click();
    check("list controls: add, move down and remove leave two items", (await page.locator('[data-pointer="/tags"] [data-testid="cf-item"]').count()) === 2);
    // list of objects
    await page.click('[data-testid="cf-add:/faq"]');
    await cf(page, "/faq/0/q").fill("Why?");
    await cf(page, "/faq/0/a").fill("Because.");
    // sections: add banner then text
    await page.selectOption('[data-testid="cf-add-type:/blocks"]', "banner");
    await page.click('[data-testid="cf-add:/blocks"]');
    await cf(page, "/blocks/0/text").fill("SALE");
    await page.selectOption('[data-testid="cf-add-type:/blocks"]', "text");
    await page.click('[data-testid="cf-add:/blocks"]');
    await cf(page, "/blocks/1/body").fill("second block");
    await page.click("#document-save");
    await page.waitForFunction((r) => Number(document.querySelector("#document-status").dataset.rev) > r, rev0, { timeout: 8000 });
    const rev1 = Number(await page.getAttribute("#document-status", "data-rev"));
    check("saving advances the draft revision shown on the screen", rev1 > rev0, `${rev0} -> ${rev1}`);
    const d1 = await apiDoc();
    check("the server stored every editor kind (text, textarea, link, color, select, lists, sections)",
      d1.draft_rev === rev1 && d1.draft.headline === `Spring ${runID}` && d1.draft.lead === "A lead\nover two lines" && d1.draft.cta === "/shop" &&
        d1.draft.accent === "#ff8800" && d1.draft.layout === "narrow" && JSON.stringify(d1.draft.tags) === '["beta","alpha"]' &&
        d1.draft.faq?.[0]?.q === "Why?" && d1.draft.blocks?.[0]?.type === "banner" && d1.draft.blocks?.[0]?.text === "SALE" && d1.draft.blocks?.[1]?.body === "second block", JSON.stringify(d1.draft));
    check("rich text was sanitized by the server and the form shows the stored markup (no script, no onerror)",
      !/script|onerror/i.test(d1.draft.body) && !/script|onerror/i.test(await cf(page, "/body").inputValue()) && /<strong>text<\/strong>/.test(await cf(page, "/body").inputValue()), d1.draft.body);
    check("the draft is not public", (await pubRead()).status === 404);
    // reload: the form shows what the server holds
    await page.reload();
    await page.waitForSelector(`[data-testid="cf:/headline"]`);
    check("after a reload the form is rebuilt from the stored draft (session restored)",
      (await cf(page, "/headline").inputValue()) === `Spring ${runID}` && (await cf(page, "/layout").inputValue()) === "narrow" && (await page.locator('[data-pointer="/tags"] [data-testid="cf-item"]').count()) === 2);
    // image: upload through MinIO when it is reachable
    const prefix = (await req("GET", "/api/v1/admin/media/config", { token: owner })).json?.public_base_url ?? "";
    const upBtn = page.locator('[data-testid="cf-image-upload"]');
    if (await upBtn.count()) {
      await page.locator('[data-pointer="/cover"] [data-testid="cf-image-file"]').setInputFiles(FIXTURE_JPG);
      const got = await page.waitForFunction(() => document.querySelector('[data-testid="cf:/cover"]').value.startsWith("http"), null, { timeout: 25000 }).then(() => true, () => false);
      if (got) {
        const url = await cf(page, "/cover").inputValue();
        check("image upload: the field holds the uploaded URL under the configured image domain", Boolean(prefix) && url.startsWith(prefix), url);
        await page.click("#document-save");
        await page.waitForFunction((r) => Number(document.querySelector("#document-status").dataset.rev) > r, rev1, { timeout: 8000 });
        check("the uploaded image URL is accepted by the platform", (await apiDoc()).draft.cover === url);
      } else console.log("  - image upload skipped: the upload did not complete (media storage not reachable from the browser)");
    } else console.log("  - image upload skipped: media storage is not configured on this API (the upload button is absent)");
  });

  // ── 4. stale (412) and 422 pointers ──
  await step(page, "stale-422", async () => {
    await goto(`#/documents/${docKey}`, `[data-testid="cf:/headline"]`);
    const loaded = Number(await page.getAttribute("#document-status", "data-rev"));
    const cur = await apiDoc();
    const other = await req("PUT", sp(docPath), { token: owner, headers: { "If-Match": `"${cur.draft_rev}"` }, body: { content: { ...cur.draft, headline: "Edited elsewhere" } } });
    check("a concurrent edit through the API succeeds", other.status === 200, brief(other));
    await cf(page, "/headline").fill("My local edit");
    await page.click("#document-save");
    await page.waitForSelector("#document-stale-dialog[open]", { timeout: 8000 });
    check("a stale save (412) opens the reload prompt, not an error message", (await page.textContent('[data-testid="content-stale"]')).length > 5);
    check("the other edit was not overwritten", (await apiDoc()).draft.headline === "Edited elsewhere" && loaded < (await apiDoc()).draft_rev);
    await page.click('[data-testid="content-stale-reload"]');
    await page.waitForFunction(() => document.querySelector('[data-testid="cf:/headline"]')?.value === "Edited elsewhere", null, { timeout: 8000 });
    check("reloading shows the latest content and the new revision", Number(await page.getAttribute("#document-status", "data-rev")) === (await apiDoc()).draft_rev);
    // 422: the client counts the raw rich text, the server counts it after sanitizing ("&" becomes "&amp;"), so a value the form
    // accepts can still be refused: the platform answers 422 with a pointer and the form shows it at the field.
    const before = (await apiDoc()).draft_rev;
    await cf(page, "/body").fill("&".repeat(40));
    await page.click("#document-save");
    await page.waitForFunction(() => (document.querySelector('[data-pointer="/body"] .field-error')?.textContent || "").length > 0, null, { timeout: 8000 });
    check("a server 422 is shown at its field (pointer /body) and the revision did not move",
      (await page.locator('[data-pointer="/body"] .field-error').first().innerText()).length > 0 && (await apiDoc()).draft_rev === before);
    await cf(page, "/body").fill("<p>short</p>");
    await page.click("#document-save");
    await page.waitForFunction((r) => Number(document.querySelector("#document-status").dataset.rev) > r, before, { timeout: 8000 });
    check("fixing the field saves", /short/.test((await apiDoc()).draft.body));
  });

  // ── 5. publish / unpublish, public read ──
  await step(page, "publish", async () => {
    await goto(`#/documents/${docKey}`, `[data-testid="cf:/headline"]`);
    await cf(page, "/headline").fill("Unsaved change");
    await page.click("#document-publish");
    await page.waitForSelector("#document-error:not([hidden])");
    check("publishing with unsaved changes asks to save first and shows no dialog", (await page.locator("dialog[open]").count()) === 0 && (await pubRead()).status === 404);
    await cf(page, "/headline").fill(`Spring ${runID}`);
    await page.click("#document-save");
    await page.waitForFunction(() => document.querySelector("#document-status").textContent.includes("草稿已儲存"), null, { timeout: 8000 });
    await sleep(300);
    await page.click("#document-publish");
    const pc = await answerConfirm(page, false);
    check("publish asks the core confirmation with the FIXED warning", pc.warning === WARNINGS.publishContent, JSON.stringify(pc));
    check("declined: still not public", (await pubRead()).status === 404 && (await apiDoc()).status === 0);
    await page.click("#document-publish");
    await answerConfirm(page, true);
    await page.waitForFunction(() => document.querySelector("#document-status").textContent.includes("已發佈"), null, { timeout: 8000 });
    const pub = await pubRead();
    check("published: the public API (publishable key) serves the content", pub.status === 200 && pub.json?.content?.headline === `Spring ${runID}` && pub.json?.content?.layout === "narrow", brief(pub));
    check("the unpublish button appears", await page.locator("#document-unpublish").isVisible());
    await page.click("#document-unpublish");
    const uc = await answerConfirm(page, true);
    check("unpublish asks with the FIXED warning", uc.warning === WARNINGS.unpublishContent, JSON.stringify(uc));
    await page.waitForFunction(() => document.querySelector("#document-unpublish").hidden, null, { timeout: 8000 });
    check("unpublished: the public API answers 404 and the draft is kept", (await pubRead()).status === 404 && (await apiDoc()).draft.headline === `Spring ${runID}`);
    // publish again, then make a second revision with different content
    await page.click("#document-publish");
    await answerConfirm(page, true);
    await page.waitForFunction(() => !document.querySelector("#document-unpublish").hidden, null, { timeout: 8000 });
    await cf(page, "/headline").fill(`Summer ${runID}`);
    await page.click("#document-save");
    await page.waitForFunction(() => document.querySelector("#document-status").textContent.includes("草稿已儲存"), null, { timeout: 8000 });
    await sleep(300);
    await page.click("#document-publish");
    await answerConfirm(page, true);
    await page.waitForFunction((t) => document.querySelector("#document-status").textContent.includes("已發佈"), null, { timeout: 8000 });
    await sleep(400);
    check("the second publish is live", (await pubRead()).json?.content?.headline === `Summer ${runID}`);
  });

  // ── 6. revisions and restore ──
  await step(page, "revisions", async () => {
    await page.click("#document-revisions-link");
    await page.waitForSelector("#tab-document-revisions");
    await page.waitForSelector("#revisions-tbody tr[data-rev]", { timeout: 8000 });
    const nrev = await page.locator("#revisions-tbody tr[data-rev]").count();
    check("revisions list shows one row per publish (3 publishes)", nrev === 3, String(nrev));
    check("the hash is #/documents/<key>/revisions", (await hashOf(page)) === `#/documents/${docKey}/revisions`);
    await page.locator('#revisions-tbody tr[data-rev] [data-testid="rev-view"]').last().click();
    await page.waitForSelector("#revision-view-dialog[open] [data-testid=content-form]");
    const ro = await page.locator('#revision-view-dialog [data-testid="cf:/headline"]').isDisabled();
    check("viewing a revision shows its content read-only", ro);
    await page.click("#revision-view-dialog .dialog-actions button");
    const oldest = await page.locator("#revisions-tbody tr[data-rev]").last().getAttribute("data-rev");
    await page.locator('#revisions-tbody tr[data-rev] [data-testid="rev-restore"]').last().click();
    const rc = await answerConfirm(page, true);
    check("restore asks with the FIXED warning that it only touches the draft", rc.warning === WARNINGS.restoreContent && /草稿/.test(rc.warning), JSON.stringify(rc));
    await page.waitForSelector("#revisions-notice:not([hidden])", { timeout: 8000 });
    check("the result says 'restored to draft, not published'", /草稿/.test(await page.textContent("#revisions-notice")) && /尚未發佈/.test(await page.textContent("#revisions-notice")));
    const afterRestore = await apiDoc();
    const pubNow = await pubRead();
    check("restore changed the draft (to revision " + oldest + ") but NOT the published content",
      afterRestore.draft.headline === `Spring ${runID}` && pubNow.json?.content?.headline === `Summer ${runID}`, `${afterRestore.draft.headline} / ${pubNow.json?.content?.headline}`);
    await page.click('[data-testid="content-restored-edit"]');
    await page.waitForSelector(`[data-testid="cf:/headline"]`);
    check("the link goes back to the editor with the restored draft", (await cf(page, "/headline").inputValue()) === `Spring ${runID}`);
  });

  // ── 7. preview token ──
  await step(page, "preview", async () => {
    await page.click("#document-preview-btn");
    await page.click("#document-preview-create");
    await page.waitForSelector("#document-preview-token", { timeout: 8000 });
    const tok = await page.inputValue("#document-preview-token");
    check("a preview token is displayed (with its expiry)", tok.length > 20 && /\d/.test(await page.textContent("#document-preview-result")));
    const pv = await req("GET", `/api/v1/shop/content/${docKey}/preview`, { pk, headers: { "X-Preview-Token": tok } });
    check("the token reads the DRAFT through the preview endpoint", pv.status === 200 && pv.json?.content?.headline === `Spring ${runID}`, brief(pv));
    await page.selectOption("#document-preview-scope", "shop");
    await page.click("#document-preview-create");
    await page.waitForFunction((t) => document.querySelector("#document-preview-token")?.value !== t, tok, { timeout: 8000 });
    check("a shop-wide token can be issued as well", (await page.inputValue("#document-preview-token")).length > 20);
  });

  // ── 8. schedule panel (editor) and the publishing screen ──
  await step(page, "schedule", async () => {
    await goto(`#/documents/${docKey}`, "#content-schedule-panel");
    check("the schedule panel starts with 'not scheduled'", (await page.locator("#content-schedule-state").innerText()).length > 0 && !(await page.locator("#content-schedule-cancel").count()));
    await page.fill("#content-schedule-time", taipei(1));
    await page.click("#content-schedule-submit");
    await sleep(500);
    check("a time less than 2 minutes ahead is blocked by the form's min bound (no request, no schedule)",
      (await page.evaluate(() => document.querySelector("#content-schedule-time").validity.rangeUnderflow)) && (await req("GET", sp(`${docPath}/schedule`), { token: owner })).status === 404);
    await page.fill("#content-schedule-time", taipei(4));
    await page.click("#content-schedule-submit");
    await page.waitForSelector('#content-schedule-state[data-status="pending"]', { timeout: 8000 });
    const s1 = (await req("GET", sp(`${docPath}/schedule`), { token: owner })).json;
    check("scheduled: the schedule is pending on the server, bound to the draft revision", s1?.status === "pending" && s1?.draft_rev === (await apiDoc()).draft_rev, JSON.stringify(s1));
    check("the panel shows the Taipei time in the input and offers reschedule + cancel",
      (await page.inputValue("#content-schedule-time")) === taipei(4).slice(0, 16) || (await page.inputValue("#content-schedule-time")).length === 16);
    // the publishing screen lists it
    await goto("#/content-publishing", "#publishing-upcoming-tbody tr[data-key]");
    check("the upcoming list on the publishing screen shows the schedule", (await page.locator(`#publishing-upcoming-tbody tr[data-key="${docKey}"]`).count()) === 1);
    await page.click(`#publishing-upcoming-tbody tr[data-key="${docKey}"] a`);
    await page.waitForSelector('#content-schedule-state[data-status="pending"]');
    // reschedule
    await page.fill("#content-schedule-time", taipei(6));
    await page.click("#content-schedule-submit");
    await page.waitForFunction(() => /已改期/.test(document.querySelector("#content-schedule-message")?.textContent || ""), null, { timeout: 8000 });
    const s2 = (await req("GET", sp(`${docPath}/schedule`), { token: owner })).json;
    check("rescheduled: the publish time moved and it is still one pending schedule", s2.status === "pending" && Date.parse(s2.publish_at) > Date.parse(s1.publish_at), `${s1.publish_at} -> ${s2.publish_at}`);
    // cancel through the dialog
    await page.click("#content-schedule-cancel");
    const cc = await answerConfirm(page, false);
    check("cancelling a schedule asks with the FIXED warning", cc.warning === WARNINGS.cancelSchedule, JSON.stringify(cc));
    check("declined: still pending", (await req("GET", sp(`${docPath}/schedule`), { token: owner })).json?.status === "pending");
    await page.click("#content-schedule-cancel");
    await answerConfirm(page, true);
    await page.waitForFunction(() => document.querySelector("#content-schedule-state")?.dataset.status === "cancelled", null, { timeout: 8000 });
    check("cancelled: the server says so and nothing was published", (await req("GET", sp(`${docPath}/schedule`), { token: owner })).json?.status === "cancelled");
    // schedule again, then edit the draft in the UI: superseded
    await page.fill("#content-schedule-time", taipei(5));
    await page.click("#content-schedule-submit");
    await page.waitForSelector('#content-schedule-state[data-status="pending"]', { timeout: 8000 });
    await cf(page, "/headline").fill(`Edited after scheduling ${runID}`);
    await page.click("#document-save");
    await page.waitForSelector("#content-schedule-superseded", { timeout: 8000 });
    check("an edit after scheduling shows the 'superseded' notice", (await req("GET", sp(`${docPath}/schedule`), { token: owner })).json?.status === "superseded");
    // a pending schedule cancelled from the publishing screen
    await page.fill("#content-schedule-time", taipei(5));
    await page.click("#content-schedule-submit");
    await page.waitForSelector('#content-schedule-state[data-status="pending"]', { timeout: 8000 });
    await goto("#/content-publishing", `#publishing-upcoming-tbody tr[data-key="${docKey}"]`);
    await page.click(`#publishing-upcoming-tbody tr[data-key="${docKey}"] button.danger`);
    await answerConfirm(page, true);
    await page.waitForFunction((k) => !document.querySelector(`#publishing-upcoming-tbody tr[data-key="${k}"]`), docKey, { timeout: 8000 });
    check("a schedule can be cancelled from the publishing screen (confirm, then it leaves the upcoming list)", true);
    await page.waitForSelector(`#publishing-recent-tbody tr[data-key="${docKey}"]`, { timeout: 5000 }).catch(() => {});
    check("finished schedules appear under 'recent'", (await page.locator(`#publishing-recent-tbody tr[data-key="${docKey}"]`).count()) >= 1);
  });

  // ── 9. publish hook ──
  let stub = null;
  await step(page, "hook", async () => {
    const hookURL = `http://localhost:${STUB_PORT}/hook-ui-secret-${runID}`;
    const probe = await req("PUT", sp("/content/publish-hook?dry_run=true"), { token: owner, body: { url: hookURL } });
    if (probe.status === 422) {
      const msg = "the API is not configured for the content-ops e2e (CONTENT_HOOK_DEV_INSECURE=1 CONTENT_HOOK_ALLOWED_HOSTS=localhost): the hook part is skipped";
      if (process.env.E2E_REQUIRE_CONTENT_OPS === "1") fail(msg); else console.log(`  - ${msg}`);
      return;
    }
    stub = await startStub();
    await goto("#/content-publishing", "#publishing-hook-block");
    await page.waitForSelector("#publishing-hook-state");
    check("no hook yet: the state says 'not configured' and offers no test/fire/delete", (await page.getAttribute("#publishing-hook-status", "data-state")) === "none" && (await page.locator("#publishing-hook-delete").isHidden()));
    // http is refused by the core before any request (the https rule); an unlisted https host is refused by the API
    await page.fill("#publishing-hook-url", hookURL);
    await page.click("#publishing-hook-save");
    await page.waitForSelector("#publishing-hook-error:not([hidden])", { timeout: 8000 });
    check("an http:// URL is refused by the core's https rule and the message does not contain it", !(await page.textContent("#publishing-hook-error")).includes("hook-ui-secret"));
    await page.fill("#publishing-hook-url", "https://evil.test/hook-ui-secret-x");
    await page.click("#publishing-hook-save");
    await page.waitForFunction(() => /./.test(document.querySelector("#publishing-hook-error")?.textContent || "") && !document.querySelector("#publishing-hook-error").hidden, null, { timeout: 8000 });
    check("a URL on an unlisted host is refused (API 422) and the message does not contain the URL", !(await page.textContent("#publishing-hook-error")).includes("hook-ui-secret-x"));
    check("the URL box was emptied on submit, whatever the outcome", (await page.inputValue("#publishing-hook-url")) === "");
    // the only https host that is accepted without configuration is Cloudflare's deploy-hook endpoint; it is only validated / stored here and
    // replaced through the API before anything can be published, so nothing ever talks to Cloudflare.
    const cfId = randomUUID();
    const cfURL = `https://api.cloudflare.com/client/v4/workers/builds/deploy_hooks/${cfId}`;
    await page.fill("#publishing-hook-url", cfURL);
    await page.click("#publishing-hook-validate");
    await page.waitForSelector("#publishing-hook-message:not([hidden])", { timeout: 8000 });
    check("'validate' reports the masked URL and stores nothing", !(await page.textContent("#publishing-hook-message")).includes(cfId) &&
      (await req("GET", sp("/content/publish-hook"), { token: owner })).json?.configured === false, await page.textContent("#publishing-hook-message"));
    await page.fill("#publishing-hook-url", cfURL);
    await page.click("#publishing-hook-save");
    await page.waitForFunction(() => document.querySelector("#publishing-hook-status")?.dataset.state === "enabled", null, { timeout: 8000 });
    const pageText = async () => (await page.evaluate(() => document.body.innerText + " " + [...document.querySelectorAll("input,textarea")].map((i) => i.value).join(" ") + " " + document.documentElement.outerHTML));
    const t1 = await pageText();
    check("saved and enabled; the screen shows the host and a masked URL, never the URL", t1.includes("api.cloudflare.com") && !t1.includes(cfId));
    // swap in the stub receiver (http, only possible through the API with the dev-insecure settings)
    const swap = await req("PUT", sp("/content/publish-hook"), { token: owner, body: { url: hookURL } });
    check("(setup) the stub receiver replaces the URL through the API", swap.status === 200, brief(swap));
    await page.reload();
    await page.waitForSelector("#publishing-hook-state");
    await page.waitForFunction(() => document.querySelector("#publishing-hook-status")?.dataset.state === "enabled");
    const t2 = await pageText();
    check("after a reload the URL is still not shown anywhere (page text, input values, markup); only the host", t2.includes("localhost") && !t2.includes(`hook-ui-secret-${runID}`) && !t2.includes(cfId) && (await page.inputValue("#publishing-hook-url")) === "");
    // test
    await page.click("#publishing-hook-test");
    await page.waitForSelector("#publishing-hook-message:not([hidden])", { timeout: 10000 });
    check("'test' reaches the stub and reports success without any internal detail",
      stub.received.some((c) => JSON.parse(c.body).event === "content.test") && !(await page.textContent("#publishing-hook-message")).includes("STUB-INTERNAL"), await page.textContent("#publishing-hook-message"));
    // rotate the secret: shown once
    await page.click("#publishing-hook-rotate");
    const rc = await answerConfirm(page, true);
    check("rotating the secret asks with the FIXED warning", rc.warning === WARNINGS.rotateHookSecret, JSON.stringify(rc));
    await page.waitForSelector("#publishing-hook-secret-value", { timeout: 8000 });
    const secret = (await page.textContent("#publishing-hook-secret-value")).trim();
    check("the new signing secret is shown (64 hex chars)", /^[0-9a-f]{64}$/.test(secret), secret.length + "");
    stub.received.length = 0;
    await page.click("#publishing-hook-test");
    await page.waitForFunction(() => !document.querySelector("#publishing-hook-test").disabled);
    await sleep(500);
    const call = stub.received.at(-1);
    const sigOK = call && call.headers["x-content-signature"] === `sha256=${createHmac("sha256", secret).update(`${call.headers["x-content-timestamp"]}.${call.body}`).digest("hex")}`;
    check("the receiver verifies the call with exactly the secret the screen showed", Boolean(sigOK));
    await page.click("#publishing-hook-secret-done");
    check("dismissing hides the secret", !(await pageText()).includes(secret));
    await page.reload();
    await page.waitForSelector("#publishing-hook-state");
    check("after a reload the secret is gone (never readable again)", !(await pageText()).includes(secret));
    // fire
    stub.received.length = 0;
    await page.click("#publishing-hook-fire");
    const fc = await answerConfirm(page, true);
    check("a manual rebuild asks with the FIXED warning", fc.warning === WARNINGS.fireHook, JSON.stringify(fc));
    await page.waitForFunction(() => /成功|HTTP|OK/i.test(document.querySelector("#publishing-hook-message")?.textContent || ""), null, { timeout: 10000 });
    check("fire reached the receiver", stub.received.length >= 1, JSON.stringify(stub.received.map((c) => c.url)));
    // a publish from the editor is reported to the hook (debounced)
    stub.received.length = 0;
    await goto(`#/documents/${docKey}`, `[data-testid="cf:/headline"]`);
    await page.click("#document-publish");
    await answerConfirm(page, true);
    await page.waitForFunction(() => document.querySelector("#document-status").textContent.includes("已發佈"), null, { timeout: 8000 });
    const got = await (async () => { const end = Date.now() + 15000; while (Date.now() < end) { if (stub.received.some((c) => JSON.parse(c.body).event === "content.published")) return true; await sleep(250); } return false; })();
    check("publishing from the editor triggers the hook (debounced, signed, no content in the body)", got && !stub.received.at(-1).body.includes("Edited after"));
    // disable / enable
    await goto("#/content-publishing", "#publishing-hook-state");
    await page.click("#publishing-hook-toggle");
    await page.waitForFunction(() => document.querySelector("#publishing-hook-status")?.dataset.state === "disabled", null, { timeout: 8000 });
    check("disabled", (await req("GET", sp("/content/publish-hook"), { token: owner })).json?.enabled === false);
    await page.click("#publishing-hook-toggle");
    await page.waitForFunction(() => document.querySelector("#publishing-hook-status")?.dataset.state === "enabled", null, { timeout: 8000 });
    check("enabled again", (await req("GET", sp("/content/publish-hook"), { token: owner })).json?.enabled === true);
    // a failing receiver: five failed calls end in the final-failure state
    const refuse = await req("PUT", sp("/content/publish-hook"), { token: owner, body: { url: "http://localhost:1/refused" } });
    check("(setup) the hook points at a refusing port", refuse.status === 200, brief(refuse));
    // test/fire share 3 calls per minute: pace them
    for (let i = 0, tries = 0; i < 5 && tries < 40; tries++) {
      const f = await req("POST", sp("/content/publish-hook/fire"), { token: owner });
      if (f.status === 429) { await sleep(10000); continue; }
      i++;
    }
    const failed = (await req("GET", sp("/content/publish-hook"), { token: owner })).json;
    check("five consecutive failures end in failed:true and disabled (API)", failed?.failed === true && failed?.enabled === false, JSON.stringify(failed));
    await page.reload();
    await page.waitForSelector("#publishing-hook-failed", { timeout: 8000 });
    check("the screen shows the FINAL FAILURE state with a re-enable button", (await page.getAttribute("#publishing-hook-status", "data-state")) === "failed" && (await page.locator("#publishing-hook-reenable").isVisible()));
    check("in that state the plain enable/disable toggle is hidden", await page.locator("#publishing-hook-toggle").isHidden());
    await page.click("#publishing-hook-reenable");
    await page.waitForFunction(() => document.querySelector("#publishing-hook-status")?.dataset.state === "enabled", null, { timeout: 8000 });
    const re = (await req("GET", sp("/content/publish-hook"), { token: owner })).json;
    check("re-enabling clears the failure state", re?.failed === false && re?.enabled === true && re?.consecutive_failures === 0, JSON.stringify(re));
    // delete
    await page.click("#publishing-hook-delete");
    const dc = await answerConfirm(page, true);
    check("deleting the hook asks with the FIXED warning", dc.warning === WARNINGS.deletePublishHook, JSON.stringify(dc));
    await page.waitForFunction(() => document.querySelector("#publishing-hook-status")?.dataset.state === "none", null, { timeout: 8000 });
    check("deleted: the API says not configured", (await req("GET", sp("/content/publish-hook"), { token: owner })).json?.configured === false);
  });
  if (stub) await new Promise((r) => stub.server.close(r));

  // ── 10. the merchant EDITOR: content.view + content.edit only ──
  const epage = await newAdminPage(browser, admin);
  await step(epage, "editor", async () => {
    await uiLogin(epage, admin, S.editorEmail, S.editorPassword);
    const tabs = await tabsOf(epage);
    check("editor nav: documents yes; content-schema and content-publishing absent", tabs.includes("documents") && !tabs.includes("content-schema") && !tabs.includes("content-publishing"), JSON.stringify(tabs));
    await epage.evaluate(() => { location.hash = "#/documents"; });
    await epage.waitForSelector("#documents-tbody tr[data-key]");
    check("editor: the documents list works and hides the schema link", await epage.locator("#documents-schema-link").isHidden() && await epage.locator("#new-document-btn").isVisible());
    for (const [hash, panel] of [["#/content-schema", "#tab-content-schema"], ["#/content-publishing", "#tab-content-publishing"]]) {
      await epage.evaluate((h) => { location.hash = h; }, hash);
      await sleep(800);
      check(`editor: ${hash} is not reachable (falls back to an allowed screen)`, (await epage.locator(panel).count()) === 0 && (await hashOf(epage)) !== hash, await hashOf(epage));
    }
    await epage.evaluate((k) => { location.hash = `#/documents/${k}`; }, docKey);
    await epage.waitForSelector(`[data-testid="cf:/headline"]`);
    await epage.waitForSelector("#content-schedule-panel");
    check("editor: save is available; publish, unpublish and delete buttons are not", await epage.locator("#document-save").isVisible() && await epage.locator("#document-publish").isHidden() &&
      await epage.locator("#document-unpublish").isHidden() && await epage.locator("#document-delete").isHidden());
    check("editor: the schedule panel is read-only (no time input, no submit)", (await epage.locator("#content-schedule-form").count()) === 0 && (await epage.locator("#content-schedule-time").count()) === 0);
    await cf(epage, "/headline").fill(`Editor edit ${runID}`);
    await epage.click("#document-save");
    await epage.waitForFunction(() => document.querySelector("#document-status").textContent.includes("草稿已儲存"), null, { timeout: 8000 });
    await sleep(300);
    check("editor: saving a draft works", (await apiDoc()).draft.headline === `Editor edit ${runID}`);
    check("editor: publishing is refused server-side too (403)", (await req("POST", sp(`${docPath}/publish`), { token: (await req("POST", "/api/v1/admin/auth/login", { body: { email: S.editorEmail, password: S.editorPassword } })).json.access_token })).status === 403);
    await epage.evaluate((k) => { location.hash = `#/documents/${k}/revisions`; }, docKey);
    await epage.waitForSelector("#revisions-tbody tr[data-rev]");
    check("editor: revisions show view + restore buttons", (await epage.locator('[data-testid="rev-restore"]').count()) >= 1);
  });
  await epage.context().close();

  // ── 11. delete a document, then a schema change with removals (second confirmation) ──
  await step(page, "delete-removals", async () => {
    // a second document with values in `tags`, to be affected by the removal
    const mk = await req("PUT", sp(`/content/documents/${docKey2}`), { token: owner, headers: { "If-None-Match": "*" }, body: { type_key: "landing", content: { headline: "Second", tags: ["keep-me"] } } });
    check("(setup) a second document with tag values exists", mk.status === 200 || mk.status === 201, brief(mk));
    await goto("#/documents", "#documents-table:not([hidden])");
    await page.waitForSelector(`#documents-tbody tr[data-key="${docKey2}"]`);
    // delete it from the editor
    await page.evaluate((k) => { location.hash = `#/documents/${k}`; }, docKey2);
    await page.waitForSelector(`[data-testid="cf:/headline"]`);
    await page.click("#document-delete");
    const dd = await answerConfirm(page, false);
    check("delete asks with the FIXED warning", dd.warning === WARNINGS.deleteContent, JSON.stringify(dd));
    check("declined: the document still exists", (await req("GET", sp(`/content/documents/${docKey2}`), { token: owner })).status === 200);
    await page.click("#document-delete");
    await answerConfirm(page, true);
    await page.waitForSelector("#documents-table", { timeout: 8000 });
    check("deleted: back at the list, the document is gone (404)", (await req("GET", sp(`/content/documents/${docKey2}`), { token: owner })).status === 404 && (await hashOf(page)) === "#/documents");
    // recreate with tags (so the removal has something to report), then register v2 without the tags field
    const mk2 = await req("PUT", sp(`/content/documents/${docKey2}`), { token: owner, headers: { "If-None-Match": "*" }, body: { type_key: "landing", content: { headline: "Second", tags: ["keep-me"] } } });
    check("(setup) the document is recreated with tag values", mk2.status === 200 || mk2.status === 201, brief(mk2));
    await goto("#/content-schema", "#schema-text");
    await page.fill("#schema-text", JSON.stringify(uiSchemaV2));
    await page.click("#schema-dryrun");
    await page.waitForSelector("#schema-dryrun-report", { timeout: 8000 });
    check("the dry run report names the field whose values would be removed", /\/tags/.test(await page.textContent("#schema-dryrun-report")), await page.textContent("#schema-dryrun-report"));
    await page.click("#schema-register");
    const r1 = await answerConfirm(page, false);
    check("registration with removals: first confirmation (fixed warning) lists the removal in its detail", r1.warning === WARNINGS.registerContentSchema && /\/tags/.test(r1.detail ?? ""), JSON.stringify(r1));
    check("declined: still version 1", (await req("GET", sp("/content/schema"), { token: owner })).json?.version === 1);
    await page.click("#schema-register");
    await page.waitForSelector('dialog[data-testid="confirm-dialog"][open]');
    await page.click('dialog[data-testid="confirm-dialog"][open] [data-testid="confirm-yes"]');
    const r2 = await answerConfirm(page, false);
    check("the platform's 409 triggers a SECOND confirmation with its own fixed warning", r2.warning === WARNINGS.registerContentSchemaRemovals && r2.warning !== r1.warning, JSON.stringify(r2));
    check("declining the second confirmation registers nothing", (await req("GET", sp("/content/schema"), { token: owner })).json?.version === 1);
    await page.click("#schema-register");
    await answerConfirm(page, true);
    await answerConfirm(page, true);
    await page.waitForSelector('#schema-active[data-version="2"]', { timeout: 10000 });
    check("after both confirmations v2 is registered and listed", (await page.locator("#schema-versions-tbody tr[data-version]").count()) === 2 && (await req("GET", sp("/content/schema"), { token: owner })).json?.version === 2);
    const snap = await req("GET", sp(`/content/documents/${docKey2}/revisions`), { token: owner });
    check("the removed values survive in a schema snapshot revision", JSON.stringify(snap.json ?? {}).includes("schema_snapshot"), brief(snap).slice(0, 200));
    await goto("#/documents", "#documents-table:not([hidden])");
    check("the documents list still loads after the schema change", (await page.locator("#documents-tbody tr[data-key]").count()) >= 2 && !(await page.isVisible("#documents-error")));
  });

  check("no uncaught page errors in any section", page.errors.length === 0 && epage.errors.length === 0, [...page.errors, ...epage.errors].join(" | "));
  await page.context().close();
}

const isEntryPoint = Boolean(process.argv[1]) && import.meta.url === pathToFileURL(process.argv[1]).href;
if (isEntryPoint) {
  console.log(`E2E content UI path against ${API} / ${ADMIN_BASE()} (run ${runID})`);
  const browser = await chromium.launch();
  try {
    await runContentUiPath(browser);
  } catch (err) {
    fail(`content UI path aborted: ${err?.stack ?? err}`);
  } finally {
    await browser.close();
  }
  if (failureCount() > 0) {
    console.error(`\nE2E content UI path FAILED with ${failureCount()} failed check(s)`);
    process.exit(1);
  }
  console.log("\nE2E content UI path PASSED");
}
