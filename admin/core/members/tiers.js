// Member tiers (CORE). min_points is POINTS and discount_percent is a PERCENT: neither ever passes through money formatting.
// discount_percent is applied at checkout (floor(line subtotal * percent / 100), goods only, snapshotted on the order).
import { api } from "../../lib/api.js";
import { need } from "../guard.js";
import { confirmDialog } from "../../ui/kit.js";
import { fixedWarning } from "../warnings.js";
import { t } from "../../lib/design.js";
import { shopPath, withDetails } from "./shared.js";

export const data = {
  /** The ladder, lowest threshold first (a tier is "the highest threshold not above the balance"). */
  async memberTiers() {
    need("member_tier.view");
    const res = await api("GET", shopPath("/member-tiers"));
    const list = res.member_tiers || [];
    return [...list].sort((a, b) => a.min_points - b.min_points);
  },
};

export const actions = {
  /** id present = edit, otherwise create. Numbers are read leniently like the old form (blank/invalid = 0). */
  async saveMemberTier({ id, name, minPoints, discountPercent }) {
    need(id ? "member_tier.edit" : "member_tier.create");
    const body = {
      name: String(name ?? "").trim(),
      min_points: parseInt(minPoints, 10) || 0,
      discount_percent: parseInt(discountPercent, 10) || 0,
    };
    try {
      if (id) await api("PUT", shopPath(`/member-tiers/${id}`), body);
      else await api("POST", shopPath("/member-tiers"), body);
    } catch (e) { throw withDetails(e); }
    return { ok: true };
  },

  async deleteMemberTier({ id, name }) {
    need("member_tier.delete");
    const ok = await confirmDialog({
      title: t("confirm.deleteMemberTier.title"),
      message: t("confirm.deleteMemberTier.text", { name }),
      warning: fixedWarning("deleteMemberTier", { name }),
      confirmText: t("confirm.deleteMemberTier.yes"),
      cancelText: t("common.cancel"),
      danger: true,
    });
    if (!ok) return { cancelled: true };
    await api("DELETE", shopPath(`/member-tiers/${id}`));
    return { ok: true };
  },
};
