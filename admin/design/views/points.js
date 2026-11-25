// Member points: balance, tier, ledger and the manual adjustment. Addressed by member id (#/points/57).
// No money on this screen: points / points_delta go through ctx.format.points / pointsDelta.

// The member and ledger page survive navigation (the old panel kept them).
const remembered = { memberID: null, page: 1 };

export default async function (root, ctx) {
  const { h, rows } = ctx.ui;
  const t = ctx.t;

  const memberInput = h("input", { type: "number", id: "points-member-input", min: "1", placeholder: t("points.memberPlaceholder") });
  memberInput.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); go.click(); } });
  const go = h("button", { id: "points-member-go", onclick: () => {
    const id = parseInt(memberInput.value, 10);
    if (Number.isFinite(id) && id > 0 && !ctx.navigate("points", id)) load(id);
  } }, t("points.go"));
  const hint = h("p", { id: "points-hint", class: "hint" },
    t("points.hint1"), " ", h("strong", {}, t("points.hint2")), t("points.hint3"));
  const errorBox = h("p", { id: "points-error", class: "error", hidden: true });

  const memberLabel = h("span", { class: "kv-v", id: "points-member-label" }, "—");
  const balance = h("span", { class: "kv-v points", id: "points-balance" }, "—");
  const level = h("span", { class: "kv-v", id: "points-level" }, "—");
  const deltaInput = h("input", { type: "number", id: "points-adjust-delta", step: "1", placeholder: t("points.adjust.deltaPlaceholder") });
  const reasonInput = h("input", { type: "text", id: "points-adjust-reason", placeholder: t("points.adjust.reasonPlaceholder") });
  const adjustError = h("p", { id: "points-adjust-error", class: "error", hidden: true });
  const adjustBox = h("div", { id: "points-adjust-box", class: "detail-section", hidden: true },
    h("h3", {}, t("points.adjust.title")),
    h("form", { id: "points-adjust-form", class: "inline-form", onsubmit: adjust },
      h("label", { class: "inline" }, t("points.adjust.delta") + " ", deltaInput),
      h("label", { class: "inline" }, t("points.adjust.reason") + " ", reasonInput),
      h("button", { type: "submit", class: "primary" }, t("points.adjust.submit"))),
    adjustError,
    h("p", { class: "hint" }, t("points.adjust.hint")));
  const tbody = h("tbody", { id: "points-tbody" });
  const prev = h("button", { id: "points-prev", onclick: () => { if (remembered.page > 1) { remembered.page--; loadLedger(); } } }, t("points.prev"));
  const info = h("span", { id: "points-pageinfo" });
  const next = h("button", { id: "points-next", onclick: () => { remembered.page++; loadLedger(); } }, t("points.next"));
  const body = h("div", { id: "points-body", hidden: true },
    h("div", { class: "detail-section" },
      h("div", { class: "detail-grid" },
        h("div", { class: "kv" }, h("span", { class: "kv-k" }, t("points.kv.member")), memberLabel),
        h("div", { class: "kv" }, h("span", { class: "kv-k" }, t("points.kv.balance")), balance),
        h("div", { class: "kv" }, h("span", { class: "kv-k" }, t("points.kv.level")), level))),
    adjustBox,
    h("h3", {}, t("points.ledger.title")),
    h("table", { class: "table" },
      h("thead", {}, h("tr", {}, ...["id", "delta", "kind", "source", "time"].map((k) => h("th", {}, t(`points.th.${k}`))))),
      tbody),
    h("div", { class: "pager" }, prev, info, next));

  root.append(h("div", { id: "tab-points", class: "tab-panel" },
    h("div", { class: "toolbar" }, h("h2", {}, t("points.title")),
      h("div", { class: "filters" },
        h("label", { class: "inline" }, t("points.memberNo") + " ", memberInput), go)),
    hint, errorBox, body));

  async function load(memberID) {
    errorBox.hidden = true;
    const n = Number(memberID);
    if (Number.isFinite(n) && n > 0 && n !== remembered.memberID) { remembered.memberID = n; remembered.page = 1; }
    memberInput.value = remembered.memberID ?? "";
    adjustBox.hidden = !ctx.can("point.adjust") || !remembered.memberID;
    if (!remembered.memberID) { body.hidden = true; hint.hidden = false; return; }
    hint.hidden = true;
    body.hidden = false;
    await Promise.all([loadBalance(), loadLedger()]);
  }

  async function loadBalance() {
    try {
      const data = await ctx.data.pointsBalance(remembered.memberID);
      balance.textContent = ctx.format.points(data.points);
      level.textContent = data.level ? data.level.name : t("points.noLevel");
      memberLabel.textContent = t("points.memberLabel", { id: data.memberId });
    } catch (e) {
      errorBox.textContent = ctx.errorMessage(e);
      errorBox.hidden = false;
      balance.textContent = "—";
      level.textContent = "—";
    }
  }

  async function loadLedger() {
    let data;
    try {
      data = await ctx.data.pointsLedger(remembered.memberID, remembered.page);
    } catch {
      rows(tbody, [], () => null, { empty: t("points.ledger.failed"), colspan: 5 });
      return;
    }
    remembered.page = data.page;
    rows(tbody, data.transactions, (tx) => h("tr", { "data-kind": String(tx.kind) },
      h("td", {}, tx.id),
      h("td", { class: tx.points_delta < 0 ? "points negative" : "points" }, ctx.format.pointsDelta(tx.points_delta)),
      h("td", {}, ctx.logic.memberLabel("pointKind", tx.kind)),
      h("td", {}, tx.order_id
        ? h("a", { href: `#/orders/${tx.order_id}` }, t("points.order", { id: tx.order_id }))
        : (tx.reason || "—")),
      h("td", {}, ctx.format.time(tx.created_at))),
    { empty: t("points.ledger.empty"), colspan: 5 });
    const pages = Math.max(1, Math.ceil((data.total || 0) / data.pageSize));
    info.textContent = t("common.pageInfo", { page: remembered.page, pages, total: data.total || 0 });
    prev.disabled = remembered.page <= 1;
    next.disabled = remembered.page >= pages;
  }

  async function adjust(e) {
    e.preventDefault();
    adjustError.hidden = true;
    try {
      await ctx.actions.adjustMemberPoints({ memberId: remembered.memberID, delta: deltaInput.value, reason: reasonInput.value });
      deltaInput.value = "";
      reasonInput.value = "";
      remembered.page = 1;
      await Promise.all([loadBalance(), loadLedger()]);
    } catch (ex) {
      adjustError.textContent = ctx.errorMessage(ex);
      adjustError.hidden = false;
    }
  }

  await load(ctx.route.params[0]);
}
