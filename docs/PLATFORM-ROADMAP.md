# 平台方向與步驟

> 這份文件說明**平台往哪裡走**與**依什麼順序走**；決策的理由與取捨在
> [`docs/decisions/0001-headless-platform-and-merchant-template.md`](decisions/0001-headless-platform-and-merchant-template.md)。

## 方向（一句話）

平台是 **headless 商務 API ＋ 平台後台 ＋ 平台官網**。每家商家的店面與後台由 merchant template repo
（`C:\Users\ks\Projects\merchant-template`）建立，並由 AI agent 只改各站的設計層來重新設計；
商家**不使用** ecommerce 託管的店面。因此 ecommerce 自己的租戶渲染（CMS 頁面、主題、渲染 API、site／網域對應、`web/` 現況）**作為產品已棄用**，
但**目前都還在、還能跑，只是不再演進**。

## 步驟

| 步驟 | 做什麼 | 對應文件 | 狀態 |
| --- | --- | --- | --- |
| 1 | 記錄決策、標記棄用、更新 `merchant-onboarding` skill、寫好後續兩個 change | [ADR 0001](decisions/0001-headless-platform-and-merchant-template.md)、本文件 | 完成（只有文件，沒有行為變更） |
| 2 | `web/` 改造成平台官網，加上商家開店申請流程（公開端點、驗證信箱、後台審核、核准即呼叫既有開店 API） | OpenSpec change [`platform-website`](../openspec/changes/platform-website/proposal.md)（**提案，待核准**；含需要使用者回答的開放問題） | 待核准 |
| 3 | 退役租戶渲染功能：端點、表、seed、admin 視圖、`web/` 殘留、e2e、CI | OpenSpec change [`retire-tenant-rendering`](../openspec/changes/retire-tenant-rendering/proposal.md)（**草稿，不得實作**，需使用者另行明確核准） | 草稿 |

## 現在可以依賴什麼

- 新商家一律走 headless：`merchant-onboarding` skill（`.claude/skills/merchant-onboarding/`）。
- 自帶前端的接入契約：[`docs/storefront-integration.md`](storefront-integration.md) ＋ `api/openapi/openapi.yaml`。
- 棄用的 API 仍受 v1 表面鎖保護（`api/openapi/v1-surface.txt`）：在步驟 3 被核准、且決定了 v1 破例或 `/api/v2` 之前，**它們不會消失**。

## 順序上的注意事項

- 步驟 2 不依賴步驟 3 的 API 移除，但 `web/` 的 Next.js 租戶渲染路由與官網的靜態輸出互斥，所以步驟 2 會帶走 `web/` 內的租戶渲染程式碼
  （細節與替代方案見 `platform-website/design.md`）。
- 步驟 3 前必須先有：棄用期與 `Sunset` 標頭（讓任何隱藏的消費者有時間反應）、資料備份與回滾演練、使用者對 v1 表面鎖的明確處置。
