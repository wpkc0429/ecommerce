#!/usr/bin/env node
// Offline tests of the content-store core (no network, no browser, no dependencies). Run: node scripts/test-content.mjs
// Adapted from merchant-template v0.3.0 scripts/test-content.mjs. A stubbed fetch records every request, so the tests prove:
//   * exact request headers (If-Match quoting, If-None-Match: *), 412 / 428 / 422 mapping;
//   * permission gating inside every function (staff, editor), before any request or dialog;
//   * the dry run only ever sends ?dry_run=true;
//   * registerContentSchema sends NO registering request unless the (injected) human confirmation resolved true, ignores
//     extra arguments, and needs a second confirmation before confirm_removals;
//   * the locked validation (required, link scheme, image domain, color, array limits, block unions) and its use before a save.
// Copy is not loaded here, so ActionError messages equal their copy keys (t() returns the key for a missing entry).
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const g = globalThis;
const mem = new Map();
const storage = { getItem: (k) => (mem.has(k) ? mem.get(k) : null), setItem: (k, v) => mem.set(k, String(v)), removeItem: (k) => mem.delete(k) };
const define = (k, v) => Object.defineProperty(g, k, { value: v, writable: true, configurable: true, enumerable: true });
define("localStorage", storage);
define("sessionStorage", storage);
define("location", { hostname: "localhost", origin: "http://localhost:3001", hash: "", href: "http://localhost:3001/", reload() {} });
define("window", { scrollTo() {}, addEventListener() {}, open: () => null });
define("document", { createElement: () => ({}), body: {}, head: {} });
define("history", { replaceState() {} });
const imp = (p) => import(pathToFileURL(path.join(ROOT, p)).href);

let pass = 0, fail = 0;
const ok = (name, cond, extra = "") => { if (cond) pass++; else { fail++; console.error(`  FAIL ${name} ${extra}`); } };
const rejects = async (fn) => { try { await fn(); return null; } catch (e) { return e; } };

// ── fetch stub ──
const calls = [];
let routes = [];
g.fetch = async (url, init = {}) => {
  const u = String(url);
  let body;
  try { body = init.body ? JSON.parse(init.body) : undefined; } catch { body = init.body; }
  const rec = { method: init.method || "GET", url: u, headers: init.headers || {}, body, rawBody: init.body };
  calls.push(rec);
  const r = routes.find((x) => x.method === rec.method && (x.match instanceof RegExp ? x.match.test(u) : u.includes(x.match)));
  if (!r) return new Response(JSON.stringify({ error: { code: "not_found", message: `no stub for ${rec.method} ${u}` } }), { status: 404, headers: { "content-type": "application/json" } });
  const out = typeof r.reply === "function" ? r.reply(rec) : r.reply;
  return new Response(out.status === 204 ? null : (out.body === undefined ? "" : JSON.stringify(out.body)), { status: out.status || 200, headers: { "content-type": "application/json", ...(out.headers || {}) } });
};
const on = (method, match, reply) => routes.unshift({ method, match, reply });
const reset = () => { calls.length = 0; routes = []; };
const puts = () => calls.filter((c) => c.method === "PUT" && /\/content\/schema/.test(c.url));

// ── load the real modules ──
const { tokens } = await imp("lib/api.js");
const { session } = await imp("lib/session.js");
const { loadPermissions } = await imp("lib/rbac.js");
const C = await imp("lib/content.js");
const S = await imp("lib/content-schema.js");
const CA = await imp("core/content/actions.js");
const { ROUTES, NAV } = await imp("core/routes.js");
const meta = await imp("lib/meta/index.js");
const { fixedWarning } = await imp("core/warnings.js");
const contentGroup = (await imp("core/groups/content.js")).default;
const copy = (await imp("design-defaults/copy/content.js")).default;
const origWarn = console.warn;
console.warn = () => {};
tokens.set({ access_token: "test-token" });
session.shopID = 1;

const asked = [];
let answers = [];
const actions = CA.createContentActions({ ask: async (id, params, opts) => { asked.push({ id, params, opts }); return answers.length ? answers.shift() : true; } });
const reads = CA.reads;
const perms = { staff: [], editor: ["content.view", "content.edit"], owner: ["content.view", "content.edit", "content.publish", "content.manage"] };
async function as(who) {
  routes = [{ method: "GET", match: "/me", reply: { body: { permissions: perms[who] } } }];
  await loadPermissions(1);
  reset();
}

