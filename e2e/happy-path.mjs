// E2E happy path (task 10.1) through the real browser:
// seed 建店 → (網域已綁 demo.localhost) → admin UI 登入 → 編輯全域內容與首頁
// → 發佈 → 前台渲染 → 換版 → 前台反映新主題。
//
// Since change commerce-demo-seed this script is the "run everything" entry
// point: after the CMS flow above it also runs the commerce chain
// (e2e/commerce-path.mjs) and reports one combined result. Keeping both behind
// the single documented command is deliberate — a second entry point nobody is
// told to run is a suite nobody runs (design D2). The commerce path needs no
// browser and can also be run on its own: `node commerce-path.mjs`.
//
// Prereqs: API on :8080, web on :3000, admin on :3001, seeded demo shop
// (make migrate && make seed-demo). Chromium resolves *.localhost → 127.0.0.1.
import { chromium } from "playwright";

import { brief, check, fail, failureCount, req, runID } from "./lib.mjs";
import { runCommercePath } from "./commerce-path.mjs";

const ADMIN_EMAIL = process.env.E2E_ADMIN_EMAIL ?? "admin@example.com";
const ADMIN_PASSWORD = process.env.E2E_ADMIN_PASSWORD ?? "admin-change-me";

const browser = await chromium.launch();
const page = await browser.newPage();
page.on("pageerror", (err) => fail(`page JS error: ${err.message}`));

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
  const login = await req("POST", "/api/v1/admin/auth/login", {
    body: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD },
  });
  const adminToken = login.json.access_token;
  const themeRes = await req("POST", "/api/v1/admin/themes", {
    token: adminToken,
    body: {
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
    },
  });
  const theme = themeRes.json;
  check("v2 theme created", Boolean(theme.id), JSON.stringify(theme));
  const tp = await req("POST", `/api/v1/admin/themes/${theme.id}/pages`, {
    token: adminToken,
    body: {
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
    },
  });
  check("v2 home page type registered", Boolean(tp.json.id), tp.text);
  const sw = await req("POST", "/api/v1/admin/shops/1/theme", {
    token: adminToken, body: { theme_id: theme.id },
  });
  check("theme switched", sw.json.theme_id === theme.id, sw.text);

  // The bundle now carries the v2 theme, and keys the v2 schema does not
  // define are pruned from the hydrated output (design D6) — while existing
  // payload values (site_title from step ⑥) are preserved, not reset.
  const bundleRes = await req("GET", "/api/v1/render/page?path=/", {
    headers: { "X-Site-Domain": "demo.localhost" },
  });
  const bundle = bundleRes.json;
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
  await req("POST", "/api/v1/admin/shops/1/theme", {
    token: adminToken, body: { theme_id: 1 },
  });

  // ══ 商品目錄後台（change admin-commerce-catalog, design D10）══
  //
  // 建立分類 → 建立含 2 個 SKU 的商品 → 金額顯示未被縮放 → 改一個 SKU 的庫存
  // → 另一個 SKU 仍在（守著 design D8 的全量 upsert 語意）→ 發佈 → 公開端點。
  const catSlug = `e2e-cat-${runID}`;
  const prodSlug = `e2e-product-${runID}`;
  const skuA = `E2E-${runID}-A`;
  const skuB = `E2E-${runID}-B`;

  console.log("⑧ Category CRUD in the admin UI");
  await page.goto("http://localhost:3001/");
  await page.waitForSelector("#view-main:not([hidden])", { timeout: 8000 });
  await page.click('.tab[data-tab="categories"]');
  await page.waitForSelector("#tab-categories:not([hidden])");
  await page.click("#new-category-btn");
  await page.waitForSelector("#category-dialog[open]");
  await page.fill("#category-name", `E2E 花束 ${runID}`);
  await page.fill("#category-slug", catSlug);
  await page.click("#category-form button[type=submit]");
  await page.waitForSelector(`#categories-tbody tr:has-text("${catSlug}")`, { timeout: 8000 });
  const newCategoryID =
    (await page.textContent(`#categories-tbody tr:has-text("${catSlug}") td:nth-child(1)`)).trim();
  check("category created through the admin UI", newCategoryID.length > 0, newCategoryID);

  console.log("⑨ Product with two nested SKUs");
  await page.click('.tab[data-tab="products"]');
  await page.waitForSelector("#tab-products:not([hidden])");
  await page.click("#new-product-btn");
  await page.waitForSelector("#tab-product-editor:not([hidden])");
  await page.fill("#product-title", `E2E 康乃馨花束 ${runID}`);
  await page.fill("#product-slug", prodSlug);
  await page.fill("#product-description", "E2E 建立的商品");
  // 多對多分類：勾選剛建立的分類（其 id 就是分類表格第一欄）。
  await page.check(`#product-form .category-box[value="${newCategoryID}"]`);

  // 兩列 SKU。價格是最小貨幣單位整數：3400 就是 NT$3,400（design D4）。
  await page.click("#add-sku-btn");
  await page.click("#add-sku-btn");
  const skuRows = page.locator("#sku-tbody tr.sku-row");
  check("two SKU rows added by the nested repeater", (await skuRows.count()) === 2);

  await skuRows.nth(0).locator(".sku-code").fill(skuA);
  await skuRows.nth(0).locator(".sku-price").fill("3400");
  await skuRows.nth(0).locator(".sku-stock").fill("5");
  // 巢狀第二層：SKU 列裡面的 options key/value repeater。
  await skuRows.nth(0).locator("button.tiny").click();
  await skuRows.nth(0).locator(".kv-key").fill("尺寸");
  await skuRows.nth(0).locator(".kv-value").fill("S");

  await skuRows.nth(1).locator(".sku-code").fill(skuB);
  await skuRows.nth(1).locator(".sku-price").fill("4400");
  await skuRows.nth(1).locator(".sku-stock").fill("3");

  const livePreview = await skuRows.nth(0).locator(".money-preview").textContent();
  check("price input previews the amount unscaled (3400 → NT$3,400)",
    livePreview?.trim() === "NT$3,400", livePreview ?? "");

  await page.click("#product-save");
  await page.waitForFunction(() =>
    document.querySelector("#product-editor-status").textContent.includes("已儲存"));
  check("product created with its nested SKUs", true);

  console.log("⑩ 金額顯示未被縮放（本 change 唯一「寫錯不會噴錯」的地方）");
  await page.click("#product-back");
  await page.waitForSelector("#tab-products:not([hidden])");
  await page.fill("#product-q", prodSlug);
  await page.click("#product-search");
  const prodRow = page.locator(`#products-tbody tr[data-slug="${prodSlug}"]`);
  await prodRow.waitFor({ timeout: 8000 });
  check("server-side keyword search finds the product", (await prodRow.count()) === 1);
  const rowText = (await prodRow.textContent()) ?? "";
  check("list shows NT$3,400 – NT$4,400 (integer minor units, never divided)",
    rowText.includes("NT$3,400") && rowText.includes("NT$4,400"), rowText);
  check("no /100 anywhere: NT$34 never appears", !rowText.includes("NT$34"), rowText);
  check("no *100 anywhere: NT$340,000 never appears", !rowText.includes("NT$340,000"), rowText);
  check("stock總計 shown as 8", rowText.includes("8"), rowText);

  console.log("⑪ 改一個 SKU 的庫存，另一個 SKU 必須毫髮無傷（design D8）");
  await prodRow.locator("button", { hasText: "編輯" }).click();
  await page.waitForSelector("#tab-product-editor:not([hidden])");
  const rowA = page.locator(`#sku-tbody tr.sku-row:has(.sku-code[value="${skuA}"])`);
  await rowA.locator(".sku-stock").fill("7");
  await page.click("#product-save");
  await page.waitForFunction(() =>
    document.querySelector("#product-editor-status").textContent.includes("已儲存"));
  await page.click("#product-back");
  await page.waitForSelector("#tab-products:not([hidden])");
  await prodRow.locator("button", { hasText: "編輯" }).click();
  await page.waitForSelector("#sku-tbody tr.sku-row");
  const reloaded = page.locator("#sku-tbody tr.sku-row");
  check("both SKUs survived the save (a partial payload would have deleted one)",
    (await reloaded.count()) === 2, String(await reloaded.count()));
  check("edited SKU has the new stock",
    (await page.inputValue(`#sku-tbody tr.sku-row:has(.sku-code[value="${skuA}"]) .sku-stock`)) === "7");
  check("untouched SKU kept its own stock",
    (await page.inputValue(`#sku-tbody tr.sku-row:has(.sku-code[value="${skuB}"]) .sku-stock`)) === "3");
  check("nested option round-tripped through jsonb",
    (await page.inputValue(`#sku-tbody tr.sku-row:has(.sku-code[value="${skuA}"]) .kv-value`)) === "S");

  console.log("⑫ 發佈 → 前台公開端點看得到");
  await page.selectOption("#product-status-select", "1");
  await page.click("#product-save");
  await page.waitForFunction(() =>
    document.querySelector("#product-editor-status").textContent.includes("已發佈"));
  const publicList = await req("GET", "/api/v1/shop/products?page_size=100", {
    pk: "pk_demoseedstorefrontkeynotsecret",
  });
  const published = (publicList.json?.products ?? []).find((p) => p.slug === prodSlug);
  check("published product is visible on the public catalog endpoint",
    Boolean(published), brief(publicList));
  const publicA = (published?.skus ?? []).find((s) => s.sku_code === skuA);
  check("public endpoint reports the edited stock", publicA?.stock_qty === 7,
    JSON.stringify(published?.skus));
  check("public endpoint reports the unscaled price", publicA?.price_amount === 3400,
    JSON.stringify(publicA));
} catch (err) {
  // Record and carry on: an admin/web outage must not hide the commerce
  // results, which need only the API.
  fail(`CMS path aborted: ${err?.stack ?? err}`);
} finally {
  await browser.close();
}

// ── 電商鏈（change commerce-demo-seed）──
// No browser involved; shares lib.mjs's failure counter so the exit below
// reflects both halves.
console.log("\n════ Commerce path ════");
await runCommercePath();

if (failureCount() > 0) {
  console.error(`\nE2E FAILED with ${failureCount()} failed check(s)`);
  process.exit(1);
}
console.log("\nE2E happy path PASSED (CMS + commerce)");
