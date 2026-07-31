## Context

現行下單路徑只有一條，且整條都在會員身分之後：

```
/api/v1/shop/*  ─ TenantMW ─┬─ (公開) products
                            ├─ (公開) auth/register|login|refresh|logout
                            └─ MemberMW ─┬─ cart/*
                                         ├─ checkout ──▶ order.Service.Checkout(ctx, shopID, memberID, addr)
                                         ├─ orders/*
                                         ├─ orders/{id}/payments
                                         ├─ orders/{id}/shipment
                                         └─ points/*
```

`order.Service.Checkout` 是本專案並發安全性最集中的一段程式：單一 `ent.Tx` 內先以 CAS 把 cart 由 Active 翻成 Converted（擋雙送），再逐行以 `UPDATE ... WHERE stock_qty >= ?` 條件式扣庫存並檢查受影響列數（擋超賣），任一行失敗整筆回滾。它有一個以 `-race` 執行的強制正確性測試（`TestCheckoutConcurrentOversellingProtection`：stock=5、10 個並發請求、恰好 5 成 5 敗、最終 `stock_qty = 0`）。**本變更最重要的實作約束是不得產生第二條與它競爭的扣庫存路徑。**

ONDO 溫度花室（`bouquet`）是零建置 vanilla JS 靜態站，全站沒有登入 UI，結帳為三步驟訪客流程；其 `state.form` 只有 `{ name, phone, addr, date, card }`——**連 email 欄位都沒有**（現有的 `state.email` 是電子報訂閱，與結帳無關）。它今天無法完成任何一筆訂單。

姊妹 change 已鋪好的前提：

- `member-identity-per-shop`：`members` 每店獨立（`UNIQUE(shop_id, email)`，NULLS DISTINCT），「既有身分加入本店需驗證密碼」分支已刪除，`password_hash` 為 `Optional().Nillable()` 且 schema 註解已寫明保留給「passwordless guest identities」。同一 email 在 ONDO 只牽涉 ONDO。
- `storefront-client-auth`：`/api/v1/shop/*` 的租戶宣告為「`X-Publishable-Key` 優先，否則 `Host`」，per-shop CORS 已可登記。其 design D2 明文記載：金鑰是公開的，「偽造宣告替別家商家建立訂單」不會因引入金鑰而變成不可能，金鑰的貢獻是讓防濫用手段有一個可指名的作用對象。

約束：PostgreSQL 16；`orders.member_id` 為 `NOT NULL` 且本變更不改動它（訪客也是會員，points 照發、`GET /shop/orders` 不必另做一套）；**系統無任何 email 發送能力**（無 SMTP／mailer，`go.mod` 零相關依賴）；公開 API 一律 `/api/v1`，design D13 規定 v1 僅接受 additive 變更，姊妹 change 已各破例一次。

## Goals / Non-Goals

**Goals:**

- 讓未認證訪客能完成一筆完整訂單，且該訂單與會員訂單在資料層**沒有任何差別**（同一張 `orders`、同一個 `member_id` 語意、同一套點數與金物流）。
- 扣庫存的並發安全保證原封不動：訪客結帳 MUST 走既有 `order.Service.Checkout`，不得有第二條實作。
- 訪客結帳所取得的存取能力嚴格小於會員 session：只能觸及**該一筆訂單**。
- 顧客日後以同一 email 註冊時，先前的訪客訂單與累積點數自動浮現，且此路徑不得成為「知道 email 就能取得他人身分」的通道之外的新弱點。
- 不因訪客結帳而使任何既有端點的路徑、請求／回應形狀、狀態碼或授權規則改變。

**Non-Goals:**

- 不引入 email 發送、email 驗證或 magic link（見 D6 的殘留風險記錄）。
- 不引入訪客購物車資料表、guest cart token 或跨裝置購物車同步。
- 不引入冪等鍵（見 D11）。
- 不讓訂單存取 token 具備取消訂單、查詢訂單歷史、變更密碼或兌換點數的能力。
- 不改變 `orders.member_id` 的可空性。
- 不新增會員自助的批次購物車 HTTP 端點。

