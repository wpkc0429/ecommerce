## 1. 前置查證（先證明缺口存在，再修）

- [x] 1.1 實測確認鎖檔看不見請求參數：從 `openapi.yaml` 移除 `Idempotency-Key`／`X-Publishable-Key` 的宣告，`make openapi` 四道**仍然全綠**——若這一步就轉紅，本變更的前提不成立
- [x] 1.2 盤點契約中全部的請求參數（端點 × 名稱 × 位置 × required），記錄總組數與必填／選填分佈
- [x] 1.3 盤點契約中全部的回應標頭（**含 `components/responses/*` 的 `$ref` 展開**，否則會漏掉 `Retry-After`）
- [x] 1.4 盤點其餘未受任何檢查保護的 v1 表面（就地展開的回應鍵／請求主體鍵／狀態碼集合／`security` 宣告／`error.code`），量化規模，作為 design D5 的取捨依據

## 2. 事實產生器擴充（spec: api-contract）

- [x] 2.1 `openapi_contract_test.go`：新增 `$ref` 解析輔助（`components/parameters/*`、`components/responses/*`），**解析失敗 MUST `t.Fatalf` 並指名該 `$ref`**，MUST NOT 靜默略過（design D7）
- [x] 2.2 參數事實：走訪每條 path × 每個 operation，合併 path-item 層與 operation 層的 `parameters`（同 `(name, in)` 以 operation 層為準），對每一組發射 `METHOD /path <in> <name>`
- [x] 2.3 必填方向性：`required` 非 true 時**額外**發射 `METHOD /path <in> <name> optional`（design D3 的 (c) 路線——方向感知住在產生器，檢查器一字不改）
- [x] 2.4 回應標頭事實：走訪每條 path × operation × 狀態碼（解析 `responses` 的 `$ref`），對每個 `headers` 鍵發射 `METHOD /path response <code> <Header-Name>`
- [x] 2.5 確認既有兩種行（路由、`Schema.property`）的產生路徑**一行未改**（design D6：不得因為擴充而弱化任何一道）
- [x] 2.6 更新鎖檔檔頭 `surfaceFileHead`：五種行形式，並寫明「選填參數產生兩行；把選填改成必填會刪掉 `optional` 那一行 ＝ 破壞」
- [x] 2.7 更新測試檔頭註解：檢查 4 的新涵蓋範圍，以及**明寫的邊界**——不鎖型別／`pattern`／長度／敘述／範例，對約束的收緊沉默（design D4）

## 3. 重新產生鎖檔（spec: api-contract）

- [x] 3.1 `cd api && go test ./internal/httpapi/ -run TestOpenAPIV1SurfaceLock -openapi.update-surface`
- [x] 3.2 **確認 `git diff` 只有新增、零刪除**——出現任何刪除即代表產生器有 bug，MUST 在繼續前查明
- [x] 3.3 抽查排序後的聚集性質：某條路由的參數行緊接在該路由行之後、且在其子路徑之前（design D2）
- [x] 3.4 記錄行數變化（371 → ?）與各類行的組成

## 4. 可信度證明（**本變更唯一真正重要的驗收**）

- [x] 4.1 從 `openapi.yaml` 刪掉兩條結帳端點的 `Idempotency-Key` 參數 → 檢查 4 **必須紅**，訊息指出是哪個端點的哪個參數
- [x] 4.2 把某個 `required: false` 的參數改成 `required: true` → **必須紅**，且訊息指向 `optional` 那一行
- [x] 4.3 刪掉 `X-Publishable-Key`（既有的、非本變更引入的參數）→ **必須紅**
- [x] 4.4 **無害修改必須綠**：只改某個參數的 `description` 文案（不動名稱／位置／required）→ 四道全綠，證明不製造假警報
- [x] 4.5 補證方向性的另一半：把某個 `required: true` 的參數改成 `required: false` → **必須綠**（放寬是 additive）
- [x] 4.6 補證回應標頭：刪掉 `Idempotency-Replayed` → **必須紅**
- [x] 4.7 補證元件層級的盲點已封死：只刪某條端點的 `- $ref: PublishableKey`（保留元件定義）→ **必須紅**
- [x] 4.8 全部還原（`git diff api/openapi/openapi.yaml` 為空），四道全綠

## 5. 驗證

- [x] 5.1 `make test` 全綠
- [x] 5.2 `make test-int` 全綠（需 `make up`，串行 `-p 1`）
- [x] 5.3 `make lint` 無新增問題
- [x] 5.4 `make openapi` 四道（擴充後）全綠
- [x] 5.5 DB 重置後 `make migrate && make seed-demo`，`cd e2e && node happy-path.mjs` 全綠（85 項斷言）
- [x] 5.6 確認 `git diff` 中 `api/openapi/openapi.yaml` 與 `api/internal/**` 的非測試碼**零改動**

## 6. 收尾

