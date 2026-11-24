# content-rendering Specification
> **部分保留並改造（ADR 0001 的修正，2026-11-24）**：已發佈內容的公開讀取、bundle 組裝、Redis 讀穿快取與事件失效、preview token 是 headless 內容儲存的讀取面，**不棄用**，將由 change `headless-content-store`（提案、未實作）改造。**已移除**的是：bundle 內的主題與元件欄位（`theme`、`component_key`、`layout_key`）、以 Host／`X-Site-Domain` 解析商家，以及消費它們的 `web/` 託管渲染店面（change `retire-hosted-storefront-and-themes`）；商家只由 `X-Publishable-Key` 解析。見 `docs/decisions/0001-headless-platform-and-merchant-template.md`。

## Purpose
TBD - created by archiving change phase1-multitenant-cms-foundation. Update Purpose after archive.
## Requirements
### Requirement: Render bundle API
前台渲染 API SHALL 依（解析出的 shop, slug）於單一回應中回傳：商家全域內容（hydrated `shops.content_json`）與頁面內容（hydrated `published_json` + `type_key` + `page.seo`）。schema 取自商家作用中的 content schema（`content_schemas`）。

#### Scenario: 單一請求取得完整渲染資料
- **WHEN** 商家站台以 publishable key 請求已發佈頁面的渲染 bundle
- **THEN** 回應同時含 `shop.content`、`page.content`、`page.type_key`、`page.seo`，不含 `theme` 與 `component_key`，無須第二次請求

### Requirement: Hydration with schema defaults
渲染輸出前系統 MUST 依對應 schema 對 payload 執行 hydration：缺漏欄位以 schema `default` 補全（物件遞迴合併、一般陣列以 payload 整體為準；section 陣列之每一項 MUST 依其 `type` 對應的區塊 schema 補全 default）；schema 未定義的鍵與未知區塊型別 MUST 自輸出剔除。

#### Scenario: Schema 新增欄位自動補預設值
- **WHEN** 商家 content schema 新版本的頁型 schema 新增 `banner.subtitle`（default: ""），既有頁面 payload 無此欄位
- **THEN** 渲染輸出的 `banner.subtitle` 為 `""`，前端不會取得 undefined

#### Scenario: 殘留鍵被剔除
- **WHEN** payload 含舊 schema 遺留、當前 schema 未定義的 `old_widget` 鍵
- **THEN** 渲染輸出不含 `old_widget`

#### Scenario: 區塊項目補全預設值
- **WHEN** 新版 content schema 的某區塊型別新增 `cta_text`（default: "了解更多"），既有頁面 `sections` 中該型別項目無此欄位
- **THEN** 渲染輸出該 section 的 `cta_text` 為預設值

### Requirement: Published-only public rendering
公開渲染 MUST 僅輸出 `status = 1` 且 `published_json` 非 NULL 的頁面；草稿或已下架頁面回 404。

#### Scenario: 草稿不可公開存取
- **WHEN** 前台請求 `status = 0` 頁面的 slug
- **THEN** 回 404

### Requirement: Redis read-through cache
渲染 bundle MUST 以 `cache:shop:{shop_id}:v{ver}:page:{slug}` 快取於 Redis（TTL 24 小時），`ver` 取自 `cache:shop:{shop_id}:ver`；cache miss 時組裝並回填，並以 singleflight 合併同 key 併發回源；Redis 不可用時 MUST 降級為直接查 DB 組裝（服務不中斷）。

#### Scenario: 快取命中
- **WHEN** 同一頁面於 TTL 內被第二次請求
- **THEN** 回應直接來自 Redis，不觸發 DB 查詢

#### Scenario: Redis 故障降級
- **WHEN** Redis 連線失敗
- **THEN** 渲染 API 改走 DB 組裝並正常回應

### Requirement: Event-driven cache invalidation
內容寫入 MUST 經領域事件觸發對應失效：頁面發佈/下架 → 刪除該頁 key；商家全域內容更新、商家狀態變更 → 對受影響商家執行 version bump（`INCR cache:shop:{id}:ver`）。失效 MUST 與寫入交易成功綁定。

#### Scenario: 發佈後下一請求即新內容
- **WHEN** 頁面發佈完成後前台再次請求該 slug
- **THEN** 快取未命中（key 已刪除），回應為新發佈內容並回填快取

#### Scenario: 商家停用使快取失效
- **WHEN** 商家狀態改變（`ShopStatusChanged`）
- **THEN** 該商家 `ver` 遞增，且其 publishable key 快取項目同時失效

### Requirement: Authenticated preview of working copy
後台預覽 SHALL 以短效 preview token 請求渲染 API 變體，輸出**工作副本**（`content_json`）組裝的 bundle；預覽回應 MUST NOT 寫入快取，且 MUST 驗證 token 對應使用者具該商家的頁面讀取權限。

#### Scenario: 預覽草稿
- **WHEN** 編輯者以有效 preview token 預覽未發佈的修改
- **THEN** 回應為工作副本內容，公開渲染與快取不受影響

