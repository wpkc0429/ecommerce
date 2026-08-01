## Context

商家今天要看一位顧客，只有兩條路：從某一張訂單詳情按「檢視此顧客點數」，或知道 member id 直接打 `#/points/57`。**沒有任何端點回答「這家店有哪些顧客」。**

這是 `admin-commerce-operations` design D7 刻意留下的缺口，理由是它需要「分頁、搜尋、一份 PII 揭露決策與一個今天不存在的權限節點」。本 change 的體積有一半在那份 PII 決策上——端點本身是兩個 handler 加一個 service。

四個既有事實限制了選項空間：

1. **`members` 是租戶資料，但後台不在租戶上下文。** `member-identity-per-shop` design D4 把這件事列為該變更的**主要風險點**：`internal/tenant` 的 interceptor 會在租戶上下文自動加 `shop_id`，而後台路徑沒有那個上下文，所以每一條查詢必須自己帶。既有的 `points.Service.GetMemberPointsAdmin` 就是照這條規則寫的。
2. **D13 的 v1 破例窗口已於 `order-extras` 用盡並關閉。** 新端點是 additive，沒問題；既有端點的既有行為一個位元都不能動。
3. **admin 仍然沒有 CI job、沒有型別檢查、沒有單元測試**，唯一守著它的是 `e2e/happy-path.mjs` 的瀏覽器斷言。這條事實從 `admin-commerce-catalog` 到今天沒有改變。
4. **admin 今天是 2,956 行**（實測：JS 2,376 ＋ HTML 396 ＋ CSS 184），而 `admin-commerce-catalog` design D1 的 tripwire #4 是「總行數超過約 3000 行」，`admin-commerce-operations` 明寫「**下一個視圖就會越過**——下一個 admin change 開工前先重評 D1，不要先寫程式」。**本 change 就是那個下一個 admin change**，所以 D1 是本文件的第一節。

## Goals / Non-Goals

**Goals:**

- 依 tripwire #4 的要求，在寫任何 code 之前重新評估 `admin/` 的前端架構，用當下的真實證據做決定，並把量到的東西留下來。
- 讓商家能瀏覽、搜尋自己的顧客，並看到一位顧客的基本資料、點數與等級、訂單紀錄。
- **正面決定哪些顧客欄位揭露給後台、以什麼粒度、給誰**，並把每一條的成本誠實寫下來——包含不做匯出這個決定為什麼不是一道安全控制。
- 訪客結帳建立的無密碼身分在商家畫面上被如實呈現，而不是被當成註冊會員。
- 新增**恰好一個**權限節點，且它有真實的消費者。
- 租戶隔離在一條新的、不在租戶上下文的讀取路徑上仍然成立，並以測試從兩個方向證明。
- `password_hash` 不出現在任何回應中，且證明落在**回應位元組**上而不是程式碼閱讀上。

**Non-Goals:**

- 不新增 `member.edit`、不做顧客資料編輯、不做停用／啟用顧客（平台今天沒有任何寫入 `members` 的後台端點）。
- 不做匯出按鈕（D5）。
- 不做顧客彙總指標：訂單數、終身消費額、平均客單價（D5）。
- 不在名冊回應中放 `phone`（D3）。
- 不做 `registered` 的篩選器（D6 末段）。
- 不在顧客詳情裡內嵌點數調整表單或點數流水帳（D9）。
- 不做顧客合併／去重（同一個人以兩個 email 下單是兩列，這是 `member-identity-per-shop` 的既有語意）。
- 不做後台讀取的審計軌跡（它是 `member.export` 的前置，見 Open Questions）。
- 不新增資料表、欄位、索引、migration、事件接線。

## Decisions

### D1. Tripwire #4 的重新評估：維持無建置步驟，但**把 #4 這條 tripwire 換掉**——因為量完之後發現它量錯了東西（本 change 最重要的決策）

`admin-commerce-catalog` design D1 的推翻條件 #4 是：

> **admin 的總行數超過約 3000 行**，或有非本專案成員需要在不學 `el()` 的前提下貢獻畫面。

它要求的動作是「重開 (b)〔引入建置步驟與框架〕的評估，不必再爭論」。本節照做，而且是**先量再想**。

---

**先把數字攤開。** 逐 commit 實測（`git ls-tree` 對 `admin/**/*.{js,html,css}` 逐檔計行）：

| 時點 | 總行數 | `lib/` | `app.js` | `views/` | 視圖數 |
| --- | ---: | ---: | ---: | ---: | ---: |
| `api-contract-parameter-lock`（拆檔前，單檔） | 838 | — | 616 | — | — |
| `admin-commerce-catalog` | 1,643 | 518 | 154 | 646 | 4 |
| `admin-commerce-operations` | 2,940 | 641 | 228 | 1,492 | 9 |
| `tier-discount-application`（今天） | 2,956 | 641 | 228 | 1,507 | 9 |
| 本 change（**實作後實測**） | **3,229** | **641（+0）** | **232（+4）** | 1,720（+213） | 10 |

（實作後回填。預估是 ≈3,190 / `lib/` +0；`lib/` 的 +0 逐字成立——本 change 沒有動 `admin/lib/` 的任何一行。
其餘增量：`index.html` 396 → 449、`styles.css` 184 → 187。）

今天這 2,956 行的組成：

| 類別 | 行數 | 佔比 |
| --- | ---: | ---: |
| `views/` 各視圖的領域程式碼 | 1,507 | 51% |
| `index.html` ＋ `styles.css` | 580 | 20% |
| `lib/schemaform.js`（CMS 的 JSON Schema → 表單編譯器） | 274 | 9% |
| `app.js` shell（登入、商家切換、註冊表、路由分派） | 228 | 8% |
| `lib/` 其餘共用詞彙（`api` 80 ＋ `dom` 121 ＋ `money` 75 ＋ `rbac` 42 ＋ `router` 49） | 367 | 12% |

---

**這張表回答了一個舊 tripwire 沒問的問題：那 3,000 行是什麼？**

