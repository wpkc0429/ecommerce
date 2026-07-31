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
import { pathToFileURL } from "node:url";
import {
  API, brief, check, deliverWebhook, fail, failureCount, isoDate, req, runID, section, tierFor, tierName,
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
// returned) + 0 (cancelled orders restore stock). Returns do NOT restore stock
// — only cancellation does — so a seeded database supports a bounded number of
// runs before it needs re-seeding (design D7).
const STOCK_PER_RUN = 3;

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
  check("order amounts compose as subtotal + shipping_fee = total",
    gOrder.subtotal_amount + gOrder.shipping_fee === gOrder.total_amount,
    `${gOrder.subtotal_amount} + ${gOrder.shipping_fee} = ${gOrder.total_amount}`);
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

  const gPay = await req("POST", "/api/v1/shop/guest/order/payments", { token: gToken, pk: PK, body: {} });
  need(gPay.status === 201, `guest payment initiation failed: ${brief(gPay)}`);
  check("the amount sent to the payment provider is the PAYABLE total (shipping included)",
    gPay.json.amount === gOrder.total_amount, `charged ${gPay.json.amount}, order total ${gOrder.total_amount}`);

  const gHook = await deliverWebhook(gPay.json.provider_reference);
  check("signed provider webhook accepted", gHook.status === 200, brief(gHook));
  const gAfterPay = await req("GET", "/api/v1/shop/guest/order", { token: gToken, pk: PK });
  check("order is marked paid", gAfterPay.json?.payment_status === 1, String(gAfterPay.json?.payment_status));

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

  // ── ⑧ 會員路徑：出貨 → 退貨 → 降級 ──
  section("⑧ Member path: ship → return → clawback and tier downgrade");
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

  // ── ⑨ 取消訂單歸還庫存 ──
  section("⑨ Cancelling an order restores stock");
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

  // ── ⑩ 冪等鍵：重送不會建立第二筆訂單 ──
  //
  // This section exists for one reason that no Go test can cover: every
  // integration test builds its own router, so nothing in `go test ./...`
  // reaches internal/app/wire.go. Forget to attach the idempotency service
  // there and the whole suite stays green while the real server silently
  // ignores Idempotency-Key — the exact failure mode change commerce-demo-seed
  // measured for the event subscriptions. This is the only assertion standing
  // between that mistake and duplicate orders in production.
  section("⑩ Idempotency-Key: a re-sent checkout replays instead of duplicating");
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
