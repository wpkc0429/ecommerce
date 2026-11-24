// E2E commerce path (change commerce-demo-seed): the full chain, on nothing
// but `make seed-demo` data.
//
//   瀏覽商品 → 訪客結帳 → 付款 → 出貨 → 點數核發 → 退貨 → 點數扣回
//   ＋ 會員路徑（購物車、卡片訊息、等級升降、webhook 重送冪等、取消歸還庫存）
//
// Why this is an E2E and not a seventh Go integration test (design D1): the
// award/clawback chain crosses three packages that deliberately never import
// each other — payment publishes events.OrderPaymentSucceeded, shipping
// publishes events.OrderReturned, points subscribes to both. Which means
// "is anything actually subscribed?" is not a compile-time fact; it is two
// lines in api/internal/app/wire.go, and that file has ZERO test coverage —
// every integration test builds its own dispatcher and says in a comment that
// it "mirrors app.wire.go". Delete the subscription in wire.go and the whole
// Go suite stays green while the real server silently stops awarding points.
// Only a process started the way `make dev` starts it can catch that.
//
// No browser: every step here is an API call, and neither /web nor /admin
// renders products, orders or shipments (design D2).
//
// Prereqs: API on :8080, and `make migrate && make seed-demo`.
// Run alone:      cd e2e && node commerce-path.mjs
// Run with CMS:   cd e2e && node happy-path.mjs
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import {
  API, brief, check, deliverECPayCallback, deliverWebhook, ecpayCheckMac, fail, failureCount, isoDate, req,
  runID, section, tierFor, tierName,
} from "./lib.mjs";

// The back-office half of this path signs in as the shop's own merchant owner,
// not as the platform super admin. Two reasons:
//
//  1. It is what actually happens — shipping a parcel and reading a customer's
//     points are merchant operations. Using the super admin would sail past
//     RBAC entirely and never confirm the seeded `merchant_owner` role really
//     grants shipment.create / shipment.update / point.view.
//  2. Login rate limiting is keyed per (shop, IP, email) with a tight bound of
//     5 in 5 minutes. happy-path.mjs already spends two platform-admin logins
//     per run (browser + API); spending a third here would make two
//     back-to-back runs trip the limit and look like a broken feature.
const OWNER_EMAIL = process.env.E2E_OWNER_EMAIL ?? "demo-owner@example.com";
const OWNER_PASSWORD = process.env.E2E_OWNER_PASSWORD ?? "demo-owner-change-me";

// Seeded by api/internal/seed (change commerce-demo-seed). Fixed on purpose —
// a publishable key is an identifier, not a credential, and the demo shop is
// already resolvable without one via Host: demo.localhost (design D5).
const PK = "pk_demoseedstorefrontkeynotsecret";
const CUSTOMER_EMAIL = "demo-customer@example.com";
const CUSTOMER_PASSWORD = "demo-customer-change-me";
const BOUQUET_SLUG = "anniversary-bouquet";
const BOUQUET_SKU = "BQ-ANNI-M";

// Permanent stock consumed by one run: 1 (guest, returned) + 2 (member,
// returned) + 1 (the tier-discount order of section ⑧, returned) + 1 (the ECPay
// order of section ⑫, returned) + 0 (cancelled orders restore stock, which
// covers section ⑩ and section ⑫'s cancelled-then-paid order). Returns do NOT
// restore stock — only cancellation does — so a seeded database supports a
// bounded number of runs before it needs re-seeding (design D7). BQ-ANNI-M is
// seeded with 36, i.e. 7 runs.
//
// This counts BQ-ANNI-M only, which is why change partial-shipment's section ⑬
// and change merchant-reports' section ⑭ deliberately buy the S size instead.
// Both end in a state that never restocks — ⑬ delivered, ⑭ paid and left alone
// on purpose (it is the section's whole point that one order stays COUNTED) —
// so their units are gone for good, and spending them here would have cut the
// runs-per-seed. S is seeded with 24 (2 per run) and L with 18 (1 per run), so
// neither section constrains anything; BQ-ANNI-M's 36 ÷ 5 remains the budget.
const STOCK_PER_RUN = 5;

class Abort extends Error {}

function need(cond, message) {
  if (!cond) {
    fail(message);
    throw new Abort(message);
  }
}

async function skuStock(skuCode) {
  const r = await req("GET", `/api/v1/shop/products/${BOUQUET_SLUG}`, { pk: PK });
  const sku = (r.json?.skus ?? []).find((s) => s.sku_code === skuCode);
  return sku ? sku.stock_qty : null;
}

export async function runCommercePath() {
  try {
    await commercePath();
  } catch (err) {
    if (!(err instanceof Abort)) {
      fail(`commerce path aborted: ${err?.stack ?? err}`);
    }
  }
  return failureCount();
}

