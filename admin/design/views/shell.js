// The app shell: topbar (brand, shop selector, account, logout), the permission-filtered navigation tabs, the
// "no access" notice and the slot where the core mounts each screen. ctx.nav.items lists only what the person may open.
// Must return { main, setActive(navKey), setNoAccess(text) }.
export default function shell(root, ctx) {
  const { h } = ctx.ui;
  const t = ctx.t;
  const s = ctx.session;

  const select = h("select", { id: "shop-select", hidden: !s.hasShopList,
    onchange: (e) => ctx.session.selectShop(e.target.value) });
  const input = h("input", { type: "number", id: "shop-input", min: "1", placeholder: t("shell.shopIdPlaceholder"), hidden: s.hasShopList,
    onchange: (e) => ctx.session.selectShop(e.target.value) });
  if (s.hasShopList) {
    for (const id of s.shopIds) select.append(h("option", { value: id }, t("shell.shopOption", { id })));
    select.value = String(s.shopId);
  } else {
    input.value = s.shopId;
  }

  const tabs = h("nav", { class: "tabs", id: "tabs" },
    ctx.nav.items.map((item) => h("button", { class: "tab", "data-tab": item.key, onclick: () => ctx.navigate(item.key) }, t(item.copy))));
  const noAccess = h("p", { id: "no-access", class: "error", hidden: true });
  const main = h("div", { class: "view-slot" });

  root.append(h("section", { id: "view-main", class: "view" },
    h("header", { class: "topbar" },
      h("span", {}, h("img", { class: "brand-logo", alt: "", hidden: true }), h("strong", {}, t("shell.brand"))),
      h("div", { class: "topbar-right" },
        h("label", {}, t("shell.shop") + " ", select, input),
        h("span", { id: "whoami" }, s.userEmail),
        h("button", { id: "logout-btn", onclick: () => ctx.auth.logout() }, t("shell.logout")))),
    tabs, noAccess, main));

  // Navigation badge for the platform applications tab. The core calls refreshBadge() after route renders and after writes
  // that change the count; the platform group exposes ctx.data.applicationsBadge() -> {count} (null when not permitted).
  async function refreshBadge() {
    if (!ctx.data || typeof ctx.data.applicationsBadge !== "function") return;
    const tab = ctx.ui.$('[data-tab="platform-applications"]', tabs);
    if (!tab) return;
    try {
      const b = await ctx.data.applicationsBadge();
      for (const prev of ctx.ui.$$(".nav-count", tab)) prev.replaceWith();
      if (b && b.count > 0) tab.append(h("span", { class: "badge warn nav-count" }, String(b.count)));
    } catch { /* the badge is a convenience only */ }
  }
  refreshBadge();

  return {
    main,
    setActive(key) { for (const b of ctx.ui.$$(".tab", tabs)) b.classList.toggle("active", b.dataset.tab === key); },
    refreshBadge,
    setNoAccess(text) { noAccess.textContent = text || ""; noAccess.hidden = !text; },
  };
}
