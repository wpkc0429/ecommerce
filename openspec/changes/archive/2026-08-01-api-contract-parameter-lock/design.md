## Context

`openapi-contract` 建立的四道檢查裡，第四道（`api/openapi/v1-surface.txt`）的存在理由是**一句話**：

> 鎖檔的每一行今天仍存在於 `openapi.yaml` 中。

這句話短到可以在 code review 時原地驗證，這正是它可信的原因。它今天涵蓋兩種行：

```
METHOD /path            111 行   一條公開路由
SchemaName.property     260 行   一個具名 component schema 的屬性
```

**實測的缺口（本變更的前置查證）：**

```
契約宣告的「端點 × 請求參數」組數                    164   （30 個不重複的參數名）
其中經 components/parameters 共用者                    5   （PublishableKey / IdempotencyKey / ShopIDPath / Page / PageSize）
其中就地展開（inline）者                              25   個不重複名稱
受任何一道檢查保護者                                   0
```

```
契約宣告的回應標頭（展開 $ref 後，端點 × 狀態碼 × 標頭）  9
受任何一道檢查保護者                                     0
   GET  /api/v1/render/page          200  X-Cache
   POST /api/v1/shop/checkout        201  Idempotency-Replayed
   POST /api/v1/shop/guest/checkout  201  Idempotency-Replayed
   （429 Retry-After ×6，來自共用的 responses/TooManyRequests）
```

已直接驗證：把 `components/parameters` 五個元件連同 164 處引用整段刪除，`make openapi` **四道全綠**。

**這個缺口的方向比它的規模重要。** 回應端的破壞有第二道防線——檢查 3 用真實回應對 schema 驗證，且 D14 的 `additionalProperties: false` 讓多回一個鍵也會轉紅。請求端**一道防線都沒有**：沒有任何測試對「這條端點接受哪些參數」發表意見。而請求端的破壞對消費端更致命——回應多一個鍵，消費端忽略它就沒事（契約 `info.description` 已明文要求忽略未知鍵）；端點不再接受 `X-Publishable-Key`，ONDO 的每一個請求當場 404。

**承接的既有紀律，全部不得動搖：**

- **D13 的 v1 破例窗口已於 `order-extras` 用盡並關閉**。本變更是純測試＋鎖檔的變更，`openapi.yaml` **逐位元組不變**。
- **假警報是防腐機制最快的死法**（`openapi-contract` design D3a 的原話，該設計在實作前就因為 `chi.Walk` 的盲點回頭改過一次）。任何會讓「改一句敘述」轉紅的設計，本變更視為失敗而非嚴格。
- **四道檢查全部住在 `internal/httpapi` 的普通 Go 測試裡**，因此自動進 CI，`ci.yml` 不必改。這一條的理由（`commerce-demo-seed` 實測發現 e2e 不在 CI 內）在本變更完全適用。
- 鎖檔 **MUST NOT 依賴 git 歷史或任何滑動的比較基準點**。

## Goals / Non-Goals

**Goals:**

- **請求參數成為 v1 表面鎖的一等公民**：移除任一端點的任一參數、或把選填改成必填，MUST 轉紅，且訊息指出是哪個端點的哪個參數。
- **放寬 MUST NOT 轉紅**：`required: true → false`、補 `examples`、放寬 `pattern`、改寫 `description` 全部是 additive 或純文字，MUST 綠。
- **檢查器那句唯一的規則一個字都不改**——這是鎖檔可信度的來源，擴充涵蓋範圍不得以犧牲它為代價。
- **既有 371 行逐字不動**，diff 全部是新增（若出現任何刪除，那本身就是本變更引入 regression 的訊號）。
- **順帶把「還有什麼沒被保護」據實盤點出來**，納入的說明理由，不納入的也說明理由與規模，不假裝沒看到。

**Non-Goals:**

- **不改任何 API 行為**、不改 `openapi.yaml`、不改 `api/internal/**` 的非測試碼。
- **不新增第五道檢查**（見 D6）。
- **不鎖參數的 schema 細節**（型別／`pattern`／`minLength`／`format`／`description`／`examples`），見 D4。
- **不鎖就地展開的回應／請求主體之鍵**（60 ＋ 106 個），見 D5——盤點出來、記為後續，本變更不做。
- **不做 OpenAPI 的通用「破壞性變更偵測器」**（`oasdiff` 之類）。那是一個對「兩份文件的差異」發表意見的工具，而鎖檔對「v1 承諾過什麼」發表意見——後者不隨 git 歷史滑動，前者隨之滑動。這個差別在 `openapi-contract` design D5 已論證過，本變更不推翻它。

