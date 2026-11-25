// E2E happy path (task 10.1) through the real browser:
// seed 建店 → admin UI 登入 → 編輯全域內容與首頁 → 發佈 → 以 publishable key
// 讀取公開渲染 bundle（內容儲存的公開讀取面）。
//
// Since change commerce-demo-seed this script is the "run everything" entry
// point: after the CMS flow above it also runs the commerce chain
// (e2e/commerce-path.mjs) and reports one combined result. Keeping both behind
// the single documented command is deliberate — a second entry point nobody is
// told to run is a suite nobody runs (design D2). The commerce path needs no
// browser and can also be run on its own: `node commerce-path.mjs`.
//
// Prereqs: API on :8080, admin on :3001, seeded demo shop
// (make migrate && make seed-demo). Chromium resolves *.localhost → 127.0.0.1.
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

import { applicationBody, runApplicationPath, submitApplication, tokenFromMessage } from "./application-path.mjs";
import { API, brief, check, deliverWebhook, dotEnvValue, fail, failureCount, isoDate, req, runID, setPlatformToken } from "./lib.mjs";
import { runCommercePath } from "./commerce-path.mjs";
import { runContentOpsPath } from "./content-ops-path.mjs";

// Same source `make seed` used: E2E_* override → env → repo .env → old default.
const ADMIN_EMAIL =
  process.env.E2E_ADMIN_EMAIL || process.env.SEED_ADMIN_EMAIL || dotEnvValue("SEED_ADMIN_EMAIL") || "admin@example.com";
const ADMIN_PASSWORD =
  process.env.E2E_ADMIN_PASSWORD ||
  process.env.SEED_ADMIN_PASSWORD ||
  dotEnvValue("SEED_ADMIN_PASSWORD") ||
  "admin-change-me";

// Base URLs are overridable so the suite can run against a second stack
// (e.g. an API on a fresh database) without touching the developer's running
// one. Defaults are the documented :3001/:8080.
const ADMIN = (process.env.E2E_ADMIN_BASE_URL ?? "http://localhost:3001").replace(/\/+$/, "");

const browser = await chromium.launch();
const page = await browser.newPage();
// The admin SPA reads its API base from localStorage at runtime
// (admin/lib/api.js); point it at the same API this run uses.
await page.addInitScript((api) => {
  try {
    localStorage.setItem("apiBase", api);
  } catch {}
}, API);
// The public read surface of the content store, addressed with the demo shop's
// publishable key (an identifier, not a credential).
const DEMO_PK = "pk_demoseedstorefrontkeynotsecret";
const renderPublic = (path = "/") =>
  req("GET", `/api/v1/render/page?path=${encodeURIComponent(path)}`, { pk: DEMO_PK });
page.on("pageerror", (err) => fail(`page JS error: ${err.message}`));