框架買到的是**膠水的消失**——宣告式渲染取代手寫 DOM 組裝、反應性取代手動重畫。它不會刪掉領域程式碼，只會換一種寫法表達。所以「框架能刪掉多少」的上界不是 3,000，是**共用詞彙 ＋ shell ＝ 595 行**（367 ＋ 228）。`schemaform.js` 那 274 行也不會消失：它是一個 JSON Schema → 控件的編譯器，與框架無關，換到 React 只是把 `el()` 換成 `createElement`。`views/` 的 1,507 行會被重寫但不會變短——那是「訂單有哪些狀態」「折抵列只在折了錢時出現」「出貨的三態」這些**規則**，不是膠水。

換句話說：**用 595 行的可能節省，去換一份 lockfile、一個 `node_modules`、一份多階段 Dockerfile、一個必須新增的 CI job、以及「`API_BASE` 是執行期設定」從唯一可能降級成一個要有人記得別破壞的約定。** 這正是 `admin-commerce-catalog` design D1 對 (b) 做過的算術，而**這次是拿真實數字做的，不是預估**。結論沒有改變，而且比當初有根據。

---

**更重要的是：共用層有沒有在漏水？** 這是「抽象撐不撐得住」的唯一可觀察訊號，而它有數據：

| 區間 | 新增視圖 | `views/` 增量 | `lib/` ＋ shell 增量 | 共用層佔比 |
| --- | ---: | ---: | ---: | ---: |
| catalog → operations | +5 | +846（169/視圖） | +197 | **23%** |
| operations → tier-discount | 0 | +15 | +0 | 0% |
| 本 change（**實測**） | +1 | +213 | **+4**（`lib/` 0 ＋ 註冊表與 import 4 行） | **1.9%** |

`admin-commerce-operations` 加 5 個視圖只讓共用層長了 197 行，而那 197 行有明確的來歷：`router.js` 49 行（一個新能力）、`dom.js` 的 `rows()`／`renderPager()`（tripwire #2 觸發後的正確處置）、shell 的路由分派 74 行。**本 change 加一個視圖，共用層增量是一行**——因為 `rows()`／`renderPager()`／`el()`／`can()`／`formatPoints()` 已經夠用了。

這就是抽象撐得住的樣子：**每個新畫面的成本幾乎全部落在該畫面自己的領域規則上，而不是被推回共用層。** 反過來，如果每加一個視圖都要往 `lib/` 塞東西，那才是該換框架的訊號——因為那代表共用詞彙不夠用，而不夠用的下一步就是自己長出一個半吊子的框架。

---

**現在說 #4 為什麼必須被取代，而不是「這次放行、下次再說」。**

一條「總行數 > 3000」的 tripwire 有一個結構性缺陷：**它在跨過的那一刻就永久失效。** 跨過之後每一個 change 都滿足它，於是它只有兩個結局——每次都強迫一次不再帶來新資訊的評估，或被所有人忽略。它是一道**一次性的門**，不是一個持續的偵測器。它已經完成了它唯一能完成的工作：**逼我們在 2,956 行的時候停下來量一次**，而那次測量產出了上面兩張表。這是 tripwire 成功了，不是它失敗了。

**已評估並否決：把門檻改成 5,000 行。** 那只是把同一個一次性的門往後移，並且明確承認它量的是錯的東西。誠實的做法是換掉它。

**已評估並否決：刪掉 #4 不補。** 那會讓「累積複雜度」這件事失去所有機械化的把關，只剩下人的感覺。

---

**四條推翻條件在本 change 之後的狀態，以及新的第四、第五條：**

| 條件 | 狀態 |
| --- | --- |
| #1 需要跨視圖的即時衍生狀態 | **未觸發，而且是被一個設計決定主動避開的**——見 D9：顧客詳情刻意不內嵌點數調整，否則「調整後餘額／等級／流水帳」就是三處手工同步。到期時機不變：`shipments` 長出 line items 的那一天。 |
| #2 同一個小元件第三次重寫 | **未觸發，而且同樣是被 D9 主動避開的**（點數三件套沒有被複製到 `members.js`）。`rows()`／`renderPager()` 直接服務本 change 的名冊與訂單區塊，零新增。 |
| #3 需要型別安全甚於 E2E 覆蓋 | 未觸發。本 change 最危險的兩件事是「`password_hash` 洩漏」與「點數被當成金額」——**兩者 `tsc` 都看不見**，守著它們的分別是回應位元組斷言（D7）與 `formatPoints`（D9）。誠實記錄：admin 仍無 CI job，這是真實缺口，處置見 Risks。 |
| ~~#4 總行數 > ~3000~~ | **已跨過（實測 3,229），並依 tripwire 的要求在寫任何 admin code 之前完成重評；結論維持 (c)，且本條被下列兩條取代。** |
| **#4′ 共用層開始追上視圖層** | 任一次 change 中 `lib/` ＋ `app.js` 的淨增行數 > 該次 `views/` 淨增行數的 **1/3**。歷史值：23%（operations）、0%（tier-discount）、**1.9%（本 change 實測：+4 / +213）**。**這一條可以在 1,500 行時響、也可以在 6,000 行時沉默**，而它響的時候指的正是 (b) 真正修得好的那個問題。 |
| **#5 單一視圖檔超過約 400 行** | 今天最大是 `orders.js` **375**（其次 `products.js` 338、`members.js` 203）。per-file 的可讀性與總行數無關，而它才是「改一個畫面要先讀多久」的實際代價。超過就先問能不能拆成兩個視圖（列表／編輯器已有先例），拆不動才是抽象的問題。 |

**兩條合起來比舊的 #4 更難在精神上被滿足**：舊條件只要跨過一次就再也不會提供資訊；新條件每一個 change 都會給出一個新的數字，而且兩個數字量的是不同的失效模式（共用層漏水 vs. 單檔失控）。

---

**最後，誠實地說出維持 (c) 的代價，不美化：**

