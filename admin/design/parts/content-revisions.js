// Revision history of one document, with restore. Restoring only puts a version back into the DRAFT (it never publishes):
// the screen says so. The confirmation (with its fixed warning) is the core's.
export default async function contentRevisions(root, ctx, key) {
  const { h, clear } = ctx.ui;
  const { t, can, data, actions, format } = ctx;
  let seq = 0;

  const tbody = h("tbody", { id: "revisions-tbody" });
  const err = h("p", { id: "revisions-error", class: "error", hidden: true });
  const notice = h("p", { id: "revisions-notice", class: "hint", role: "status", hidden: true });
  const viewBody = h("div", { id: "revision-view-body" });
  const viewTitle = h("h3", {});
  const viewDialog = h("dialog", { id: "revision-view-dialog" }, viewTitle, viewBody,
    h("div", { class: "dialog-actions" }, h("button", { type: "button", onclick: () => viewDialog.close() }, t("content.rev.close"))));

  root.append(h("div", { id: "tab-document-revisions", class: "tab-panel" },
    h("div", { class: "toolbar" }, h("h2", {}, `${t("content.rev.title")} ｜ ${key}`),
      h("div", { class: "toolbar-actions" }, h("button", { id: "revisions-back", onclick: () => ctx.navigate("documents", key) }, t("content.rev.openEditor")))),
    h("p", { class: "hint" }, t("content.rev.intro")),
    err, notice,
    h("table", { class: "table" },
      h("thead", {}, h("tr", {},
        h("th", {}, t("content.rev.col.rev")), h("th", {}, t("content.rev.col.kind")), h("th", {}, t("content.rev.col.schema")),
        h("th", {}, t("content.rev.col.when")), h("th", {}, t("content.col.actions")))),
      tbody),
    viewDialog));

  async function load() {
    const my = ++seq;
    let list;
    try { list = await data.contentRevisions(key); } catch (e) { if (ctx.alive() && my === seq) { err.textContent = ctx.errorMessage(e); err.hidden = false; } return; }
    if (!ctx.alive() || my !== seq) return;
    err.hidden = true;
    clear(tbody);
    if (!list.length) { tbody.append(h("tr", {}, h("td", { colspan: "5", class: "empty" }, t("content.rev.empty")))); return; }
    for (const r of list) {
      tbody.append(h("tr", { "data-rev": String(r.rev) },
        h("td", {}, h("strong", {}, `#${r.rev}`)),
        h("td", {}, h("span", { class: r.kind === "publish" ? "badge ok" : "badge warn" }, t(`content.rev.kind.${r.kind}`))),
        h("td", {}, `v${r.schemaVersion}`),
        h("td", {}, format.time(r.createdAt)),
        h("td", {},
          h("button", { "data-testid": "rev-view", onclick: () => view(r) }, t("content.rev.view")),
          can("content.edit") ? h("button", { "data-testid": "rev-restore", onclick: (e) => restore(e.currentTarget, r) }, t("content.rev.restore")) : "")));
    }
  }

  async function view(r) {
    let full;
    let node;
    try { [full, node] = await Promise.all([data.contentRevision(key, r.rev), data.contentDocument(key)]); } catch (e) { err.textContent = ctx.errorMessage(e); err.hidden = false; return; }
    viewTitle.textContent = t("content.rev.viewTitle", { rev: r.rev });
    clear(viewBody).append(node.schemaNode
      ? ctx.ui.contentForm(node.schemaNode, full.content, { readOnly: true }).el
      : h("p", { class: "hint" }, t("cf.err.noType")));
    viewDialog.showModal();
  }

  async function restore(btn, r) {
    btn.disabled = true;
    err.hidden = true;
    try {
      const res = await actions.restoreContentRevision(key, r.rev);
      if (res.cancelled) return;
      notice.replaceChildren(res.restoredValid ? t("content.rev.restored") : t("content.rev.restoredInvalid"), " ",
        h("a", { href: `#/documents/${key}`, "data-testid": "content-restored-edit" }, t("content.rev.openEditor")));
      notice.hidden = false;
    } catch (e) {
      err.textContent = ctx.errorMessage(e);
      err.hidden = false;
    } finally { btn.disabled = false; }
  }

  await load();
}
