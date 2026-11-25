// Core group "platform" (CORE, locked): the platform operator screens (shops, accounts, shop applications).
// Everything here is platform-level (platformPerm, GET /admin/me platform_permissions), independent of the selected shop.
// One-time setup/reset links are only RETURNED to the caller and drawn by ui.platformLinkOnce; they are never stored.
import { api } from "../../lib/api.js";
import { canPlatform } from "../../lib/rbac.js";
import { emitBadge } from "../../lib/session.js";
import { needPlatform } from "../guard.js";
import { invalid, blocked } from "../errors.js";
import { fixedWarning } from "../warnings.js";
import { confirmDialog } from "../../ui/kit.js";
import { t } from "../../lib/design.js";
import { showLinkOnce } from "../platform/linkonce.js";

const PAGE_SIZE = 20;
const SETUP_CHECKS = ["owner_assigned", "owner_password_set", "home_published", "shipping_method_active", "payment_credentials_set", "publishable_key_active"];
const SHOP_STATUSES = [0, 1, 2];
const APP_STATUS = {
  pending_verification: "warn", pending_review: "warn", approved: "ok", rejected: "", expired: "", withdrawn: "",
};
const APP_FLAGS = ["filled_too_fast", "email_has_account", "manually_verified"];
const APP_EVENTS = ["submitted", "verification_sent", "verified", "verified_manually", "approved", "approve_failed", "rejected", "message_marked_sent", "expired", "purged"];
const MSG_STATUSES = ["pending_manual", "queued", "sent", "failed"];

const pageOf = (n) => Math.max(1, Math.floor(Number(n)) || 1);
const idOf = (v) => {
  const n = Number(v);
  if (!Number.isInteger(n) || n < 1) throw invalid({ "/id": t("err.invalid") });
  return n;
};

let merchantRolesCache = null;

/** Asks the core-owned confirmation; true when the person agreed. */
const ask = (id, params) => confirmDialog({
  title: t(`confirm.${id}.title`),
  message: t(`confirm.${id}.text`, params),
  warning: fixedWarning(id, params),
  confirmText: t(`confirm.${id}.yes`),
  cancelText: t("common.cancel"),
  danger: true,
});

const getAccount = (id) => api("GET", `/admin/accounts/${id}`);
const getApplication = (id) => api("GET", `/admin/shop-applications/${id}`);

