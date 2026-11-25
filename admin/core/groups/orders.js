// Core group "orders" (CORE, locked): order list/detail, cancel, per-item shipments, shipping methods.
// Shop binding, URLs, permission checks, quantity validation and the confirmations of irreversible actions live here;
// the design layer only draws what these functions return.
import { api } from "../../lib/api.js";
import { session } from "../../lib/session.js";
import { can } from "../../lib/rbac.js";
import { t } from "../../lib/design.js";
import { confirmDialog } from "../../ui/kit.js";
import { need } from "../guard.js";
import { invalid } from "../errors.js";
import { fixedWarning } from "../warnings.js";

const PAGE_SIZE = 20;
const SHIPPED = 1;
const DELIVERED = 2;
const RETURNED = 3;

const shopPath = (suffix) => `/admin/shops/${session.shopID}${suffix}`;
const orderPath = (id) => shopPath(`/orders/${encodeURIComponent(String(id))}`);

/** Pure: how many units of an order line are still unshipped (the only arithmetic the old UI did). */
const remainingOf = (it) => Number(it.quantity) - Number(it.shipped_quantity ?? 0);

const FILTER_KEYS = ["status", "payment_status", "fulfillment_status", "delivery_from", "delivery_to"];

const data = {
  /** filters: {status, payment_status, fulfillment_status, delivery_from, delivery_to, q}; all optional strings. */
  async ordersList({ page = 1, filters = {} } = {}) {
    need("order.view");
    const params = new URLSearchParams({ page: String(page), page_size: String(PAGE_SIZE) });
    for (const k of FILTER_KEYS) {
      const v = filters[k];
      if (v !== undefined && v !== null && v !== "") params.set(k, String(v));
    }
    const q = String(filters.q ?? "").trim();
    if (q) params.set("q", q);
    const res = await api("GET", shopPath(`/orders?${params.toString()}`));
    return { orders: res.orders || [], total: res.total ?? 0, page: res.page ?? 1, pageSize: PAGE_SIZE };
  },
  async orderDetail(id) {
    need("order.view");
    return api("GET", orderPath(id));
  },
  /** Empty list without shipment.view (the view then only offers the create form). */
  async orderShipments(id) {
    if (!can("shipment.view")) return [];
    const res = await api("GET", `${orderPath(id)}/shipments`);
    return res.shipments || [];
  },
  async orderPayments(id) {
    need("payment.view");
    const res = await api("GET", `${orderPath(id)}/payments`);
    return res.payments || [];
  },
  async shippingMethodsList() {
    need("shipping_method.view");
    const res = await api("GET", shopPath("/shipping-methods"));
    return res.shipping_methods || [];
  },
};

