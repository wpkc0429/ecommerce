# 0001 — 平台定位為 headless 商務 API，商家前端由 merchant template 產生

- 狀態：**已決定**（2026-11-24，由專案擁有者決定；本文件只記錄，不重新討論）。**已修正一次**：見文末「修正（Amendment）」——CMS 內容儲存保留，只棄用託管渲染、主題與 site／網域路由
- 範圍：產品方向。本文件本身不改任何行為；實際移除走另案、另行核准（見「階段計畫」）。
- 相關：[`docs/PLATFORM-ROADMAP.md`](../PLATFORM-ROADMAP.md)、OpenSpec change `platform-website`（提案）、`headless-content-store`（**提案，未實作**：把 CMS 內容功能改造成 headless 內容儲存）、`retire-hosted-storefront-and-themes`（**已核准 2026-11-24，可實作**）

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
3. ecommerce 自己的**租戶渲染**功能**作為產品已棄用**，範圍**只有**：託管的 Next.js 店面（`web/` 現況的租戶渲染部分）及其 e2e 與 CI、
   **主題與主題系統**（`themes`／`theme_pages`、主題端點、`web/themes/` 元件庫、`starter` 主題 seed、`theme.*` 權限）、
   **site／網域對應與 Host 解析**（`sites`／`site_shop`、`/admin/sites*`、`tenantmw` 的 Host 後備、`X-Site-Domain`）。
4. **CMS 的內容功能保留，並改造成通用的 headless 內容儲存**（見文末「修正」）：每家商家的內容文件（schema 驗證、草稿／發佈、預覽 token、HTML 消毒、
   圖片網域檢查、以 publishable key 讀取的公開 API、帶事件失效的快取）是商家站台的**內容後端**；改造由 `headless-content-store` 負責。
5. **移除已核准（2026-11-24，使用者：「移除舊功能，直接移除」）**：由 `retire-hosted-storefront-and-themes` 執行，**沒有棄用期、沒有 sunset 視窗**；v1 表面鎖採一次性、明確記錄的例外（ADR 0002，於該 change 實作時建立）。內容儲存改造（`headless-content-store`）是另案、待核准；其第一個切片（每店 schema 儲存）由退役 change 的第一階段交付，因為頁面 schema 今天來自主題，必須先搬走才能移除主題。

## 後果

**不變（bouquet 與 template 用到的一切，持續維護）**：商品目錄（分類、商品、巢狀 SKU、圖片、公開分類樹）、購物車、訪客與會員結帳、
訂單與出貨、付款（mock／ECPay、每店憑證）、會員（每商家獨立身分、等級、點數、顧客名冊）、圖片（R2）、報表、
後台帳號與開店（`POST /admin/shop-provisioning`）、員工邀請、publishable key 與 allowed origin（含 `purpose=admin`）、
後台對外開放（Cloudflare Access、`PLATFORM_ADMIN_HOSTS`、admin 主機綁定、branding）。

**保留並改造（內容儲存）**：頁面內容與全域內容（`pages`、`shops.content_json`）、schema 驗證與 hydration、草稿／發佈、preview token、
rich text 消毒（`api/internal/cms/richtext`）、圖片網域檢查、渲染快取與其事件失效、公開讀取端點（`GET /api/v1/render/page`、`/render/preview`）、
`/api/v1/admin/shops/{id}/pages*`、`/content`、`/page-types`、`POST …/media/content-images`、admin 的「頁面」「全域內容」視圖與 `schemaform.js`、
`page.*` 權限、`PREVIEW_TOKEN_TTL`、`cmd/sanitize-content`。這些**會被改造**（schema 改為每店註冊、文件改以穩定的 `key` 定位、新增 `content.*` 權限與
版本歷史、發佈通知），改造期間既有端點照常運作；細節見 `openspec/changes/headless-content-store`。

**移除（已核准，執行前仍保留運作）**：
`/api/v1/admin/themes*`、`/api/v1/admin/shops/{id}/theme*`、`/shops/{id}/themes`；`/api/v1/admin/sites*`；
對應的 ent 表（`themes`、`theme_pages`、`sites`、`site_shop`，以及 `shops.theme_id`）、`web/` 現況（Next.js 租戶渲染）、
admin 開店精靈的主題／網域欄位與檢查清單的 `theme_applied`、`domain_bound`、`starter` 主題 seed、demo 商家的 `demo.localhost` 站點、
e2e 的租戶渲染區段（依賴 `WEB` 的斷言）。完整清單與依賴見 `openspec/changes/retire-hosted-storefront-and-themes/design.md`。
**順序上的相依**：今天的頁面 schema 來自主題的 `theme_pages.page_schema`，所以主題只能在內容儲存改成「每店自帶 schema」**之後**才能真正移除。

**對文件與契約的後果**：`/api/v1` 的表面鎖（`api/openapi/v1-surface.txt`）規定移除任何一行＝破壞 v1，且 design D13 的破例窗口已關閉。
使用者已核准移除，因此採**一次性、明確記錄的例外**：決策紀錄 ADR 0002、鎖檔的刪除與路由移除在同一個原子 commit、追加專用的 `api/openapi/v1-removed.txt` 與其契約測試、`CLAUDE.md`／`project.md`／`api-contract` spec 的說明。例外**只涵蓋**退役 change 列出的約 60 行（主題與 site 的 16 條路由與相關欄位）；未列入的任何移除仍須另行例外。這**不是**重新開啟 D13 的窗口。

## 取捨（誠實版）

**得到的**

- **完整、由 AI 驅動的設計自由。** 每家店是自己的 repo，AI 在設計層裡可以做出完全不同的版型與互動，不受平台主題的 section 清單限制。
  舊模型下，商家能改的只有 schema 允許的欄位。
