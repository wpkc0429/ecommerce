## 1. 前置：tripwire #4 的重評與量測（**必須在寫任何 admin code 之前完成**）

- [x] 1.1 實測 `admin/` 總行數（JS ＋ HTML ＋ CSS）並記錄基準值（`tier-discount-application` 後實測 2,956；tripwire #4 的界線是 ~3000）
- [x] 1.2 實測 `lib/`／`app.js`／`views/` 的逐 commit 演進（`admin-commerce-catalog` → `admin-commerce-operations` → 今天），得出「共用層增量 ÷ 視圖增量」的歷史值
- [x] 1.3 在 design.md D1 完成 (b)〔建置步驟＋框架〕與 (c)〔維持現狀〕的重新評估，用 1.1／1.2 的實測值論證，並定案 #4 的處置（取代／保留／改門檻）
- [x] 1.4 在 design.md D1 寫下取代後的 tripwire（#4′ 共用層佔比、#5 單一視圖檔行數）與它們的當前數值

## 2. 權限節點

- [x] 2.1 `api/internal/seed/seed.go`：`PermissionCatalog` 新增 `{"member.view", "檢視顧客名冊"}`，並附註為何沒有 `member.edit`（design D4：沒有消費者的節點不新增）
- [x] 2.2 同檔：`merchant_owner` 與 `editor` 的 `Perms` 各加入 `member.view`，`editor` 那筆附上授予理由的註解（design D4）
- [x] 2.3 確認 `seedPermissions`／`seedRoles` 對既有資料庫是冪等 upsert（既有角色會被補上缺少的 `role_permission` 列），因此**不需要 migration**

## 3. API：顧客名冊（新 package ＋ handler）

- [x] 3.1 新增 `api/internal/member/service.go`：`Service{Client}`、`ListParams{Page, PageSize, Q}`、`Page{Members, Total, Page, PageSize}`、`ErrNotFound`
- [x] 3.2 `Service.ListMembers(ctx, shopID, params)`：**顯式** `entmember.ShopIDEQ(shopID)`、`WithMemberTier()` 預載等級、`Q` 對 email `ContainsFold`（`Q` 可解析為正整數時 OR 比對 `IDEQ`）、`normalizePage` 上限 100、依 `id` 排序
- [x] 3.3 `Service.GetMember(ctx, shopID, memberID)`：同樣顯式帶 `shop_id`，查不到回 `ErrNotFound`（design D8：他店的 id 是 404 不是 403）
- [x] 3.4 package doc 寫明「後台不在租戶上下文，每個方法第一個參數就是 shopID，且**不提供**任何不帶 shopID 的重載」（design D8）
- [x] 3.5 新增 `api/internal/httpapi/members.go`：`MembersHandler`、`MountShopAdmin`（兩條路由皆 `RequireShopPermission("member.view")`）、手寫 `memberJSON`（**不含** `password_hash`、**不含** `phone`、**不含**任何彙總；`registered` = `password_hash != nil`）
- [x] 3.6 `memberJSON` 的註解寫明每一個欄位為何在／為何不在（design D3／D6／D7），以及為何 `registered` 不叫 `has_password`
- [x] 3.7 `api/internal/httpapi/router.go`：`Deps` 新增 `Members *MembersHandler` 並掛進 `/admin/shops/{shopID}` 群組
- [x] 3.8 `api/internal/app/wire.go`：建構 `member.Service` 與 `MembersHandler` 並接線
- [x] 3.9 確認 `chi.Walk` 看得到兩條新路由（`openapi-contract` 的已知盲點：同一樣板既有子 router 又有方法處理器）

## 4. API：訂單列表的 `member_id`（additive）

- [x] 4.1 `api/internal/order/service.go`：`AdminListParams` 新增 `MemberID *int`，附註它存在的理由（design D2：避免在顧客端點長出第二份訂單序列化）
- [x] 4.2 `ListOrdersAdmin`：新增條件式 `if params.MemberID != nil { q = q.Where(entorder.MemberIDEQ(*params.MemberID)) }`
- [x] 4.3 `api/internal/httpapi/order.go`：`listAdmin` 讀 `optionalIntQuery(r, "member_id")`
- [x] 4.4 確認不帶 `member_id` 時查詢逐行相同（`git diff` 核對）

## 5. 契約與表面鎖

- [x] 5.1 `api/openapi/openapi.yaml`：新增 `GET /api/v1/admin/shops/{shopID}/members` 與 `GET /api/v1/admin/shops/{shopID}/members/{memberID}`，含 `Member`／`MemberPage` 具名 schema；`Member.description` 寫明不含 `phone`／`password_hash` 與其理由
- [x] 5.2 同檔：`listOrdersAdmin` 新增選填 `member_id` 參數
- [x] 5.3 以 `-openapi.update-surface` 重生 `v1-surface.txt`，逐行檢查 diff **只有新增、零刪除**
- [x] 5.4 `make openapi` 四道全綠

## 6. 整合測試（API 層）

