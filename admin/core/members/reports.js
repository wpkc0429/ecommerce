// Operations reports, shop timezone and the admin-branding form (CORE). The period is sent to the server (the response is
// aggregated, there is nothing to filter client-side). Currency is the outermost dimension: nothing here ever adds two currencies.
import { api } from "../../lib/api.js";
import { need } from "../guard.js";
import { ActionError } from "../errors.js";
import { confirmDialog, applyBranding } from "../../ui/kit.js";
import { fixedWarning } from "../warnings.js";
import { t } from "../../lib/design.js";
import { shopPath } from "./shared.js";

const counts = new Intl.NumberFormat("zh-Hant-TW");
const isoDay = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;

/** The four classes of orders created in the period add up to ALL orders of the period: that is how a merchant verifies the report. */
function reconcile(s) {
  return {
    counted: s.order_count, returned: s.returned_order_count, returnedAmount: s.returned_total_amount,
    cancelled: s.cancelled_order_count, unpaid: s.unpaid_order_count,
    total: Number(s.order_count) + Number(s.returned_order_count) + Number(s.cancelled_order_count) + Number(s.unpaid_order_count),
  };
}

export const data = {
  /**
   * {timezone: {name, source}, currencies: [{currency, summary, buckets, products: [items], reconciliation}]}.
   * groupBy: "day" | "month". Products are matched to the sales block of the same currency.
   */
  async reportLoad({ from, to, groupBy }) {
    need("report.view");
    const params = new URLSearchParams({ from, to });
    params.set("group_by", groupBy);
    const sales = await api("GET", shopPath(`/reports/sales?${params.toString()}`));
    const products = await api("GET", shopPath(`/reports/products?${new URLSearchParams({ from, to }).toString()}`));
    const byCurrency = {};
    for (const c of products.currencies ?? []) byCurrency[c.currency] = c;
    return {
      timezone: sales.timezone,
      currencies: (sales.currencies ?? []).map((c) => ({
        currency: c.currency,
        summary: c.summary,
        buckets: c.buckets,
        products: byCurrency[c.currency]?.items ?? [],
        reconciliation: reconcile(c.summary),
      })),
    };
  },

  /** The admin branding of the current shop: {logo_url, primary_color, accent_color}. */
  async adminBranding() {
    need("shop.update");
    return (await api("GET", shopPath("/branding"))).branding;
  },
};

export const actions = {
  /** value: a timezone name, or null to clear the shop's own setting. Asks its own confirmation. */
  async setReportTimezone(value) {
    need("shop.update");
    const v = value === null ? null : String(value).trim();
    if (v === "") throw new ActionError("invalid", t("err.invalid"));
    const label = v === null ? t("reports.timezone.labelClear") : t("reports.timezone.labelSet", { value: v });
    const ok = await confirmDialog({
      title: t("confirm.changeTimezone.title"),
      message: t("confirm.changeTimezone.text", { label }),
      warning: fixedWarning("changeTimezone", { label }),
      confirmText: t("confirm.changeTimezone.yes"),
      cancelText: t("common.cancel"),
      danger: true,
    });
    if (!ok) return { cancelled: true };
    await api("PUT", shopPath("/settings"), { timezone: v });
    return { ok: true };
  },

  /** Saves ONLY admin_branding (the timezone is untouched) and applies the saved colors / logo to this admin. */
  async saveAdminBranding({ logoUrl, primaryColor, accentColor }) {
    need("shop.update");
    const admin_branding = {
      logo_url: String(logoUrl ?? "").trim(),
      primary_color: String(primaryColor ?? "").trim(),
      accent_color: String(accentColor ?? "").trim(),
    };
    try {
      const res = await api("PUT", shopPath("/settings"), { admin_branding });
      applyBranding(res.admin_branding);
    } catch (err) {
      if (err && typeof err.status === "number" && err.details?.[0]?.message) {
        throw new ActionError("invalid", `${err.message}：${err.details[0].message}`);
      }
      throw err;
    }
    return { ok: true };
  },
};

export const logic = {
  /** The default report period: the 1st of this month through today (local days). */
  reportDefaultPeriod() {
    const now = new Date();
    return { from: isoDay(new Date(now.getFullYear(), now.getMonth(), 1)), to: isoDay(now) };
  },
};

export const format = {
  /** A count of orders / pieces: NOT money (no currency sign, never formatMoney). */
  reportCount(n, unit) {
    const v = Number(n);
    return Number.isFinite(v) ? `${counts.format(v)} ${unit}` : "—";
  },
};
