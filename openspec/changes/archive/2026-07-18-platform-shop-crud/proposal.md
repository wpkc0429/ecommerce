## Why

`shops`（租戶）目前只能透過 `make seed`/`make seed-demo` 建立——沒有任何正式 API 可以建店、查店、改店、停用/啟用店。這代表平台方若要上架新商家，唯一手段是工程師手動跑 seed script 或直接寫 DB，無法支撐正常營運。`openspec/specs/multi-tenancy/spec.md` 已定義 shop status（0/1/2）如何影響前台放行（Shop status gating），但完全沒有定義「誰、透過什麼 API」能建立或轉換這個 status——這是 multi-tenancy capability 的一個缺口，也是接下來所有商家維運操作（含未來 self-service）的前置基礎。

## What Changes

- 新增平台層（`role_user.shop_id IS NULL` 的平台角色）商家管理 API，掛載於 `/api/v1/admin`：
  - `POST /admin/shops`：建立商家（`name` 必填、`theme_id`/`status` 可選），交易內同時觸發既有「建店自動建首頁」行為（`type_key=home, slug=home` 草稿頁，spec page-management 既有需求）。
  - `GET /admin/shops`：分頁列表。
  - `GET /admin/shops/{shopID}`：單筆查詢。
  - `PUT /admin/shops/{shopID}`：更新 `name`/`status`/`meta`（partial update）。
- 新增權限節點 `shop.create`、`shop.list`，加入 `PermissionCatalog` 並授予 `super_admin` 平台角色；`status` 轉換與一般更新沿用既有 `shop.update` 節點（不新開 `shop.disable`/`shop.enable`，理由見 design.md）。
- `cms.Service` 新增 `CreateShop` 服務函式（shop + home page 同一交易），供 handler 呼叫；不複製 `seed.go` 的種子邏輯，`seed.go` 後續可選擇性重構呼叫此函式（本次不強制，避免動到既有 seed 測試面）。
- 不新增/修改 `site_shop` 綁定或網域管理邏輯（`api/internal/httpapi/sites.go` 職責維持不變）。
- 不做商家自助（self-service）改自己資料的 API——本次 100% 平台層操作。

## Capabilities

### New Capabilities
（無）

### Modified Capabilities
- `multi-tenancy`：新增「商家管理 API」需求層級——目前只講路由解析與 status gating 的*效果*，沒有講 shop 實體本身如何被建立/查詢/更新/狀態轉換。新增 ADDED Requirements：
  - Platform-only shop creation（含自動建首頁的呼叫點）
  - Platform shop query and listing（含分頁）
  - Platform shop update and status transition（含合法 status 值校驗）
  - 沿用既有 Requirement「Shop status gating」定義的 0/1/2 語意，不重複定義，只新增「誰能把 status 改成什麼」的管理面規則。

  理由：不另開新 capability——shop CRUD 與既有的「Shop status gating」「Tenant data isolation enforcement」同屬 shop 實體的生命週期治理，讀者查 multi-tenancy spec 時應該能一次看到 shop 從建立到狀態轉換的完整規則，拆開會割裂閱讀動線；且本次沒有新增獨立的資料模型或跨 capability 的新概念，不足以構成獨立 capability。

## Impact

- **新增程式碼**：`api/internal/httpapi/shops.go`（新 handler，比照 `sites.go`/`roles.go` 的 `MountPlatform` 模式）、`api/internal/cms/service.go` 新增 `CreateShop`/`ListShops`/`UpdateShop` 等服務函式、`api/internal/seed/seed.go` 的 `PermissionCatalog` 與 `super_admin` 角色授權集合新增兩個權限節點。
- **路由**：`router.go` 的 `Deps` 新增 `Shops *ShopsHandler` 欄位，掛載於現有 `AdminMW` group、與 `d.Roles.MountPlatform(pr)`/`d.Sites.MountPlatform(pr)` 同級。
- **Wiring**：`api/internal/app/wire.go` 組裝 `deps.Shops`。
- **測試**：`api/internal/httpapi` 新增整合測試（建店+自動首頁、非平台角色 403、status 轉換合法性、分頁正確性）。
- **不影響**：`web`、`admin`、`site_shop` 相關 API 與職責、`rbac`/`authentication`/`theme-system`/`content-rendering`/`page-management` capability 的既有 spec 內容（`page-management` 的「Auto-created home page」需求維持原樣，本次只是新增一個呼叫它的入口）。
