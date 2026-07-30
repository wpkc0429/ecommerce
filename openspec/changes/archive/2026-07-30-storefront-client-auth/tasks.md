## 1. 前置查證

- [x] 1.1 再次確認 v1 尚無外部消費者：`bouquet/public/app.js` 仍為硬編碼資料、全樹零 API 呼叫（design D12 的破例前提）
      — 查證結果：`bouquet/public/` 全樹 grep `fetch(` / `api/v1` / `axios` / `XMLHttpRequest` **零命中**（`app.js` 1173 行皆為硬編碼資料與 DOM 操作）。破例前提成立。
- [x] 1.2 盤點倉庫內 `X-Site-Domain` / `X-Site-Path` 的全部消費點，標註各自打的是前台群組還是渲染端點，作為 D6 的改動清單

  | 消費點 | 目標端點 | 處置 |
  | --- | --- | --- |
  | `web/lib/api.ts:56` | `/api/v1/render/page` | **不動**（渲染通道保留，D6） |
  | `e2e/happy-path.mjs:172` | `/api/v1/render/page` | **不動** |
  | `httpapi/cms_integration_test.go:126` | `/api/v1/render/page` | 不動 |
  | `httpapi/tenantmw.go:18,22` | 前台群組 | 移除，改金鑰 → Host |
  | `httpapi/render.go:32` | 渲染端點 | 保留，前面加金鑰通道 |
  | `httpapi/middleware.go:34` | CORS Allow-Headers | 換成 `X-Publishable-Key` |
  | `cart/order/payment/shipping/points/categories/tenancy` 整合測試 | 前台群組（真實 TenantMW） | 改 `req.Host` 或金鑰 |
  | `authflow/ratelimit` 整合測試 | 前台群組（**假** TenantMW） | 測試自有的 host→shop 假件，不經真實解析；改用金鑰以免留下誤導性用法 |

- [x] 1.3 確認 `/web` 與 `admin` 不呼叫任何 `/api/v1/shop/*` 端點（破壞性面向的影響半徑）
      — 查證結果：`web` 僅 `render/page`（`lib/api.ts`）與 `render/preview`（`app/preview/page.tsx`）；`admin` 僅 `/api/v1/admin/*`（`app.js` 四處）。影響半徑為零。

## 2. ent schema

- [x] 2.1 新增 `internal/ent/schema/clientauth.go`：`PublishableKey`（`shop_id`、`key` 唯一、`label`、`created_at`、`revoked_at` optional）與指向 `Shop` 的 edge、`shop_id` 索引
- [x] 2.2 同檔新增 `ShopAllowedOrigin`（`shop_id`、`origin` 全域唯一、`created_at`）與指向 `Shop` 的 edge、`shop_id` 索引
- [x] 2.3 `internal/tenant/enttenancy.go`：兩個實體加入 `tenantOwned`（租戶擁有資料；解析路徑刻意在租戶上下文之外執行，於註解記錄）
- [x] 2.4 執行 `go generate ./internal/ent`，確認產生的 client 可編譯

## 3. Migration

- [x] 3.1 以 `make migrate-gen name=add_storefront_client_auth` 產生 migration（純新增兩張表，無資料搬移）
      — `api/migrations/20260730161118_add_storefront_client_auth.{up,down}.sql`
- [x] 3.2 檢查 diff 是否誤提既有 NULLS NOT DISTINCT 約束的 DROP/re-ADD，比照既有慣例移除
      — diff 誤提四項：`uq_members_shop_email`／`uq_members_shop_phone`（姊妹 change 手寫的 per-shop 唯一索引）與 `role_user`／`user_permission` 的 `UNIQUE NULLS NOT DISTINCT`。全數手動移除，並於 up.sql 開頭註記原因；套用後以 `pg_indexes`／`pg_constraint` 確認四者仍在。
- [x] 3.3 `make migrate` 於 dev DB 套用成功、`make migrate-down` 可回滾，`atlas.sum` 同步
      — up → down → up 全數成功；`atlas.sum` 以 atlas `migrate.WriteSumFile` + `migrate.Validate` 重算並驗證（手改 up.sql 後必要）。

## 4. 租戶解析（spec: storefront-client-auth, multi-tenancy）