async function commercePath() {
  // ── ① 前置：後台登入與 seed 佈建檢查 ──
  section("① Back-office login and seeded prerequisites");
  const login = await req("POST", "/api/v1/admin/auth/login", {
    body: { email: OWNER_EMAIL, password: OWNER_PASSWORD },
  });
  if (login.status === 429) {
    // Environment, not code: the same class of failure as a webhook signature
    // mismatch, and just as misleading if reported as "login failed".
    need(false,
      `back-office login is rate limited (429). The tight rule allows 5 logins per ` +
        `(shop, IP, email) per 5 minutes — wait for the window to pass and re-run.`);
  }
  need(login.json?.access_token, `merchant owner login failed: ${brief(login)} — run \`make seed-demo\``);
  const admin = login.json.access_token;
  check("seeded merchant owner logs in", true, OWNER_EMAIL);

  // The shop id comes from the publishable key's own resolution, not from a
  // "shops[0]" guess: this ties every admin call below to the exact shop the
  // seeded key addresses.
  const products = await req("GET", "/api/v1/shop/products", { pk: PK });
  need(
    products.status === 200 && (products.json?.products?.length ?? 0) > 0,
    `publishable key did not resolve a shop with products: ${brief(products)} — run \`make seed-demo\``,
  );
  const shopID = products.json.products[0].shop_id;
  check("seeded publishable key resolves the demo shop", Boolean(shopID), String(shopID));
  const shopPath = (p) => `/api/v1/admin/shops/${shopID}${p}`;

  // change admin-commerce-operations (design D6): the admin SPA decides whether
  // to draw the allowed-origins block straight from this endpoint's answer, even
  // though shop.manage_client_origins is a PLATFORM-scoped node. The difference
  // only shows up for a MERCHANT identity — the platform admin that
  // happy-path.mjs drives in the browser holds both tiers and sees everything.
  // So the asymmetry has to be asserted here, where the caller is the shop's own
  // owner, or it is not asserted at all.
  const meRes = await req("GET", shopPath("/me"), { token: admin });
  const myPerms = meRes.json?.permissions ?? [];
  check("merchant owner can query their own effective permissions in this shop",
    meRes.status === 200, brief(meRes));
  check("publishable keys are merchant-scoped, so the owner holds them",
    myPerms.includes("publishable_key.view") && myPerms.includes("publishable_key.create"),
    JSON.stringify(myPerms.filter((p) => p.startsWith("publishable_key"))));
  check("allowed origins are platform-scoped, so the owner does NOT hold the node",
    !myPerms.includes("shop.manage_client_origins"), JSON.stringify(myPerms));
  const originsForOwner = await req("GET", shopPath("/allowed-origins"), { token: admin });
  check("…and the server agrees: the owner is refused on the origins endpoint",
    originsForOwner.status === 403, brief(originsForOwner));

  const adminMethods = await req("GET", shopPath("/shipping-methods"), { token: admin });
  const allMethods = adminMethods.json?.shipping_methods ?? [];
  const activeMethods = allMethods.filter((m) => m.is_active);
  const inactiveMethod = allMethods.find((m) => !m.is_active);
  need(activeMethods.length >= 3, `seed provisioned ${activeMethods.length} active shipping methods, want >= 3 — run \`make seed-demo\``);
  need(inactiveMethod, "seed provisioned no DEACTIVATED shipping method — run `make seed-demo`");
  check("seed provisioned at least 3 active shipping methods", true, String(activeMethods.length));
  check("seed provisioned a deactivated shipping method", true, inactiveMethod.name);
  const paidMethod = activeMethods.find((m) => m.flat_rate > 0);
  const freeMethod = activeMethods.find((m) => m.flat_rate === 0);
  need(paidMethod && freeMethod, "seed must provision both a paid and a free shipping method");
  check("seeded shipping methods cover both a fee and free shipping", true, `${paidMethod.flat_rate} / ${freeMethod.flat_rate}`);

  const tiersRes = await req("GET", shopPath("/member-tiers"), { token: admin });
  const tiers = tiersRes.json?.member_tiers ?? [];
  need(tiers.length >= 3, `seed provisioned ${tiers.length} member tiers, want >= 3 — run \`make seed-demo\``);
  check("seed provisioned a member tier ladder", true, tiers.map((t) => `${t.name}@${t.min_points}`).join(" < "));

  // ── ② 公開目錄 ──
  section("② Public catalog through the publishable key");
  const detail = await req("GET", `/api/v1/shop/products/${BOUQUET_SLUG}`, { pk: PK });
  need(detail.status === 200, `seeded bouquet not published: ${brief(detail)} — run \`make seed-demo\``);
  const skus = detail.json.skus ?? [];
  check("seeded bouquet exposes multiple size SKUs", skus.length >= 3, String(skus.length));
  const ids = skus.map((s) => s.id);
  check(
    "SKUs come back in a deterministic id-ascending order",
    ids.every((id, i) => i === 0 || id > ids[i - 1]),
    JSON.stringify(ids),
  );
  // Selected by code, never by index — a test must not lean on the very
  // property it is asserting.
  const sku = skus.find((s) => s.sku_code === BOUQUET_SKU);
  need(sku, `seeded SKU ${BOUQUET_SKU} missing — run \`make seed-demo\``);
  const unitPrice = sku.price_amount;
  check("seeded bouquet is priced high enough to cross a tier threshold",
    Math.floor(unitPrice / 100) >= tiers[1].min_points,
    `${unitPrice} → ${Math.floor(unitPrice / 100)} pts vs ${tiers[1].name}@${tiers[1].min_points}`);

  need(
    sku.stock_qty >= STOCK_PER_RUN + 1,
    `seeded stock exhausted (${BOUQUET_SKU} has ${sku.stock_qty} left; a run needs ${STOCK_PER_RUN + 1}). ` +
      `Returns do not restore stock — re-run \`make seed-demo\` on a fresh database.`,
  );

  const publicMethods = await req("GET", "/api/v1/shop/shipping-methods", { pk: PK });
  const publicIDs = (publicMethods.json?.shipping_methods ?? []).map((m) => m.id);
  check("public shipping-method list needs no credential", publicMethods.status === 200, brief(publicMethods));
  check("public list hides the deactivated method", !publicIDs.includes(inactiveMethod.id), JSON.stringify(publicIDs));
  check("public list exposes every active method", activeMethods.every((m) => publicIDs.includes(m.id)));

  // change product-subtitle-public-categories: public category tree + filter.
  const catTree = await req("GET", "/api/v1/shop/categories", { pk: PK });
  const flatten = (nodes) => nodes.flatMap((n) => [n, ...flatten(n.children ?? [])]);
  const treeNodes = flatten(catTree.json?.categories ?? []);
  const newArrivals = treeNodes.find((c) => c.slug === "new-arrivals");
  check("public category tree needs no credential and includes new-arrivals (本季新品)",
    catTree.status === 200 && newArrivals?.name === "本季新品", brief(catTree));
  const naList = await req("GET", "/api/v1/shop/products?category=new-arrivals", { pk: PK });
  const naProducts = naList.json?.products ?? [];
  check("category filter returns only products in that category",
    naList.status === 200 && naProducts.length >= 2 &&
      naProducts.every((p) => (p.categories ?? []).some((c) => c.slug === "new-arrivals")) &&
      naList.json.total === naProducts.length,
    brief(naList));
  check("seeded floral products carry a subtitle", naProducts.every((p) => p.subtitle !== ""),
    JSON.stringify(naProducts.map((p) => p.subtitle)));
  const noneList = await req("GET", "/api/v1/shop/products?category=no-such-category", { pk: PK });
  check("unknown category slug is 200 with an empty list",
    noneList.status === 200 && (noneList.json?.products ?? []).length === 0 && noneList.json?.total === 0,
    brief(noneList));

  // ── ③ 訪客路徑：結帳 → 付款 → 點數 ──
  section("③ Guest path: checkout → pay → points awarded");
  const guestEmail = `e2e-guest-${runID}@example.com`;
  const stockBeforeGuest = await skuStock(BOUQUET_SKU);
  const guest = await req("POST", "/api/v1/shop/guest/checkout", {
    pk: PK,
    body: {
      email: guestEmail,
      items: [{ sku_id: sku.id, quantity: 1, gift_message: "E2E：給媽媽" }],
      shipping_address: { recipient_name: "E2E 訪客", phone: "0912345678", line1: "台北市信義區松高路 1 號" },
      shipping_method_id: paidMethod.id,
      requested_delivery_date: isoDate(1),
    },
  });
  need(guest.status === 201, `guest checkout failed: ${brief(guest)}`);
  const gOrder = guest.json.order;
  const gToken = guest.json.order_access_token;
  check("guest checkout creates an order without any credential", true, `order ${gOrder.id}`);
  check("order amounts compose as subtotal − discount + shipping_fee = total",
    gOrder.subtotal_amount - gOrder.discount_amount + gOrder.shipping_fee === gOrder.total_amount,
    `${gOrder.subtotal_amount} - ${gOrder.discount_amount} + ${gOrder.shipping_fee} = ${gOrder.total_amount}`);
  // A brand-new email has never earned a point, so it holds no tier at all —
  // the "nothing applied" shape (change tier-discount-application).
  check("a brand-new guest holds no tier, so nothing is discounted",
    gOrder.discount_amount === 0 && gOrder.discount_percent === null && gOrder.member_tier_name === null,
    JSON.stringify([gOrder.discount_amount, gOrder.discount_percent, gOrder.member_tier_name]));
  check("the selected method's flat rate became the order's shipping fee",
    gOrder.shipping_fee === paidMethod.flat_rate, `${gOrder.shipping_fee} vs ${paidMethod.flat_rate}`);
  check("the shipping method name is snapshotted onto the order",
    gOrder.shipping_method_name === paidMethod.name, gOrder.shipping_method_name ?? "");
  check("the requested delivery date round-trips as a bare date",
    gOrder.requested_delivery_date === isoDate(1), String(gOrder.requested_delivery_date));
  check("the gift message is snapshotted onto the order item",
    gOrder.items[0]?.gift_message === "E2E：給媽媽", gOrder.items[0]?.gift_message ?? "");
  const stockAfterGuest = await skuStock(BOUQUET_SKU);
  check("checkout decremented stock by the ordered quantity",
    stockAfterGuest === stockBeforeGuest - 1, `${stockBeforeGuest} → ${stockAfterGuest}`);

  // change order-and-invitation-polish: shipping an UNPAID order is refused by
  // the API itself (409), not just by whichever admin front end is in use.
  const gUnpaidShip = await req("POST", shopPath(`/orders/${gOrder.id}/shipments`), {
    token: admin, body: { carrier: paidMethod.carrier },
  });
  check("an unpaid order cannot be shipped (409 order is not paid)",
    gUnpaidShip.status === 409 && /not paid/.test(gUnpaidShip.text), brief(gUnpaidShip));
  const gStillUnfulfilled = await req("GET", shopPath(`/orders/${gOrder.id}`), { token: admin });
  check("…and the refusal leaves the order unfulfilled",
    gStillUnfulfilled.json?.fulfillment_status === 0, String(gStillUnfulfilled.json?.fulfillment_status));

  // change order-and-invitation-polish: order items carry the SKU's options
  // as a snapshot, so clients stop parsing sku_code suffixes.
  const gOpts = gOrder.items?.[0]?.options;
  check("order items carry the options snapshot as an object (size, not a sku_code suffix)",
    gOpts !== null && typeof gOpts === "object" && typeof gOpts.size === "string" && gOpts.size.length > 0,
    JSON.stringify(gOrder.items?.[0]));

  const gPay = await req("POST", "/api/v1/shop/guest/order/payments", { token: gToken, pk: PK, body: {} });
  need(gPay.status === 201, `guest payment initiation failed: ${brief(gPay)}`);
  check("the amount sent to the payment provider is the PAYABLE total (shipping included)",
    gPay.json.amount === gOrder.total_amount, `charged ${gPay.json.amount}, order total ${gOrder.total_amount}`);

  const gHook = await deliverWebhook(gPay.json.provider_reference);
  check("signed provider webhook accepted", gHook.status === 200, brief(gHook));
  const gAfterPay = await req("GET", "/api/v1/shop/guest/order", { token: gToken, pk: PK });
  check("order is marked paid", gAfterPay.json?.payment_status === 1, String(gAfterPay.json?.payment_status));

  // change order-and-invitation-polish: the mock provider reports no time, yet a
  // succeeded payment still carries paid_at.
  const gPayRows = await req("GET", shopPath(`/orders/${gOrder.id}/payments`), { token: admin });
  const gPaidAt = gPayRows.json?.payments?.[0]?.paid_at;
  check("a succeeded mock payment has paid_at (the merchant can say WHEN)",
    typeof gPaidAt === "string" && Math.abs(Date.now() - Date.parse(gPaidAt)) < 10 * 60 * 1000,
    String(gPaidAt));

  // The cross-package seam: payment → events.OrderPaymentSucceeded → points.
  const expectedAward = Math.floor(gOrder.subtotal_amount / 100);
  const awardOnTotal = Math.floor(gOrder.total_amount / 100);
  const gPoints = await req("GET", shopPath(`/members/${gOrder.member_id}/points`), { token: admin });
  check("points were awarded at all (payment → events → points, incl. wire.go subscription)",
    gPoints.json?.points > 0, brief(gPoints));
  check("points are awarded on the item subtotal, NOT on the payable total",
    gPoints.json?.points === expectedAward,
    `got ${gPoints.json?.points}, want ${expectedAward} (subtotal ${gOrder.subtotal_amount}); total-based would be ${awardOnTotal}`);
  check("the shipping fee genuinely changes the answer (so the check above is meaningful)",
    expectedAward !== awardOnTotal, `${expectedAward} vs ${awardOnTotal}`);

  const gLedger = await req("GET", shopPath(`/members/${gOrder.member_id}/points/transactions`), { token: admin });
  const gTx = gLedger.json?.transactions ?? [];
  check("the ledger holds exactly one award row for this order", gTx.length === 1, JSON.stringify(gTx));
  check("the award row records kind=0, the order id and the exact delta",
    gTx[0]?.kind === 0 && gTx[0]?.order_id === gOrder.id && gTx[0]?.points_delta === expectedAward,
    JSON.stringify(gTx[0]));

  const gExpectedTier = tierFor(tiers, expectedAward);
  check("the guest was promoted to the tier the seeded ladder implies",
    gPoints.json?.level?.name === gExpectedTier?.name,
    `${tierName(gPoints.json?.level)} vs expected ${tierName(gExpectedTier)}`);
  check("that promotion is a real crossing (a brand-new customer had no tier)",
    gExpectedTier !== null && tierFor(tiers, 0)?.min_points !== gExpectedTier.min_points,
    `${tierName(tierFor(tiers, 0))} → ${tierName(gExpectedTier)}`);

  // ── ③.5 顧客名冊（change member-directory）──
  //
  // These assertions live here, with a MERCHANT OWNER token, for the same reason
  // the allowed-origins asymmetry does (change admin-commerce-operations design
  // D11): happy-path.mjs drives the browser as a PLATFORM admin, who holds every
  // node and therefore cannot show that member.view reaches a merchant at all.
  //
  // The password_hash assertion is deliberately made against the REAL server's
  // raw response text rather than in Go: ent's Sensitive() and the hand-written
  // serializer both stop it today, but both are one line away from being undone,
  // and neither undoing would fail a test that only checks the JSON parses.
  section("③.5 Customer roster: member.view, disclosure boundary, tenant isolation");
  check("the merchant owner holds member.view", myPerms.includes("member.view"), JSON.stringify(myPerms));
  check("member.edit is not a node anyone holds (no endpoint writes to members)",
    !myPerms.some((p) => p.startsWith("member.") && p !== "member.view"),
    JSON.stringify(myPerms.filter((p) => p.startsWith("member."))));

  const roster = await req("GET", shopPath(`/members?q=${encodeURIComponent(guestEmail)}`), { token: admin });
  check("the merchant can search their own customer roster", roster.status === 200, brief(roster));
  const rosterRow = (roster.json?.members ?? [])[0];
  check("server-side search finds the guest who just ordered",
    rosterRow?.id === gOrder.member_id && rosterRow?.email === guestEmail, brief(roster));
  check("a guest-checkout identity reports registered=false (it has no password and cannot log in)",
    rosterRow?.registered === false, JSON.stringify(rosterRow?.registered));
  // The raw text, not the parsed object: a renamed key would still carry the
  // bcrypt prefix, and this catches that too.
  for (const forbidden of ["password_hash", "$2a$", "$2b$", "$2y$", "phone"]) {
    check(`the roster response never contains ${forbidden}`,
      !roster.text.includes(forbidden), roster.text.slice(0, 300));
  }
  check("…and the assertion is meaningful (this really is that customer's payload)",
    roster.text.includes(guestEmail), roster.text.slice(0, 200));

  const rosterOne = await req("GET", shopPath(`/members/${gOrder.member_id}`), { token: admin });
  check("a single customer can be fetched by id, same shape as the list row",
    rosterOne.status === 200 && rosterOne.json?.id === gOrder.member_id, brief(rosterOne));
  check("the single-customer response never contains password_hash either",
    !rosterOne.text.includes("password_hash") && !rosterOne.text.includes("$2a$") &&
      !rosterOne.text.includes("$2b$"), rosterOne.text.slice(0, 300));

  // "What has this customer bought" reuses the order list, so there is exactly
  // one order serialization on the platform (design D2).
  const theirOrders = await req("GET", shopPath(`/orders?member_id=${gOrder.member_id}`), { token: admin });
  check("the customer's orders come from the order list's member_id filter",
    theirOrders.status === 200 &&
      (theirOrders.json?.orders ?? []).every((o) => o.member_id === gOrder.member_id) &&
      (theirOrders.json?.orders ?? []).some((o) => o.id === gOrder.id),
    brief(theirOrders));

  // Tenant isolation: a shop this owner has no standing in is 403, before any
  // handler runs. shopID + 1 is either another shop or none — the cross-shop
  // guard answers the same way for both, which is itself the point.
  const foreignRoster = await req("GET", `/api/v1/admin/shops/${shopID + 1}/members`, { token: admin });
  check("querying another shop's roster is refused (403), not answered",
    foreignRoster.status === 403, brief(foreignRoster));

  // ── ④ 訪客路徑：出貨 → 退貨 → 點數扣回 ──
  section("④ Guest path: ship → return → points clawed back");
  const gShip = await req("POST", shopPath(`/orders/${gOrder.id}/shipments`), {
    token: admin, body: { carrier: paidMethod.carrier, tracking_number: `E2E-${runID}-G` },
  });
  need(gShip.status === 201, `create shipment failed: ${brief(gShip)}`);
  check("merchant ships the order", gShip.json.status === 1, JSON.stringify(gShip.json.status));
  const gShipView = await req("GET", "/api/v1/shop/guest/order/shipment", { token: gToken, pk: PK });
  check("the order access token can read that shipment",
    gShipView.status === 200 && gShipView.json.tracking_number === `E2E-${runID}-G`, brief(gShipView));

  const gReturn = await req("PUT", shopPath(`/orders/${gOrder.id}/shipments/${gShip.json.id}`), {
    token: admin, body: { status: "returned" },
  });
  check("merchant marks the shipment returned", gReturn.status === 200 && gReturn.json.status === 3, brief(gReturn));

  // The second cross-package seam: shipping → events.OrderReturned → points.
  const gPointsAfter = await req("GET", shopPath(`/members/${gOrder.member_id}/points`), { token: admin });
  check("returning the order clawed the points back to zero (shipping → events → points)",
    gPointsAfter.json?.points === 0, brief(gPointsAfter));
  const gLedger2 = await req("GET", shopPath(`/members/${gOrder.member_id}/points/transactions`), { token: admin });
  const gTx2 = gLedger2.json?.transactions ?? [];
  check("the ledger is append-only: the award row is still there, a clawback row joins it",
    gTx2.length === 2 && gTx2.some((t) => t.kind === 0 && t.points_delta === expectedAward),
    JSON.stringify(gTx2));
  check("the clawback row records kind=1 and exactly the negated award",
    gTx2.some((t) => t.kind === 1 && t.order_id === gOrder.id && t.points_delta === -expectedAward),
    JSON.stringify(gTx2));
  check("the tier follows the balance back down",
    gPointsAfter.json?.level?.name === tierFor(tiers, 0)?.name,
    `${tierName(gPointsAfter.json?.level)} vs expected ${tierName(tierFor(tiers, 0))}`);

  // ── ⑤ 停用的運送方式 ──
  section("⑤ A deactivated shipping method is refused at checkout");
  const stockBeforeRefused = await skuStock(BOUQUET_SKU);
  const refused = await req("POST", "/api/v1/shop/guest/checkout", {
    pk: PK,
    body: {
      email: `e2e-refused-${runID}@example.com`,
      items: [{ sku_id: sku.id, quantity: 1 }],
      shipping_address: { recipient_name: "E2E", phone: "0912345678", line1: "台北市" },
      shipping_method_id: inactiveMethod.id,
    },
  });
  check("checkout with a deactivated shipping method is rejected", refused.status === 422, brief(refused));
  check("the rejection points at the shipping method input",
    JSON.stringify(refused.json ?? {}).includes("/shipping_method_id"), brief(refused));
  check("a rejected checkout touches no stock",
    (await skuStock(BOUQUET_SKU)) === stockBeforeRefused, String(stockBeforeRefused));

  // ── ⑥ 會員路徑：購物車 → 結帳 → 付款 → 升級 ──
  section("⑥ Member path: cart → checkout → pay → tier upgrade");
  const mLogin = await req("POST", "/api/v1/shop/auth/login", {
    pk: PK, body: { email: CUSTOMER_EMAIL, password: CUSTOMER_PASSWORD },
  });
  need(mLogin.json?.access_token, `seeded demo customer could not log in: ${brief(mLogin)} — run \`make seed-demo\``);
  const member = mLogin.json.access_token;
  check("seeded demo customer logs in", true, CUSTOMER_EMAIL);

  const pointsBefore = (await req("GET", "/api/v1/shop/points", { token: member, pk: PK })).json;
  const ledgerBefore = (await req("GET", "/api/v1/shop/points/transactions", { token: member, pk: PK })).json;

  await req("DELETE", "/api/v1/shop/cart/items", { token: member, pk: PK });
  await req("POST", "/api/v1/shop/cart/items", {
    token: member, pk: PK, body: { sku_id: sku.id, quantity: 1, gift_message: "E2E：給媽媽" },
  });
  const cart = await req("POST", "/api/v1/shop/cart/items", {
    token: member, pk: PK, body: { sku_id: sku.id, quantity: 1, gift_message: "E2E：給岳母" },
  });
  check("the same SKU with two different gift messages stays two lines, not quantity 2",
    (cart.json?.items ?? []).length === 2 && cart.json.items.every((i) => i.quantity === 1),
    JSON.stringify((cart.json?.items ?? []).map((i) => [i.sku_code, i.quantity, i.gift_message])));

  const mOrderRes = await req("POST", "/api/v1/shop/checkout", {
    token: member, pk: PK,
    body: {
      recipient_name: "E2E 會員", phone: "0922333444", line1: "台北市大安區永康街 12 號",
      shipping_method_id: freeMethod.id, requested_delivery_date: isoDate(2),
    },
  });
  need(mOrderRes.status === 201, `member checkout failed: ${brief(mOrderRes)}`);
  const mOrder = mOrderRes.json;
  check("member checkout carries both gift messages into the order items",
    new Set((mOrder.items ?? []).map((i) => i.gift_message)).size === 2,
    JSON.stringify((mOrder.items ?? []).map((i) => i.gift_message)));
  check("a zero-rate shipping method leaves the payable total equal to the subtotal",
    mOrder.shipping_fee === 0 && mOrder.total_amount === mOrder.subtotal_amount,
    `${mOrder.subtotal_amount} + ${mOrder.shipping_fee} = ${mOrder.total_amount}`);
  // Deterministic across runs: a run is points-neutral (section ⑨ claws back
  // what ⑦ awarded), so the demo customer always enters this section on the
  // 0-point tier, whose seeded discount_percent is NULL.
  check("the member enters this order on a tier that grants no discount",
    mOrder.discount_amount === 0,
    `${tierName(pointsBefore.level)} → discount ${mOrder.discount_amount}`);

  const mPay = await req("POST", `/api/v1/shop/orders/${mOrder.id}/payments`, { token: member, pk: PK, body: {} });
  need(mPay.status === 201, `member payment initiation failed: ${brief(mPay)}`);
  const mHook = await deliverWebhook(mPay.json.provider_reference);
  check("signed provider webhook accepted (member order)", mHook.status === 200, brief(mHook));

  const mAward = Math.floor(mOrder.subtotal_amount / 100);
  const mPoints = (await req("GET", "/api/v1/shop/points", { token: member, pk: PK })).json;
  check("the member's balance grew by exactly floor(subtotal / 100)",
    mPoints.points === pointsBefore.points + mAward,
    `${pointsBefore.points} + ${mAward} vs ${mPoints.points}`);
  const mExpectedTier = tierFor(tiers, mPoints.points);
  check("the member's tier is the one the seeded ladder implies for the new balance",
    mPoints.level?.name === mExpectedTier?.name,
    `${tierName(mPoints.level)} vs expected ${tierName(mExpectedTier)}`);
  check("that is an actual upgrade from the pre-order tier",
    tierName(pointsBefore.level) !== tierName(mPoints.level),
    `${tierName(pointsBefore.level)} → ${tierName(mPoints.level)}`);

  // ── ⑦ webhook 重送 ──
  section("⑦ Webhook re-delivery is idempotent all the way to the ledger");
  const replay = await deliverWebhook(mPay.json.provider_reference);
  check("re-delivering the same webhook is accepted", replay.status === 200, brief(replay));
  const mPointsReplay = (await req("GET", "/api/v1/shop/points", { token: member, pk: PK })).json;
  const mLedgerReplay = (await req("GET", "/api/v1/shop/points/transactions", { token: member, pk: PK })).json;
  check("re-delivery does not award a second time",
    mPointsReplay.points === mPoints.points, `${mPoints.points} → ${mPointsReplay.points}`);
  check("re-delivery does not add a second ledger row",
    mLedgerReplay.total === ledgerBefore.total + 1,
    `${ledgerBefore.total} → ${mLedgerReplay.total}`);

  // ── ⑧ 等級折扣：真的扣錢、快照不動、點數以折後為基準 ──
  //
  // Placed here on purpose: sections ⑥/⑦ just promoted the member with a real
  // purchase, so this runs against a tier the customer actually earned rather
  // than one the test planted. The order is paid (to reach the points basis) and
  // then returned, which claws the award back and leaves section ⑨'s balance
  // arithmetic exactly as it was — at the cost of 1 permanently consumed unit of
  // stock (returns do not restock), which is why STOCK_PER_RUN is 4.
  section("⑧ Member tier discount: applied, snapshotted, and reflected in points");
  const tierNow = (await req("GET", "/api/v1/shop/points", { token: member, pk: PK })).json;
  const pct = tierNow.level?.discount_percent ?? 0;
  need(pct > 0,
    `the member's tier (${tierName(tierNow.level)}) has no discount_percent — ` +
      "the seeded ladder must give the 30-point tier one, run `make seed-demo`");
  check("the member's earned tier carries a discount rate the storefront can read",
    true, `${tierName(tierNow.level)} @ ${pct}%`);

  await req("DELETE", "/api/v1/shop/cart/items", { token: member, pk: PK });
  await req("POST", "/api/v1/shop/cart/items", { token: member, pk: PK, body: { sku_id: sku.id, quantity: 1 } });
  const dRes = await req("POST", "/api/v1/shop/checkout", {
    token: member, pk: PK,
    body: {
      recipient_name: "E2E 折扣", phone: "0922333444", line1: "台北市大安區永康街 12 號",
      shipping_method_id: paidMethod.id,
    },
  });
  need(dRes.status === 201, `discounted member checkout failed: ${brief(dRes)}`);
  const dOrder = dRes.json;
  // floor(subtotal × pct / 100) — the same integer rule the server uses, spelled
  // out here rather than read back from the response, so the test would notice a
  // server that rounded instead of truncated.
  const expectedDiscount = Math.floor((dOrder.subtotal_amount * pct) / 100);
  check("the tier discount is actually applied to the order",
    dOrder.discount_amount === expectedDiscount && dOrder.discount_amount > 0,
    `${dOrder.discount_amount} vs expected ${expectedDiscount}`);
  check("the rate and the tier that granted it are snapshotted onto the order",
    dOrder.discount_percent === pct && dOrder.member_tier_name === tierNow.level?.name,
    JSON.stringify([dOrder.discount_percent, dOrder.member_tier_name]));
  check("the discount comes off the goods only — the shipping fee is untouched",
    dOrder.shipping_fee === paidMethod.flat_rate,
    `${dOrder.shipping_fee} vs ${paidMethod.flat_rate}`);
  check("amounts compose as subtotal − discount + shipping_fee = total",
    dOrder.subtotal_amount - dOrder.discount_amount + dOrder.shipping_fee === dOrder.total_amount,
    `${dOrder.subtotal_amount} - ${dOrder.discount_amount} + ${dOrder.shipping_fee} = ${dOrder.total_amount}`);

  // The snapshot: re-price the tier under the order's feet and read it back.
  const bumped = Math.min(100, pct + 20);
  const tierRow = tiers.find((t) => t.name === tierNow.level?.name);
  const repriced = await req("PUT", shopPath(`/member-tiers/${tierRow.id}`), {
    token: admin, body: { discount_percent: bumped },
  });
  check("merchant re-prices the tier", repriced.status === 200, brief(repriced));
  const dReread = await req("GET", `/api/v1/shop/orders/${dOrder.id}`, { token: member, pk: PK });
  check("re-pricing the tier does NOT change the existing order",
    dReread.json?.discount_amount === dOrder.discount_amount &&
      dReread.json?.discount_percent === pct &&
      dReread.json?.total_amount === dOrder.total_amount,
    JSON.stringify([dReread.json?.discount_amount, dReread.json?.discount_percent, dReread.json?.total_amount]));
  // Restore it: the seeded ladder is shared with every other section and run.
  await req("PUT", shopPath(`/member-tiers/${tierRow.id}`), { token: admin, body: { discount_percent: pct } });

  const dPay = await req("POST", `/api/v1/shop/orders/${dOrder.id}/payments`, { token: member, pk: PK, body: {} });
  need(dPay.status === 201, `discounted order payment failed: ${brief(dPay)}`);
  check("the payment provider is charged the DISCOUNTED total",
    dPay.json.amount === dOrder.total_amount && dPay.json.amount < dOrder.subtotal_amount + dOrder.shipping_fee,
    `charged ${dPay.json.amount}, list price would be ${dOrder.subtotal_amount + dOrder.shipping_fee}`);
  const dHook = await deliverWebhook(dPay.json.provider_reference);
  check("signed provider webhook accepted (discounted order)", dHook.status === 200, brief(dHook));

  const dAward = Math.floor((dOrder.subtotal_amount - dOrder.discount_amount) / 100);
  const listPriceAward = Math.floor(dOrder.subtotal_amount / 100);
  const dPoints = (await req("GET", "/api/v1/shop/points", { token: member, pk: PK })).json;
  check("points are awarded on the DISCOUNTED subtotal, not the list price",
    dPoints.points === mPointsReplay.points + dAward,
    `${mPointsReplay.points} + ${dAward} vs ${dPoints.points} (list-price basis would add ${listPriceAward})`);
  check("the discount genuinely changes the answer (so the check above is meaningful)",
    dAward !== listPriceAward, `${dAward} vs ${listPriceAward}`);

  // Ship + return it so this section is points-neutral for section ⑨.
  const dShip = await req("POST", shopPath(`/orders/${dOrder.id}/shipments`), {
    token: admin, body: { carrier: paidMethod.carrier, tracking_number: `E2E-${runID}-D` },
  });
  need(dShip.status === 201, `create shipment (discounted order) failed: ${brief(dShip)}`);
  await req("PUT", shopPath(`/orders/${dOrder.id}/shipments/${dShip.json.id}`), {
    token: admin, body: { status: "returned" },
  });
  const dAfterReturn = (await req("GET", "/api/v1/shop/points", { token: member, pk: PK })).json;
  check("returning the discounted order claws back exactly what it awarded",
    dAfterReturn.points === mPointsReplay.points,
    `${dPoints.points} → ${dAfterReturn.points}, want ${mPointsReplay.points}`);

  // ── ⑨ 會員路徑：出貨 → 退貨 → 降級 ──
  section("⑨ Member path: ship → return → clawback and tier downgrade");
  const mShip = await req("POST", shopPath(`/orders/${mOrder.id}/shipments`), {
    token: admin, body: { carrier: freeMethod.carrier, tracking_number: `E2E-${runID}-M` },
  });
  need(mShip.status === 201, `create shipment failed: ${brief(mShip)}`);
  const mShipView = await req("GET", `/api/v1/shop/orders/${mOrder.id}/shipment`, { token: member, pk: PK });
  check("the member can read their own shipment", mShipView.status === 200 && mShipView.json.status === 1, brief(mShipView));

  const mReturn = await req("PUT", shopPath(`/orders/${mOrder.id}/shipments/${mShip.json.id}`), {
    token: admin, body: { status: "returned" },
  });
  check("merchant marks the member's shipment returned", mReturn.status === 200 && mReturn.json.status === 3, brief(mReturn));

  const mPointsAfter = (await req("GET", "/api/v1/shop/points", { token: member, pk: PK })).json;
  check("the clawback restores the balance the member had before this order",
    mPointsAfter.points === pointsBefore.points, `${pointsBefore.points} vs ${mPointsAfter.points}`);
  // Compared against the ladder's answer for the restored balance rather than
  // against the tier the member literally held before: a customer with no
  // ledger history at all starts at level NULL, and the first clawback is
  // itself a ledger row, so it correctly lands them on the 0-point tier.
  check("the tier follows the balance back down to the ladder's answer for it",
    tierName(mPointsAfter.level) === tierName(tierFor(tiers, pointsBefore.points)),
    `${tierName(mPointsAfter.level)} vs expected ${tierName(tierFor(tiers, pointsBefore.points))}`);
  const mLedgerAfter = (await req("GET", "/api/v1/shop/points/transactions", { token: member, pk: PK })).json;
  const forThisOrder = (mLedgerAfter.transactions ?? []).filter((t) => t.order_id === mOrder.id);
  check("this order contributed exactly two ledger rows: +award then −award",
    forThisOrder.length === 2 &&
      forThisOrder.some((t) => t.kind === 0 && t.points_delta === mAward) &&
      forThisOrder.some((t) => t.kind === 1 && t.points_delta === -mAward),
    JSON.stringify(forThisOrder));

  // ── ⑩ 取消訂單歸還庫存 ──
  section("⑩ Cancelling an order restores stock");
  const stockBeforeCancel = await skuStock(BOUQUET_SKU);
  await req("DELETE", "/api/v1/shop/cart/items", { token: member, pk: PK });
  await req("POST", "/api/v1/shop/cart/items", { token: member, pk: PK, body: { sku_id: sku.id, quantity: 1 } });
  const plain = await req("POST", "/api/v1/shop/checkout", {
    token: member, pk: PK,
    body: { recipient_name: "E2E 會員", phone: "0922333444", line1: "台北市大安區永康街 12 號" },
  });
  need(plain.status === 201, `member checkout without a shipping method failed: ${brief(plain)}`);
  check("omitting the shipping method leaves the total equal to the subtotal",
    plain.json.shipping_fee === 0 && plain.json.shipping_method_id === null &&
      plain.json.total_amount === plain.json.subtotal_amount,
    JSON.stringify([plain.json.subtotal_amount, plain.json.shipping_fee, plain.json.total_amount]));
  check("that checkout decremented stock",
    (await skuStock(BOUQUET_SKU)) === stockBeforeCancel - 1, String(stockBeforeCancel));
  const cancelled = await req("POST", `/api/v1/shop/orders/${plain.json.id}/cancel`, { token: member, pk: PK });
  check("the member can cancel an unpaid order", cancelled.status === 200 && cancelled.json.status === 1, brief(cancelled));
  check("cancellation restores the stock it held",
    (await skuStock(BOUQUET_SKU)) === stockBeforeCancel, String(stockBeforeCancel));

  // ── ⑪ 冪等鍵：重送不會建立第二筆訂單 ──
  //
  // This section exists for one reason that no Go test can cover: every
  // integration test builds its own router, so nothing in `go test ./...`
  // reaches internal/app/wire.go. Forget to attach the idempotency service
  // there and the whole suite stays green while the real server silently
  // ignores Idempotency-Key — the exact failure mode change commerce-demo-seed
  // measured for the event subscriptions. This is the only assertion standing
  // between that mistake and duplicate orders in production.
  section("⑪ Idempotency-Key: a re-sent checkout replays instead of duplicating");
  const idemKey = `e2e-idem-${runID}`;
  const idemBody = {
    email: `e2e-idem-${runID}@example.com`,
    items: [{ sku_id: sku.id, quantity: 1 }],
    shipping_address: { recipient_name: "E2E 重送", phone: "0912345678", line1: "台北市中山區南京東路一段 1 號" },
  };
  const stockBeforeIdem = await skuStock(BOUQUET_SKU);
  const idemFirst = await req("POST", "/api/v1/shop/guest/checkout", {
    pk: PK, headers: { "Idempotency-Key": idemKey }, body: idemBody,
  });
  need(idemFirst.status === 201, `idempotent guest checkout failed: ${brief(idemFirst)}`);
  check("the first keyed checkout is not marked as a replay",
    idemFirst.headers.get("Idempotency-Replayed") === null, String(idemFirst.headers.get("Idempotency-Replayed")));
  const stockAfterFirstIdem = await skuStock(BOUQUET_SKU);

  const idemReplay = await req("POST", "/api/v1/shop/guest/checkout", {
    pk: PK, headers: { "Idempotency-Key": idemKey }, body: idemBody,
  });
  check("re-sending the same key returns 201, indistinguishable from the original",
    idemReplay.status === 201, brief(idemReplay));
  check("the replay is the SAME order, not a second one",
    idemReplay.json?.order?.id === idemFirst.json.order.id,
    `${idemReplay.json?.order?.id} vs ${idemFirst.json.order.id}`);
  check("the replay is marked with Idempotency-Replayed",
    idemReplay.headers.get("Idempotency-Replayed") === "true",
    String(idemReplay.headers.get("Idempotency-Replayed")));
  check("the replay does NOT deduct stock a second time",
    (await skuStock(BOUQUET_SKU)) === stockAfterFirstIdem,
    `${stockBeforeIdem} → ${stockAfterFirstIdem} → ${await skuStock(BOUQUET_SKU)}`);
  check("the replay hands back a usable order access token",
    typeof idemReplay.json?.order_access_token === "string" && idemReplay.json.order_access_token.length > 0);

  const reused = await req("POST", "/api/v1/shop/guest/checkout", {
    pk: PK, headers: { "Idempotency-Key": idemKey },
    body: { ...idemBody, items: [{ sku_id: sku.id, quantity: 2 }] },
  });
  check("the same key with a different body is refused", reused.status === 422, brief(reused));
  check("...with the idempotency_key_reused code",
    reused.json?.error?.code === "idempotency_key_reused", brief(reused));
  check("a refused reuse touches no stock",
    (await skuStock(BOUQUET_SKU)) === stockAfterFirstIdem, String(stockAfterFirstIdem));

  // ── ⑫ 綠界 ECPay：表單導向、1|OK、模擬付款不付錢、反查對帳 ──
  //
  // Driven entirely through the API: the callbacks are signed by this script's
  // own CheckMacValue implementation (lib.mjs), which is a SECOND, independent
  // implementation of 綠界's algorithm and is verified against the same
  // published vector the Go unit test uses. `make ecpay-sim` exists for humans;
  // this chain needs nothing but the API.
  //
  // It uses the MEMBER path on purpose: guest checkout is rate limited (20 per
  // 10 minutes per IP), and this script is meant to be re-runnable.
  section("⑫ 綠界 ECPay: form redirect, 1|OK, simulated payment, reconciliation lookup");

  const ecPointsBefore = (await req("GET", "/api/v1/shop/points", { token: member, pk: PK })).json;
  await req("DELETE", "/api/v1/shop/cart/items", { token: member, pk: PK });
  await req("POST", "/api/v1/shop/cart/items", { token: member, pk: PK, body: { sku_id: sku.id, quantity: 1 } });
  const ecCheckout = await req("POST", "/api/v1/shop/checkout", {
    token: member, pk: PK,
    body: {
      shipping_method_id: paidMethod.id,
      recipient_name: "E2E 綠界", phone: "0955666777", line1: "台北市信義區松高路 11 號",
    },
  });
  need(ecCheckout.status === 201, `ecpay member checkout failed: ${brief(ecCheckout)}`);
  const ecOrder = ecCheckout.json;

  const ecPay = await req("POST", `/api/v1/shop/orders/${ecOrder.id}/payments`, {
    token: member, pk: PK, body: { provider: "ecpay" },
  });
  need(ecPay.status === 201, `ecpay payment initiation failed: ${brief(ecPay)}`);

  // design D2: 綠界's cashier is a form POST, so a single redirect URL cannot
  // carry it. The API hands back the field set; redirect_url survives as a v1
  // key with no meaning for this provider.
  const ecForm = ecPay.json.redirect_form;
  check("ECPay returns a redirect FORM, not a URL",
    ecForm !== null && ecForm?.method === "POST" && typeof ecForm?.action === "string",
    JSON.stringify([ecPay.json.redirect_url, ecForm?.method, ecForm?.action]));
  check("redirect_url is empty for a form-POST provider",
    ecPay.json.redirect_url === "", JSON.stringify(ecPay.json.redirect_url));
  check("every field 綠界 requires is present",
    ["MerchantID", "MerchantTradeNo", "MerchantTradeDate", "PaymentType", "TotalAmount",
      "TradeDesc", "ItemName", "ReturnURL", "ChoosePayment", "EncryptType", "CheckMacValue"]
      .every((k) => (ecForm?.fields?.[k] ?? "") !== ""),
    JSON.stringify(Object.keys(ecForm?.fields ?? {})));
  // The one place the OpenAPI money check cannot reach (design D17): form values
  // are strings, so the contract declares no per-field schema for them.
  check("TotalAmount is the payable total with NO scaling",
    ecForm?.fields?.TotalAmount === String(ecOrder.total_amount),
    `${ecForm?.fields?.TotalAmount} vs ${ecOrder.total_amount}`);
  check("the CheckMacValue we generated is the one an independent implementation computes",
    ecForm?.fields?.CheckMacValue === ecpayCheckMac(ecForm.fields),
    `${ecForm?.fields?.CheckMacValue} vs ${ecpayCheckMac(ecForm?.fields ?? {})}`);

  // design D6: 綠界's back-office 模擬付款 button sends RtnCode=1 / RtnMsg=交易成功
  // and settles nothing. Reading only RtnCode marks the order paid, awards
  // points and queues it for shipping, with nothing in the bank.
  const ecSim = await deliverECPayCallback(shopID, ecPay.json.provider_reference, ecOrder.total_amount, "simulate");
  check("a simulated-payment notification is still acknowledged with exactly 1|OK",
    ecSim.status === 200 && (ecSim.text ?? "").trim() === "1|OK", brief(ecSim));
  const ecAfterSim = await req("GET", `/api/v1/shop/orders/${ecOrder.id}`, { token: member, pk: PK });
  check("SimulatePaid=1 does NOT mark the order paid (綠界 settles no money for it)",
    ecAfterSim.json?.payment_status === 0, String(ecAfterSim.json?.payment_status));
  const ecPointsAfterSim = (await req("GET", "/api/v1/shop/points", { token: member, pk: PK })).json;
  check("...and awards no points",
    ecPointsAfterSim.points === ecPointsBefore.points,
    `${ecPointsBefore.points} → ${ecPointsAfterSim.points}`);

  // And the row is still claimable — that is the whole reason Outcome has a
  // third value: mapping the above to "failed" would make this delivery lose
  // the CAS, and the order would never become paid.
  const ecReal = await deliverECPayCallback(shopID, ecPay.json.provider_reference, ecOrder.total_amount, "success");
  check("the real notification is acknowledged with exactly 1|OK (no JSON envelope)",
    ecReal.status === 200 && (ecReal.text ?? "").trim() === "1|OK", brief(ecReal));
  const ecAfterPay = await req("GET", `/api/v1/shop/orders/${ecOrder.id}`, { token: member, pk: PK });
  check("the real notification marks the order paid",
    ecAfterPay.json?.payment_status === 1, String(ecAfterPay.json?.payment_status));

  // design D4/D10/D12: manual reconciliation starts from 綠界's own number.
  const lookup = await req("GET", shopPath(`/payments?q=${encodeURIComponent(ecReal.tradeNo)}`), { token: admin });
  check("the merchant can find the order from 綠界's OWN transaction number",
    lookup.status === 200 && lookup.json?.payments?.length === 1 &&
      lookup.json.payments[0].order_id === ecOrder.id,
    brief(lookup));
  const paidRow = lookup.json?.payments?.[0] ?? {};
  check("the reconciliation row carries 付款單號、金額、時間",
    paidRow.provider_reference === ecPay.json.provider_reference &&
      paidRow.provider_transaction_id === ecReal.tradeNo &&
      paidRow.amount === ecOrder.total_amount &&
      typeof paidRow.paid_at === "string",
    JSON.stringify([paidRow.provider_reference, paidRow.provider_transaction_id, paidRow.amount, paidRow.paid_at]));

  // Ship + return it so this section leaves the member's balance where it found
  // it, the same discipline sections ③/⑧ follow.
  const ecShip = await req("POST", shopPath(`/orders/${ecOrder.id}/shipments`), {
    token: admin, body: { carrier: paidMethod.carrier, tracking_number: `E2E-${runID}-EC` },
  });
  need(ecShip.status === 201, `create shipment (ecpay order) failed: ${brief(ecShip)}`);
  await req("PUT", shopPath(`/orders/${ecOrder.id}/shipments/${ecShip.json.id}`), {
    token: admin, body: { status: "returned" },
  });
  const ecPointsRestored = (await req("GET", "/api/v1/shop/points", { token: member, pk: PK })).json;
  check("returning the ECPay order claws back exactly what it awarded",
    ecPointsRestored.points === ecPointsBefore.points,
    `${ecPointsBefore.points} → ${ecPointsRestored.points}`);

  // design D9: the customer pays after the order was cancelled. The money is
  // recorded truthfully; the order is NOT advanced and earns no points. Before
  // this change the same sequence produced an order that was cancelled AND paid,
  // with its stock already back on the shelf and points awarded for it.
  await req("DELETE", "/api/v1/shop/cart/items", { token: member, pk: PK });
  await req("POST", "/api/v1/shop/cart/items", { token: member, pk: PK, body: { sku_id: sku.id, quantity: 1 } });
  const orphanCheckout = await req("POST", "/api/v1/shop/checkout", {
    token: member, pk: PK,
    body: { recipient_name: "E2E 取消", phone: "0955000111", line1: "台北市信義區松高路 12 號" },
  });
  need(orphanCheckout.status === 201, `orphan checkout failed: ${brief(orphanCheckout)}`);
  const orphanOrder = orphanCheckout.json;
  const orphanPay = await req("POST", `/api/v1/shop/orders/${orphanOrder.id}/payments`, {
    token: member, pk: PK, body: { provider: "ecpay" },
  });
  need(orphanPay.status === 201, `orphan payment initiation failed: ${brief(orphanPay)}`);
  const orphanStockBefore = await skuStock(BOUQUET_SKU);
  const orphanCancel = await req("POST", `/api/v1/shop/orders/${orphanOrder.id}/cancel`, { token: member, pk: PK });
  need(orphanCancel.status === 200, `orphan cancel failed: ${brief(orphanCancel)}`);
  check("cancelling returns the stock the pending payment was holding",
    (await skuStock(BOUQUET_SKU)) === orphanStockBefore + 1, String(orphanStockBefore));

  const orphanHook = await deliverECPayCallback(shopID, orphanPay.json.provider_reference, orphanOrder.total_amount, "success");
  check("a payment that lands after cancellation is still acknowledged with 1|OK",
    orphanHook.status === 200 && (orphanHook.text ?? "").trim() === "1|OK", brief(orphanHook));
  const orphanAfter = await req("GET", `/api/v1/shop/orders/${orphanOrder.id}`, { token: member, pk: PK });
  check("a CANCELLED order is never advanced to paid",
    orphanAfter.json?.status === 1 && orphanAfter.json?.payment_status === 0,
    JSON.stringify([orphanAfter.json?.status, orphanAfter.json?.payment_status]));
  const orphanPoints = (await req("GET", "/api/v1/shop/points", { token: member, pk: PK })).json;
  check("...and earns no points (it will never ship)",
    orphanPoints.points === ecPointsBefore.points, `${ecPointsBefore.points} → ${orphanPoints.points}`);
  const orphanLookup = await req("GET",
    shopPath(`/payments?q=${encodeURIComponent(orphanPay.json.provider_reference)}`), { token: admin });
  check("but the merchant can always find the money that needs a manual refund",
    orphanLookup.json?.payments?.length === 1 &&
      orphanLookup.json.payments[0].status === 1 &&
      orphanLookup.json.payments[0].order_id === orphanOrder.id,
    brief(orphanLookup));

  // ── ⑬ 部分出貨：一張訂單、兩箱、兩次 ──
  //
  // change partial-shipment. Deliberately the whole arc rather than a spot
  // check, because every interesting property here is about a *sequence*: the
  // order-level status is derived from all shipments (never asserted), so the
  // only way to catch a wrong derivation is to walk the states.
  //
  // Two S/L bouquets rather than the tracked BQ-ANNI-M: this section consumes
  // two units permanently (the order ends delivered, and only cancellation
  // restocks), and spending them on the M budget would cut the runs-per-seed
  // roughly in half. S has 24 and L has 18 seeded, so this is the section that
  // constrains nothing.
  section("⑬ Partial shipment: one order, two parcels, derived order status");
  const partSKUs = ["S", "L"].map((sz) =>
    (detail.json.skus ?? []).find((s) => s.sku_code === `${BOUQUET_SKU.slice(0, -1)}${sz}`));
  need(partSKUs.every(Boolean),
    `seeded S/L bouquet SKUs missing — run \`make seed-demo\``);

  await req("DELETE", "/api/v1/shop/cart/items", { token: member, pk: PK });
  for (const s of partSKUs) {
    await req("POST", "/api/v1/shop/cart/items", { token: member, pk: PK, body: { sku_id: s.id, quantity: 1 } });
  }
  const pRes = await req("POST", "/api/v1/shop/checkout", {
    token: member, pk: PK,
    body: {
      recipient_name: "E2E 分批", phone: "0955666777", line1: "台北市信義區松高路 11 號",
      shipping_method_id: freeMethod.id,
    },
  });
  need(pRes.status === 201, `partial-shipment checkout failed: ${brief(pRes)}`);
  const pOrder = pRes.json;
  need((pOrder.items ?? []).length === 2, `expected a two-line order, got ${(pOrder.items ?? []).length}`);
  check("a fresh order reports every line as entirely unshipped",
    pOrder.items.every((i) => i.shipped_quantity === 0) && pOrder.fulfillment_status === 0,
    JSON.stringify(pOrder.items.map((i) => i.shipped_quantity)));

  // Shipping needs a PAID order (change order-and-invitation-polish). Paying
  // awards points to the shared demo customer, so the section hands them back
  // at the end with a manual adjustment (a delivered parcel cannot be returned,
  // and this section needs it delivered) — that is what keeps this file
  // re-runnable.
  const pPointsBefore = (await req("GET", "/api/v1/shop/points", { token: member, pk: PK })).json?.points;
  const pPay = await req("POST", `/api/v1/shop/orders/${pOrder.id}/payments`, { token: member, pk: PK, body: {} });
  need(pPay.status === 201, `partial-shipment payment initiation failed: ${brief(pPay)}`);
  const pHook = await deliverWebhook(pPay.json.provider_reference);
  need(pHook.status === 200, `partial-shipment payment webhook failed: ${brief(pHook)}`);

  // The plural customer endpoint answers 200 with an empty collection before
  // anything ships — a list's empty case is not a 404 (design D6).
  const pNone = await req("GET", `/api/v1/shop/orders/${pOrder.id}/shipments`, { token: member, pk: PK });
  check("the customer's shipment list is an empty collection, not a 404, before anything ships",
    pNone.status === 200 && (pNone.json?.shipments ?? []).length === 0, brief(pNone));

  // 第一箱：只出第一個品項。
  const [lineA, lineB] = pOrder.items;
  const ship1 = await req("POST", shopPath(`/orders/${pOrder.id}/shipments`), {
    token: admin,
    body: {
      carrier: freeMethod.carrier, tracking_number: `E2E-${runID}-P1`,
      items: [{ order_item_id: lineA.id, quantity: 1 }],
    },
  });
  need(ship1.status === 201, `first partial shipment failed: ${brief(ship1)}`);
  check("a parcel says exactly what is in it",
    (ship1.json.items ?? []).length === 1 &&
      ship1.json.items[0].order_item_id === lineA.id &&
      ship1.json.items[0].quantity === 1,
    JSON.stringify(ship1.json.items));

  const afterFirst = await req("GET", `/api/v1/shop/orders/${pOrder.id}`, { token: member, pk: PK });
  check("shipping part of an order makes it PARTIALLY SHIPPED (the new value 4)",
    afterFirst.json?.fulfillment_status === 4, JSON.stringify(afterFirst.json?.fulfillment_status));
  const afterFirstLines = afterFirst.json?.items ?? [];
  check("the shipped line reports what left and the untouched line reports nothing",
    afterFirstLines.find((i) => i.id === lineA.id)?.shipped_quantity === 1 &&
      afterFirstLines.find((i) => i.id === lineB.id)?.shipped_quantity === 0,
    JSON.stringify(afterFirstLines.map((i) => [i.id, i.shipped_quantity])));
  // The order lines must not reorder themselves just because one of them was
  // updated. order_items only became a mutable table in this change, and an
  // unordered eager-load returns the just-updated row last.
  check("the order's lines keep their original order after a line is updated",
    afterFirstLines.map((i) => i.id).join(",") === pOrder.items.map((i) => i.id).join(","),
    `${pOrder.items.map((i) => i.id)} → ${afterFirstLines.map((i) => i.id)}`);

  const oneParcel = await req("GET", `/api/v1/shop/orders/${pOrder.id}/shipments`, { token: member, pk: PK });
  check("the customer sees the one parcel that has gone out",
    oneParcel.status === 200 && (oneParcel.json?.shipments ?? []).length === 1 &&
      oneParcel.json.shipments[0].tracking_number === `E2E-${runID}-P1`,
    brief(oneParcel));

  // 超出訂購量：被拒，而且是 422（請求主體上的值），不是 409。
  const overShip = await req("POST", shopPath(`/orders/${pOrder.id}/shipments`), {
    token: admin,
    body: { carrier: freeMethod.carrier, items: [{ order_item_id: lineA.id, quantity: 1 }] },
  });
  check("shipping more of a line than was ordered is refused (422)",
    overShip.status === 422, brief(overShip));
  const stillFour = await req("GET", `/api/v1/shop/orders/${pOrder.id}`, { token: member, pk: PK });
  check("a refused shipment changes nothing",
    stillFour.json?.fulfillment_status === 4 &&
      (stillFour.json?.items ?? []).find((i) => i.id === lineA.id)?.shipped_quantity === 1,
    brief(stillFour));

  // 第二箱：省略 items ＝ 出貨全部未出品項（design D5，與本 change 之前逐位元組相同）。
  const ship2 = await req("POST", shopPath(`/orders/${pOrder.id}/shipments`), {
    token: admin, body: { carrier: freeMethod.carrier, tracking_number: `E2E-${runID}-P2` },
  });
  need(ship2.status === 201, `second (remainder) shipment failed: ${brief(ship2)}`);
  check("omitting the item list ships exactly what was still outstanding",
    (ship2.json.items ?? []).length === 1 && ship2.json.items[0].order_item_id === lineB.id,
    JSON.stringify(ship2.json.items));

  const afterSecond = await req("GET", `/api/v1/shop/orders/${pOrder.id}`, { token: member, pk: PK });
  check("once every line has gone out the order is SHIPPED, not still partial",
    afterSecond.json?.fulfillment_status === 1, JSON.stringify(afterSecond.json?.fulfillment_status));

  const bothParcels = await req("GET", `/api/v1/shop/orders/${pOrder.id}/shipments`, { token: member, pk: PK });
  const parcelIDs = (bothParcels.json?.shipments ?? []).map((s) => s.id);
  check("the customer sees both parcels, newest first",
    parcelIDs.length === 2 && parcelIDs[0] > parcelIDs[1], JSON.stringify(parcelIDs));
  // 單數端點在多筆出貨下回**最近一筆**，不是 404 —— 它今天的 .Only() 會壞掉。
  const singular = await req("GET", `/api/v1/shop/orders/${pOrder.id}/shipment`, { token: member, pk: PK });
  check("the singular endpoint still answers on a multi-parcel order (the newest one)",
    singular.status === 200 && singular.json?.id === parcelIDs[0], brief(singular));

  // 一箱送達不會讓整張訂單變成已送達。
  const deliver1 = await req("PUT", shopPath(`/orders/${pOrder.id}/shipments/${ship1.json.id}`), {
    token: admin, body: { status: "delivered" },
  });
  need(deliver1.status === 200, `delivering the first parcel failed: ${brief(deliver1)}`);
  const oneDelivered = await req("GET", `/api/v1/shop/orders/${pOrder.id}`, { token: member, pk: PK });
  check("one parcel arriving does NOT mark the whole order delivered",
    oneDelivered.json?.fulfillment_status === 1, JSON.stringify(oneDelivered.json?.fulfillment_status));

  const deliver2 = await req("PUT", shopPath(`/orders/${pOrder.id}/shipments/${ship2.json.id}`), {
    token: admin, body: { status: "delivered" },
  });
  need(deliver2.status === 200, `delivering the second parcel failed: ${brief(deliver2)}`);
  const allDelivered = await req("GET", `/api/v1/shop/orders/${pOrder.id}`, { token: member, pk: PK });
  check("only when every parcel has arrived is the order DELIVERED",
    allDelivered.json?.fulfillment_status === 2, JSON.stringify(allDelivered.json?.fulfillment_status));

  // 全部出完之後再出貨：409（沒有未出品項），不是 422。
  const nothingLeft = await req("POST", shopPath(`/orders/${pOrder.id}/shipments`), {
    token: admin, body: { carrier: freeMethod.carrier },
  });
  check("a fully shipped order refuses a further parcel (409)",
    nothingLeft.status === 409, brief(nothingLeft));

  // Hand the points back (see above) with a merchant adjustment of exactly the award.
  const pPointsMid = (await req("GET", "/api/v1/shop/points", { token: member, pk: PK })).json?.points;
  if (pPointsMid !== pPointsBefore) {
    const give = await req("POST", shopPath(`/members/${pOrder.member_id}/points/adjust`), {
      token: admin, body: { points_delta: pPointsBefore - pPointsMid, reason: "e2e: undo partial-shipment award" },
    });
    need(give.status === 200 || give.status === 201, `points hand-back failed: ${brief(give)}`);
  }
  const pPointsAfter = (await req("GET", "/api/v1/shop/points", { token: member, pk: PK })).json?.points;
  check("the section is points-neutral again (re-runnable)",
    pPointsAfter === pPointsBefore, `${pPointsBefore} → ${pPointsMid} → ${pPointsAfter}`);

  // ── ⑭ 營運報表（change merchant-reports）──
  //
  // 這一節與其他節不同的地方在於**期望值是算得出來的**：這一輪跑過的每一筆訂單
  // 都是本檔自己建的，金額、折扣、運費在前面幾節都已經斷言過了。所以「報表的數字
  // 對不對」是一條硬斷言，不是「回了 200」。
  //
  // 它同時是唯一能證明「口徑真的被套用」的地方：本輪刻意製造過已取消的訂單（⑩）、
  // 已退貨的訂單（⑨、⑫）與未付款的訂單（⑫ 的 ECPay pending），而它們必須各自
  // 落在對帳分割的正確那一格。
  section("⑭ Merchant reports: criteria, reconciliation, timezone disclosure");
  // The window is ±1 UTC day around now, and that width is not laziness: the
  // report's days are cut in the SHOP's zone, isoDate() produces UTC days, and
  // between 16:00Z and midnight those two disagree. A one-day window would go
  // green or red depending on what time of day the suite runs.
  const from = isoDate(-1);
  const to = isoDate(1);
  const reportQS = `from=${from}&to=${to}`;

  // **A measured surprise, recorded because it is the point of this section.**
  // Every order the preceding sections create ends up cancelled, returned or
  // unpaid — that is what they exist to test — so at this moment the shop's
  // counted revenue is genuinely ZERO, and the product ranking is genuinely
  // empty. That is the report being right, and it is also why this section has
  // to place an order of its own: without one, the revenue lines would only
  // ever be asserted against 0, which any broken query also produces.
  const before = await req("GET", shopPath(`/reports/sales?${reportQS}`), { token: admin });
  need(before.status === 200, `baseline sales report failed: ${brief(before)}`);
  const baseline = (before.json?.currencies ?? []).find((c) => c.currency === "TWD")?.summary
    ?? { order_count: 0, subtotal_amount: 0, discount_amount: 0, shipping_fee: 0, total_amount: 0 };

  // One order that STAYS counted: paid, not returned, not cancelled.
  //
  // **It is placed by a throwaway GUEST identity, and that is load-bearing, not
  // stylistic.** This file is documented as repeatable, and the reason it is
  // repeatable is that every order the other sections create is eventually
  // returned or cancelled, so the demo customer's points return to where they
  // started. An order that stays paid permanently awards points — and a first
  // draft of this section used the demo member, which pushed them onto 銀卡會員
  // and made section ⑥'s "enters this order on a tier that grants no discount"
  // fail on the NEXT run. Measured, not theorised.
  //
  // A per-run guest email creates a fresh member row each time, so the points
  // land somewhere nobody asserts on and repeatability is preserved.
  //
  // It buys the S bouquet rather than the tracked M so the runs-per-seed budget
  // is unaffected (S is seeded with 24 and only ⑬ otherwise touches it).
  const reportSKU = (detail.json.skus ?? []).find((s) => s.sku_code === `${BOUQUET_SKU.slice(0, -1)}S`);
  need(reportSKU, "seeded S bouquet SKU missing — run `make seed-demo`");
  const rGuest = await req("POST", "/api/v1/shop/guest/checkout", {
    pk: PK,
    body: {
      email: `e2e-report-${runID}@example.com`,
      items: [{ sku_id: reportSKU.id, quantity: 1 }],
      shipping_address: {
        recipient_name: "E2E 報表", phone: "0955000111", line1: "台北市中正區重慶南路一段 122 號",
      },
      shipping_method_id: paidMethod.id,
    },
  });
  need(rGuest.status === 201, `report fixture checkout failed: ${brief(rGuest)}`);
  const rOrder = rGuest.json.order;
  const rPay = await req("POST", "/api/v1/shop/guest/order/payments", {
    token: rGuest.json.order_access_token, pk: PK, body: {},
  });
  need(rPay.json?.provider_reference, `report fixture payment failed: ${brief(rPay)}`);
  need((await deliverWebhook(rPay.json.provider_reference)).status === 200, "report fixture webhook failed");

  const sales = await req("GET", shopPath(`/reports/sales?${reportQS}`), { token: admin });
  need(sales.status === 200, `sales report failed: ${brief(sales)}`);

  // 時區：demo 商家從未設定過，所以生效值必須被標示為**回落**而不是選擇。
  check("an unconfigured shop's report says its timezone is a platform fallback",
    sales.json?.timezone?.source === "default" && Boolean(sales.json?.timezone?.name),
    JSON.stringify(sales.json?.timezone));

  const twd = (sales.json?.currencies ?? []).find((c) => c.currency === "TWD");
  need(twd, `no TWD block in the report: ${brief(sales)}`);
  const sum = twd.summary;

  // 金額的組合恆等式，套在彙總值上。它成立不是巧合——報表只做 int64 加法，
  // 而每一筆訂單的組成本身由 DB CHECK 強制。
  check("report: subtotal − discount = net",
    sum.net_amount === sum.subtotal_amount - sum.discount_amount,
    JSON.stringify([sum.subtotal_amount, sum.discount_amount, sum.net_amount]));
  check("report: net + shipping = payable total",
    sum.total_amount === sum.net_amount + sum.shipping_fee,
    JSON.stringify([sum.net_amount, sum.shipping_fee, sum.total_amount]));

  // **這是本節最重要的一組斷言**：報表的增量必須逐項等於那一筆訂單自己的欄位。
  // 期望值不是猜的——`rOrder` 是本檔剛剛建立的，它的每一個金額都在回應裡。
  check("report: the paid order moved order_count by exactly one",
    sum.order_count === baseline.order_count + 1, `${baseline.order_count} → ${sum.order_count}`);
  check("report: subtotal moved by exactly the order's own subtotal",
    sum.subtotal_amount === baseline.subtotal_amount + rOrder.subtotal_amount,
    `${baseline.subtotal_amount} + ${rOrder.subtotal_amount} vs ${sum.subtotal_amount}`);
  check("report: the tier discount moved by exactly the order's own discount",
    sum.discount_amount === baseline.discount_amount + rOrder.discount_amount,
    `${baseline.discount_amount} + ${rOrder.discount_amount} vs ${sum.discount_amount}`);
  check("report: shipping moved by exactly the selected method's flat rate",
    sum.shipping_fee === baseline.shipping_fee + paidMethod.flat_rate,
    `${baseline.shipping_fee} + ${paidMethod.flat_rate} vs ${sum.shipping_fee}`);
  check("report: the payable total moved by exactly what will be charged",
    sum.total_amount === baseline.total_amount + rOrder.total_amount,
    `${baseline.total_amount} + ${rOrder.total_amount} vs ${sum.total_amount}`);

  // 對帳分割：四類互斥且窮盡。與訂單列表的總筆數逐一比對——那是商家自己會做的
  // 動作，也是這份報表唯一能被驗證的方式。
  const partition = sum.order_count + sum.returned_order_count
    + sum.cancelled_order_count + sum.unpaid_order_count;
  check("report: the four classes are a partition (counted + returned + cancelled + unpaid)",
    partition > 0, JSON.stringify([sum.order_count, sum.returned_order_count,
      sum.cancelled_order_count, sum.unpaid_order_count]));

  // 本輪一定製造過這三種訂單，所以三個計數都不得為零——一份把它們算進營業額
  // （或整批漏掉）的報表會在這裡露出來。
  check("report: this run's cancelled order landed in the cancelled class",
    sum.cancelled_order_count >= 1, String(sum.cancelled_order_count));
  check("report: this run's returned orders landed in the returned class, not in revenue",
    sum.returned_order_count >= 1 && sum.returned_total_amount > 0,
    JSON.stringify([sum.returned_order_count, sum.returned_total_amount]));
  check("report: this run's unpaid order landed in the unpaid class",
    sum.unpaid_order_count >= 1, String(sum.unpaid_order_count));

  // 期間內建立的訂單總數，由訂單列表獨立取得——不同的端點、不同的查詢、不同的
  // 程式路徑（ent vs 原生 SQL）。兩者相等是「四類窮盡且互斥」的證明；不等就是
  // 報表漏算或重複計算了某一類，而那正是商家會自己做的核對動作。
  //
  // 日期在這裡**以商家的生效時區重新推導一次**（報表自己說了那是哪一個時區），
  // 所以這條斷言順帶端到端驗證了時區換算：拿 UTC 日去比會在 16:00Z 之後對不上。
  const tzName = sales.json.timezone.name;
  const localDay = (isoStr) => new Intl.DateTimeFormat("en-CA", {
    timeZone: tzName, year: "numeric", month: "2-digit", day: "2-digit",
  }).format(new Date(isoStr));

  const allOrders = [];
  for (let page = 1; ; page++) {
    const r = await req("GET", shopPath(`/orders?page=${page}&page_size=100`), { token: admin });
    need(r.status === 200, `order list page ${page} failed: ${brief(r)}`);
    allOrders.push(...(r.json?.orders ?? []));
    if (allOrders.length >= (r.json?.total ?? 0) || (r.json?.orders ?? []).length === 0) break;
  }
  const inWindow = allOrders.filter((o) => {
    const d = localDay(o.created_at);
    return o.currency === "TWD" && d >= from && d <= to;
  }).length;
  check("report: the partition equals the order list's own count for the same window",
    partition === inWindow, `report=${partition} list=${inWindow} tz=${tzName}`);

  // 商品排行：口徑相同，金額折扣前，且不回傳識別碼（它們可能指向已刪除的列）。
  const ranking = await req("GET", shopPath(`/reports/products?${reportQS}`), { token: admin });
  need(ranking.status === 200, `product ranking failed: ${brief(ranking)}`);
  const rankTWD = (ranking.json?.currencies ?? []).find((c) => c.currency === "TWD");
  need(rankTWD, `no TWD block in the ranking: ${brief(ranking)}`);
  check("ranking lists the bouquet this section actually sold, and nothing that was returned",
    rankTWD.items.some((i) => i.sku_code === reportSKU.sku_code),
    JSON.stringify(rankTWD.items.map((i) => i.sku_code)));
  // 折扣前：這一筆若套用了等級折扣，排行上的金額仍必須是單價 × 數量。
  const rankedRow = rankTWD.items.find((i) => i.sku_code === reportSKU.sku_code);
  check("ranking amounts are pre-discount (price × quantity), matching the order's own line",
    rankedRow.gross_amount >= rOrder.items[0].line_total,
    `${rankedRow.gross_amount} vs line_total ${rOrder.items[0].line_total}`);
  check("ranking rows carry snapshots only — no product_id/sku_id to build a dead link from",
    rankTWD.items.every((i) => i.product_id === undefined && i.sku_id === undefined),
    JSON.stringify(rankTWD.items[0] ?? {}));
  check("ranking is ordered by gross amount descending",
    rankTWD.items.every((it, i) => i === 0 || rankTWD.items[i - 1].gross_amount >= it.gross_amount),
    JSON.stringify(rankTWD.items.map((i) => i.gross_amount)));

  // 期間是必填的：一份沒講清楚期間的報表，是一份數字無法被重現的報表。
  const noPeriod = await req("GET", shopPath("/reports/sales"), { token: admin });
  check("a report without an explicit period is refused (422), not silently defaulted",
    noPeriod.status === 422, brief(noPeriod));

  await mediaPath(admin, shopID, shopPath);
}

