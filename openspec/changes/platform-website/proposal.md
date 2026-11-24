## Why

ADR `docs/decisions/0001-headless-platform-and-merchant-template.md` 決定：平台是 headless 商務 API ＋ 平台後台 ＋ **平台官網**；商家的店面與後台由 merchant template 建立。官網目前不存在，而新商家目前只能「人工談好、操作員手動開店」。`platform-account-provisioning` 已經留下可被程式呼叫的開店動作（`provision.Service.ProvisionShop`），並在 proposal 寫明後續 phase 是「商家經平台網站申請開店」。本 change 補上它：

1. 一個公開的官網（介紹、運作方式、範本 live demo、價格佔位、條款與隱私佔位）。
2. 一條**開店申請流程**：公開未認證端點 → 驗證信箱 → 平台後台審核（核准／拒絕＋理由）→ 核准時呼叫既有的一次性開店 API 並產生 owner 的密碼設定連結。

注意兩個查證後的事實（影響設計）：

- **`web/` 今天是租戶渲染店面**（Next.js `output: "standalone"`、`app/[[...path]]/page.tsx` 依 Host 渲染）。這與官網的靜態輸出互斥，所以改造官網必然帶走 `web/` 內的租戶渲染程式碼（見 design D1、D9）。
- **「沒有寄信能力」只對一半成立**：`internal/notify` 有 `Sender` 介面與 `SMTPSender`，但只服務訂單事件、正式環境未設定 SMTP（`DiscardSender`）；後台帳號連結走 `account.ManualDelivery`。所以本 change 設計一個通知抽象，預設走**人工後備**，接上 SMTP 時不改呼叫端。

## What Changes

- **官網**（改造 `web/`，靜態輸出）：`/`、`/how-it-works`、`/demo`、`/pricing`（佔位）、`/terms`、`/privacy`（佔位，非法律文字）、`/apply`、`/apply/verify`、`/apply/received`；SEO 基礎（metadata、canonical、OG、`sitemap.xml`、`robots.txt`、結構化資料）、安全標頭、託管。
- **公開申請 API**（新）：`POST /api/v1/public/shop-applications`、`POST /api/v1/public/shop-applications/verify`。未認證；Cloudflare Turnstile（或等價）、honeypot、輸入驗證、per-IP 與 per-email 限流、**不洩漏帳號／申請是否存在**（一律同一個 202）。
- **資料模型**（新表）：`shop_applications`、`shop_application_tokens`（驗證 token，只存雜湊）、`shop_application_events`（稽核）、`shop_application_messages`（通知 outbox，人工後備）。
- **平台後台審核**（新）：`GET /api/v1/admin/shop-applications`、`GET …/{id}`、`POST …/{id}/approve`、`POST …/{id}/reject`、`POST …/{id}/messages/{mid}/mark-sent`；新平台權限節點 `shop_application.view`／`shop_application.review`；admin 新視圖「開店申請」。
- **核准＝呼叫 `provision.Service`**：把 `ProvisionShop` 拆成「交易本體」與「commit 後」兩半（同 `CreateShop`／`CreateShopTx` 的做法），讓申請列狀態與開店同一交易；owner 密碼設定連結沿用 `account.LinkDelivery`（今天 manual）。
- **通知抽象**：`application.Notifier`（人工 outbox 預設；`notify.Sender` 的 SMTP 轉接器於設定 SMTP 時啟用）。
- **隱私與保存**：同意紀錄、保存期限、清除指令 `cmd/purge-applications`（預設 dry run）。
- **`web/` 租戶渲染程式碼移除**（官網的前提，見 design D9）：`app/[[...path]]`、`app/preview`、`themes/`、`lib/richtext-allowlist.json`、`lib/sanitize.ts`、`scripts/gen-types.mjs`、CI 的 gen:types 檢查；e2e 中依賴 `WEB` 的斷言。**API 與 admin 的 CMS 端點／視圖一個都不動**（那是 `retire-tenant-rendering`）。

## Capabilities

### New Capabilities
- `platform-website`：官網頁面、SEO、託管與安全標頭。
- `shop-application`：公開申請端點、驗證信箱、濫用防護、資料模型、核准／拒絕與開店、通知、保存與稽核。
- `admin-application-review`：平台後台審核視圖與權限。

### Modified Capabilities
- 無（新增的平台權限節點寫在 `shop-application`；`account-provisioning` 仍在未封存的 change 內，本 change 只呼叫它）。

## Impact

- **資料**：4 張新表（皆**不含 `shop_id` 欄位**，用 `provisioned_shop_id`，避免被 `enttenancy_internal_test` 要求分類）；`shops` 不改。
- **程式碼**：新 `api/internal/ent/schema/application.go`、`api/internal/application/`、`api/internal/turnstile/`、`api/internal/httpapi/applications.go`＋`applications_public.go`、`router.go`、`ratelimitmw.go`、`config/config.go`、`app/wire.go`、`provision/service.go`（拆分）、`seed/seed.go`（權限）、`cmd/purge-applications`；`admin/views/platform-applications.js`、`admin/app.js`、`admin/index.html`；`web/` 整個改寫；`e2e/`；`.github/workflows/ci.yml` 的 web job（僅此一個 workflow 變動，需明確核准）。
- **API 契約**：新增 ≥ 9 條路由（additive）；`openapi.yaml` 與 `v1-surface.txt` 只新增；`wiring_integration_test` 的 ⑤⑥ 需涵蓋新 Deps；不新增領域事件（檢查 ⑦ 不動）。
- **部署**：官網靜態託管（見 design D7）；`CORS_ALLOWED_ORIGINS` 加官網 origin；新環境變數 `TURNSTILE_*`、`RATE_LIMIT_APPLICATION_*`、`APPLICATION_*`。
- **相依**：`platform-account-provisioning`（已實作，未封存）。不相依 `retire-tenant-rendering`。
- **需使用者回答的開放問題**：見 design「Open Questions」，**在回答前不要開始實作**。
