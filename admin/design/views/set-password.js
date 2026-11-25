// Set-password screen (#/set-password/{token}). The link token never reaches this file: ctx.auth holds it.
export default async function setPassword(root, ctx) {
  const { h } = ctx.ui;
  const t = ctx.t;

  const invalidMsg = h("p", { id: "sp-invalid", class: "error", hidden: true }, t("setPassword.invalid"));
  const emailEl = h("strong", { id: "sp-email" });
  const pw = h("input", { type: "password", id: "sp-password", required: true, minlength: "12", autocomplete: "new-password" });
  const pw2 = h("input", { type: "password", id: "sp-password2", required: true, minlength: "12", autocomplete: "new-password" });
  const errorEl = h("p", { id: "sp-error", class: "error", hidden: true });
  const showError = (msg) => { errorEl.textContent = msg; errorEl.hidden = false; };
  const form = h("form", { id: "sp-form", hidden: true,
    onsubmit: async (e) => {
      e.preventDefault();
      errorEl.hidden = true;
      const res = await ctx.auth.passwordSetup.submit(pw.value, pw2.value);
      if (res.status === "invalid") { form.hidden = true; invalidMsg.hidden = false; return; }
      if (res.status === "error") { showError(res.message); return; }
      pw.value = ""; pw2.value = "";
    } },
    h("p", {}, t("setPassword.account"), emailEl),
    h("label", {}, t("setPassword.password"), pw),
    h("label", {}, t("setPassword.confirm"), pw2),
    h("button", { type: "submit", class: "primary" }, t("setPassword.submit")),
    errorEl);

  root.append(h("section", { id: "view-set-password", class: "view" },
    h("div", { class: "card login-card" },
      h("h1", {}, t("setPassword.title")),
      invalidMsg,
      form,
      h("p", {}, h("a", { href: "#/", id: "sp-to-login", onclick: (e) => { e.preventDefault(); ctx.auth.goLogin(""); } }, t("setPassword.toLogin"))))));

  const info = await ctx.auth.passwordSetup.inspect();
  if (!ctx.alive()) return;
  if (!info.valid) { invalidMsg.hidden = false; return; }
  emailEl.textContent = info.email;
  form.hidden = false;
}