- [x] 4.1 `internal/tenant`：新增金鑰產生器（`pk_` + 20 bytes crypto/rand 的小寫 base32，design D8）與格式檢查
      — `internal/tenant/clientauth.go`：`NewPublishableKey` / `ValidKeyFormat`。`ValidKeyFormat` 允許 `_` 出現在隨機段之後（未來 `pk_test_` 中綴），但產生器永不輸出 `_`——單元測試同時鎖住這兩件事。
- [x] 4.2 `internal/tenant`：`Resolver.ResolveKey` — 金鑰 → shop（含 `shops.status` 放行判定），未知／已撤銷回同一個哨兵錯誤；Redis 快取 `pkey:{key}`（TTL 5 分鐘），Redis 故障 degrade 到 DB
- [x] 4.3 `internal/tenant`：`NormalizeOrigin` 與 `Resolver.IsRegisteredOrigin`（含否定快取 `corsorigin:{origin}`）
      — 偏離說明：金鑰查詢**不做**否定快取（撤銷須即時生效，且無效金鑰不是量的問題）；origin 查詢做否定快取（每個 preflight 都會查一次）。理由寫在程式碼註解。
- [x] 4.4 `internal/tenant`：`InvalidateKeys` / `InvalidateOrigins` / `InvalidateShopKeys`（依 shop 查出金鑰後刪除快取條目）
- [x] 4.5 `internal/httpapi/tenantmw.go`：解析順序改為 `X-Publishable-Key` → `Host`，移除 `X-Site-Domain` / `X-Site-Path`；金鑰無效不回退（design D7）
      — 無金鑰時路徑固定為 `/`（`X-Site-Path` 消失後前台群組不再有前台路徑可用，見 multi-tenancy spec 的 Path-prefix 補述）。`NewTenantMiddleware` 簽章未變（金鑰解析掛在既有 `*tenant.Resolver` 上），既有測試佈線不必改。
- [x] 4.6 `internal/httpapi/render.go`：解析順序改為 `X-Publishable-Key` → `X-Site-Domain` → `Host`，並於註解記錄該 header 僅存活於此端點的理由（design D6）
- [x] 4.7 金鑰解析失敗的錯誤回應：404 + 專屬 code，未知與已撤銷同構
      — `unknown_publishable_key`；未知／格式不符／已撤銷三者的狀態碼與 body 位元組完全相同（測試以字串相等斷言）。

## 5. CORS 與 origin 登記（spec: storefront-client-auth）

- [x] 5.1 `internal/httpapi/middleware.go`：`NewCORSMiddleware` 接受 origin registry，允許集合 = 環境清單 ∪ 已登記 origin；平台清單先行短路
      — 以 `OriginRegistry` 介面接（`*tenant.Resolver` 實作），nil 時行為與本變更前完全一致。
- [x] 5.2 `Access-Control-Allow-Headers` 以 `X-Publishable-Key` 取代 `X-Site-Domain, X-Site-Path`
- [x] 5.3 `internal/httpapi/router.go`：CORS 中介層改為無條件掛載（登記的 origin 不再需要環境變數存在）
      — 精確條件為「環境清單非空 **或** registry 已佈線」，避免既有（兩者皆無）的測試路由被動掛上 CORS。
- [x] 5.4 確認 preflight 路徑不需要金鑰即可放行（design D3）
      — 整合測試以「只送 `Access-Control-Request-Headers: X-Publishable-Key`（名稱，無值）」模擬真實 preflight。

## 6. 後台管理 API 與 RBAC（spec: storefront-client-auth）

- [x] 6.1 `internal/httpapi/clientauth.go`：`GET/POST /admin/shops/{shopID}/publishable-keys`、`DELETE .../{keyID}`（撤銷為軟刪除），商家層 RBAC
      — 已撤銷金鑰仍留在列表（含 `revoked_at`），這正是軟刪除要回答的稽核問題。重複撤銷為 204（冪等）。
- [x] 6.2 同檔：`GET/POST /admin/shops/{shopID}/allowed-origins`、`DELETE .../{originID}`，平台層 RBAC
      — 路徑掛在 `/admin/shops/{shopID}` 下但守衛是 `RequirePlatformPermission`（該守衛不看 `{shopID}`，故 create 另行確認 shop 存在，否則 404）。
