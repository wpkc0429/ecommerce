// Group "platform": routes, navigation and fixed warnings (pure data). See lib/meta/index.js for the format.
export default {
  routes: [
    { id: "platformShops", name: "platform-shops", platformPerm: "shop.list", nav: "platform-shops" },
    { id: "platformAccounts", name: "platform-accounts", platformPerm: "account.view", nav: "platform-accounts" },
    { id: "platformApplications", name: "platform-applications", platformPerm: "shop_application.view", nav: "platform-applications" },
  ],
  nav: [
    { key: "platform-shops", copy: "nav.platform-shops", order: 130, platformPerm: "shop.list" },
    { key: "platform-accounts", copy: "nav.platform-accounts", order: 140, platformPerm: "account.view" },
    { key: "platform-applications", copy: "nav.platform-applications", order: 150, platformPerm: "shop_application.view" },
  ],
  warned: {
    disableAccount: "其已登入的工作階段會立即失效。",
    removeMembership: "該店的角色指派一併移除。",
    confirmApplicantContact: "此動作會記入稽核。",
  },
};
