// Customer roster (#/members) and detail (#/members/{id}). Points are read-only here; the adjustment lives in the points view.
// Points go through ctx.format.points, money through ctx.format.money, never the other way round.

// The roster's search term and page survive navigation (the old panel kept them).
const remembered = { page: 1, q: "" };

export default async function (root, ctx) {
  const { h, clear, rows } = ctx.ui;
  const t = ctx.t;
  const id = Number(ctx.route.params[0]);
  const isDetail = Number.isFinite(id) && id > 0;

  /** Registration badge. A guest identity is not a "worse member": it is a person who never agreed to anything. */
  const identityBadge = (m) => (m.registered
    ? h("span", { class: "badge ok", "data-identity": "registered" }, t("members.badge.registered"))
    : h("span", { class: "badge", "data-identity": "guest" }, t("members.badge.guest")));

  if (isDetail) { root.append(await buildDetail(id)); return; }
  root.append(await buildRoster());

  // ── roster ───────────────────────────────────────────────────────
  async function buildRoster() {
    const q = h("input", { type: "search", id: "member-q", placeholder: t("members.search.placeholder") });
    q.value = remembered.q;
    const search = h("button", { id: "member-search", onclick: research }, t("members.search"));
    q.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); research(); } });
    const errorBox = h("p", { id: "members-error", class: "error", hidden: true });
    const tbody = h("tbody", { id: "members-tbody" });
    const prev = h("button", { id: "members-prev", onclick: () => { if (remembered.page > 1) { remembered.page--; loadList(); } } }, t("members.prev"));
    const info = h("span", { id: "members-pageinfo" });
    const next = h("button", { id: "members-next", onclick: () => { remembered.page++; loadList(); } }, t("members.next"));
    const panel = h("div", { id: "tab-members", class: "tab-panel" },
      h("div", { class: "toolbar" }, h("h2", {}, t("members.title")), h("div", { class: "filters" }, q, search)),
      h("p", { class: "hint" },
        t("members.hint1"), h("strong", {}, t("members.hint2")), t("members.hint3"),
        h("strong", {}, t("members.hint4")), t("members.hint5"), " ", t("members.hint6"), " ", t("members.hint7")),
      errorBox,
      h("table", { class: "table" },
        h("thead", {}, h("tr", {},
          ...["id", "email", "identity", "points", "level", "created", "actions"].map((k) => h("th", {}, t(`members.th.${k}`))))),
        tbody),
      h("div", { class: "pager" }, prev, info, next));

    function research() { remembered.q = q.value.trim(); remembered.page = 1; loadList(); }

    async function loadList() {
      errorBox.hidden = true;
      let data;
      try {
        data = await ctx.data.memberRoster({ page: remembered.page, q: remembered.q });
      } catch (e) {
        errorBox.textContent = ctx.errorMessage(e);
        errorBox.hidden = false;
        rows(tbody, [], () => null, { empty: t("members.loadFailed"), colspan: 7 });
        return;
      }
      remembered.page = data.page;
      rows(tbody, data.members, (m) => h("tr", { "data-member-id": String(m.id) },
        h("td", {}, m.id),
        h("td", { class: "member-email" }, m.email || t("members.noEmail")),
        h("td", {}, identityBadge(m)),
        // points, not money
        h("td", { class: "points" }, ctx.format.points(m.points ?? 0)),
        h("td", {}, m.level ? m.level.name : "—"),
        h("td", {}, ctx.format.time(m.created_at)),
        h("td", {}, h("button", { onclick: () => ctx.navigate("members", m.id) }, t("members.detail")))),
      { empty: remembered.q ? t("members.emptyFiltered") : t("members.empty"), colspan: 7 });
      pager(data);
    }

    function pager(data) {
      const pages = Math.max(1, Math.ceil((data.total || 0) / data.pageSize));
      info.textContent = t("common.pageInfo", { page: remembered.page, pages, total: data.total || 0 });
      prev.disabled = remembered.page <= 1;
      next.disabled = remembered.page >= pages;
    }

    await loadList();
    return panel;
  }

  // ── detail ───────────────────────────────────────────────────────
  async function buildDetail(memberID) {
    const title = h("h2", { id: "member-detail-title" }, t("members.detail.titleId", { id: memberID }));
    const pointsBtn = h("button", { id: "member-points-btn", onclick: () => ctx.navigate("points", memberID) }, t("members.detail.pointsBtn"));
    const errorBox = h("p", { id: "member-detail-error", class: "error", hidden: true });
    const summary = h("div", { id: "member-summary", class: "detail-section" });
    const ordersBody = h("tbody", { id: "member-orders-tbody" });
    const ordersBlock = h("div", { id: "member-orders-block" },
      h("h3", {}, t("members.orders.title")),
      h("table", { class: "table" },
        h("thead", {}, h("tr", {},
          ...["id", "created", "delivery", "total", "payment", "fulfillment", "actions"].map((k) => h("th", {}, t(`members.orders.th.${k}`))))),
        ordersBody),
      h("p", { class: "hint" }, t("members.orders.hint")));
    const panel = h("div", { id: "tab-member-detail", class: "tab-panel" },
      h("div", { class: "toolbar" }, title,
        h("div", { class: "toolbar-actions" },
          h("button", { id: "member-back", onclick: () => ctx.navigate("members") }, t("members.detail.back")),
          pointsBtn)),
      errorBox, summary, ordersBlock);

    const row = (k, v) => h("div", { class: "kv" }, h("span", { class: "kv-k" }, k), h("span", { class: "kv-v" }, v));

    function renderSummary(m) {
      clear(summary);
      summary.append(h("div", { class: "detail-grid" },
        row(t("members.summary.id"), `#${m.id}`),
        row(t("members.summary.email"), h("span", { id: "member-email" }, m.email || t("members.noEmail"))),
        row(t("members.summary.identity"), identityBadge(m)),
        row(t("members.summary.status"), ctx.logic.memberLabel("memberStatus", m.status)),
        row(t("members.summary.created"), ctx.format.time(m.created_at))));
      if (!ctx.can("point.view")) return;
      // read-only; the adjustment entrance is #/points/{id}
      summary.append(h("div", { class: "detail-grid" },
        row(t("members.summary.points"), h("span", { class: "points", id: "member-points" }, ctx.format.points(m.points ?? 0))),
        row(t("members.summary.level"), h("span", { id: "member-level" }, m.level ? m.level.name : t("members.summary.noLevel")))));
      summary.append(h("p", { class: "hint" }, t("members.summary.hint")));
      if (!m.registered) summary.append(h("p", { class: "hint", id: "member-guest-note" }, t("members.summary.guestNote")));
    }

    async function renderOrders(m) {
      ordersBlock.hidden = !ctx.can("order.view");
      if (!ctx.can("order.view")) return;
      let list = [];
      try {
        list = await ctx.data.memberOrders(m.id);
      } catch (e) {
        rows(ordersBody, [], () => null, { empty: ctx.errorMessage(e), colspan: 7 });
        return;
      }
      rows(ordersBody, list, (o) => h("tr", { "data-order-id": String(o.id) },
        h("td", {}, o.id),
        h("td", {}, ctx.format.time(o.created_at)),
        h("td", {}, o.requested_delivery_date || "—"),
        // money, not points
        h("td", { class: "money" }, ctx.format.money(o.total_amount, o.currency)),
        h("td", {}, ctx.logic.memberLabel("payment", o.payment_status)),
        h("td", {}, ctx.logic.memberLabel("fulfillment", o.fulfillment_status)),
        h("td", {}, h("a", { href: `#/orders/${o.id}` }, t("members.orders.detail")))),
      { empty: t("members.orders.empty"), colspan: 7 });
    }

    errorBox.hidden = true;
    let m;
    try {
      m = await ctx.data.memberDetail(memberID);
    } catch (e) {
      errorBox.textContent = ctx.errorMessage(e);
      errorBox.hidden = false;
      clear(summary);
      clear(ordersBody);
      pointsBtn.hidden = true;
      return panel;
    }
    renderSummary(m);
    pointsBtn.hidden = !ctx.can("point.view");
    await renderOrders(m);
    return panel;
  }
}