- **3,000 行 JavaScript 沒有任何靜態檢查**，這是真的。但它的正解是一個 CI job，不是一個框架——而且那個 CI job 在無建置步驟下依然做得到（`tsc --checkJS` 讀 JSDoc，產物仍是原生 ES module）。把「我們需要型別」翻譯成「我們需要 Vite ＋ React」是一次沒有必要的綁定。
- **換框架的成本正在上升**，因為 E2E 斷言綁在具體的 DOM id（`#order-subtotal`、`#points-balance`、`#members-tbody`…）上，重寫會在同一步驟裡作廢 admin 唯一的測試覆蓋。這是**支持現在就換**的最強論點，必須正面回答：它只有在「我們遲早會換」成立時才有力。而 (b) 真正解決的問題——反應性衍生狀態、元件生態系、型別——第一個有名字有日期的觸發點（#1，`shipments` 的 line items），另外兩個沒有跡象。為一個不確定會發生的遷移，現在付一筆確定的成本，正是 `admin-commerce-catalog` D1 拒絕 (b) 的同一個理由。**但這件事要記下來：如果哪天真的要換，那必須是一個「內容就是換框架」的獨立 change，不是夾帶在某個功能裡。**

**結論：維持無建置步驟、原生 ES module、最小共用詞彙。本 change 不動 `admin/lib/` 一行。**

### D2. 端點：兩條新路由 ＋ 一個 additive 參數；先證明既有端點真的不夠

**先確認缺什麼。** 逐項查證，不假設：

| 需要 | 既有端點 | 夠嗎 |
| --- | --- | --- |
| 列出顧客 | 無 | **缺** |
| 一位顧客的 email／註冊狀態／建立時間 | 無（`.../points` 只回 `shop_id`／`member_id`／`points`／`level_id`／`level`） | **缺** |
| 點數餘額與等級 | `GET /admin/shops/{shopID}/members/{memberID}/points` | 夠 |
| 點數流水帳（分頁） | `GET .../points/transactions` | 夠 |
| 該顧客的訂單 | `GET /admin/shops/{shopID}/orders`——`AdminListParams` 有 `Status`／`PaymentStatus`／`FulfillmentStatus`／`DeliveryFrom`／`DeliveryTo`／`Q`，**沒有 member** | **缺一個參數** |
| 訂單詳情 | `GET .../orders/{orderID}` | 夠 |

**決定：**

```
GET /api/v1/admin/shops/{shopID}/members              page, page_size, q      → member.view
GET /api/v1/admin/shops/{shopID}/members/{memberID}                           → member.view
GET /api/v1/admin/shops/{shopID}/orders               + member_id（選填）     → order.view（既有）
```

**為什麼訂單走參數而不是把訂單塞進顧客回應。** 把最近 N 筆訂單嵌進 `GET /members/{id}` 會產生第二份訂單序列化，而訂單的形狀在過去三個 change 裡動過三次（`order-extras` 的三件組、`checkout-idempotency`、`tier-discount-application` 的折抵三欄）。兩份序列化遲早分歧，而分歧的樣子是「顧客頁上的金額和訂單頁上的不一樣」。一個選填參數複用既有的 `orderJSON` 與既有的分頁，零重複。

**為什麼詳情端點值得存在（而不是叫前端從列表撈）。** 深層連結 `#/members/57` 重新整理後必須畫得出來，而從列表撈需要「先取一整頁再在前端找那一筆」——那是 `admin-commerce-catalog` design D6 已經論證過會說謊的模式（那一筆可能在第 3 頁）。它與列表共用同一個序列化函式，成本是一個 handler。

**additive 的論證**（逐項，比照 `tier-discount-application` D12 的作法）：新增兩條路由（表面鎖只增）、既有端點新增一個**選填** query 參數（不帶時 `ListOrdersAdmin` 走與過去逐行相同的查詢）、**沒有任何既有鍵被移除、改名或改型別**。不使用 D13 的破例窗口。

### D3. 列表揭露哪些欄位：email 要放、phone 不放、搜尋要模糊——三個決定的理由各不相同

**回應欄位定案：** `id`、`shop_id`、`email`、`registered`、`status`、`points`、`level_id`、`level`、`created_at`、`updated_at`。

---

**email：放，而且不遮罩。**

**要面對的事實**：整份顧客 email 名單本身就是有價值的資料，本 change 讓它第一次可以被批次取得。這是一個**真實的擴張**，不是措辭問題。

但擴張的是**粒度**，不是**可見性**：`order.view` 的持有者今天已經逐筆看得到每一筆訂單的收件人姓名、電話與完整地址，而訂單列表同樣是分頁的、同樣可以翻到底。名冊多給的是「沒下過單或訂單已被清掉的那些身分」與「以 email 為軸的集合視角」。界定它的是 D4（誰拿得到）與 D5（不提供批次帶走的捷徑），不是把欄位藏起來。

**已評估並否決：列表遮罩成 `a***@example.com`，詳情才顯示完整值。** 三個理由：(1) API 必須回完整 email——商家要用它聯絡顧客，詳情端點藏不掉，所以遮罩只發生在畫面上，對任何腳本零成本；(2) 對「我要找 email 開頭是 wang 的那位」的商家是純摩擦，而那正是名冊存在的理由；(3) **它會讓平台以為自己做了保護。** 一道擋不住任何人的界線比沒有界線更糟，因為它會停止後續的思考。

---

**phone：不放。而且理由是查證出來的，不是偏好。**

全樹搜尋 `SetPhone`／`SetNillablePhone`：**生產程式碼零命中**（只有三處測試 fixture）。`POST /shop/auth/register` 只接受 email，`guest.Service` 依 `guest-checkout` design D2 **刻意不寫入 phone**（寫了會開出第二條唯一性軸線，而訪客結帳沒有辦法向使用者解釋「你的電話已被使用」）。所以 `members.phone` 在今天的系統裡**恆為 NULL**。

