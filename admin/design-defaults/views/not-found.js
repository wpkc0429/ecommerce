// Unknown address.
export default function notFound(root, ctx) {
  root.append(ctx.ui.h("p", { class: "error" }, ctx.t("notFound.message")));
}
