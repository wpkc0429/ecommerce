// Group "catalog": categories and products (the product editor and its image section live INSIDE the products view,
// as the old `product-editor` transient panel did). Pure data: routes, navigation and the FIXED confirmation warnings.
export default {
  routes: [
    { id: "categories", name: "categories", perm: ["category.view"], nav: "categories" },
    { id: "products", name: "products", perm: ["product.view"], nav: "products" },
  ],
  nav: [
    { key: "products", copy: "nav.products", order: 40, perm: ["product.view"] },
    { key: "categories", copy: "nav.categories", order: 50, perm: ["category.view"] },
  ],
  warned: {
    deleteProduct: "其 SKU 會一併刪除。",
    deleteCategory: "刪除後無法復原。",
    deleteImage: "刪除後無法復原。",
  },
};
