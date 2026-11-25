// Documents list + the new-document dialog. The core decides who may open this screen (content.view) and who may create (content.edit).
export default async function contentList(root, ctx) {
  const { h, clear, fillSelect } = ctx.ui;
  const { t, can, data, actions, format } = ctx;
  let seq = 0;

  const tbody = h("tbody", { id: "documents-tbody" });
  const err = h("p", { id: "documents-error", class: "error", hidden: true });
  const notice = h("p", { id: "documents-notice", class: "hint", hidden: true });
  const newBtn = h("button", { id: "new-document-btn", class: "primary", hidden: true, onclick: openNew }, t("content.new.button"));
  const schemaBtn = h("button", { id: "documents-schema-link", hidden: !can("content.manage"), onclick: () => ctx.navigate("content-schema") }, t("content.schemaLink"));
  const table = h("table", { class: "table", id: "documents-table" },
    h("thead", {}, h("tr", {},
      h("th", {}, t("content.col.name")), h("th", {}, t("content.col.type")), h("th", {}, t("content.col.status")),
      h("th", {}, t("content.col.updated")), h("th", {}, t("content.col.actions")))),
    tbody);

  const keyIn = h("input", { type: "text", id: "new-document-key", required: true, pattern: "[a-z0-9][a-z0-9-]{0,62}", autocomplete: "off" });
  const typeSel = h("select", { id: "new-document-type" });
  const titleIn = h("input", { type: "text", id: "new-document-title", maxlength: "150", autocomplete: "off" });
  const newHint = h("p", { class: "hint" });
  const newErr = h("p", { id: "new-document-error", class: "error", hidden: true });
  const dialog = h("dialog", { id: "new-document-dialog" },
    h("form", { id: "new-document-form", method: "dialog", onsubmit: submitNew },
      h("h3", {}, t("content.new.title")),
      h("label", {}, `${t("content.new.key")} `, keyIn),
      h("label", {}, `${t("content.new.type")} `, typeSel),
      h("label", {}, `${t("content.new.name")} `, titleIn),
      newHint, newErr,
      h("div", { class: "dialog-actions" },
        h("button", { type: "button", id: "new-document-cancel", onclick: () => dialog.close() }, t("common.cancel")),
        h("button", { type: "submit", class: "primary" }, t("content.new.create")))));

  root.append(h("div", { id: "tab-documents", class: "tab-panel" },
    h("div", { class: "toolbar" }, h("h2", {}, t("content.title")), h("div", { class: "toolbar-actions" }, schemaBtn, newBtn)),
    err, notice, table, dialog));

  let info = { registered: false, types: [], collections: [] };

  function row(d) {
    return h("tr", { "data-key": d.key },
      h("td", {}, h("a", { href: `#/documents/${d.key}`, "data-testid": "content-open" }, h("strong", {}, d.title || d.key)), " ", h("small", { class: "mono" }, d.key)),
      h("td", {}, d.type),
      h("td", {},
        d.isPublished ? h("span", { class: "badge ok" }, t("content.status.published")) : h("span", { class: "badge" }, t("content.status.draft")),
        d.needsAttention ? h("span", { class: "badge warn", "data-testid": "content-flag-attention" }, t("content.flag.attention")) : "",
        d.declared ? "" : h("span", { class: "badge warn", "data-testid": "content-flag-orphan" }, t("content.flag.orphan")),
        d.scheduledPublishAt ? h("span", { class: "badge", title: format.time(d.scheduledPublishAt) }, t("content.flag.scheduled")) : ""),
      h("td", {}, format.time(d.updatedAt)),
      h("td", {},
        h("button", { onclick: () => ctx.navigate("documents", d.key) }, t("content.open")),
        h("button", { onclick: () => ctx.navigate("documents", d.key, "revisions") }, t("content.revisions"))));
  }

  async function load() {
    const my = ++seq;
    let docs = [];
    try {
      info = await data.contentTypes();
      if (info.registered) docs = await data.contentDocuments();
    } catch (e) { if (ctx.alive() && my === seq) { err.textContent = ctx.errorMessage(e); err.hidden = false; } return; }
    if (!ctx.alive() || my !== seq) return;
    err.hidden = true;
    newBtn.hidden = !(info.registered && can("content.edit"));
    table.hidden = !info.registered;
    notice.hidden = info.registered;
    if (!info.registered) {
      notice.textContent = `${t("content.notRegistered.title")} ${t("content.notRegistered.hint")}`;
      return;
    }
    clear(tbody);
    if (!docs.length) { tbody.append(h("tr", {}, h("td", { colspan: "5", class: "empty" }, t("content.empty.title")))); return; }
    for (const d of docs) tbody.append(row(d));
  }

  function openNew() {
    fillSelect(typeSel, info.types.map((ty) => ({ value: ty.key, label: ty.title === ty.key ? ty.key : `${ty.title} (${ty.key})` })), info.types.length ? info.types[0].key : "");
    keyIn.value = "";
    titleIn.value = "";
    newHint.textContent = info.collections.length
      ? t("content.new.hintCollections", { prefixes: info.collections.map((c) => c.prefix).join(", ") })
      : t("content.new.hint");
    newErr.hidden = true;
    dialog.showModal();
  }

  async function submitNew(ev) {
    ev.preventDefault();
    newErr.hidden = true;
    try {
      const r = await actions.createContentDocument({ key: keyIn.value.trim(), typeKey: typeSel.value, title: titleIn.value.trim() });
      dialog.close();
      ctx.navigate("documents", r.key);
    } catch (e) {
      newErr.textContent = ctx.errorMessage(e);
      newErr.hidden = false;
    }
  }

  await load();
}
