# admin-catalog-ui Specification

## Purpose
TBD - created by archiving change admin-commerce-catalog. Update Purpose after archive.
## Requirements
### Requirement: 後台提供商品目錄的操作介面

平台的後台介面 SHALL 讓商家在不接觸 HTTP 客戶端或 JSON 的前提下，完成商品分類與商品（含其 SKU）的日常管理：分類的建立、修改、刪除；商品的列表、建立、修改、刪除；SKU 的新增、修改、移除；以及商品的草稿／發佈切換。

介面 MUST 只透過既有的公開 API（`/api/v1/admin/shops/{shopID}/...`）取得與寫入資料，MUST NOT 直接存取資料庫，也 MUST NOT 依賴任何未載於 `api/openapi/openapi.yaml` 的端點或欄位。

#### Scenario: 商家不需要 HTTP 客戶端即可建立商品

- **WHEN** 具備 `product.create` 的使用者在後台填寫商品標題、slug 與至少一個 SKU 後送出
- **THEN** 商品連同其 SKU 一併建立成功，且該使用者全程未撰寫任何 JSON 或 HTTP 請求

#### Scenario: 分類刪除被伺服器拒絕時原因對商家可見

- **WHEN** 使用者刪除一個仍掛有商品或仍有子分類的分類，伺服器回 409
- **THEN** 介面 MUST 顯示伺服器回傳的原因訊息，MUST NOT 只顯示通用失敗字樣或靜默無反應

### Requirement: 巢狀 SKU 於商品編輯器內直接增修刪

商品編輯器 SHALL 在同一畫面內編輯該商品的全部 SKU：每個 SKU 的代碼、選項（鍵值對）、價格、幣別、庫存數量與啟用旗標 MUST 可直接修改，且 MUST 可新增一列 SKU 與移除一列 SKU。

儲存時介面 MUST 送出該商品**當前完整的** SKU 集合，既有 SKU MUST 帶回其識別碼。介面 MUST NOT 提供任何只送出部分 SKU 集合的儲存路徑——伺服器的巢狀 upsert 語意會刪除未出現在酬載中的既有 SKU，部分送出將造成靜默資料遺失。

#### Scenario: 修改單一 SKU 的庫存不影響同商品的其他 SKU

- **WHEN** 一個含兩個 SKU 的商品，使用者只改動其中一個 SKU 的庫存並儲存
- **THEN** 該 SKU 的庫存更新為新值，**另一個 SKU 仍存在**且其代碼、價格、庫存皆未變動

#### Scenario: 移除一列 SKU 後儲存即刪除該 SKU

- **WHEN** 使用者在編輯器移除一列既有 SKU 後儲存
- **THEN** 該 SKU 自商品消失，其餘 SKU 不受影響

#### Scenario: 新增的 SKU 不帶識別碼

- **WHEN** 使用者新增一列 SKU 並儲存
- **THEN** 該列以無識別碼的形式送出，伺服器建立為新 SKU，既有 SKU 全數保留

### Requirement: 金額在顯示與輸入的任一路徑都不得被縮放

介面對任何金額的顯示與輸入 MUST 直接使用 API 的整數值，MUST NOT 乘以或除以任何倍率。金額的單位由同層的 `currency` 決定（見 spec product-catalog 的最小貨幣單位定義）；在 TWD 之下，整數 `3400` MUST 顯示為 NT$3,400。

金額輸入欄位 MUST 只接受整數，且送出的值 MUST 與使用者輸入的整數逐位元組相同。

#### Scenario: TWD 金額原值顯示

- **WHEN** 某 SKU 的 `price_amount` 為 `3400`、`currency` 為 `TWD`
- **THEN** 介面顯示的金額 MUST 表示 NT$3,400，MUST NOT 顯示為 34、340000 或任何其他被縮放的數值

#### Scenario: 金額輸入原值送出

- **WHEN** 使用者在價格欄輸入 `4400` 並儲存
- **THEN** 送出的 `price_amount` 為 `4400`，重新載入後該欄仍顯示 `4400`

### Requirement: 介面依呼叫者的實際權限決定可見與可操作

