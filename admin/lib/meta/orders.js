// Group "orders": routes, navigation and fixed warnings (pure data). See lib/meta/index.js for the format.
export default {
  routes: [
    { id: "orders", name: "orders", perm: ["order.view"], nav: "orders" },
    { id: "shippingMethods", name: "shipping-methods", perm: ["shipping_method.view"], nav: "shipping-methods" },
  ],
  nav: [
    { key: "orders", copy: "nav.orders", order: 30, perm: ["order.view"] },
    { key: "shipping-methods", copy: "nav.shipping-methods", order: 60, perm: ["shipping_method.view"] },
  ],
  warned: {
    cancelOrder: "庫存會歸還，已核發的點數會被扣回。已付款或已出貨的訂單無法取消。",
    shipDelivered: "已送達是終態，無法復原。",
    shipReturned: "此訂單已核發的點數會被全額扣回（即使這箱只裝了部分品項），且此訂單之後無法再建立任何出貨。退貨是終態，無法復原。",
    deleteShippingMethod: "已使用它的既有訂單不受影響（運費與名稱在結帳時已快照）。",
  },
};
