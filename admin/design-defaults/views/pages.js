// Pages: list + new-page dialog + the page editor (SEO box, preview, save draft, publish, unpublish, delete).
// The editor panel lives inside this view (the old transient `editor` tab). The core owns permissions, the delete
// confirmation, the preview window and the media limits; this view only draws.
export default async function pages(root, ctx) {
  const { h, clear } = ctx.ui;
  const { t, can, actions, data } = ctx;
  const sf = ctx.ui.schemaForm;
  const sfOpts = { upload: actions.cmsUploadImage };

  let editing = null;
  let listSeq = 0;
  let editSeq = 0;

  // ── list panel ──
  const tbody = h("tbody", { id: "pages-tbody" });
  const listError = h("p", { id: "pages-error", class: "error", hidden: true });
  const newBtn = h("button", { id: "new-page-btn", class: "primary", onclick: openNewDialog }, t("cms.pages.new"));
  const typeSel = h("select", { id: "new-page-type" });
  const titleIn = h("input", { type: "text", id: "new-page-title", required: true });
  const slugIn = h("input", { type: "text", id: "new-page-slug", required: true, pattern: "[a-z0-9-]+" });
  const newError = h("p", { id: "new-page-error", class: "error", hidden: true });
  const dialog = h("dialog", { id: "new-page-dialog" },
    h("form", { id: "new-page-form", method: "dialog", onsubmit: submitNew },
      h("h3", {}, t("cms.newPage.title")),
      h("label", {}, `${t("cms.newPage.type")} `, typeSel),
      h("label", {}, `${t("cms.newPage.name")} `, titleIn),
      h("label", {}, `${t("cms.newPage.slug")} `, slugIn),
      newError,
      h("div", { class: "dialog-actions" },
        h("button", { type: "button", id: "new-page-cancel", onclick: () => dialog.close() }, t("cms.newPage.cancel")),
        h("button", { type: "submit", class: "primary" }, t("cms.newPage.create")))));
  const listPanel = h("div", { id: "tab-pages", class: "tab-panel" },
    h("div", { class: "toolbar" }, h("h2", {}, t("cms.pages.title")), newBtn),
    listError,
    h("table", { class: "table" },
      h("thead", {}, h("tr", {},
        h("th", {}, t("cms.pages.colId")), h("th", {}, t("cms.pages.colTitle")), h("th", {}, t("cms.pages.colSlug")),
        h("th", {}, t("cms.pages.colType")), h("th", {}, t("cms.pages.colStatus")), h("th", {}, t("cms.pages.colActions")))),
      tbody),
    dialog);

  // ── editor panel ──
  const edTitle = h("h2", { id: "editor-title" }, t("cms.editor.title"));
  const edStatus = h("p", { id: "editor-status", class: "hint" });
  const edError = h("p", { id: "editor-error", class: "error", hidden: true });
  const form = h("form", { id: "editor-form", class: "schema-form" });
  const seoTitle = h("input", { type: "text", id: "seo-title" });
  const seoKeywords = h("input", { type: "text", id: "seo-keywords" });
  const seoDesc = h("textarea", { id: "seo-description", rows: 2 });
  const saveBtn = h("button", { id: "editor-save", class: "primary", onclick: save }, t("cms.editor.save"));
  const pubBtn = h("button", { id: "editor-publish", class: "publish", onclick: publishPage }, t("cms.editor.publish"));
  const unpubBtn = h("button", { id: "editor-unpublish", onclick: unpublishPage }, t("cms.editor.unpublish"));
  const editorPanel = h("div", { id: "tab-editor", class: "tab-panel", hidden: true },
    h("div", { class: "toolbar" }, edTitle,
      h("div", { class: "toolbar-actions" },
        h("button", { id: "editor-back", onclick: back }, t("cms.editor.back")),
        h("button", { id: "editor-preview", onclick: () => preview(editing && editing.id, edError) }, t("cms.editor.preview")),
        saveBtn, pubBtn, unpubBtn)),
    edStatus, edError, form,
    h("details", { class: "seo-box" },
      h("summary", {}, t("cms.editor.seo")),
      h("label", {}, `${t("cms.editor.seoTitle")} `, seoTitle),
      h("label", {}, `${t("cms.editor.seoKeywords")} `, seoKeywords),
      h("label", {}, `${t("cms.editor.seoDescription")} `, seoDesc)));

  root.append(listPanel, editorPanel);

  function showList() { listPanel.hidden = false; editorPanel.hidden = true; }
  function showEditor() { listPanel.hidden = true; editorPanel.hidden = false; }
  function say(box, err) { box.textContent = ctx.errorMessage(err); box.hidden = false; }

  async function load() {
    const seq = ++listSeq;
    newBtn.hidden = !can("page.create");
    let list;
    try { list = await data.cmsPages(); } catch (e) { if (ctx.alive() && seq === listSeq) say(listError, e); return; }
    if (!ctx.alive() || seq !== listSeq) return;
    listError.hidden = true;
    clear(tbody);
    for (const p of list) {
      const status = p.status === 1
        ? h("span", { class: "badge ok" }, t("cms.pages.published"))
        : h("span", { class: "badge" }, t("cms.pages.draft"));
      tbody.append(h("tr", {},
        h("td", {}, p.id),
        h("td", {}, p.title || "—"),
        h("td", {}, p.slug),
        h("td", {}, p.incompatible
          ? h("span", { class: "badge warn", title: t("cms.pages.incompatibleTitle") }, t("cms.pages.incompatible", { type: p.type_key }))
          : p.type_key),
        h("td", {}, status),
        h("td", {},
          h("button", { onclick: () => openEditor(p.id) }, t("cms.pages.edit")),
          h("button", { onclick: () => preview(p.id, listError) }, t("cms.pages.preview")),
          can("page.publish") ? (p.status === 1
            ? h("button", { onclick: () => pageAction(p.id, "unpublish") }, t("cms.pages.unpublish"))
            : h("button", { class: "publish", onclick: () => pageAction(p.id, "publish") }, t("cms.pages.publish"))) : "",
          p.slug === "home" ? "" : (can("page.delete")
            ? h("button", { class: "danger", onclick: () => removePage(p.id) }, t("cms.pages.delete")) : ""))));
    }
  }

  async function pageAction(id, action) {
    try {
      await (action === "publish" ? actions.cmsPublishPage(id) : actions.cmsUnpublishPage(id));
      await load();
    } catch (e) { say(listError, e); }
  }

  async function removePage(id) {
    try {
      const r = await actions.cmsDeletePage(id);
      if (r && r.cancelled) return;
      await load();
    } catch (e) { say(listError, e); }
  }

  async function preview(id, box) {
    try { await actions.cmsPreviewPage(id); } catch (e) { say(box, e); }
  }

  async function openEditor(pageID) {
    const seq = ++editSeq;
    let p;
    try { p = await data.cmsPage(pageID); } catch (e) { if (ctx.alive() && seq === editSeq) say(listError, e); return; }
    if (!ctx.alive() || seq !== editSeq) return;
    editing = p;
    showEditor();
    edTitle.textContent = t("cms.editor.titleNamed", { name: p.title || p.slug });
    edStatus.textContent = (p.status === 1 ? t("cms.pages.published") : t("cms.pages.draft"))
      + (p.incompatible ? t("cms.editor.incompatibleSuffix") : "");
    edError.hidden = true;
    const meta = p.meta || {};
    seoTitle.value = meta.seo_title || "";
    seoKeywords.value = meta.seo_keywords || "";
    seoDesc.value = meta.seo_description || "";
    saveBtn.hidden = !can("page.edit");
    pubBtn.hidden = !can("page.publish");
    unpubBtn.hidden = !can("page.publish");
    if (p.page_schema) {
      sf.build(p.page_schema, p.content_json || {}, form, sfOpts);
    } else {
      clear(form);
      form.append(t("cms.editor.noSchema"));
    }
  }

  function back() { showList(); load(); }

  async function openNewDialog() {
    try {
      const types = await data.cmsPageTypes();
      clear(typeSel);
      for (const ty of types) typeSel.append(h("option", { value: ty.type_key }, ty.type_key));
    } catch (e) { say(listError, e); return; }
    newError.hidden = true;
    dialog.showModal();
  }

  async function submitNew(ev) {
    ev.preventDefault();
    try {
      const created = await actions.cmsCreatePage({ type_key: typeSel.value, title: titleIn.value, slug: slugIn.value });
      dialog.close();
      await load();
      openEditor(created.id);
    } catch (err) {
      newError.textContent = err.message + (err.details && err.details.length ? `（${err.details.map((d) => d.pointer).join(", ")}）` : "");
      newError.hidden = false;
    }
  }

  async function save() {
    const p = editing;
    edError.hidden = true;
    try {
      const saved = await actions.cmsSavePage(p.id, {
        content: sf.collect(form),
        meta: { seo_title: seoTitle.value, seo_keywords: seoKeywords.value, seo_description: seoDesc.value },
      });
      // Rebuild from what the server actually stored (rich text is sanitized on write).
      if (p.page_schema && saved && saved.content_json) {
        p.content_json = saved.content_json;
        sf.build(p.page_schema, saved.content_json, form, sfOpts);
      }
      edStatus.textContent = t("cms.editor.saved");
    } catch (err) {
      sf.showValidationErrors(form, edError, err);
    }
  }

  async function publishPage() {
    try {
      await actions.cmsPublishPage(editing.id);
      edStatus.textContent = t("cms.editor.publishedOk");
    } catch (err) {
      sf.showValidationErrors(form, edError, err);
    }
  }

  async function unpublishPage() {
    try {
      await actions.cmsUnpublishPage(editing.id);
      edStatus.textContent = t("cms.editor.unpublishedOk");
    } catch (err) { say(edError, err); }
  }

  await load();
}
