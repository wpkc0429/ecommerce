# 0001 — 平台定位為 headless 商務 API，商家前端由 merchant template 產生

- 狀態：**已決定**（2026-11-24，由專案擁有者決定；本文件只記錄，不重新討論）
- 範圍：產品方向。本文件本身不改任何行為；實際移除走另案、另行核准（見「階段計畫」）。
- 相關：[`docs/PLATFORM-ROADMAP.md`](../PLATFORM-ROADMAP.md)、OpenSpec change `platform-website`（提案）、`retire-tenant-rendering`（**草稿，未核准**）

## 背景

這個 repo 最初的設計（Phase 1，`openspec/changes/archive/2026-07-18-phase1-multitenant-cms-foundation`）是「平台託管店面」：
多租戶、schema 驅動的 CMS（頁面、頁型、sections）、主題（`themes`／`theme_pages`＝JSON Schema ＋ `web/themes/registry.ts` 的元件庫）、
`sites`／`site_shop` 的網域對應，加上一個 Next.js SSR 店面（`web/`）透過渲染 bundle API（`/api/v1/render/page`）把每個商家畫出來。

之後的實際走向偏離了它：

- 第一個真實商家（ONDO 溫度花室，repo 名 `bouquet`）是**自帶前端**：零建置的靜態站，以 `X-Publishable-Key` 呼叫 `/api/v1/shop/*`，
  完全不碰 `web/`、CMS、主題或 render API（`docs/storefront-integration.md` 是為它寫的）。
- `openspec/project.md` 早已記載「主題模式店面的電商前台尚未實作、**目前零消費者**」。主題模式店面連商品、購物車、結帳都沒有。
- 商家的店面與後台已被抽成範本 repo（`merchant-template`，本機路徑 `C:\Users\ks\Projects\merchant-template`）：
  `public/`（店面）＋ `admin/`（後台）在同一個 repo、零建置、沒有自己的後端。
  `scripts/new-merchant.mjs` 由範本建立某一家商家的 repo；`template.json` 把檔案分成**設計層**（AI 可改：店面的
  `public/app.js content.js styles.css index.html assets/`、後台的 `admin/design/`）與**鎖定核心**（`public/commerce/**`、`admin/lib|core|ui/…`、
  `config.js`、守門員腳本；以 sha256 雜湊清單與 `check-scope`／`check-locked` 保護）。
  AI agent 每次只改一個站的設計層；核心由人經版本與 `scripts/sync-core.mjs` 推送到各商家 repo。

## 決定

1. 平台由三部分組成：**headless 商務 API**（`api/`，唯一資料權威）、**平台後台**（`admin/` 的平台層視圖：開店、帳號、日後的申請審核）、
   **平台官網**（產品介紹、商家範本的 live demo、開店申請表單；由 `web/` 改造，見 `platform-website`）。
2. 每一家商家的**店面與後台都由 merchant template 建立**，並由 AI agent 只改各站的設計層來重新設計。
   商家**不使用**由 ecommerce 託管的店面。headless（`X-Publishable-Key` ＋ `/api/v1/shop/*`）是**唯一支援**的店面路徑。
3. ecommerce 自己的「租戶渲染」功能**作為產品已棄用**：CMS 頁面／頁型／sections、主題與主題系統、渲染 bundle 端點與快取、
   site／網域對應、租戶渲染的 Next.js 店面（`web/` 現況）及其 e2e 與 CI。
4. **現在不移除任何東西。** 移除在之後另一個明確核准的 change（`retire-tenant-rendering`，目前只是草稿）裡進行。

## 後果

**不變（bouquet 與 template 用到的一切，持續維護）**：商品目錄（分類、商品、巢狀 SKU、圖片、公開分類樹）、購物車、訪客與會員結帳、
訂單與出貨、付款（mock／ECPay、每店憑證）、會員（每商家獨立身分、等級、點數、顧客名冊）、圖片（R2）、報表、
後台帳號與開店（`POST /admin/shop-provisioning`）、員工邀請、publishable key 與 allowed origin（含 `purpose=admin`）、
後台對外開放（Cloudflare Access、`PLATFORM_ADMIN_HOSTS`、admin 主機綁定、branding）。

**棄用（保留運作、不再演進、新商家不使用）**：
`/api/v1/admin/shops/{id}/pages*`、`/content`、`/page-types`；`/api/v1/admin/themes*`、`/shops/{id}/theme*`、`/shops/{id}/themes`；
`/api/v1/admin/sites*`；`/api/v1/render/page`、`/api/v1/render/preview`；對應的 ent 表（`themes`、`theme_pages`、`pages`、`sites`、`site_shop`，
以及 `shops.theme_id`／`shops.content_json`）、渲染快取與其失效事件、rich text 消毒、`web/` 現況（Next.js 租戶渲染）、
admin 的「頁面」「全域內容」視圖、開店精靈的主題／網域欄位與檢查清單三項（`theme_applied`、`domain_bound`、`home_published`）、
e2e 的 CMS 區段、`starter` 主題 seed 與 demo 商家的 CMS 首頁草稿與 `demo.localhost` 站點。
完整清單與依賴見 `openspec/changes/retire-tenant-rendering/design.md`。

