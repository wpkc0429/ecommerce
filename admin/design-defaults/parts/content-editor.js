// Content document editor. The form (fields, validation, uploads) is built by the core from the platform's schema:
// this part only lays the screen out and calls the core's actions, which check the permissions, validate again,
// handle the optimistic lock (a stale save asks for a reload) and map server errors to fields.
import { renderSchedulePanel } from "./content-schedule.js";
import { renderPreviewBox } from "./content-preview.js";

export default async function contentEditor(root, ctx, key) {
  const { h, clear } = ctx.ui;
  const { t, can, data, actions, format } = ctx;

  const editable = can("content.edit");
  let doc = null;
  let form = null;
  let seq = 0;

  const title = h("h2", { id: "document-title" }, key);
  const status = h("p", { id: "document-status", class: "hint" });
  const err = h("p", { id: "document-error", class: "error", hidden: true });
  const banners = h("div", { id: "document-banners" });
  const formHost = h("div", { id: "document-form" });
  const scheduleSlot = h("div", { id: "document-schedule-slot" });
  const previewHost = h("div", { id: "document-preview" });
  const saveBtn = h("button", { id: "document-save", class: "primary", hidden: !editable, onclick: (e) => run(e.currentTarget, save) }, t("content.save"));
  const pubBtn = h("button", { id: "document-publish", class: "publish", hidden: !can("content.publish"), onclick: (e) => run(e.currentTarget, publish) }, t("content.publish"));
  const unpubBtn = h("button", { id: "document-unpublish", hidden: true, onclick: (e) => run(e.currentTarget, unpublish) }, t("content.unpublish"));
  const delBtn = h("button", { id: "document-delete", class: "danger", hidden: !can("content.publish"), onclick: (e) => run(e.currentTarget, remove) }, t("content.delete"));
  const previewBtn = h("button", { id: "document-preview-btn", onclick: () => previewHost.replaceChildren(renderPreviewBox(ctx, key)) }, t("content.preview"));
  const revBtn = h("button", { id: "document-revisions-link", onclick: () => ctx.navigate("documents", key, "revisions") }, t("content.revisions"));

  const staleText = h("p", { class: "dlg__msg", "data-testid": "content-stale" }, t("content.stale.text"));
  const staleDialog = h("dialog", { id: "document-stale-dialog", class: "confirm-dialog" },
    h("h3", {}, t("content.stale.title")), staleText,
    h("div", { class: "dialog-actions" },
      h("button", { type: "button", class: "primary", "data-testid": "content-stale-reload", onclick: async () => { staleDialog.close(); await load(); } }, t("content.stale.reload"))));

  root.append(h("div", { id: "tab-document", class: "tab-panel" },
    h("div", { class: "toolbar" }, title,
      h("div", { class: "toolbar-actions" },
        h("button", { id: "document-back", onclick: () => ctx.navigate("documents") }, t("content.back")),
        previewBtn, revBtn, saveBtn, pubBtn, unpubBtn, delBtn)),
    status, err, banners, formHost, previewHost, scheduleSlot, staleDialog));

  const say = (msg) => { err.textContent = msg; err.hidden = !msg; };
  async function run(btn, fn) {
    btn.disabled = true;
    try { await fn(); } finally { btn.disabled = false; }
  }
  const dirty = () => Boolean(form) && JSON.stringify(form.read()) !== JSON.stringify(doc.draft);

  async function load() {
    const my = ++seq;
    say("");
    let d;
    try { d = await data.contentDocument(key); } catch (e) { if (ctx.alive() && my === seq) say(ctx.errorMessage(e)); return; }
    if (!ctx.alive() || my !== seq) return;
    doc = d;
    draw();
  }

  // The form is always rebuilt from the SERVER's response: rich text is rewritten on save.
  function draw() {
    clear(formHost);
    clear(banners);
    title.textContent = t("content.titleNamed", { name: doc.title || doc.key });
    status.textContent = `${doc.isPublished ? t("content.status.published") : t("content.status.draft")} ｜ ${t("content.rev.label", { rev: doc.rev })} ｜ ${format.time(doc.updatedAt)}`;
    status.dataset.rev = String(doc.rev);
    unpubBtn.hidden = !(can("content.publish") && doc.isPublished);
    pubBtn.disabled = doc.needsAttention;
    if (doc.needsAttention) banners.append(h("p", { class: "error", "data-testid": "content-banner-attention" }, t("content.banner.attention")));
    if (!doc.declared) banners.append(h("p", { class: "hint" }, t("content.banner.orphan")));
    if (!editable) banners.append(h("p", { class: "hint", "data-testid": "content-readonly" }, t("content.banner.readOnly")));
    if (!doc.schemaNode) { form = null; formHost.append(h("p", { class: "hint" }, t("cf.err.noType"))); } else {
      form = ctx.ui.contentForm(doc.schemaNode, doc.draft, { readOnly: !editable });
      formHost.append(form.el);
    }
    refreshSchedule();
  }

  // The scheduling panel belongs to another group; a failing panel never breaks the editor.
  async function refreshSchedule() {
    try {
      await renderSchedulePanel(ctx, scheduleSlot, { key, draftRev: doc.rev, status: doc.status, canPublish: can("content.publish") });
    } catch { /* optional panel */ }
  }

  async function save() {
    if (!form) return;
    say("");
    const v = form.validate();
    if (!v.ok) { form.focusFirstError(); return; }
    try {
      const r = await actions.saveContentDocument(key, form.read(), { rev: doc.rev });
      doc = r.doc;
      draw();
      status.textContent = `${t("content.saved")} ｜ ${status.textContent}`;
    } catch (e) { fail(e); }
  }

  async function publish() {
    if (dirty()) { say(t("content.dirtyFirst")); return; }
    say("");
    try {
      const r = await actions.publishContentDocument(key, { rev: doc.rev });
      if (r.cancelled) return;
      await load();
      status.textContent = `${t("content.published")} ｜ ${status.textContent}`;
    } catch (e) { fail(e); }
  }

  async function unpublish() {
    say("");
    try {
      const r = await actions.unpublishContentDocument(key);
      if (r.cancelled) return;
      await load();
      status.textContent = `${t("content.unpublished")} ｜ ${status.textContent}`;
    } catch (e) { say(ctx.errorMessage(e)); }
  }

  async function remove() {
    say("");
    try {
      const r = await actions.deleteContentDocument(key);
      if (r.cancelled) return;
      ctx.navigate("documents");
    } catch (e) { say(ctx.errorMessage(e)); }
  }

  function fail(e) {
    if (e.kind === "stale") { staleDialog.showModal(); return; }
    if (e.fields && form) {
      const left = form.setErrors(e.fields);
      say(left.length ? `${e.message} ${left.join(" ")}` : e.message);
      return;
    }
    say(ctx.errorMessage(e));
  }

  await load();
}