try {
  // ── 9.1 登入 ──
  console.log("① Admin login");
  await page.goto(`${ADMIN}/`);
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

  // ── 公開讀取：已發佈首頁經 publishable key 取得 ──
  console.log("⑤ The published home is served by the public render API");
  const pubHome = await renderPublic("/");
  check("render API serves the published home", pubHome.status === 200, brief(pubHome));
  const heroTitle0 = (pubHome.json?.page?.content?.sections ?? []).find((x) => x.type === "hero")?.title ?? "";
  check("the bundle carries the edited hero title", heroTitle0.includes("E2E 全新首頁標題"), heroTitle0);
  check("the bundle carries a page title", (pubHome.json?.page?.title ?? "").length > 0);

  // ── 9.3 全域內容編輯 → 立即生效 ──
  console.log("⑥ Shop global content update");
  await page.goto(`${ADMIN}/`);
  await page.waitForSelector("#view-main:not([hidden])");
  await page.click('.tab[data-tab="content"]');
  await page.waitForSelector("#content-form fieldset");
  const siteTitleInput = page.locator('#content-form input[data-pointer="/header/site_title"]');
  await siteTitleInput.fill("E2E 招牌");
  page.once("dialog", (d) => d.accept());
  await page.click("#content-save");
  await page.waitForTimeout(400);
  const pubAfter = await renderPublic("/");
  check("the render API reflects the updated site title at once",
    pubAfter.json?.shop?.content?.header?.site_title === "E2E 招牌", JSON.stringify(pubAfter.json?.shop?.content?.header));

  // ── change cms-rich-text-sanitization：rich text 於寫入時消毒，前台不執行注入 ──
  console.log("⑥b Rich text is sanitized on write and the public API serves only the sanitized markup");
  {
    const tok = (await req("POST", "/api/v1/admin/auth/login", {
      body: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD },
    })).json.access_token;
    const list = await req("GET", "/api/v1/admin/shops/1/pages", { token: tok });
    const home = (list.json?.pages ?? []).find((p) => p.slug === "home");
    const cur = await req("GET", `/api/v1/admin/shops/1/pages/${home?.id}`, { token: tok });
    const sections = [...(cur.json?.content_json?.sections ?? [])].filter((s) => s.type !== "rich_text");
    sections.push({
      type: "rich_text",
      html: '<p id="rt-e2e">安全段落</p><img src="https://img.invalid/x.png" onerror="window.__xss=1">' +
        '<script>window.__xss=2</script><a href="javascript:window.__xss=3">連結</a>' +
        '<table><tr><td colspan="2">表格</td></tr></table>',
    });
    const put = await req("PUT", `/api/v1/admin/shops/1/pages/${home?.id}`, {
      token: tok, body: { content: { sections } },
    });
    const stored = (put.json?.content_json?.sections ?? []).find((s) => s.type === "rich_text")?.html ?? "";
    check("rich text save succeeds (rewrite, not reject)", put.status === 200, brief(put));
    check("stored rich text has no onerror/script/javascript:",
      !/onerror|<script|javascript:/i.test(stored), stored);
    check("stored rich text keeps the table", stored.includes('colspan="2"'), stored);
    const pub = await req("POST", `/api/v1/admin/shops/1/pages/${home?.id}/publish`, { token: tok });
    check("publish after rich text edit", pub.status === 200, brief(pub));

    const served = (await renderPublic("/")).json?.page?.content?.sections?.find((x) => x.type === "rich_text")?.html ?? "";
    check("the public API serves the sanitized table", served.includes('colspan="2"'), served);
    check("the public API markup has no onerror, script or javascript: link",
      !/onerror|<script|javascript:/i.test(served), served);
  }

  const login = await req("POST", "/api/v1/admin/auth/login", {
    body: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD },
  });
  const adminToken = login.json.access_token;
  setPlatformToken(adminToken);

  // ══ 商品目錄後台（change admin-commerce-catalog, design D10）══
  //
  // 建立分類 → 建立含 2 個 SKU 的商品 → 金額顯示未被縮放 → 改一個 SKU 的庫存
  // → 另一個 SKU 仍在（守著 design D8 的全量 upsert 語意）→ 發佈 → 公開端點。
  const catSlug = `e2e-cat-${runID}`;
  const prodSlug = `e2e-product-${runID}`;
  const skuA = `E2E-${runID}-A`;
  const skuB = `E2E-${runID}-B`;

  console.log("⑧ Category CRUD in the admin UI");
  await page.goto(`${ADMIN}/`);
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
  const prodSubtitle = `E2E Carnation Bouquet ${runID}`;
  await page.fill("#product-subtitle", prodSubtitle);
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
  check("subtitle saved in the admin editor is on the public product (change product-subtitle-public-categories)",
    published?.subtitle === prodSubtitle, String(published?.subtitle));
  check("public product carries its category summary",
    (published?.categories ?? []).some((c) => c.slug === catSlug && c.id === Number(newCategoryID)),
    JSON.stringify(published?.categories));

  // ══ 商品圖片（change product-images-r2）══
  //
  // 走真正的 MinIO（presigned PUT 直傳、伺服器轉檔成 WebP）。API 未設定媒體儲存時
  // 整段略過——那本身是被支援的部署形態（路由回 503），不是失敗。
  console.log("⑫a 商品圖片：在編輯器上傳兩張（含 EXIF 方向 6）、改 alt、重排 → 公開詳情");
  const mediaCfg = await req("GET", "/api/v1/admin/media/config", { token: adminToken });
  if (mediaCfg.json?.enabled !== true) {
    console.log("  - media storage is not configured on this API: image checks skipped");
  } else {
    await page.waitForSelector("#product-image-input", { state: "attached" });
    const fixture = fileURLToPath(new URL("./fixtures/exif-orientation-6.jpg", import.meta.url));
    await page.setInputFiles("#product-image-input", [fixture, fixture]);
    const imageRows = page.locator("#product-images .image-row");
    await page.waitForFunction(
      () => document.querySelectorAll("#product-images .image-row").length === 2, null, { timeout: 60000 });
    check("two images uploaded through the editor (presign -> direct PUT -> server conversion)",
      (await imageRows.count()) === 2);
    const idA = Number(await imageRows.nth(0).getAttribute("data-image-id"));
    const idB = Number(await imageRows.nth(1).getAttribute("data-image-id"));
    check("the file input is the only way images enter: product save button is not involved",
      (await page.locator("#product-images").count()) === 1);

    const adminImages = async () =>
      (await req("GET", `/api/v1/admin/shops/1/products/${published.id}/images`, { token: adminToken })).json?.images ?? [];
    const until = async (name, pred) => {
      for (let i = 0; i < 50; i++) {
        if (pred(await adminImages())) return true;
        await new Promise((r) => setTimeout(r, 200));
      }
      return check(name, false);
    };

    const altText = `E2E 第一張 ${runID}`;
    await imageRows.nth(0).locator(".image-alt").fill(altText);
    await imageRows.nth(0).locator(".image-alt").press("Tab"); // change event -> PUT alt
    await until("alt saved", (imgs) => imgs[0]?.alt === altText);
    await imageRows.nth(1).locator(".image-up").click();
    await until("reordered", (imgs) => imgs[0]?.id === idB && imgs[1]?.id === idA);

    const pub = await req("GET", `/api/v1/shop/products/${prodSlug}`, { pk: "pk_demoseedstorefrontkeynotsecret" });
    const pimgs = pub.json?.images ?? [];
    check("public detail lists both images in the reordered order",
      pimgs.length === 2 && pimgs[0].id === idB && pimgs[1].id === idA, JSON.stringify(pimgs.map((i) => i.id)));
    check("alt set in the editor is on the public image", pimgs[1]?.alt === altText, JSON.stringify(pimgs[1]));
    check("EXIF orientation 6 was applied by the server (600x400 landscape -> 400x600 portrait)",
      pimgs[0]?.width === 400 && pimgs[0]?.height === 600, JSON.stringify(pimgs[0]));
    const large = await fetch(pimgs[0].urls.large);
    check("urls.large is publicly readable as image/webp",
      large.status === 200 && large.headers.get("content-type") === "image/webp",
      `${large.status} ${large.headers.get("content-type")}`);
    check("variant objects are immutable-cacheable",
      (large.headers.get("cache-control") ?? "").includes("immutable"), String(large.headers.get("cache-control")));
    const thumb = await fetch(pimgs[0].urls.thumb);
    check("urls.thumb is publicly readable", thumb.status === 200, String(thumb.status));
    const bytes = new Uint8Array(await large.arrayBuffer());
    check("the stored variant is a real WebP (RIFF....WEBP) with no EXIF chunk",
      String.fromCharCode(...bytes.slice(0, 4)) === "RIFF" && String.fromCharCode(...bytes.slice(8, 12)) === "WEBP" &&
        !Buffer.from(bytes).includes("EXIF"));
    // 圖片是子資源：商品「儲存」（全量 PUT）不得影響它們。
    await page.click("#product-save");
    await page.waitForFunction(() =>
      document.querySelector("#product-editor-status").textContent.includes("已儲存"));
    check("saving the product does not touch its images", (await adminImages()).length === 2);
  }

  console.log("⑫b 分類視圖調整「本季新品」商品排序 → 公開列表順序一致，結束時還原");
  const PK_ORDER = "pk_demoseedstorefrontkeynotsecret";
  const orderOf = async () => {
    const r = await req("GET", "/api/v1/shop/products?category=new-arrivals", { pk: PK_ORDER });
    return (r.json?.products ?? []).map((p) => p.slug);
  };
  const catsPublic = await req("GET", "/api/v1/shop/categories", { pk: PK_ORDER });
  check("public category tree lists new-arrivals",
    (catsPublic.json?.categories ?? []).some((c) => c.slug === "new-arrivals"), brief(catsPublic));
  const originalOrder = await orderOf();
  check("new-arrivals holds at least two published products", originalOrder.length >= 2, JSON.stringify(originalOrder));

  const moveLastUp = async () => {
    await page.click('.tab[data-tab="categories"]');
    await page.waitForSelector("#tab-categories:not([hidden])");
    await page.click('#categories-tbody tr:has-text("new-arrivals") .category-order-btn');
    await page.waitForSelector("#category-order-dialog[open]");
    await page.waitForSelector("#category-order-list li[data-product-id]");
    await page.locator("#category-order-list li[data-product-id]").last().locator(".order-up").click();
    await page.click("#category-order-save");
    await page.waitForSelector("#category-order-dialog[open]", { state: "hidden", timeout: 8000 });
  };
  await moveLastUp();
  const reordered = await orderOf();
  const expectedMoved = [...originalOrder];
  const lastItem = expectedMoved.length - 1;
  [expectedMoved[lastItem - 1], expectedMoved[lastItem]] = [expectedMoved[lastItem], expectedMoved[lastItem - 1]];
  check("public list order follows the order saved in the category view",
    JSON.stringify(reordered) === JSON.stringify(expectedMoved),
    `${JSON.stringify(reordered)} vs ${JSON.stringify(expectedMoved)}`);
  await moveLastUp();
  check("moving the same pair back restores the original order (repeatable)",
    JSON.stringify(await orderOf()) === JSON.stringify(originalOrder), JSON.stringify(await orderOf()));

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
  await page.goto(`${ADMIN}/#/orders/${opsOrder.id}`);
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
  // Shipments require a paid order (change order-and-invitation-polish).
  const partPay = await req("POST", "/api/v1/shop/guest/order/payments", {
    token: partPlaced.json?.order_access_token, pk: PK, body: {},
  });
  const partHook = await deliverWebhook(partPay.json?.provider_reference);
  check("the partial-shipment order was paid so it can ship", partPay.status === 201 && partHook.status === 200,
    `${partPay.status}/${partHook.status}`);
  const partLines = partOrder.items ?? [];
  const lineOf = (code) => partLines.find((i) => i.sku_code === code);

  await page.goto(`${ADMIN}/#/orders/${partOrder.id}`);
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
  await page.goto(`${ADMIN}/#/orders/${opsOrder.id}`);
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

  await page.goto(`${ADMIN}/#/orders/${repeatOrder.id}`);
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
  await page.goto(`${ADMIN}/#/reports`);
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

  console.log("㉔ 平台開店：精靈 → 設定連結 → 新 owner 設密碼登入 → 停用後被踢回登入（change platform-account-provisioning）");
  // 每輪唯一的店名、網域與 email；不碰 demo 商家，所以可重複執行。
  const provShop = `E2E Provisioned ${runID}`;
  const provOwner = `e2e-owner-${runID}@example.test`;
  // 測試值，只在本機即時產生，不寫入任何檔案。
  const provPassword = `pw-${runID}-${Math.random().toString(36).slice(2, 10)}`;

  await page.goto(`${ADMIN}/#/platform-shops`);
  await page.waitForSelector("#tab-platform-shops:not([hidden])", { timeout: 8000 });
  check("the platform admin sees the platform tabs",
    (await page.locator('.tab[data-tab="platform-shops"]').count()) === 1 &&
    (await page.locator('.tab[data-tab="platform-accounts"]').count()) === 1);

  await page.click("#platform-shop-new-btn");
  await page.waitForSelector("#platform-shop-dialog[open]");
  await page.fill("#platform-shop-name", provShop);
  await page.fill("#platform-shop-owner", provOwner);
  await page.click("#platform-shop-form button[type=submit]");
  await page.waitForSelector("#platform-shop-result .link-once", { timeout: 8000 });
  const setupLink = await page.inputValue("#platform-shop-result .link-once");
  check("the wizard shows the owner's one-time setup link", setupLink.includes("/#/set-password/"), setupLink.slice(0, 60));
  const resultText = (await page.textContent("#platform-shop-result")) ?? "";
  check("the link is labelled as shown once, with its lifetime",
    resultText.includes("只顯示這一次") && resultText.includes("小時後失效"), resultText.replace(/\s+/g, " ").slice(0, 160));
  check("the checklist shows the owner assigned but the password not yet set",
    (await page.locator('#platform-shop-result [data-check="owner_assigned"]').getAttribute("data-done")) === "true" &&
    (await page.locator('#platform-shop-result [data-check="owner_password_set"]').getAttribute("data-done")) === "false");
  check("the checklist no longer has theme or domain items",
    (await page.locator('#platform-shop-result [data-check="theme_applied"], #platform-shop-result [data-check="domain_bound"]').count()) === 0);
  check("the new shop is in the list", (await page.locator(`#platform-shops-tbody tr[data-name="${provShop}"]`).count()) === 1);

  // 另一個瀏覽器 context（沒有任何登入狀態）：新 owner 開連結。
  const ownerCtx = await browser.newContext();
  await ownerCtx.addInitScript((api) => {
    try {
      localStorage.setItem("apiBase", api);
    } catch {}
  }, API);
  const ownerPage = await ownerCtx.newPage();
  ownerPage.on("pageerror", (err) => fail(`owner page JS error: ${err.message}`));
  try {
    await ownerPage.goto(setupLink);
    await ownerPage.waitForSelector("#sp-form:not([hidden])", { timeout: 8000 });
    check("the set-password page shows the account email without any login",
      ((await ownerPage.textContent("#sp-email")) ?? "") === provOwner);
    await ownerPage.fill("#sp-password", provPassword);
    await ownerPage.fill("#sp-password2", provPassword + "x");
    await ownerPage.click("#sp-form button[type=submit]");
    await ownerPage.waitForSelector("#sp-error:not([hidden])");
    check("mismatched passwords are rejected client-side", true);
    await ownerPage.fill("#sp-password2", provPassword);
    await ownerPage.click("#sp-form button[type=submit]");
    await ownerPage.waitForSelector("#view-login:not([hidden])", { timeout: 8000 });
    check("after setting the password the UI returns to the login page with the email prefilled",
      (await ownerPage.inputValue("#login-email")) === provOwner);
    const stored = await ownerPage.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }));
    const linkToken = setupLink.split("/#/set-password/")[1];
    check("the token is never written to any persistent storage", !stored.includes(linkToken));
    check("the token is gone from the address bar", !ownerPage.url().includes(linkToken));

    // 用過的連結：只顯示無效說明，沒有密碼輸入框。
    const reuse = await ownerCtx.newPage();
    await reuse.goto(setupLink);
    await reuse.waitForSelector("#sp-invalid:not([hidden])", { timeout: 8000 });
    check("a used link shows the invalid-link notice and no password form",
      (await reuse.isVisible("#sp-form")) === false &&
      ((await reuse.textContent("#sp-invalid")) ?? "").includes("連結無效或已過期"));
    await reuse.close();

    // 以新密碼登入，看到自己的商家；沒有平台視圖。
    await ownerPage.fill("#login-password", provPassword);
    await ownerPage.click("#login-form button[type=submit]");
    await ownerPage.waitForSelector("#view-main:not([hidden])", { timeout: 8000 });
    check("the new owner can log in with the password they set",
      ((await ownerPage.textContent("#whoami")) ?? "").includes(provOwner));
    check("the owner is a member of exactly the new shop",
      (await ownerPage.locator("#shop-select option").count()) === 1);
    await ownerPage.waitForSelector("#tabs .tab", { timeout: 8000 });
    check("a merchant owner sees no platform tabs",
      (await ownerPage.locator('.tab[data-tab="platform-shops"], .tab[data-tab="platform-accounts"], .tab[data-tab="platform-applications"]').count()) === 0);

    // super admin 停用該 owner；owner 的下一個操作被拒並被導回登入。
    await page.goto(`${ADMIN}/#/platform-accounts`);
    await page.waitForSelector("#tab-platform-accounts:not([hidden])", { timeout: 8000 });
    await page.fill("#platform-account-q", provOwner);
    await page.click("#platform-account-search");
    const acctRow = page.locator(`#platform-accounts-tbody tr[data-email="${provOwner}"]`);
    await acctRow.waitFor({ timeout: 8000 });
    check("the account list shows the password as set",
      ((await acctRow.textContent()) ?? "").includes("已設定"));
    await acctRow.locator("button", { hasText: "詳情" }).click();
    await page.waitForSelector('#platform-account-detail [data-act="disable"]', { timeout: 8000 });
    page.once("dialog", (d) => d.accept());
    await page.click('#platform-account-detail [data-act="disable"]');
    await page.waitForSelector('#platform-account-detail [data-act="enable"]', { timeout: 8000 });
    check("the account is disabled from the platform view", true);

    await ownerPage.click('#tabs .tab[data-tab="orders"]');
    await ownerPage.waitForSelector("#view-login:not([hidden])", { timeout: 8000 });
    check("the disabled owner's next action is refused and sends them back to the login page", true);
    const relogin = await req("POST", "/api/v1/admin/auth/login", { body: { email: provOwner, password: provPassword } });
    check("the disabled owner can no longer log in", relogin.status === 401, brief(relogin));
  } finally {
    await ownerCtx.close();
  }

  // checklist from the platform side: the owner has set the password.
  await page.goto(`${ADMIN}/#/platform-shops`);
  await page.waitForSelector(`#platform-shops-tbody tr[data-name="${provShop}"]`, { timeout: 8000 });
  await page.locator(`#platform-shops-tbody tr[data-name="${provShop}"] button`).click();
  await page.waitForSelector('#platform-shop-checklist [data-check="owner_password_set"]', { timeout: 8000 });
  check("the checklist now shows the owner's password as set",
    (await page.locator('#platform-shop-checklist [data-check="owner_password_set"]').getAttribute("data-done")) === "true");

  console.log("㉔b 開店申請審核 UI：列表／篩選／詳情、outbox 訊息、核准（連結只顯示一次）、拒絕（change platform-website）");
  {
    const uiApprove = `e2e-ui-apply-${runID}@example.com`;
    const uiReject = `e2e-ui-reject-${runID}@example.com`;
    await submitApplication(applicationBody(uiApprove, { shop_name: `E2E UI 申請店 ${runID}` }));
    await submitApplication(applicationBody(uiReject));
    await page.goto(`${ADMIN}/#/platform-applications`);
    await page.waitForSelector("#tab-platform-applications:not([hidden])", { timeout: 8000 });
    await page.waitForSelector('.tab[data-tab="platform-applications"] .nav-count', { timeout: 8000 }).catch(() => {});
    check("the platform admin sees the applications tab with a pending badge",
      (await page.locator('.tab[data-tab="platform-applications"]').count()) === 1 &&
      (await page.locator('.tab[data-tab="platform-applications"] .nav-count').count()) === 1);
    await page.selectOption("#platform-applications-filter", "pending_verification");
    const rowSel = (email) => `#platform-applications-tbody tr[data-email="${email}"]`;
    await page.waitForSelector(rowSel(uiApprove), { timeout: 8000 });
    check("the status filter lists unverified applications, marked unverified",
      ((await page.textContent(rowSel(uiApprove))) ?? "").includes("待驗證信箱"));
    await page.click(`${rowSel(uiApprove)} [data-act="open"]`);
    await page.waitForSelector("#platform-application-detail [data-message]", { timeout: 8000 });
    const outboxText = (await page.textContent("#platform-application-detail [data-message] pre")) ?? "";
    const smtpOn = !outboxText.includes("/apply/verify#");
    check("the detail shows the outbox verification mail (manual delivery) with a copy button and mark-sent",
      smtpOn || ((await page.locator('#platform-application-detail [data-message] button', { hasText: "複製內文" }).count()) === 1 &&
        (await page.locator('#platform-application-detail [data-act="mark-sent"]').count()) === 1));
    check("an unverified application offers manual confirmation but no approve button",
      (await page.locator('#platform-application-detail [data-act="approve"]').count()) === 0 &&
      (await page.locator('#platform-application-detail [data-act="verify-manually"]').count()) === 1);
    if (!smtpOn) {
      await page.click('#platform-application-detail [data-act="mark-sent"]');
      await page.waitForSelector('#platform-application-detail [data-event="message_marked_sent"]', { timeout: 8000 });
      check("marking the message sent updates it and the timeline",
        ((await page.textContent("#platform-application-detail")) ?? "").includes("標記訊息已寄出"));
      const vtok = tokenFromMessage(outboxText);
      const v = await req("POST", "/api/v1/public/shop-applications/verify", { body: { token: vtok } });
      check("the applicant verifies through the link's token", v.status === 200, brief(v));
    } else {
      page.once("dialog", (d) => d.accept());
      await page.click('#platform-application-detail [data-act="verify-manually"]');
    }
    await page.selectOption("#platform-applications-filter", "pending_review");
    await page.waitForSelector(rowSel(uiApprove), { timeout: 8000 });
    await page.click(`${rowSel(uiApprove)} [data-act="open"]`);
    await page.waitForSelector('#platform-application-detail [data-act="approve"]', { timeout: 8000 });
    await page.click('#platform-application-detail [data-act="approve"]');
    await page.waitForSelector("#platform-application-approve-dialog[open]");
    check("the approve dialog defaults the shop to under review",
      (await page.inputValue("#platform-application-approve-status")) === "2" &&
      (await page.inputValue("#platform-application-approve-name")).includes("E2E UI 申請店"));
    await page.click("#platform-application-approve-form button[type=submit]");
    await page.waitForSelector("#platform-application-result .link-once", { timeout: 8000 });
    const appLink = await page.inputValue("#platform-application-result .link-once");
    const resultText2 = (await page.textContent("#platform-application-result")) ?? "";
    check("approval shows the owner's one-time setup link, only once",
      appLink.includes("/#/set-password/") && resultText2.includes("只顯示這一次"), appLink.slice(0, 60));
    const afterStorage = await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }));
    check("the link is written to no storage", !afterStorage.includes(appLink.split("/#/set-password/")[1]));
    await page.selectOption("#platform-applications-filter", "approved");
    await page.waitForSelector(rowSel(uiApprove), { timeout: 8000 });
    check("the approved application is listed as approved",
      ((await page.textContent(rowSel(uiApprove))) ?? "").includes("已核准"));
    await page.click(`${rowSel(uiApprove)} [data-act="open"]`);
    await page.waitForSelector("#platform-application-detail .kv", { timeout: 8000 });
    check("the approved detail has no approve/reject buttons and no link anywhere",
      (await page.locator('#platform-application-detail [data-act="approve"], #platform-application-detail [data-act="reject"]').count()) === 0 &&
      !((await page.textContent("#platform-application-detail")) ?? "").includes("set-password"));

    await page.selectOption("#platform-applications-filter", "pending_verification");
    await page.waitForSelector(rowSel(uiReject), { timeout: 8000 });
    await page.click(`${rowSel(uiReject)} [data-act="open"]`);
    await page.waitForSelector('#platform-application-detail [data-act="reject"]', { timeout: 8000 });
    await page.click('#platform-application-detail [data-act="reject"]');
    await page.waitForSelector("#platform-application-reject-dialog[open]");
    await page.fill("#platform-application-reject-reason", `UI 拒絕理由 ${runID}`);
    await page.click("#platform-application-reject-form button[type=submit]");
    await page.waitForSelector('#platform-application-detail [data-event="rejected"]', { timeout: 8000 });
    const rejDetail = (await page.textContent("#platform-application-detail")) ?? "";
    check("the rejection is recorded with the reason the applicant will see",
      rejDetail.includes(`UI 拒絕理由 ${runID}`) && rejDetail.includes("已拒絕"));
  }

  console.log("㉕ 店員 staff：導覽只有訂單、可出貨與看付款紀錄、被禁止的動作回 403（change merchant-staff-role）");
  const SHOP = 1;
  const adminLogin = async (email, password) =>
    (await req("POST", "/api/v1/admin/auth/login", { body: { email, password } })).json?.access_token;
  const newAdminCtx = async () => {
    const c = await browser.newContext();
    await c.addInitScript((api) => {
      try {
        localStorage.setItem("apiBase", api);
      } catch {}
    }, API);
    return c;
  };
  const uiLogin = async (p, email, password) => {
    await p.goto(`${ADMIN}/`);
    await p.fill("#login-email", email);
    await p.fill("#login-password", password);
    await p.click("#login-form button[type=submit]");
    await p.waitForSelector("#view-main:not([hidden])", { timeout: 8000 });
    await p.waitForSelector("#tabs .tab", { timeout: 8000 });
  };
  const tabsOf = async (p) => p.$$eval("#tabs .tab", (ns) => ns.map((n) => n.dataset.tab));

  const ownerTok = await adminLogin("demo-owner@example.com", "demo-owner-change-me");
  const staffTok = await adminLogin("demo-staff@example.com", "demo-staff-change-me");
  check("the seeded clerk can log in", Boolean(staffTok));

  // 兩筆已付款訂單：一筆走 UI 出貨，一筆走 API 驗證退貨被拒。
  const placePaid = async (label) => {
    const o = await req("POST", "/api/v1/shop/guest/checkout", {
      pk: PK,
      body: {
        email: `e2e-staff-${label}-${runID}@example.com`,
        items: [{ sku_id: skuID, quantity: 1 }],
        shipping_address: { recipient_name: `E2E 店員單 ${label} ${runID}`, phone: "0955123456", line1: "台北市大安區信義路四段 1 號" },
      },
    });
    const pay = await req("POST", "/api/v1/shop/guest/order/payments", { token: o.json?.order_access_token, pk: PK, body: {} });
    await deliverWebhook(pay.json?.provider_reference);
    return o.json?.order ?? {};
  };
  const staffOrderUI = await placePaid("ui");
  const staffOrderAPI = await placePaid("api");
  check("two paid orders exist for the clerk", Boolean(staffOrderUI.id) && Boolean(staffOrderAPI.id));

  const staffCtx = await newAdminCtx();
  try {
    const sp = await staffCtx.newPage();
    sp.on("pageerror", (err) => fail(`staff page JS error: ${err.message}`));
    await uiLogin(sp, "demo-staff@example.com", "demo-staff-change-me");
    check("the clerk's navigation contains only the orders view",
      JSON.stringify(await tabsOf(sp)) === JSON.stringify(["orders"]), JSON.stringify(await tabsOf(sp)));

    await sp.goto(`${ADMIN}/#/orders/${staffOrderUI.id}`);
    await sp.waitForSelector("#order-amounts", { timeout: 8000 });
    await sp.waitForSelector("#order-payments-tbody tr", { timeout: 8000 });
    check("the clerk sees the order's payment record (payment.view)",
      ((await sp.textContent("#order-payments")) ?? "").includes("mock"), ((await sp.textContent("#order-payments")) ?? "").slice(0, 120));
    check("the clerk has no cancel button", (await sp.isVisible("#order-cancel-btn")) === false);

    await sp.waitForSelector("#create-shipment-btn", { timeout: 8000 });
    await sp.fill("#shipment-carrier", "E2E 快遞");
    await sp.fill("#shipment-tracking", `E2E-STAFF-${runID}`);
    await sp.click("#create-shipment-btn");
    await sp.waitForSelector("#shipment-detail", { timeout: 8000 });
    check("the clerk created a shipment", ((await sp.textContent("#order-shipment")) ?? "").includes(`E2E-STAFF-${runID}`));
    check("the clerk can mark it delivered but has no return button",
      (await sp.locator("#shipment-delivered-btn").count()) === 1 && (await sp.locator("#shipment-returned-btn").count()) === 0);
    sp.once("dialog", (d) => d.accept());
    await sp.click("#shipment-delivered-btn");
    await sp.waitForSelector("#shipment-terminal-note", { timeout: 8000 });
    check("the clerk marked the shipment delivered",
      ((await sp.textContent("#order-shipment")) ?? "").includes("已送達"));
  } finally {
    await staffCtx.close();
  }

  // 被禁止的動作：直接打 API，一律 403，且不改變任何東西。
  const staffShip = await req("POST", `/api/v1/admin/shops/${SHOP}/orders/${staffOrderAPI.id}/shipments`, {
    token: staffTok, body: { carrier: "E2E 快遞", tracking_number: `E2E-STAFF-API-${runID}` },
  });
  check("the clerk can create a shipment through the API", staffShip.status === 201, brief(staffShip));
  const staffReturn = await req("PUT", `/api/v1/admin/shops/${SHOP}/orders/${staffOrderAPI.id}/shipments/${staffShip.json?.id}`, {
    token: staffTok, body: { status: "returned" },
  });
  check("the clerk cannot return a shipment (403)", staffReturn.status === 403, brief(staffReturn));
  const afterReturn = await req("GET", `/api/v1/admin/shops/${SHOP}/orders/${staffOrderAPI.id}`, { token: ownerTok });
  check("the refused return changed nothing (order still shipped)", afterReturn.json?.fulfillment_status === 1, brief(afterReturn));
  const unpaid = await req("POST", "/api/v1/shop/guest/checkout", {
    pk: PK,
    body: {
      email: `e2e-staff-unpaid-${runID}@example.com`,
      items: [{ sku_id: skuID, quantity: 1 }],
      shipping_address: { recipient_name: `E2E 未付款 ${runID}`, phone: "0955123456", line1: "台北市大安區信義路四段 1 號" },
    },
  });
  const staffCancel = await req("POST", `/api/v1/admin/shops/${SHOP}/orders/${unpaid.json?.order?.id}/cancel`, { token: staffTok });
  check("the clerk cannot cancel even an unpaid order (403)", staffCancel.status === 403, brief(staffCancel));
  const staffEdit = await req("PUT", `/api/v1/admin/shops/${SHOP}/products/${published?.id}`, { token: staffTok, body: {} });
  check("the clerk cannot edit a product (403)", staffEdit.status === 403, brief(staffEdit));
  const staffMembers = await req("GET", `/api/v1/admin/shops/${SHOP}/members`, { token: staffTok });
  check("the clerk cannot read the customer roster (403)", staffMembers.status === 403, brief(staffMembers));
  const staffInvite = await req("POST", `/api/v1/admin/shops/${SHOP}/invitations`, { token: staffTok, body: { email: "x@example.test", role_id: 1 } });
  check("the clerk cannot invite members (403)", staffInvite.status === 403, brief(staffInvite));
  const staffPayments = await req("GET", `/api/v1/admin/shops/${SHOP}/orders/${staffOrderUI.id}/payments`, { token: staffTok });
  check("the clerk can read payment records (200)", staffPayments.status === 200, brief(staffPayments));
  const ownerCancel = await req("POST", `/api/v1/admin/shops/${SHOP}/orders/${unpaid.json?.order?.id}/cancel`, { token: ownerTok });
  check("the owner can cancel that unpaid order (200)", ownerCancel.status === 200, brief(ownerCancel));

  console.log("㉖ owner 邀請店員 → 新 context 開連結設密碼 → 只看到訂單 → owner 停用 → 下一個操作 403（change merchant-staff-role）");
  const inviteEmail = `e2e-clerk-${runID}@example.test`;
  // 測試值，只在本機即時產生，不寫入任何檔案。
  const invitePassword = `pw-${runID}-${Math.random().toString(36).slice(2, 10)}`;
  const ownerCtx2 = await newAdminCtx();
  const clerkCtx = await newAdminCtx();
  try {
    const op = await ownerCtx2.newPage();
    op.on("pageerror", (err) => fail(`owner page JS error: ${err.message}`));
    await uiLogin(op, "demo-owner@example.com", "demo-owner-change-me");
    check("the owner sees the members view", (await tabsOf(op)).includes("team"), JSON.stringify(await tabsOf(op)));
    await op.goto(`${ADMIN}/#/team`);
    await op.waitForSelector("#tab-team:not([hidden])", { timeout: 8000 });
    await op.waitForSelector('#team-members-tbody tr[data-email="demo-staff@example.com"]', { timeout: 8000 });
    const ownerRowText = (await op.textContent('#team-members-tbody tr[data-email="demo-owner@example.com"]')) ?? "";
    check("the owner's own row offers no actions",
      (await op.locator('#team-members-tbody tr[data-email="demo-owner@example.com"] button').count()) === 0, ownerRowText);
    const roleOptions = await op.$$eval("#team-invite-role option", (ns) => ns.map((n) => n.textContent));
    check("the invite role list offers staff and editor but never merchant_owner",
      roleOptions.includes("staff") && roleOptions.includes("editor") && !roleOptions.includes("merchant_owner"), JSON.stringify(roleOptions));

    await op.fill("#team-invite-email", inviteEmail);
    await op.selectOption("#team-invite-role", { label: "staff" });
    await op.click("#team-invite-form button[type=submit]");
    await op.waitForSelector("#team-link .link-once", { timeout: 8000 });
    const inviteLink = await op.inputValue("#team-link .link-once");
    check("the invite link is shown once, in the fragment", inviteLink.includes("/#/accept-invite/"), inviteLink.slice(0, 60));
    check("the invitation is listed as pending",
      (await op.locator(`#team-invites-tbody tr[data-email="${inviteEmail}"]`).count()) === 1);

    // 已有後台帳號的 email：連結不能設密碼，只能登入後接受（不改動任何東西）。
    const existingInvite = await req("POST", `/api/v1/admin/shops/${SHOP}/invitations`, {
      token: ownerTok, body: { email: ADMIN_EMAIL, role_id: (await req("GET", `/api/v1/admin/shops/${SHOP}/roles`, { token: ownerTok })).json?.roles?.find((r) => r.name === "staff")?.id },
    });
    check("inviting an e-mail that already has an account looks the same (201 + link)",
      existingInvite.status === 201 && Boolean(existingInvite.json?.invite_url), brief(existingInvite));
    const existingToken = (existingInvite.json?.invite_url ?? "").split("/#/accept-invite/")[1];
    const takeover = await req("POST", "/api/v1/admin/auth/invitations/accept", { body: { token: existingToken, password: "attacker-chosen-pw-1" } });
    check("the anonymous accept refuses an existing account (409)", takeover.status === 409, brief(takeover));
    const loginStill = await req("POST", "/api/v1/admin/auth/login", { body: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD } });
    check("the existing account's password is unchanged", loginStill.status === 200, brief(loginStill));
    const existingCtx = await newAdminCtx();
    try {
      const ep = await existingCtx.newPage();
      await ep.goto(existingInvite.json.invite_url);
      await ep.waitForSelector("#ai-login-form:not([hidden])", { timeout: 8000 });
      check("the accept page asks an existing account to sign in instead of setting a password",
        (await ep.isVisible("#ai-new-form")) === false && ((await ep.textContent("#ai-info")) ?? "").includes(ADMIN_EMAIL));
    } finally {
      await existingCtx.close();
    }
    const pending = await req("GET", `/api/v1/admin/shops/${SHOP}/invitations`, { token: ownerTok });
    check("every listed invitation carries an explicit status, and the live ones are pending",
      (pending.json?.invitations ?? []).length > 0 &&
        pending.json.invitations.every((i) => i.status === "pending" || i.status === "expired") &&
        pending.json.invitations.find((i) => i.email === inviteEmail)?.status === "pending",
      JSON.stringify(pending.json?.invitations?.map((i) => [i.email, i.status])));
    const adminInviteID = pending.json?.invitations?.find((i) => i.email === ADMIN_EMAIL.toLowerCase())?.id;
    const revoked = await req("DELETE", `/api/v1/admin/shops/${SHOP}/invitations/${adminInviteID}`, { token: ownerTok });
    check("that invitation is revoked again", revoked.status === 204, brief(revoked));

    // 新人：另一個沒有任何登入狀態的 context。
    const cp = await clerkCtx.newPage();
    cp.on("pageerror", (err) => fail(`clerk page JS error: ${err.message}`));
    await cp.goto(inviteLink);
    await cp.waitForSelector("#ai-new-form:not([hidden])", { timeout: 8000 });
    check("the accept page shows the shop, role and e-mail without any login",
      ((await cp.textContent("#ai-info")) ?? "").includes(inviteEmail) && ((await cp.textContent("#ai-role")) ?? "") === "staff");
    await cp.fill("#ai-password", invitePassword);
    await cp.fill("#ai-password2", invitePassword + "x");
    await cp.click("#ai-new-form button[type=submit]");
    await cp.waitForSelector("#ai-error:not([hidden])");
    check("mismatched passwords are rejected client-side", true);
    await cp.fill("#ai-password2", invitePassword);
    await cp.click("#ai-new-form button[type=submit]");
    await cp.waitForSelector("#view-login:not([hidden])", { timeout: 8000 });
    check("after accepting, the UI returns to the login page with the e-mail prefilled",
      (await cp.inputValue("#login-email")) === inviteEmail);
    const stored = await cp.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }));
    check("the invitation token is never written to persistent storage",
      !stored.includes(inviteLink.split("/#/accept-invite/")[1]));

    await cp.fill("#login-password", invitePassword);
    await cp.click("#login-form button[type=submit]");
    await cp.waitForSelector("#view-main:not([hidden])", { timeout: 8000 });
    await cp.waitForSelector("#tabs .tab", { timeout: 8000 });
    check("the invited clerk sees only the orders view", JSON.stringify(await tabsOf(cp)) === JSON.stringify(["orders"]), JSON.stringify(await tabsOf(cp)));

    // 一次性：用過的連結。
    const reuse = await clerkCtx.newPage();
    await reuse.goto(inviteLink);
    await reuse.waitForSelector("#ai-invalid:not([hidden])", { timeout: 8000 });
    check("a used invitation shows the invalid-link notice", (await reuse.isVisible("#ai-new-form")) === false);
    await reuse.close();

    // owner 停用（UI）；店員用「同一個 access token」的下一個請求被拒。
    const clerkTok = await adminLogin(inviteEmail, invitePassword);
    const before = await req("GET", `/api/v1/admin/shops/${SHOP}/orders`, { token: clerkTok });
    check("before the disable the clerk can list orders", before.status === 200, brief(before));
    await op.reload();
    await op.waitForSelector(`#team-members-tbody tr[data-email="${inviteEmail}"]`, { timeout: 8000 });
    op.once("dialog", (d) => d.accept());
    await op.click(`#team-members-tbody tr[data-email="${inviteEmail}"] [data-act="disable"]`);
    await op.waitForSelector(`#team-members-tbody tr[data-email="${inviteEmail}"] [data-act="enable"]`, { timeout: 8000 });
    check("the owner disabled the membership from the members view", true);
    const after = await req("GET", `/api/v1/admin/shops/${SHOP}/orders`, { token: clerkTok });
    check("the clerk's very next request is refused (403) with the same access token", after.status === 403, brief(after));
    const afterRelogin = await req("POST", "/api/v1/admin/auth/login", { body: { email: inviteEmail, password: invitePassword } });
    check("the account itself is not disabled (login still works)", afterRelogin.status === 200, brief(afterRelogin));
    const meAfter = await req("GET", "/api/v1/admin/me", { token: afterRelogin.json?.access_token });
    check("the disabled membership is gone from the new token's shops", !(meAfter.json?.sids ?? []).includes(SHOP), brief(meAfter));
    const clerkRow = await req("GET", `/api/v1/admin/shops/${SHOP}/users`, { token: ownerTok });
    const clerkID = clerkRow.json?.users?.find((u) => u.email === inviteEmail)?.user_id;
    const ownerDisable = await req("POST", `/api/v1/admin/shops/${SHOP}/users/${clerkRow.json?.users?.find((u) => u.email === "demo-owner@example.com")?.user_id}/disable`, { token: ownerTok });
    check("an owner cannot disable themself (409)", ownerDisable.status === 409, brief(ownerDisable));
    check("the clerk stays listed with a disabled membership",
      clerkRow.json?.users?.find((u) => u.user_id === clerkID)?.membership_status === 0, brief(clerkRow));
    const ownerAssign = await req("POST", `/api/v1/admin/shops/${SHOP}/users/${clerkID}/roles`, {
      token: ownerTok, body: { role_id: (await req("GET", `/api/v1/admin/shops/${SHOP}/roles`, { token: ownerTok })).json?.roles?.find((r) => r.name === "merchant_owner")?.id },
    });
    check("an owner cannot appoint another owner (403)", ownerAssign.status === 403, brief(ownerAssign));
  } finally {
    await ownerCtx2.close();
    await clerkCtx.close();
  }
  console.log("㉗ 後台 API 對外開放：CORS 依用途分流、admin 主機綁定、branding、cookie 模式 refresh（change admin-api-public-exposure）");
  {
    const adminOrigin = `https://admin-${runID}.e2e.example.com`;
    const storefrontOrigin = `https://front-${runID}.e2e.example.com`;
    const registered = [];
    const register = async (origin, purpose) => {
      const r = await req("POST", "/api/v1/admin/shops/1/allowed-origins", { token: adminToken, body: { origin, purpose } });
      if (r.status === 201) registered.push(r.json.id);
      return r;
    };
    try {
      const a = await register(adminOrigin, "admin");
      check("an admin-purpose origin registers (201, purpose echoed)", a.status === 201 && a.json?.purpose === "admin", brief(a));
      const sOrig = await register(storefrontOrigin, undefined);
      check("an origin registered without a purpose is a storefront origin", sOrig.status === 201 && sOrig.json?.purpose === "storefront", brief(sOrig));
      check("an unknown purpose is rejected (422)", (await register(`https://x-${runID}.e2e.example.com`, "root")).status === 422);

      const preflight = (origin, path) => req("OPTIONS", path, {
        headers: { Origin: origin, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "authorization,x-admin-refresh" },
      });
      const allowedFor = async (origin, path) => (await preflight(origin, path)).headers.get("access-control-allow-origin") === origin;
      check("a storefront origin gets no CORS on /admin/auth/login", !(await allowedFor(storefrontOrigin, "/api/v1/admin/auth/login")));
      check("a storefront origin keeps CORS on the storefront API", await allowedFor(storefrontOrigin, "/api/v1/shop/products"));
      check("an admin origin gets CORS on its own shop's admin path", await allowedFor(adminOrigin, "/api/v1/admin/shops/1/orders"));
      check("an admin origin gets none on another shop's admin path", !(await allowedFor(adminOrigin, "/api/v1/admin/shops/2/orders")));
      check("an admin origin gets none on a platform-level path", !(await allowedFor(adminOrigin, "/api/v1/admin/accounts")));
      check("an admin origin gets none on the storefront API", !(await allowedFor(adminOrigin, "/api/v1/shop/products")));
      check("an admin origin gets CORS on the shared auth path", await allowedFor(adminOrigin, "/api/v1/admin/auth/refresh"));
      check("Allow-Headers announces X-Admin-Refresh",
        (await preflight(adminOrigin, "/api/v1/admin/auth/refresh")).headers.get("access-control-allow-headers")?.includes("X-Admin-Refresh"));

      // Server-side host binding: a request carrying the shop's admin Origin can
      // only touch that shop, even for the platform administrator.
      const asAdminOrigin = { Origin: adminOrigin };
      const own = await req("GET", "/api/v1/admin/shops/1/me", { token: adminToken, headers: asAdminOrigin });
      check("bound to shop 1: its own /me answers 200", own.status === 200, brief(own));
      const other = await req("GET", "/api/v1/admin/shops/999999/me", { token: adminToken, headers: asAdminOrigin });
      check("bound to shop 1: another shop answers 403 even for the platform admin", other.status === 403, brief(other));
      const plat = await req("GET", "/api/v1/admin/accounts", { token: adminToken, headers: asAdminOrigin });
      check("bound to shop 1: a platform-level path answers 403", plat.status === 403, brief(plat));
      check("bound host still reaches /me", (await req("GET", "/api/v1/admin/me", { token: adminToken, headers: asAdminOrigin })).status === 200);
      check("unbound (no admin origin): the platform path follows RBAC (200)",
        (await req("GET", "/api/v1/admin/accounts", { token: adminToken })).status === 200);

      // Branding: resolved from the registered admin origin, before login.
      const set = await req("PUT", "/api/v1/admin/shops/1/settings", {
        token: adminToken, body: { admin_branding: { primary_color: "#112233", accent_color: "#445566" } },
      });
      check("admin_branding written through PUT /settings", set.status === 200 && set.json?.admin_branding?.primary_color === "#112233", brief(set));
      check("a non-hex color is rejected (422)",
        (await req("PUT", "/api/v1/admin/shops/1/settings", { token: adminToken, body: { admin_branding: { primary_color: "red" } } })).status === 422);
      const pub = await req("GET", "/api/v1/admin/branding", { headers: asAdminOrigin });
      check("GET /admin/branding resolves the shop from the admin origin without a token",
        pub.status === 200 && pub.json?.branding?.accent_color === "#445566" && pub.json?.shop_id === undefined, brief(pub));
      check("GET /admin/branding on an unregistered origin is 404",
        (await req("GET", "/api/v1/admin/branding", { headers: { Origin: `https://nobody-${runID}.e2e.example.com` } })).status === 404);
      await req("PUT", "/api/v1/admin/shops/1/settings", { token: adminToken, body: { admin_branding: null } });

      // Cookie-mode refresh.
      const cl = await req("POST", "/api/v1/admin/auth/login", {
        body: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD, refresh_transport: "cookie" },
      });
      const setCookie = cl.headers.getSetCookie?.()[0] ?? "";
      check("cookie-mode login: no refresh_token in the body", cl.status === 200 && cl.json?.refresh_token === undefined && !!cl.json?.access_token, brief(cl));
      check("cookie-mode login: HttpOnly Secure SameSite=Strict cookie scoped to /api/v1/admin/auth",
        /^__Secure-admin_refresh=/.test(setCookie) && /HttpOnly/i.test(setCookie) && /Secure/i.test(setCookie) &&
          /SameSite=Strict/i.test(setCookie) && /Path=\/api\/v1\/admin\/auth/.test(setCookie), setCookie.slice(0, 80));
      const cookie = setCookie.split(";")[0];
      const noHeader = await req("POST", "/api/v1/admin/auth/refresh", { headers: { Cookie: cookie }, body: {} });
      check("cookie refresh without X-Admin-Refresh is 401 and not rotated", noHeader.status === 401 && !noHeader.headers.getSetCookie?.().length, brief(noHeader));
      const good = await req("POST", "/api/v1/admin/auth/refresh", { headers: { Cookie: cookie, "X-Admin-Refresh": "1" }, body: {} });
      check("cookie refresh with the header rotates the cookie", good.status === 200 && good.json?.refresh_token === undefined &&
        (good.headers.getSetCookie?.()[0] ?? "").split(";")[0] !== cookie, brief(good));
      const logout = await req("POST", "/api/v1/admin/auth/logout", {
        headers: { Cookie: (good.headers.getSetCookie?.()[0] ?? "").split(";")[0], "X-Admin-Refresh": "1" }, body: {},
      });
      check("cookie logout answers 204", logout.status === 204, brief(logout));
      // The very first login of this run (default mode). Not repeated here: the
      // per-IP+e-mail login limiter (5 / 5 min) is shared with every admin login above.
      check("default (body-mode) login is unchanged: refresh_token present, no Set-Cookie",
        !!login.json?.refresh_token && !(login.headers.getSetCookie?.().length), brief(login));
    } finally {
      for (const id of registered) await req("DELETE", `/api/v1/admin/shops/1/allowed-origins/${id}`, { token: adminToken });
    }
  }
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

// ── 開店申請 API 流程（change platform-website）：同樣不需要瀏覽器。──
try {
  await runApplicationPath();
} catch (err) {
  fail(`application path aborted: ${err?.stack ?? err}`);
}

// ── 內容儲存的發佈 hook／排程發佈／清除（change headless-content-store 第三切片）：需要為它設定的 API，見該檔開頭。──
try {
  await runContentOpsPath();
} catch (err) {
  fail(`content ops path aborted: ${err?.stack ?? err}`);
}

if (failureCount() > 0) {
  console.error(`\nE2E FAILED with ${failureCount()} failed check(s)`);
  process.exit(1);
}
console.log("\nE2E happy path PASSED (CMS + commerce)");