## Decisions

### D1. 訪客結帳是「把品項物化進既有購物車，再跑既有結帳」，不是第二條結帳路徑

`POST /api/v1/shop/guest/checkout` 的處理順序固定為四步，全部由新的 `internal/guest` 協調，**每一步都是呼叫既有擁有者的服務方法**：

1. 正規化 email → 在該商家找出或建立 `members`（無密碼，D2）
2. `cart.Service.ReplaceItems(ctx, shopID, memberID, lines)` — 單一交易整批取代該身分 active cart 的內容（D7）
3. `order.Service.Checkout(ctx, shopID, memberID, addr)` — **完全未改動的既有方法**
4. `auth.TokenIssuer.IssueOrderAccess(orderID, shopID, ttl)`（D3）

**替代方案：給 `order.Service` 加一個 `CheckoutItems(ctx, shopID, memberID, items, addr)`，跳過購物車直接建單。** 省掉兩次寫入，看似更直接。否決：它會複製 `Checkout` 步驟 3～6 的全部邏輯（可購買性驗證、條件式扣庫存、快照建立），於是專案裡出現兩段各自維護的扣庫存程式碼。下一個修改庫存語意的人只會改到其中一段，而超賣是一種無法事後補救的錯誤（訂單重複可以取消，賣出不存在的庫存不能）。

**替代方案：把 `Checkout` 拆成 `checkoutCart(tx, cart)` 供兩條路徑共用。** 比上者好，但仍需動到目前唯一被並發測試守住的那段程式。本變更的收益不值得重寫它的風險；**呼叫**它是最保守也最誠實的複用。

**代價**：訪客結帳多兩次資料庫往返（清空 + 寫入購物車）。可接受——結帳本就是低頻寫入路徑，而換到的是「扣庫存只有一份實作」這個不變式。

**驗證方式**：既有的 `TestCheckoutConcurrentOversellingProtection` 一行不改地保留（會員路徑），另新增一份結構相同的訪客版並發測試（10 個不同 email 的訪客同時搶 stock=5），兩者以 `-race` 通過即為「保證未被削弱」的證據。

### D2. 身分以 email 為鍵；phone **不**寫入 `members`

訪客結帳必填 email。理由不是偏好而是可認領性：`POST /api/v1/shop/auth/register` 只接受 email，以 phone 建立的身分**永遠無法被認領**——那會製造一種顧客拿不回來的資料，正是方案要避免的東西。

ONDO 的收件資訊裡有 phone，且 `members.phone` 有 `UNIQUE(shop_id, phone)`。本設計**刻意不把 phone 寫入 `members`**：寫了會開出第二條唯一性軸線，回頭客若換 email 但同 phone（或同住址不同人共用市話）就會撞上 unique violation，而訪客結帳沒有任何合理方式向使用者解釋「你的電話已被使用」。phone 存在 `orders.shipping_address` 裡——那才是它的語意所在（這一筆的收件電話），而不是身分屬性。

**代價**：商家後台看到的訪客顧客只有 email，phone 要從訂單看。可接受；若日後要做「顧客資料自動補完」，那是一個需要處理衝突策略的獨立決策。

### D3. 訂單存取 token：沿用 JWT 體系，`aud=order:{shop_id}`，以 member 金鑰簽發，預設 24 小時

```
IssueOrderAccess(orderID, shopID, ttl) →  HS256(memberSecret)
    sub = orderID
    aud = "order:{shopID}"
    iss = 既有 issuer
    exp = now + ttl        （ORDER_ACCESS_TOKEN_TTL，預設 24h）
    claims.shop_id = shopID
```

