## Context

Phase 1 將 `members` 設計為平台級單一消費者身分（design.md 表格第 83 行：「email/phone 全平台唯一為刻意設計」），每店會籍由 `shop_member` 承載。風險登記表（同檔第 215 行）僅記錄了「跨店帳號存在性洩漏面」並以同構回應與速率限制緩解，未涵蓋憑證影響半徑。

現況參照圖：

```
Member (平台級，無 shop_id)
  ├── Order.member_id            ──▶ Member    （註解明寫 "not ShopMember"）
  ├── Cart.member_id             ──▶ Member
  └── MemberRefreshToken.member_id ──▶ Member  （另存 shop_id 欄位）

ShopMember (shop_id, member_id)  ← 每店會籍
  ├── points, level_id
  └── PointTransaction.shop_member_id ──▶ ShopMember
```

電商實體指向平台級 `Member`、忠誠度實體指向 `ShopMember`，兩種模式並存。因 `Member` 無 `shop_id`，`internal/tenant` 的 interceptor 不會對其自動附加範圍，`Order.QueryMember()` 實際上穿過了租戶邊界。

驅動時機：`storefront-client-auth` 與 `guest-checkout` 皆以此為前置。後者（訪客結帳自動以 email 建立身分）在現行模型下會直接撞上 `memberauth.go` 中「既有平台身分加入本店必須驗證密碼」的分支——該分支存在的理由正是防止「知道某人 email 就能在任意商家以其身分建立會籍」。本變更移除該衝突的根源，而非繞過它。

約束：Postgres 16；ent + versioned migrations（golang-migrate）；ent 無法表達的約束以手寫 SQL 補（既有慣例，如 `UNIQUE NULLS NOT DISTINCT`）；尚無正式環境，`members` 僅有 seed-demo 資料。

## Goals / Non-Goals

**Goals:**

- 顧客憑證的影響半徑收斂至單一商家：任一商家（或其自行撰寫的前端）取得的明文密碼，MUST NOT 可用於其他商家。
- 一家商家的顧客資料集自我完備：`WHERE shop_id = X` 即為該商家全部顧客資料，使租戶搬遷／刪除／匯出（含個資刪除請求）可乾淨執行。
- 消除「註冊失敗但無法向顧客解釋原因」的死路。
- 使 `members` 與系統其餘每一層的隔離粒度一致（points、JWT audience、速率限制早已是每店獨立）。
- 讓 `orders` / `carts` 對 `members` 的參照落入租戶隔離管轄。

**Non-Goals:**

- 不實作 partitioning。本變更僅解除 DDL 層的封鎖（見 D6）。
- 不引入 email 發送、email 驗證或 magic link。
- 不變更會員 JWT 結構、audience 規則或 refresh token 輪替機制。
- 不變更會員自助 API（`/api/v1/shop/*`）的請求／回應形狀。
- 不處理既有跨店身分的資料合併策略——現況無此資料（僅 seed-demo），migration 直接以 `shop_member` 展開。

## Decisions

### D1. 每店獨立身分，而非「平台身分 + 每店憑證」

`members` 取得 `shop_id`，唯一約束改為 `UNIQUE(shop_id, email)` / `UNIQUE(shop_id, phone)`。同一 email 於兩家商家為兩筆完全獨立的列。

**替代方案：保留平台級 `Member` 作為身分錨點，將 `password_hash` 下放至 `shop_member`。** 可解決密碼共用，但 email/phone 仍全平台唯一，因此：(a) 跨商家帳號存在性洩漏面依然存在；(b)「既有身分加入新店」的語意仍需保留，註冊流程仍有兩條分支；(c) 商家資料集仍不自我完備，租戶搬遷／刪除仍會牽動其他商家。只解決三個問題中的一個，且留下最複雜的一半。