## Decisions

### D1. 鎖在「端點 × 參數」，不鎖 `components/parameters`

兩種粒度都能擋住「把 `IdempotencyKey` 元件整個刪掉」，但只有一種能擋住真正常見的那種破壞：

```yaml
# 元件定義原封不動保留 ↓                    元件層級的鎖：全綠
components:
  parameters:
    PublishableKey: { name: X-Publishable-Key, in: header, ... }

paths:
  /api/v1/shop/products:
    get:
      parameters:
-       - $ref: '#/components/parameters/PublishableKey'   # ← 這條端點不再接受它
```

消費端依賴的承諾是「**這條端點**接受 `X-Publishable-Key`」，不是「文件裡有一個叫 PublishableKey 的可重用元件」。元件是文件的內部組織方式，不是對外承諾——這與既有的路由行同理：鎖的是 111 條路由，不是「router 存在」。

**第二個決定性理由：25 個不重複的參數名根本沒有元件**（`orderID`、`slug`、`theme_id`、`token`、`status`、`flat`、`path`、`category_id`…全部就地展開）。元件層級的鎖對它們**結構性地看不見**——只能覆蓋 5/30，而 `token`（預覽權杖）與 `theme_id`（切換主題的前置檢查）這種必填查詢參數一旦消失，後台當場壞掉。

**代價，誠實記錄：** 164 行而不是 5 行。`X-Publishable-Key` 在 26 條端點上各佔一行，`shopID` 佔 58 行。這是重複，但它是**產生出來的**重複，維護成本為零；而它換到的是「刪掉某條端點的某個參數」這個具體動作在 diff 中留下具體痕跡。真正該問的是「這 164 行會不會製造噪音」——不會，因為它們只在**移除**時說話（D3）。

### D2. 行的形式：五種行、同一份排序清單、與既有兩種行同居

```
METHOD /path                                   一條公開路由                   （既有 111）
SchemaName.property                            具名 schema 的屬性             （既有 260）
METHOD /path <in> <name>                       該端點接受此請求參數            （新增 164）
METHOD /path <in> <name> optional              且此參數可以省略                （新增  48）
METHOD /path response <code> <Header-Name>     該端點承諾在此狀態碼回應此標頭   （新增   9）
```

`<in>` 逐字採用 OpenAPI 自己的詞彙（`header` / `query` / `path` / `cookie`），**不發明對照表**。這與 D3 選 `chi.Walk` 的理由同源：chi 的路徑樣板與 OpenAPI 的 path template 逐字相同，比對不需要轉換規則；同理，鎖檔的 `header` 與 YAML 的 `in: header` 逐字相同，讀鎖檔的人可以零翻譯地跳回 YAML 的那一行。

**為何不用縮寫或符號**（`?page`、`<-header:Name`、`Schema.param.page`）。鎖檔的讀者不是解析器，是**在 code review 裡看到一行被刪掉的人**。那一行必須自我解釋：

```
- POST /api/v1/shop/checkout header Idempotency-Key optional
```

這一行被刪掉，審查者不需要任何背景知識就知道發生了什麼。符號會讓他先問「`<-` 是什麼意思」。

**排序後的意外收穫（實測確認）。** 既有檔案是單一 `sort.Strings` 的扁平清單。空白（0x20）排在 `/`（0x2F）之前，於是同一條路由的參數行**緊接在它的路由行之後、且在它的子路徑之前**：

```
GET /api/v1/shop/products
GET /api/v1/shop/products header X-Publishable-Key
GET /api/v1/shop/products header X-Publishable-Key optional
GET /api/v1/shop/products query page
GET /api/v1/shop/products query page optional
GET /api/v1/shop/products query page_size
GET /api/v1/shop/products query page_size optional
GET /api/v1/shop/products/{slug}
```

不必分區、不必加標題、不必改排序函式，一個端點的完整承諾自然聚成一段。`header` < `path` < `query` < `response` 的字母序也剛好是「先進後出」的閱讀順序。

### D3. `required` 的方向性：拆成兩個獨立事實，而不是讓檢查器變聰明

這是本變更唯一有真正取捨的決定。`true → false` 是放寬（additive，應綠），`false → true` 是收緊（破壞性，應紅）。三條路：

