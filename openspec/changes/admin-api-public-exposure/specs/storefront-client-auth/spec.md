## MODIFIED Requirements

### Requirement: 每商家瀏覽器 origin 登記

平台 SHALL 提供每商家的 allowed origin 登記，作為瀏覽器跨源存取（CORS）的授予依據。每筆登記 MUST 帶 `purpose`：`storefront`（商家自帶前端呼叫前台 API）或 `admin`（商家的後台前端呼叫後台 API）；未提供時 MUST 預設為 `storefront`，本變更前既有的登記 MUST 視為 `storefront`。非後台路徑的 CORS 允許集合 MUST 為「平台環境設定清單」與「全部 `purpose=storefront` 的商家 origin」的聯集；後台路徑的允許集合由 capability `admin-api-exposure` 定義。

登記值 MUST 正規化為 `scheme://host[:port]` 形式（scheme 與 host 小寫、無路徑、無尾斜線），scheme MUST 為 `http` 或 `https`。含路徑、含萬用字元或無法正規化的輸入 MUST 回 422。origin MUST 全域唯一（不分用途），重複登記 MUST 回 422。`purpose` 不屬於上述兩值 MUST 回 422。

登記與移除 MUST 即時生效，MUST NOT 需要變更環境變數或重新啟動 API。

平台自身前端（後台 SPA、平台代管的多租戶前端）的 origin 屬於平台環境設定清單，MUST NOT 登記於任一商家名下。

#### Scenario: 登記後跨源請求獲得授權

- **WHEN** 已登記 `https://ondo.example` 為 shop A 的 origin（未指定 `purpose`），瀏覽器自該 origin 對前台 API 發出請求
- **THEN** 回應帶 `Access-Control-Allow-Origin: https://ondo.example`

#### Scenario: 移除登記後不再授權

- **WHEN** 移除該 origin 的登記後，自同一 origin 再次發出請求
- **THEN** 回應 MUST NOT 帶 `Access-Control-Allow-Origin`

#### Scenario: 未登記的 origin 不獲授權

- **WHEN** 瀏覽器自未登記且不在平台清單中的 origin 發出請求
- **THEN** 回應 MUST NOT 帶 `Access-Control-Allow-Origin`

#### Scenario: 重複登記被拒

- **WHEN** 將已登記於 shop A 的 origin 登記給 shop B（不論 `purpose` 是否相同）
- **THEN** 回應 422，原登記不變

#### Scenario: 非法 origin 被拒

- **WHEN** 登記 `https://ondo.example/path`、`*.ondo.example` 或 `ftp://ondo.example`
- **THEN** 回應 422

#### Scenario: admin origin 不授予前台 CORS

- **WHEN** `https://admin.ondo.example` 登記為 shop A 的 `admin` origin，瀏覽器自該 origin 對 `/api/v1/shop/products` 發出請求
- **THEN** 回應 MUST NOT 帶 `Access-Control-Allow-Origin`

#### Scenario: 既有登記視為 storefront

- **WHEN** 讀取本變更前建立的 origin 登記
- **THEN** 其 `purpose` 為 `storefront`
