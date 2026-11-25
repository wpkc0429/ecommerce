// Orders: server-side filtered list (#/orders) and the detail (#/orders/{id}) inside the same view.
import { badge, kv } from "../parts/orders-shared.js";
import { renderOrderShipments } from "../parts/order-shipments.js";

// Filters and page survive going into an order and back (the old panel kept its inputs).
const filt = { q: "", status: "", payment: "", fulfillment: "", from: "", to: "", page: 1 };

export default async function orders(root, ctx) {
  const id = Number(ctx.route.params[0]);
  if (Number.isFinite(id) && id > 0) return detail(root, ctx, id);
  return list(root, ctx);
}

// ── List ─────────────────────────────────────────────────────────────
async function list(root, ctx) {
  const { h, rows } = ctx.ui;
  const { t, enums, format } = ctx;

  const opt = (value, text) => h("option", { value }, text);
  const q = h("input", { type: "search", id: "order-q", placeholder: t("orders.searchPlaceholder") });
  const status = h("select", { id: "order-status" },
    opt("", t("orders.filter.allStatus")), opt("0", t(enums.orderStatus[0])), opt("1", t(enums.orderStatus[1])));
  const payment = h("select", { id: "order-payment-status" },
    opt("", t("orders.filter.allPayment")), opt("0", t(enums.paymentStatus[0])), opt("1", t(enums.paymentStatus[1])));
  const fulfillment = h("select", { id: "order-fulfillment-status" },
    opt("", t("orders.filter.allFulfillment")),
    ...[0, 1, 2, 3, 4].map((v) => opt(String(v), t(enums.fulfillmentStatus[v]))));
  const from = h("input", { type: "date", id: "order-delivery-from" });
  const to = h("input", { type: "date", id: "order-delivery-to" });
  const search = h("button", { id: "order-search" }, t("orders.filter.search"));
  q.value = filt.q; status.value = filt.status; payment.value = filt.payment;
  fulfillment.value = filt.fulfillment; from.value = filt.from; to.value = filt.to;

  const note = h("p", { id: "orders-date-note", class: "hint", hidden: true },
    t("orders.dateNote.a"), h("strong", {}, t("orders.dateNote.b")), t("orders.dateNote.c"),
    h("strong", {}, t("orders.dateNote.d")), t("orders.dateNote.e"));
  const err = h("p", { id: "orders-error", class: "error", hidden: true });
  const tbody = h("tbody", { id: "orders-tbody" });
  const prev = h("button", { id: "orders-prev" }, t("orders.pager.prev"));
  const pageinfo = h("span", { id: "orders-pageinfo" });
  const next = h("button", { id: "orders-next" }, t("orders.pager.next"));

  const panel = h("div", { id: "tab-orders", class: "tab-panel" },
    h("div", { class: "toolbar" },
      h("h2", {}, t("orders.title")),
      h("div", { class: "filters" },
        q, status, payment, fulfillment,
        h("label", { class: "inline" }, `${t("orders.filter.deliveryFrom")} `, from),
        h("label", { class: "inline" }, `${t("orders.filter.deliveryTo")} `, to),
        search)),
    note, err,
    h("table", { class: "table" },
      h("thead", {}, h("tr", {},
        ...["id", "created", "recipient", "deliveryDate", "total", "order", "payment", "fulfillment", "actions"]
          .map((k) => h("th", {}, t(`orders.col.${k}`))))),
      tbody),
    h("div", { class: "pager" }, prev, pageinfo, next));
  root.append(panel);

  let seq = 0;
  async function load() {
    const my = ++seq;
    err.hidden = true;
    note.hidden = !(from.value !== "" || to.value !== "");
    let res;
    try {
      res = await ctx.data.ordersList({
        page: filt.page,
        filters: {
          status: status.value, payment_status: payment.value, fulfillment_status: fulfillment.value,
          delivery_from: from.value, delivery_to: to.value, q: q.value,
        },
      });
    } catch (e) {
      if (my !== seq || !ctx.alive()) return;
      err.textContent = ctx.errorMessage(e);
      err.hidden = false;
      return;
    }
    if (my !== seq || !ctx.alive()) return;
    filt.page = res.page;
    rows(tbody, res.orders, (o) => {
      const addr = o.shipping_address || {};
      return h("tr", { "data-order-id": String(o.id) },
        h("td", {}, o.id),
        h("td", {}, format.time(o.created_at)),
        h("td", {}, addr.recipient_name || "—"),
        h("td", {}, o.requested_delivery_date || "—"),
        h("td", { class: "money" }, format.money(o.total_amount, o.currency)),
        h("td", {}, badge(ctx, enums.orderStatus, o.status, [0])),
        h("td", {}, badge(ctx, enums.paymentStatus, o.payment_status, [1])),
        h("td", {}, badge(ctx, enums.fulfillmentStatus, o.fulfillment_status, [1, 2])),
        h("td", {}, h("button", { onclick: () => ctx.navigate("orders", o.id) }, t("orders.detailBtn"))));
    }, { empty: t("orders.empty"), colspan: 9 });

    const pages = Math.max(1, Math.ceil((res.total || 0) / res.pageSize));
    pageinfo.textContent = t("common.pageInfo", { page: filt.page, pages, total: res.total || 0 });
    prev.disabled = filt.page <= 1;
    next.disabled = filt.page >= pages;
  }

  const refilter = () => {
    filt.q = q.value; filt.status = status.value; filt.payment = payment.value;
    filt.fulfillment = fulfillment.value; filt.from = from.value; filt.to = to.value;
    filt.page = 1;
    load();
  };
  search.addEventListener("click", refilter);
  q.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); refilter(); } });
  for (const s of [status, payment, fulfillment, from, to]) s.addEventListener("change", refilter);
  prev.addEventListener("click", () => { if (filt.page > 1) { filt.page--; load(); } });
  next.addEventListener("click", () => { filt.page++; load(); });

  await load();
}

