// Shared pieces of the platform views. Uses only what the view passes in (ctx).

/** The `.pager` block of a list: ids `{prefix}-prev|pageinfo|next` as in the old panels. */
export function pager(ctx, prefix, { onPrev, onNext }) {
  const { h } = ctx.ui;
  const prev = h("button", { id: `${prefix}-prev`, onclick: onPrev }, ctx.t("platform.pager.prev"));
  const info = h("span", { id: `${prefix}-pageinfo` });
  const next = h("button", { id: `${prefix}-next`, onclick: onNext }, ctx.t("platform.pager.next"));
  return {
    node: h("div", { class: "pager" }, prev, info, next),
    update(page, total, pageSize) {
      const pages = Math.max(1, Math.ceil((total || 0) / pageSize));
      info.textContent = ctx.t("common.pageInfo", { page, pages, total: total || 0 });
      prev.disabled = page <= 1;
      next.disabled = page >= pages;
    },
  };
}
