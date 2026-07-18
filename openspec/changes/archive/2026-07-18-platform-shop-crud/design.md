## Context

`shops`（design D2 的租戶表）與其 status gating（0 停用/1 啟用/2 審核中）自 Phase 1 起就存在，但沒有管理 API——唯一寫入路徑是 `api/internal/seed/seed.go` 的 `seedDemoShop`（種子腳本，非服務層、非 HTTP API）。`api/internal/tenant/resolver.go` 的 `loadEntry` 甚至留了一段註解明講這個缺口：

> Note: shop status is cached inside the route entry; Phase 1 has no shop status mutation API, so staleness is bounded by the 5-minute TTL.

本 change 補上這個 API，同時順手把上述註解描述的「沒有 mutation API 所以只能靠 TTL 兜底」的暫時性妥協收斂掉——既然現在有了 mutation API，就该讓 status 轉換立即使 route cache 失效，而不是繼續依賴 5 分鐘 TTL。

讀者對象：後端工程師。沿用 Phase 1 design.md 的 D-編號慣例，但編號獨立（本 change 自己的 D1、D2…），不与 phase1 design.md 衝突。

## Goals / Non-Goals

**Goals:**

- 平台層 shop CRUD：建立（含自動建首頁）、查詢單筆、分頁列表、更新（`name`/`status`/`meta`）。
- status 轉換立即使受影響網域的 `route:{host}` 快取失效（補上 resolver.go 註解提到的已知缺口）。
- 新增權限節點沿用既有目錄慣例並授予 `super_admin`。

**Non-Goals:**

- 商家自助（self-service）API（商家改自己的 `shops.content_json` 是既有 `PagesHandler.updateContent`，那是 shop-scoped 而非 platform-scoped，本次不動）。
- `site_shop` 綁定/網域管理（`api/internal/httpapi/sites.go` 已完整覆蓋，職責不重疊）。
- 刪除商家（硬刪除租戶是高風險操作，Phase 1 spec 也未定義；本次只做狀態轉換式的「停用」，真正刪除留待未來單獨提案）。
- theme 綁定的完整校驗（沿用既有 `theme.manage`/`shop.update` 對 theme 存在性的檢查方式，不新增 theme 相容性 precheck——建店當下商家還沒有任何頁面，不存在 `PrecheckTheme` 場景）。

## Decisions

### D1. RBAC：完全重用既有三層判定 + `RequirePlatformPermission`

不新增授權機制。`ShopsHandler.MountPlatform` 比照 `SitesHandler.MountPlatform`/`RolesHandler.MountPlatform`，路由掛 `h.Authz.RequirePlatformPermission(node)`——同一個 `AuthzMW`、同一個 `rbac.Engine`。放棄的替代方案：在 handler 內另寫「檢查 `role_user.shop_id IS NULL`」的 ad-hoc 判斷——RBAC spec 的 Platform-scope roles 需求已經是这个语意，`RequirePlatformPermission` 就是它的實作，重複寫等於分裂授權邏輯的唯一真相來源。

### D2. 權限節點：新增 `shop.create`、`shop.list`；沿用既有 `shop.update`

`api/internal/seed/seed.go` 的 `PermissionCatalog` 新增：

| 節點 | 說明 |
|---|---|
| `shop.create` | 建立商家（平台層） |
| `shop.list` | 列出所有商家（平台層） |

`GET /admin/shops/{shopID}`、`PUT /admin/shops/{shopID}`（含 status 轉換）沿用既有 `shop.view`/`shop.update`——這兩個節點目前授予商家自己的 `merchant_owner`/`editor`（shop-scoped）。平台層路由用 `RequirePlatformPermission`，判定時只看該使用者「平台層」的角色集合（`role_user.shop_id IS NULL`），因此商家角色持有的 `shop.view`/`shop.update` 不會誤放行平台路由——三層判定的 shop_ctx 在平台路由固定是 `rbac.PlatformCtx`，商家層的 `role_user`（`shop_id = 該商家`）本就不參與這個 ctx 的聯集。不新開 `shop.disable`/`shop.enable`：status 轉換是 `PUT` 的一種欄位變更，語意上與改 `name`/`meta` 同屬「更新商家」，拆成獨立權限節點徒增管理面複雜度且無實際隔離價值（平台管理員本就該能做全部）。