const schemaNode = {
  type: "object", additionalProperties: false, required: ["title", "sections"],
  properties: {
    title: { type: "string", maxLength: 20, minLength: 1, default: "x", "x-editor-order": 0 },
    link: { type: "string", maxLength: 200, default: "", "x-editor": "link" },
    img: { type: "string", maxLength: 200, default: "", "x-editor": "image" },
    color: { type: "string", maxLength: 7, default: "#000000", "x-editor": "color" },
    tag: { type: "string", maxLength: 10, default: "a", enum: ["a", "b"], "x-editor": "select" },
    items: { type: "array", maxItems: 2, items: { type: "string", maxLength: 5, default: "" }, default: [] },
    sections: { type: "array", maxItems: 3, default: [], "x-editor": "sections", items: { oneOf: [
      { type: "object", additionalProperties: false, required: ["type", "text"], properties: { type: { const: "hero" }, text: { type: "string", maxLength: 10, default: "" } } },
    ] } },
  },
};
const goodContent = { title: "Hi", link: "/a", img: "", color: "#aabbcc", tag: "a", items: ["x"], sections: [{ type: "hero", text: "t" }] };
const docBody = (over = {}) => ({ key: "home", type_key: "home", title: "Home", status: 0, needs_attention: false, declared: true, draft_rev: 4, published_rev: 0, schema_version: 1, published_schema_version: 0, updated_at: "2026-11-25T00:00:00Z", published_at: null, updated_by_user_id: 1, draft: goodContent, published: null, meta: {}, schema_node: schemaNode, type_title: "Home", ...over });
const etagOK = (rev) => C.etagOf(rev) === `"${rev}"`;

// ── 1. header helpers ──
ok("etagOf quotes the revision", etagOK(3) && C.etagOf("7") === '"7"');
ok("KEY_RE accepts valid keys only", ["home", "a-b", "x1"].every((k) => C.KEY_RE.test(k)) && ["", "-a", "A", "a_b", "a/b", "..", "x".repeat(64)].every((k) => !C.KEY_RE.test(k)));

// ── 2. request shape ──
await as("owner");
on("GET", "/content/documents/home", { body: docBody() });
on("PUT", "/content/documents/home", () => ({ status: 200, headers: { ETag: '"5"' }, body: docBody({ draft_rev: 5 }) }));
let res = await actions.saveContentDocument("home", goodContent, { rev: 4 });
const put1 = calls.find((c) => c.method === "PUT");
ok("update sends If-Match with the quoted loaded revision and no If-None-Match", put1.headers["If-Match"] === '"4"' && !("If-None-Match" in put1.headers), JSON.stringify(put1.headers));
ok("the stored document comes back (draft_rev from the body), ready to rebuild the form", res.ok && res.doc.rev === 5 && res.doc.draft.title === "Hi");
ok("the body carries only content (no title, no meta)", JSON.stringify(Object.keys(put1.body)) === '["content"]');
ok("the request carries the bearer token", put1.headers.Authorization === "Bearer test-token");
reset();
let e = await rejects(() => actions.saveContentDocument("home", goodContent, {}));
ok("an update without a revision is refused before any request (428 can never be our doing)", e && e.kind === "internal" && calls.length === 0);
e = await rejects(() => C.saveDocument(1, "home", { content: {} }, {}));
ok("lib refuses to PUT without a precondition", e && /revision is required/.test(e.message) && calls.length === 0);
e = await rejects(() => actions.saveContentDocument("../x", goodContent, { rev: 1 }));
ok("an invalid key never reaches the network", e && e.status === 404 && calls.length === 0);
const huge = { ...goodContent, title: "x".repeat(C.MAX_CONTENT_BYTES + 1) };
e = await rejects(() => actions.saveContentDocument("home", huge, { rev: 1 }));
ok("content over 128 KiB is refused locally (limit) before any request", e && e.kind === "limit" && calls.length === 0);