放一個永遠是 null 的欄位進 v1，就是 `tier-discount-application` design D1 診斷過的那個錯誤的重演——**在沒有消費端的時候就把一個空欄位交出去**，然後下一個人以為它有值。顧客的電話在 `orders.shipping_address` 裡（那才是它的語意：這一筆的收件電話），而訂單列表的 `q` 已經可以用電話找人。

日後若出現 phone 註冊路徑，加 `phone` 是純 additive 的一行。

---

**搜尋：`q` 對 email 做大小寫不敏感子字串比對，`q` 可解析為正整數時另比對 member id。**

**「模糊搜尋整個顧客名單是一個列舉面」這句話要拆開看。** 列舉面**不是模糊搜尋開出來的，是列表端點本身**：`GET /members?page=1&page_size=100` 翻幾次就是全表。所以「給名冊但只給精確比對」不會縮小任何攻擊面——它只會讓合法商家必須先知道完整 email 才找得到人，而抓取者照樣翻頁。**那是劇場。**

真正的決定發生在上一層：**要不要給名冊。** 答案是要，理由已在 Why 與上面的 email 段落：這是商家自己的顧客資料，而且以更寬的形式（含地址與電話）早就存在於訂單列表裡。決定了給名冊，模糊搜尋就是那個能力的自然形狀，而不是額外的授予。

**比對 id 是為了與訂單列表的 `q` 同型**：商家從訂單詳情看到「會員 #57」，把 57 貼進搜尋框應該要能用。`recipientOrIDMatches` 已經是這個形狀，兩個名冊的搜尋框行為一致比各自最小化更有價值。

**誠實的邊界，一次說完：**

- `page_size` 上限 100（既有 `normalizePage`，本 change 不放寬）。這是唯一的節流。
- **後台讀取沒有任何領域層審計軌跡**，只有 request log。所以「誰在什麼時候看了顧客名冊」今天答不出來。這是本 change 最大的未緩解事項，記為 Open Questions 的第一條，也是 `member.export` 的前置。
- `ILIKE '%…%'` 走不到索引。與 `admin-commerce-catalog` D6／`admin-commerce-operations` D3 相同的規模判斷與相同的日後路徑（pg_trgm GIN／全文檢索），屆時 API 契約不變。

### D4. `member.view`：只有一個節點，授予 `merchant_owner` 與 `editor`

**為什麼是一個節點而不是兩個。** `member.edit` 沒有消費者：平台今天**沒有任何修改 `members` 的後台端點**（`members` 的寫入路徑只有註冊、訪客結帳認領、以及 `points.Service` 對 `points`／`level_id` 的維護）。新增一個什麼都守不住的節點，會出現在 `GET /shops/{shopID}/me` 的回應裡、出現在 `v1-surface.txt`（不會，它不是契約的一部分，但會出現在權限目錄與角色設定畫面裡），然後讓平台管理員以為他授予了一種能力。**不新增沒有消費者的權限節點**，這條規則在本 change 沒有例外。

**為什麼 `editor` 也拿得到——這是本節真正需要論證的部分。**

先看既有慣例。`editor` 今天持有的 `.view` 節點：`shop.view`、`page.view`、`theme.view`、`category.view`、`product.view`、`order.view`、`payment.view`、`shipping_method.view`、`shipment.view`、`member_tier.view`、`point.view`。目錄裡它**沒有**的 `.view` 只有兩個：

- `user.view` — 後台**成員**名單（與 `user.manage_roles` 成對，是「管管理員」，不是商業資料）。
- `publishable_key.view` — 前端接入身分（seed 註解明說那是整合動作，不是內容編輯）。

也就是說：**`editor` 拿到的是「商家自己的商業資料」的每一個 `.view`**，缺的兩個都是身分／整合類。顧客是商業資料。

再看實質。這一條比慣例更重要：**`editor` 經由 `order.view` 已經看得到比名冊更多的 PII**——每一筆訂單的收件人姓名、電話、完整地址、卡片訊息，而訂單列表分頁可翻、`q` 可搜姓名與電話。名冊給的是 email ＋ 點數 ＋ 等級。

因此「不給 `editor` `member.view`」的實際效果是：**它看得到的 PII 一個字都沒少，只是必須改用訂單列表去做同一件事。** 一條擋不住的界線比沒有界線更糟——它會讓人以為擋住了，然後停止思考真正的界線在哪裡。

而且 `editor` 已經持有 `point.adjust`：它可以**改**一位顧客的點數餘額。拒絕它「看到有哪些顧客」而允許它「改任一顧客的點數」，是一組互相矛盾的授權。

**已評估並否決：把 email 拆成第二個節點（`member.view_contact`），`editor` 只拿到不含 email 的名冊。** 兩個理由：(1) 沒有 email 的名冊只剩一串 id 與點數，那不是名冊，是一份沒有用的清單；(2) `order.view` 那條路仍然開著，所以拆出來的節點擋不住任何人——為零收益增加一個永久的權限節點。**真正該有的粒度是欄位級授權，而本平台的 RBAC 是節點級**，這是一個架構層的缺口，不是本 change 該偷偷開始的實驗。

**三層判定與 `/me` 的自動跟上。** `rbac.Snapshot.Allows` 對新節點與對舊節點完全相同（個人覆蓋 → 角色聯集 → 預設拒絕），個人層的 deny override 一樣有效。`meInShop` 是對**權限目錄逐一** `snap.Allows(name)`，不是硬編碼清單——所以 `member.view` 一經 seed 就自動出現在 `GET /admin/shops/{shopID}/me` 的回應裡，`admin/lib/rbac.js` 不需要任何改動（`admin-commerce-catalog` design D5 的設計在這裡兌現了它的承諾）。

### D5. 不做匯出，也不做彙總指標——兩個「不做」的理由完全不同

**匯出：不做。**

**先釐清 `member-identity-per-shop` 的那條需求說了什麼。** spec「商家顧客資料集自我完備」的原文是：

