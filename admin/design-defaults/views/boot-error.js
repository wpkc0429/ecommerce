// Shown when the session cannot be restored (server unreachable). ctx.route.state.error holds the message.
export default function bootError(root, ctx) {
  const { h } = ctx.ui;
  root.append(h("section", { id: "view-boot-error", class: "view" },
    h("div", { class: "card login-card" },
      h("h1", {}, ctx.t("bootError.title")),
      h("p", { class: "error" }, ctx.route.state.error || ""),
      h("p", { class: "hint" }, ctx.t("bootError.hint")))));
}
