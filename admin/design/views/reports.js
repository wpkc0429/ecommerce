// Operations reports + the shop timezone form + the admin-branding form. The period goes to the server; the criteria prose
// is static; the reconciliation split is shown so the merchant can verify the report. Counts never go through money formatting.

// Period and grouping survive navigation (the old panel's inputs kept them).
const remembered = { from: "", to: "", groupBy: "day" };

const TIMEZONES = ["Asia/Taipei", "Asia/Tokyo", "Asia/Shanghai", "Asia/Singapore", "America/New_York", "America/Los_Angeles", "Europe/London", "UTC"];

export default async function (root, ctx) {
  const { h, clear, rows } = ctx.ui;
  const t = ctx.t;
  const count = (n, unitKey) => ctx.format.reportCount(n, t(unitKey));

  const fromInput = h("input", { type: "date", id: "report-from" });
  const toInput = h("input", { type: "date", id: "report-to" });
  const groupBy = h("select", { id: "report-group-by", onchange: () => load() },
    h("option", { value: "day" }, t("reports.groupBy.day")), h("option", { value: "month" }, t("reports.groupBy.month")));
  groupBy.value = remembered.groupBy;

  const tzValue = h("span", { class: "kv-v", id: "report-timezone" }, "—");
  const tzInput = h("input", { type: "text", id: "report-timezone-input", placeholder: "Asia/Taipei", list: "report-timezone-list" });
  const tzForm = h("form", { id: "report-timezone-form", class: "inline-form", hidden: true,
    onsubmit: (e) => { e.preventDefault(); const v = tzInput.value.trim(); if (v) saveTimezone(v); } },
    h("label", { class: "inline" }, t("reports.timezone.set") + " ", tzInput),
    h("datalist", { id: "report-timezone-list" }, ...TIMEZONES.map((z) => h("option", { value: z }))),
    h("button", { type: "submit" }, t("reports.timezone.save")),
    h("button", { type: "button", id: "report-timezone-clear", onclick: () => saveTimezone(null) }, t("reports.timezone.clear")));
  const tzNote = h("p", { class: "hint", id: "report-timezone-note", hidden: true },
    h("strong", {}, t("reports.timezone.note1")), t("reports.timezone.note2"));

  const errorBox = h("p", { id: "report-error", class: "error", hidden: true });
  const currencies = h("div", { id: "report-currencies" });

  // ── admin branding form ──────────────────────────────────────────
  const logo = h("input", { type: "url", id: "branding-logo", placeholder: t("reports.branding.logoPlaceholder") });
  const primary = h("input", { type: "text", id: "branding-primary", placeholder: "#2563eb", maxlength: "7" });
  const accent = h("input", { type: "text", id: "branding-accent", placeholder: "#111827", maxlength: "7" });
  const saved = h("span", { class: "hint", id: "branding-saved", hidden: true }, t("reports.branding.saved"));
  const brandingForm = h("form", { id: "branding-form", class: "inline-form", hidden: true, onsubmit: saveBranding },
    h("label", { class: "inline" }, t("reports.branding.logo") + " ", logo),
    h("label", { class: "inline" }, t("reports.branding.primary") + " ", primary),
    h("label", { class: "inline" }, t("reports.branding.accent") + " ", accent),
    h("button", { type: "submit" }, t("reports.branding.save")),
    saved);

  const strong = (k) => h("strong", {}, t(k));
  root.append(h("div", { id: "tab-reports", class: "tab-panel" },
    h("div", { class: "toolbar" }, h("h2", {}, t("reports.title")),
      h("div", { class: "filters" },
        h("label", { class: "inline" }, t("reports.from") + " ", fromInput),
        h("label", { class: "inline" }, t("reports.to") + " ", toInput),
        groupBy,
        h("button", { id: "report-search", class: "primary", onclick: () => load() }, t("reports.search")))),
    h("div", { class: "detail-section" },
      h("div", { class: "detail-grid" },
        h("div", { class: "kv" }, h("span", { class: "kv-k" }, t("reports.timezone.label")), tzValue)),
      tzForm, tzNote),
    h("div", { class: "detail-section" },
      brandingForm,
      h("p", { class: "hint" }, t("reports.branding.hint1") + " ", h("code", {}, "#RRGGBB"), t("reports.branding.hint2"))),
    h("div", { class: "detail-section" },
      h("h3", {}, t("reports.criteria.title")),
      h("p", { class: "hint" },
        strong("reports.criteria.p1a"), t("reports.criteria.p1b"), strong("reports.criteria.p1c"), t("reports.criteria.p1d"), h("br"),
        t("reports.criteria.p1e"), " ", t("reports.criteria.p1f"), strong("reports.criteria.p1g"), t("reports.criteria.p1h")),
      h("p", { class: "hint" },
        strong("reports.criteria.p2a"), t("reports.criteria.p2b"), strong("reports.criteria.p2c"), t("reports.criteria.p2d"), " ",
        t("reports.criteria.p2e")),
      h("p", { class: "hint" },
        strong("reports.criteria.p3a"), t("reports.criteria.p3b"), " ", t("reports.criteria.p3c")),
      h("p", { class: "hint" },
        t("reports.criteria.p4a"), strong("reports.criteria.p4b"), t("reports.criteria.p4c"), " ",
        t("reports.criteria.p4d"), strong("reports.criteria.p4e"), t("reports.criteria.p4f"), strong("reports.criteria.p4g"),
        t("reports.criteria.p4h"))),
    errorBox, currencies));

  function showError(msg) { errorBox.textContent = msg; errorBox.hidden = false; }

  // ── period: default is the 1st of this month through today ───────
  function ensureDefaults() {
    if (!remembered.from || !remembered.to) {
      const d = ctx.logic.reportDefaultPeriod();
      remembered.from = d.from;
      remembered.to = d.to;
    }
    if (!fromInput.value || !toInput.value) { fromInput.value = remembered.from; toInput.value = remembered.to; }
  }

  // ── timezone ─────────────────────────────────────────────────────
  function renderTimezone(tz) {
    const fallback = tz?.source === "default";
    // "chose Asia/Taipei" and "chose nothing, the platform default happens to be Asia/Taipei" compute the same numbers,
    // but only the second should stop a merchant in Tokyo: the fallback is always said out loud.
    tzValue.textContent = fallback
      ? t("reports.timezone.fallback", { name: tz?.name ?? "—" })
      : t("reports.timezone.own", { name: tz?.name ?? "—" });
    tzForm.hidden = !ctx.can("shop.update");
    tzNote.hidden = tzForm.hidden;
    if (!tzForm.hidden && !fallback) tzInput.value = tz?.name ?? "";
  }

  async function saveTimezone(value) {
    errorBox.hidden = true;
    try {
      const res = await ctx.actions.setReportTimezone(value);
      if (res && res.cancelled) return;
    } catch (e) {
      showError(ctx.errorMessage(e));
      return;
    }
    if (value === null) tzInput.value = "";
    await load();
  }

  // ── branding ─────────────────────────────────────────────────────
  async function loadBranding() {
    brandingForm.hidden = !ctx.can("shop.update");
    if (brandingForm.hidden) return;
    try {
      const b = await ctx.data.adminBranding();
      logo.value = b.logo_url;
      primary.value = b.primary_color;
      accent.value = b.accent_color;
    } catch (e) {
      showError(ctx.errorMessage(e));
    }
  }

  async function saveBranding(e) {
    e.preventDefault();
    saved.hidden = true;
    try {
      await ctx.actions.saveAdminBranding({ logoUrl: logo.value, primaryColor: primary.value, accentColor: accent.value });
      saved.hidden = false;
    } catch (err) {
      showError(ctx.errorMessage(err));
    }
  }

  // ── one currency block ───────────────────────────────────────────
  const kv = (label, value, key) => h("div", { class: "kv", "data-k": key },
    h("span", { class: "kv-k" }, label), h("span", { class: "kv-v" }, value));

  function amountComposition(s, currency) {
    const discounted = Number(s.discount_amount) > 0;
    const part = (label, value, cls) => h("span", { class: cls ? `amount-part ${cls}` : "amount-part" },
      h("span", { class: "amount-label" }, label),
      h("span", { class: "money" }, ctx.format.money(value, currency)));
    // composed, not five parallel numbers; the discount row only appears when money was actually discounted
    return h("div", { class: "amount-composition" },
      part(t("reports.amount.subtotal"), s.subtotal_amount),
      discounted ? h("span", { class: "amount-op" }, t("reports.amount.minus")) : "",
      discounted ? part(t("reports.amount.discount"), s.discount_amount) : "",
      discounted ? h("span", { class: "amount-op" }, t("reports.amount.equals")) : "",
      discounted ? part(t("reports.amount.net"), s.net_amount) : "",
      h("span", { class: "amount-op" }, t("reports.amount.plus")),
      part(t("reports.amount.shipping"), s.shipping_fee),
      h("span", { class: "amount-op" }, t("reports.amount.equals")),
      part(t("reports.amount.total"), s.total_amount, "total"));
  }

  function reconciliation(r, currency) {
    return h("div", { class: "detail-grid" },
      kv(t("reports.recon.counted"), count(r.counted, "reports.unit.order"), "counted"),
      kv(t("reports.recon.returned"),
        `${count(r.returned, "reports.unit.order")} ／ ${ctx.format.money(r.returnedAmount, currency)}`, "returned"),
      kv(t("reports.recon.cancelled"), count(r.cancelled, "reports.unit.order"), "cancelled"),
      kv(t("reports.recon.unpaid"), count(r.unpaid, "reports.unit.order"), "unpaid"),
      kv(t("reports.recon.total"), count(r.total, "reports.unit.order"), "period-total"));
  }

  function table(headers, tbody) {
    return h("table", { class: "table" },
      h("thead", {}, h("tr", {}, ...headers.map((x) => h("th", {}, x)))),
      tbody);
  }

  function bucketsTable(c) {
    const body = h("tbody");
    rows(body, c.buckets, (b) => h("tr", { "data-period": b.period },
      h("td", {}, b.period),
      h("td", {}, count(b.order_count, "reports.unit.order")),
      h("td", { class: "money" }, ctx.format.money(b.subtotal_amount, c.currency)),
      h("td", { class: "money" }, ctx.format.money(b.discount_amount, c.currency)),
      h("td", { class: "money" }, ctx.format.money(b.shipping_fee, c.currency)),
      h("td", { class: "money" }, ctx.format.money(b.total_amount, c.currency)),
      h("td", {}, count(b.returned_order_count, "reports.unit.order"))),
    { empty: t("reports.buckets.empty"), colspan: 7 });
    return table(["period", "orders", "subtotal", "discount", "shipping", "total", "returned"].map((k) => t(`reports.buckets.th.${k}`)), body);
  }

  function productsTable(c) {
    const body = h("tbody");
    rows(body, c.products, (it) => h("tr", { "data-sku": it.sku_code },
      h("td", {}, it.product_title),
      h("td", { class: "mono" }, it.sku_code),
      h("td", {}, count(it.quantity, "reports.unit.piece")),
      h("td", { class: "money" }, ctx.format.money(it.gross_amount, c.currency))),
    { empty: t("reports.products.empty"), colspan: 4 });
    return table(["product", "sku", "quantity", "gross"].map((k) => t(`reports.products.th.${k}`)), body);
  }

  function currencyBlock(c) {
    return h("div", { class: "detail-section", "data-currency": c.currency },
      h("h3", {}, t("reports.currencyTitle", { currency: c.currency })),
      amountComposition(c.summary, c.currency),
      reconciliation(c.reconciliation, c.currency),
      h("h4", {}, t("reports.buckets.title")),
      bucketsTable(c),
      h("h4", {}, t("reports.products.title")),
      productsTable(c));
  }

  // ── load ─────────────────────────────────────────────────────────
  async function load() {
    ensureDefaults();
    errorBox.hidden = true;
    clear(currencies);
    remembered.from = fromInput.value;
    remembered.to = toInput.value;
    remembered.groupBy = groupBy.value;
    let report;
    try {
      report = await ctx.data.reportLoad({ from: fromInput.value, to: toInput.value, groupBy: groupBy.value });
    } catch (e) {
      showError(ctx.errorMessage(e));
      return;
    }
    renderTimezone(report.timezone);
    loadBranding();
    if (report.currencies.length === 0) {
      currencies.append(h("p", { class: "hint", id: "report-empty" }, t("reports.empty")));
      return;
    }
    // several currencies: one complete block each; nothing on this page adds them together
    if (report.currencies.length > 1) {
      currencies.append(h("p", { class: "hint", id: "report-multi-currency" }, t("reports.multiCurrency")));
    }
    for (const c of report.currencies) currencies.append(currencyBlock(c));
  }

  await load();
}
