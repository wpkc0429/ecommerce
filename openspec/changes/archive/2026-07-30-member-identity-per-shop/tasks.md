## 1. 前置查證

- [x] 1.1 確認 v1 尚無外部消費者：`bouquet` 專案 `public/app.js` 仍為硬編碼資料、無任何 API 呼叫；`admin` 與 `web` 為同倉庫同步部署（design D7 的破例前提）
      — 查證結果：`bouquet/` 僅含 `CLAUDE.md`/`README.md`/`openspec/`/`public/`，全樹 grep `fetch(`/`api/v1`/`axios`/`XMLHttpRequest` 零命中。`admin`/`web`/`e2e` 全樹 grep `shopMemberID`/`shop_member` 亦零命中（破壞性路徑更名無倉庫內消費者）。破例前提成立。
- [x] 1.2 查詢現況 `members` 是否存在孤兒列（無對應 `shop_member`），記錄筆數；若非零，確認來源後決定處置（design Open Questions）
      — 查證結果：`members` 0 列、`shop_member` 0 列、孤兒列 **0**。Open Question 落空（無資料需處置），migration 仍保留刪除孤兒列的敘述以防他機資料庫非空。
- [x] 1.3 查詢現況是否存在跨店身分（同一 `members` 對應多筆 `shop_member`），確認 migration 步驟 2 的 1:1 展開假設成立
      — 查證結果：跨店身分 **0** 筆（`point_transactions` / `orders` / `carts` 亦皆 0 列）。1:1 展開假設成立。
- [x] 1.4 盤點 `internal/` 下所有 `Member.Query()` / `ShopMember.Query()` 呼叫點，標註各自的執行上下文（租戶 / 後台 / 系統），作為 D4 風險的檢查清單

  | 呼叫點 | 執行上下文 | D4 處置 |
  | --- | --- | --- |
  | `httpapi/memberauth.go` Register（`Member.Query` / `Member.Create`） | 租戶（TenantMW） | 顯式加 `shop_id`；Create 顯式 `SetShopID`（hook 亦會覆寫） |
  | `httpapi/memberauth.go` Login（`Member.Query`） | 租戶 | 顯式加 `shop_id` |
  | `httpapi/memberauth.go` `Me`（`Member.Get`） | 租戶 | 依賴 interceptor 自動範圍化即可（`Get` 走 Query 路徑）；跨店 member_id 自動 404 |
  | `httpapi/memberauth.go` `ensureMembership`（`ShopMember.Query/Create`） | 租戶 | 整段移除（`shop_member` 不存在） |
  | `auth/refresh.go` `RotateMember`（`Member.Query`） | 租戶（refresh 端點掛在 TenantMW 下） | 顯式加 `shop_id`（不倚賴 interceptor，符合 D4） |
  | `points/service.go` `HandleOrderPaid` / `HandleOrderReturned` | **系統**（webhook 無租戶上下文；shipping 由後台觸發） | 顯式 `member.ShopIDEQ(ev.ShopID)` + `member.IDEQ(ev.MemberID)` |
  | `points/service.go` `AdjustPoints` / `GetMemberPointsAdmin` / `ListTransactionsAdmin` | **後台**（`/admin/shops/{shopID}`，無租戶上下文） | 顯式 `member.ShopIDEQ(shopID)`，shopID 來自 RBAC 授權後的路徑參數 |
  | `points/service.go` `GetMemberPointsSelf` / `ListTransactionsSelf` | 租戶 | 已顯式帶 `shop_id`，維持不變（雙重保險） |
  | `points/service.go` `recomputeLevel`（`Member.UpdateOneID`） | 隨呼叫者（系統／後台） | 目標 id 由上游 shop-scoped 查詢解出；租戶上下文中 hook 另加 `shop_id` 述詞 |
  | `rbac/{decide,engine}.go` 的 `IsShopMember` | — | 名稱巧合，指 `shop_user`（後台使用者的商家歸屬），與本變更無關 |