**選擇理由**：平台級身分目前未換到任何已實現的產品能力。points、JWT audience（`aud=shop:{id}`，跨店重用回 401）、速率限制（spec 明文要求跨商家計數獨立）都已是每店獨立；共享的僅有 email/phone 唯一性與密碼——恰為問題來源本身。此定位（多網站、各自品牌與網域）近於 Shopify 的每店顧客名單，而非 Shopee 的單一平台帳號。

**代價**：放棄「一個帳號逛所有商家」。此能力目前無人使用，且與現行定位相斥；若 Phase 3+ 需要平台入口，屆時應以獨立的「平台帳號 ↔ 商家顧客」連結表實作，而非回退本決定。

### D2. 合併 `shop_member` 進 `members`，而非保留為 1:1 表

`members` 併入 `points` / `level_id`，`shop_member` 刪除，`point_transactions.shop_member_id` → `member_id`。

**替代方案：保留 `shop_member` 不動。** 改動面小得多——`internal/points` 完全不必更動，後台 API 路徑不變，D13 的 additive 約束不必破例。

**選擇理由**：`Member` 一旦具備 `shop_id`，`shop_member` 即與其嚴格 1:1，成為永久冗餘。保留它會讓「為何 Member 已是每店的卻仍有 shop_member」成為每個後續功能都要重新支付的認知成本，且每次讀取點數多一次 join。此外，破壞性 API 變更的窗口正在關閉：v1 目前無任何外部消費者，ONDO 接入後即不再具備此條件（見 D7）。

**代價**：`internal/points`（最近完成的變更）需全面調整；後台三個點數端點的路徑參數更名。

### D3. 唯一約束以手寫 SQL 表達 NULL 語意

`email` / `phone` 為 nullable。per-shop 之後的 `UNIQUE(shop_id, email)` 在 Postgres 預設語意下，`(1, NULL)` 與 `(1, NULL)` 視為相異，允許同一商家存在多筆 email 為 NULL 的顧客——這是正確的（phone-only 註冊、未來的訪客身分皆需要）。

因此本設計**維持 Postgres 預設的 NULLS DISTINCT 行為**，不套用 `NULLS NOT DISTINCT`。此處明確記錄，是因為既有 migration 中存在使用 `UNIQUE NULLS NOT DISTINCT` 的先例（`site_shop` 的預設商家約束），避免實作時誤套。

由於 ent 對「複合唯一索引 + nullable 欄位」的表達與實際 DDL 存在落差，唯一索引以手寫 migration 建立，ent schema 端僅宣告欄位與非唯一索引，比照專案既有慣例。

### D4. `members` 納入租戶 interceptor，後台路徑以顯式 shop 條件查詢

`members` 取得 `shop_id` 後，`internal/tenant` 的 interceptor/hook 會在租戶上下文中自動附加 `shop_id` 條件。這正是期望行為：`orders.member_id` / `carts.member_id` 的參照隨之落入隔離管轄。

但後台 API 不在租戶上下文中執行——其目標 shop 由 URL 的 `{shopID}` 加 RBAC 判定（multi-tenancy spec：「後台 API 的目標 shop MUST 由授權上下文決定、不得信任 client 傳入的 shop 識別」）。因此後台對 `members` 的查詢 MUST 顯式帶 `shop_id` 條件，不得依賴 interceptor。

此為本變更的**主要風險點**：`members` 從「不受 interceptor 管轄」變為「受管轄」，任何既有的無條件 `Member.Query()` 在租戶上下文中行為會改變。實作時需逐一檢視 `internal/auth`、`internal/httpapi/memberauth.go`、`internal/points` 中所有 `Member` 查詢並確認其執行上下文。

### D5. `member_refresh_tokens.shop_id` 保留

該欄位在本變更後可由 `member_id` 推導，成為冗餘。保留之，理由：(a) 既有輪替／撤銷查詢以 `(shop_id, token_hash)` 為索引，移除會迫使多一次 join；(b) refresh token 的撤銷路徑是安全敏感程式碼，本變更不觸碰其邏輯以縮小風險面。語意上降級為「快取的推導值」，於 schema 註解記錄。

