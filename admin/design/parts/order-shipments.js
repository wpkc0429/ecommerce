// The "shipments" section of the order detail: parcels (per shipment) and the create-shipment form.
// Quantity validation and the confirmations belong to the core actions; this only draws and collects.
import { badge, kv } from "./orders-shared.js";

export async function renderOrderShipments(ctx, host, order, deps) {
  const { h, clear } = ctx.ui;
  const { t } = ctx;
  clear(host);
  if (!ctx.can("shipment.view") && !ctx.can("shipment.create")) {
    host.append(h("p", { class: "hint" }, t("orders.shipment.noPermission")));
    return;
  }
  let shipments = [];
  if (ctx.can("shipment.view")) {
    try {
      shipments = await ctx.data.orderShipments(order.id);
    } catch (e) {
      host.append(h("p", { class: "error" }, ctx.errorMessage(e)));
      return;
    }
  }
  // shipment_items carry only order_item_id: names and SKUs come from the order's own items.
  const byID = new Map((order.items || []).map((it) => [it.id, it]));
  renderParcels(ctx, host, order, shipments, deps, byID);
  renderCreateForm(ctx, host, order, shipments, deps);
}

function renderParcels(ctx, host, order, shipments, deps, byID) {
  const { h } = ctx.ui;
  const list = h("div", { id: "order-shipments-list" });
  host.append(list);
  if (shipments.length === 0) {
    list.append(h("p", { class: "hint", id: "order-shipments-empty" }, ctx.t("orders.shipment.none")));
    return;
  }
  list.append(h("p", { class: "hint" }, ctx.t("orders.shipment.count", { count: shipments.length })));
  shipments.forEach((sh, i) => list.append(parcelCard(ctx, order, sh, deps, byID, i === 0)));
}

function parcelCard(ctx, order, sh, deps, byID, primary) {
  const { h } = ctx.ui;
  const { t } = ctx;
  const codes = ctx.enums.shipmentCodes;

  const contents = h("ul", { class: "parcel-contents" });
  const items = sh.items || [];
  if (items.length === 0) contents.append(h("li", { class: "empty" }, t("orders.shipment.noLines")));
  for (const si of items) {
    const line = byID.get(si.order_item_id);
    contents.append(h("li", { class: "parcel-line" },
      h("span", { class: "parcel-line-title" }, line ? line.product_title : t("orders.shipment.unknownLine", { id: si.order_item_id })),
      h("span", { class: "mono" }, line ? line.sku_code : ""),
      h("span", { class: "parcel-line-qty" }, t("orders.shipment.qty", { quantity: si.quantity }))));
  }

  const card = h("div", { class: "parcel", "data-shipment-id": String(sh.id) },
    h("div", primary ? { class: "detail-grid", id: "shipment-detail" } : { class: "detail-grid" },
      kv(ctx, t("orders.shipment.kvId"), `#${sh.id}`),
      kv(ctx, t("orders.shipment.kvStatus"), badge(ctx, ctx.enums.shipmentStatus, sh.status, [1, 2])),
      kv(ctx, t("orders.shipment.kvCarrier"), sh.carrier),
      kv(ctx, t("orders.shipment.kvTracking"), h("span", { class: "mono" }, sh.tracking_number || "—")),
      kv(ctx, t("orders.shipment.kvShippedAt"), ctx.format.time(sh.shipped_at)),
      kv(ctx, t("orders.shipment.kvDeliveredAt"), ctx.format.time(sh.delivered_at))),
    h("h4", {}, t("orders.shipment.contents")),
    contents);

  if (sh.status !== codes.shipped) {
    card.append(h("p", primary ? { class: "hint", id: "shipment-terminal-note" } : { class: "hint" }, t("orders.shipment.terminal")));
    return card;
  }
  if (!ctx.can("shipment.update")) return card;
  card.append(h("div", { class: "toolbar-actions" },
    h("button", {
      ...(primary ? { id: "shipment-delivered-btn" } : {}),
      class: "primary shipment-delivered", onclick: () => advance(ctx, order, sh, "delivered", deps),
    }, t("orders.shipment.delivered")),
    ctx.can("shipment.return") && h("button", {
      ...(primary ? { id: "shipment-returned-btn" } : {}),
      class: "danger shipment-returned", onclick: () => advance(ctx, order, sh, "returned", deps),
    }, t("orders.shipment.returned"))));
  return card;
}

