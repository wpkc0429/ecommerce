// Preview token box: asks the core for a short-lived preview token and shows it with a copy button. The admin does not know
// the shop's site URL, so it cannot open the preview itself; the explanation says where the token goes.
export function renderPreviewBox(ctx, key) {
  const { h } = ctx.ui;
  const { t, actions, format } = ctx;
  const out = h("div", { id: "document-preview-result", hidden: true });
  const err = h("p", { class: "error", hidden: true });
  const scope = h("select", { id: "document-preview-scope" },
    h("option", { value: "one" }, t("content.preview.scopeOne")),
    h("option", { value: "shop" }, t("content.preview.scopeShop")));
  const go = h("button", { id: "document-preview-create", class: "primary", onclick: create }, t("content.preview.create"));
  const box = h("div", { class: "seo-box", id: "document-preview-box" },
    h("strong", {}, t("content.preview.title")),
    h("p", { class: "hint" }, t("content.preview.help")),
    h("label", {}, `${t("content.preview.scope")} `, scope), h("div", {}, go), err, out);

  async function create() {
    go.disabled = true;
    err.hidden = true;
    try {
      const r = await actions.createContentPreviewToken(key, { scope: scope.value });
      const field = h("input", { type: "text", id: "document-preview-token", class: "mono", readonly: true, value: r.token });
      const status = h("span", { class: "hint" });
      const copy = h("button", {
        id: "document-preview-copy",
        onclick: async () => { status.textContent = (await ctx.ui.copyText(r.token)) ? t("content.preview.copied") : t("content.preview.copyFailed"); },
      }, t("content.preview.copy"));
      out.replaceChildren(field, copy, status, h("p", { class: "hint" }, t("content.preview.expires", { time: format.time(r.expiresAt), seconds: r.expiresIn })));
      out.hidden = false;
    } catch (e) {
      err.textContent = ctx.errorMessage(e);
      err.hidden = false;
    } finally { go.disabled = false; }
  }
  return box;
}
