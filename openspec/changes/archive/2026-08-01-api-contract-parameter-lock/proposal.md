## Why

姊妹 change `checkout-idempotency` 在**實作完成後自報**了一個它無法自己修的缺口（該 change 的 design Open Questions 原文）：

> `v1-surface.txt` 收錄兩種行：路由（`METHOD /path`）與具名 schema 的屬性（`Schema.property`）。它**不收錄 `components/parameters`**，因此本變更**沒有在鎖檔裡新增任何一行**——`Idempotency-Key` 這個新的公開承諾目前不受鎖檔保護，日後有人把它從契約中拿掉，四道檢查全部沉默。

實測確認這句話為真，而且範圍比它自報的更大：契約目前宣告 **164 組「端點 × 請求參數」**（30 個不重複的參數名），**沒有任何一組被任何一道檢查保護**。把 `Idempotency-Key`、`X-Publishable-Key`、`X-Site-Domain`、`page`、`page_size`、`theme_id`、`token` 從 `openapi.yaml` 整段刪掉，`make openapi` 四道**全綠**——路由還在、schema 屬性還在，鎖檔看不見參數。

這件事的嚴重性不在數量而在**方向**：表面鎖的全部意義是把 D13 的「v1 只做 additive 變更」從一句口號變成「一個必須手動刪掉一行、且該刪除會出現在 code review diff 中的動作」。一個**看不見請求參數**的表面鎖，對「把某個選填標頭改成必填」「悄悄不再接受某個查詢參數」這兩種最常見的請求端破壞完全無感——而請求端的破壞比回應端更致命：回應多回一個鍵，消費端忽略它就沒事；請求端少接受一個參數，消費端的呼叫直接失效。

`checkout-idempotency` 把它記為 `api-contract` 能力的候選後續（理由是「補它要改動 `openapi-contract` 能力的檢查邏輯與鎖檔格式，屬於那個能力的變更而不是這一個」）。本變更就是那個後續。

## What Changes

- **表面鎖的事實集合擴充為五種行**（原本兩種）。新增的三種全部是**每個端點一組**，不是每個 `components/parameters` 元件一組——消費端依賴的承諾是「**這條端點**接受 `X-Publishable-Key`」，不是「文件裡剛好有一個可重用元件」。把 `- $ref: PublishableKey` 從某條端點刪掉而保留元件定義，是一次真實的破壞，元件層級的鎖會沉默。

  ```
  METHOD /path                              一條公開路由            （既有）
  SchemaName.property                       具名 schema 的屬性       （既有）
  METHOD /path header|query|path NAME       該端點接受此請求參數      （新增）
  METHOD /path header|query|path NAME optional   且此參數可以省略     （新增）
  METHOD /path response CODE Header-Name    該端點承諾回應此標頭      （新增）
  ```

- **`required` 的方向性由「兩個獨立事實」表達，而不是由檢查器的方向感知邏輯表達**：選填參數同時產生「接受」與「可省略」兩行，必填參數只產生「接受」一行。於是 `false → true`（收緊，破壞性）讓 `optional` 那一行消失 → **紅**；`true → false`（放寬，additive）只是新增一行 `optional` → **綠**。**檢查器那句唯一的規則一個字都不改**（「鎖檔的每一行今天仍存在於契約中」），方向感知住在產生器裡。

- **粒度刻意停在「名稱 ＋ 位置 ＋ 可否省略」**，不鎖 `schema` 的型別、`pattern`、`minLength`、`description`、`examples`。理由與 `openapi-contract` design D3a 同源：**假警報是防腐機制最快的死法**。放寬 `pattern`、補一個 `example`、修一句敘述全部是 additive 或純文字，鎖住它們會讓這些日常動作轉紅，而人會學會忽略轉紅的測試。

- **順帶納入回應標頭（9 組）**，因為 `Idempotency-Replayed` 是**同一份契約的另外一半**：本變更若只保護 `Idempotency-Key` 進去、不保護 `Idempotency-Replayed` 出來，等於把一個承諾鎖了一半。同一個產生器多一個發射點，零額外機制。

