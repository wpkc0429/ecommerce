// Global content editor (old "全域內容" tab): a schema-driven form over the shop's content document.
export default async function content(root, ctx) {
  const { h, clear } = ctx.ui;
  const { t, can, actions, data } = ctx;
  const sf = ctx.ui.schemaForm;
  const sfOpts = { upload: actions.cmsUploadImage };

  const saveBtn = h("button", { id: "content-save", class: "primary", onclick: save }, t("cms.content.save"));
  const errBox = h("p", { id: "content-error", class: "error", hidden: true });
  const status = h("p", { id: "content-status", class: "hint", hidden: true });
  const form = h("form", { id: "content-form", class: "schema-form" });
  root.append(h("div", { id: "tab-content", class: "tab-panel" },
    h("div", { class: "toolbar" }, h("h2", {}, t("cms.content.title")), saveBtn),
    errBox, status, form));

  async function load() {
    let d;
    try { d = await data.cmsContent(); } catch (e) {
      if (ctx.alive()) { errBox.textContent = ctx.errorMessage(e); errBox.hidden = false; }
      return;
    }
    if (!ctx.alive()) return;
    saveBtn.hidden = !can("shop.update");
    if (d.config_schema) {
      sf.build(d.config_schema, d.content || {}, form, sfOpts);
    } else {
      clear(form);
      form.append(t("cms.content.none"));
    }
  }

  async function save() {
    errBox.hidden = true;
    status.hidden = true;
    try {
      await actions.cmsSaveContent(sf.collect(form));
      await load(); // reload what was actually stored (rich text may have been sanitized)
      status.textContent = t("cms.content.saved");
      status.hidden = false;
    } catch (err) {
      sf.showValidationErrors(form, errBox, err);
    }
  }

  await load();
}