**載體：JWT 而非不透明 token。** 不透明 token 需要一張新表、雜湊儲存、撤銷語意與清理排程（比照 `member_refresh_tokens`）。訂單存取 token 是**短效、單一資源、無法刷新**的憑證，這些機制買不到相應的東西：它的失效手段就是過期。既有的 preview token（`IssuePreview` / `VerifyPreview`，`aud=preview`、以 admin 金鑰簽發、TTL 由呼叫端給）正是同型別的先例——短效、綁定單一資源、無狀態，本變更照抄它的形狀。

**簽章金鑰：member secret。** 它是前台顧客面的 token，與 admin 體系必須完全無關（design D9 的兩套隔離金鑰不容第三套混入其中任一邊的語意）。以 member secret 簽發並不會使它變成 member token——`VerifyMember` 要求 `aud = shop:{id}`，本 token 的 `aud = order:{id}`，jwt 函式庫在 audience 檢查即拒絕。反向亦然。

**audience 為何是 `order:{shop_id}` 而不是純 `order`。** 把 shop 綁進 audience，跨商家重用由函式庫在驗證階段直接擋下，與 member token 的 `shop:{id}` 同一種防護，不必倚賴 handler 記得比對 claim。`claims.shop_id` 仍然保留，作為 defense in depth 與日誌可讀性。

**TTL 為何是 24 小時。** 下界由付款往返決定（發起付款 → 導向金流 → 回站），30 分鐘就夠；但顧客關掉分頁、當天稍晚回來看訂單是完全正常的行為，那才是這個 token 的日常用途。上界由「這是一個無法撤銷的 bearer 值」決定——它不該變成一個永久的訂單存取連結。24 小時是「涵蓋當天」與「隔天就失效」的交界，且可由 `ORDER_ACCESS_TOKEN_TTL` 調整。**過期之後怎麼辦**是 D5 的答案：以同一 email 註冊，該訂單本來就掛在那個 `member_id` 上，直接出現在訂單歷史裡。

**不簽發 refresh token。** 訪客結帳不是登入。能刷新的東西就是 session，而本變更的全部安全論證建立在「訪客結帳不產生 session」之上。

**不簽發完整會員 JWT**（使用者已拍板，此處記錄理由以免日後被「順手加上去比較方便」翻案）：若簽發，`POST /guest/checkout` 就成了一個「輸入任意 email → 取得該 email 在本店的完整身分」的端點，可讀取他人的訂單歷史、點數與個資。這不是可以用速率限制緩解的問題，而是端點語意本身的錯誤。

### D4. token 的作用範圍：查該訂單、對該訂單付款、查該訂單出貨；**路徑不帶訂單 id**

```
POST /api/v1/shop/guest/checkout          無需 token（TenantMW only）
GET  /api/v1/shop/guest/order             ─┐
POST /api/v1/shop/guest/order/payments     ├ OrderAccessMW：token 指名訂單
GET  /api/v1/shop/guest/order/shipment    ─┘
```

三個讀寫端點**不接受路徑上的訂單 id**，目標訂單完全由 token 的 `sub` 決定。這是 `CartHandler` 既有慣例的直接沿用（「Routes never take a cart id or member id path parameter ... avoids IDOR by construction」）：沒有外部可控的識別碼，就沒有可以竄改的識別碼。若採用 `/guest/orders/{orderID}` 並在 handler 比對 token，語意相同但多出一個必須每次記得寫的比對，且錯漏一次就是水平越權。

**發起付款是必要的**：ONDO 的第三步就是付款，沒有它訪客訂單無法被支付。它是寫入，但寫入的是 token 指名的那一筆訂單自身的付款流程，且 `payment.Service.InitiatePayment` 既有的狀態守衛（已取消／非未付款 → 409）原樣適用。實作上以 `order.MemberID` 作為 `memberID` 參數傳入——訪客**就是**那位會員，這不是繞過檢查而是把檢查餵給正確的值。

