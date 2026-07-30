## Why

`members` 是整個 schema 裡唯一一張跨租戶共享列的表：email/phone 全平台唯一，密碼因此也是全平台共用。Phase 1 design.md 明確記載這是刻意設計，但風險登記表只分析了「跨店帳號存在性洩漏」一面，**沒有分析憑證影響半徑**——顧客在 A 商家設定的密碼，在平台上每一家商家都能登入。

即將接入的第一個外部前端（ONDO 溫度花室）是商家自行撰寫、部署在自家網域的靜態站，**由商家自己的程式碼收取明文密碼**。這讓上述風險從「語意上怪」變成「單一商家被入侵即波及全平台顧客」。同時它也產生一個無法對顧客解釋的死路：顧客在 ONDO 註冊失敗，真正原因是他曾在另一家不相干的商家註冊過，而該原因按設計不得揭露。

平台級身分目前並未換到任何已實現的產品能力——points、JWT audience、速率限制都早已是每店獨立；共享的只有 email/phone 唯一性與密碼，而這三者正是問題來源。此外，`members` 缺少 `shop_id` 使得「一家商家的資料集」無法自我完備，租戶搬遷／刪除／匯出（含個資刪除請求）都無法乾淨執行。

現在改的成本接近於零：尚無正式環境、`members` 僅有 seed-demo 資料、v1 API 尚無任何外部消費者。等 ONDO 上線並累積真實顧客後，此改動將升級為需要協調停機與顧客重設密碼的資料遷移專案。

## What Changes

- **BREAKING** `members` 改為每店獨立顧客身分：新增 `shop_id`，唯一約束由 `UNIQUE(email)` / `UNIQUE(phone)` 改為 `UNIQUE(shop_id, email)` / `UNIQUE(shop_id, phone)`。同一 email 於不同商家為兩筆互不相干的身分，密碼各自獨立。
- **BREAKING** 移除「既有平台身分加入第二家店」的註冊路徑。註冊一律在該商家範圍內判定 email/phone 是否已使用；不再存在需要驗證既有密碼才能建立會籍的分支。
- **BREAKING** 刪除 `shop_member` 表，`points` / `level_id` 合併回 `members`。`point_transactions.shop_member_id` 改為 `member_id`。
- **BREAKING** 商家後台點數 API 路徑由 `/api/v1/admin/shops/{shopID}/members/{shopMemberID}/points{,/transactions}`、`/points/adjust` 改為以 `{memberID}` 為識別碼。此為 v1 的破壞性變更，與 design D13「v1 僅接受 additive 變更」牴觸——本提案主張現在破例，理由是 v1 尚無任何外部消費者，破例成本為零，而 ONDO 接入後即不再具備此條件。
- `members` 因取得 `shop_id` 而自動納入既有租戶 interceptor 的強制範圍（`internal/tenant`）。`orders.member_id` / `carts.member_id` 對 `members` 的參照因此由跨租戶參照收斂為租戶內參照。
- `member_refresh_tokens` 的 `shop_id` 成為可由 `member_id` 推導的冗餘欄位，保留（索引效率）但語意上不再是獨立事實。

不在本次範圍內（明確記錄，非遺漏）：

- **不實作資料表分割（partitioning）**。本改動使 `members` 具備日後 `PARTITION BY ... (shop_id)` 的前提條件（Postgres 要求 partitioned table 的唯一約束必須包含 partition key），但實際分割另需將主鍵改為複合鍵並將 `orders` / `carts` 的外鍵改為複合外鍵，屬於獨立的效能工作，且 `members` 並非最先觸及規模瓶頸的表。此處僅記錄為「已保留的未來選項」。
- 不引入 email 發送能力、email 驗證或 magic link。
- 不變更會員 JWT 的結構或 audience 規則（早已是每店獨立）。

## Capabilities

### New Capabilities
- `member-identity`: 前台顧客身分的範圍界定——身分以商家為界、憑證不跨商家共用、一家商家的顧客資料集自我完備（可獨立搬遷／刪除／匯出）。此不變式為跨切面性質，現行 specs 中無任何一份擁有它。

### Modified Capabilities
- `authentication`: 「Member registration and login in shop context」的註冊語意改變——移除「既有平台身分於第二家店註冊需驗證密碼」情境，改為每店範圍內判定；同構回應要求的理由隨之改變（不再是為了隱藏跨商家帳號存在，而是為了隱藏本店帳號存在）。
- `member-tiers-and-points`: 點數流水帳與餘額快取的載體由 `shop_member` 改為 `members`（`point_transactions.member_id`、`members.points`）；商家後台點數 API 的路徑識別碼由 `shopMemberID` 改為 `memberID`。
- `multi-tenancy`: 「Tenant data isolation enforcement」的租戶擁有資料清單中 `shop_member` 由 `members` 取代；新增「租戶資料集自我完備」的隔離要求。

## Impact

**Schema / migration**
- `api/internal/ent/schema/account.go`：`Member` 加 `shop_id` 與 shop edge、改唯一約束、併入 `points` / `level_id`；刪除 `ShopMember`。
- `api/internal/ent/schema/points.go`：`PointTransaction.shop_member_id` → `member_id`，edge 改指 `Member`。
- `api/migrations/`：新增 versioned migration。`email` / `phone` 為 nullable unique，per-shop 後需以手寫 SQL 處理 `UNIQUE NULLS NOT DISTINCT` 語意（ent 無法表達，比照既有慣例）。`shop_member` 的 `points` / `level_id` 需在刪表前搬移至 `members`。

**Go packages**
- `api/internal/auth`：註冊／登入查詢加入 shop 條件；`registerFailed` 的既有身分密碼驗證分支移除。
- `api/internal/httpapi/memberauth.go`：`Register` / `Login` / `ensureMembership` 改寫。
- `api/internal/points`：全面由 `shop_member_id` 改為 `member_id`；`members.points` 成為維護中的餘額快取。
- `api/internal/httpapi/points.go`：後台路徑參數改名。
- `api/internal/tenant`：確認 `members` 納入 interceptor 後，**後台（非租戶上下文）查詢仍正常運作**——此為本改動主要風險點。
- `api/internal/order`、`api/internal/cart`：`member_id` 語意不變，但查詢改受租戶隔離管轄，需驗證既有行為不變。
- `api/internal/seed`：demo 會員資料調整。

**API 契約**
- 後台點數三個端點的路徑參數更名（破壞性，見上）。
- 會員註冊／登入的請求與回應形狀不變；行為改變（不再有跨商家身分沿用）。
- 會員自助端點（`/api/v1/shop/*`）不受影響。

**下游相依**
- 本變更為 `storefront-client-auth`（publishable key + per-shop CORS）與 `guest-checkout`（訪客結帳自動建立無密碼身分）的前置條件。後者在本變更完成後大幅簡化：不再需要處理「email 已屬於其他商家身分」的跨租戶衝突。