| | 做法 | `false→true` | `true→false` | 檢查器 |
| --- | --- | --- | --- | --- |
| **(a)** 單行帶旗標：`… page query required\|optional` | 最少的行 | 紅 ✓ | **紅 ✗ 假警報** | 純字串比對 |
| **(b)** 檢查器解析行、比較方向 | 最少的行且正確 | 紅 ✓ | 綠 ✓ | **變成規則引擎** |
| **(c)** 兩個獨立事實：`… page query` ＋ `… page query optional` | +48 行 | 紅 ✓ | 綠 ✓ | **純字串比對，一字不改** |

**(a) 直接出局。** 它讓一個 additive 的動作轉紅，正是本變更宣稱要避免的失敗模式。有人會說「放寬必填在本專案幾乎不會發生（路徑參數依 OpenAPI 規範永遠 `required: true`，能放寬的只有 `theme_id` 與 `token` 兩個查詢參數）」——這是真的，但把一個**已知會誤報**的設計留在防腐機制裡，靠「大概不會踩到」保護它，與 D3a 當初拒絕人工白名單的理由一模一樣。

**(b) 才是真正誘人的那個，也是必須拒絕的那個。** 它的行數最少、語意最直接。它的代價不在程式碼，在**那句話**：

> 檢查規則從「鎖檔的每一行今天仍存在於契約中」，變成「鎖檔的每一行依其種類套用對應的比較規則，其中參數行的 `required` 欄位採方向感知比較」。

第一句話任何人都能在 review 時原地驗證：把那一行貼進 `openapi.yaml` 搜尋，找到就是綠的。第二句話要驗證得先讀測試程式碼。**鎖檔的可信度不來自它擋住多少東西，來自任何人都能在三十秒內確認它有沒有在說謊**——一份沒有人能快速驗證的防腐機制，最後會變成一份沒有人敢動的檔案。

**(c) 的洞察：`required: false` 不是一個修飾語，是兩個獨立的承諾。**

- 「這條端點在這個位置接受名為 `page` 的參數」——移除它是破壞。
- 「你可以不帶它」——收回它也是破壞。

必填參數只做出第一個承諾（它從未承諾你可以省略），所以只產生一行。選填參數做出兩個，所以產生兩行。於是：

```
false → true   `… query page optional` 這一行從事實集合中消失 → 紅，訊息指名該行
true  → false  既有的 `… query page` 仍在；`… query page optional` 是新增 → 綠
移除參數        兩行同時消失 → 紅，兩行都被指名
```

**方向感知確實存在，但它住在產生器裡，不住在檢查器裡。** 這個分工是刻意的：產生器在**開發者面前**執行（`-openapi.update-surface`，人正盯著 diff），聰明一點沒關係；檢查器在 **CI 裡**執行，它的價值全部來自「笨到不可能誤判」。

**代價：+48 行、且對第一次讀的人有一瞬間的困惑**（「為什麼 `page` 有兩行？」）。用檔頭的兩行說明買斷：

```
#   METHOD /path <in> <name>            該端點接受此請求參數
#   METHOD /path <in> <name> optional   且該參數可以省略（把選填改成必填會刪掉這一行 ＝ 破壞）
```

### D4. 粒度停在「名稱 ＋ 位置 ＋ 可否省略」——不鎖 schema 細節

被刻意排除的：`type`、`format`、`pattern`、`minLength` / `maxLength`、`minimum`、`enum`、`default`、`description`、`examples`。

**排除敘述與範例是不需要論證的**（純文字，改它不改變任何行為，鎖住它等於禁止改文件）。真正需要論證的是**型別與格式限制**，因為「把 `pattern` 改嚴」確實是破壞性變更。三個理由，由弱到強：

1. **多數約束的破壞方向與鎖的方向相反。** 鎖檔的機制是「這一行必須還在」，它天生擅長偵測**移除**。但約束的破壞是**收緊**（加上 `pattern`、提高 `minLength`、縮小 `enum`），而收緊在鎖檔裡表現為「新增一行」——鎖檔對新增一律放行。要偵測收緊得改成 D3 的 (b) 路線，對每一種約束各寫一條方向規則（`minimum` 變大是收緊、`maximum` 變大是放寬、`enum` 變短是收緊、`pattern` 沒有偏序關係……）。這不是「多寫幾行」，這是一個完整的 JSON Schema 相容性判定器。