**取消訂單刻意不納入。** 取消會歸還庫存、改變訂單終態，且其 409 語意與付款／出貨狀態耦合；它是帳號層動作。持有 token 者要取消，路徑有二：以同一 email 註冊（認領身分，隨即取得完整會員能力，含 `POST /shop/orders/{id}/cancel`），或聯繫商家（後台 `order.cancel` 一向可用）。**明確記錄這是取捨而非疏漏**：允許取消的邊際風險其實很低（持有 token 者本就看得到該訂單全部內容），但「這個 token 只能完成與觀察一筆訂單」是一條清楚、可背誦、不會在下一個變更裡被逐步侵蝕的界線，而「它還可以取消，但不能……」不是。

**`GET /api/v1/shop/orders`（訂單歷史）永不接受此 token**：它是會員 session 的能力。`OrderAccessMW` 與 `MemberMW` 掛在互不相交的路由群組上，兩者不共用任何 context 鍵——`OrderAccessMW` 寫入的是 `auth.WithOrderAccess`，`memberFrom` 讀不到它，因此即使日後有人誤把 guest 路由掛到會員群組下，會員 handler 也拿不到身分而回 401。

### D5. 註冊認領無密碼身分

`Register` 的分支由「本店 email 已存在 → 一律拒絕」改為：

| 本店 (shop_id, email) 狀態 | 行為 |
| --- | --- |
| 不存在 | 建立身分並設密碼（既有行為） |
| 存在，`password_hash IS NULL` | **認領**：以 `WHERE password_hash IS NULL` 為條件設定密碼；受影響列數為 0（並發認領）則走統一拒絕 |
| 存在，`password_hash` 非 NULL | 統一拒絕（既有行為，含 `EqualizeVerifyTiming`） |

認領成功的回應與全新註冊**位元組層級同構**（同樣的 200 + token 三元組）。認領後 `member_id` 不變，因此該身分名下的訂單與點數自動出現在 `GET /shop/orders` 與 `GET /shop/points`——**不需要任何搬移邏輯**，這正是「訂單一開始就綁在 `member_id` 上」買到的東西。

**這不只是好處，也是必需品。** 若不做認領，本變更會製造一個任何人都能發動的封鎖攻擊：以受害者的 email 做一次訪客結帳（甚至不必付款），該 email 從此在該商家永遠無法註冊，且系統依同構要求不得說明原因。認領把這條攻擊變回「攻擊者替受害者建立了一個他隨時可以接管的空身分」。

**回應可區分性的邊界隨之改變，spec 必須跟著改。** 姊妹 change 的 design D8 記載註冊回應「不得洩漏該 email 是否為本店顧客」。認領之後，可由回應區分的是「該 email 在本店**是否已設定密碼**」，而非「是否為本店顧客」——無密碼身分與從未存在的 email 兩者的回應完全相同。這是一個**更窄**的洩漏面（先前連「曾以訪客身分下過單」都會洩漏，現在不會），但它與原句字面不符，故於 authentication spec 明文更正。

### D6. 「email 已是有密碼會員」與「認領無密碼身分」的安全邊界（本變更最需要想清楚的一節）

分成兩個必須分開回答的問題。

**問題一：訪客結帳輸入一個已是本店有密碼會員的 email，會怎樣？**

**決定：照常建單，訂單掛在該會員名下，回應與其他情形完全同構。**

理由：任何其他做法都要求端點對「這個 email 有帳號」做出可觀察的反應，那等於把註冊表單既有的存在性洩漏面複製到一個未認證的公開端點上，而且是更糟的版本——註冊至少要求提交密碼，訪客結帳只要求 email。

