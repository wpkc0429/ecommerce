// Platform view: accounts (list, create, disable/enable, memberships, one-time reset link).
import { pager } from "../parts/platform-common.js";

export default async function (root, ctx) {
  const { h, clear, rows } = ctx.ui;
  const t = ctx.t;
  let page = 1;
  let q = "";
  let loadSeq = 0;

  const qIn = h("input", { type: "search", id: "platform-account-q", placeholder: t("platform.accounts.searchPlaceholder") });
  const newBtn = h("button", { id: "platform-account-new-btn", class: "primary", onclick: openCreate }, t("platform.accounts.new"));
  const errBox = h("p", { id: "platform-accounts-error", class: "error", hidden: true });
  const linkHost = h("div", { id: "platform-account-link", class: "card", hidden: true });
  const tbody = h("tbody", { id: "platform-accounts-tbody" });
  const pg = pager(ctx, "platform-accounts", {
    onPrev: () => { page = Math.max(1, page - 1); loadList(); },
    onNext: () => { page += 1; loadList(); },
  });
  const detail = h("div", { id: "platform-account-detail", class: "card", hidden: true });

  const emailIn = h("input", { type: "email", id: "platform-account-email", required: true, autocomplete: "off" });
  const shopIn = h("input", { type: "number", id: "platform-account-shop", min: "1" });
  let roleSel = roleSelect([], true);
  roleSel.id = "platform-account-role";
  const dlgErr = h("p", { id: "platform-account-error", class: "error", hidden: true });
  const dialog = h("dialog", { id: "platform-account-dialog" },
    h("form", { id: "platform-account-form", method: "dialog", onsubmit: submitCreate },
      h("h3", {}, t("platform.accounts.create.title")),
      h("label", {}, t("platform.accounts.create.email"), " ", emailIn),
      h("label", {}, t("platform.accounts.create.shop"), " ", shopIn),
      h("label", {}, t("platform.accounts.create.role"), " ", roleSel),
      h("p", { class: "hint" }, t("platform.accounts.create.hint1"), h("strong", {}, t("platform.accounts.create.hintStrong")), t("platform.accounts.create.hint2")),
      dlgErr,
      h("div", { class: "dialog-actions" },
        h("button", { type: "button", id: "platform-account-cancel", onclick: () => dialog.close() }, t("common.cancel")),
        h("button", { type: "submit", class: "primary" }, t("platform.accounts.create.submit")))));

  root.append(h("div", { id: "tab-platform-accounts", class: "tab-panel" },
    h("div", { class: "toolbar" },
      h("h2", {}, t("platform.accounts.title")),
      h("div", { class: "filters" },
        qIn,
        h("button", { id: "platform-account-search", onclick: () => { q = qIn.value.trim(); page = 1; loadList(); } }, t("platform.accounts.search")),
        newBtn)),
    errBox,
    linkHost,
    h("table", { class: "table" },
      h("thead", {}, h("tr", {},
        h("th", {}, t("platform.accounts.col.id")), h("th", {}, "Email"), h("th", {}, t("platform.accounts.col.status")),
        h("th", {}, t("platform.accounts.col.password")), h("th", {}, t("platform.accounts.col.actions")))),
      tbody),
    pg.node,
    detail,
    dialog));

  function roleSelect(roles, withNone) {
    const sel = h("select", {});
    if (withNone) sel.append(h("option", { value: "" }, t("platform.accounts.noRole")));
    for (const r of roles) sel.append(h("option", { value: String(r.id) }, r.name));
    return sel;
  }

  async function loadList() {
    const seq = ++loadSeq;
    errBox.hidden = true;
    newBtn.hidden = !ctx.canPlatform("account.manage");
    let data;
    try {
      data = await ctx.data.platformAccountsList({ page, q });
    } catch (e) {
      if (seq !== loadSeq || !ctx.alive()) return;
      errBox.textContent = e.message;
      errBox.hidden = false;
      return;
    }
    if (seq !== loadSeq || !ctx.alive()) return;
    pg.update(data.page, data.total, data.pageSize);
    rows(tbody, data.accounts, (a) => h("tr", { "data-email": a.email },
      h("td", {}, a.id),
      h("td", {}, a.email),
      h("td", {}, a.status === 1
        ? h("span", { class: "badge ok" }, t("platform.accounts.active"))
        : h("span", { class: "badge warn" }, t("platform.accounts.disabled"))),
      h("td", {}, a.has_password ? t("platform.accounts.pwSet") : h("span", { class: "badge" }, t("platform.accounts.pwUnset"))),
      h("td", {}, h("button", { onclick: () => openDetail(a.id) }, t("platform.accounts.detailBtn")))),
    { empty: t("platform.accounts.empty"), colspan: 5 });
  }

  async function openDetail(id) {
    const host = detail;
    clear(host).hidden = false;
    let d;
    try {
      d = await ctx.data.platformAccount(id);
    } catch (e) {
      host.append(h("p", { class: "error" }, e.message));
      return;
    }
    const manage = ctx.canPlatform("account.manage");
    const status = h("p", { class: "error", hidden: true });
    const act = async (fn) => {
      status.hidden = true;
      try { await fn(); } catch (e) { status.textContent = ctx.format.platformErrorText(e); status.hidden = false; }
    };

    host.append(
      h("h3", {}, t("platform.accounts.detail.title", { id: d.id, email: d.email })),
      h("p", {}, d.status === 1 ? t("platform.accounts.detail.statusActive") : t("platform.accounts.detail.statusDisabled"), "　｜　",
        d.has_password ? t("platform.accounts.detail.pwSet") : t("platform.accounts.detail.pwUnset")),
      h("p", {}, t("platform.accounts.detail.platformRoles"), d.platform_roles.length ? d.platform_roles.map((r) => r.name).join("、") : t("platform.accounts.detail.none")),
      status);

    if (manage) {
      host.append(h("div", { class: "toolbar" },
        d.status === 1
          ? h("button", { class: "danger", "data-act": "disable", onclick: () => act(async () => {
            const r = await ctx.actions.platformDisableAccount(d.id);
            if (r.cancelled) return;
            await refresh(d.id);
          }) }, t("platform.accounts.disableBtn"))
          : h("button", { "data-act": "enable", onclick: () => act(async () => {
            await ctx.actions.platformEnableAccount(d.id);
            await refresh(d.id);
          }) }, t("platform.accounts.enableBtn")),
        h("button", { "data-act": "reset", onclick: () => act(async () => {
          const r = await ctx.actions.platformResetAccountPassword(d.id);
          ctx.ui.platformLinkOnce(linkHost, { title: t("platform.accounts.resetTitle", { email: r.email }), link: r.link });
        }) }, t("platform.accounts.resetBtn"))));
    }

    host.append(h("h4", {}, t("platform.accounts.memberships")));
    const tbody2 = h("tbody", {});
    host.append(h("table", { class: "table" },
      h("thead", {}, h("tr", {}, h("th", {}, t("platform.accounts.m.shop")), h("th", {}, t("platform.accounts.m.roles")), h("th", {}, t("platform.accounts.m.actions")))),
      tbody2));
    rows(tbody2, d.memberships, (m) => h("tr", {},
      h("td", {}, `#${m.shop_id} ${m.shop_name}`),
      h("td", {}, m.roles.map((r) => r.name).join("、") || t("platform.accounts.m.noRole")),
      h("td", {}, manage ? h("button", { class: "danger", onclick: () => act(async () => {
        const r = await ctx.actions.platformRemoveMembership({ accountId: d.id, shopId: m.shop_id });
        if (r.cancelled) return;
        await refresh(d.id);
      }) }, t("platform.accounts.m.remove")) : "")),
    { empty: t("platform.accounts.m.empty"), colspan: 3 });

    if (manage) {
      const roles = await ctx.data.platformMerchantRoles();
      const shopInput = h("input", { type: "number", min: "1", placeholder: t("platform.accounts.shopIdPlaceholder") });
      const sel = roleSelect(roles, false);
      host.append(h("div", { class: "inline-form" },
        h("label", { class: "inline" }, t("platform.accounts.shopIdLabel"), " ", shopInput),
        h("label", { class: "inline" }, t("platform.accounts.roleLabel"), " ", sel),
        h("button", { class: "primary", onclick: () => act(async () => {
          await ctx.actions.platformAddMembership({ accountId: d.id, shopId: shopInput.value, roleId: sel.value });
          await refresh(d.id);
        }) }, t("platform.accounts.addMembership"))));
    }
  }

  async function refresh(id) {
    await loadList();
    await openDetail(id);
  }

  async function openCreate() {
    const roles = await ctx.data.platformMerchantRoles();
    const fresh = roleSelect(roles, true);
    fresh.id = "platform-account-role";
    roleSel.replaceWith(fresh);
    roleSel = fresh;
    emailIn.value = "";
    shopIn.value = "";
    dlgErr.hidden = true;
    dialog.showModal();
  }

  async function submitCreate(e) {
    e.preventDefault();
    dlgErr.hidden = true;
    try {
      const created = await ctx.actions.platformCreateAccount({ email: emailIn.value, shopId: shopIn.value, roleId: roleSel.value });
      dialog.close();
      await loadList();
      ctx.ui.platformLinkOnce(linkHost, { title: t("platform.accounts.setupTitle", { email: created.email }), link: created });
    } catch (err) {
      dlgErr.textContent = ctx.format.platformErrorText(err);
      dlgErr.hidden = false;
    }
  }

  // Mount: clear the link and detail boxes, then load the list.
  linkHost.hidden = true;
  detail.hidden = true;
  loadList();
}
