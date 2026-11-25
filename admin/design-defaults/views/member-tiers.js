// Member tiers. No amounts on this screen: min_points is POINTS and discount_percent is a PERCENT (never formatMoney).
export default async function (root, ctx) {
  const { h, $, rows } = ctx.ui;
  const t = ctx.t;

  const newBtn = h("button", { id: "new-member-tier-btn", class: "primary", onclick: () => openDialog(null) }, t("memberTiers.new"));
  const errorBox = h("p", { id: "member-tiers-error", class: "error", hidden: true });
  const tbody = h("tbody", { id: "member-tiers-tbody" });

  // dialog
  const dlgTitle = h("h3", { id: "member-tier-dialog-title" }, t("memberTiers.dialog.new"));
  const nameInput = h("input", { type: "text", id: "member-tier-name", required: true });
  const minInput = h("input", { type: "number", id: "member-tier-min-points", step: "1", min: "0", value: "0", required: true });
  const discInput = h("input", { type: "number", id: "member-tier-discount", step: "1", min: "0", max: "100", value: "0" });
  const dlgError = h("p", { id: "member-tier-error", class: "error", hidden: true });
  const form = h("form", { id: "member-tier-form", method: "dialog", onsubmit: submit },
    dlgTitle,
    h("label", {}, t("memberTiers.field.name") + " ", nameInput),
    h("label", {}, t("memberTiers.field.minPoints") + " ", minInput),
    h("label", {}, t("memberTiers.field.discount") + " ", discInput),
    dlgError,
    h("div", { class: "dialog-actions" },
      h("button", { type: "button", id: "member-tier-cancel", onclick: () => dialog.close() }, t("memberTiers.cancel")),
      h("button", { type: "submit", class: "primary" }, t("memberTiers.save"))));
  const dialog = h("dialog", { id: "member-tier-dialog" }, form);

  const panel = h("div", { id: "tab-member-tiers", class: "tab-panel" },
    h("div", { class: "toolbar" }, h("h2", {}, t("memberTiers.title")), newBtn),
    h("p", { class: "hint" },
      t("memberTiers.hint1"), h("strong", {}, t("memberTiers.hint2")), t("memberTiers.hint3"), " ",
      t("memberTiers.hint4"), h("strong", {}, t("memberTiers.hint5")), t("memberTiers.hint6"),
      h("strong", {}, t("memberTiers.hint7")), t("memberTiers.hint8")),
    errorBox,
    h("table", { class: "table" },
      h("thead", {}, h("tr", {},
        h("th", {}, t("memberTiers.th.id")), h("th", {}, t("memberTiers.th.name")), h("th", {}, t("memberTiers.th.minPoints")),
        h("th", {}, t("memberTiers.th.discount")), h("th", {}, t("memberTiers.th.actions")))),
      tbody),
    dialog);
  root.append(panel);

  let editing = null;

  function openDialog(tier) {
    editing = tier ?? null;
    dlgTitle.textContent = tier ? t("memberTiers.dialog.edit", { id: tier.id }) : t("memberTiers.dialog.new");
    nameInput.value = tier?.name ?? "";
    minInput.value = tier?.min_points ?? 0;
    discInput.value = tier?.discount_percent ?? 0;
    dlgError.hidden = true;
    dialog.showModal();
  }

  async function load() {
    errorBox.hidden = true;
    newBtn.hidden = !ctx.can("member_tier.create");
    let ladder;
    try {
      ladder = await ctx.data.memberTiers();
    } catch (e) {
      errorBox.textContent = ctx.errorMessage(e);
      errorBox.hidden = false;
      return;
    }
    rows(tbody, ladder, (tier) => h("tr", { "data-tier": tier.name },
      h("td", {}, tier.id),
      h("td", {}, tier.name),
      h("td", { class: "points" }, ctx.format.points(tier.min_points)),
      h("td", {}, ctx.format.percent(tier.discount_percent ?? 0)),
      h("td", {},
        ctx.can("member_tier.edit") ? h("button", { onclick: () => openDialog(tier) }, t("memberTiers.edit")) : "",
        ctx.can("member_tier.delete") ? h("button", { class: "danger", onclick: () => remove(tier) }, t("memberTiers.delete")) : "")),
    { empty: t("memberTiers.empty"), colspan: 5 });
  }

  async function submit(e) {
    e.preventDefault();
    dlgError.hidden = true;
    try {
      await ctx.actions.saveMemberTier({
        id: editing ? editing.id : undefined,
        name: nameInput.value, minPoints: minInput.value, discountPercent: discInput.value,
      });
      dialog.close();
      await load();
    } catch (err) {
      dlgError.textContent = ctx.errorMessage(err);
      dlgError.hidden = false;
    }
  }

  async function remove(tier) {
    errorBox.hidden = true;
    try {
      const res = await ctx.actions.deleteMemberTier({ id: tier.id, name: tier.name });
      if (res && res.cancelled) return;
      await load();
    } catch (e) {
      errorBox.textContent = ctx.errorMessage(e);
      errorBox.hidden = false;
    }
  }

  await load();
}