- **重新產生 `api/openapi/v1-surface.txt`**：371 行 → **592 行**（+164 參數接受 +48 參數可省略 +9 回應標頭）。既有 371 行**逐字不動**，diff 全部是新增。

- **零行為變更**：`api/internal/` 的非測試程式碼**一行不動**，`api/openapi/openapi.yaml` 一行不動，資料庫與 migrations 不動。本變更只動一份測試檔、一份鎖檔、與文件。

- **既有四道檢查全部維持原狀**：檢查 1／2／3 完全不碰；檢查 4 只擴充它讀到的事實集合，既有兩種行的判定邏輯逐字不變。

## Capabilities

### New Capabilities

無。本變更不引入新能力——它修改的是 `api-contract` 既有的一條 requirement 的涵蓋範圍。

### Modified Capabilities

- `api-contract`: 「v1 表面受鎖檔保護」這條 requirement 目前只要求鎖檔記錄「每一條路由與每一個公開回應鍵」。修改為**同時**要求記錄每條端點接受的請求參數（名稱、位置、可否省略）與承諾的回應標頭，並明訂 additive 方向的判定（放寬必填 MUST NOT 失敗、收緊必填 MUST 失敗），以及**刻意不鎖**的粒度邊界（型別／格式／敘述）與其理由。

## Impact

**修改檔案**

- `api/internal/httpapi/openapi_contract_test.go` — 檢查 4 的事實產生器擴充（參數與回應標頭）；`$ref` 解析；檔頭的行形式說明
- `api/openapi/v1-surface.txt` — 重新產生（371 → 592 行，**只有新增**）
- `CLAUDE.md` — 表面鎖粒度的一句話更新（目前寫「四道檢查」時未言明鎖檔看不見參數）

**不變**

- `api/openapi/openapi.yaml`（**逐位元組不變**——本變更不是契約的變更，是鎖的變更）
- 任何 `api/internal/**` 的非測試程式碼、`go.mod`／`go.sum`
- `.github/workflows/ci.yml`（檢查 4 仍是 `internal/httpapi` 的普通 Go 測試，自動進 CI）
- 資料庫 schema、migrations、seed
- `docs/storefront-integration.md`（對消費端而言什麼都沒變）

**已盤點但不納入本變更的既有缺口**（實測數字，理由見 design D5）

| 未受保護的表面 | 規模 | 處置 |
| --- | --- | --- |
| 就地展開（非 `$ref`）的回應物件之鍵 | 35 個回應、60 個鍵 | **記為後續**；正確的修法可能是「回應 schema MUST 為具名元件」的紀律檢查，而不是把鎖檔的定址方式改成巢狀 |
| 就地展開的請求主體之鍵 | 42 個主體、106 個鍵 | **記為後續**（同上，且請求端的 additive 方向與回應端相反） |
| 每個端點宣告的狀態碼集合 | 477 組（op, status） | **不做**；D8 已論證端點層只列「呼叫端需要分辨」的狀態碼，鎖住它會與那條紀律打架 |
| 每個端點的 `security` 宣告（含 14 條 `security: []` 的公開端點） | 111 組 | **記為後續**；把公開端點改成需要認證是破壞性變更，但機械驗證授權宣告的觸發條件已由 `openapi-contract` design 記錄（「出現第一個第一方以外的後台消費端」） |
| `error.code` 的字面值集合 | 純敘述文字 | **不做**；`openapi-contract` design D8 已明確否決逐一列舉（無集中列舉、無法被檢查 3 證偽，列一份必然會漏的清單比不列更糟） |

**下游相依**

- 無。消費端（ONDO／`bouquet`）讀到的契約逐位元組不變。
- 未來任何**移除**請求參數或回應標頭的 change：MUST 手動刪掉鎖檔對應行——這是本變更留給後續變更的唯一新負擔，也是它的全部意義。