四個新/沿用節點統一加進 `super_admin`（platform）角色的授權集合（`roleDefs` 的 `"*"` 已自動涵蓋新節點，因為 `super_admin` 授權邏輯是「目錄裡全部節點」——不需要額外改 `roleDefs`，只需確保新節點進了 `PermissionCatalog`）。

### D3. 分頁：`page`/`page_size` query params + envelope

```
GET /admin/shops?page=1&page_size=20
→ 200 {"shops": [...], "page": 1, "page_size": 20, "total": 37}
```

- `page` 預設 1（`<1` 正規化為 1，不回 400——寬容輸入優於拒絕）。
- `page_size` 預設 20，上限 100（超過上限截斷，不回 400，理由同上；下限 1）。
- 選 `page`/`page_size` 而非 `limit`/`offset`：對 admin UI 使用者更直覺（頁碼式 UI），且與現有程式庫其餘查詢參數命名風格（snake_case、語意化）一致；`total` 用單一 `Count()` 查詢 + 一次 `Limit/Offset` 查詢（兩次查詢换取程式碼簡單，商家數量在可預期的規模內非熱路徑，不做 keyset pagination）。
- 排序固定 `ORDER BY id ASC`（比照 `ListPages`/`listAllRoles`），不開放排序參數（YAGNI，需要時再加）。

### D4. `cms.Service.CreateShop`：交易內建店 + 自動建首頁

`seedDemoShop`（seed.go）目前是唯一會建立「shop + home page」的程式碼，但它是種子專用（挾帶 demo 內容、demo owner 帳號、demo site 綁定），不適合直接被 API 呼叫。新增 `cms.Service.CreateShop(ctx, ShopInput) (*ent.Shop, error)`：

1. 驗證 `name` 非空、`status`（若提供）∈ {0,1,2}、`theme_id`（若提供）存在。
2. 開一個 `ent.Tx`：建立 `shops` 列 → 呼叫內部 `createHomePage(ctx, tx, shopID)`（新的私有 helper，從 `seedDemoShop` 的首頁建立邏輯抽出通用形態：`type_key=home, slug=home, status=0`，但不帶 demo 內容，`content_json` 給 `{"sections":[]}`——即便商家尚未套用主題也能建立，因為首頁的存在性是 spec page-management「Auto-created home page」的硬性要求，不依賴 schema 校驗通過；有主題時之後透過既有 `PagesHandler.update`/`publish` 正常編輯發佈）→ commit。
3. `theme_id` 為 optional（ent schema 本就 `Optional().Nillable()`）——建店當下不強制選主題，可稍後透過既有 `ThemesHandler`/`SwitchTheme` 流程套用；首頁自動建立不因此受阻（見上一點）。

`seed.go` 的 `seedDemoShop` 本次**不**重構為呼叫 `CreateShop`——它還要處理 demo owner/demo site/demo 內容等種子專屬邏輯，且改動既有種子路徑會擴大本次變更的回歸面（seed 沒有自動化測試直接覆蓋這條路徑之外的 demo 內容正確性）。留給後續視需要再重構，不在本次範圍內造成不必要風險。

### D5. Status 轉換立即使 route cache 失效

