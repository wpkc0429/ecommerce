// Group "members": routes, navigation and fixed warnings (pure data). See lib/meta/index.js for the format.
export default {
  routes: [
    { id: "memberTiers", name: "member-tiers", perm: ["member_tier.view"], nav: "member-tiers" },
    { id: "members", name: "members", perm: ["member.view"], nav: "members" },
    { id: "points", name: "points", perm: ["point.view"], nav: "points" },
    { id: "reports", name: "reports", perm: ["report.view"], nav: "reports" },
  ],
  nav: [
    { key: "member-tiers", copy: "nav.member-tiers", order: 70, perm: ["member_tier.view"] },
    { key: "members", copy: "nav.members", order: 80, perm: ["member.view"] },
    { key: "points", copy: "nav.points", order: 90, perm: ["point.view"] },
    { key: "reports", copy: "nav.reports", order: 100, perm: ["report.view"] },
  ],
  warned: {
    deleteMemberTier: "目前屬於此等級的會員會在下次點數異動時重新歸級，之後的訂單不再享有此折扣。已成立的訂單金額不受影響。",
    changeTimezone: "變更會改變所有歷史報表的日界線——同一段期間的數字會跟著改變。",
  },
};
