// Platform view: shops list, provisioning wizard, setup checklist.
import { pager } from "../parts/platform-common.js";

export default async function (root, ctx) {
  const { h, clear, rows } = ctx.ui;
  const t = ctx.t;
  let page = 1;
  let loadSeq = 0;

  const newBtn = h("button", { id: "platform-shop-new-btn", class: "primary", onclick: openWizard }, t("platform.shops.new"));
  const errBox = h("p", { id: "platform-shops-error", class: "error", hidden: true });
  const result = h("div", { id: "platform-shop-result", class: "card", hidden: true });
  const tbody = h("tbody", { id: "platform-shops-tbody" });
  const pg = pager(ctx, "platform-shops", {
    onPrev: () => { page = Math.max(1, page - 1); loadList(); },
    onNext: () => { page += 1; loadList(); },
  });
  const checklist = h("div", { id: "platform-shop-checklist", class: "card", hidden: true });

  const nameIn = h("input", { type: "text", id: "platform-shop-name", required: true });
  const originIn = h("input", { type: "text", id: "platform-shop-origin", placeholder: t("platform.shops.wizard.originPlaceholder") });
  const ownerIn = h("input", { type: "email", id: "platform-shop-owner", required: true, autocomplete: "off" });
  const wizErr = h("p", { id: "platform-shop-error", class: "error", hidden: true });
  const dialog = h("dialog", { id: "platform-shop-dialog" },
    h("form", { id: "platform-shop-form", method: "dialog", onsubmit: submitWizard },
      h("h3", {}, t("platform.shops.wizard.title")),
      h("label", {}, t("platform.shops.wizard.name"), " ", nameIn),
      h("label", {}, t("platform.shops.wizard.origin"), " ", originIn),
      h("label", {}, t("platform.shops.wizard.owner"), " ", ownerIn),
      h("p", { class: "hint" }, t("platform.shops.wizard.hint")),
      wizErr,
      h("div", { class: "dialog-actions" },
        h("button", { type: "button", id: "platform-shop-cancel", onclick: () => dialog.close() }, t("common.cancel")),
        h("button", { type: "submit", class: "primary" }, t("platform.shops.wizard.submit")))));

  root.append(h("div", { id: "tab-platform-shops", class: "tab-panel" },
    h("div", { class: "toolbar" }, h("h2", {}, t("platform.shops.title")), newBtn),
    h("p", { class: "hint" }, t("platform.shops.hint")),
    errBox,
    result,
    h("table", { class: "table" },
      h("thead", {}, h("tr", {},
        h("th", {}, t("platform.shops.col.id")), h("th", {}, t("platform.shops.col.name")),
        h("th", {}, t("platform.shops.col.status")), h("th", {}, t("platform.shops.col.actions")))),
      tbody),
    pg.node,
    checklist,
    dialog));

  async function loadList() {
    const seq = ++loadSeq;
    errBox.hidden = true;
    newBtn.hidden = !ctx.canPlatform("shop.create");
    let data;
    try {
      data = await ctx.data.platformShopsList({ page });
    } catch (e) {
      if (seq !== loadSeq || !ctx.alive()) return;
      errBox.textContent = e.message;
      errBox.hidden = false;
      return;
    }
    if (seq !== loadSeq || !ctx.alive()) return;
    pg.update(data.page, data.total, data.pageSize);
    rows(tbody, data.shops, (s) => h("tr", { "data-name": s.name },
      h("td", {}, s.id),
      h("td", {}, s.name),
      h("td", {}, ctx.format.platformShopStatus(s.status)),
      h("td", {}, ctx.canPlatform("shop.view")
        ? h("button", { onclick: () => showChecklist(s.id, s.name, checklist) }, t("platform.shops.checklistBtn")) : "")),
    { empty: t("platform.shops.empty"), colspan: 4 });
  }

  async function showChecklist(shopID, name, host) {
    clear(host).hidden = false;
    host.append(h("h3", {}, t("platform.shops.checklistTitle", { name, id: shopID })));
    try {
      const st = await ctx.data.platformShopSetupStatus(shopID);
      const list = h("ul", { class: "checklist" });
      for (const key of ctx.enums.platformSetupChecks) {
        list.append(h("li", { "data-check": key, "data-done": String(st[key]) },
          st[key] ? h("span", { class: "badge ok" }, t("platform.shops.done")) : h("span", { class: "badge warn" }, t("platform.shops.todo")),
          " ", t(`platform.check.${key}`)));
      }
      host.append(list);
    } catch (e) {
      host.append(h("p", { class: "error" }, e.message));
    }
  }

  function openWizard() {
    nameIn.value = "";
    originIn.value = "";
    ownerIn.value = "";
    wizErr.hidden = true;
    dialog.showModal();
  }

  async function submitWizard(e) {
    e.preventDefault();
    wizErr.hidden = true;
    let res;
    try {
      res = await ctx.actions.platformProvisionShop({ name: nameIn.value, origin: originIn.value, ownerEmail: ownerIn.value });
    } catch (err) {
      wizErr.textContent = ctx.format.platformErrorText(err);
      wizErr.hidden = false;
      return;
    }
    dialog.close();
    await loadList();
    const host = clear(result);
    host.hidden = false;
    host.append(h("h3", {}, t("platform.shops.opened", { name: res.shop.name, id: res.shop.id })));
    const linkHost = h("div", { class: "card" });
    host.append(linkHost);
    if (res.owner.created) {
      ctx.ui.platformLinkOnce(linkHost, { title: t("platform.owner.linkTitle", { email: res.owner.email }), link: res.owner });
    } else {
      linkHost.append(h("p", {}, t("platform.owner.existing", { email: res.owner.email })));
    }
    const box = h("div", { class: "card" });
    host.append(box);
    await showChecklist(res.shop.id, res.shop.name, box);
  }

  loadList();
}
