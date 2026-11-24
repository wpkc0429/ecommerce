# 平台方向與步驟

> 這份文件說明**平台往哪裡走**與**依什麼順序走**；決策的理由與取捨在
> [`docs/decisions/0001-headless-platform-and-merchant-template.md`](decisions/0001-headless-platform-and-merchant-template.md)。

## 方向（一句話）

平台是 **headless 商務 API ＋ 平台後台 ＋ 平台官網**。每家商家的店面與後台由 merchant template repo
（`C:/Users/ks/Projects/merchant-template`）建立，並由 AI agent 只改各站的設計層來重新設計；
商家**不使用** ecommerce 託管的店面。商家用後台表單編輯的小東西（文字、banner、活動文案、促銷圖片、店家資訊、FAQ）存在 ecommerce 的
**headless 內容儲存**（由 CMS 的內容功能改造而來），不寫進商家站台的程式碼。
**已移除的只有**：託管渲染（`web/` 的 Next.js 租戶渲染店面）、主題與主題系統、site／網域路由（Host 解析）——
**使用者核准直接移除**（2026-11-24，無棄用期），已於 2026-11-25 完成（change `retire-hosted-storefront-and-themes`，v1 例外見 [ADR 0002](decisions/0002-v1-exemption-retire-hosted-storefront-and-themes.md)）。CMS 內容功能**不是**移除對象（見 ADR 0001 的「修正」）。

**放置規則（使用者決定，2026-11-24）**：需要後端運算或伺服器端狀態的東西，歸 ecommerce（API 與平台後台）；很少改動的內容，放在站台自己的 repo（官網 repo，或商家店面 repo 的設計層）；商家或操作員經常用表單編輯的內容，放在 ecommerce 的內容儲存（`headless-content-store`）。
具體到平台官網：介紹頁、範本展示、價格文字、條款與隱私文字與文案，**放在官網 repo 作為靜態內容**（不依賴 CMS、不在執行期抓取）；申請表單的送出、防濫用驗證、儲存、審核與核准、通知與稽核，**歸 ecommerce**；官網的鎖定核心只呼叫 ecommerce 的公開申請端點，**官網 repo 不需要自己的後端**。

## 步驟

| 步驟 | 做什麼 | 對應文件 | 狀態 |
| --- | --- | --- | --- |
| 1 | 記錄決策、標記棄用、更新 `merchant-onboarding` skill、寫好後續 change | [ADR 0001](decisions/0001-headless-platform-and-merchant-template.md)、本文件 | 完成（只有文件，沒有行為變更） |
| 2 | 官網與商家開店申請流程（公開端點、驗證信箱、後台審核、核准即呼叫既有開店 API）。**已決定：零建置靜態站、獨立 repo、採 merchant template 的鎖定核心＋設計層模式**；官網文案與頁面是 repo 內的靜態內容，ecommerce 負責申請的全部後端 | OpenSpec change [`platform-website`](../openspec/changes/platform-website/proposal.md)（**A 部分〔ecommerce 側：公開申請與驗證端點、`shop_applications`、後台審核與核准、通知 outbox、保存與清除〕已實作（2026-11-25）**；B 部分〔官網 repo〕尚未建立，需要使用者決定網域、託管、Turnstile 帳號、條款文字與腳手架來源；運維手冊見 [`docs/deploy/platform-website-api-notes.md`](deploy/platform-website-api-notes.md)） | A 完成、B 待使用者 |
| 3 | 把 CMS 內容功能改造成 headless 內容儲存：每店 schema、穩定 `key`、草稿／發佈、版本歷史、`content.*` 權限、發佈通知、公開讀取 API；加上 merchant template 的後台內容功能（後續工作） | OpenSpec change [`headless-content-store`](../openspec/changes/headless-content-store/proposal.md)（**提案，未實作**；含需要使用者回答的開放問題） | 提案 |
| 4 | 退役託管店面與主題：主題端點／表／seed／權限、site／網域路由與 Host 後備、`web/` 的租戶渲染程式碼、相關 e2e 與 CI。**第一階段先交付每店 schema 儲存**（頁面 schema 今天來自主題） | OpenSpec change [`retire-hosted-storefront-and-themes`](../openspec/changes/retire-hosted-storefront-and-themes/proposal.md)（**已實作 2026-11-25**；直接移除、無棄用期；v1 例外見 ADR 0002） | 完成（資料遷移待在正式資料庫套用） |

## 現在可以依賴什麼

- 新商家一律走 headless：`merchant-onboarding` skill（`.claude/skills/merchant-onboarding/`）。
- 自帶前端的接入契約：[`docs/storefront-integration.md`](storefront-integration.md) ＋ `api/openapi/openapi.yaml`。
- CMS 內容端點（`/admin/shops/{id}/pages*`、`/content`、`/render/*`）目前**照常運作**，並將由步驟 3 改造；改造期間相容。
- 主題與 site 的 API 已以 ADR 0002 的一次性例外移除（60 行鎖檔行，記錄在 `api/openapi/v1-removed.txt`），其他任何移除仍受鎖保護。

## 順序上的注意事項

- 步驟 4 的主題退役要求頁面 schema 先離開主題：今天頁面與全域內容的 JSON Schema 來自 `theme_pages.page_schema`／`themes.config_schema`，渲染 bundle 也要查主題。**解法**：步驟 4 的**第一階段**就交付 `headless-content-store` 的第一個切片（`content_schemas` 每店 schema 儲存，附資料遷移與讀取端切換），之後才刪主題；不需要等步驟 3 的其餘部分。
- 官網在獨立 repo，`web/` 的退役與官網建置無關。
- 步驟 4 前必須先有：資料備份與回滾演練（階段 5 DROP 之前）；v1 例外已由使用者決定。
