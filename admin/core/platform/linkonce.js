// The one-time link box (setup / reset URLs). CORE-owned: the URL exists only in the DOM of `host`; nothing is stored,
// logged or kept beyond this call. Re-rendering the host (reload, other page, another action) removes it.
import { h, clear } from "../../ui/kit.js";
import { t } from "../../lib/design.js";

/** Shows `link` once inside host. delivery !== "manual" (or no URL) -> only the "sent by the system" note. */
export function showLinkOnce(host, { title, link }) {
  clear(host);
  host.hidden = false;
  host.append(h("h3", {}, title));
  const url = link.setup_url || link.reset_url || link.invite_url;
  if (link.delivery !== "manual" || !url) {
    host.append(h("p", {}, t("platform.link.sentByMail")));
    return;
  }
  const hours = Math.max(1, Math.round((new Date(link.expires_at) - Date.now()) / 3600000));
  const input = h("input", { type: "text", readonly: "", value: url, class: "link-once" });
  const copy = h("button", { type: "button" }, t("platform.link.copy"));
  copy.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(url);
      copy.textContent = t("platform.link.copied");
    } catch {
      input.select();
    }
  });
  host.append(h("p", { class: "hint" }, t("platform.link.hint", { hours })), input, copy);
}