- [x] 6.3 後台查詢一律顯式帶 `shop_id`（不在租戶上下文，比照 member-identity-per-shop design D4）
- [x] 6.4 `internal/seed`：新增權限節點 `publishable_key.view/create/revoke`（授予 `merchant_owner`）與 `shop.manage_client_origins`（僅平台角色）
      — `super_admin` 以 `*` 自動取得全部四個節點；`editor` 一個都不給。
- [x] 6.5 金鑰列表回應完整金鑰值（不遮罩，design D1）；origin 重複登記回 422

## 7. 快取失效與佈線

- [x] 7.1 `internal/events`：新增 `StorefrontClientChanged{ShopID, Keys, Origins}`
- [x] 7.2 `internal/app/wire.go`：訂閱 `StorefrontClientChanged` 與既有 `SiteMappingChanged`（後者用於商家 status 變更時失效該商家全部金鑰快取），掛載新 handler 與 CORS registry
- [x] 7.3 確認失效一律走事件層，handler 不直呼 Redis（CLAUDE.md 硬規則）
      — handler 只 `Dispatcher.Publish`；建立金鑰時其實沒有快取可清，仍照發事件以免出現第二條失效路徑。

## 8. 測試

- [x] 8.1 更新使用真實 `TenantMW` 的整合測試：以 `req.Host` 或金鑰取代 `X-Site-Domain`（cart / order / payment / shipping / points / categories / tenancy）
      — 另把 `authflow` / `ratelimit` 的假 TenantMW 由 host→shop 改為 key→shop（原本模擬的通道已不存在，留著只會教出誤導性用法）。
- [x] 8.2 `tenancy_integration_test.go`：新增金鑰解析、金鑰優先於 Host、已撤銷金鑰不回退、前台 API 忽略 `X-Site-Domain`、渲染端點仍接受 `X-Site-Domain` 五個案例
      — 前四項在 `TestPublishableKeyTenantResolution` / `TestTenantMiddlewareHTTP`；第五項由既有的 `cms_integration_test.go` `renderPage`（本來就以 `X-Site-Domain` 打渲染端點）持續守住，未重複實作。另補停用／審核中商家的金鑰、查詢參數不被當作宣告兩案。
- [x] 8.3 新增 CORS 整合測試：登記後放行、移除後不放行、未登記不放行、preflight 不需金鑰、`Allow-Headers` 含 `X-Publishable-Key`
      — 另斷言平台環境清單仍有效、`Vary: Origin` 存在。
- [x] 8.4 新增後台管理 API 整合測試：產生／列出／撤銷、輪替（新舊並存 → 撤銷舊）、撤銷即時生效、跨店 403、商家角色登記 origin 403、重複 origin 422、非法 origin 422
- [x] 8.5 新增「金鑰不授予任何權限」測試：帶金鑰但無會員 token → 401；帶 A 金鑰與 `aud=shop:B` 的 token → 401
- [x] 8.6 新增「金鑰不擴大速率限制配額」測試：同商家兩把金鑰共用同一組計數
      — 併同斷言跨商家仍互不干擾（shop B 的金鑰不受 shop A 耗盡的配額影響）。
- [x] 8.7 `internal/tenant` 單元測試：`NormalizeOrigin` 與金鑰格式產生
      — `internal/tenant/clientauth_test.go`（不需基礎設施，`make test` 即涵蓋）。
- [x] 8.8 `make test` 全綠
- [x] 8.9 `make test-int` 全綠
- [x] 8.10 `make lint` 無新增問題
      — `golangci-lint run` → 0 issues（過程中修掉一個 staticcheck QF1001）。

## 9. 收尾

- [x] 9.1 `make seed-demo` 後 `cd e2e && node happy-path.mjs` 通過（渲染端點行為不變，e2e 應一行都不必改）
      — 17/17 checks PASSED，`e2e/happy-path.mjs` 一行未改。另以 curl 對執行中的服務做了一次手動煙霧測試：帶金鑰＋未綁定 Host → 200；未帶金鑰＋未綁定 Host → 404；**偽造 `X-Site-Domain: demo.localhost` → 404**（本變更的核心）；`Host: demo.localhost` → 200；渲染端點金鑰與 `X-Site-Domain` 皆 200；登記 origin 前 preflight 無 CORS 標頭、登記後 204 + `Access-Control-Allow-Origin` + `Allow-Headers` 含 `X-Publishable-Key`。