攻擊者能得到什麼？他可以替受害者的帳號製造訂單紀錄（收件資訊是**攻擊者自己的**，不含受害者任何個資），若他實際付款則點數記到受害者頭上（付錢送人點數不是攻擊）。他**得不到**受害者的訂單歷史、email 以外的個資、點數餘額或任何 token——回傳的 token 只指名他剛建立的那一筆訂單（D3/D4）。因此殘留危害是「訂單歷史被污染」，屬於騷擾等級，且與「未付款訂單佔用庫存」共用同一個緩解手段（D9 的速率限制、商家取消）。

一個例外：**該身分 `status = 0`（商家已停用此顧客）時拒絕結帳**。商家停用一位顧客的意圖不能被一條不需要密碼的路徑繞過。此拒絕使用專屬的 422 code（`checkout_unavailable`）與中性訊息，等於承認它對「已知該 email」的人洩漏了「這個 email 在本店被停用」——這個洩漏是停用本身的必然語意（被停用者必須以某種方式知道自己被拒），且遠窄於一般存在性洩漏。**明確記錄為已評估並接受的取捨**：另一個選項是回傳與正常結帳無法區分的成功回應然後靜默丟棄訂單，那是拿誠實性換一個更差的除錯體驗。

**問題二：無密碼身分可以被任何知道該 email 的人認領（D5），這可接受嗎？**

這是本變更**唯一**的實質新風險，必須寫清楚而不是含糊帶過。

具體情境：Alice 以 `alice@example.com` 在某商家做了訪客結帳；在 Alice 自己註冊之前，Mallory 以同一 email 註冊，於是取得該身分——連同 Alice 那些訂單的收件人姓名、電話與地址。

為什麼仍然選它：

1. **不做認領的代價更高。** 見 D5：那是一條人人可發動、受害者無法自救、系統依規不得解釋的封鎖攻擊。用一個需要「猜中 email + 知道他在這家店下過單 + 搶在他之前註冊」的資訊洩漏，換掉一個零門檻的阻斷，方向是對的。
2. **半徑已由姊妹 change ⓪ 收斂到單一商家。** Mallory 取得的是該商家的身分，不涉任何其他商家，也拿不到密碼（本來就沒有）。
3. **本系統從未驗證過 email。** 認領前的今天，Mallory 一樣可以用 `alice@example.com` 在該商家註冊並佔用該 email。本變更改變的不是「能不能冒名註冊」，而是「冒名註冊後能看到什麼」——從「空帳號」變成「該 email 先前的訪客訂單」。
4. **正確的修法明確存在且不遠。** 認領要求驗證 email（或 magic link）即完全關閉此洞，且**不需要改動本變更的任何資料模型**：屆時只是在認領分支前插入一道驗證。本變更刻意不預先設計那道驗證的形狀（它牽涉寄信基礎設施、模板、退信處理、驗證 token 生命週期），但把它記為 `email-verification` change 的首要驅動理由。

**已評估並否決的中間方案：認領成功但不揭露認領前的訂單與點數**（例如以 `claimed_at` 過濾）。它確實堵住上述洩漏，但同時廢掉本方案的全部產品價值——Alice 五分鐘後註冊，看不到自己剛下的單，而系統無法向她解釋為什麼。它把一個罕見情境的風險，轉嫁成一個常見情境的荒謬體驗，並且留下一個永遠不會被清理的過濾條件。

**交付給商家的說明必須包含這句**：在 email 驗證上線之前，「以同一 email 註冊即取得先前訪客訂單」是**未經驗證**的關聯。

### D7. 購物車以 `ReplaceItems` 單一交易整批取代，不新增任何訪客購物車機制

`cart.Service.ReplaceItems(ctx, shopID, memberID, lines)`：在一個 `ent.Tx` 內找出或建立 active cart、刪除其全部品項、逐行驗證（SKU 屬於本店、`is_active`、商品已發佈、幣別一致、數量 > 0、庫存足夠——與 `AddItem` 同一組規則）並寫入；重複 `sku_id` 累加（沿用 `AddItem` 的語意）；任一行失敗整筆回滾。