// create
reset();
on("GET", "/content/schema", { body: { version: 1, format: 2, sha256: "x", source: "registered", created_at: "2026-11-25T00:00:00Z", has_snapshot: false, schema: { format: 2, types: { campaign: { title: "C", schema: schemaNode } } } } });
on("PUT", "/content/documents/campaign-a", { status: 201, headers: { ETag: '"1"' }, body: docBody({ key: "campaign-a", draft_rev: 1 }) });
res = await actions.createContentDocument({ key: "campaign-a", typeKey: "campaign", title: "A" });
const cput = calls.find((c) => c.method === "PUT");
ok("create sends If-None-Match: * and no If-Match, the type key, a title and the type's default content", cput.headers["If-None-Match"] === "*" && !("If-Match" in cput.headers) && cput.body.type_key === "campaign" && cput.body.title === "A" && cput.body.content.title === "x" && res.key === "campaign-a", JSON.stringify(cput.body));
reset();
on("GET", "/content/schema", { body: { version: 1, format: 2, sha256: "x", source: "registered", created_at: "2026-11-25T00:00:00Z", schema: { format: 2, types: { campaign: { title: "C", schema: schemaNode } } } } });
on("PUT", "/content/documents/campaign-a", { status: 412, body: { error: { code: "precondition_failed", message: "exists" } } });
e = await rejects(() => actions.createContentDocument({ key: "campaign-a", typeKey: "campaign" }));
ok("412 on create means the key exists: a state error, not a stale reload prompt", e && e.kind === "state" && e.reason === "exists");
reset();
e = await rejects(() => actions.createContentDocument({ key: "Bad Key", typeKey: "campaign" }));
ok("create refuses a bad key before any request", e && e.kind === "invalid" && calls.length === 0);
reset();
on("GET", "/content/schema", { body: { version: 1, format: 2, sha256: "x", source: "registered", created_at: "2026-11-25T00:00:00Z", schema: { format: 2, types: {} } } });
e = await rejects(() => actions.createContentDocument({ key: "x", typeKey: "nope" }));
ok("create refuses an unknown type and PUTs nothing", e && e.kind === "invalid" && calls.every((c) => c.method === "GET"));

// ── 3. error mapping ──
reset();
on("GET", "/content/documents/home", { body: docBody() });
on("PUT", "/content/documents/home", { status: 412, body: { error: { code: "precondition_failed", message: "stale", details: { current_rev: 9 } } } });
e = await rejects(() => actions.saveContentDocument("home", goodContent, { rev: 4 }));
ok("412 -> ActionError kind stale (with the current revision), status 412", e && e.isActionError && e.kind === "stale" && e.status === 412 && e.currentRev === 9, JSON.stringify(e));
reset();
on("GET", "/content/documents/home", { body: docBody() });
on("PUT", "/content/documents/home", { status: 428, body: { error: { code: "precondition_required", message: "need If-Match" } } });
const origErr = console.error; console.error = () => {};
e = await rejects(() => actions.saveContentDocument("home", goodContent, { rev: 4 }));
console.error = origErr;
ok("428 -> kind internal (never shown as a user error)", e && e.kind === "internal" && e.status === 500);
reset();
on("GET", "/content/documents/home", { body: docBody() });
on("PUT", "/content/documents/home", { status: 422, body: { error: { code: "validation_failed", message: "invalid", details: [{ pointer: "/title", message: "too long" }, { pointer: "/sections/0/text", message: "bad" }] } } });
e = await rejects(() => actions.saveContentDocument("home", goodContent, { rev: 4 }));
ok("422 -> kind invalid with JSON-pointer fields", e && e.kind === "invalid" && e.fields["/title"] && e.fields["/sections/0/text"], JSON.stringify(e?.fields));
ok("a leading /content in a pointer is tolerated", S.mapServerDetails([{ pointer: "/content/title", message: "x" }], schemaNode)["/title"] !== undefined);
ok("a field called content is not stripped", S.mapServerDetails([{ pointer: "/content/title", message: "x" }], { properties: { content: {} } })["/content/title"] !== undefined);
reset();
on("GET", "/content/documents/home", { body: docBody() });
on("PUT", "/content/documents/home", { status: 429, body: { error: { code: "rate_limited", message: "slow" } } });
e = await rejects(() => actions.saveContentDocument("home", goodContent, { rev: 4 }));
ok("429 -> kind limit", e && e.kind === "limit" && e.status === 429);
reset();
on("GET", "/content/documents/home", { body: docBody() });
on("PUT", "/content/documents/home", { status: 403, body: { error: { code: "forbidden", message: "no" } } });
e = await rejects(() => actions.saveContentDocument("home", goodContent, { rev: 4 }));
ok("403 -> permission ActionError", e && e.kind === "permission");

