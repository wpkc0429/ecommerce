// E2E happy path (task 10.1) through the real browser:
// seed 建店 → (網域已綁 demo.localhost) → admin UI 登入 → 編輯全域內容與首頁
// → 發佈 → 前台渲染 → 換版 → 前台反映新主題。
//
// Prereqs: API on :8080, web on :3000, admin on :3001, seeded demo shop
// (make migrate && make seed-demo). Chromium resolves *.localhost → 127.0.0.1.
import { chromium } from "playwright";

const API = "http://localhost:8080";
const ADMIN_EMAIL = "admin@example.com";
const ADMIN_PASSWORD = "admin-change-me";

let failures = 0;
function check(name, cond, extra = "") {
  if (cond) {
    console.log(`  ✓ ${name}`);
  } else {
    failures++;
    console.error(`  ✗ ${name} ${extra}`);
  }
}

const browser = await chromium.launch();
const page = await browser.newPage();
page.on("pageerror", (err) => {
  failures++;
  console.error("  ✗ page JS error:", err.message);
});

try {
  // ── 9.1 登入 ──
  console.log("① Admin login");
  await page.goto("http://localhost:3001/");
  await page.fill("#login-email", ADMIN_EMAIL);
  await page.fill("#login-password", ADMIN_PASSWORD);
  await page.click("#login-form button[type=submit]");
  await page.waitForSelector("#view-main:not([hidden])", { timeout: 8000 });
  check("main view shown after login", true);
  const who = await page.textContent("#whoami");
  check("whoami shows admin email", who?.includes(ADMIN_EMAIL), who ?? "");

  // Platform admin has no sids → manual shop input (defaults to 1).
  await page.waitForSelector("#pages-tbody tr", { timeout: 8000 });
  const homeRow = page.locator("#pages-tbody tr", { hasText: "home" });
  check("pages list shows auto-created home draft",
    (await homeRow.count()) === 1 && (await homeRow.textContent()).includes("草稿"));

  // ── 9.2 新增頁面 422 定位（保留字 slug）──
  console.log("② Reserved slug rejected in create dialog");
  await page.click("#new-page-btn");
  await page.waitForSelector("#new-page-dialog[open]");
  await page.selectOption("#new-page-type", "about");
  await page.fill("#new-page-title", "測試");
  await page.fill("#new-page-slug", "api");
  await page.click("#new-page-form button[type=submit]");
  await page.waitForSelector("#new-page-error:not([hidden])");
  check("422 error surfaced in dialog", true);
  await page.click("#new-page-cancel");

  // ── 9.2 編輯首頁（schema 驅動表單 + sections 編輯器）──
  console.log("③ Edit home page via schema-driven form");
  await homeRow.locator("button", { hasText: "編輯" }).click();
  await page.waitForSelector("#tab-editor:not([hidden])");
  await page.waitForSelector("#editor-form fieldset.sections");
  check("sections editor rendered from page_schema", true);
  // Note: jsonb re-sorts schema keys, so field order is not authoring order —
  // locate the hero "title" field by its label, not by position.
  const heroRow = page.locator(".section-row", { hasText: "hero" }).first();
  const heroTitle = heroRow.getByLabel("title", { exact: true });
  await heroTitle.fill("E2E 全新首頁標題");
  await page.click("#editor-save");
  await page.waitForFunction(() =>
    document.querySelector("#editor-status").textContent.includes("草稿已儲存"));
  check("draft saved", true);

  // ── 9.3 發佈 ──
  console.log("④ Publish");
  await page.click("#editor-publish");
  await page.waitForFunction(() =>
    document.querySelector("#editor-status").textContent.includes("已發佈"));
  check("published from editor", true);

  // ── 前台渲染 ──
  console.log("⑤ Storefront renders the published home");
  await page.goto("http://demo.localhost:3000/");
  await page.waitForSelector("h1");
  const h1 = await page.textContent("h1");
  check("storefront shows the edited hero title", h1?.includes("E2E 全新首頁標題"), h1 ?? "");
  const title = await page.title();
  check("document title from page", title.length > 0, title);

  // ── 9.3 全域內容編輯 → 立即生效 ──
  console.log("⑥ Shop global content update");
  await page.goto("http://localhost:3001/");
  await page.waitForSelector("#view-main:not([hidden])");
  await page.click('.tab[data-tab="content"]');
  await page.waitForSelector("#content-form fieldset");
  const siteTitleInput = page.locator('#content-form input[data-pointer="/header/site_title"]');
  await siteTitleInput.fill("E2E 招牌");
  page.once("dialog", (d) => d.accept());
  await page.click("#content-save");
  await page.waitForTimeout(400);
  await page.goto("http://demo.localhost:3000/");
  const headerText = await page.textContent("header");
  check("storefront header shows updated site title", headerText?.includes("E2E 招牌"), headerText ?? "");

  // ── 換版 → 前台反映新主題 ──
  console.log("⑦ Theme switch reflected on the storefront");
  // Create a second theme via the API (admin UI has no theme manager — 平台端).
  const login = await (await fetch(`${API}/api/v1/admin/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ email: ADMIN_EMAIL, password: ADMIN_PASSWORD }),
  })).json();
  const authed = { "Content-Type": "application/json", Authorization: `Bearer ${login.access_token}` };
  const theme = await (await fetch(`${API}/api/v1/admin/themes`, {
    method: "POST", headers: authed,
    body: JSON.stringify({
      code: "starter-v2", name: "Starter V2", layout_key: "starter/main",
      config_schema: {
        type: "object", additionalProperties: false,
        properties: {
          tokens: { type: "object", additionalProperties: false, properties: {
            color_primary: { type: "string", default: "#dc2626" } } },
          header: { type: "object", additionalProperties: false, properties: {
            site_title: { type: "string", default: "V2 商店" },
            logo_url: { type: "string", default: "" },
            nav: { type: "array", default: [], items: { type: "object",
              additionalProperties: false, properties: {
                label: { type: "string", default: "" },
                href: { type: "string", default: "/" } } } } } },
          footer: { type: "object", additionalProperties: false, properties: {
            text: { type: "string", default: "V2 footer" },
            links: { type: "array", default: [], items: { type: "object",
              additionalProperties: false, properties: {
                label: { type: "string", default: "" },
                href: { type: "string", default: "/" } } } } } },
        },
      },
    }),
  })).json();
  check("v2 theme created", Boolean(theme.id), JSON.stringify(theme));
  const tp = await (await fetch(`${API}/api/v1/admin/themes/${theme.id}/pages`, {
    method: "POST", headers: authed,
    body: JSON.stringify({
      type_key: "home", component_key: "starter/home",
      page_schema: {
        type: "object", additionalProperties: false,
        properties: { sections: { type: "array", default: [], items: { oneOf: [{
          type: "object", additionalProperties: false, required: ["type"],
          properties: {
            type: { const: "hero" },
            title: { type: "string", default: "" },
            subtitle: { type: "string", default: "" },
            image: { type: "string", default: "" },
            cta_text: { type: "string", default: "" },
            cta_href: { type: "string", default: "/" },
          } }] } } },
      },
    }),
  })).json();
  check("v2 home page type registered", Boolean(tp.id), JSON.stringify(tp));
  const sw = await (await fetch(`${API}/api/v1/admin/shops/1/theme`, {
    method: "POST", headers: authed, body: JSON.stringify({ theme_id: theme.id }),
  })).json();
  check("theme switched", sw.theme_id === theme.id, JSON.stringify(sw));

  // The bundle now carries the v2 theme, and keys the v2 schema does not
  // define are pruned from the hydrated output (design D6) — while existing
  // payload values (site_title from step ⑥) are preserved, not reset.
  const bundle = await (await fetch(`${API}/api/v1/render/page?path=/`, {
    headers: { "X-Site-Domain": "demo.localhost" },
  })).json();
  check("bundle switches to the v2 theme immediately",
    bundle.shop?.theme?.code === "starter-v2", JSON.stringify(bundle.shop?.theme));
  check("keys undefined in the v2 schema are pruned",
    bundle.shop?.content?.tokens?.color_background === undefined);
  check("existing payload values survive the switch (hydration, not reset)",
    bundle.shop?.content?.header?.site_title === "E2E 招牌",
    JSON.stringify(bundle.shop?.content?.header));

  await page.goto("http://demo.localhost:3000/");
  const heroStill = await page.textContent("h1");
  check("page payload renders after the switch (hydrated against v2 schema)",
    heroStill?.includes("E2E 全新首頁標題"), heroStill ?? "");

  // Switch back to starter for repeatability.
  await fetch(`${API}/api/v1/admin/shops/1/theme`, {
    method: "POST", headers: authed, body: JSON.stringify({ theme_id: 1 }),
  });
} finally {
  await browser.close();
}

if (failures > 0) {
  console.error(`\nE2E FAILED with ${failures} failed check(s)`);
  process.exit(1);
}
console.log("\nE2E happy path PASSED");
