// Group "publishing": scheduled publishing overview and the publish hook (stage C part 2). Pure data, see lib/meta/index.js.
// The scheduling panel of the document editor is a part (design-defaults/parts/content-schedule.js); it has no route of its own.
export default {
  routes: [
    { id: "contentPublishing", name: "content-publishing", permAny: ["content.publish", "content.manage"], nav: "content-publishing" },
  ],
  nav: [
    { key: "content-publishing", copy: "nav.content-publishing", order: 27, permAny: ["content.publish", "content.manage"] },
  ],
  warned: {
    deletePublishHook: "刪除後 hook 網址即被移除且無法復原，要再使用必須重新填入完整網址。",
    rotateHookSecret: "舊的簽章 secret 會立即失效，接收端必須改用新的 secret，否則之後的呼叫都會驗證失敗。",
    cancelSchedule: "取消後這份文件不會在原定時間自動發佈，要發佈必須重新排程。",
    fireHook: "這會立即觸發商家網站的真實重建，並佔用今天的呼叫上限（上限用完前無法撤回這次呼叫）。",
  },
};