## 2. ent schema

- [x] 2.1 `internal/ent/schema/account.go`：`Member` 加 `shop_id` 欄位與指向 `Shop` 的 edge
- [x] 2.2 `internal/ent/schema/account.go`：`Member` 併入 `points`（Int32, Default 0）與 `level_id`（Int, Optional, Nillable）欄位及 `MemberTier` edge（SetNull 行為比照原 `ShopMember`）
- [x] 2.3 `internal/ent/schema/account.go`：移除 `email` / `phone` 的單欄 `.Unique()`，改以非唯一索引宣告（唯一約束交由手寫 migration，design D3）
      — 偏離說明：宣告的非唯一索引為 `index.Fields("shop_id")`，**不**宣告 `(shop_id, email)` / `(shop_id, phone)` 的非唯一版本。若宣告，會與手寫 migration 的 `UNIQUE(shop_id, email)` / `UNIQUE(shop_id, phone)` 產生欄位完全相同的重複 B-tree。D3 的意圖（唯一約束不由 ent 表達）已達成。
- [x] 2.4 `internal/ent/schema/account.go`：刪除 `ShopMember` entity
- [x] 2.5 `internal/ent/schema/points.go`：`PointTransaction.shop_member_id` → `member_id`，edge 改指 `Member`，索引改為 `(shop_id, member_id)`
- [x] 2.6 `internal/ent/schema/token.go`：`MemberRefreshToken.shop_id` 加註解，記錄其語意降級為可由 `member_id` 推導的快取值（design D5）
- [x] 2.7 執行 `go generate ./internal/ent`，確認產生的 client 可編譯
      — 另於 `internal/tenant/enttenancy.go` 的 `tenantOwned` 以 `"Member"` 取代 `"ShopMember"`（D4 的落地點）。

## 3. Migration

- [x] 3.1 以 `make migrate-gen name=member_identity_per_shop` 產生骨架 → `api/migrations/20260730152723_member_identity_per_shop.{up,down}.sql`
- [x] 3.2 改寫 up migration 為 design Migration Plan 的七步順序（加 `shop_id` nullable → 由 `shop_member` 回填 → 加 `points`/`level_id` 並回填 → `shop_id` 設 NOT NULL 加 FK → 換唯一索引 → `point_transactions` 欄位更名與 FK 重建 → DROP `shop_member`）
      — 依既有慣例移除 atlas diff 誤提的 `role_user` / `user_permission` NULLS NOT DISTINCT 約束 DROP/re-ADD；diff 不會產生 `DROP TABLE shop_member` 與任何資料搬移，皆為手寫。另補 design 未提及但必要的兩步：(a) 開頭以 `DO $$` 檢查跨店身分，非零即 `RAISE EXCEPTION` 中止（避免 1:1 回填在多筆匹配時無聲取任一筆）；(b) 步驟 6 更名後必須把欄位值由 `shop_member.id` 換算為 `members.id`，否則 FK 會指向錯誤的會員。
- [x] 3.3 唯一索引以手寫 SQL 建立：`UNIQUE(shop_id, email)`、`UNIQUE(shop_id, phone)`，維持 Postgres 預設 NULLS DISTINCT 語意，**不套用 `NULLS NOT DISTINCT`**（design D3）
      — 明寫 `NULLS DISTINCT` 關鍵字（PG15+）而非仰賴預設，讓意圖無法被誤讀。實測：同店兩筆 `email IS NULL` 允許、同店重複 email 被 `uq_members_shop_email` 拒絕、跨店同 email 允許。
- [x] 3.4 撰寫 down migration：反向重建 `shop_member` 與原唯一約束；於註解記錄「無法還原跨店身分合併語意」的限制
- [x] 3.5 `make migrate` 於乾淨 dev DB 套用成功，`make migrate-down` 可回滾
      — 以帶資料的 dev DB 驗證 up → down → 資料還原（含孤兒身分刪除、points/level_id 搬移、`point_transactions` 值換算）；並驗證跨店身分守衛觸發時整份 migration 於單一交易內回滾，schema 未被改動。