// ── 4. validation runs in the core before any save ──
reset();
on("GET", "/content/documents/home", { body: docBody() });
on("PUT", "/content/documents/home", { status: 200, body: docBody() });
const bad = { ...goodContent, title: "", link: "javascript:alert(1)", img: "http://evil.example/x.png", color: "red", tag: "z", items: ["a", "b", "c"], sections: [{ type: "nope" }] };
e = await rejects(() => actions.saveContentDocument("home", bad, { rev: 4 }));
ok("invalid values are refused locally with every field named, and nothing is PUT", e && e.kind === "invalid" && calls.every((c) => c.method !== "PUT")
  && ["/title", "/link", "/img", "/color", "/tag", "/items", "/sections/0"].every((p) => e.fields[p]), JSON.stringify(e?.fields));
const missing = { ...goodContent }; delete missing.title;
e = await rejects(() => actions.saveContentDocument("home", missing, { rev: 4 }));
ok("a required field cannot be omitted from the saved content", e && e.kind === "invalid" && e.fields["/title"] && calls.every((c) => c.method !== "PUT"));
ok("validate: good content passes", Object.keys(S.validate(schemaNode, goodContent, { root: schemaNode })).length === 0);
ok("editor kinds are the ones the platform validator allows", JSON.stringify(S.EDITOR_KINDS) === JSON.stringify(["text", "textarea", "richtext", "image", "link", "color", "select", "list", "sections"]));
ok("link: https, http, mailto, tel, /path and #anchor pass; javascript:, data:, //host and spaces fail", ["https://a.b/c", "http://a.b", "mailto:a@b.c", "tel:+886", "/x", "#y", ""].every(S.linkOK) && ["javascript:x", "data:text/html,x", "//evil.example", "https:// a", "ftp://a.b"].every((v) => !S.linkOK(v)));
ok("image: only under the configured image domain (with the trailing slash), or empty", S.imageOK("https://img.example.com/a.webp", "https://img.example.com") && !S.imageOK("https://img.example.com.evil.test/a.webp", "https://img.example.com") && S.imageOK("", "https://img.example.com") && !S.imageOK("http://img.example.com/a", "https://img.example.com"));
ok("color: #RRGGBB only", S.colorOK("#a1B2c3") && !S.colorOK("#abc") && !S.colorOK("blue"));
ok("requiredPointers lists required properties, recursively through required objects", JSON.stringify(S.requiredPointers({ type: "object", required: ["a"], properties: { a: { type: "object", required: ["b"], properties: { b: { type: "string" }, c: { type: "string" } } }, d: { type: "string" } } })) === '["/a","/a/b"]');
ok("nodeAt resolves nested pointers and $ref", S.nodeAt({ type: "object", $defs: { x: { type: "string", title: "T" } }, properties: { a: { $ref: "#/$defs/x" } } }, "/a")?.title === "T");
ok("kindOf: every x-editor hint maps to its editor kind", ["textarea", "richtext", "image", "link", "color"].every((k) => S.kindOf({ type: "string", "x-editor": k }, {}) === k) && S.kindOf({ type: "string", enum: ["a"] }, {}) === "select" && S.kindOf({ type: "array", items: { type: "string" } }, {}) === "list" && S.kindOf(schemaNode.properties.sections, schemaNode) === "sections");
ok("hydrate fills missing required properties with defaults", S.hydrate(schemaNode, { title: "k" }).sections.length === 0 && S.hydrate(schemaNode, {}).title === "x");
ok("hydrate drops keys the schema does not declare (a legacy draft stays savable)", !("legacy" in S.hydrate(schemaNode, { title: "k", legacy: [1] })) && S.hydrate({ type: "object", properties: { a: { type: "object", properties: { b: { type: "string", default: "" } } } } }, { a: { b: "1", c: 2 } }).a.c === undefined);
ok("typeNode reads format 2 ({title, schema}) and format 1 (the node itself, global separate)", CA.typeNode({ schema: { types: { a: { title: "A", schema: schemaNode } } } }, "a") === schemaNode
  && CA.typeNode({ schema: { format: 1, types: { home: schemaNode }, global: { type: "object" } } }, "home") === schemaNode
  && CA.typeNode({ schema: { format: 1, types: {}, global: { type: "object" } } }, "global").type === "object"
  && CA.typeNode({ schema: { types: {} } }, "x") === null);

