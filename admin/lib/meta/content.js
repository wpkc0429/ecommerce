// Group "content": the content-store document screens (stage C part 1): documents list / editor / revisions at
// `#/documents[/<key>[/revisions]]` (one route, the view switches on ctx.route.params) and the schema status screen.
// The pre-split global-content editor (`content`, cms group) is a different route and is untouched.
// Fixed warnings (adapted from merchant-template v0.3.0 core/warnings.js to this API; {name} placeholders are filled by the core).
export default {
  routes: [
    { id: "documents", name: "documents", perm: ["content.view"], nav: "documents" },
    { id: "contentSchema", name: "content-schema", perm: ["content.manage"], nav: "content-schema" },
  ],
  nav: [
    { key: "documents", copy: "nav.documents", order: 25, perm: ["content.view"] },
    { key: "content-schema", copy: "nav.content-schema", order: 26, perm: ["content.manage"] },
  ],
  warned: {
    publishContent: "發佈後，這份內容會出現在商家站台上（瀏覽器快取最多再延遲約 1 分鐘）。",
    unpublishContent: "下架後，商家站台這一塊會改回預設文案；草稿與版本歷史仍會保留。",
    deleteContent: "這份內容與它的版本歷史會被永久刪除，無法復原。",
    restoreContent: "只會把這個版本放回草稿，不會發佈；目前的草稿內容會被取代。",
    registerContentSchema: "內容結構會改成這份檔案的版本，之後所有內容文件都依它驗證。這是店主的操作，不能由 AI 或自動化代為進行。",
    registerContentSchemaRemovals: "這份檔案會讓上面列出的欄位值不再顯示，或讓文件失去類型。值會留在版本快照裡，但商家站台不會再讀到它們。確認後才會送出。",
  },
};