- [x] 6.1 更新 `CLAUDE.md`：表面鎖的涵蓋範圍（路由／回應鍵／請求參數／回應標頭）與其粒度邊界
- [x] 6.2 `openspec validate api-contract-parameter-lock` 通過

---

### 實作過程中的補充決定（與 design 的差異）

| 項目 | 決定 | 理由 |
| --- | --- | --- |
| **移除的參數不只 5 個元件，還有 25 個就地展開的名稱**（前置查證時確認） | 鎖在「端點 × 參數」而非元件（design D1）——本來就是這個方向，但查證讓它從「比較好」變成「唯一可行」 | 元件層級的鎖對 `orderID`／`slug`／`token`／`theme_id` 這 25 個就地展開的名稱**結構性地看不見**，只覆蓋 5/30。 |
| `X-Publishable-Key` 有 26 條端點但只有 25 個 `$ref` | 產生器按 **operation** 展開，不按 `$ref` 行數 | `/api/v1/shop/cart/items/{itemID}` 在 **path-item 層**宣告參數，PUT 與 DELETE 共用同一行 `$ref`。承諾是每個 operation 各一個，產生器合併 path-item 層與 operation 層（同 `(name, in)` 以 operation 層為準）。 |
| `TestOpenAPIV1SurfaceLock` 失敗時的收尾 log | 失敗後 `return`，不再印「N 項承諾全部仍在」 | 既有寫法會在錯誤訊息之後緊接著印一句與它矛盾的話（「592 項全部仍在（文件目前共 588 項）」）。純訊息品質修正，判定邏輯未變。 |
| 額外做了第五、六、七項可信度證明 | 放寬方向（`true → false` 必須綠）、回應標頭、**只刪單一端點的 `$ref` 而保留元件** | 前四項只證明「擋得住破壞」，這三項證明的是「不製造假警報」與「元件層級的盲點確實封死」——後者正是 D1 的全部理由，不證等於沒證。 |
| 追加證明「`$ref` 解析不了必須硬失敗」 | 把一個 `$ref` 打成錯字，確認 `t.Fatalf` 指名該 `$ref` | design D7 的斷言，spec 有對應 scenario，不實測等於沒有。 |

### 可信度證明（task 4 的實際輸出）

**⓪ 先證明缺口存在**（把 `components/parameters/{IdempotencyKey,PublishableKey}` 的定義與全部 27 處 `$ref` 刪除，**擴充前的檢查**）

```
--- PASS: TestOpenAPIResponsesMatchImplementation (0.89s)
--- PASS: TestOpenAPICoversEveryRoute (0.01s)
--- PASS: TestOpenAPIMoneyFieldsDeclareUnit (0.03s)
--- PASS: TestOpenAPIV1SurfaceLock (0.02s)
    表面鎖：371 項承諾全部仍在（文件目前共 371 項，新增不受限）
```

四道全綠。`Idempotency-Key` 與 `X-Publishable-Key` 憑空消失，沒有任何一道檢查發出聲音——這是本變更的前提。

**① 刪掉 `Idempotency-Key`（擴充後）**

```
--- FAIL: TestOpenAPIV1SurfaceLock (0.02s)
    4 個 v1 已承諾的項目從契約中消失了——這是破壞性變更。
    若確實要移除，必須同時從 ../../openapi/v1-surface.txt 手動刪掉對應的行（該刪除會出現在 code review 的 diff 中）：
      POST /api/v1/shop/checkout header Idempotency-Key
      POST /api/v1/shop/checkout header Idempotency-Key optional
      POST /api/v1/shop/guest/checkout header Idempotency-Key
      POST /api/v1/shop/guest/checkout header Idempotency-Key optional
```

**② `required: false → true`**（`components/parameters/Page`，收緊）

```
--- FAIL: TestOpenAPIV1SurfaceLock (0.03s)
    7 個 v1 已承諾的項目從契約中消失了——這是破壞性變更。
      GET /api/v1/admin/shops query page optional
      GET /api/v1/admin/shops/{shopID}/members/{memberID}/points/transactions query page optional
      GET /api/v1/admin/shops/{shopID}/orders query page optional
      GET /api/v1/admin/shops/{shopID}/products query page optional
      GET /api/v1/shop/orders query page optional
      GET /api/v1/shop/points/transactions query page optional
      GET /api/v1/shop/products query page optional
```

**消失的恰好只有 `optional` 那 7 行，「接受」的 7 行原封不動**——這就是 design D3 的兩個獨立事實在運作。

**③ 刪掉 `X-Publishable-Key`（既有參數，非本變更引入）**

```
--- FAIL: TestOpenAPIV1SurfaceLock (0.02s)
    52 個 v1 已承諾的項目從契約中消失了——這是破壞性變更。
      DELETE /api/v1/shop/cart/items header X-Publishable-Key
      DELETE /api/v1/shop/cart/items header X-Publishable-Key optional
      DELETE /api/v1/shop/cart/items/{itemID} header X-Publishable-Key
      DELETE /api/v1/shop/cart/items/{itemID} header X-Publishable-Key optional
      GET /api/v1/render/page header X-Publishable-Key
      ...（26 條端點 × 2 行）
```