// ── Detail ───────────────────────────────────────────────────────────
async function detail(root, ctx, orderID) {
  const { h, clear, rows } = ctx.ui;
  const { t, enums, format } = ctx;

  const title = h("h2", { id: "order-detail-title" }, t("orders.title"));
  const back = h("button", { id: "order-back" }, t("orders.detail.back"));
  const pointsBtn = h("button", { id: "order-points-btn" }, t("orders.detail.points"));
  const cancelBtn = h("button", { id: "order-cancel-btn", class: "danger" }, t("orders.detail.cancel"));
  const err = h("p", { id: "order-detail-error", class: "error", hidden: true });
  const summary = h("div", { id: "order-summary", class: "detail-section" });
  const itemsBody = h("tbody", { id: "order-items-tbody" });
  const shipment = h("div", { id: "order-shipment", class: "detail-section" });
  const payments = h("div", { id: "order-payments", class: "detail-section" });

  root.append(h("div", { id: "tab-order-detail", class: "tab-panel" },
    h("div", { class: "toolbar" }, title, h("div", { class: "toolbar-actions" }, back, pointsBtn, cancelBtn)),
    err, summary,
    h("h3", {}, t("orders.detail.items")),
    h("table", { class: "table" },
      h("thead", {}, h("tr", {},
        ...["product", "sku", "price", "quantity", "shipped", "subtotal", "gift"]
          .map((k) => h("th", {}, t(`orders.item.${k}`))))),
      itemsBody),
    h("h3", {}, t("orders.detail.shipping")), shipment,
    h("h3", {}, t("orders.detail.payments")), payments));

  let current = null;
  const showError = (msg) => { err.textContent = msg; err.hidden = false; };

  function renderSummary(o) {
    const addr = o.shipping_address || {};
    clear(summary);
    const discounted = Number(o.discount_amount) > 0;
    summary.append(h("div", { class: "amount-composition", id: "order-amounts" },
      h("span", { class: "amount-part" },
        h("span", { class: "amount-label" }, t("orders.amount.subtotal")),
        h("span", { class: "money", id: "order-subtotal" }, format.money(o.subtotal_amount, o.currency))),
      discounted ? h("span", { class: "amount-op" }, "－") : "",
      discounted ? h("span", { class: "amount-part" },
        h("span", { class: "amount-label" },
          t("orders.amount.discount", { tier: o.member_tier_name ?? "—", percent: format.percent(o.discount_percent ?? 0) })),
        h("span", { class: "money", id: "order-discount" }, format.money(o.discount_amount, o.currency))) : "",
      h("span", { class: "amount-op" }, "＋"),
      h("span", { class: "amount-part" },
        h("span", { class: "amount-label" },
          o.shipping_method_name ? t("orders.amount.shippingNamed", { method: o.shipping_method_name }) : t("orders.amount.shipping")),
        h("span", { class: "money", id: "order-shipping-fee" }, format.money(o.shipping_fee, o.currency))),
      h("span", { class: "amount-op" }, "＝"),
      h("span", { class: "amount-part total" },
        h("span", { class: "amount-label" }, t("orders.amount.total")),
        h("span", { class: "money", id: "order-total" }, format.money(o.total_amount, o.currency)))));

    const row = (k, v) => kv(ctx, t(k), v);
    summary.append(h("div", { class: "detail-grid" },
      row("orders.kv.orderStatus", badge(ctx, enums.orderStatus, o.status, [0])),
      row("orders.kv.paymentStatus", badge(ctx, enums.paymentStatus, o.payment_status, [1])),
      row("orders.kv.fulfillmentStatus", badge(ctx, enums.fulfillmentStatus, o.fulfillment_status, [1, 2])),
      row("orders.kv.created", format.time(o.created_at)),
      row("orders.kv.deliveryDate", o.requested_delivery_date || t("orders.kv.unspecified")),
      row("orders.kv.shippingMethod", o.shipping_method_name || t("orders.kv.unselected")),
      row("orders.kv.recipient", addr.recipient_name || "—"),
      row("orders.kv.phone", addr.phone || "—"),
      row("orders.kv.address", [addr.postal_code, addr.city, addr.line1, addr.line2].filter(Boolean).join(" ") || "—"),
      row("orders.kv.customer", h("a", { href: `#/members/${o.member_id}` }, t("orders.kv.customerLink", { id: o.member_id }))),
      o.cancelled_at ? row("orders.kv.cancelledAt", format.time(o.cancelled_at)) : ""));
  }

  function renderItems(o) {
    rows(itemsBody, o.items, (it) => h("tr", { "data-sku": it.sku_code },
      h("td", {}, it.product_title),
      h("td", { class: "mono" }, it.sku_code),
      h("td", { class: "money" }, format.money(it.price_amount, it.currency)),
      h("td", {}, it.quantity),
      // Both numbers come from the server; a quantity is never money.
      h("td", { class: "shipped-qty" }, `${it.shipped_quantity ?? 0} / ${it.quantity}`),
      h("td", { class: "money" }, format.money(it.line_total, it.currency)),
      h("td", { class: "gift-message" }, it.gift_message || "—")),
    { empty: t("orders.items.empty"), colspan: 7 });
  }

  async function renderPayments(o) {
    clear(payments);
    if (!ctx.can("payment.view")) {
      payments.append(h("p", { class: "hint" }, t("orders.payments.noPermission")));
      return;
    }
    let listing = [];
    try {
      listing = await ctx.data.orderPayments(o.id);
    } catch (e) {
      payments.append(h("p", { class: "error" }, ctx.errorMessage(e)));
      return;
    }
    const body = h("tbody", { id: "order-payments-tbody" });
    payments.append(h("table", { class: "table" },
      h("thead", {}, h("tr", {},
        h("th", {}, t("orders.payments.id")), h("th", {}, t("orders.payments.provider")),
        h("th", {}, t("orders.payments.reference")), h("th", {}, t("orders.payments.amount")),
        h("th", {}, t("orders.payments.status")), h("th", {}, t("orders.payments.time")))),
      body));
    rows(body, listing, (p) => h("tr", {},
      h("td", {}, p.id),
      h("td", {}, p.provider),
      h("td", { class: "mono" }, p.provider_reference),
      h("td", { class: "money" }, format.money(p.amount, p.currency)),
      h("td", {}, badge(ctx, enums.paymentRecordStatus, p.status, [1])),
      h("td", {}, format.time(p.created_at))),
    { empty: t("orders.payments.empty"), colspan: 6 });
    payments.append(h("p", { class: "hint" }, t("orders.payments.hint")));
  }

  async function loadDetail() {
    err.hidden = true;
    title.textContent = t("orders.detail.title", { id: orderID });
    let order;
    try {
      order = await ctx.data.orderDetail(orderID);
    } catch (e) {
      if (!ctx.alive()) return;
      showError(ctx.errorMessage(e));
      clear(summary); clear(itemsBody); clear(shipment); clear(payments);
      return;
    }
    if (!ctx.alive()) return;
    current = order;
    renderSummary(order);
    renderItems(order);
    await renderOrderShipments(ctx, shipment, order, { reload: loadDetail, showError });
    await renderPayments(order);
    cancelBtn.hidden = !ctx.logic.canCancelOrder(order);
    pointsBtn.hidden = !ctx.can("point.view");
  }

  back.addEventListener("click", () => ctx.navigate("orders"));
  pointsBtn.addEventListener("click", () => { if (current) ctx.navigate("points", current.member_id); });
  cancelBtn.addEventListener("click", async () => {
    if (!current) return;
    err.hidden = true;
    try {
      const r = await ctx.actions.cancelOrder(current.id);
      if (r && r.cancelled) return;
      await loadDetail();
    } catch (e) {
      // 409 messages are written for people: shown as-is.
      showError(ctx.errorMessage(e));
    }
  });

  await loadDetail();
}