// ── 5. permission gating (inside every function, before any request) ──
const gated = [
  ["reads.types", () => reads.types(), "staff"],
  ["reads.documents", () => reads.documents(), "staff"],
  ["reads.document", () => reads.document("home"), "staff"],
  ["reads.revisions", () => reads.revisions("home"), "staff"],
  ["reads.revision", () => reads.revision("home", 1), "staff"],
  ["reads.schemaStatus (editor)", () => reads.schemaStatus(), "editor"],
  ["reads.dryRunSchema (editor)", () => reads.dryRunSchema("{}"), "editor"],
  ["reads.dryRunSchema (staff)", () => reads.dryRunSchema("{}"), "staff"],
  ["saveContentDocument (staff)", () => actions.saveContentDocument("home", goodContent, { rev: 1 }), "staff"],
  ["createContentDocument (staff)", () => actions.createContentDocument({ key: "x", typeKey: "campaign" }), "staff"],
  ["publishContentDocument (editor)", () => actions.publishContentDocument("home", { rev: 1 }), "editor"],
  ["unpublishContentDocument (editor)", () => actions.unpublishContentDocument("home"), "editor"],
  ["deleteContentDocument (editor)", () => actions.deleteContentDocument("home"), "editor"],
  ["restoreContentRevision (staff)", () => actions.restoreContentRevision("home", 1), "staff"],
  ["uploadContentImage (staff)", () => actions.uploadContentImage({}), "staff"],
  ["createContentPreviewToken (staff)", () => actions.createContentPreviewToken("home"), "staff"],
  ["readContentSchemaFile (editor)", () => actions.readContentSchemaFile({ size: 1, text: async () => "{}" }), "editor"],
  ["registerContentSchema (editor)", () => actions.registerContentSchema("{}"), "editor"],
  ["registerContentSchema (staff)", () => actions.registerContentSchema("{}"), "staff"],
];
for (const [name, fn, who] of gated) {
  await as(who); asked.length = 0;
  const err = await rejects(fn);
  ok(`gated: ${name} is refused for ${who} before any request or dialog`, err && err.isActionError && err.kind === "permission" && calls.length === 0 && asked.length === 0, err ? `${err.kind} calls=${calls.length}` : "no error");
}
await as("editor");
on("GET", "/content/documents/home", { body: docBody() });
on("PUT", "/content/documents/home", { body: docBody() });
res = await actions.saveContentDocument("home", goodContent, { rev: 4 });
ok("an editor (content.edit) can save", res.ok);
ok("the saved doc carries the schema node (the PUT response has none) so the form can be rebuilt", res.doc.schemaNode && res.doc.schemaNode.properties.title);
await as("editor");
on("POST", "/preview-token", { body: { preview_token: "tok", expires_in: 600, scope: "document", key: "home" } });
res = await actions.createContentPreviewToken("home");
ok("an editor (content.view) can read a preview token", res.token === "tok");
ok("routes: documents needs content.view (nav order 25), content-schema needs content.manage (order 26), names and ids as specified",
  ROUTES.find((r) => r.id === "documents")?.name === "documents" && ROUTES.find((r) => r.id === "documents").perm.join() === "content.view"
  && ROUTES.find((r) => r.id === "contentSchema")?.name === "content-schema" && ROUTES.find((r) => r.id === "contentSchema").perm.join() === "content.manage"
  && NAV.find((n) => n.key === "documents")?.order === 25 && NAV.find((n) => n.key === "content-schema")?.order === 26
  && NAV.find((n) => n.key === "documents").copy === "nav.documents" && NAV.find((n) => n.key === "content-schema").copy === "nav.content-schema");
ok("the old global-content route is untouched", ROUTES.find((r) => r.id === "content")?.name === "content");