**④ 無害修改必須綠**（改寫 `Page` 的 `description` ＋ 為 `PublishableKey` 多補一個 `examples` 值）

```
--- PASS: TestOpenAPIResponsesMatchImplementation (0.92s)
--- PASS: TestOpenAPICoversEveryRoute (0.01s)
--- PASS: TestOpenAPIMoneyFieldsDeclareUnit (0.03s)
--- PASS: TestOpenAPIV1SurfaceLock (0.02s)
    表面鎖：592 項承諾全部仍在（文件目前共 592 項，新增不受限）
```

四道全綠。**不製造假警報**——這是本變更能不能活下去的那一項。

**⑤ 放寬方向必須綠**（`theme_id` 的 `required: true → false`）

```
--- PASS: TestOpenAPIV1SurfaceLock (0.03s)
    表面鎖：592 項承諾全部仍在（文件目前共 593 項，新增不受限）
```

文件從 592 項變 593 項——多出來的正是新的 `optional` 行。放寬是 additive，放行。

**⑥ 刪掉回應標頭 `Idempotency-Replayed`**

```
--- FAIL: TestOpenAPIV1SurfaceLock (0.02s)
    2 個 v1 已承諾的項目從契約中消失了——這是破壞性變更。
      POST /api/v1/shop/checkout response 201 Idempotency-Replayed
      POST /api/v1/shop/guest/checkout response 201 Idempotency-Replayed
```

**⑦ 只刪單一端點的 `- $ref: PublishableKey`，元件定義完整保留**（design D1 的全部理由）

```
--- FAIL: TestOpenAPIV1SurfaceLock (0.02s)
    2 個 v1 已承諾的項目從契約中消失了——這是破壞性變更。
      GET /api/v1/shop/products header X-Publishable-Key
      GET /api/v1/shop/products header X-Publishable-Key optional
```

**⑧ `$ref` 解析不了必須硬失敗**（把 `IdempotencyKey` 打成 `IdempotencyKeyy`）

```
--- FAIL: TestOpenAPIV1SurfaceLock (0.02s)
    openapi.yaml: $ref "#/components/parameters/IdempotencyKeyy" does not resolve (no "IdempotencyKeyy")
```

不是靜默略過該參數（那會讓 `-openapi.update-surface` 安靜地寫出一份少了兩行的鎖檔）。

**⑨ 全部還原後**（`git diff api/openapi/openapi.yaml` 為空）

```
--- PASS: TestOpenAPIResponsesMatchImplementation (1.20s)
--- PASS: TestOpenAPICoversEveryRoute (0.02s)
--- PASS: TestOpenAPIMoneyFieldsDeclareUnit (0.03s)
--- PASS: TestOpenAPIV1SurfaceLock (0.02s)
```

### 覆蓋數字（實測）

| 項目 | 變更前 | 變更後 |
| --- | --- | --- |
| 表面鎖總行數 | 371 | **592** |
| ├ 路由 | 111 | 111（不變） |
| ├ 具名 schema 屬性 | 260 | 260（不變） |
| ├ 請求參數「接受」 | — | **164**（30 個不重複名稱，其中 25 個就地展開） |
| ├ 請求參數「可省略」 | — | **48** |
| └ 回應標頭 | — | **9**（`Retry-After` ×6、`Idempotency-Replayed` ×2、`X-Cache` ×1） |
| 鎖檔 diff | — | **238 新增 / 3 刪除，且 3 個刪除全部是舊的檔頭註解行，零項目行被刪** |
| `openapi.yaml` 改動 | — | **0 位元組** |
| `api/internal/**` 非測試碼改動 | — | **0 行** |

### 排序後的聚集性質（design D2 實測）

```
GET /api/v1/render/page
GET /api/v1/render/page header X-Publishable-Key
GET /api/v1/render/page header X-Publishable-Key optional
GET /api/v1/render/page header X-Site-Domain
GET /api/v1/render/page header X-Site-Domain optional
GET /api/v1/render/page query path
GET /api/v1/render/page query path optional
GET /api/v1/render/page response 200 X-Cache
```

空白（0x20）排在 `/`（0x2F）之前，於是參數行自然落在該路由行之下、其子路徑之上。不必分區、不必改排序函式。

### 驗證結果

| 項目 | 結果 |
| --- | --- |
| `make test` | 全綠 |
| `make test-int` | 全綠（首次執行時 `TestLimiterRecoversAfterWindow` 轉紅一次——200 ms 視窗 ＋ 300 ms sleep 的掛鐘測試，在 `-p 1` 全套負載下的既有 flake；單獨與重跑全套皆綠，且本變更零非測試碼改動） |
| `make lint` | `0 issues.` |
| `make openapi` | 四道全綠 |
| DB 重置 → `make migrate && make seed-demo` → `cd e2e && node happy-path.mjs` | **PASSED，85 項斷言** |