**為何需要新方法而不是「`ClearCart` 然後逐筆 `AddItem`」**：後者是兩到 N 個獨立交易。若第三筆品項驗證失敗，購物車已被清空且塞了兩筆——對一個已被認領、正在別的裝置上購物的身分，這是一次無聲的資料破壞。整批取代讓「訪客結帳失敗」等於「什麼都沒發生」。

**為何是取代而不是合併**：訪客結帳在單一請求裡送出完整訂單內容。若與既有 active cart 合併，顧客會被收取他這次沒有選購的品項的費用。

**明確記錄的副作用**：對一個已被認領且擁有非空 active cart 的身分執行訪客結帳，會清掉那個購物車。這是「訪客結帳送出的就是全部」的必然結果，寫進 spec 而非留作驚喜。

**不新增 HTTP 批次端點。** `ReplaceItems` 目前唯一的呼叫者是訪客結帳。會員自助的批次購物車端點沒有消費者（ONDO 不用會員購物車），現在加等於憑空多一個要維護的公開契約。

### D8. 收件地址必填欄位縮小為 `recipient_name` / `phone` / `line1`

`city` / `postal_code` / `country` 改為選填，欄位本身與 JSON 序列化形狀**完全不變**（仍然總是輸出這些鍵，只是允許空字串），既有訂單資料與既有回應形狀因此不受任何影響。

理由：這三個欄位**目前無任何程式碼讀取**（全樹只有 `validate()` 檢查它們非空，shipping/payment 皆不觸碰），它們是預留的一般性而非驗證。而 ONDO 的實體表單是「收件人／手機／配送地址」三欄——台灣的單欄地址本就把縣市寫在字串裡，要求它額外拆出 `city` 並填 `country: "TW"`，換到的是零，付出的是每個接入的商家都要處理一次的摩擦。

**替代方案：只放寬訪客端點。** 否決。同一個 `orders.shipping_address` 欄位、同一個 `ShippingAddress` 型別，卻依呼叫路徑套用兩套必填規則——這種規則會在第三個呼叫端出現時變成一個沒人說得清楚的謎。

**替代方案：留給前端組裝（`line1` 塞完整地址、`city`/`country` 填假值）。** 否決。它會在資料庫裡種下一批 `city: "N/A"`，讓這些欄位日後真的要被使用時毫無價值，而且是我們主動教商家這樣做的。

這是**放寬**：先前會通過的請求全部照常通過、回應相同；先前被 422 拒絕的一部分請求現在會成功。無既有消費端因此損壞，落在 D13 additive 範圍內。order-management spec 的對應需求與情境同步更正。

### D9. 訪客結帳不要求 publishable key；速率限制沿用既有維度並新增獨立門檻

**不要求金鑰。** 端點掛在 `/api/v1/shop/*` 之下，租戶解析沿用姊妹 change 的「金鑰優先，否則 `Host`」。要求金鑰看似能讓濫用可歸因，實際買不到：金鑰是公開值（姊妹 change design D1/D2），攻擊者可從商家自己的網站原始碼讀出；就算他不想讀，`Host` 通道對他一樣開放。強制要求只會讓這個端點成為群組裡唯一一個解析規則不同的特例，並讓第一方前端（平台自己託管的 storefront，若日後需要）無故失敗。姊妹 change design D7 已就同一問題做出「回退 `Host`，不拒絕」的決定，此處不製造例外。

**速率限制新增一組獨立門檻**，沿用既有 `rateLimitRule` 機制與既有兩條規則型別，不引入新維度：

| 規則 | 鍵 | 預設 |
| --- | --- | --- |
| 寬鬆 | `shop_id + IP` | 20 / 10 分鐘 |
| 嚴格 | `shop_id + IP + email` | 5 / 10 分鐘 |