// ── 6. publish / unpublish / delete / restore ──
await as("owner");
on("GET", "/content/documents/home", { body: docBody({ needs_attention: true }) });
e = await rejects(() => actions.publishContentDocument("home", { rev: 4 }));
ok("a document that needs attention cannot be published (no dialog, no request but the read)", e && e.kind === "state" && calls.every((c) => c.method === "GET") && asked.length === 0);
reset(); asked.length = 0;
on("GET", "/content/documents/home", { body: docBody() });
on("POST", "/content/documents/home/publish", () => ({ body: docBody({ status: 1 }) }));
res = await actions.publishContentDocument("home", { rev: 4 });
const pub = calls.find((c) => c.method === "POST");
ok("publish asks for the confirmation and sends If-Match with the reviewed revision", asked[0].id === "publishContent" && pub.headers["If-Match"] === '"4"' && res.ok);
reset(); asked.length = 0; answers = [false];
on("GET", "/content/documents/home", { body: docBody() });
res = await actions.publishContentDocument("home", { rev: 4 });
ok("a declined publish sends nothing", res.cancelled && calls.every((c) => c.method === "GET"));
reset(); asked.length = 0; answers = [true];
on("GET", "/content/documents/home", { body: docBody() });
on("DELETE", "/content/documents/home", { status: 204 });
res = await actions.deleteContentDocument("home");
ok("delete asks first, then DELETEs", res.ok && asked[0].id === "deleteContent" && calls.some((c) => c.method === "DELETE"));
reset(); asked.length = 0; answers = [false];
on("GET", "/content/documents/home", { body: docBody() });
res = await actions.deleteContentDocument("home");
ok("a declined delete sends nothing", res.cancelled && calls.every((c) => c.method === "GET"));
reset(); asked.length = 0; answers = [false];
on("GET", "/content/documents/home", { body: docBody() });
res = await actions.unpublishContentDocument("home");
ok("a declined unpublish sends nothing", res.cancelled && calls.every((c) => c.method === "GET") && asked[0].id === "unpublishContent");
reset(); asked.length = 0; answers = [];
on("GET", "/content/documents/home", { body: docBody({ draft_rev: 6 }) });
on("POST", "/revisions/3/restore", { body: docBody({ draft_rev: 6, restored_valid: false, schema_node: undefined }) });
res = await actions.restoreContentRevision("home", 3, { rev: 5 });
const rc = calls.find((c) => c.method === "POST");
ok("restore: confirmation first; it only restores to the draft (nothing is published); If-Match is the screen's revision; restored_valid:false is reported",
  asked[0].id === "restoreContent" && res.restoredToDraft === true && res.restoredValid === false && rc.headers["If-Match"] === '"5"' && !calls.some((c) => /publish/.test(c.url)), JSON.stringify(res));
ok("the restore warning says it is only a draft", /草稿/.test(fixedWarning("restoreContent")) && /不會發佈/.test(fixedWarning("restoreContent")));
ok("the restored-to-draft copy says it is not published yet", copy["content.rev.restored"].startsWith("已放回草稿，尚未發佈"));

// ── 7. dry run is read-only; registration needs the human ──
const schemaText = JSON.stringify({ format: 2, types: { global: { title: "G", schema: { type: "object" } } } });
const stubSchemaRoutes = ({ removals = false } = {}) => {
  on("PUT", /\/content\/schema(\?|$)/, (r) => {
    if (r.url.includes("dry_run=true")) return { body: { version: 1, changed: true, dry_run: true, would_register_version: 2, report: { documents: [{ kind: "global", key: "global", status: "ok", removed_paths: removals ? ["/old"] : [], invalid: [], published_invalid: false }] } } };
    if (removals && !r.url.includes("confirm_removals=true")) return { status: 409, body: { error: { code: "removals_require_confirmation", message: "confirm", details: { report: { documents: [{ kind: "global", key: "global", status: "ok", removed_paths: ["/old"], invalid: [], published_invalid: false }] } } } } };
    return { body: { version: 2, changed: true, dry_run: false, report: { documents: [] } } };
  });
};
await as("owner"); stubSchemaRoutes({ removals: true });
const dry = await reads.dryRunSchema(schemaText);
ok("dry run: every schema PUT carries dry_run=true and nothing else is written", puts().length === 1 && puts().every((c) => /dry_run=true/.test(c.url) && !/confirm_removals/.test(c.url)) && calls.every((c) => c.method === "PUT"), JSON.stringify(calls.map((c) => c.url)));
ok("dry run reports the removals as plain text lines", dry.changed && dry.hasRemovals && dry.lines.some((l) => l.includes("/old")) && dry.wouldRegisterVersion === 2);
ok("the human's text is sent byte for byte", puts()[0].rawBody === schemaText);
reset();
e = await rejects(() => reads.dryRunSchema("{not json"));
ok("text that is not JSON is refused locally (no request)", e && e.kind === "invalid" && e.fields[""] && calls.length === 0);
e = await rejects(() => reads.dryRunSchema("   "));
ok("empty text is refused locally (no request)", e && e.kind === "invalid" && calls.length === 0);
e = await rejects(() => reads.dryRunSchema(`{"a":"${"x".repeat(C.MAX_SCHEMA_BYTES)}"}`));
ok("text over 128 KiB is refused locally (limit)", e && e.kind === "limit" && calls.length === 0);
on("PUT", /\/content\/schema/, { status: 422, body: { error: { code: "validation_failed", message: "bad schema", details: [{ pointer: "/types/x", message: "no default" }] } } });
e = await rejects(() => reads.dryRunSchema(schemaText));
ok("a 422 on the schema becomes field messages keyed by JSON Pointer", e && e.kind === "invalid" && e.fields["/types/x"]);