> 此不變式 MUST 使下列操作可在不影響其他商家的前提下執行：**匯出**單一商家的全部顧客資料、刪除單一商家的全部顧客資料、將單一商家的資料搬遷至獨立資料庫。

它要求的是**平台層的資料可攜性**——`WHERE shop_id = X` 就是那家商家的全部，所以租戶搬遷、租戶刪除、個資刪除請求可以乾淨執行。它的執行者是平台營運，它的驗收是「不會牽動別家商家」。本 change 不動這個不變式，也不需要一顆按鈕來滿足它。**「資料可以被匯出」與「後台有一個匯出鍵」是兩件事。**

**不做的四個理由，由弱到強：**

1. 沒有消費者提出。`admin-commerce-operations` 已把訂單匯出列為 Non-Goal，沒有人回來要過。
2. 一個匯出鍵是**唯一用途就是大量帶走 PII** 的動作。它與「查一位顧客」不是程度差別，是種類差別。
3. **平台今天對後台讀取沒有任何領域層審計軌跡**（只有 request log）。也就是說匯出會是不可追溯的：三個月後沒有人答得出「那份名單是誰匯出的」。在有審計之前先給匯出，順序是反的。
4. **誠實的讓步，必須寫下來**：不做匯出鍵**不會**阻止有心人翻頁抓取（D3 已經說了列表本身就是列舉面）。它阻止的是**隨手**與**意外**——一位員工在離職前順手按一下，或一位商家把 CSV 寄給一個「幫忙做行銷」的外包。**不宣稱這是一道安全控制。** 它是一個不主動提供的決定。

**真的要做匯出時它需要什麼**（寫在這裡，免得下一個人以為只要加一顆按鈕）：一條專用端點、一筆領域層審計紀錄（誰、何時、幾筆、什麼條件）、以及一個**比 `member.view` 更窄**的節點 `member.export`（預設只給 `merchant_owner`）。那是一個 change，不是一顆按鈕。

---

**彙總指標（訂單數、終身消費額）：不做，理由與匯出無關。**

1. **它的定義是有爭議的**：含不含已取消的訂單？含不含未付款的？運費算不算？折抵前還是折抵後？四個問題有十六種組合，而畫面上只會出現一個看起來很權威的數字。**一個錯的權威數字比沒有數字更糟**——商家會拿它做決策（誰該升等、誰該送禮），而沒有任何東西會噴錯。
2. **它是 N+1 或一個 group by**，而名冊是分頁的。要做對就得寫一個彙總查詢，而那個查詢的正確性取決於第 1 點還沒決定的定義。
3. **詳情頁直接顯示真實的訂單列更誠實**：每一列可以點進去對帳，商家自己看得到「這三筆有一筆取消了」。用真實的資料回答「這是不是好顧客」，而不是發明一個指標。

等它真的要做，它是一份**報表**（有期間、有分組、有明確的口徑定義），與 `tier-discount-application` Open Questions 留下的「這一級目前折出了多少錢」是同一份東西。

### D6. 訪客身分：區分、明說、但不提供把他們批次挑出來的捷徑（本 change 對顧客那一側最重要的一節）

**事實。** `guest-checkout` 建立的身分是 `password_hash IS NULL`、`status = 1`。這些人**從未主動註冊**、從未設定密碼、從未同意成為任何名單的一員——他們買了一次花。而系統**沒有任何 email 發送能力**，所以平台也沒有辦法事後徵求他們的同意。

**決定：API 回一個布林 `registered`，UI 把訪客那列明確標成「訪客身分（未註冊）」。**

---

**對商家有意義嗎？有，而且是三件具體的、會影響他當下行為的事：**

1. **訪客無法登入。** `Login` 的 `m.PasswordHash == nil` 分支直接回 401（`guest-checkout` design D10：「使訪客身分無法登入的是 `password_hash IS NULL` 本身」）。商家對這位顧客說「你登入看一下訂單」是**錯誤的指示**，而商家沒有別的辦法知道這件事。他需要說的是「以這個 email 註冊，先前的訂單就會出現」（那是既有的認領路徑）。
2. **這不是一份可以拿去發信的名單。** 沒有標記的話，一份標題寫著「會員」的表格會被當成會員名單使用——這是可預期的，不是假想。
3. **它解釋了為什麼同一個人可能出現兩列。** 訪客結帳以 email 為鍵、不去重；同一位顧客換一個 email 就是另一列。商家看到「訪客身分」才知道要去比對訂單，而不是以為系統壞了。

**對顧客的期待有影響嗎？有，而且這是本節真正的重點。** 一個買過一次花的人並不知道自己在某個系統裡是一列資料，更不會預期自己被稱為某家店的「會員」。平台能做的最小誠實動作有兩個，兩個都做：

- **用詞。** 這個能力叫**顧客名冊**，不叫會員名冊；分頁標籤是「顧客」。`members` 這張表的名字改不動（那是 `member-identity-per-shop` 的既有命名），但商家看到的字可以是對的。
- **畫面上的一句話。** 名冊頂端寫明：「訪客身分是顧客結帳時自動建立的。這些顧客未曾註冊帳號，也未曾同意接收行銷訊息。」這句話寫在畫面上而不是只寫在這份 design 裡，因為讀 design 的人不是會拿名冊去發信的人。

---

**為什麼欄位叫 `registered` 而不是 `has_password`。** 兩者是同一個位元，但名字要說的是**顧客那一邊的事實**：「這個人主動建立過帳號嗎」。`has_password` 會誘使日後有人把它讀成「密碼設定狀態／強度」，然後在它旁邊加第二個密碼相關欄位。同型判斷：`tier-discount-application` design D7 選 `AwardBasisAmount` 而不是 `DiscountedSubtotalAmount`，理由是「名字要描述角色而不是算術」。