寬鬆規則不比註冊更寬，是因為訪客結帳是本 API 上**唯一一個未認證即可寫入業務資料並佔用庫存**的端點；但也不能太緊——CGNAT 與企業出口下多位真實顧客共用一個 IP 是常態，過緊會誤傷正常成交。嚴格規則的作用是擋住「以同一 email 大量建單」與「大量探測 email」。限流器故障時 fail-open（既有慣例）。

**不新增 per-key 或 per-email-domain 維度**：前者等於讓商家自助擴額（姊妹 change design D9），後者會把整個 gmail.com 綁在一起。

### D10. 訪客身分的 `status` 與其「無法登入」的性質

新建的訪客身分 `status = 1`（啟用），`password_hash` 為 NULL。

**為何不是 `status = 0`**：`status = 0` 的語意是「商家停用了這位顧客」，既有 spec 要求它無法登入且 refresh 失效。拿它來表達「尚未設定密碼」會讓兩個正交的事實共用一個欄位，並使「認領」變成「解除停用」，語意上完全錯誤。

**使訪客身分無法登入的是 `password_hash IS NULL` 本身**：`Login` 既有的 `m.PasswordHash == nil` 檢查直接回 401（無需改動），`RotateMember` 也不會為它簽出任何東西（從未簽發過 refresh token）。因此「有身分」與「能登入」在資料層是分離的，不需要新的旗標。

### D11. 不做冪等鍵；重複送出的後果明寫於 spec

訪客結帳沒有冪等鍵：同一份請求送兩次會建立兩筆訂單，**各自正確扣庫存**（不超賣），兩張單都可由顧客（各自的 token）或商家取消。

會員結帳沒有這個問題，因為它的輸入是伺服器端的購物車，第二次呼叫時購物車已是 Converted → 422。訪客結帳每次都自帶完整品項，因此天然可重入。

**為何不現在做**：冪等鍵是一套需要獨立設計的機制——鍵的作用域與儲存（表或 Redis？）、TTL、回放時要回傳原回應還是 409、以及「相同鍵不同 body」的處置。塞進本變更會讓它變成兩個變更，而它要解決的問題（重複下單）是**可逆的**（取消即可），與扣庫存的不可逆錯誤不在同一個嚴重性等級。前端以停用送出按鈕處理雙擊是標準作法，已寫入接入說明。

**明確記錄為將來的候選變更**，而非「已經想過所以不用做」。

### D12. 本變更為純 additive，不動用 D13 的破例窗口

破壞性面向：**零**。新增四條路徑；`Register` 與 `ShippingAddress.validate` 的改動都是**接受更多輸入**（先前成功的一律照常成功、回應相同），既有整合測試不需要為了配合本變更而修改任何斷言。

姊妹 change `member-identity-per-shop` 與 `storefront-client-auth` 各破例一次，理由是 v1 尚無外部消費者。**本變更刻意不使用第三次**：ONDO 即將接入，這個窗口本來就該在它接入時關閉，而不是每次覺得方便就再開一次。實作前仍依既有慣例確認 `bouquet/public/app.js` 為零 API 呼叫（作為「窗口尚未關閉」的事實記錄），但本變更不依賴該事實。

## Risks / Trade-offs

- **[無密碼身分可被任何知道該 email 的人認領，取得先前訪客訂單的收件個資]** → 這是本變更唯一的實質新風險，D6 問題二完整記錄其情境、選擇理由與否決的替代方案。緩解：半徑限於單一商家（姊妹 change ⓪）、註冊端點既有速率限制、訪客結帳新增的 per-email 嚴格限流；正確修法為 `email-verification` change，屆時只需在認領分支前插入驗證，資料模型不變。交付商家的說明 MUST 載明此關聯未經驗證。

- **[以他人 email 建立未付款訂單佔用庫存（訂單歷史污染 + 庫存騷擾）]** → D9 的兩條限流規則；訂單未付款不發點數；商家後台 `order.cancel` 可回收庫存。此暴露面與「任何人造訪商家公開網站」同級（姊妹 change design D2 已論證金鑰無法阻止），不因本變更而質變，但頻次上限由本變更負責設定。

