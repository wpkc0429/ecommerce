// Group "settings": routes, navigation and fixed warnings (pure data). See lib/meta/index.js for the format.
export default {
  routes: [
    { id: "storefrontClients", name: "storefront-clients", permAny: ["publishable_key.view", "shop.manage_client_origins", "payment_credential.manage"], nav: "storefront-clients" },
    { id: "team", name: "team", perm: ["user.manage_members"], nav: "team" },
  ],
  nav: [
    { key: "storefront-clients", copy: "nav.storefront-clients", order: 110, permAny: ["publishable_key.view", "shop.manage_client_origins", "payment_credential.manage"] },
    { key: "team", copy: "nav.team", order: 120, perm: ["user.manage_members"] },
  ],
  warned: {
    revokeKey: "撤銷即時生效，使用它的前端會立刻收到 404。",
    removeOrigin: "該來源的瀏覽器請求會立刻被 CORS 擋下。",
    disableMember: "他在本店的下一個操作就會被拒絕。這只影響本店：他的帳號與其他商家的權限不受影響。",
  },
};
