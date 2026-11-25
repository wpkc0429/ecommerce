// Member points (CORE): balance, tier, ledger and the manual adjustment. points / points_delta are POINTS, never money.
import { api } from "../../lib/api.js";
import { need } from "../guard.js";
import { invalid } from "../errors.js";
import { t } from "../../lib/design.js";
import { shopPath, memberNotFound } from "./shared.js";

const PAGE_SIZE = 20;
const memberPath = (memberId, suffix) => shopPath(`/members/${memberId}${suffix}`);

export const data = {
  /** {memberId, points, level} (level is null or {name}). */
  async pointsBalance(memberId) {
    need("point.view");
    try {
      const res = await api("GET", memberPath(memberId, "/points"));
      return { memberId: res.member_id, points: res.points ?? 0, level: res.level || null };
    } catch (e) { throw memberNotFound(e, memberId); }
  },

  /** {transactions, total, page, pageSize} */
  async pointsLedger(memberId, page = 1) {
    need("point.view");
    const res = await api("GET", memberPath(memberId, `/points/transactions?page=${page}&page_size=${PAGE_SIZE}`));
    return { transactions: res.transactions || [], total: res.total ?? 0, page: res.page ?? 1, pageSize: PAGE_SIZE };
  },
};

export const actions = {
  /** delta and reason are the raw form values. The ledger is an audit trail: a non-zero integer and a reason are mandatory. */
  async adjustMemberPoints({ memberId, delta, reason }) {
    need("point.adjust");
    const n = parseInt(delta, 10);
    const why = String(reason ?? "").trim();
    if (!Number.isFinite(n) || n === 0) { const m = t("points.err.delta"); throw invalid({ "/points_delta": m }, m); }
    if (why === "") { const m = t("points.err.reason"); throw invalid({ "/reason": m }, m); }
    await api("POST", memberPath(memberId, "/points/adjust"), { points_delta: n, reason: why });
    return { ok: true };
  },
};