- [x] 6.1 `api/internal/httpapi/members_integration_test.go`：列表回傳僅本商家顧客、分頁、`page_size` 被夾到 100
- [x] 6.2 `q` 的三種比對：email 子字串（大小寫不敏感）、正整數比對 member id、無命中回零筆
- [x] 6.3 **`password_hash` 不外洩**：對兩條端點的**原始回應主體字串**斷言不含 `password_hash`、不含 `$2a$`／`$2b$`、不含 `phone`（design D7）
- [x] 6.4 `registered`：訪客身分（`password_hash IS NULL`）為 false、有密碼者為 true
- [x] 6.5 授權：不具 `member.view` 的角色對兩條端點皆回 403
- [x] 6.6 租戶隔離：跨商家請求列表回 **403**；具本店權限但指定他店 member id 回 **404** 且回應不含該顧客任何欄位
- [x] 6.7 `GET /shops/{shopID}/me` 對持有者回報 `member.view`。**實作時改了位置**：`meInShop` 由權限目錄推導、既有測試已證明該機制，所以真正需要證明的是「節點確實進了目錄與兩個角色」——那是 seed 的事實，覆蓋在 `internal/seed/seed_integration_test.go`（並一併斷言 `member.edit` **不存在**），加上 `e2e/commerce-path.mjs` 以真實 seed ＋ 真實伺服器、以**商家 owner** 身分斷言 `/me` 含 `member.view`。在 `catalogEnv` 裡再造一份目錄只會重測既有機制
- [x] 6.8 `order_integration_test.go`：`member_id` 篩選只回該顧客的訂單、以他店顧客 id 篩選回零筆、**不帶 `member_id` 時結果與過去相同**

## 7. 後台 UI

- [x] 7.1 `admin/views/members.js`：名冊（`rows()` ＋ `renderPager()` ＋ 搜尋框直送伺服器）與詳情（基本資料／點數唯讀＋連往 `#/points/{id}`／訂單列表由 `GET /orders?member_id=` 取得）
- [x] 7.2 檔頭註解寫明三件事：點數為何不在這裡調整（design D9 與 tripwire #1／#2）、`formatPoints` 不得換成 `formatMoney`、以及訪客身分的呈現規則
- [x] 7.3 `admin/index.html`：`tab-members` 與 `tab-member-detail` 兩個 panel；名冊頂端寫明訪客身分未註冊、未同意接收行銷訊息（design D6）
- [x] 7.4 `admin/app.js`：視圖註冊表新增 `members`（`perm: "member.view"`）。**沒有第二個註冊表項目**：與 `orders`／`tab-order-detail` 同型，詳情 panel 由視圖自己 `ctx.showPanel` 切過去，一個項目兩個面貌（`#/members` 與 `#/members/57`）
- [x] 7.5 `admin/styles.css`：徽章樣式（已註冊／訪客身分）
- [x] 7.6 修掉過期文案：`admin/views/points.js` 檔頭與 `admin/index.html` 的 `#points-hint`（現在寫著「平台目前沒有顧客名冊端點」，本 change 之後是假的）
- [x] 7.7 訂單詳情的「顧客」列改為連往 `#/members/{member_id}`（`#/points/{id}` 的按鈕保留）
- [x] 7.8 **核對 `admin/lib/` 一行未改**（design D1 的預測）

## 8. E2E

- [x] 8.1 `e2e/happy-path.mjs`：在 ⑱.5 之後插入顧客名冊區段（搜尋該訪客 email → 詳情 → 34 點且不含 `NT$` → 銀卡會員 → 兩筆訂單 → 跳到點數視圖），後續節次編號順移
- [x] 8.2 `e2e/commerce-path.mjs`：商家 owner 身分的 API 斷言——`/me` 含 `member.view`、`GET /members` 200 且搜得到、**原始回應文字不含 `password_hash`／bcrypt 前綴／`phone`**、跨商家 `GET /admin/shops/{other}/members` 回 403
- [x] 8.3 確認新區段不建立也不刪除任何資料、不新增登入（登入限流）

## 9. 驗證與收尾

- [x] 9.1 `make test` 全綠
- [x] 9.2 `make test-int` 全綠（`make up`，串行 `-p 1`）
- [x] 9.3 `make lint` 無新增問題
- [x] 9.4 `make openapi` 四道全綠
- [x] 9.5 `make migrate` 為 **no-op**，`api/migrations/` 與 `atlas.sum` 未變更
- [x] 9.6 重置 DB → `make migrate && make seed-demo` → 三服務啟動 → `cd e2e && node happy-path.mjs` 全綠
- [x] 9.7 **實作後再量一次 `admin/` 行數**，與 1.1 的基準值、#4′ 與 #5 的門檻一起寫進交付回報（design D1）
- [x] 9.8 更新 `CLAUDE.md` 的 architecture notes：顧客名冊、`member.view`、PII 決策要點、tripwire 現況（#4 已被 #4′／#5 取代）、以及更新後的營運缺口清單