async function advance(ctx, order, sh, target, deps) {
  try {
    const r = await ctx.actions.advanceShipment(order.id, sh.id, target);
    if (r && r.cancelled) return;
    await deps.reload();
  } catch (e) {
    deps.showError(ctx.errorMessage(e));
  }
}

function renderCreateForm(ctx, host, order, shipments, deps) {
  const { h, repeater } = ctx.ui;
  const { t } = ctx;
  if (order.status === 1) {
    host.append(h("p", { class: "hint" }, t("orders.shipment.cancelledOrder")));
    return;
  }
  if (ctx.logic.hasReturnedShipment(shipments)) {
    host.append(h("p", { class: "hint", id: "order-shipment-returned-note" }, t("orders.shipment.returnedNote")));
    return;
  }
  const outstanding = ctx.logic.outstandingItems(order);
  if (outstanding.length === 0) {
    host.append(h("p", { class: "hint", id: "order-shipment-complete-note" }, t("orders.shipment.completeNote")));
    return;
  }
  if (!ctx.can("shipment.create")) {
    host.append(h("p", { class: "hint" }, t("orders.shipment.noCreatePermission")));
    return;
  }

  const carrier = h("input", { type: "text", id: "shipment-carrier", placeholder: t("orders.shipment.carrierPlaceholder") });
  const tracking = h("input", { type: "text", id: "shipment-tracking", placeholder: t("orders.shipment.trackingPlaceholder") });
  const lines = h("div", { class: "shipment-lines", id: "shipment-lines" });

  // Each row's read() closes over its own qty input: no DOM queries when collecting.
  const rep = repeater(lines, (it) => {
    const remaining = ctx.logic.shipmentRemaining(it);
    const qty = h("input", {
      type: "number", min: "0", max: String(remaining), value: String(remaining),
      class: "shipment-line-qty", "data-order-item-id": String(it.id),
    });
    return {
      node: h("div", { class: "shipment-line" },
        h("span", { class: "shipment-line-title" }, it.product_title),
        h("span", { class: "mono" }, it.sku_code),
        h("span", { class: "shipment-line-remaining" }, t("orders.shipment.remaining", { remaining, quantity: it.quantity })),
        h("label", { class: "field inline" },
          h("span", { class: "field-label" }, t("orders.shipment.thisTime")), qty)),
      read: () => ({ order_item_id: it.id, quantity: Number(qty.value) }),
    };
  });
  rep.reset(outstanding);

  async function send(lineList) {
    try {
      await ctx.actions.createShipment(order.id, { carrier: carrier.value, tracking: tracking.value, lines: lineList });
      await deps.reload();
    } catch (e) {
      deps.showError(ctx.errorMessage(e));
    }
  }

  host.append(h("div", { class: "shipment-form" },
    h("label", { class: "field" }, h("span", { class: "field-label" }, t("orders.shipment.carrierLabel")), carrier),
    h("label", { class: "field" }, h("span", { class: "field-label" }, t("orders.shipment.trackingLabel")), tracking),
    h("h4", {}, t("orders.shipment.linesHeading")),
    lines,
    h("div", { class: "toolbar-actions" },
      h("button", { id: "create-shipment-btn", class: "primary", onclick: () => send(null) }, t("orders.shipment.shipAll")),
      h("button", { id: "create-partial-shipment-btn", onclick: () => send(rep.read()) }, t("orders.shipment.shipPartial"))),
    h("p", { class: "hint" }, t("orders.shipment.hint"))));
}