reset(); asked.length = 0; answers = [false]; stubSchemaRoutes();
res = await actions.registerContentSchema(schemaText);
ok("registration: a declined confirmation sends NO registering request (only the dry run)", res.cancelled && puts().length === 1 && /dry_run=true/.test(puts()[0].url) && asked.length === 1 && asked[0].id === "registerContentSchema");
ok("the confirmation shows the report as detail text", typeof asked[0].opts.detail === "string" && asked[0].opts.detail.length > 0);
reset(); asked.length = 0; answers = [false]; stubSchemaRoutes();
res = await actions.registerContentSchema(schemaText, { confirm: true, confirmRemovals: true, skipDialog: true }, true);
ok("registration ignores extra arguments: flags cannot skip the dialog", res.cancelled && puts().every((c) => /dry_run=true/.test(c.url)) && asked.length === 1 && actions.registerContentSchema.length === 1);
reset(); asked.length = 0; answers = [true]; stubSchemaRoutes();
res = await actions.registerContentSchema(schemaText);
ok("a confirmed registration sends one real PUT (no confirm_removals) after the dry run", res.ok && res.version === 2 && puts().length === 2 && /dry_run=true/.test(puts()[0].url) && !/dry_run/.test(puts()[1].url) && !/confirm_removals/.test(puts()[1].url));
reset(); asked.length = 0; answers = [true, false]; stubSchemaRoutes({ removals: true });
res = await actions.registerContentSchema(schemaText);
ok("removals: the platform's 409 needs a SECOND explicit confirmation; declining it sends no confirm_removals", res.cancelled && asked.map((a) => a.id).join() === "registerContentSchema,registerContentSchemaRemovals" && !calls.some((c) => /confirm_removals=true/.test(c.url)));
reset(); asked.length = 0; answers = [true, true]; stubSchemaRoutes({ removals: true });
res = await actions.registerContentSchema(schemaText);
ok("removals: after both confirmations confirm_removals=true is sent", res.ok && calls.some((c) => /confirm_removals=true/.test(c.url)));
reset(); asked.length = 0; answers = [];
on("PUT", /\/content\/schema/, { body: { version: 3, changed: false, dry_run: true, would_register_version: 0, report: { documents: [] } } });
res = await actions.registerContentSchema(schemaText);
ok("an unchanged schema is not registered and asks nothing", res.unchanged === true && asked.length === 0 && puts().length === 1);
reset(); asked.length = 0;
e = await rejects(() => actions.registerContentSchema("not json"));
ok("registration with invalid text asks nothing and sends nothing", e && e.kind === "invalid" && asked.length === 0 && calls.length === 0);
// the file read goes through a core action (views cannot use FileReader)
const fakeFile = (text, size) => ({ size: size ?? text.length, text: async () => text });
ok("readContentSchemaFile returns the file text", (await actions.readContentSchemaFile(fakeFile(schemaText))) === schemaText);
e = await rejects(() => actions.readContentSchemaFile(fakeFile("x", C.MAX_SCHEMA_BYTES + 1)));
ok("readContentSchemaFile refuses a file over the limit without reading it", e && e.kind === "limit");
e = await rejects(() => actions.readContentSchemaFile("not a file"));
ok("readContentSchemaFile refuses a non-file", e && e.kind === "invalid");

