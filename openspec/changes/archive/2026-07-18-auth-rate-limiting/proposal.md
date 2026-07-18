## Why

`openspec/specs/authentication/spec.md` 定義的後台登入/refresh 與會員註冊/登入/refresh 端點目前完全沒有速率限制，任何人可對這些端點做無限次嘗試。Phase 1 design.md 的 Risks 已記錄「`members` 全平台唯一 email 有跨店帳號存在性洩漏面」，緩解措施寫的正是「速率限制」，但當時沒有對應 task，故從未實作——同時這也是密碼暴力破解、帳號列舉、refresh token 暴力猜測的共通防線。現在補上，避免技術債累積到後續 phase 才處理。

## What Changes

- 新增 Redis-backed 速率限制器（sliding window，鍵值依端點類型 IP-scope 或 IP+email 組合 scope），套用於：
  - 後台 `POST /api/v1/admin/auth/login`、`POST /api/v1/admin/auth/refresh`
  - 會員 `POST /api/v1/shop/auth/register`、`POST /api/v1/shop/auth/login`、`POST /api/v1/shop/auth/refresh`
- 超限回應 `429 Too Many Requests`，統一錯誤格式 `{"error":{"code":"rate_limited",...}}`，並帶 `Retry-After` header（秒數）。
- 限流鍵值正確納入 shop 範圍（會員端點）或維持全平台 IP 範圍（後台端點無 shop 上下文），確保某租戶的流量不會誤傷其他租戶。
- Redis 不可用時的降級行為（fail-open，記錄警告）——沿用專案既有「Redis 只做 cache/輔助功能，故障不擋路」慣例（design D9/D8 的 degrade 精神）。
- 不修改登入/註冊/refresh 本身的業務邏輯或回應格式（成功路徑不變）。

## Capabilities

### New Capabilities
（無新能力；速率限制是既有 authentication 能力的行為強化，不獨立成 capability）

### Modified Capabilities
- `authentication`：
  - 「Admin login and token issuance」新增速率限制條款與超限情境。
  - 「Member registration and login in shop context」新增速率限制條款與超限情境。
  - 「Refresh token rotation and revocation」新增 refresh 端點的速率限制條款與超限情境。

## Impact

- **新增程式碼**：`api/internal/ratelimit`（新 package，sliding window 限流器，Redis 實作）；`api/internal/httpapi` 新增速率限制 middleware 並掛載於受影響路由。
- **設定**：新增環境變數（限流門檻/視窗長度，附合理預設值），`api/internal/config` 擴充。
- **依賴**：不新增外部服務或第三方套件，沿用既有 `go-redis/v9`。
- **測試**：`api/internal/ratelimit` 單元測試（無外部依賴，用 miniredis 或既有 INTEGRATION=1 real Redis 皆可）；`api/internal/httpapi` 整合測試覆蓋 429 情境、視窗過期恢復、shop/IP 互不干擾。
- **不影響**：`web`、`admin`、其他 capability（rbac、multi-tenancy、theme-system、page-management、content-rendering）。