**`registered` 洩漏什麼？** 它告訴一個已通過後台認證、且持有 `member.view` 的商家員工：這個 email 在**這家店**有沒有設密碼。這正是 `member.view` 本來就授予的資訊層級，而且它不出現在任何公開端點上（`guest-checkout` design D5 對註冊回應同構的要求完全不受影響——那條要求管的是**未認證的公開端點**）。

**刻意不做 `registered` 的篩選器。** 沒有消費者，而且它最可能的第一個用途正是「把已註冊的挑出來發信」——我們剛在畫面上勸阻的那個動作，不該由 API 提供便利。這是一個**刻意的不對稱**：商家看得到每一位顧客的註冊狀態（他需要它來正確地對話），但平台不提供一個把某一類批次挑出來的參數。同樣的精神見 D5 的匯出決定。

### D7. `password_hash`：兩層防護都成立，但證明必須落在回應位元組上

**第一層（ent）：** `field.String("password_hash")…Sensitive()`。實測產生的 struct 是 `PasswordHash *string \`json:"-"\``（`api/internal/ent/member.go:34`），且 `String()` 不輸出它。

**第二層（handler）：** 本專案每一個序列化函式都是手寫的 `map[string]any`（`orderJSON`、`memberPointsJSON`、`memberTierJSON`…），沒有任何路徑把 `*ent.Member` 直接交給 `httpx.WriteJSON`。新的 `memberJSON` 照這個形狀寫。

**兩層都成立，而兩層都可能被日後的一行程式碼推翻**：有人把 `httpx.WriteJSON(w, 200, memberJSON(m))` 改成 `httpx.WriteJSON(w, 200, m)`，或有人把 `.Sensitive()` 拿掉。前者不會有任何測試噴錯（回應仍是合法 JSON、仍含 `email`／`points`），後者連編譯都不會抱怨。

**因此證明的形式是規定的：斷言落在回應的原始位元組上，不是落在讀程式碼。**

- 整合測試對 `GET /members` 與 `GET /members/{memberID}` 的**回應主體字串**斷言：不含 `password_hash`、不含 `$2a$`／`$2b$`（bcrypt 前綴——即使欄位被改名，雜湊值本身也逃不掉）、不含 `phone`（D3 的決定同時被這條守著）。
- E2E 在真實伺服器上再做一次同樣的斷言（`commerce-path.mjs`，商家 owner 身分）。理由與 `commerce-demo-seed` 對 `wire.go` 的結論同型：能用 Go 測試證明的東西仍然可能在真實接線上失效，而這一條的失效後果是不可回收的。

### D8. 租戶隔離：顯式 `shop_id`，且「跨商家 403」與「跨商家 404」是兩件不同的事

`members` 是租戶資料（`internal/tenant.tenantOwned`），但後台**不在租戶上下文**，所以 interceptor 不會幫忙。`member-identity-per-shop` design D4 把這一點列為該變更的主要風險點。

**實作紀律：**

- `internal/member` 的每一個方法簽名第一個參數就是 `shopID`，查詢一律 `entmember.ShopIDEQ(shopID)`——與 `points.Service.GetMemberPointsAdmin` 逐字同型。
- **不提供任何不帶 `shopID` 的重載或便利方法。** 一個 `GetMember(ctx, id)` 存在的那一刻，就會有人在後台路徑上呼叫它。

**兩種跨商家，回不同的碼，兩者都要測：**

| 情境 | 誰擋 | 回應 |
| --- | --- | --- |
| 呼叫者對 shop B 沒有存取權，打 `/admin/shops/B/members` | `RequireShopPermission` 的 `CanAccessShop()`（在 handler 之前） | **403** |
| 呼叫者對 shop B 有存取權，但 `{memberID}` 是 shop A 的列 | service 的顯式 `shop_id` 條件查不到 | **404** |

第二種回 404 是正確的：那一列不在這家店，說「不存在」比說「你沒權限存取別人的東西」洩漏更少（後者確認了那個 id 存在於某處）。這與既有的 `GetMemberPointsAdmin` 行為一致。

**不變式未被破壞**：本 change 新增的每一條 `Member` 查詢都帶 `shop_id`，`WHERE shop_id = X` 仍然界定該商家的全部顧客資料，租戶搬遷／刪除／個資刪除請求的可執行性不受影響。

### D9. 顧客詳情**不**擁有點數——這是一個被 tripwire #1 與 #2 逼出來的產品決定

顧客詳情最自然的長相是把點數調整表單與流水帳一起放上去。**不做。**

- **tripwire #2（第三次重寫）**：「餘額 ＋ 等級 ＋ 調整表單 ＋ 流水帳表格」在 `views/points.js` 已經有一份，`views/orders.js` 有第二個入口（「檢視此顧客點數」按鈕）。複製到 `views/members.js` 就是第三次，而它**不能**用 `rows()` 解決（它有表單、有錯誤處理、有調整後的重新載入），也就是說它會是真正的重複而不是可抽取的重複。
- **tripwire #1（跨視圖衍生狀態）**：調整點數之後必須同步的是**餘額、等級、流水帳**三處。`admin-commerce-catalog` design D1 的原話是「手動同步兩處還好，四處就是 bug 農場」——三處正好落在那條線上，而且沒有理由主動走過去。
- **產品面也對**：點數只有一個地方可以改，稽核軌跡就只有一條路徑；商家不會在兩個畫面上看到兩個可能不同步的餘額。

**因此詳情的形狀是：**

- **基本資料**：顧客 #id、email、註冊狀態（已註冊／訪客身分）、帳號狀態、建立時間。
- **點數與等級**：餘額與等級名稱，**唯讀**，加一顆「檢視流水帳／調整點數」按鈕連到 `#/points/{id}`（有 `point.view` 才畫）。餘額走 `formatPoints`，永不經過 `formatMoney`（`admin-commerce-operations` design D10——`34` 這個數字丟進 `formatMoney` 會得到 `NT$34`，數字對、單位錯、看起來完全合理）。
- **訂單紀錄**：`GET /orders?member_id={id}&page_size=10` 的精簡列表，每列連到 `#/orders/{id}`，**不分頁**（要看全部就去訂單列表，那裡本來就有分頁與篩選）。金額走 `formatMoney`。有 `order.view` 才畫。

