# 0003 — 平台官網：零建置靜態站、獨立 repo；申請流程的後端歸 ecommerce

- 狀態：**已決定（2026-11-24／25，使用者決定）；ecommerce 側（A 部分）已實作（2026-11-25）；官網 repo（B 部分）尚未建立**
- 核准者：專案擁有者（使用者）
- 相關：[ADR 0001](0001-headless-platform-and-merchant-template.md)（方向與放置規則）、`openspec/changes/platform-website/`（design D1／D13）、[`docs/deploy/platform-website-api-notes.md`](../deploy/platform-website-api-notes.md)

## 決定

1. **官網是零建置靜態站**（純 HTML／CSS／少量經典 script，沒有框架、沒有 `node_modules`、沒有建置步驟），沿用 merchant template 的「鎖定核心＋設計層」模式、守門員與嚴格 CSP。Next.js `output: "export"` 的方案被否決：官網只有約 9 頁與一個表單，找不到任何需要框架的具體需求，而框架會削弱守門員的靜態掃描並擴大 AI 可弄壞的面積。
2. **官網的程式碼放在獨立 repo，不放 ecommerce，`web/` 不被改造**。理由：AI 編輯官網時 repo 範圍就是邊界（看不到 Go API 與商家資料）；CI 與部署與 API 發佈節奏解耦；`web/` 的退役與官網完全無關。
3. **放置規則的應用**：需要後端運算或伺服器端狀態的東西歸 ecommerce——申請的送出端點、Turnstile 驗證、儲存、信箱驗證、審核與核准（呼叫既有的一次開店服務）、通知（outbox／SMTP）、限流、稽核、個資保存；官網 repo **沒有 server、沒有 serverless function、沒有資料庫、沒有自己的密鑰**（Turnstile secret 只在 ecommerce），它的鎖定核心只呼叫兩個公開端點。
4. **平台後台（`admin/`）日後也應切成鎖定核心與設計層**（使用者原則：每個站台的畫面都可以由 AI 重新設計，只要功能接好）。這是**後續工作（follow-up）**，不在 `platform-website`，也尚未排進任何 change。
5. 通知抽象 `application.Notifier` 以 **outbox 為後備、SMTP 為正式**：上線前必須接上交易式 email／SMTP 服務；在那之前系統完全能運作（操作員代轉驗證連結，或以稽核過的人工確認取代信箱驗證）。

## 結果與代價

- 官網與 API 的契約靠 `openapi.yaml`（含完整範例）與 `docs/deploy/platform-website-api-notes.md` 保持一致；官網核心需要以那些範例做契約測試。
- 多一個 repo 要維護，且需要一份「website 種類」的腳手架（建議在 merchant template 增加 `sites.website`，備案是另開小 template repo；repo 名稱與擁有者待使用者決定）。
- 官網 origin 只經 `WEBSITE_URL` 取得 `/api/v1/public/*` 的 CORS，**不**加進 `CORS_ALLOWED_ORIGINS`。
- 申請者資料有保存期限（已拒絕 90 天、已核准 1 年，**待法務確認**）；`cmd/purge-applications` 需由營運者排程。
- 仍待使用者提供：官網網域與託管、Turnstile 帳號、條款與隱私文字、寄信服務、官網 repo 與腳手架來源。