- [x] 3.6 migration 後對帳：對每一 shop 比對 `SUM(point_transactions.points_delta)` 與 `members.points`，全數相符
      — 對帳寫成 up migration 結尾的 `DO $$` 區塊（逐會員比對，因兩側在遷移後皆已 shop-scoped），不符即 `RAISE EXCEPTION` 中止。
- [x] 3.7 確認 `atlas.sum` checksum 同步（手改 up.sql 後重算）

## 4. 認證與身分（spec: member-identity, authentication）

- [x] 4.1 `internal/httpapi/memberauth.go`：`Register` 的 email/phone 查詢加入 `shop_id` 條件
- [x] 4.2 `internal/httpapi/memberauth.go`：移除「既有平台身分加入本店需驗證密碼」的 `default:` 分支；本店 email 已存在一律走統一的 `registerFailed`
- [x] 4.3 `internal/httpapi/memberauth.go`：移除 `ensureMembership`（`shop_member` 已不存在），身分建立時直接寫入 `shop_id`
- [x] 4.4 `internal/httpapi/memberauth.go`：`Login` 的查詢加入 `shop_id` 條件（不再 auto-join，他店憑證於本店一律 401）
- [x] 4.5 確認 `registerFailed` 與 `auth.EqualizeVerifyTiming` 的同構回應與時序防護在新流程下仍成立（現在防護的是「本店帳號存在性」，design D8）
      — 舊流程的「既有身分」分支會付出一次 Argon2id 驗證成本；新流程直接拒絕會使失敗遠快於成功（成功需一次 Argon2id 雜湊），形成計時側通道。故於重複註冊分支補呼叫 `auth.EqualizeVerifyTiming()`。
- [x] 4.6 `internal/auth/refresh.go`：`IssueMember` / `RotateMember` / `RevokeMember` 的 `Member` 查詢加入 shop 條件，確認輪替與撤銷鏈行為不變
      — 三者中僅 `RotateMember` 查詢 `Member`（已加 `member.ShopIDEQ(shopID)`）；`IssueMember` / `RevokeMember` 只碰 `member_refresh_tokens`，本就以 `shop_id` 為條件，無須改動。

## 5. 點數（spec: member-tiers-and-points）

- [x] 5.1 `internal/points`：Service 介面與內部型別由 `shopMemberID` 改為 `memberID`（回傳型別由 `*ent.ShopMember` 改為 `*ent.Member`）
- [x] 5.2 `internal/points`：餘額快取的讀寫目標由 `shop_member.points` 改為 `members.points`，維持「同一交易內同步更新」不變式
- [x] 5.3 `internal/points`：`level_id` 重新評估邏輯的目標改為 `members`
- [x] 5.4 `internal/httpapi/points.go`：後台三端點路徑參數 `{shopMemberID}` → `{memberID}`，含 `intParam` 取值處
- [x] 5.5 確認會員自助端點（`GET /api/v1/shop/points{,/transactions}`）的請求與回應 JSON 形狀完全未變（design D7 範圍限制）
      — **查證結果與 design 假設不符**：`shop_member_id` 確實出現在回應 body（`shopMemberPointsJSON` 與 `pointTransactionJSON` 皆輸出），故亦出現在這兩個自助端點的回應。因該鍵指向已刪除的資料表、且其值本即等同 member 識別碼，改為：餘額回應移除 `shop_member_id`（該回應本已含 `member_id`），流水帳回應 `shop_member_id` → `member_id`。請求形狀、存取判定與分頁語意不變。已同步更正 design.md 的「已解決」條目與 member-tiers-and-points spec 該句。倉庫內外皆無消費端（`admin`/`web`/`e2e` 全樹無點數端點呼叫）。
