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

import { brief, check, deliverWebhook, dotEnvValue, fail, failureCount, isoDate, req, runID } from "./lib.mjs";
import { runCommercePath } from "./commerce-path.mjs";

// Same source `make seed` used: E2E_* override → env → repo .env → old default.
const ADMIN_EMAIL =
  process.env.E2E_ADMIN_EMAIL || process.env.SEED_ADMIN_EMAIL || dotEnvValue("SEED_ADMIN_EMAIL") || "admin@example.com";
const ADMIN_PASSWORD =
  process.env.E2E_ADMIN_PASSWORD ||
  process.env.SEED_ADMIN_PASSWORD ||
  dotEnvValue("SEED_ADMIN_PASSWORD") ||
  "admin-change-me";

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
  const publicB = (published?.skus ?? []).find((s) => s.sku_code === skuB);
  check("public endpoint reports the edited stock", publicA?.stock_qty === 7,
    JSON.stringify(published?.skus));
  check("public endpoint reports the unscaled price", publicA?.price_amount === 3400,
    JSON.stringify(publicA));

  // ══ 商家營運後台（change admin-commerce-operations, design D11）══
  //
  // 建立運送方式 → 用「上面剛發佈的那個商品」下單 → 後台以深層連結看到訂單 →
  // 付款 → 出貨並標記已送達 → 點數餘額與流水帳 → 產生 publishable key →
  // 會員等級 CRUD → allowed origin 登記與移除。
  //
  // 刻意用自建商品而不是 seed 的花束：commerce-path 的 STOCK_PER_RUN 是對花束
  // 庫存的預算，這一段若也吃它，就會靜默縮短「一份 seed 能跑幾次」。
  const PK = "pk_demoseedstorefrontkeynotsecret";
  const methodName = `E2E 專車配送 ${runID}`;

  console.log("⑬ 後台建立運送方式（運費不縮放）");
  await page.click('.tab[data-tab="shipping-methods"]');
  await page.waitForSelector("#tab-shipping-methods:not([hidden])");
  await page.click("#new-shipping-method-btn");
  await page.waitForSelector("#shipping-method-dialog[open]");
  await page.fill("#shipping-method-name", methodName);
  await page.fill("#shipping-method-carrier", "E2E 快遞");
  await page.fill("#shipping-method-rate", "150");
  await page.click("#shipping-method-form button[type=submit]");
  const methodRow = page.locator(`#shipping-methods-tbody tr[data-name="${methodName}"]`);
  await methodRow.waitFor({ timeout: 8000 });
  const methodID = Number((await methodRow.locator("td").first().textContent()).trim());
  const methodText = (await methodRow.textContent()) ?? "";
  check("shipping method created through the admin UI", Number.isFinite(methodID) && methodID > 0, String(methodID));
  check("flat rate shown unscaled (150 → NT$150)", methodText.includes("NT$150"), methodText);
  check("no /100: NT$1.5 never appears", !methodText.includes("NT$1.5"), methodText);
  check("no *100: NT$15,000 never appears", !methodText.includes("NT$15,000"), methodText);

  console.log("⑭ 以該運送方式在前台下單");
  const skuID = publicA?.id;
  const guestEmail = `e2e-ops-${runID}@example.com`;
  const placed = await req("POST", "/api/v1/shop/guest/checkout", {
    pk: PK,
    body: {
      email: guestEmail,
      items: [{ sku_id: skuID, quantity: 1, gift_message: "E2E：營運後台測試卡片" }],
      shipping_address: { recipient_name: `E2E 收件人 ${runID}`, phone: "0955123456", line1: "台北市大安區信義路四段 1 號" },
      shipping_method_id: methodID,
      requested_delivery_date: isoDate(1),
    },
  });
  check("guest order placed with the just-created shipping method",
    placed.status === 201, brief(placed));
  const opsOrder = placed.json?.order ?? {};
  const opsToken = placed.json?.order_access_token;

  console.log("⑮ 後台以深層連結 #/orders/{id} 開啟詳情");
  await page.goto(`http://localhost:3001/#/orders/${opsOrder.id}`);
  await page.waitForSelector("#tab-order-detail:not([hidden])", { timeout: 8000 });
  await page.waitForSelector("#order-amounts");
  check("deep link opens the order detail directly (no list navigation)",
    (await page.textContent("#order-detail-title")).includes(String(opsOrder.id)),
    await page.textContent("#order-detail-title"));
  // 金額三件組：3400 + 150 = 3550，全部不縮放，且組合關係看得見。
  const subtotal = (await page.textContent("#order-subtotal")).trim();
  const shippingFee = (await page.textContent("#order-shipping-fee")).trim();
  const total = (await page.textContent("#order-total")).trim();
  check("subtotal shown unscaled", subtotal === "NT$3,400", subtotal);
  check("shipping fee shown unscaled", shippingFee === "NT$150", shippingFee);
  check("payable total is subtotal + shipping fee", total === "NT$3,550", `${subtotal} + ${shippingFee} = ${total}`);
  // change tier-discount-application (design D13): a brand-new email holds no
  // tier, and a "− NT$0" row on every order would teach merchants to ignore the
  // one row that matters. The positive case is asserted in ⑱.5 below.
  check("an undiscounted order shows no discount row at all",
    (await page.locator("#order-discount").count()) === 0);
  const summaryText = (await page.textContent("#order-summary")) ?? "";
  check("recipient shown on the detail", summaryText.includes(`E2E 收件人 ${runID}`), summaryText.slice(0, 200));
  check("requested delivery date shown on the detail", summaryText.includes(isoDate(1)), summaryText.slice(0, 200));
  check("shipping method name snapshotted onto the order is shown", summaryText.includes(methodName), summaryText.slice(0, 200));
  const itemsText = (await page.textContent("#order-items-tbody")) ?? "";
  check("gift message shown on the item row, not at order level",
    itemsText.includes("E2E：營運後台測試卡片"), itemsText.slice(0, 200));

  console.log("⑯ 付款 → 後台顯示已付款");
  const opsPay = await req("POST", "/api/v1/shop/guest/order/payments", { token: opsToken, pk: PK, body: {} });
  check("payment initiated for the ops order", opsPay.status === 201, brief(opsPay));
  const opsHook = await deliverWebhook(opsPay.json?.provider_reference);
  check("signed provider webhook accepted (ops order)", opsHook.status === 200, brief(opsHook));
  await page.reload();
  await page.waitForSelector("#order-amounts", { timeout: 8000 });
  check("back office reflects the paid state",
    ((await page.textContent("#order-summary")) ?? "").includes("已付款"),
    (await page.textContent("#order-summary"))?.slice(0, 200) ?? "");

  console.log("⑰ 後台出貨 → 標記已送達（終態）");
  await page.waitForSelector("#create-shipment-btn", { timeout: 8000 });
  await page.fill("#shipment-carrier", "E2E 快遞");
  await page.fill("#shipment-tracking", `E2E-OPS-${runID}`);
  await page.click("#create-shipment-btn");
  await page.waitForSelector("#shipment-detail", { timeout: 8000 });
  const shipText = (await page.textContent("#order-shipment")) ?? "";
  check("shipment created through the admin UI", shipText.includes(`E2E-OPS-${runID}`), shipText.slice(0, 200));
  check("order is now marked shipped", shipText.includes("已出貨"), shipText.slice(0, 200));
  page.once("dialog", (d) => d.accept());
  await page.click("#shipment-delivered-btn");
  await page.waitForSelector("#shipment-terminal-note", { timeout: 8000 });
  check("shipment advanced to delivered",
    ((await page.textContent("#order-shipment")) ?? "").includes("已送達"),
    (await page.textContent("#order-shipment"))?.slice(0, 200) ?? "");
  check("a terminal shipment offers no further state buttons",
    (await page.locator("#shipment-delivered-btn").count()) === 0 &&
      (await page.locator("#shipment-returned-btn").count()) === 0);

  console.log("⑰.5 後台部分出貨（change partial-shipment）");
  // A second order, deliberately with a line of quantity 2 and a line of
  // quantity 1: the interesting case is not "two lines" but "part of one line".
  const partPlaced = await req("POST", "/api/v1/shop/guest/checkout", {
    pk: PK,
    body: {
      email: `e2e-partial-${runID}@example.com`,
      items: [
        { sku_id: publicA?.id, quantity: 2, gift_message: "E2E：分批出貨 A" },
        { sku_id: publicB?.id, quantity: 1, gift_message: "E2E：分批出貨 B" },
      ],
      shipping_address: { recipient_name: `E2E 分批 ${runID}`, phone: "0955123456", line1: "台北市大安區信義路四段 1 號" },
      shipping_method_id: methodID,
    },
  });
  check("a two-line guest order was placed for the partial-shipment screen",
    partPlaced.status === 201, brief(partPlaced));
  const partOrder = partPlaced.json?.order ?? {};
  const partLines = partOrder.items ?? [];
  const lineOf = (code) => partLines.find((i) => i.sku_code === code);

  await page.goto(`http://localhost:3001/#/orders/${partOrder.id}`);
  await page.waitForSelector("#tab-order-detail:not([hidden])", { timeout: 8000 });
  await page.waitForSelector("#shipment-lines", { timeout: 8000 });

  const shippedCell = async (code) =>
    (await page.textContent(`#order-items-tbody tr[data-sku="${code}"] .shipped-qty`) ?? "").trim();
  check("an unshipped order reports 0 of each line as shipped",
    (await shippedCell(skuA)) === "0 / 2" && (await shippedCell(skuB)) === "0 / 1",
    `${await shippedCell(skuA)} | ${await shippedCell(skuB)}`);
  check("the create-shipment form offers one row per outstanding line",
    (await page.locator("#shipment-lines .shipment-line").count()) === 2,
    String(await page.locator("#shipment-lines .shipment-line").count()));

  // **This is the assertion the whole repeater discipline exists for.** Each
  // row's read() closes over its own input; a collector that went back to the
  // DOM by label text (lib/schemaform.js's path) would mis-assign these two
  // same-named quantity fields, and it would do it silently. Ship 1 of A and 0
  // of B — if the values crossed, B would ship and A would not.
  const qtyFor = (line) => `#shipment-lines .shipment-line-qty[data-order-item-id="${line.id}"]`;
  await page.fill(qtyFor(lineOf(skuA)), "1");
  await page.fill(qtyFor(lineOf(skuB)), "0");
  await page.fill("#shipment-carrier", "E2E 快遞");
  await page.fill("#shipment-tracking", `E2E-PART-${runID}`);
  await page.click("#create-partial-shipment-btn");
  await page.waitForSelector("#order-shipments-list .parcel", { timeout: 8000 });

  check("shipping part of one line marks the order 部分出貨, not 已出貨",
    ((await page.textContent("#order-summary")) ?? "").includes("部分出貨"),
    (await page.textContent("#order-summary"))?.slice(0, 300) ?? "");
  check("each row's quantity went to its own line (1 of A, none of B)",
    (await shippedCell(skuA)) === "1 / 2" && (await shippedCell(skuB)) === "0 / 1",
    `${await shippedCell(skuA)} | ${await shippedCell(skuB)}`);
  const parcelText = (await page.textContent("#order-shipments-list")) ?? "";
  check("the parcel says what is inside it, by product name",
    parcelText.includes(`E2E-PART-${runID}`) && parcelText.includes("× 1"),
    parcelText.slice(0, 300));
  check("the remainder is still offered for a second parcel",
    (await page.locator("#create-shipment-btn").count()) === 1 &&
      (await page.locator("#shipment-lines .shipment-line").count()) === 2,
    String(await page.locator("#shipment-lines .shipment-line").count()));

  // Ship everything outstanding: the button that sends NO item list, i.e. the
  // request body that predates this change, byte for byte.
  await page.fill("#shipment-carrier", "E2E 快遞");
  await page.click("#create-shipment-btn");
  await page.waitForFunction(
    () => document.querySelectorAll("#order-shipments-list .parcel").length === 2,
    null, { timeout: 8000 });
  check("shipping the remainder completes the order (已出貨, every line full)",
    (await shippedCell(skuA)) === "2 / 2" && (await shippedCell(skuB)) === "1 / 1",
    `${await shippedCell(skuA)} | ${await shippedCell(skuB)}`);
  check("a fully shipped order stops offering a create-shipment form",
    (await page.locator("#create-shipment-btn").count()) === 0 &&
      (await page.locator("#order-shipment-complete-note").count()) === 1);
  // 已出貨數不是金額：這一欄絕不能長出貨幣符號（design D4/D10 的同一條紀律）。
  const itemsTableText = (await page.textContent("#order-items-tbody")) ?? "";
  check("the shipped-quantity column never renders as money",
    !/NT\$\s*[0-9]*\s*\/\s*[0-9]/.test(itemsTableText), itemsTableText.slice(0, 200));

  // Back to the ops order before ⑱: that section clicks #order-points-btn, which
  // reads the member of whatever order is currently on screen. Leaving this
  // section's own (different) customer displayed would send ⑱ to look up a
  // brand-new guest with no points and no ledger — a failure that looks exactly
  // like a broken points chain and has nothing to do with points.
  await page.goto(`http://localhost:3001/#/orders/${opsOrder.id}`);
  await page.waitForSelector("#tab-order-detail:not([hidden])", { timeout: 8000 });
  await page.waitForSelector("#order-amounts", { timeout: 8000 });

  console.log("⑱ 後台看到該顧客的點數餘額與流水帳");
  await page.click("#order-points-btn");
  await page.waitForSelector("#tab-points:not([hidden])", { timeout: 8000 });
  await page.waitForFunction(() =>
    document.querySelector("#points-balance").textContent.trim() !== "—", null, { timeout: 8000 });
  // The view fires loadBalance() and loadLedger() in parallel (Promise.all), so a
  // populated balance does not imply a populated ledger — wait for the second one
  // too, or this section is a coin flip. (Observed failing once before this wait
  // existed; it has nothing to do with what is being asserted.)
  await page.waitForSelector("#points-tbody tr", { timeout: 8000 });
  const balance = (await page.textContent("#points-balance")).trim();
  // 核發基準是品項小計，不是應付總額（change order-extras design D5）：
  // floor(3400 / 100) = 34；若誤用 total 會是 floor(3550 / 100) = 35。
  check("balance is floor(subtotal / 100), not floor(total / 100)", balance === "34 點", balance);
  check("points are NOT rendered as money", !balance.includes("NT$") && !balance.includes("$"), balance);
  const ledgerText = (await page.textContent("#points-tbody")) ?? "";
  check("ledger shows the award row for this order",
    ledgerText.includes("訂單核發") && ledgerText.includes(`訂單 #${opsOrder.id}`), ledgerText.slice(0, 200));
  check("ledger delta is a point count, not currency",
    ledgerText.includes("+34 點") && !ledgerText.includes("NT$34"), ledgerText.slice(0, 200));

  console.log("⑱.5 同一位顧客的第二筆訂單自動帶等級折扣（後台看得見）");
  // No setup: ⑯ paid 3,400 → 34 points → past the seeded 銀卡會員 threshold (30),
  // which carries 5%. So the *same guest email* placing a second order is
  // enough — and it doubles as the proof that a returning guest gets the
  // discount without ever logging in (design D9), which is the only way ONDO's
  // customers can get one at all.
  const repeat = await req("POST", "/api/v1/shop/guest/checkout", {
    pk: PK,
    body: {
      email: guestEmail,
      items: [{ sku_id: skuID, quantity: 1 }],
      shipping_address: { recipient_name: `E2E 收件人 ${runID}`, phone: "0955123456", line1: "台北市大安區信義路四段 1 號" },
      shipping_method_id: methodID,
    },
  });
  check("the returning guest's second order is placed", repeat.status === 201, brief(repeat));
  const repeatOrder = repeat.json?.order ?? {};
  // 3400 − floor(3400 × 5 / 100) + 150 = 3400 − 170 + 150 = 3380.
  check("the returning guest's tier discount is applied without any login",
    repeatOrder.discount_amount === 170 && repeatOrder.discount_percent === 5 &&
      repeatOrder.total_amount === 3380,
    JSON.stringify([repeatOrder.discount_amount, repeatOrder.discount_percent, repeatOrder.total_amount]));

  await page.goto(`http://localhost:3001/#/orders/${repeatOrder.id}`);
  await page.waitForSelector("#tab-order-detail:not([hidden])", { timeout: 8000 });
  await page.waitForSelector("#order-discount", { timeout: 8000 });
  const dDiscount = (await page.textContent("#order-discount")).trim();
  const dTotal = (await page.textContent("#order-total")).trim();
  const amountsText = (await page.textContent("#order-amounts")) ?? "";
  check("the discount is shown unscaled (170 → NT$170)", dDiscount === "NT$170", dDiscount);
  check("the payable total reflects the discount", dTotal === "NT$3,380", `${dDiscount} → ${dTotal}`);
  check("the tier that granted it is named, with its rate as a percentage not money",
    amountsText.includes("銀卡會員") && amountsText.includes("5%") && !amountsText.includes("NT$5"),
    amountsText.replace(/\s+/g, " ").slice(0, 200));

  console.log("⑲ 顧客名冊：搜尋 → 詳情 → 點數與訂單（change member-directory）");
  // No setup at all: by this point ⑯ paid an order for `guestEmail` (34 points →
  // 銀卡會員) and ⑱.5 placed a second one. So the roster has a customer with a
  // tier, two orders, and — because guest checkout never sets a password — the
  // "訪客身分" case, which is the one this change most needed to get right.
  await page.click('.tab[data-tab="members"]');
  await page.waitForSelector("#tab-members:not([hidden])", { timeout: 8000 });
  const rosterNotice = (await page.textContent("#tab-members")) ?? "";
  check("the roster states guest identities never registered nor consented",
    rosterNotice.includes("訪客身分") && rosterNotice.includes("未曾註冊") && rosterNotice.includes("行銷"),
    rosterNotice.replace(/\s+/g, " ").slice(0, 160));

  await page.fill("#member-q", guestEmail);
  await page.click("#member-search");
  const memberRow = page.locator(`#members-tbody tr:has-text("${guestEmail}")`);
  await memberRow.waitFor({ timeout: 8000 });
  const memberRowText = (await memberRow.textContent()) ?? "";
  check("server-side search finds the customer who placed those orders", true, guestEmail);
  check("a guest-checkout identity is labelled as such, not as a registered member",
    memberRowText.includes("訪客身分") && !memberRowText.includes("已註冊"), memberRowText);
  // Points are points on the roster too — 34 must never render as NT$34.
  check("roster shows the balance as points, not money",
    memberRowText.includes("34 點") && !memberRowText.includes("NT$"), memberRowText);
  check("roster shows the tier the customer reached", memberRowText.includes("銀卡會員"), memberRowText);

  await memberRow.locator("button", { hasText: "詳情" }).click();
  await page.waitForSelector("#tab-member-detail:not([hidden])", { timeout: 8000 });
  await page.waitForSelector("#member-orders-tbody tr", { timeout: 8000 });
  const memberID = Number((await page.getAttribute(`#members-tbody tr:has-text("${guestEmail}")`, "data-member-id")));
  const detailText = (await page.textContent("#member-summary")) ?? "";
  check("detail shows the customer's email", detailText.includes(guestEmail), detailText.slice(0, 200));
  const memberPoints = (await page.textContent("#member-points")).trim();
  check("detail shows the balance as points, not money",
    memberPoints === "34 點" && !memberPoints.includes("NT$"), memberPoints);
  check("detail names the current tier",
    ((await page.textContent("#member-level")) ?? "").includes("銀卡會員"),
    await page.textContent("#member-level"));
  check("detail explains that a guest identity cannot log in",
    (await page.locator("#member-guest-note").count()) === 1);
  // design D9: the customer detail must NOT own points. No adjust form here —
  // one balance, one write path, one audit trail.
  check("the customer detail offers no points adjustment (points has exactly one owner)",
    (await page.locator("#tab-member-detail input").count()) === 0 &&
      (await page.locator("#tab-member-detail form").count()) === 0,
    `inputs=${await page.locator("#tab-member-detail input").count()} forms=${await page.locator("#tab-member-detail form").count()}`);

  const memberOrdersText = (await page.textContent("#member-orders-tbody")) ?? "";
  check("detail lists both of this customer's orders",
    memberOrdersText.includes(String(opsOrder.id)) && memberOrdersText.includes(String(repeatOrder.id)),
    memberOrdersText.replace(/\s+/g, " ").slice(0, 200));
  check("order amounts on the customer detail are money and unscaled",
    memberOrdersText.includes("NT$3,550") && memberOrdersText.includes("NT$3,380"),
    memberOrdersText.replace(/\s+/g, " ").slice(0, 200));

  await page.click("#member-points-btn");
  await page.waitForSelector("#tab-points:not([hidden])", { timeout: 8000 });
  check("the detail's points button hands off to the view that owns adjustment",
    (await page.textContent("#points-member-label")).includes(String(memberID)),
    await page.textContent("#points-member-label"));

  console.log("⑳ 產生 publishable key");
  await page.click('.tab[data-tab="storefront-clients"]');
  await page.waitForSelector("#tab-storefront-clients:not([hidden])");
  const keyLabel = `e2e-${runID}`;
  page.once("dialog", (d) => d.accept(keyLabel));
  await page.click("#new-publishable-key-btn");
  const newKeyRow = page.locator(`#publishable-keys-tbody tr:has-text("${keyLabel}")`);
  await newKeyRow.waitFor({ timeout: 8000 });
  const keyValue = (await newKeyRow.locator("td.key-value").textContent()).trim();
  check("publishable key generated through the admin UI", keyValue.startsWith("pk_"), keyValue);
  check("the key is shown in full (public identifier, not a credential)",
    keyValue.length > 10 && !keyValue.includes("…") && !keyValue.includes("*"), keyValue);

  console.log("㉑ allowed origin（平台層）登記與移除");
  const e2eOrigin = `https://e2e-${runID}.example.com`;
  check("platform-level origins block is visible to the platform admin",
    await page.isVisible("#allowed-origins-block"));
  await page.fill("#allowed-origin-input", e2eOrigin);
  await page.click("#allowed-origin-form button[type=submit]");
  const originRow = page.locator(`#allowed-origins-tbody tr[data-origin="${e2eOrigin}"]`);
  await originRow.waitFor({ timeout: 8000 });
  check("allowed origin registered through the admin UI", true, e2eOrigin);
  page.once("dialog", (d) => d.accept());
  await originRow.locator("button").click();
  await originRow.waitFor({ state: "detached", timeout: 8000 });
  check("allowed origin removed again (globally unique — leaving it would 422 next run)", true);

  console.log("㉒ 會員等級 CRUD（門檻極高 + 用完刪除，對點數階梯淨零影響）");
  await page.click('.tab[data-tab="member-tiers"]');
  await page.waitForSelector("#tab-member-tiers:not([hidden])");
  const tierName = `E2E 等級 ${runID}`;
  await page.click("#new-member-tier-btn");
  await page.waitForSelector("#member-tier-dialog[open]");
  await page.fill("#member-tier-name", tierName);
  await page.fill("#member-tier-min-points", "999000");
  await page.fill("#member-tier-discount", "1");
  await page.click("#member-tier-form button[type=submit]");
  const tierRow = page.locator(`#member-tiers-tbody tr[data-tier="${tierName}"]`);
  await tierRow.waitFor({ timeout: 8000 });
  const tierText = (await tierRow.textContent()) ?? "";
  check("member tier created through the admin UI", tierText.includes(tierName), tierText);
  // change tier-discount-application (design D13): the screen used to say the
  // discount was stored but not applied. It is applied now, and a back office
  // still claiming otherwise is the bug that change exists to fix.
  const tierHelp = (await page.textContent("#tab-member-tiers")) ?? "";
  check("the tier screen no longer claims the discount is inert",
    !tierHelp.includes("尚未參與") && !tierHelp.includes("只儲存") && tierHelp.includes("結帳時"),
    tierHelp.replace(/\s+/g, " ").slice(0, 160));
  check("min_points rendered as points, never as money",
    tierText.includes("999,000 點") && !tierText.includes("NT$"), tierText);
  await tierRow.locator("button", { hasText: "編輯" }).click();
  await page.waitForSelector("#member-tier-dialog[open]");
  await page.fill("#member-tier-discount", "2");
  await page.click("#member-tier-form button[type=submit]");
  await page.waitForFunction((name) => {
    const row = [...document.querySelectorAll("#member-tiers-tbody tr")]
      .find((r) => r.dataset.tier === name);
    return row && row.textContent.includes("2%");
  }, tierName, { timeout: 8000 });
  check("member tier edited through the admin UI", true, "discount 1% → 2%");
  page.once("dialog", (d) => d.accept());
  await tierRow.locator("button", { hasText: "刪除" }).click();
  await tierRow.waitFor({ state: "detached", timeout: 8000 });
  check("member tier deleted (the seeded ladder is left exactly as it was)", true);

  console.log("㉓ 營運報表：口徑揭露、時區來源、金額組合、筆數不是金額（change merchant-reports）");
  // 零佈置：⑯ 已付了一筆訂單、⑱.5 又下了一筆，兩筆都是今天建立的，所以報表
  // 的預設期間（本月一日 → 今天）一定不是空的。
  await page.goto("http://localhost:3001/#/reports");
  await page.waitForSelector("#tab-reports:not([hidden])", { timeout: 8000 });
  await page.waitForSelector('#report-currencies [data-currency="TWD"]', { timeout: 8000 });

  // 口徑必須在畫面上，不是只在文件裡。三句缺一不可：算什麼、時間軸是什麼、
  // 以及「你可以自己核對」。
  const criteria = (await page.textContent("#tab-reports")) ?? "";
  check("the report screen states which orders it counts",
    criteria.includes("已付款") && criteria.includes("未取消") && criteria.includes("未退貨"),
    criteria.replace(/\s+/g, " ").slice(0, 200));
  check("the report screen admits it is not a cash-flow statement",
    criteria.includes("不是現金流量表") && criteria.includes("訂單建立日"),
    criteria.replace(/\s+/g, " ").slice(0, 200));
  check("the report screen tells the merchant how to verify it themselves",
    criteria.includes("四類筆數相加"), criteria.replace(/\s+/g, " ").slice(0, 200));
  check("the ranking screen states its amount is pre-discount and renames split rows",
    criteria.includes("折扣前") && criteria.includes("改名"),
    criteria.replace(/\s+/g, " ").slice(0, 200));

  // 時區：demo 商家從未設定過，畫面必須說那是**回落**而不是他的選擇。
  const tzText = (await page.textContent("#report-timezone")) ?? "";
  check("an unconfigured shop is told its timezone is a platform fallback, not its own choice",
    tzText.includes("平台預設") && tzText.includes("尚未設定"), tzText);
  check("changing the timezone warns that every historical report moves with it",
    (await page.isVisible("#report-timezone-note")), tzText);

  // 金額組合式（design D3 項 5）——與訂單詳情同一條規則。
  const twdBlock = (await page.textContent('#report-currencies [data-currency="TWD"]')) ?? "";
  check("report amounts are composed, not a single opaque 「營業額」",
    twdBlock.includes("品項小計") && twdBlock.includes("運費") && twdBlock.includes("請款總額"),
    twdBlock.replace(/\s+/g, " ").slice(0, 200));
  check("report money is unscaled (NT$ with thousands separators, never divided)",
    /NT\$[\d,]+/.test(twdBlock) && !twdBlock.includes("NT$0.00"),
    twdBlock.replace(/\s+/g, " ").slice(0, 200));

  // 筆數不是金額。這是本 change 裡「寫錯不會噴錯」的那一個地方：一筆訂單丟進
  // formatMoney 會得到「NT$1」——數字對、單位錯、看起來完全合理。
  const counted = (await page.textContent('[data-currency="TWD"] [data-k="counted"]')) ?? "";
  check("order counts are counts, never money (NT$1 for one order is the silent bug here)",
    counted.includes("筆") && !counted.includes("NT$"), counted);
  const periodTotal = (await page.textContent('[data-currency="TWD"] [data-k="period-total"]')) ?? "";
  check("the reconciliation total is on screen so the merchant can check the report",
    periodTotal.includes("筆") && !periodTotal.includes("NT$"), periodTotal);
  check("the four excluded/included classes are all shown",
    (await page.locator('[data-currency="TWD"] [data-k]').count()) === 5,
    String(await page.locator('[data-currency="TWD"] [data-k]').count()));

  // 商品排行：售出數量是數量，不是金額。
  const rankingRow = page.locator('[data-currency="TWD"] tr[data-sku]').first();
  await rankingRow.waitFor({ timeout: 8000 });
  const rankingText = (await rankingRow.textContent()) ?? "";
  check("the ranking shows quantities as quantities and amounts as money",
    rankingText.includes("件") && /NT\$[\d,]+/.test(rankingText) && !rankingText.includes("NT$1 件"),
    rankingText.replace(/\s+/g, " "));
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
