// Group "auth": the screens that exist outside the permission-gated app, plus the shell and the fallback screens.
export default {
  routes: [
    { id: "login", name: "login", public: true },
    { id: "setPassword", name: "set-password", public: true },
    { id: "acceptInvite", name: "accept-invite", public: true },
  ],
  nav: [],
  warned: {},
};