const actions = {
  async cancelOrder(id) {
    need("order.cancel");
    const ok = await confirmDialog({
      title: t("confirm.cancelOrder.title"),
      message: t("confirm.cancelOrder.text", { id }),
      warning: fixedWarning("cancelOrder", { id }),
      confirmText: t("confirm.cancelOrder.yes"),
      cancelText: t("common.cancel"),
      danger: true,
    });
    if (!ok) return { cancelled: true };
    await api("POST", `${orderPath(id)}/cancel`);
    return { ok: true };
  },

  /**
   * lines: null = ship every outstanding item (no `items` in the request, decided by the server);
   * otherwise [{order_item_id, quantity}] checked against the order as it is NOW.
   */
  async createShipment(orderID, { carrier = "", tracking = "", lines = null } = {}) {
    need("shipment.create");
    const body = { carrier: String(carrier).trim() };
    const trk = String(tracking).trim();
    if (trk) body.tracking_number = trk;
    if (lines !== null) {
      const order = await api("GET", orderPath(orderID));
      const byID = new Map((order.items || []).map((it) => [it.id, it]));
      const checked = (Array.isArray(lines) ? lines : []).map((l) => {
        const it = byID.get(l.order_item_id);
        return {
          order_item_id: l.order_item_id,
          quantity: Number(l.quantity),
          remaining: it ? remainingOf(it) : 0,
          title: it ? it.product_title : t("orders.shipment.unknownLine", { id: l.order_item_id }),
        };
      });
      // `max` on a number input is advisory; the server's conditional UPDATE is the final arbiter, this names the row.
      const over = checked.find((l) => l.quantity > l.remaining);
      if (over) throw invalid({}, t("orders.shipment.err.over", { title: over.title, remaining: over.remaining, quantity: over.quantity }));
      const picked = checked.filter((l) => l.quantity > 0).map((l) => ({ order_item_id: l.order_item_id, quantity: l.quantity }));
      if (picked.length === 0) throw invalid({}, t("orders.shipment.err.none"));
      body.items = picked;
    }
    await api("POST", `${orderPath(orderID)}/shipments`, body);
    return { ok: true };
  },

  /** target: "delivered" | "returned". Both are terminal; the confirmation is asked here. */
  async advanceShipment(orderID, shipmentID, target) {
    if (target !== "delivered" && target !== "returned") throw invalid({}, t("err.invalid"));
    need("shipment.update");
    if (target === "returned") need("shipment.return");
    const id = shipmentID;
    const cid = target === "returned" ? "shipReturned" : "shipDelivered";
    const ok = await confirmDialog({
      title: t(`confirm.${cid}.title`),
      message: t(`confirm.${cid}.text`, { id }),
      warning: fixedWarning(cid, { id }),
      confirmText: t(`confirm.${cid}.yes`),
      cancelText: t("common.cancel"),
      danger: true,
    });
    if (!ok) return { cancelled: true };
    await api("PUT", `${orderPath(orderID)}/shipments/${encodeURIComponent(String(shipmentID))}`, { status: target });
    return { ok: true };
  },

  /** id null = create. input: {name, carrier, flat_rate (integer, never scaled), is_active}. */
  async saveShippingMethod(id, { name, carrier, flat_rate, is_active }) {
    need(id ? "shipping_method.edit" : "shipping_method.create");
    const body = { name: String(name ?? "").trim(), carrier: String(carrier ?? "").trim(), flat_rate, is_active: Boolean(is_active) };
    if (id) await api("PUT", shopPath(`/shipping-methods/${encodeURIComponent(String(id))}`), body);
    else await api("POST", shopPath("/shipping-methods"), body);
    return { ok: true };
  },

  async deleteShippingMethod(id, name) {
    need("shipping_method.delete");
    const ok = await confirmDialog({
      title: t("confirm.deleteShippingMethod.title"),
      message: t("confirm.deleteShippingMethod.text", { name }),
      warning: fixedWarning("deleteShippingMethod", { name }),
      confirmText: t("confirm.deleteShippingMethod.yes"),
      cancelText: t("common.cancel"),
      danger: true,
    });
    if (!ok) return { cancelled: true };
    await api("DELETE", shopPath(`/shipping-methods/${encodeURIComponent(String(id))}`));
    return { ok: true };
  },
};

const logic = {
  shipmentRemaining: remainingOf,
  /** Order lines that still have units to ship. */
  outstandingItems: (order) => (order.items || []).filter((it) => remainingOf(it) > 0),
  hasReturnedShipment: (shipments) => (shipments || []).some((s) => s.status === RETURNED),
  /** The cancel button shows only for a cancellable (status 0) order the person may cancel. */
  canCancelOrder: (order) => can("order.cancel") && order.status === 0,
};

// value -> copy key; unknown values are shown as "#value" by the view.
const enums = {
  orderStatus: { 0: "orders.orderStatus.0", 1: "orders.orderStatus.1" },
  paymentStatus: { 0: "orders.paymentStatus.0", 1: "orders.paymentStatus.1", 2: "orders.paymentStatus.2", 3: "orders.paymentStatus.3" },
  fulfillmentStatus: {
    0: "orders.fulfillmentStatus.0", 1: "orders.fulfillmentStatus.1", 2: "orders.fulfillmentStatus.2",
    3: "orders.fulfillmentStatus.3", 4: "orders.fulfillmentStatus.4",
  },
  paymentRecordStatus: {
    0: "orders.paymentRecordStatus.0", 1: "orders.paymentRecordStatus.1",
    2: "orders.paymentRecordStatus.2", 3: "orders.paymentRecordStatus.3",
  },
  shipmentStatus: { 1: "orders.shipmentStatus.1", 2: "orders.shipmentStatus.2", 3: "orders.shipmentStatus.3" },
  shipmentCodes: { shipped: SHIPPED, delivered: DELIVERED, returned: RETURNED },
};

export default { data, actions, logic, enums, ui: {}, format: {} };