- **[重複送出建立兩筆訂單]** → D11：可逆、可取消、不超賣；前端停用送出按鈕（接入說明）；冪等鍵記為將來的候選變更。

- **[訂單存取 token 洩漏後無法撤銷]** → TTL 24 小時且不可刷新（D3）；作用範圍僅該筆訂單的讀取與付款發起，不含取消與任何帳號層動作（D4）；不進入 URL（僅 `Authorization: Bearer`），因此不落入 access log 與 Referer。

- **[新增第三個 token 家族後，三者的隔離出錯而互相污染]** → audience 全部互斥且由 jwt 函式庫在驗證階段強制（`admin` / `preview` / `shop:{id}` / `order:{id}`）；admin 與 member 兩把簽章金鑰的既有隔離不變；spec 以三向隔離矩陣固定，並以整合測試逐格覆蓋（會員 token 打 guest 端點、訂單 token 打會員端點、跨商家、跨訂單、過期）。

- **[訪客結帳清掉一個已被認領身分的既有購物車]** → D7 的必然結果，寫入 spec 並於接入說明提醒；`ReplaceItems` 的原子性確保「失敗＝什麼都沒發生」，不會出現半套購物車。

- **[放寬地址必填欄位後，日後真的需要 `city`/`postal_code` 的能力（如運費分區）會拿到空值]** → 屆時該能力自行定義它需要的欄位與其必填時機（例如依 `shipping_method` 決定），這比現在讓所有商家為一個尚不存在的能力填假資料好。D8 已否決「前端填假值」正是為了保住這條路。

- **[trade-off：訪客結帳多兩次 DB 往返（清空 + 寫入購物車）]** → 接受。換到「扣庫存只有一份實作」（D1），且結帳是低頻寫入路徑。

## Migration Plan

**無 migration。** 本變更不新增資料表、不改欄位、不改索引：`members.password_hash` 早已是 nullable、`UNIQUE(shop_id, email)` 早已是 NULLS DISTINCT 因而允許無密碼身分共存、`orders.member_id` 維持 `NOT NULL` 不動。`api/migrations/` 與 `atlas.sum` 皆不變——實作後 `make migrate` 應為 no-op，此即驗收條件之一。

**設定**：新增 `ORDER_ACCESS_TOKEN_TTL`（預設 24h）與四個限流門檻變數，全部有預設值，`.env` 不設也能運作（比照既有限流設定）。

**部署**：無正式環境，程式部署即生效。既有會員路徑行為不變，因此與 ONDO 的接入沒有時序耦合：平台先上線，ONDO 再改前端。

**Rollback**：移除四條新路由即回到本變更前的行為。唯一不可逆的是**已建立的無密碼身分**——它們留在 `members` 裡，但那是正確的（訂單掛在上面）；回滾後它們無法被認領（`Register` 恢復為一律拒絕），需連同認領分支一起保留，或以手動 SQL 清理無訂單的無密碼身分。此限制記錄於此。

## Open Questions

- **訂單存取 token 的 TTL 是否需要與付款供應商的 session 長度對齊？** 目前唯一的 provider 是 mock（同步回傳 `redirect_url`，無真實逾時），24 小時遠長於任何真實金流的 session。等真實 provider 接入時再依其行為調整；`ORDER_ACCESS_TOKEN_TTL` 已是設定值，屆時不需改程式。
- **商家後台是否需要一個「訪客顧客」的檢視維度**（例如篩選 `password_hash IS NULL` 的顧客、或看到某位顧客是否曾以訪客身分下單）？本變更不加：後台目前沒有顧客列表 API，加了也無處顯示。等後台顧客管理成為一個 change 時，這個維度是它的一部分，而不是本變更的。
