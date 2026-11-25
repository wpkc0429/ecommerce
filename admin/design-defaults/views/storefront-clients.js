// Storefront clients: publishable keys, allowed origins (platform level) and ECPay credentials (write-only secrets).
export default async function storefrontClients(root, ctx) {
  const { h, rows } = ctx.ui;
  const { t, data, actions, logic } = ctx;

  const showErr = (box, msg) => { box.textContent = msg || ""; box.hidden = !msg; };
  const errText = (e) => logic.settingsErrorText(e);

  // ── publishable keys ──
  const keysErr = h("p", { id: "publishable-keys-error", class: "error", hidden: true });
  const keysBody = h("tbody", { id: "publishable-keys-tbody" });
  const newKeyBtn = h("button", { id: "new-publishable-key-btn", class: "primary", hidden: !ctx.can("publishable_key.create"), onclick: () => openKeyDialog() }, t("settings.keys.new"));
  const keysBlock = h("div", { id: "publishable-keys-block", hidden: !ctx.can("publishable_key.view") },
    h("div", { class: "toolbar" }, h("h2", {}, t("settings.keys.title")), newKeyBtn),
    h("p", { class: "hint" }, t("settings.keys.hintPre"), h("strong", {}, t("settings.keys.hintStrong")), t("settings.keys.hintPost")),
    keysErr,
    h("table", { class: "table" },
      h("thead", {}, h("tr", {}, ...["Id", "Key", "Label", "Created", "Status", "Actions"].map((k) => h("th", {}, t(`settings.keys.col${k}`))))),
      keysBody));

  // Replaces the old native prompt(): a dialog with one label input. Cancel creates nothing.
  const labelInput = h("input", { type: "text", id: "publishable-key-label", autocomplete: "off" });
  const keyDialog = h("dialog", { id: "publishable-key-dialog", class: "confirm-dialog" },
    h("form", { id: "publishable-key-form", onsubmit: (e) => { e.preventDefault(); const label = labelInput.value; keyDialog.close(); createKey(label); } },
      h("h3", {}, t("settings.keys.dialogTitle")),
      h("p", { class: "dlg__msg" }, t("settings.keys.labelPrompt")),
      labelInput,
      h("div", { class: "dialog-actions" },
        h("button", { type: "button", id: "publishable-key-cancel", onclick: () => keyDialog.close() }, t("common.cancel")),
        h("button", { type: "submit", class: "primary" }, t("settings.keys.dialogCreate")))));

  function openKeyDialog() {
    showErr(keysErr, "");
    labelInput.value = "";
    keyDialog.showModal();
  }

  async function loadKeys() {
    showErr(keysErr, "");
    let list;
    try { list = await data.settingsPublishableKeys(); } catch (e) { if (ctx.alive()) showErr(keysErr, ctx.errorMessage(e)); return; }
    if (!ctx.alive()) return;
    rows(keysBody, list, (k) => h("tr", { "data-key": k.key },
      h("td", {}, k.id),
      h("td", { class: "mono key-value" }, k.key),
      h("td", {}, k.label || "—"),
      h("td", {}, ctx.format.time(k.createdAt)),
      h("td", {}, k.revokedAt
        ? h("span", { class: "badge" }, t("settings.keys.revoked", { time: ctx.format.time(k.revokedAt) }))
        : h("span", { class: "badge ok" }, t("settings.keys.active"))),
      h("td", {}, !k.revokedAt && ctx.can("publishable_key.revoke")
        ? h("button", { class: "danger", onclick: () => revoke(k) }, t("settings.keys.revoke"))
        : "")),
    { empty: t("settings.keys.empty"), colspan: 6 });
  }

  async function createKey(label) {
    showErr(keysErr, "");
    try { await actions.settingsCreatePublishableKey({ label }); await loadKeys(); } catch (e) { showErr(keysErr, errText(e)); }
  }

  async function revoke(k) {
    showErr(keysErr, "");
    try {
      const r = await actions.settingsRevokePublishableKey(k);
      if (r.cancelled) return;
      await loadKeys();
    } catch (e) { showErr(keysErr, ctx.errorMessage(e)); }
  }

  // ── allowed origins ──
  const originsErr = h("p", { id: "allowed-origins-error", class: "error", hidden: true });
  const originsBody = h("tbody", { id: "allowed-origins-tbody" });
  const originInput = h("input", { type: "url", id: "allowed-origin-input", placeholder: t("settings.origins.originPlaceholder"), required: true });
  const purposeSel = h("select", { id: "allowed-origin-purpose" },
    h("option", { value: "storefront" }, t("settings.origins.purposeStorefront")),
    h("option", { value: "admin" }, t("settings.origins.purposeAdmin")));
  const originsBlock = h("div", { id: "allowed-origins-block", hidden: !ctx.can("shop.manage_client_origins") },
    h("div", { class: "toolbar" }, h("h2", {}, t("settings.origins.title"), " ", h("span", { class: "badge" }, t("settings.origins.badge")))),
    h("p", { class: "hint" }, t("settings.origins.hint1")),
    h("p", { class: "hint" },
      h("strong", {}, t("settings.origins.hint2Strong1")), t("settings.origins.hint2A"),
      h("em", {}, t("settings.origins.hint2Em1")), t("settings.origins.hint2B"),
      h("em", {}, t("settings.origins.hint2Em2")), t("settings.origins.hint2C"),
      h("strong", {}, t("settings.origins.hint2Strong2")), t("settings.origins.hint2D")),
    h("form", { id: "allowed-origin-form", class: "inline-form", onsubmit: addOrigin },
      h("label", { class: "inline" }, t("settings.origins.originLabel"), originInput),
      h("label", { class: "inline" }, t("settings.origins.purposeLabel"), purposeSel),
      h("button", { type: "submit", class: "primary" }, t("settings.origins.register"))),
    originsErr,
    h("table", { class: "table" },
      h("thead", {}, h("tr", {}, ...["Id", "Origin", "Purpose", "Created", "Actions"].map((k) => h("th", {}, t(`settings.origins.col${k}`))))),
      originsBody));

  async function loadOrigins() {
    showErr(originsErr, "");
    let list;
    try { list = await data.settingsAllowedOrigins(); } catch (e) { if (ctx.alive()) showErr(originsErr, ctx.errorMessage(e)); return; }
    if (!ctx.alive()) return;
    rows(originsBody, list, (o) => h("tr", { "data-origin": o.origin },
      h("td", {}, o.id),
      h("td", { class: "mono" }, o.origin),
      h("td", {}, o.purpose === "admin" ? t("settings.origins.purposeAdminShort") : t("settings.origins.purposeStorefrontShort")),
      h("td", {}, ctx.format.time(o.createdAt)),
      h("td", {}, h("button", { class: "danger", onclick: () => removeOrigin(o) }, t("settings.origins.remove")))),
    { empty: t("settings.origins.empty"), colspan: 5 });
  }

  async function addOrigin(e) {
    e.preventDefault();
    showErr(originsErr, "");
    try {
      await actions.settingsAddOrigin({ origin: originInput.value.trim(), purpose: purposeSel.value });
      originInput.value = "";
      await loadOrigins();
    } catch (ex) { showErr(originsErr, errText(ex)); }
  }

  async function removeOrigin(o) {
    showErr(originsErr, "");
    try {
      const r = await actions.settingsRemoveOrigin(o);
      if (r.cancelled) return;
      await loadOrigins();
    } catch (e) { showErr(originsErr, ctx.errorMessage(e)); }
  }

  // ── ECPay credentials: HashKey/HashIV are never shown, the two inputs stay blank ──
  const ecErr = h("p", { id: "ecpay-credentials-error", class: "error", hidden: true });
  const ecStatus = h("span", { id: "ecpay-credentials-status", class: "badge" });
  const ecMerchant = h("input", { type: "text", id: "ecpay-merchant-id", required: true });
  const ecKey = h("input", { type: "password", id: "ecpay-hash-key", placeholder: t("settings.ecpay.keepPlaceholder"), autocomplete: "off" });
  const ecIV = h("input", { type: "password", id: "ecpay-hash-iv", placeholder: t("settings.ecpay.keepPlaceholder"), autocomplete: "off" });
  const ecChoose = h("input", { type: "text", id: "ecpay-choose-payment", placeholder: t("settings.ecpay.choosePaymentPlaceholder") });
  const ecBlock = h("div", { id: "ecpay-credentials-block", hidden: !ctx.can("payment_credential.manage") },
    h("div", { class: "toolbar" }, h("h2", {}, t("settings.ecpay.title")), ecStatus),
    h("p", { class: "hint" }, t("settings.ecpay.hintPre"), h("strong", {}, t("settings.ecpay.hintStrong")), t("settings.ecpay.hintPost")),
    ecErr,
    h("form", { id: "ecpay-credentials-form", class: "inline-form", onsubmit: saveEcpay },
      h("label", { class: "inline" }, t("settings.ecpay.merchantId"), ecMerchant),
      h("label", { class: "inline" }, t("settings.ecpay.hashKey"), ecKey),
      h("label", { class: "inline" }, t("settings.ecpay.hashIv"), ecIV),
      h("label", { class: "inline" }, t("settings.ecpay.choosePayment"), ecChoose),
      h("button", { type: "submit", class: "primary" }, t("settings.ecpay.save"))));

  async function loadEcpay() {
    showErr(ecErr, "");
    let d;
    try { d = await data.settingsEcpay(); } catch (e) { if (ctx.alive()) showErr(ecErr, ctx.errorMessage(e)); return; }
    if (!ctx.alive()) return;
    ecMerchant.value = d.merchantId;
    ecChoose.value = d.choosePayment;
    ecKey.value = "";
    ecIV.value = "";
    ecStatus.textContent = t(d.status === "none" ? "settings.ecpay.statusNone" : d.status === "ok" ? "settings.ecpay.statusOk" : "settings.ecpay.statusPartial");
    ecStatus.className = d.status === "ok" ? "badge ok" : "badge";
  }

  async function saveEcpay(e) {
    e.preventDefault();
    showErr(ecErr, "");
    try {
      await actions.settingsSaveEcpay({ merchantId: ecMerchant.value.trim(), hashKey: ecKey.value, hashIV: ecIV.value, choosePayment: ecChoose.value.trim() });
      await loadEcpay();
    } catch (ex) { showErr(ecErr, errText(ex)); }
  }

  root.append(h("div", { id: "tab-storefront-clients", class: "tab-panel" }, keysBlock, originsBlock, ecBlock, keyDialog));

  await Promise.all([
    ctx.can("publishable_key.view") ? loadKeys() : Promise.resolve(),
    ctx.can("shop.manage_client_origins") ? loadOrigins() : Promise.resolve(),
    ctx.can("payment_credential.manage") ? loadEcpay() : Promise.resolve(),
  ]);
}
