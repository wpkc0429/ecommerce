// Accept-invite screen (#/accept-invite/{token}). The link token never reaches this file: ctx.auth holds it.
export default async function acceptInvite(root, ctx) {
  const { h } = ctx.ui;
  const t = ctx.t;
  const inv = ctx.auth.invitation;

  const invalidMsg = h("p", { id: "ai-invalid", class: "error", hidden: true }, t("acceptInvite.invalid"));
  const shopEl = h("strong", { id: "ai-shop" });
  const roleEl = h("strong", { id: "ai-role" });
  const emailEl = h("strong", { id: "ai-email" });
  const info = h("div", { id: "ai-info", hidden: true },
    h("p", {}, t("acceptInvite.shop"), shopEl),
    h("p", {}, t("acceptInvite.role"), roleEl),
    h("p", {}, t("acceptInvite.email"), emailEl));
  const errorEl = h("p", { id: "ai-error", class: "error", hidden: true });
  const showError = (msg) => { errorEl.textContent = msg || ""; errorEl.hidden = !msg; };

  const pw = h("input", { type: "password", id: "ai-password", required: true, minlength: "12", autocomplete: "new-password" });
  const pw2 = h("input", { type: "password", id: "ai-password2", required: true, minlength: "12", autocomplete: "new-password" });
  const loginPw = h("input", { type: "password", id: "ai-login-password", required: true, autocomplete: "current-password" });
  const loginEmail = h("strong", { id: "ai-login-email" });
  const switchEmail = h("strong", { id: "ai-switch-email" });

  const newForm = h("form", { id: "ai-new-form", hidden: true,
    onsubmit: async (e) => {
      e.preventDefault();
      showError("");
      const res = await inv.acceptNew(pw.value, pw2.value);
      if (res.status === "invalid") return only("#ai-invalid");
      if (res.status === "retry") return load();
      if (res.status === "error") return showError(res.message);
      pw.value = ""; pw2.value = "";
    } },
    h("p", { class: "hint" }, t("acceptInvite.newHint")),
    h("label", {}, t("acceptInvite.password"), pw),
    h("label", {}, t("acceptInvite.confirm"), pw2),
    h("button", { type: "submit", class: "primary" }, t("acceptInvite.newSubmit")));

  // Result of "accept as the signed-in account" (shared by the login form and the switch button).
  const joined = (res) => {
    if (res.status === "invalid") return only("#ai-invalid");
    if (res.status === "error") return showError(res.message);
    loginPw.value = "";
  };

  const loginForm = h("form", { id: "ai-login-form", hidden: true,
    onsubmit: async (e) => {
      e.preventDefault();
      showError("");
      joined(await inv.loginAndJoin(loginPw.value));
    } },
    h("p", { class: "hint" }, t("acceptInvite.loginHint")),
    h("p", {}, t("acceptInvite.account"), loginEmail),
    h("label", {}, t("acceptInvite.loginPassword"), loginPw),
    h("button", { type: "submit", class: "primary" }, t("acceptInvite.loginSubmit")));

  const switchBox = h("div", { id: "ai-switch", hidden: true },
    h("p", {}, t("acceptInvite.current"), switchEmail),
    h("button", { type: "button", id: "ai-switch-btn", class: "primary",
      onclick: async () => { showError(""); joined(await inv.acceptAsCurrent()); } }, t("acceptInvite.accept")));

  const parts = { "#ai-invalid": invalidMsg, "#ai-new-form": newForm, "#ai-login-form": loginForm, "#ai-switch": switchBox, "#ai-info": info };
  function only(...ids) { for (const [sel, el] of Object.entries(parts)) el.hidden = !ids.includes(sel); }

  root.append(h("section", { id: "view-accept-invite", class: "view" },
    h("div", { class: "card login-card" },
      h("h1", {}, t("acceptInvite.title")),
      invalidMsg, info, newForm, loginForm, switchBox, errorEl,
      h("p", {}, h("a", { href: "#/", id: "ai-to-login", onclick: (e) => { e.preventDefault(); ctx.auth.goLogin(""); } }, t("acceptInvite.toLogin"))))));
  only();

  async function load() {
    showError("");
    only();
    for (const el of [pw, pw2, loginPw]) el.value = "";
    const r = await inv.inspect();
    if (!ctx.alive()) return;
    if (r.status === "invalid") return only("#ai-invalid");
    shopEl.textContent = r.shop;
    roleEl.textContent = r.role;
    emailEl.textContent = r.email;
    if (r.status === "new") return only("#ai-info", "#ai-new-form");
    if (r.status === "switch") { switchEmail.textContent = r.currentEmail; return only("#ai-info", "#ai-switch"); }
    loginEmail.textContent = r.email;
    only("#ai-info", "#ai-login-form");
  }
  await load();
}