- [x] 9.2 確認 `web/lib/api.ts` 與 `e2e/happy-path.mjs` 的 `X-Site-Domain` 用法仍有效（皆打渲染端點），如無效則同步更新
      — 兩者皆打 `/api/v1/render/page`，依 design D6 保留，**無須修改**（e2e 通過即為證據）。
- [x] 9.3 更新 `CLAUDE.md` 架構註記與 `openspec/project.md` 的 `X-Site-Domain` 描述
      — 順手修掉 `project.md` 一條姊妹 change 遺留的過時約束（`members.email/phone` 全平台唯一），並補上「publishable key 非憑證」的約束條目。
- [x] 9.4 整理 ONDO 接入所需的前端作法（帶哪個 header、金鑰從哪來、CORS 要登記什麼）作為交付說明
      — 見本檔末的「ONDO 接入說明」。

## ONDO 接入說明（交付給 bouquet 專案）

前端（`bouquet/public/app.js`）需要做的事只有三件：

1. **每個 API 請求帶 `X-Publishable-Key` 標頭。** 值由平台在後台產生（`POST /api/v1/admin/shops/{shopID}/publishable-keys`）後交付，形如 `pk_e7gagijdzmaxsrxwm3nfoarlrbwf3dav`。**可以硬編碼在前端 JS 裡**——它是公開識別碼不是憑證，出現在原始碼中是預期用法，不需要環境變數注入、不需要打包時替換、洩漏不是資安事件。

   ```js
   const API = "https://api.<platform-domain>";
   const PUBLISHABLE_KEY = "pk_…";
   const res = await fetch(`${API}/api/v1/shop/products`, {
     headers: { "X-Publishable-Key": PUBLISHABLE_KEY },
   });
   ```

2. **不要送 `X-Site-Domain` / `X-Site-Path`。** 前台 API 已不再讀取它們；送了不會出錯，但也不會有任何作用（租戶仍由金鑰決定）。

3. **登入後的會員請求照常帶 `Authorization: Bearer <access_token>`，且金鑰仍要一起帶**——金鑰決定「哪一家商家」，token 決定「哪一位顧客」，兩者不互相取代。token 的 `aud` 與金鑰所屬商家不一致時一律 401。

平台端（本專案）需要先完成的事：

- **登記 origin**：`POST /api/v1/admin/shops/{shopID}/allowed-origins`，body `{"origin": "https://ondo.example"}`。**平台層權限**（`shop.manage_client_origins`），商家自己不能登記。未登記的 origin 一律拿不到 `Access-Control-Allow-Origin`，瀏覽器端會全數失敗。正式網域與需要用到的固定 preview 網域都要各登記一筆（**不支援萬用字元**，Cloudflare Pages 的隨機 hash preview 網域無法涵蓋——見 design Open Questions）。
- **產生金鑰**：`POST /api/v1/admin/shops/{shopID}/publishable-keys`，body `{"label": "ondo-prod"}`（label 純標註）。回應的 `key` 即交付值。
- **輪替**：先產生第二把金鑰 → ONDO 更新部署 → `DELETE /api/v1/admin/shops/{shopID}/publishable-keys/{keyID}` 撤銷舊的。撤銷即時生效，過渡期內兩把都有效。

錯誤排查對照：

| 現象 | 意義 |
| --- | --- |
| 404 `unknown_publishable_key` | 金鑰打錯、未帶、或已被撤銷 |
| 404 `not_found`（前台 API，未帶金鑰時） | 落到 `Host` 解析而該網域未綁定——自帶前端一定要帶金鑰 |
| 瀏覽器 CORS 錯誤（伺服器沒有回錯） | origin 未登記；用 curl 帶同一組標頭會成功，這是判別依據 |
| 401 | 金鑰沒問題，是會員 token 缺少／過期／`aud` 不符 |
| 503 `shop_disabled` | 商家被停用（金鑰與網域同時失效） |