2. **假警報的比例會壓倒真警報。** `X-Publishable-Key` 的 `pattern: '^pk_[a-z2-7]+$'` 若寫進鎖檔，任何一次 base32 字元集的修正、任何一次為了容納新格式而放寬——全部是 additive 或修正，全部轉紅。而它擋住的是「有人把 pattern 改嚴」這個從未在本專案發生過的動作。**用高頻的假警報換低頻的真警報，是防腐機制自殺的標準劇本。**

3. **決定性的：這三個事實才是消費端的程式碼真正依賴的東西。** ONDO 寫下的是
   ```js
   fetch(url, { headers: { 'X-Publishable-Key': KEY, 'Idempotency-Key': k } })
   ```
   它依賴的是：**哪個名字**（`X-Publishable-Key`）、**放在哪裡**（header 而非 query）、**能不能不放**（省略時退回 Host）。`pattern` 收緊時，它送出的值多半仍然合法；參數改名或搬家時，它**必定**當場壞掉。鎖應該鎖住必定會壞的那三件事，把不一定會壞的留給 code review。

**誠實的邊界，明寫進測試檔頭：** 本鎖對「把 `Idempotency-Key` 的 `maxLength` 從 255 改成 32」**沉默**。這是已知且接受的缺口，不是疏漏。可機械化又不誤報的部分，本鎖百分之百擋住；剩下的部分**不宣稱**擋得住——這與 `openapi-contract` design D5 對「語意破壞」的處置態度一致（鎖檔負責可機械化的部分，並且明確不宣稱它能擋語意）。

### D5. 其他未受保護表面的盤點：納入回應標頭，其餘據實記錄為後續

既然要動這道檢查，把「還有什麼沒被任何一道檢查保護」一次盤清。實測結果：

| # | 未受保護的表面 | 規模 | 本變更 | 理由 |
| --- | --- | --- | --- | --- |
| 1 | 請求參數 | 164 組 | **納入** | 本變更的驅動力 |
| 2 | 回應標頭 | 9 組 | **納入** | 見下 |
| 3 | 就地展開的回應物件之鍵 | 35 個回應 / 60 個鍵 | 後續 | 見下 |
| 4 | 就地展開的請求主體之鍵 | 42 個主體 / 106 個鍵 | 後續 | 見下 |
| 5 | 每個端點的狀態碼集合 | 477 組 (op, status) | **不做** | 與 D8 的紀律打架 |
| 6 | 每個端點的 `security` 宣告 | 111 組（含 14 條 `security: []`） | 後續 | 觸發條件已被記錄 |
| 7 | `error.code` 的字面值集合 | 純敘述文字 | **不做** | D8 已明確否決 |

**#2 回應標頭：納入，而且不納入才是不合理的。** `Idempotency-Replayed` 與 `Idempotency-Key` 是**同一個承諾的兩端**——`checkout-idempotency` design 明文說它「把可觀測性還給接入者」，是接入方唯一能驗證「我的冪等鍵到底有沒有生效」的手段。本變更若只鎖進去的那一半，等於交付一個保護 request 不保護 response 的冪等契約。技術成本：同一個產生器多一個發射點，同一種行形式，9 行。

有一個小陷阱值得記下：`Retry-After` 定義在共用的 `components/responses/TooManyRequests`，若不解析 `$ref` 就只看得到 3 個標頭而漏掉 6 個。產生器因此必須解析 `responses` 的 `$ref`——與解析 `parameters` 的 `$ref` 是同一段程式碼。

**#3／#4 就地展開的鍵：這是本次盤點發現的最大缺口，但它不是本變更的形狀。** 規模比參數還大（166 個鍵），而且 `GET /api/v1/shop/me` 這種 ONDO 會走的端點就在裡面（它的 `id`／`email`／`phone` 完全沒有元件、也不在鎖檔中）。不納入的理由是**它需要的不是同一個決定**：

- **定址方式不同。** 具名 schema 的行是 `Order.total_amount`——一個全域唯一的名字。就地展開的鍵沒有名字，只有位置，而位置是巢狀的（`GET /api/v1/admin/shops/{shopID}/page-types` 的鍵在 `page_types[].type_key`）。要鎖它就得發明一套巢狀定址語法，而那套語法會反過來變成鎖檔最難讀的部分。
- **正確的修法可能根本不是鎖。** 更可能的答案是一道**紀律檢查**：「回應的 schema MUST 是具名元件的 `$ref`」——那會把 60 個鍵一次全部搬進既有的 `Schema.property` 行，鎖檔一行語法都不必新增，而且順帶讓契約更好讀。但那是**改 `openapi.yaml` 的變更**（要新增 35 個 schema 元件），與本變更「`openapi.yaml` 逐位元組不變」的約束直接衝突。
- **請求端的 additive 方向與回應端相反**（#4）：回應多一個鍵是 additive，請求少接受一個欄位才是；同一套判定不能直接套用。

