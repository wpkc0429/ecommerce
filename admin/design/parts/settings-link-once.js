// Shows a one-time link inside `host` (shown once, never stored). delivery "email" means there is nothing to show.
export function showLinkOnce(ctx, host, { title, link }) {
  const { h, clear } = ctx.ui;
  const { t } = ctx;
  clear(host);
  host.hidden = false;
  host.append(h("h3", {}, title));
  if (link.delivery !== "manual" || !link.url) {
    host.append(h("p", {}, t("team.link.emailed")));
    return;
  }
  const hours = Math.max(1, Math.round((new Date(link.expiresAt) - Date.now()) / 3600000));
  const input = h("input", { type: "text", readonly: "", value: link.url, class: "link-once" });
  const copy = h("button", { type: "button", onclick: async () => {
    if (await ctx.ui.copyText(link.url)) copy.textContent = t("team.link.copied"); else input.select();
  } }, t("team.link.copy"));
  host.append(h("p", { class: "hint" }, t("team.link.hint", { hours })), input, copy);
}
