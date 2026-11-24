# 平台方向與步驟

> 這份文件說明**平台往哪裡走**與**依什麼順序走**；決策的理由與取捨在
> [`docs/decisions/0001-headless-platform-and-merchant-template.md`](decisions/0001-headless-platform-and-merchant-template.md)。

## 方向（一句話）

平台是 **headless 商務 API ＋ 平台後台 ＋ 平台官網**。每家商家的店面與後台由 merchant template repo
（`C:/Users/ks/Projects/merchant-template`）建立，並由 AI agent 只改各站的設計層來重新設計；
商家**不使用** ecommerce 託管的店面。商家用後台表單編輯的小東西（文字、banner、活動文案、促銷圖片、店家資訊、FAQ）存在 ecommerce 的
**headless 內容儲存**（由 CMS 的內容功能改造而來），不寫進商家站台的程式碼。
**已棄用的只有**：託管渲染（`web/` 的 Next.js 租戶渲染店面）、主題與主題系統、site／網域路由（Host 解析）——
**目前都還在、還能跑，只是不再演進**。CMS 內容功能**不是**棄用對象（見 ADR 0001 的「修正」）。

## 步驟

| 步驟 | 做什麼 | 對應文件 | 狀態 |
| --- | --- | --- | --- |
| 1 | 記錄決策、標記棄用、更新 `merchant-onboarding` skill、寫好後續 change | [ADR 0001](decisions/0001-headless-platform-and-merchant-template.md)、本文件 | 完成（只有文件，沒有行為變更） |
| 2 | 官網與商家開店申請流程（公開端點、驗證信箱、後台審核、核准即呼叫既有開店 API）；建議官網放獨立 repo、採 merchant template 的零建置設計層模式 | OpenSpec change [`platform-website`](../openspec/changes/platform-website/proposal.md)（**提案，待核准**；含需要使用者回答的開放問題） | 待核准 |
| 3 | 把 CMS 內容功能改造成 headless 內容儲存：每店 schema、穩定 `key`、草稿／發佈、版本歷史、`content.*` 權限、發佈通知、公開讀取 API；加上 merchant template 的後台內容功能（後續工作） | OpenSpec change [`headless-content-store`](../openspec/changes/headless-content-store/proposal.md)（**提案，未實作**；含需要使用者回答的開放問題） | 提案 |
| 4 | 退役託管店面與主題：主題端點／表／seed／權限、site／網域路由與 Host 後備、`web/` 的租戶渲染程式碼、相關 e2e 與 CI | OpenSpec change [`retire-hosted-storefront-and-themes`](../openspec/changes/retire-hosted-storefront-and-themes/proposal.md)（**草稿，不得實作**，需使用者另行明確核准） | 草稿 |

## 現在可以依賴什麼

- 新商家一律走 headless：`merchant-onboarding` skill（`.claude/skills/merchant-onboarding/`）。
- 自帶前端的接入契約：[`docs/storefront-integration.md`](storefront-integration.md) ＋ `api/openapi/openapi.yaml`。
- CMS 內容端點（`/admin/shops/{id}/pages*`、`/content`、`/render/*`）目前**照常運作**，並將由步驟 3 改造；改造期間相容。
- 棄用的 API（主題、site）仍受 v1 表面鎖保護（`api/openapi/v1-surface.txt`）：在步驟 4 被核准、且決定了 v1 破例或 `/api/v2` 之前，**它們不會消失**。

## 順序上的注意事項

- 步驟 4 的主題退役**必須在步驟 3 的 schema 所有權轉移之後**：今天頁面與全域內容的 JSON Schema 來自 `theme_pages.page_schema`／`themes.config_schema`，
  渲染 bundle 也要查主題；沒有每店 schema 就不能刪主題。
- 官網若放獨立 repo，`web/` 的退役與官網建置無關；若留在 `web/`，Next.js 租戶渲染路由與靜態輸出互斥，步驟 2 會帶走 `web/` 內的租戶渲染程式碼
  （細節與替代方案見 `platform-website/design.md`）。
- 步驟 4 前必須先有：棄用期與 `Sunset` 標頭、資料備份與回滾演練、使用者對 v1 表面鎖的明確處置。