**深層連結**：`#/members`、`#/members/{id}`，沿用既有的 `lib/router.js`；**搜尋條件不入 hash**（`admin-commerce-operations` design D5 的既有規則：深層連結要解的是「這一筆」，不是「這個篩選」）。

### D10. 契約與表面鎖

`openapi.yaml` 新增：兩條路由、`Member` 與 `MemberPage` 兩個具名 schema、`listOrdersAdmin` 的 `member_id` 選填參數。`v1-surface.txt` 以 `-openapi.update-surface` 重生後**逐行檢查 diff 只有新增，零刪除**。

四道檢查逐一預判：

- **①（路由覆蓋）** 會強制兩條新路由必須入契約。注意 `chi.Walk` 的已知盲點（同一樣板上既有子 router 又有方法處理器）——本 change 的 `/members/{memberID}` 與既有的 `/members/{memberID}/points` 是同一個 `chi.Router` 上的兩個**扁平**樣板，不是子 router，預期不踩到；實作時仍以 Walk 的實際清單確認，而不是假設。
- **②（金額 `$ref`）** 不受影響：新回應**沒有任何 `_amount` 結尾的欄位**。`points` 是點數不是金額——這正是 D9 那條規則的另一面，值得記下來：若日後有人把它改名成 `points_amount`，檢查 ② 會要求它 `$ref` `MinorUnitAmount`，而那個要求是對的（名字說了它是金額）。
- **③（回應驗證）** 新端點納入取樣。
- **④（表面鎖）** 是本次動得最多的一道，逐行檢視。

### D11. E2E：掛在既有入口，且**不需要任何佈置**

`commerce-demo-seed` design D2 已定案不開第三個入口。

**瀏覽器（`happy-path.mjs`）**：插在 ⑱.5（等級折扣）之後。那個時點該訪客 email 已經有 **34 點、銀卡會員、兩筆訂單**——全部由前面幾節自然產生，**本區段一行 fixture 都不用寫**。覆蓋：顧客分頁 → 以該 email 搜尋 → 命中一列且標為「訪客身分」→ 進詳情 → 斷言點數是「34 點」且**不含 `NT$`**、等級是銀卡會員、兩筆訂單都在且金額未縮放 → 點「檢視流水帳／調整點數」跳到 `#/points/{id}`。

沿用瀏覽器裡已登入的平台管理員 session，**不另外登入**（登入限流是每 (shop, IP, email) 5 分鐘 5 次，`admin-commerce-operations` design D11 的同一個理由）。本區段**不建立也不刪除任何資料**，因此對後續區段零擾動。

**純 API（`commerce-path.mjs`，商家 owner 身分）**：`/shops/{id}/me` 含 `member.view`、`GET /members` 200 且能搜到該顧客、**回應原始文字不含 `password_hash`／bcrypt 前綴／`phone`**、跨商家 `GET /admin/shops/{other}/members` 回 **403**。

**為什麼這幾條必須以商家 owner 驗**：瀏覽器區段是平台管理員（`*` 全部權限），看不到 merchant scope 的差異——與 `admin-commerce-operations` design D11 對 `shop.manage_client_origins` 的判斷完全同型。

**「沒有 `member.view` 就打不到」的證明放 Go 整合測試**，因為它需要一個刻意缺少該節點的自訂角色，而在 e2e 裡佈置一個角色會留下必須清理的狀態。

## Risks / Trade-offs

- **[名冊讓整份顧客 email 集合第一次可以被批次取得]** → 三道界定：`member.view`（D4）、不提供匯出（D5）、`page_size` 上限 100。**誠實記錄：這三道都不阻止有心人翻頁抓取**（D3 已明說列表本身就是列舉面）；它們阻止的是隨手與意外。真正的緩解是後台讀取的審計軌跡，平台今天沒有，記為 Open Questions 第一條。**不宣稱這個擴張為零。**

- **[訪客身分被商家當成行銷名單]** → D6 的三道處置（用詞、徽章、畫面上的明文）。**但平台沒有 email 發送能力，因此無法事後徵求這些人的同意**——這是一個結構性的缺口，本 change 只能不加深它。記為 `email-verification`／consent 的另一條驅動理由（`guest-checkout` design D6 已命名該 change）。

