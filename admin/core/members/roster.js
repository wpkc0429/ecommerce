// Customer roster and detail (CORE). Points are read-only here (adjustment lives in the points screen); the search goes to the server.
import { api } from "../../lib/api.js";
import { need } from "../guard.js";
import { t } from "../../lib/design.js";
import { shopPath, memberNotFound } from "./shared.js";

const PAGE_SIZE = 20;
const RECENT_ORDERS = 10;
const KNOWN = { memberStatus: [0, 1], payment: [0, 1, 2, 3], fulfillment: [0, 1, 2, 3], pointKind: [0, 1, 2] };

export const data = {
  /** {members, total, page, pageSize}; the search term and the page number are applied by the server. */
  async memberRoster({ page = 1, q = "" } = {}) {
    need("member.view");
    const params = new URLSearchParams({ page: String(page), page_size: String(PAGE_SIZE) });
    if (q) params.set("q", q);
    const res = await api("GET", shopPath(`/members?${params}`));
    return { members: res.members || [], total: res.total ?? 0, page: res.page ?? 1, pageSize: PAGE_SIZE };
  },

  async memberDetail(id) {
    need("member.view");
    try { return await api("GET", shopPath(`/members/${id}`)); } catch (e) { throw memberNotFound(e, id); }
  },

  /** The most recent orders of one customer (the order list endpoint with member_id). */
  async memberOrders(id) {
    need("order.view");
    const res = await api("GET", shopPath(`/orders?member_id=${id}&page=1&page_size=${RECENT_ORDERS}`));
    return res.orders || [];
  },
};

export const logic = {
  /** Label of an enum value of this group; unknown values show as "#value". kind: memberStatus | payment | fulfillment | pointKind. */
  memberLabel(kind, value) {
    return (KNOWN[kind] || []).includes(value) ? t(`members.${kind}.${value}`) : `#${value}`;
  },
};