介面 SHALL 依呼叫者在**當前所選商家**的有效權限集合，決定分頁、按鈕與表單的可見性與可用性：缺少檢視權限的資源分頁 MUST NOT 出現；缺少建立／修改／刪除權限時，對應的操作入口 MUST NOT 出現或 MUST 為不可用狀態。

此機制為體驗層。伺服器端的授權判定（spec rbac）MUST 仍為唯一權威，介面的權限判斷 MUST NOT 被視為安全邊界。切換商家時介面 MUST 重新取得該商家的權限集合。

#### Scenario: 無檢視權限則分頁不出現

- **WHEN** 使用者在該商家不具 `product.view`
- **THEN** 商品分頁 MUST NOT 出現在導覽中

#### Scenario: 無刪除權限則不顯示刪除入口

- **WHEN** 使用者具 `product.view`／`product.edit` 但不具 `product.delete`
- **THEN** 商品列表與編輯器 MUST NOT 出現刪除入口，但檢視與編輯功能正常

#### Scenario: 切換商家後權限重新判定

- **WHEN** 使用者於 shop A 具 `product.delete`、於 shop B 不具，並在介面上由 shop A 切換到 shop B
- **THEN** 刪除入口在切換後 MUST 消失，MUST NOT 沿用 shop A 的判定

### Requirement: 商品列表支援分頁、關鍵字搜尋與篩選，且搜尋結果涵蓋整個目錄

商品列表介面 SHALL 支援分頁、依分類篩選、依草稿／發佈狀態篩選，以及關鍵字搜尋。

關鍵字搜尋 MUST 由伺服器在**整個商家目錄**上執行，MUST NOT 只對當前頁已載入的資料進行過濾——只過濾當頁會對不在當頁的既有商品回報「找不到」，導致商家重複建立商品。

#### Scenario: 搜尋命中不在當前頁的商品

- **WHEN** 商家有超過一頁的商品，使用者搜尋一個只存在於後面頁次的商品標題關鍵字
- **THEN** 該商品 MUST 出現在搜尋結果中

#### Scenario: 篩選條件可組合

- **WHEN** 使用者同時指定分類與狀態篩選
- **THEN** 結果 MUST 同時滿足兩個條件

### Requirement: 後台前端維持無建置步驟的靜態交付形式

後台前端 SHALL 以瀏覽器可直接載入的靜態資源交付：MUST NOT 引入打包器、轉譯器或任何需要在部署前執行的建置步驟；模組化 MUST 以瀏覽器原生的 ES module `import`／`export` 達成。

後台的 API 位址 MUST 維持為執行期設定，MUST NOT 在建置或映像檔製作階段被固定，以保持同一份靜態資源可服務於任何環境。

#### Scenario: 以靜態伺服器即可完整執行

- **WHEN** 以任何靜態檔案伺服器服務後台目錄
- **THEN** 後台 MUST 完整運作，MUST NOT 需要任何先行的編譯、打包或安裝步驟

#### Scenario: 新增模組檔案不需要調整部署設定

- **WHEN** 後台新增一個 ES module 檔案
- **THEN** 既有的映像檔製作流程 MUST 自動涵蓋它，MUST NOT 需要逐檔登記

### Requirement: 商品目錄的關鍵操作路徑由瀏覽器 E2E 覆蓋

商品目錄後台的關鍵路徑 SHALL 由真實瀏覽器的端對端測試覆蓋，且該測試 MUST 屬於既有的單一 E2E 入口，MUST NOT 是一個需要另外被告知才會執行的獨立入口。

覆蓋範圍 MUST 至少包含：建立分類、建立含多個 SKU 的商品、修改其中一個 SKU 的庫存、發佈商品、並於公開商品端點確認結果，以及**一條專門斷言金額顯示未被縮放的檢查**。

#### Scenario: 端對端鏈路可驗證

- **WHEN** 執行既有的 E2E 入口
- **THEN** 上述路徑 MUST 全數被實際執行並斷言，任一環節失敗 MUST 使整體結果為失敗

#### Scenario: 金額縮放錯誤會使測試失敗

- **WHEN** 介面在任一處把金額除以或乘以 100
- **THEN** E2E 的金額斷言 MUST 轉為失敗