因此記為 `api-contract` 的下一個候選後續，並在此寫下**具體的形狀與規模**（不是「以後再說」），讓下一個人不必重做這次盤點。

**#5 狀態碼：不做，而且是主動拒絕。** `openapi-contract` design D8 明訂「端點層只列呼叫端需要分辨並採取不同動作的狀態碼」，500 與多數 429 刻意不列。鎖住狀態碼集合等於把這條紀律凍結成鎖檔的一部分——日後把某個端點的 `409` 收斂成 `422`（一次澄清而非破壞）會轉紅，而真正的破壞（新增一個未記載的 4xx）反而是 additive 不會轉紅。方向不對。

**#6 `security` 宣告：後續，且觸發條件已經寫好了。** 把 `security: []`（14 條公開端點，包含 ONDO 走的全部目錄與訪客結帳）改成需要認證，是明確的破壞性變更，目前無人守護。但 `openapi-contract` design D3 已論證：security 宣告是**人工維護的敘述**，鎖住敘述只是鎖住敘述——真正要驗證授權必須對每個端點實際發無憑證請求。它記錄的觸發條件（「出現第一個第一方以外的後台消費端」）在此原樣適用。鎖住 `security: []` 這 14 條的變體或許值得單獨評估，但它與參數鎖沒有共用機制，硬塞進來只會讓本變更變成一個雜燴。

**#7 `error.code`：不做。** D8 已論證：`code` 散落在 9 個 `write*Error` 函式，沒有集中列舉，也沒有機械方式驗證清單完整——「列一份必然會漏且無法被證偽的清單，比不列更糟」。本變更不推翻這個判斷。

### D6. 擴充檢查 4，不新增第五道檢查

技術上可以寫一個獨立的 `TestOpenAPIParameterLock` 配一份 `v1-parameters.txt`。不這麼做的理由：

- **鎖檔的單一性就是它的可讀性。** 「v1 承諾過什麼」應該有一個地方回答。拆成兩個檔案後，「這條端點的完整承諾」要跨檔閱讀，而 D2 那個「參數行自然聚在路由行下面」的性質會直接消失。
- **兩個檔案就是兩個會不同步的東西。** 兩份鎖檔、兩個更新 flag、兩個檔頭警語，其中一份被遺忘的機率不是零。
- **對「四道檢查」這個既定說法的傷害。** `CLAUDE.md`、`README.md`、`Makefile` 的 `openapi` target、`openapi-contract` 的 spec 全部圍繞「四道」組織。變成五道要動四處敘述，換到的只是一個測試函式名——而本變更的實質是「第四道看得更多」，不是「多了一道」。

**因此：檢查 4 的判定邏輯逐字不變，只有產生事實集合的那個函式（`currentSurface`）多發射三種行。** 既有兩種行的產生路徑一行不動——這是「不得因為擴充而弱化任何一道」在本變更裡最具體的落實方式。

### D7. 產生器遇到解析不了的 `$ref` MUST 讓測試失敗，不得靜默略過

新增的三種行都需要解析 `$ref`（`components/parameters/*`、`components/responses/*`）。若某個 `$ref` 指向不存在的元件，有兩種處置：

- **靜默略過**：檢查階段是安全的（該參數的事實消失 → 鎖檔的行對不上 → 紅）。但**產生階段是危險的**：`-openapi.update-surface` 會安靜地寫出一份少了那些行的鎖檔，而執行它的人正在看 diff——他會看到「刪除」，但看不出原因是打錯字。
- **硬失敗**：`t.Fatalf` 指名那個 `$ref`。

選硬失敗。理由是產生器與檢查器的信任模型不同：檢查器可以保守（存疑就轉紅），產生器**必須誠實**，因為它的輸出會被當成新的事實基準提交進 git。一份靜默不完整的鎖檔比一份轉紅的測試危險得多。

（`openapi.yaml` 目前不存在斷掉的 `$ref`——這是為日後準備的護欄，成本是三行程式碼。）

