// Login screen. ctx.route.state.email prefills the email (the password then gets focus); state.notice, when non-empty, is
// shown as a hint (a copy key such as "login.notice.expired", or plain text).
export default function login(root, ctx) {
  const { h } = ctx.ui;
  const t = ctx.t;
  const { email = "", notice = "" } = ctx.route.state;

  const emailInput = h("input", { type: "email", id: "login-email", required: true, autocomplete: "username" });
  const passwordInput = h("input", { type: "password", id: "login-password", required: true, autocomplete: "current-password" });
  const error = h("p", { id: "login-error", class: "error", hidden: true }, t("login.error"));
  emailInput.value = email;

  const form = h("form", { id: "login-form",
    onsubmit: async (e) => {
      e.preventDefault();
      error.hidden = true;
      try {
        await ctx.auth.login(emailInput.value, passwordInput.value);
      } catch {
        error.hidden = false;
      }
    } },
    h("label", {}, t("login.email") + " ", emailInput),
    h("label", {}, t("login.password") + " ", passwordInput),
    h("button", { type: "submit", class: "primary" }, t("login.submit")),
    error);

  const noticeText = notice ? (/^login\.notice\./.test(notice) ? t(notice) : notice) : "";
  root.append(h("section", { id: "view-login", class: "view" },
    h("div", { class: "card login-card" },
      h("img", { class: "brand-logo", alt: "", hidden: true }),
      h("h1", {}, t("login.title")),
      noticeText ? h("p", { id: "login-notice", class: "hint" }, noticeText) : null,
      form)));

  // The fragment is not in the document yet: focus once it has been mounted.
  if (email) setTimeout(() => passwordInput.focus(), 0);
}