### D6. Partitioning 僅解除封鎖，不實作

現行 `CREATE UNIQUE INDEX members_email_key ON members (email)` 使 `PARTITION BY ... (shop_id)` 在 DDL 層即被拒——Postgres 要求 partitioned table 的唯一約束與主鍵必須包含 partition key。本變更的 `UNIQUE(shop_id, email)` 解除此封鎖。

實際分割另需：主鍵改為 `(shop_id, id)`；`orders` / `carts` / `point_transactions` 的外鍵改為複合外鍵 `(shop_id, member_id) → members(shop_id, id)`（partitioned table 上不可能存在單獨 `members(id)` 的唯一約束）。這些不在本變更範圍內。

**明確記錄**：`members` 並非最先觸及規模瓶頸的表。其查詢型態為 `(shop_id, email)` 或 `id` 的 point lookup，B-tree 索引已屬最適；成長速度為「每顧客一列」，較 `order_items` / `point_transactions` 等「每筆交易一列」的 append-only 表低一至兩個數量級，而後者早已具備 `shop_id`、隨時可分割。partitioning 不應作為本變更的理由，僅為零成本的附帶保留選項。

### D7. 破壞性 v1 變更的正當性與範圍

design D13 規定 v1 僅接受 additive 變更。本變更破例，破壞性面向有二：

1. 後台點數三端點的路徑參數 `{shopMemberID}` → `{memberID}`；
2. 註冊行為改變（同一 email 在不同商家不再沿用身分）。

**正當性**：v1 目前的消費端僅有本 monorepo 內的 `admin` SPA 與 `web` storefront，兩者與 API 同倉庫同步部署；外部消費者（ONDO）尚未接入。破例成本因此為零。ONDO 接入後此條件即消失，屆時同等變更需走 v2 或 additive 遷移路徑。

**範圍限制**：破例僅及於上述兩項。會員自助端點（`/api/v1/shop/*`）的請求與回應形狀 MUST 保持不變，確保後續 `guest-checkout` 與 ONDO 的接入面不受本變更影響。

### D8. 註冊回應同構要求的理由改變，要求本身保留

現行 spec 要求註冊回應「不得洩漏該帳號已存在於其他商家」。per-shop 之後不存在「其他商家」的洩漏面，但同構要求仍保留——理由改為「不得洩漏該 email 是否為本店顧客」。

這是所有電商註冊表單皆有的固有洩漏面，無法根除（任何註冊流程都必須對重複 email 有所反應），但影響半徑已由全平台收斂至單店，屬可接受水準。既有的 `registerFailed` 統一拒絕回應與速率限制沿用。

## Risks / Trade-offs

- **[`members` 納入 interceptor 後，後台既有查詢在非租戶上下文中行為改變]** → 逐一檢視所有 `Member.Query()` 呼叫點並標註其執行上下文（D4）；後台查詢一律顯式帶 `shop_id`；以整合測試覆蓋「後台跨店查詢會員回 403/404」與「租戶上下文自動範圍化」兩個方向。

- **[`shop_member` → `members` 的資料搬移遺漏 points/level_id，造成點數餘額歸零]** → migration 中先 `ALTER TABLE members ADD COLUMN points/level_id`、再由 `shop_member` UPDATE 回填、最後才 DROP `shop_member`，三者於同一 migration 交易內完成；回填後以 `point_transactions` 加總對帳作為 migration 後驗證步驟。

- **[`point_transactions` 外鍵改指 `members` 期間，流水帳與餘額快取不一致]** → `internal/points` 的「同一交易內同步更新餘額」不變式（member-tiers-and-points spec）在本變更中 MUST 維持；調整後以既有的冪等性與封頂測試回歸驗證。

- **[放棄跨店身分，未來若需平台入口將無法直接回退]** → 已於 D1 記錄補救路徑（獨立的平台帳號 ↔ 商家顧客連結表）。此路徑不要求 `members` 回到平台級，故本決定不構成死路。

