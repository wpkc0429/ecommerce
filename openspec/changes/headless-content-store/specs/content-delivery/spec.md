## ADDED Requirements

### Requirement: 公開讀取只回已發佈內容

系統 SHALL 提供 `GET /api/v1/shop/content`（列出已發佈文件，`?include=content` 時帶內容）與 `GET /api/v1/shop/content/{key}`，以 `X-Publishable-Key` 識別商家。回應內容 MUST 經 hydrate（填預設值、丟棄 schema 未定義的鍵）。不存在的文件與未發佈的文件 MUST 回位元組相同的 404。已發佈內容在 schema 變更後即使不再通過驗證 MUST 仍照常提供。

#### Scenario: 草稿不外洩
- **WHEN** 文件只有草稿、尚未發佈
- **THEN** 公開讀取回 404，且與不存在的 key 的回應位元組相同

#### Scenario: 預設值補齊
- **WHEN** 已發佈內容缺少某個有 `default` 的欄位
- **THEN** 回應含該欄位的預設值

### Requirement: 快取標頭與條件請求

公開讀取回應 MUST 帶 `ETag` 與 `Cache-Control`（`public`、`max-age` 預設 60 秒）並 MUST 支援 `If-None-Match` 回 304；預覽回應 MUST 為 `Cache-Control: no-store`。伺服端快取 MUST 在發佈、下架與 schema 註冊時失效，且 Redis 故障時 MUST 退回資料庫而不中斷服務。

#### Scenario: 304
- **WHEN** 以相同 `ETag` 的 `If-None-Match` 重讀未變的文件
- **THEN** 回 304 且無主體

#### Scenario: 發佈後立即失效
- **WHEN** 發佈新內容後重讀
- **THEN** 伺服端回傳新內容（不受舊快取影響）

### Requirement: 預覽以短效 token 並經標頭傳遞

預覽端點 MUST 以 `X-Preview-Token` 標頭接受 token（MUST NOT 接受 query 參數），token MUST 綁定商家與範圍並可被商家限制到單一文件；使用時 MUST 重新檢查持有者仍具 `content.view` 且商家未停用。回應 MUST 帶 `no-store` 與 `X-Robots-Tag: noindex`。

#### Scenario: 跨店 token
- **WHEN** 以 A 店的 token 預覽 B 店的文件
- **THEN** 回 401 或 403

#### Scenario: 權限被撤銷
- **WHEN** token 持有者的 `content.view` 在簽發後被撤銷
- **THEN** 預覽回 403

### Requirement: 既有頁面與渲染端點行為不變

遷移期間 `/api/v1/admin/shops/{shopID}/pages*`、`/content`、`/page-types`、`/api/v1/render/page` 與 `/render/preview` MUST 維持既有的請求與回應形狀與語意（`slug` 對應 `key`），既有整合測試 MUST 不修改斷言即通過；寫入 MUST 同時反映到舊欄位（雙寫）直到清理階段。

#### Scenario: 舊端點回歸
- **WHEN** 以舊端點建立、發佈頁面
- **THEN** 以新公開端點可讀到相同內容，且舊 `render/page` 也回相同內容

#### Scenario: 全域內容即時生效
- **WHEN** 以舊 `PUT /content` 更新全域內容
- **THEN** 鍵 `global` 的文件草稿與已發佈內容同時更新