// ── 8. status, types, preview token, upload ──
await as("owner");
on("GET", "/content/schema/versions", { body: { versions: [{ version: 1, format: 1, sha256: "aa", source: "theme_backfill", created_by_user_id: null, created_at: "2026-11-24T00:00:00Z", has_snapshot: false }, { version: 2, format: 2, sha256: "bb", source: "registered", created_by_user_id: 3, created_at: "2026-11-25T00:00:00Z", has_snapshot: true }] } });
on("GET", /\/content\/schema$/, { body: { version: 2, format: 2, sha256: "bb", source: "registered", created_at: "2026-11-25T00:00:00Z", has_snapshot: true, schema: { format: 2, types: {} } } });
const st = await reads.schemaStatus();
ok("schemaStatus: active version, sha, source and versions (newest first)", st.active.version === 2 && st.active.sha256 === "bb" && st.active.source === "registered" && st.versions.map((v) => v.version).join() === "2,1");
reset(); await as("owner");
on("GET", "/content/schema", { status: 404, body: { error: { code: "content_schema_not_found", message: "none" } } });
const ty = await reads.types();
ok("types: no registered schema is a normal answer, not an error", ty.registered === false && ty.types.length === 0);
reset(); await as("owner");
on("GET", "/content/schema", { body: { version: 2, format: 2, sha256: "bb", source: "registered", created_at: "x", schema: { format: 2, types: { home: { title: "Home", schema: schemaNode }, campaign: { title: "Campaign", schema: schemaNode } }, collections: { "campaign-": { type: "campaign", max: 20 } } } } });
const ty2 = await reads.types();
ok("types: lists the document types and the collection prefixes", ty2.registered && ty2.types.map((x) => x.key).sort().join() === "campaign,home" && ty2.collections[0].prefix === "campaign-");
reset(); await as("owner");
on("POST", "/preview-token", (r) => ({ body: { preview_token: "tok/1", expires_in: 600, scope: r.body.scope, key: "home" } }));
res = await actions.createContentPreviewToken("home", { scope: "shop" });
ok("preview token: returns the token with its expiry and sends the scope; nothing is opened", res.token === "tok/1" && res.expiresIn === 600 && !Number.isNaN(Date.parse(res.expiresAt)) && calls[0].body.scope === "shop" && calls.length === 1);
reset();
on("POST", "/preview-token", (r) => ({ body: { preview_token: "t", expires_in: 60, scope: r.body.scope, key: "home" } }));
await actions.createContentPreviewToken("home", { scope: "anything" });
ok("an unknown scope falls back to the document scope", calls[0].body.scope === "document");
reset();
e = await rejects(() => actions.uploadContentImage({}));
ok("image upload with media disabled is a state error (503), no request", e && e.kind === "state" && e.status === 503 && calls.length === 0);

// ── 9. the group, copy and warnings ──
const surface = [...Object.keys(contentGroup.data), ...Object.keys(contentGroup.actions), ...Object.keys(contentGroup.logic), ...Object.keys(contentGroup.enums), ...Object.keys(contentGroup.ui)];
ok("ctx surface names are prefixed or specific (no cms*/catalog collisions)", surface.length === new Set(surface).size && surface.every((n) => !/^(cms|catalog)/.test(n)) && contentGroup.ui.contentForm && contentGroup.ui.contentField);
ok("the group exposes the documents ctx.data/actions the views call", ["contentTypes", "contentDocuments", "contentDocument", "contentRevisions", "contentRevision", "contentSchemaStatus", "dryRunContentSchema"].every((n) => typeof contentGroup.data[n] === "function")
  && ["saveContentDocument", "createContentDocument", "publishContentDocument", "unpublishContentDocument", "deleteContentDocument", "restoreContentRevision", "uploadContentImage", "createContentPreviewToken", "readContentSchemaFile", "registerContentSchema"].every((n) => typeof contentGroup.actions[n] === "function"));
const warnedIds = ["publishContent", "unpublishContent", "deleteContent", "restoreContent", "registerContentSchema", "registerContentSchemaRemovals"];
ok("every content confirmation id has a fixed warning (lib/meta) and the copy keys title/text/yes", warnedIds.every((id) => meta.WARNED_IDS.includes(id) && fixedWarning(id, { version: 2 }).length > 10 && `confirm.${id}.title` in copy && `confirm.${id}.text` in copy && `confirm.${id}.yes` in copy));
ok("no copy key defines a warning under confirm.*", !Object.keys(copy).some((k) => /^confirm\..*warn/i.test(k)));
ok("the registration warning states it cannot be automated", /不能由 AI 或自動化/.test(fixedWarning("registerContentSchema")));
const usedKeys = new Set();
for (const f of ["lib/content-schema.js", "core/content/actions.js", "ui/content-form.js"]) {
  const src = (await import("node:fs")).readFileSync(path.join(ROOT, f), "utf8");
  for (const m of src.matchAll(/\bt\("([a-z][A-Za-z0-9]*(?:\.[A-Za-z0-9_-]+)+)"/g)) usedKeys.add(m[1]);
}
const missingKeys = [...usedKeys].filter((k) => !(k in copy) && !["common.cancel", "err.403", "err.422"].includes(k));
ok("every copy key the core code uses is defined in design-defaults/copy/content.js", missingKeys.length === 0, missingKeys.join(", "));
console.warn = origWarn;

console.log(`\ncontent core: ${pass} passed, ${fail} failed`);
process.exit(fail ? 1 : 0);
