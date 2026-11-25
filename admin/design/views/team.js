// Team: members of this shop, invitations, role assignment, membership disable/enable. UI gating only (the core re-checks).
import { showLinkOnce } from "../parts/settings-link-once.js";

export default async function team(root, ctx) {
  const { h, rows, clear } = ctx.ui;
  const { t, data, actions, logic } = ctx;

  const errBox = h("p", { id: "team-error", class: "error", hidden: true });
  const linkBox = h("div", { id: "team-link", class: "card", hidden: true });
  const membersBody = h("tbody", { id: "team-members-tbody" });
  const invitesBody = h("tbody", { id: "team-invites-tbody" });
  const emailInput = h("input", { type: "email", id: "team-invite-email", required: true, autocomplete: "off" });
  const roleSel = h("select", { id: "team-invite-role" });

  const showError = (msg) => { errBox.textContent = msg || ""; errBox.hidden = !msg; };
  async function act(fn) {
    showError("");
    try { await fn(); } catch (e) { showError(logic.settingsErrorText(e)); }
  }

  async function load() {
    clear(linkBox).hidden = true;
    showError("");
    try {
      const d = await data.teamOverview();
      if (!ctx.alive()) return;
      renderInviteForm(d.roles);
      renderMembers(d.members, d.roles);
      renderInvitations(d.invitations);
    } catch (e) {
      if (ctx.alive()) showError(ctx.errorMessage(e));
    }
  }

  function roleSelect(roles) {
    return h("select", {}, ...roles.map((r) => h("option", { value: String(r.id) }, r.name)));
  }

  function renderInviteForm(roles) {
    clear(roleSel);
    for (const r of roles) roleSel.append(h("option", { value: String(r.id) }, r.name));
  }

  function renderMembers(members, roles) {
    rows(membersBody, members, (u) => {
      const status = u.enabled
        ? h("span", { class: "badge ok" }, t("team.enabled"))
        : h("span", { class: "badge warn" }, t("team.disabled"));
      let actionsCell = "";
      if (u.locked) {
        actionsCell = h("span", { class: "hint" }, t("team.lockedHint"));
      } else if (u.mine) {
        actionsCell = h("span", { class: "hint" }, t("team.selfHint"));
      } else {
        const sel = roleSelect(roles);
        if (u.heldRoleId !== null) sel.value = String(u.heldRoleId);
        actionsCell = h("div", { class: "toolbar-actions" },
          sel,
          h("button", { "data-act": "set-role", onclick: () => act(async () => { await actions.teamSetRole(u.userId, Number(sel.value)); await load(); }) }, t("team.setRole")),
          u.enabled
            ? h("button", { class: "danger", "data-act": "disable", onclick: () => act(() => setEnabled(u, false)) }, t("team.disable"))
            : h("button", { "data-act": "enable", onclick: () => act(() => setEnabled(u, true)) }, t("team.enable")));
      }
      return h("tr", { "data-email": u.email },
        h("td", {}, u.email),
        h("td", {}, u.roleNames.join(t("team.roleSeparator")) || t("team.noRoles")),
        h("td", {}, status),
        h("td", {}, actionsCell));
    }, { empty: t("team.membersEmpty"), colspan: 4 });
  }

  async function setEnabled(u, enabled) {
    const r = await actions.teamSetEnabled(u.userId, enabled);
    if (r.cancelled) return;
    await load();
  }

  function renderInvitations(list) {
    rows(invitesBody, list, (i) => h("tr", { "data-email": i.email },
      h("td", {}, i.email),
      h("td", {}, i.roleName),
      h("td", { "data-status": i.status }, ctx.format.time(i.expiresAt) + (i.status === "expired" ? t("team.expired") : "")),
      h("td", {}, h("button", { class: "danger", "data-act": "revoke", onclick: () => act(async () => {
        await actions.teamRevokeInvite(i.id);
        await load();
      }) }, t("team.revoke")))),
    { empty: t("team.invitesEmpty"), colspan: 4 });
  }

  const inviteForm = h("form", { id: "team-invite-form", class: "inline-form", onsubmit: (e) => {
    e.preventDefault();
    act(async () => {
      const email = emailInput.value.trim();
      const res = await actions.teamInvite({ email, roleID: Number(roleSel.value) });
      emailInput.value = "";
      await load();
      showLinkOnce(ctx, linkBox, { title: t("team.linkTitle", { email }), link: res });
    });
  } },
    h("label", { class: "inline" }, t("team.inviteEmail"), emailInput),
    h("label", { class: "inline" }, t("team.inviteRole"), roleSel),
    h("button", { type: "submit", class: "primary" }, t("team.inviteSubmit")));

  const th = (k) => h("th", {}, t(k));
  root.append(h("div", { id: "tab-team", class: "tab-panel" },
    h("div", { class: "toolbar" }, h("h2", {}, t("team.title"))),
    h("p", { class: "hint" }, t("team.hintPre"), h("strong", {}, t("team.hintStrong")), t("team.hintPost")),
    errBox,
    linkBox,
    h("table", { class: "table" },
      h("thead", {}, h("tr", {}, th("team.colEmail"), th("team.colRoles"), th("team.colMembership"), th("team.colActions"))),
      membersBody),
    h("h3", {}, t("team.inviteTitle")),
    inviteForm,
    h("p", { class: "hint" }, t("team.inviteHint")),
    h("h3", {}, t("team.pendingTitle")),
    h("table", { class: "table" },
      h("thead", {}, h("tr", {}, th("team.colEmail"), th("team.colRoles"), th("team.colExpires"), th("team.colActions"))),
      invitesBody)));

  await load();
}