// ── 商品圖片（change product-images-r2）──
//
// API 層走完 presign → 直傳 PUT → attach，不需要瀏覽器。用一個**這一輪專屬的
// 暫時商品**，結束時刪掉（連同儲存空間裡的物件），所以不會破壞這支腳本的
// 可重複執行性，也不會動到 demo 商品。API 未設定媒體儲存時整段略過。
async function mediaPath(admin, shopID, shopPath) {
  section("⑬ Product images: presign -> direct PUT -> attach -> public read");
  const cfg = await req("GET", "/api/v1/admin/media/config", { token: admin });
  if (cfg.json?.enabled !== true) {
    console.log("  - media storage is not configured on this API: image checks skipped");
    // Still worth asserting: routes are mounted and answer the documented 503.
    const off = await req("POST", shopPath("/media/uploads"), {
      token: admin, body: { content_type: "image/jpeg", size_bytes: 10, purpose: "product_image" },
    });
    check("disabled media answers 503 media_not_configured (routes stay mounted)",
      off.status === 503 && off.json?.error?.code === "media_not_configured", brief(off));
    return;
  }

  const slug = `e2e-media-${runID}`;
  const created = await req("POST", shopPath("/products"), {
    token: admin,
    body: { title: `E2E media ${runID}`, slug, status: 1,
      skus: [{ sku_code: `E2E-MEDIA-${runID}`, price_amount: 100, stock_qty: 1 }] },
  });
  need(created.status === 201, `temp product create failed: ${brief(created)}`);
  const pid = created.json.id;
  try {
    const data = readFileSync(new URL("./fixtures/exif-orientation-6.jpg", import.meta.url));
    const upload = async (purpose, type = "image/jpeg", body = data) => {
      const intent = await req("POST", shopPath("/media/uploads"), {
        token: admin, body: { content_type: type, size_bytes: body.length, purpose },
      });
      if (intent.status !== 201) return { intent };
      const put = await fetch(intent.json.upload_url, { method: "PUT", headers: intent.json.headers, body });
      return { intent, put };
    };

    const heic = await req("POST", shopPath("/media/uploads"), {
      token: admin, body: { content_type: "image/heic", size_bytes: 10, purpose: "product_image" },
    });
    check("HEIC is refused by the server (frontends convert first)", heic.status === 422, brief(heic));

    const up = await upload("product_image");
    check("presigned PUT to object storage succeeds", up.put?.status === 200, String(up.put?.status));
    const wrongType = await fetch(up.intent.json.upload_url, {
      method: "PUT", headers: { "Content-Type": "text/html" }, body: data,
    });
    check("the presigned URL is bound to its signed Content-Type (a different one is refused)",
      wrongType.status >= 400, String(wrongType.status));

    const attach = await req("POST", shopPath(`/products/${pid}/images`), {
      token: admin, body: { upload_id: up.intent.json.upload_id, alt: "e2e alt" },
    });
    check("attach converts and returns 201", attach.status === 201, brief(attach));
    check("variants: EXIF applied, portrait 400x600, thumb/large WebP urls",
      attach.json?.width === 400 && attach.json?.height === 600 &&
        attach.json?.urls?.large?.endsWith("/large.webp") && attach.json?.urls?.thumb?.endsWith("/thumb.webp"),
      JSON.stringify(attach.json));
    const asset = await fetch(attach.json.urls.large);
    check("the converted object is publicly readable as image/webp",
      asset.status === 200 && asset.headers.get("content-type") === "image/webp",
      `${asset.status} ${asset.headers.get("content-type")}`);

    const reuse = await req("POST", shopPath(`/products/${pid}/images`), {
      token: admin, body: { upload_id: up.intent.json.upload_id },
    });
    check("reusing an upload_id is a 409", reuse.status === 409, brief(reuse));

    const html = Buffer.from("<html><script>alert(1)</script></html>");
    const bad = await upload("product_image", "image/jpeg", html);
    const badAttach = await req("POST", shopPath(`/products/${pid}/images`), {
      token: admin, body: { upload_id: bad.intent.json.upload_id },
    });
    check("HTML declared as JPEG is a 422", badAttach.status === 422, brief(badAttach));

    const pub = await req("GET", `/api/v1/shop/products/${slug}`, { pk: PK });
    check("public detail carries the image", pub.json?.images?.length === 1 && pub.json.images[0].id === attach.json.id,
      brief(pub));

    // The product PUT is full-replacement for SKUs — and must leave images alone.
    const put = await req("PUT", shopPath(`/products/${pid}`), {
      token: admin,
      body: { title: `E2E media ${runID} (renamed)`, skus: created.json.skus.map((s) => ({
        id: s.id, sku_code: s.sku_code, price_amount: s.price_amount, stock_qty: s.stock_qty })) },
    });
    check("product PUT succeeds", put.status === 200, brief(put));
    const list = await req("GET", shopPath(`/products/${pid}/images`), { token: admin });
    check("product PUT left the image untouched", list.json?.images?.length === 1, brief(list));

    // Content image (CMS image field pipeline).
    const cu = await upload("content_image");
    const content = await req("POST", shopPath("/media/content-images"), {
      token: admin, body: { upload_id: cu.intent.json.upload_id },
    });
    check("content image conversion returns urls", content.status === 201 && Boolean(content.json?.urls?.large),
      brief(content));
  } finally {
    const del = await req("DELETE", shopPath(`/products/${pid}`), { token: admin });
    check("temporary product removed (images cascade)", del.status === 204, brief(del));
  }
}


// Entry-point detection: when run directly this script owns the exit code;
// when imported by happy-path.mjs it only contributes to the shared failure
// count and lets that script decide (design D2).
const isEntryPoint = Boolean(process.argv[1]) && import.meta.url === pathToFileURL(process.argv[1]).href;
if (isEntryPoint) {
  console.log(`E2E commerce path against ${API} (run ${runID})`);
  await runCommercePath();
  if (failureCount() > 0) {
    console.error(`\nE2E commerce path FAILED with ${failureCount()} failed check(s)`);
    process.exit(1);
  }
  console.log("\nE2E commerce path PASSED");
}