- **[取代 tripwire #4 可能被讀成「把門檻改高以便通過」]** → D1 的處置是逐條給出替代條件在**同一份資料**上的數值（#4′：23% / 0% / <1%；#5：最大 373 行），而且說明了舊 #4 的結構性缺陷（跨過即永久失效，是一次性的門不是偵測器）。**本 change 沒有規避舊 #4**——它跨過了，並依 tripwire 的要求在寫 code 之前完成了重評。

- **[admin 現在有 ~3,190 行 JavaScript，沒有型別檢查、沒有單元測試、沒有 CI job]** → 這是真實缺口，D1 沒有否認。**刻意不在本 change 加一個象徵性的 CI job**：`node --check` 只驗語法，而語法錯誤在 E2E 的第一個斷言就會炸，那個 job 買不到任何東西，只會讓人以為 admin 有 CI 了。有用的那一階（`tsc --checkJS` ＋ JSDoc 標註，產物仍是原生 ES module、仍無建置步驟）是它自己的 change，需要一輪標註工作。記為 Open Questions。

- **[`q` 的 `ILIKE '%…%'` 無索引]** → 與 `admin-commerce-catalog` D6／`admin-commerce-operations` D3 相同的規模判斷與相同的日後路徑（pg_trgm GIN／全文檢索），API 契約屆時不變。

- **[顧客詳情不提供點數調整，商家要多按一次]** → D9 的刻意取捨，換到「點數只有一個擁有者、一條稽核路徑、不會出現兩個不同步的餘額」，同時讓 tripwire #1／#2 不被觸發。

- **[`member.view` 授予 `editor` 之後，一位內容編輯看得到全部顧客 email]** → D4 已論證他經由 `order.view` 看得到的 PII 更寬（姓名＋電話＋完整地址），且他已持有 `point.adjust`。**真正該有的是欄位級授權，而本平台的 RBAC 是節點級**——這是架構層的缺口，不在本 change 開始實驗。商家若要更嚴的分工，路徑是建立一個不含 `order.view`／`member.view` 的自訂角色（RBAC 已支援）。

- **[新增一個權限節點後，既有部署必須重跑 `make seed` 才會拿到它]** → `seedPermissions` 與 `seedRoles` 皆為冪等 upsert（`seedRoles` 會替既有角色補上缺少的 `role_permission` 列，已查證），所以 `make seed` 就夠，不需要 migration。**但在 seed 之前，兩條新路由對所有人回 403**（節點不存在 → `snap.Allows` 為假）——這是 fail-closed 的正確方向，且 `GET /shops/{id}/me` 不會回報它，所以 UI 連分頁都不畫，不會出現一顆按下去 403 的按鈕。

- **[`GET /orders?member_id=` 讓 `listOrdersAdmin` 的參數面再寬一格，日後想收緊會破壞 v1]** → 選填參數的移除／收緊確實會讓表面鎖轉紅，那正是它存在的目的。這一個參數對應一句商家每天會說的話（「這位顧客買過什麼」），而且它避免了在顧客端點裡長出第二份訂單序列化（D2）。

- **[trade-off：新增一個 `internal/member` package 只為了約 80 行的兩個查詢]** → 接受。既有的每一個領域都有自己的 package，把顧客名冊塞進 `internal/points` 會讓「點數」擁有一份與點數無關的資料存取路徑；寫進 handler 則是本專案唯一的例外。一致性在這裡比行數重要。

## Migration Plan

**無資料庫變更**：不新增資料表、欄位、索引或 CHECK。`api/migrations/` 與 `atlas.sum` **一個位元都不動**——實作後 `make migrate` 應為 no-op，這是驗收條件之一。

實作順序（對應 tasks.md）：

1. **權限節點**：`member.view` 進 `PermissionCatalog`，加入 `merchant_owner` 與 `editor` 的 `Perms`。`make seed` 冪等補上。
2. **API**：`internal/member`（`Service.ListMembers` / `GetMember`，顯式 `shopID`，`WithMemberTier()` 預載等級）、`internal/httpapi/members.go`（`MembersHandler`，兩條路由，`member.view` 守衛，手寫 `memberJSON`）、`router.go` ＋ `app/wire.go` 掛載、`order.AdminListParams.MemberID` ＋ `ListOrdersAdmin` 的條件式 `Where` ＋ handler 的 `optionalIntQuery(r, "member_id")`。
3. **整合測試**：`member.view` 缺席回 403、跨商家回 403、跨商家的 member id 回 404、`q` 的三種比對、分頁、**回應位元組不含 `password_hash`／bcrypt 前綴／`phone`**、`member_id` 篩選、以及「不帶 `member_id` 時訂單列表結果與過去相同」。
4. **契約**：`openapi.yaml` ＋ `v1-surface.txt` 重生，逐行確認零刪除；`make openapi` 四道全綠。
5. **admin**：`views/members.js`、`index.html` 一個 panel、`app.js` 註冊表一列、`styles.css`；修掉 `views/points.js` 與 `index.html` 裡「平台沒有顧客名冊」的過期文案。**`admin/lib/` 不動一行**（D1 的預測，實作後回頭核對）。
6. **E2E**：`happy-path.mjs` 瀏覽器區段（插在 ⑱.5 之後，後續節次編號順移）、`commerce-path.mjs` 的商家 owner API 斷言。
7. **全套驗證**：`make test`、`make test-int`、`make lint`、`make openapi`、`make migrate` 為 no-op、重置 DB 後 `make migrate && make seed-demo` 再跑完整 E2E。
8. **量一次 admin 行數**，把實測值與 #4′／#5 的數值寫進交付回報（D1 要求）。

**Rollback**：移除兩條路由與 `member_id` 參數即回到本 change 前的行為（`v1-surface.txt` 的新增行會一起消失，那是回滾而非破壞）。admin 為靜態檔，回退 commit 即回滾。**唯一留下的是 `permissions` 表裡的 `member.view` 列與兩筆 `role_permission`**——回滾後它們不守著任何東西，無害；真要清除需要手動 SQL，記錄於此。

## Open Questions

- **後台讀取的審計軌跡。** 這是本 change 最大的未緩解事項（D3／D5 各指到它一次）：今天沒有人答得出「誰在什麼時候看了顧客名冊」。它是 `member.export` 的前置，也是任何「更嚴的 PII 分工」的前置。它的形狀不小（要決定記什麼、記多久、誰看得到），是一個獨立 change。
- **`member.export`。** 等有商家真的需要一份 CSV 時再做，且必須在審計軌跡之後。D5 已寫下它需要的三件東西。
- **停用／啟用顧客。** `members.status` 今天**只有 SQL 改得動**，而 `guest-checkout` design D6 的 `checkout_unavailable` 分支已經依賴它——也就是說平台有一個生效中的機制，卻沒有任何操作它的介面。**這是本 change 之後最明顯的下一個缺口**，而它需要的是 `member.edit` 這個節點與一條 PATCH 端點（屆時該節點就有消費者了）。
- **顧客資料刪除請求的執行路徑。** `member-identity` spec 要求它可執行且僅及於該商家，今天同樣只有 SQL。與上一條是同一個 change 的兩面。
- **`admin/` 的 CI job（`tsc --checkJS` ＋ JSDoc）。** D1 與 Risks 已記錄為什麼不在本 change 做，以及為什麼它不需要框架。
- **顧客彙總報表。** D5 已論證它是報表不是欄位；它與 `tier-discount-application` 留下的「這一級折了多少錢」是同一份東西，應該一起做。
