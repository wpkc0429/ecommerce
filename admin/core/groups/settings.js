// Core group "settings" (CORE, locked): storefront clients (publishable keys, allowed origins, ECPay credentials) and team.
// Secrets are write-only: HashKey/HashIV are never read back. The one-time invite URL is returned to the caller once, never stored.
import { api, ApiError } from "../../lib/api.js";
import { session } from "../../lib/session.js";
import { need } from "../guard.js";
import { blocked } from "../errors.js";
import { fixedWarning } from "../warnings.js";
import { confirmDialog } from "../../ui/kit.js";
import { t } from "../../lib/design.js";

const shopPath = (p) => `/admin/shops/${session.shopID}${p}`;

function errorText(err) {
  if (err && err.isActionError) return err.message;
  const msg = err && err.message ? err.message : t("err.generic");
  if (err instanceof ApiError && err.details.length) {
    return msg + t("settings.err.details", { details: err.details.map((d) => `${d.pointer} ${d.message}`).join("；") });
  }
  return msg;
}

async function ask(id, params) {
  return confirmDialog({
    title: t(`confirm.${id}.title`),
    message: t(`confirm.${id}.text`, params),
    warning: fixedWarning(id, params),
    confirmText: t(`confirm.${id}.yes`),
    cancelText: t("common.cancel"),
    danger: true,
  });
}

// ── team helpers ──
async function myUserID() {
  if (session.me && session.me.id !== undefined) return session.me.id;
  return (await api("GET", "/admin/me")).id;
}
const assignableRoles = async () => ((await api("GET", shopPath("/roles"))).roles || []).filter((r) => r.merchant_assignable);
async function findMember(userID) {
  const users = (await api("GET", shopPath("/users"))).users || [];
  const u = users.find((x) => x.user_id === userID);
  if (!u) throw blocked("missing", t("settings.err.noMember"));
  return u;
}
async function guardMember(u) {
  if ((u.roles || []).some((r) => !r.merchant_assignable)) throw blocked("locked", t("settings.err.locked"));
  if (u.user_id === (await myUserID())) throw blocked("self", t("settings.err.self"));
}

const data = {
  settingsPublishableKeys: async () => {
    need("publishable_key.view");
    const d = await api("GET", shopPath("/publishable-keys"));
    return (d.keys || []).map((k) => ({ id: k.id, key: k.key, label: k.label || "", createdAt: k.created_at, revokedAt: k.revoked_at || null }));
  },
  settingsAllowedOrigins: async () => {
    need("shop.manage_client_origins");
    const d = await api("GET", shopPath("/allowed-origins"));
    return (d.origins || []).map((o) => ({ id: o.id, origin: o.origin, purpose: o.purpose === "admin" ? "admin" : "storefront", createdAt: o.created_at }));
  },
  // Booleans only: the stored HashKey/HashIV never reach the screen. status: "none" | "ok" | "partial".
  settingsEcpay: async () => {
    need("payment_credential.manage");
    const d = await api("GET", shopPath("/ecpay-credentials"));
    const status = !d.configured ? "none" : (d.hash_key_configured && d.hash_iv_configured ? "ok" : "partial");
    return { merchantId: d.merchant_id || "", choosePayment: d.choose_payment || "", status };
  },
  teamOverview: async () => {
    need("user.manage_members");
    const myID = await myUserID();
    const [roles, users, invites] = await Promise.all([
      assignableRoles(), api("GET", shopPath("/users")), api("GET", shopPath("/invitations")),
    ]);
    return {
      roles: roles.map((r) => ({ id: r.id, name: r.name })),
      members: (users.users || []).map((u) => {
        const rs = u.roles || [];
        const held = rs.find((r) => r.merchant_assignable);
        return {
          userId: u.user_id, email: u.email, roleNames: rs.map((r) => r.name),
          locked: rs.some((r) => !r.merchant_assignable), mine: u.user_id === myID,
          enabled: u.membership_status !== 0, heldRoleId: held ? held.id : null,
        };
      }),
      invitations: (invites.invitations || []).map((i) => ({ id: i.id, email: i.email, roleName: i.role_name, status: i.status, expiresAt: i.expires_at })),
    };
  },
};

const actions = {
  settingsCreatePublishableKey: async ({ label } = {}) => {
    need("publishable_key.create");
    await api("POST", shopPath("/publishable-keys"), { label: String(label ?? "") });
    return {};
  },
  settingsRevokePublishableKey: async (key) => {
    need("publishable_key.revoke");
    if (!(await ask("revokeKey", { key: key.key }))) return { cancelled: true };
    await api("DELETE", shopPath(`/publishable-keys/${key.id}`));
    return {};
  },
  settingsAddOrigin: async ({ origin, purpose }) => {
    need("shop.manage_client_origins");
    await api("POST", shopPath("/allowed-origins"), { origin: String(origin).trim(), purpose: purpose === "admin" ? "admin" : "storefront" });
    return {};
  },
  settingsRemoveOrigin: async (o) => {
    need("shop.manage_client_origins");
    if (!(await ask("removeOrigin", { origin: o.origin }))) return { cancelled: true };
    await api("DELETE", shopPath(`/allowed-origins/${o.id}`));
    return {};
  },
  settingsSaveEcpay: async ({ merchantId, hashKey, hashIV, choosePayment }) => {
    need("payment_credential.manage");
    await api("PUT", shopPath("/ecpay-credentials"), {
      merchant_id: String(merchantId).trim(), hash_key: hashKey, hash_iv: hashIV,
      choose_payment: String(choosePayment).trim() || null,
    });
    return {};
  },
  // Change role = assign the new role first, then remove the other assignable ones (never leave a member with no role).
  teamSetRole: async (userID, roleID) => {
    need("user.manage_members");
    const [u, assignable] = await Promise.all([findMember(userID), assignableRoles()]);
    await guardMember(u);
    if (!assignable.some((r) => r.id === roleID)) throw blocked("role", t("settings.err.notAssignable"));
    await api("POST", shopPath(`/users/${userID}/roles`), { role_id: roleID });
    for (const r of u.roles || []) {
      if (r.merchant_assignable && r.id !== roleID) await api("DELETE", shopPath(`/users/${userID}/roles/${r.id}`));
    }
    return {};
  },
  teamSetEnabled: async (userID, enabled) => {
    need("user.manage_members");
    const u = await findMember(userID);
    await guardMember(u);
    if (!enabled && !(await ask("disableMember", { email: u.email }))) return { cancelled: true };
    await api("POST", shopPath(`/users/${userID}/${enabled ? "enable" : "disable"}`));
    return {};
  },
  // Returns the one-time link data to the caller; nothing is stored here.
  teamInvite: async ({ email, roleID }) => {
    need("user.manage_members");
    const assignable = await assignableRoles();
    if (!assignable.some((r) => r.id === roleID)) throw blocked("role", t("settings.err.notAssignable"));
    const res = await api("POST", shopPath("/invitations"), { email: String(email).trim(), role_id: roleID });
    return { delivery: res.delivery, url: res.setup_url || res.reset_url || res.invite_url || "", expiresAt: res.expires_at };
  },
  teamRevokeInvite: async (id) => {
    need("user.manage_members");
    await api("DELETE", shopPath(`/invitations/${id}`));
    return {};
  },
};

export default {
  data, actions,
  logic: { settingsErrorText: errorText },
  enums: {},
  ui: {},
  format: {},
};
