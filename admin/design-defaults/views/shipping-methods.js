// Shipping methods: list, create/edit dialog, delete. flat_rate is an integer in minor units, never scaled.
export default async function shippingMethods(root, ctx) {
  const { h, rows } = ctx.ui;
  const { t, format } = ctx;
  let editing = null;

  const newBtn = h("button", { id: "new-shipping-method-btn", class: "primary" }, t("shippingMethods.new"));
  const err = h("p", { id: "shipping-methods-error", class: "error", hidden: true });
  const tbody = h("tbody", { id: "shipping-methods-tbody" });

  const dTitle = h("h3", { id: "shipping-method-dialog-title" }, t("shippingMethods.dialog.new"));
  const name = h("input", { type: "text", id: "shipping-method-name", required: true });
  const carrier = h("input", { type: "text", id: "shipping-method-carrier", required: true });
  const rate = h("input", { type: "number", id: "shipping-method-rate", step: "1", min: "0", required: true });
  rate.value = "0";
  const preview = h("span", { class: "money-preview", id: "shipping-method-rate-preview" });
  const active = h("input", { type: "checkbox", id: "shipping-method-active", checked: true });
  const dErr = h("p", { id: "shipping-method-error", class: "error", hidden: true });
  const cancel = h("button", { type: "button", id: "shipping-method-cancel" }, t("shippingMethods.cancel"));
  const form = h("form", { id: "shipping-method-form", method: "dialog" },
    dTitle,
    h("label", {}, `${t("shippingMethods.field.name")} `, name),
    h("label", {}, `${t("shippingMethods.field.carrier")} `, carrier),
    h("label", {}, `${t("shippingMethods.field.rate")} `, rate, preview),
    h("label", { class: "checkbox" }, active, " ", h("span", {}, t("shippingMethods.field.active"))),
    dErr,
    h("div", { class: "dialog-actions" }, cancel, h("button", { type: "submit", class: "primary" }, t("shippingMethods.save"))));
  const dialog = h("dialog", { id: "shipping-method-dialog" }, form);

  root.append(h("div", { id: "tab-shipping-methods", class: "tab-panel" },
    h("div", { class: "toolbar" }, h("h2", {}, t("shippingMethods.title")), newBtn),
    err,
    h("table", { class: "table" },
      h("thead", {}, h("tr", {},
        ...["id", "name", "carrier", "rate", "status", "actions"].map((k) => h("th", {}, t(`shippingMethods.col.${k}`))))),
      tbody),
    dialog));

  async function load() {
    err.hidden = true;
    newBtn.hidden = !ctx.can("shipping_method.create");
    let list;
    try {
      list = await ctx.data.shippingMethodsList();
    } catch (e) {
      if (!ctx.alive()) return;
      err.textContent = ctx.errorMessage(e);
      err.hidden = false;
      return;
    }
    if (!ctx.alive()) return;
    rows(tbody, list, (m) => h("tr", { "data-name": m.name },
      h("td", {}, m.id),
      h("td", {}, m.name),
      h("td", {}, m.carrier),
      h("td", { class: "money" }, format.money(m.flat_rate, "TWD")),
      h("td", {}, m.is_active
        ? h("span", { class: "badge ok" }, t("shippingMethods.active"))
        : h("span", { class: "badge" }, t("shippingMethods.inactive"))),
      h("td", {},
        ctx.can("shipping_method.edit") ? h("button", { onclick: () => openDialog(m) }, t("shippingMethods.edit")) : "",
        ctx.can("shipping_method.delete")
          ? h("button", { class: "danger", onclick: () => remove(m) }, t("shippingMethods.delete")) : "")),
    { empty: t("shippingMethods.empty"), colspan: 6 });
  }

  function syncPreview() {
    preview.textContent = format.money(format.parseAmount(rate), "TWD");
  }

  function openDialog(m) {
    editing = m ?? null;
    dTitle.textContent = m ? t("shippingMethods.dialog.edit", { id: m.id }) : t("shippingMethods.dialog.new");
    name.value = m?.name ?? "";
    carrier.value = m?.carrier ?? "";
    rate.value = String(m?.flat_rate ?? 0);
    active.checked = m?.is_active ?? true;
    dErr.hidden = true;
    syncPreview();
    dialog.showModal();
  }

  async function submit(e) {
    e.preventDefault();
    dErr.hidden = true;
    try {
      await ctx.actions.saveShippingMethod(editing ? editing.id : null, {
        name: name.value, carrier: carrier.value,
        flat_rate: format.parseAmount(rate),
        is_active: active.checked,
      });
      dialog.close();
      await load();
    } catch (er) {
      const fields = Object.entries(ctx.fieldErrors(er));
      dErr.textContent = ctx.errorMessage(er) +
        (fields.length ? `（${fields.map(([p, m]) => `${p} ${m}`).join("；")}）` : "");
      dErr.hidden = false;
    }
  }

  async function remove(m) {
    err.hidden = true;
    try {
      const r = await ctx.actions.deleteShippingMethod(m.id, m.name);
      if (r && r.cancelled) return;
      await load();
    } catch (e) {
      err.textContent = ctx.errorMessage(e);
      err.hidden = false;
    }
  }

  newBtn.addEventListener("click", () => openDialog(null));
  cancel.addEventListener("click", () => dialog.close());
  form.addEventListener("submit", submit);
  rate.addEventListener("input", syncPreview);

  await load();
}