const group = {
  data: {
    platformShopsList: async ({ page = 1 } = {}) => {
      needPlatform("shop.list");
      const d = await api("GET", `/admin/shops?page=${pageOf(page)}&page_size=${PAGE_SIZE}`);
      return { shops: d.shops, page: d.page, total: d.total, pageSize: PAGE_SIZE };
    },
    platformShopSetupStatus: async (shopID) => {
      needPlatform("shop.view");
      return api("GET", `/admin/shops/${idOf(shopID)}/setup-status`);
    },
    platformAccountsList: async ({ page = 1, q = "" } = {}) => {
      needPlatform("account.view");
      const qs = new URLSearchParams({ page: pageOf(page), page_size: PAGE_SIZE });
      if (q) qs.set("q", String(q));
      const d = await api("GET", `/admin/accounts?${qs}`);
      return { accounts: d.accounts, page: d.page, total: d.total, pageSize: PAGE_SIZE };
    },
    platformAccount: async (id) => {
      needPlatform("account.view");
      return getAccount(idOf(id));
    },
    /** Merchant-scope roles for the "add to shop" pickers (cached like the old view did; [] when the list cannot be read). */
    platformMerchantRoles: async () => {
      needPlatform("account.manage");
      if (merchantRolesCache) return merchantRolesCache;
      try {
        const d = await api("GET", "/admin/roles");
        merchantRolesCache = (d.roles || []).filter((r) => r.scope === "merchant");
      } catch {
        merchantRolesCache = [];
      }
      return merchantRolesCache;
    },
    platformApplicationsList: async ({ page = 1, status = "", q = "" } = {}) => {
      needPlatform("shop_application.view");
      const qs = new URLSearchParams({ page: pageOf(page), page_size: PAGE_SIZE });
      if (status) qs.set("status", String(status));
      if (q) qs.set("q", String(q));
      const d = await api("GET", `/admin/shop-applications?${qs}`);
      return { ...d, page: pageOf(page), pageSize: PAGE_SIZE };
    },
    platformApplication: async (id) => {
      needPlatform("shop_application.view");
      return getApplication(idOf(id));
    },
    /** Nav badge: pending-review applications + messages waiting for manual sending. null when not allowed / not readable. */
    applicationsBadge: async () => {
      if (!canPlatform("shop_application.view")) return null;
      try {
        const d = await api("GET", "/admin/shop-applications?page_size=1");
        return { count: (d.counts.pending_review || 0) + (d.pending_messages || 0) };
      } catch {
        return null; // the badge is only a convenience
      }
    },
  },

  actions: {
    platformProvisionShop: async ({ name, origin = "", ownerEmail }) => {
      needPlatform("shop.create");
      const body = { name: String(name ?? "").trim(), owner_email: String(ownerEmail ?? "").trim() };
      const fields = {};
      if (!body.name) fields["/name"] = t("platform.err.shopNameRequired");
      if (!body.owner_email) fields["/owner_email"] = t("platform.err.emailRequired");
      if (Object.keys(fields).length) throw invalid(fields);
      const o = String(origin ?? "").trim();
      if (o) body.storefront_origins = [o];
      return api("POST", "/admin/shop-provisioning", body);
    },
    platformCreateAccount: async ({ email, shopId = "", roleId = "" }) => {
      needPlatform("account.manage");
      const body = { email: String(email ?? "").trim() };
      if (!body.email) throw invalid({ "/email": t("platform.err.emailRequired") });
      if (shopId || roleId) {
        body.shop_id = Number(shopId);
        body.role_id = Number(roleId);
      }
      return api("POST", "/admin/accounts", body);
    },
    platformDisableAccount: async (id) => {
      needPlatform("account.manage");
      const aid = idOf(id);
      const acct = await getAccount(aid);
      if (!(await ask("disableAccount", { email: acct.email }))) return { cancelled: true };
      await api("POST", `/admin/accounts/${aid}/disable`);
      return { ok: true };
    },
    platformEnableAccount: async (id) => {
      needPlatform("account.manage");
      await api("POST", `/admin/accounts/${idOf(id)}/enable`);
      return { ok: true };
    },
    /** Returns {email, link}; the link is shown once by ui.platformLinkOnce and never kept. */
    platformResetAccountPassword: async (id) => {
      needPlatform("account.manage");
      const aid = idOf(id);
      const acct = await getAccount(aid);
      const link = await api("POST", `/admin/accounts/${aid}/password-reset`);
      return { email: acct.email, link };
    },
    platformAddMembership: async ({ accountId, shopId, roleId }) => {
      needPlatform("account.manage");
      await api("POST", `/admin/accounts/${idOf(accountId)}/memberships`, { shop_id: Number(shopId), role_id: Number(roleId) });
      return { ok: true };
    },
    platformRemoveMembership: async ({ accountId, shopId }) => {
      needPlatform("account.manage");
      const aid = idOf(accountId);
      const sid = idOf(shopId);
      const acct = await getAccount(aid);
      if (!(await ask("removeMembership", { email: acct.email, shopId: sid }))) return { cancelled: true };
      await api("DELETE", `/admin/accounts/${aid}/memberships/${sid}`);
      return { ok: true };
    },
    platformVerifyApplicationManually: async (id) => {
      needPlatform("shop_application.review");
      const aid = idOf(id);
      const d = await getApplication(aid);
      if (d.status !== "pending_verification") throw blocked("not-pending-verification", t("platform.err.notPendingVerification"));
      if (!(await ask("confirmApplicantContact", {}))) return { cancelled: true };
      await api("POST", `/admin/shop-applications/${aid}/verify-manually`);
      return { ok: true };
    },
    platformMarkMessageSent: async ({ applicationId, messageId }) => {
      needPlatform("shop_application.review");
      await api("POST", `/admin/shop-applications/${idOf(applicationId)}/messages/${idOf(messageId)}/mark-sent`);
      return { ok: true };
    },
    /** Approve = open the shop + owner in one server transaction. Returns the server result (owner link inside, show once). */
    platformApproveApplication: async ({ id, shopName, status = 2, confirmExisting = false }) => {
      needPlatform("shop_application.review");
      const aid = idOf(id);
      const d = await getApplication(aid);
      if (d.status !== "pending_review" || !d.email_verified) throw blocked("not-approvable", t("platform.err.notApprovable"));
      const name = String(shopName ?? "").trim();
      if (!name) throw invalid({ "/shop_name": t("platform.err.shopNameRequired") });
      const st = Number(status);
      if (!SHOP_STATUSES.includes(st)) throw invalid({ "/status": t("err.invalid") });
      // Anti-takeover: an existing back-office account may only become owner of the new shop with an explicit confirmation.
      if (d.email_has_account && confirmExisting !== true) throw invalid({ "/confirm_existing_account": t("platform.err.confirmExisting") });
      return api("POST", `/admin/shop-applications/${aid}/approve`, {
        shop_name: name,
        status: st,
        confirm_existing_account: !!d.email_has_account,
      });
    },
    platformRejectApplication: async ({ id, reason, note = "" }) => {
      needPlatform("shop_application.review");
      const aid = idOf(id);
      const decision = String(reason ?? "").trim();
      if (!decision) throw invalid({ "/decision_reason": t("platform.err.reasonRequired") });
      const d = await getApplication(aid);
      if (d.status !== "pending_verification" && d.status !== "pending_review") throw blocked("not-open", t("platform.err.notOpen"));
      await api("POST", `/admin/shop-applications/${aid}/reject`, { decision_reason: decision, internal_note: String(note ?? "").trim() });
      return { ok: true };
    },
  },

  logic: {},
  enums: { platformSetupChecks: Object.freeze([...SETUP_CHECKS]), platformShopStatuses: Object.freeze([...SHOP_STATUSES]) },

  ui: { platformLinkOnce: showLinkOnce },

  format: {
    platformErrorText: (err) => {
      const details = err && Array.isArray(err.details) ? err.details : [];
      return (err && err.message ? err.message : t("err.generic")) +
        (details.length ? `（${details.map((d) => `${d.pointer} ${d.message}`).join("；")}）` : "");
    },
    platformShopStatus: (s) => (SHOP_STATUSES.includes(s) ? t(`platform.shopStatus.${s}`) : s),
    platformApplicationStatus: (s) => (s in APP_STATUS ? { label: t(`platform.appStatus.${s}`), cls: APP_STATUS[s] } : { label: s, cls: "" }),
    platformApplicationFlags: (flags) => Object.keys(flags || {}).filter((k) => flags[k]).map((k) => (APP_FLAGS.includes(k) ? t(`platform.flag.${k}`) : k)).join("、"),
    platformApplicationEvent: (e) => (APP_EVENTS.includes(e) ? t(`platform.event.${e}`) : e),
    platformMessageStatus: (s) => (MSG_STATUSES.includes(s) ? t(`platform.msgStatus.${s}`) : s),
  },
};

// The nav badge (pending applications + messages to send by hand) is refreshed by the shell after every list load and
// every write that changes it (the pre-split admin refreshed it after each applications list load).
const REFRESH = { data: ["platformApplicationsList"], actions: ["platformVerifyApplicationManually", "platformMarkMessageSent", "platformApproveApplication", "platformRejectApplication"] };
for (const [kind, names] of Object.entries(REFRESH)) {
  for (const n of names) {
    const fn = group[kind][n];
    group[kind][n] = async (...a) => { const r = await fn(...a); emitBadge(); return r; };
  }
}
export default group;