- [x] 5.6 `internal/events`：確認 `OrderPaymentSucceeded` / `OrderReturned` 的訂閱端在改用 `member_id` 後仍正確解析目標會員
      — 事件 payload 本就帶 `ShopID` + `MemberID`（`orders.member_id` 一向指向 `members`），故事件定義無須改動；訂閱端由「以 (shop_id, member_id) 查 `shop_member`」改為「以 (id, shop_id) 查 `members`」。

## 6. 租戶隔離（spec: multi-tenancy）

- [x] 6.1 依 1.4 的清單，逐一確認每個 `Member` 查詢在新的 interceptor 管轄下行為正確
- [x] 6.2 後台路徑（非租戶上下文）對 `members` 的查詢一律顯式帶 `shop_id`，不依賴 interceptor（design D4）
- [x] 6.3 確認 `internal/order`、`internal/cart` 對 `members` 的參照在租戶上下文中正確範圍化，既有行為不變
      — 兩者皆不查詢 `members`（只存 `orders.member_id` / `carts.member_id` 並以 `shop_id` + `member_id` 為條件查自己的表），故無程式碼改動；既有整合測試全綠即為回歸證據。

## 7. Seed 與測試

- [x] 7.1 `internal/seed`：demo 會員資料改為每店身分，移除 `shop_member` 建立邏輯
      — 無須改動：seed（含 `-demo`）從未建立任何 `members` / `shop_member` 列（僅建 shop / site / 頁面 / 目錄 / 後台使用者）。
- [x] 7.2 `internal/httpapi/authflow_integration_test.go`：更新註冊/登入情境，新增「他店 email 於本店可自由註冊」「他店密碼無法登入本店」「本店 email 重複註冊被拒」三個案例
      — 拆成 `TestMemberRegistrationIsShopScopedAndIsomorphic`、`TestMemberCredentialsDoNotCrossShops`、`TestMemberLoginScopeAndTokenIsolation`（原 `...AutoJoin...` 的自動加入會籍斷言已無意義而移除）。
- [x] 7.3 `internal/httpapi/points_integration_test.go`：更新為 `memberID` 路徑，回歸驗證冪等核發、封頂扣回、等級重評
- [x] 7.4 `internal/httpapi/tenancy_integration_test.go`：新增「租戶上下文自動範圍化 `members` 查詢」與「後台跨商家查詢會員回 403」案例
      — 前者新增於 `TestMemberIdentityIsShopScoped`（含以租戶上下文、無述詞的 delete 由 hook 自動範圍化）。後者留在 `points_integration_test.go` 的 `TestPointsAdminCrossShopIsolation`（403）與 `TestPointsAdminUnknownMemberIsNotFound`（404）：`tenancyEnv` 未接 RBAC/points 佈線，搬過去等同複製一份 `pointsEnv`。
- [x] 7.5 新增測試涵蓋 spec member-identity 的「同商家內多筆 email 為 NULL 被允許」
- [x] 7.6 新增測試涵蓋「刪除單一商家顧客資料不影響其他商家」（資料集自我完備）
- [x] 7.7 `make test` 全綠
- [x] 7.8 `make test-int` 全綠
      — 另修 `TestRateLimitShopScopeIsolation`：它原本以「同一 email 於同店連續註冊兩次皆回 200」來耗用配額，此前提隨註冊語意改變而失效；改為斷言兩次皆非 429（配額照樣被耗用），測試本意（跨商家限流互不干擾）不變。
- [x] 7.9 `make lint` 無新增問題（`golangci-lint run` → 0 issues）

## 8. 收尾

- [x] 8.1 `cd e2e && node happy-path.mjs` 於 `make seed-demo` 後的乾淨狀態通過
- [x] 8.2 檢查 `admin` SPA 是否有引用後台點數端點路徑，如有則同步更新
      — 無：`admin`/`web`/`e2e` 全樹 grep `shop_member` / `shopMemberID` / points 端點皆零命中，無須更新。
- [x] 8.3 更新 `CLAUDE.md` 的架構註記，記錄顧客身分以商家為界的不變式
