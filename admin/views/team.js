// 商家視圖「成員」（change merchant-staff-role）。
//
// 可見性：`user.manage_members`（只有 owner）。列出本店成員、邀請、待接受邀請、
// 停用／啟用成員資格、變更角色。**體驗層，不是安全邊界**——伺服器端的守衛與
// 可委派檢查才是權威；這裡只是不畫一定會被拒絕的按鈕：
//   - 持有不可委派角色（`merchant_assignable = false`，例如 owner）的列不給任何操作；
//   - 自己的那一列不給任何操作（伺服器對自己回 409）；
//   - 邀請與變更角色的下拉只列可委派角色。
//
// 停用的是**這家店的成員資格**，不是帳號——文字必須這樣說，否則商家會以為自己
// 能把人鎖在整個平台之外。
//
// 邀請連結於 delivery = "manual" 時只顯示一次（showLinkOnce，與密碼設定連結同一個
// 元件），不寫入任何持久儲存。
import { api } from "../lib/api.js";
import { $, el, rows, clear } from "../lib/dom.js";
import { formatTime } from "../lib/format.js";
import { errText, showLinkOnce } from "./platform-accounts.js";

export function createTeamView(ctx) {
  const shopPath = (p) => `/admin/shops/${ctx.getShopID()}${p}`;
  let myID = null;

  function showError(msg) {
    const box = $("#team-error");
    box.textContent = msg || "";
    box.hidden = !msg;
  }

  async function act(fn) {
    showError("");
    try { await fn(); } catch (e) { showError(errText(e)); }
  }

  async function load() {
    clear($("#team-link")).hidden = true;
    showError("");
    try {
      if (myID === null) myID = (await api("GET", "/admin/me")).id;
      const [roles, users, invites] = await Promise.all([
        api("GET", shopPath("/roles")),
        api("GET", shopPath("/users")),
        api("GET", shopPath("/invitations")),
      ]);
      const assignable = (roles.roles || []).filter((r) => r.merchant_assignable);
      renderInviteForm(assignable);
      renderMembers(users.users || [], assignable);
      renderInvitations(invites.invitations || []);
    } catch (e) {
      showError(e.message);
    }
  }

  function roleSelect(assignable) {
    const sel = el("select", {});
    for (const r of assignable) sel.append(el("option", { value: String(r.id) }, r.name));
    return sel;
  }

  function renderInviteForm(assignable) {
    const sel = $("#team-invite-role");
    clear(sel);
    for (const r of assignable) sel.append(el("option", { value: String(r.id) }, r.name));
  }

  function renderMembers(users, assignable) {
    rows($("#team-members-tbody"), users, (u) => {
      const roles = u.roles || [];
      const locked = roles.some((r) => !r.merchant_assignable);
      const mine = u.user_id === myID;
      const enabled = u.membership_status !== 0;
      const status = enabled
        ? el("span", { class: "badge ok" }, "啟用中")
        : el("span", { class: "badge warn" }, "已停用");
      let actions = "";
      if (locked) {
        actions = el("span", { class: "hint" }, "擁有者／不可委派角色，僅平台可變更");
      } else if (mine) {
        actions = el("span", { class: "hint" }, "（你自己）");
      } else {
        const sel = roleSelect(assignable);
        const held = roles.find((r) => r.merchant_assignable);
        if (held) sel.value = String(held.id);
        actions = el("div", { class: "toolbar-actions" },
          sel,
          el("button", { "data-act": "set-role", onclick: () => act(() => setRole(u, roles, Number(sel.value))) }, "變更角色"),
          enabled
            ? el("button", { class: "danger", "data-act": "disable", onclick: () => act(() => setEnabled(u, false)) }, "停用成員資格")
            : el("button", { "data-act": "enable", onclick: () => act(() => setEnabled(u, true)) }, "啟用成員資格"));
      }
      return el("tr", { "data-email": u.email },
        el("td", {}, u.email),
        el("td", {}, roles.map((r) => r.name).join("、") || "（無角色）"),
        el("td", {}, status),
        el("td", {}, actions));
    }, { empty: "目前沒有成員。", colspan: 4 });
  }

  // 變更角色 = 先指派新角色、再移除其餘可委派角色（順序重要：中途失敗時成員至少
  // 還有一個角色，而不是變成沒有任何權限的幽靈成員）。
  async function setRole(u, roles, roleID) {
    await api("POST", shopPath(`/users/${u.user_id}/roles`), { role_id: roleID });
    for (const r of roles) {
      if (r.merchant_assignable && r.id !== roleID) {
        await api("DELETE", shopPath(`/users/${u.user_id}/roles/${r.id}`));
      }
    }
    await load();
  }

  async function setEnabled(u, enabled) {
    if (!enabled && !confirm(
      `確定停用 ${u.email} 在本店的成員資格？他在本店的下一個操作就會被拒絕。`
      + "這只影響本店：他的帳號與其他商家的權限不受影響。")) return;
    await api("POST", shopPath(`/users/${u.user_id}/${enabled ? "enable" : "disable"}`));
    await load();
  }

  function renderInvitations(list) {
    rows($("#team-invites-tbody"), list, (i) => el("tr", { "data-email": i.email },
      el("td", {}, i.email),
      el("td", {}, i.role_name),
      el("td", { "data-status": i.status }, formatTime(i.expires_at) + (i.status === "expired" ? "（已過期）" : "")),
      el("td", {}, el("button", { class: "danger", "data-act": "revoke", onclick: () => act(async () => {
        await api("DELETE", shopPath(`/invitations/${i.id}`));
        await load();
      }) }, "撤回"))),
    { empty: "沒有待接受或已過期的邀請。", colspan: 4 });
  }

  $("#team-invite-form").addEventListener("submit", (e) => {
    e.preventDefault();
    act(async () => {
      const email = $("#team-invite-email").value.trim();
      const res = await api("POST", shopPath("/invitations"), {
        email, role_id: Number($("#team-invite-role").value),
      });
      $("#team-invite-email").value = "";
      await load();
      showLinkOnce($("#team-link"), { title: `${email} 的邀請連結`, link: res });
    });
  });

  return { load };
}