- **[破壞性 v1 變更的窗口判斷若有誤（存在未知的 v1 消費者）]** → 本 monorepo 外唯一預期消費者 ONDO 尚未開始接入（bouquet 專案的 `public/app.js` 現為硬編碼資料，無任何 API 呼叫）；`admin` 與 `web` 同倉庫同步部署。實作前確認此前提仍成立。

- **[trade-off：`internal/points` 為最近完成的變更，本次再度大幅調整]** → 接受。理由為 D2：保留 `shop_member` 的長期認知成本高於一次性調整成本，且此為成本最低的時機。

## Migration Plan

單一 versioned migration（`api/migrations/`），順序：

1. `ALTER TABLE members ADD COLUMN shop_id bigint`（暫時 nullable）。
2. 由 `shop_member` 回填 `members.shop_id`。**現況每筆 `members` 至多對應一筆 `shop_member`（無跨店身分資料），故為 1:1 展開**；若回填後存在 `shop_id IS NULL` 的孤兒列（無任何會籍的身分），一併刪除。
3. `ALTER TABLE members ADD COLUMN points integer NOT NULL DEFAULT 0, ADD COLUMN level_id integer NULL`，由 `shop_member` 回填。
4. `ALTER TABLE members ALTER COLUMN shop_id SET NOT NULL`，加 FK 至 `shops`。
5. `DROP INDEX members_email_key, members_phone_key`；建立 `UNIQUE(shop_id, email)`、`UNIQUE(shop_id, phone)`（NULLS DISTINCT，見 D3）。
6. `ALTER TABLE point_transactions RENAME COLUMN shop_member_id TO member_id`，重建 FK 指向 `members`，重建 `(shop_id, member_id)` 索引。
7. `DROP TABLE shop_member`。

**驗證步驟**（migration 後、部署前）：對每一 shop 比對 `SUM(point_transactions.points_delta)` 與 `members.points`，任何不符即中止。

**Rollback**：`.down.sql` 反向重建 `shop_member` 並還原唯一約束。但 down migration **無法還原跨店身分的合併語意**（若有兩筆 per-shop 列源自同一 email，反向時無從判定應合併為一筆平台身分）——現況無此資料故安全；此限制於 down migration 註解中記錄。

**部署**：無正式環境，隨 `make migrate` 一次套用。ONDO 尚未接入，無需協調外部消費者。

## Open Questions

- `members` 的孤兒列（存在 `members` 但無任何 `shop_member`）在現況 seed-demo 資料中是否存在？migration 步驟 2 假設可直接刪除；實作時先以查詢確認筆數，若非零需確認其來源後再決定處置。

**已解決**（提案階段查證）：

- ~~`shop_member_id` 是否曝光於對外回應 JSON？~~ 否。`internal/httpapi/points.go` 中該識別碼僅作為後台路徑參數（`shopMemberID`）出現，未進入任何回應 body（回應僅含 `points_delta`、`reason` 等欄位）。D7「會員自助端點回應形狀不變」的約束因此可乾淨達成，更名範圍侷限於後台路徑與內部型別。

  **實作期更正（此項查證有誤）**：`shop_member_id` **確實**出現在回應 body——`shopMemberPointsJSON` 與 `pointTransactionJSON` 皆輸出該鍵，因此 `GET /api/v1/shop/points{,/transactions}` 這兩個會員自助端點的回應也含有它。D7「回應形狀完全不變」的前提因此不成立：`shop_member` 表已刪除，該鍵已無所指。處置：於兩個序列化函式中將 `shop_member_id` 改為 `member_id`（餘額回應本已含 `member_id`，故實為移除死鍵；流水帳回應為更名），並同步修正 member-tiers-and-points spec 中該句。理由與 D7 破例本身一致——v1 尚無任何消費端（已再次確認 `admin`/`web`/`e2e` 全樹無點數端點呼叫），此刻更正成本為零；保留一個指向已刪除資料表的欄位名，其長期認知成本正是 D2 所反對者。
