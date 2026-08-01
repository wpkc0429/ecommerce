## Why

**商家看不到自己的顧客。** 平台沒有任何列出會員的端點——後台的點數畫面只能從某一張訂單反查到一位顧客（訂單詳情的「檢視此顧客點數」），或直接知道 member id 並手動輸入。「這個月有多少新顧客」「王小姐的 email 是哪一個」「誰是我的金卡會員」三個問題今天都無法在後台回答。

這不是被遺忘的缺口，是被寫下來的延後。`admin-commerce-operations` design D7 明文拒絕順手加一個 `GET /members`，理由是：

> 一份顧客名冊不是一個 `GET /members`：它要分頁、要搜尋、要決定回應裡放不放 email／電話／消費金額（那是 PII 揭露決策……），還要決定它算不算一個新的權限節點（`member.view` 今天不存在）。把這些塞進一個「補營運 UI」的 change 裡，做出來的會是一個看起來夠用、實際上沒想清楚 PII 邊界的端點，而且它一旦進 v1 就永遠拔不掉。

**本 change 就是那個獨立 change，它的主要交付物是那份 PII 揭露決策**（design D3–D7），端點與畫面只是它的結果。

## What Changes

**新增顧客名冊 API（兩條新路由，additive）**

- `GET /api/v1/admin/shops/{shopID}/members` — 分頁（`page`／`page_size`，上限 100）＋ 選填 `q`（email 的大小寫不敏感子字串；`q` 可解析為正整數時另比對 member id，與訂單列表的 `q` 逐字同型）。
- `GET /api/v1/admin/shops/{shopID}/members/{memberID}` — 單一顧客，與列表同一份序列化。

回應欄位：`id`、`shop_id`、`email`、`registered`、`status`、`points`、`level_id`、`level`、`created_at`、`updated_at`。**刻意不含 `phone`**（全樹沒有任何生產路徑寫入 `members.phone`——電話住在 `orders.shipping_address`，那是「這一筆的收件電話」而不是身分屬性，`guest-checkout` design D2 已定案），**刻意不含終身消費額之類的彙總**（design D5），**永不含 `password_hash`**（design D7）。

`registered` 是 `password_hash IS NOT NULL` 的布林投影，不是 hash 本身。它回答的是「這個人主動註冊過嗎／他能不能登入」——訪客結帳建立的無密碼身分兩者皆否，而 UI 必須說出這件事（design D6）。

**訂單列表新增選填 `member_id`（additive）**

`GET /api/v1/admin/shops/{shopID}/orders` 新增選填 `member_id`（精確比對）。它讓「這位顧客買過什麼」複用既有的分頁訂單列表與既有的訂單序列化，而不是在顧客端點裡再長一份訂單 JSON。不帶它時查詢逐行相同。

**新增權限節點 `member.view`（且只有這一個）**

加入權限目錄，授予 `merchant_owner` 與 `editor`（授予理由見 design D4——`editor` 今天已持有目錄裡每一個「商家自己的商業資料」的 `.view` 節點，而它經由 `order.view` 看得到的 PII 比名冊更寬）。兩條新路由皆以它為守衛。**不新增 `member.edit`**：平台今天沒有任何修改 `members` 的後台端點，一個沒有消費者的權限節點只會是一句謊言。

`GET /api/v1/admin/shops/{shopID}/me` 由權限目錄推導，因此 seed 後自動回報這個節點，admin 的 `lib/rbac.js` 不需要第二個來源。

**後台新增「顧客」視圖（一個分頁，兩個面貌）**

- `#/members` 名冊：id、email、註冊狀態徽章（已註冊／訪客身分）、點數、等級、建立時間、詳情。搜尋框直送伺服器。
- `#/members/{id}` 詳情：基本資料＋點數餘額與等級（唯讀）＋該顧客的訂單紀錄（每列連到 `#/orders/{id}`）。

**詳情刻意不內嵌點數調整表單也不內嵌流水帳**——那會是同一個小元件的第三次重寫（tripwire #2），而且會讓「調整後餘額／等級／流水帳」三處必須手工同步（tripwire #1）。點數的擁有者仍然是既有的 `#/points/{id}` 視圖，詳情只放一顆連過去的按鈕。

**修正兩處現在會說謊的文案**

`admin/views/points.js` 的檔頭與 `index.html` 的提示目前寫著「平台目前沒有顧客名冊端點，因此無法在此瀏覽全部會員」。本 change 之後那是假的。（與 `tier-discount-application` design D13 同型的義務：修好功能卻留下一個宣告功能不存在的畫面，等於沒修。）

**明確不做**

- **匯出按鈕**（design D5）。`member-identity-per-shop` 的「資料集自我完備、可匯出」講的是**平台層的資料可攜性**（租戶搬遷／刪除／個資請求），它的執行者是平台營運而不是後台的一顆按鈕。
- **`member.edit` / 停用顧客 / 編輯顧客資料**——沒有對應端點，見上。
- **顧客彙總指標**（訂單數、終身消費額）——那是一份報表，不是一個欄位（design D5）。
- **email 遮罩**——對 API 無效、對商家是純摩擦（design D3）。
- 任何資料庫 schema、migration、事件接線的變更。

## Capabilities

### New Capabilities
- `member-directory`: 商家瀏覽自己顧客名冊的行為契約——列表與搜尋、單一顧客的檢視、`member.view` 的授權邊界、租戶隔離、以及本 change 的核心：哪些顧客欄位揭露給後台、訪客身分如何呈現、為何沒有匯出。

### Modified Capabilities
- `order-management`: 商家訂單列表新增選填 `member_id` 篩選；既有分頁與其餘篩選語意不變。
- `admin-operations-ui`: 點數視圖新增「調整與流水帳由它獨佔擁有，其他視圖只能唯讀 ＋ 連過去」的約束；深層連結的帶參數視圖從兩個變成三個。

`rbac` **不列為 modified**：新增一個權限節點不改變 RBAC 的任何行為需求（三層判定、跨店守衛、`/me` 由目錄推導皆原樣成立）；節點本身的授權邊界寫在新 capability 裡。

`member-identity` **不列為 modified**：本 change 不改變顧客身分的任何性質，只是第一次讓商家看得到它們。

## Impact

- 新檔案：`admin/views/members.js`、`api/internal/httpapi/members.go`、`api/internal/httpapi/members_integration_test.go`。
- 修改（API，additive）：`api/internal/seed/seed.go`（`member.view` 進目錄與兩個角色）、`api/internal/order/service.go`（`AdminListParams.MemberID`）、`api/internal/httpapi/order.go`（讀取 `member_id`）、`api/internal/app/wire.go`（掛載新 handler）、`api/openapi/openapi.yaml`、`api/openapi/v1-surface.txt`。
- 修改（admin）：`admin/app.js`（視圖註冊表一列）、`admin/index.html`（一個 panel）、`admin/styles.css`、`admin/views/points.js` 與 `index.html` 的過期文案。
- 修改：`e2e/happy-path.mjs`（瀏覽器：名冊 → 搜尋 → 詳情 → 點數與訂單）、`e2e/commerce-path.mjs`（API：`member.view` 可見性、`password_hash` 不外洩、跨商家 403）。
- 修改：`openspec/changes/archive/…/design.md` **不動**（archive 是歷史），但 `CLAUDE.md` 的 architecture notes 需更新 tripwire 現況與營運缺口。
- 不動：資料庫 schema、`api/migrations/`、`atlas.sum`、events 接線、任何既有端點的既有行為、`web/`。