`UpdateShop` 若 `status` 有變更，在交易 commit 後查 `shop.QuerySites().Select(site.FieldDomain).Strings(ctx)` 取得所有綁定網域，透過既有 `events.SiteMappingChanged{Hosts: domains, ShopIDs: []int{shopID}}` 事件發布——完全重用 `api/internal/httpapi/sites.go` 已经在用的同一個事件類型與 `app/wire.go` 已註冊的訂閱者（`resolver.InvalidateHosts`），不新增事件類型、不新增訂閱邏輯。若商家尚未綁定任何網域（`domains` 為空切片），`InvalidateHosts` 對空輸入本就是 no-op（見 resolver.go 判斷 `len(hosts) == 0`）。

這修正了 `resolver.go` 註解描述的已知缺口（此前只能靠 5 分鐘 TTL 兜底），使其符合 spec multi-tenancy「Route resolution cache」需求的精神（「對應異動後立即生效」原本只針對 `site_shop` 異動情境撰寫，本次視 shop status 轉換為同一類「影響路由解析結果的異動」，適用同一保證）。

### D6. 不新增限流

建店/改店 API 是已認證（admin JWT）+ 平台角色閘門的後台端點，威脅模型與 `RolesHandler`/`SitesHandler` 的既有平台端點（`POST /admin/permissions`、`POST /admin/sites` 等）相同——這些從未套用 `ratelimit` 中介層（該原語目前只用於*未認證*的 auth 端點：登入/註冊/refresh，防的是憑證暴力破解與帳號列舉）。已通過平台角色驗證的呼叫者本就是受信任的管理員，比照既有同類端點不加限流，維持一致性；若未來需要，可用既有 `ratelimit.Limiter` + `orPassthrough` 模式直接掛載，不需要新設計。

### D7. 不需要新的 DB migration

`shops` 表結構（`name`/`theme_id`/`status`/`content_json`/`meta`）Phase 1 已建立且完全滿足本次欄位需求，本次不改 ent schema、不跑 `make migrate-gen`。唯一的資料面變更是透過既有 `seedPermissions`/`seedRoles` 機制新增的兩筆 `permissions` 列與 `role_permission` 關聯——這是 seed 資料而非 schema migration。

## Risks / Trade-offs

- [`page_size` 上限 100 可能不夠某些超大平台的批次匯出需求] → 目前商家規模（Phase 1/2 单租户 SaaS 起步）远低于该量级；需要时用既有分页参数直接调大上限即可，不是架構限制。
- [D5 的 route cache 失效查詢（`QuerySites`）在商家綁定大量網域時是 O(n) 查詢] → 現實中一個商家的網域數量是個位數（主網域 + 少量別名），非熱路徑（僅 status 變更時觸發），不構成效能風險。
- [建店時允許 `theme_id` 為空，首頁因此以未經 schema 校驗的預設內容建立——之後若商家一直不套用主題，首頁永遠無法通過 `PublishPage` 的 schema 校驗（`currentPageSchema` 要求 `shop.ThemeID != nil`）] → 這是既有行為（`CreatePage`/`PublishPage` 服務函式本就有這條規則），本次沒有改變它，只是让「建店」这个动作本身不因为没有主题而失败；商家上线前必须套用主题才能发布，这与 spec page-management 的既有语意一致，不是新引入的风险。

## Migration Plan

無 schema migration。部署順序：

1. 部署新程式碼（新增 handler/service/permission 節點，向後相容——不改任何既有端點行為）。
2. 對既有環境跑 `make seed`（或服務啟動時既有的 seed 執行路徑，視部署方式而定）補上兩個新權限節點並授予既有 `super_admin` 角色——`seedPermissions`/`seedRoles` 皆為 idempotent（natural key 查找後才插入），可安全重跑。
3. 無需資料回填、無需停機。

回滾：純程式碼回滾（無 schema 變更可回滾）；新增的兩個 permission 列即使回滾後仍留在 DB 也無副作用（未被任何路由引用）。

## Open Questions

- 商家硬刪除 API（真正 `DELETE`）——本次刻意排除（Non-Goals），留待有明確需求（如帳務/歸檔流程定案）時再開新提案，届时也需要一并设计对 pages/site_shop/shop_user 等关联资料的清理策略。