**對文件與契約的後果**：`/api/v1` 的表面鎖（`api/openapi/v1-surface.txt`）規定移除任何一行＝破壞 v1，且 design D13 的破例窗口已關閉。
所以日後移除這些端點**不是**單純刪程式碼：需要一個明確核准的「v1 破例」或走 `/api/v2`；在那之前只能標記棄用（例如 `Deprecation`／`Sunset` 標頭）。
這是這項決定最大的隱藏成本，詳見草稿 change 的 design。

## 取捨（誠實版）

**得到的**

- **完整、由 AI 驅動的設計自由。** 每家店是自己的 repo，AI 在設計層裡可以做出完全不同的版型與互動，不受平台主題的 section 清單限制。
  舊模型下，商家能改的只有 schema 允許的欄位。
- **爆炸半徑小。** 一家店的設計壞了，只影響它自己的 repo 與部署；鎖定核心（金額、API、認證）有雜湊與守門員保護。
- **平台自己要維護的前端表面變小。** 不必維護一套通用的頁面編輯器、主題相容性預檢、渲染快取與其失效。
- **與實際使用情況一致。** 唯一真實商家本來就是這樣用的。

**付出的**

- **每家商家一份營運成本**：一個 repo、一組 Cloudflare Workers 部署（店面）、一個後台靜態站與其 Tunnel／Access／DNS 項目、
  R2 CORS 清單、固定 preview 網域登記。Cloudflare 那一側今天**全部手動**（`merchant-onboarding` skill 的 known-gaps 8）；
  開店從「一次 API 呼叫」變成「API ＋ 建 repo ＋ 部署 ＋ DNS ＋ Access」。
- **需要一套核心同步流程。** 鎖定核心的修正或新功能要版本化（template tag）並逐店 `sync-core`；N 家商家＝N 次 PR／部署，
  且若新核心要求設計層跟進（`Design-layer action`），要逐店處理。沒有同步紀律，各店的核心會漂移。
- **對「很多小商家」的營運規模較差。** 共用託管主題的模式下，改一次主題全部商家同時生效、開店零基礎設施；
  repo-per-merchant 下成本隨商家數線性增加。這個模型押注在「商家數量少、單價高、願意為客製付費」，而不是長尾。
  若日後要服務大量低客單價商家，這項決定需要被重新評估（那時才會知道要不要一個輕量的託管路徑）。
- **安全責任下放到更多地方。** 每個站是獨立部署，CSP、依賴、設定錯誤的面積隨店數增加；publishable key 與 CORS 登記有人為出錯空間。
- **平台自己的 demo 與回歸測試失去「託管店面」這個免費樣本。** 之後 live demo 必須是一個真的 merchant-template 部署。
- **範本本身成為關鍵相依。** 範本的版本、`sync-core` 與守門員的品質，直接決定所有商家的維護成本；
  範本與平台 API 的契約（`docs/storefront-integration.md`、`openapi.yaml`）必須保持穩定。
- **若 AI 設計失控，仍需人審。** 守門員只能檢查範圍與靜態規則，不能判斷好壞；「核可」這一步仍是人。

## 考慮過的替代方案

- **維持雙軌（託管主題 ＋ 自帶前端）。** 保留了長尾商家的低成本路徑，但要永遠維護一套沒有消費者的 CMS／主題／渲染系統，
  且 `openspec/project.md` 記載的「主題店面電商前台」（選項 C）從未實作。決定不維持。
- **現在就移除。** 風險集中、且被 v1 表面鎖擋住（見後果）。決定分階段，先標記。

## 階段計畫

| 步驟 | 內容 | 狀態 |
| --- | --- | --- |
| 1 | **記錄並標記**：本決定記錄、`docs/PLATFORM-ROADMAP.md`、README／CLAUDE.md／相關 docs 與 specs 的「已棄用」註記、`merchant-onboarding` skill 改為 headless 唯一路徑、兩個 OpenSpec change（`platform-website` 提案、`retire-tenant-rendering` 草稿）。**不改任何行為。** | 本次完成 |
| 2 | **`web/` 改造成平台官網**：產品介紹、運作方式、範本 live demo、價格（佔位）、條款／隱私（佔位）、開店申請流程（公開端點、驗證信箱、後台審核、核准觸發既有開店 API）。見 `openspec/changes/platform-website`。依賴使用者回答其中的開放問題。 | 待核准 |
| 3 | **退役租戶渲染功能**：依 `retire-tenant-rendering`（草稿，需使用者另行明確核准，且需先決定 v1 破例或 `/api/v2`）。含棄用期與 `Sunset` 標頭、資料遷移與回滾。 | 草稿，**不得實作** |

注意步驟 2 與 3 的耦合：`web/` 現有的 Next.js catch-all 渲染路由與靜態輸出（官網建議用 `output: "export"`）互斥，
所以官網改造不可避免地帶走 `web/` 內的租戶渲染程式碼。`platform-website` 的 design 如實記錄這一點與建議的處理。
