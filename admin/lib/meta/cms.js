// Group "cms": routes, navigation and fixed warnings (pure data). See lib/meta/index.js for the format.
// The page editor (old transient `editor` panel) lives INSIDE the `pages` view, so it has no route of its own.
export default {
  routes: [
    { id: "pages", name: "pages", perm: ["page.view"], nav: "pages" },
    { id: "content", name: "content", perm: ["shop.view"], nav: "content" },
  ],
  nav: [
    { key: "pages", copy: "nav.pages", order: 10, perm: ["page.view"] },
    { key: "content", copy: "nav.content", order: 20, perm: ["shop.view"] },
  ],
  warned: {
    deletePage: "刪除後，這個頁面將不再顯示於商家站台。",
  },
};