- **爆炸半徑小。** 一家店的設計壞了，只影響它自己的 repo 與部署；鎖定核心（金額、API、認證）有雜湊與守門員保護。
- **平台自己要維護的前端表面變小。** 不必維護主題系統、主題相容性預檢、託管的渲染店面與 Host 路由；內容編輯器與內容快取則**保留**（商家文案仍需要一個不在程式碼裡的家）。
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

- **維持雙軌（託管主題 ＋ 自帶前端）。** 保留了長尾商家的低成本路徑，但要永遠維護一套沒有消費者的主題／託管渲染系統，
  且 `openspec/project.md` 記載的「主題店面電商前台」（選項 C）從未實作。決定不維持。
- **一次全部移除。** 風險集中。決定分階段（每階段可獨立測試與回滾），並由使用者核准 v1 例外，不經棄用期。

## 階段計畫

| 步驟 | 內容 | 狀態 |
| --- | --- | --- |
| 1 | **記錄並標記**：本決定記錄、`docs/PLATFORM-ROADMAP.md`、README／CLAUDE.md／相關 docs 與 specs 的註記、`merchant-onboarding` skill 改為 headless 唯一路徑、OpenSpec change。**不改任何行為。** | 完成（含本次修正） |
| 2 | **平台官網**：產品介紹、範本 live demo、開店申請流程。**已決定（2026-11-24）：零建置靜態站（沿用 merchant template 的鎖定核心＋設計層與守門員），放在獨立 repo，不改造 `web/`**。見 `platform-website`。其餘開放問題待使用者回答。 | 待核准（技術路線已定） |
| 3 | **`headless-content-store`**：把 CMS 內容功能改造成通用 headless 內容儲存（每店 schema、穩定 `key`、版本歷史、`content.*` 權限、發佈通知、新的公開讀取端點）；既有 `pages`／`render` 端點在棄用期內相容，之後由該 change 內的清理階段移除。含 merchant template 的後續工作（`ctx.data`／`ctx.actions`、schema 驅動表單）。 | 提案，待核准 |
| 4 | **退役託管店面與主題**：依 `retire-hosted-storefront-and-themes`（**已核准**：直接移除、無棄用期、v1 例外）。**第一階段先交付每店 schema 儲存（步驟 3 的第一個切片）**，之後才移除主題與 site。 | 已核准，可實作 |

步驟 2 與 4 已無耦合：官網在獨立 repo，`web/`（Next.js 租戶渲染）由步驟 4 整個刪除。

## 修正（Amendment，2026-11-24，同日）

**修正內容**：本文件最初把**整個 CMS**（頁面、頁型、sections、全域內容、渲染 API、快取、消毒）與託管店面、主題一起列為棄用。這**與先前的決定相牴觸**，現修正如下：

- 商家會透過後台**表單**（而不是透過 AI）修改小東西：文字、banner、活動文案、促銷圖片、店家資訊、FAQ。
- 這類由商家編輯的內容**必須存在商家站台程式碼之外**；否則每改一段文字就是一次程式碼變更，並與 AI 對設計層的修改互相衝突。
- 正確的家是 ecommerce 的 CMS **以 headless 方式使用**：透過它的公開讀取 API（`GET /api/v1/render/page` 可用 publishable key 讀取，另有 preview 端點），這些都已存在。

**因此**：CMS 內容儲存**保留**，並改造成通用的 headless **內容儲存**，作為商家站台的內容後端。**只有**託管渲染（Next.js 租戶渲染店面）、**主題與主題系統**、
**site／網域路由（Host 解析）** 被棄用。上面的「決定」「後果」「階段計畫」已依此改寫；原先「CMS 整體棄用」的說法一律作廢。

**為什麼不是先前的做法**：把內容與設計分離是 headless 模式的前提。商家的設計層由 AI 修改、受守門員保護；商家自己的文案若寫在 `content.js` 之類的設計層檔案裡，
每次文案變更都會進入同一條 AI／審核流程，且與設計編輯衝突。內容儲存把「誰改什麼」切乾淨：AI 改設計層，商家改內容文件，兩者在發佈時才相遇。

**放置規則（Placement rule，使用者於 2026-11-24 決定）**：需要後端運算或伺服器端狀態的東西，歸 ecommerce（API 與平台後台）；很少改動的內容，放在站台自己的 repo（官網 repo，或商家店面 repo 的設計層）；商家或操作員經常用表單編輯的內容，放在 ecommerce 的內容儲存（`headless-content-store`）。
具體到平台官網：介紹頁、範本展示、價格文字、條款與隱私文字與文案，**放在官網 repo 作為靜態內容**（不依賴 CMS、不在執行期抓取）；申請表單的送出、防濫用驗證、儲存、審核與核准、通知與稽核，**歸 ecommerce**；官網的鎖定核心只呼叫 ecommerce 的公開申請端點，**官網 repo 不需要自己的後端**。

**退役已核准**：使用者隨後核准了退役（範圍限於託管店面、主題、site 路由；內容儲存保留），直接移除、無棄用期。順序限制見 `retire-hosted-storefront-and-themes` 的 design D4。

**對先前文件的影響**：`retire-tenant-rendering`（草稿）縮小範圍並更名為 `retire-hosted-storefront-and-themes`；新增 `headless-content-store`；
`docs/PLATFORM-ROADMAP.md`、README、CLAUDE.md、`storefront-integration`、`media-r2-setup`、`openspec/project.md` 與相關 specs 的棄用註記已改為只涵蓋被棄用的部分。
