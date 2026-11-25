// Shown instead of a screen whose permissions the person does not hold.
export default function noPermission(root, ctx) {
  root.append(ctx.ui.h("p", { class: "error" }, ctx.t("noPermission.message")));
}