## Risks / Trade-offs

- **[鎖檔從 371 行長到 592 行，`X-Publishable-Key` 與 `shopID` 大量重複]** → 已知並接受（D1）。它是產生出來的，維護成本為零；排序讓它聚在對應的路由底下（D2），不是散落的噪音。真正的風險是「檔案太大導致 review 時被略過」——緩解是 diff 的性質：本變更之後，這個檔案的正常 diff 只會有**新增**行，任何一行刪除都是刻意的破壞行為，在 diff 中極其顯眼。

- **[不鎖型別與格式限制，`maxLength: 255 → 32` 這類收緊會沉默]** → D4 正面記錄，並寫進測試檔頭。這是「擋得住破壞 vs 不製造噪音」這個取捨的付款側，不是疏漏。**明確不宣稱鎖得住約束的收緊。**

- **[+48 行的 `optional` 對第一次讀鎖檔的人是困惑]** → D3 選擇用檔頭兩行說明買斷，換到的是「檢查器那句話一字不改」。若日後證明這個困惑是真實成本（例如有人誤刪 `optional` 行以為是重複），退路是把兩行合併成 (a) 並接受放寬會誤報——但那是把可信度換給美觀，不建議。

- **[本變更擴大了「改契約」的摩擦：以後移除任何參數都多一個手動步驟]** → 這是**刻意加上**的成本，也是它的全部意義（與 `openapi-contract` 對新增端點的處置相同）。緩解：失敗訊息直接把該刪的行原文列出來，把「猜哪裡不對」變成「照著刪」。

- **[盤點出兩個更大的缺口（166 個就地展開的鍵）卻不修]** → 誠實記錄於 D5，並寫下具體形狀、規模、與**建議的修法**（把回應 schema 一律提為具名元件，而不是擴充鎖檔語法）。不順手做的理由與 `checkout-idempotency` 拒絕順手做本變更的理由一致：驅動力應該是一個具體缺口，不是機制的完備性；而且那個修法會動到 `openapi.yaml`，與本變更「零契約變更」的約束衝突。

- **[產生器與檢查器共用 `$ref` 解析，若解析邏輯本身有 bug，鎖檔與檢查會一起錯]** → 這是所有 golden-file 機制的固有性質（`gen:types` 的 `git diff --exit-code` 同理）。緩解是本變更的驗收要求：四項可信度證明**必須實際執行並貼出輸出**，其中包含一次「無害修改必須綠」——那一項正是在證明解析邏輯沒有把敘述當成事實。

## Migration Plan

無資料遷移、無部署順序耦合、無下游動作。

1. 擴充 `currentSurface` 的事實產生（既有兩種行的程式路徑不動）。
2. `go test ./internal/httpapi/ -run TestOpenAPIV1SurfaceLock -openapi.update-surface` 重新產生鎖檔。
3. **確認 diff 只有新增、沒有刪除**——出現任何刪除即代表產生器有 bug，MUST 在提交前查明。
4. 執行四項可信度證明（刪 `Idempotency-Key`／`false→true`／刪 `X-Publishable-Key`／只改 `description`），還原後四道全綠。

回滾：`git revert` 即可。伺服器不讀鎖檔也不讀契約，回滾對執行期零影響。

## Open Questions

- **就地展開的回應／請求 schema（166 個鍵）該用哪種修法？** D5 已寫下兩個候選（擴充鎖檔的巢狀定址 vs 把回應 schema 一律提為具名元件並沿用既有的 `Schema.property` 行）並傾向後者，但後者要動 `openapi.yaml`（新增約 35 個 schema 元件），是一個獨立變更的規模。此刻決定會是憑空承諾，故留白——但**規模與形狀已經量化，下一個人不必重做盤點**。

- **`security: []` 的 14 條公開端點要不要單獨上鎖？** 把公開端點改成需要認證是明確的破壞，但鎖住 `security` 宣告只鎖住了敘述（`openapi-contract` design D3 已論證授權宣告是人工維護的）。真正的驗證要對每條端點發無憑證請求。與 D5 #6 同一個觸發條件：出現第一個第一方以外的消費端時重新評估。

- **鎖檔會不會有一天大到需要拆分？** 592 行還遠遠不到。可以先記下**拆分的正確切法不是「參數一個檔、路由一個檔」**（那會摧毀 D2 的聚集性質），而是依 API 分區（`/admin` 一份、`/shop` 一份）。同樣不為想像中的問題付現金。
