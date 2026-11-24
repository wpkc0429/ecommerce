## ADDED Requirements

### Requirement: 發佈 hook 預設關閉且 URL 為祕密

每店的發佈 hook MUST 預設關閉；其 URL MUST 加密儲存，管理端 API MUST NOT 回傳完整 URL 或簽章 secret（只回主機、遮罩與狀態），應用程式日誌 MUST NOT 含 URL。

#### Scenario: 回應不含 URL
- **WHEN** 設定 hook 後讀取其狀態
- **THEN** 回應位元組不含完整 URL 與 secret

### Requirement: 出站請求受 allowlist 與 SSRF 防護

hook 發送 MUST 只允許 `https`、無 userinfo、非 IP 字面值、長度不超過 512 的 URL，且主機 MUST 在平台層 allowlist 內。系統 MUST 在發送時自行解析 DNS，拒絕私有、loopback、link-local（含 `169.254.169.254`）、CGNAT、multicast 與保留位址（含 IPv4-mapped IPv6），且 MUST 只連線到剛驗證過的 IP。系統 MUST NOT 跟隨重導向，MUST 以 5 秒整體逾時，MUST 只讀回應前 4 KiB 且 MUST NOT 把回應內容回傳給呼叫者或寫入日誌。

#### Scenario: 內網位址
- **WHEN** allowlist 內的主機解析到 `10.0.0.5`
- **THEN** 發送被拒，狀態記為 `blocked`

#### Scenario: DNS rebinding
- **WHEN** 驗證時解析到公網 IP、連線前再次解析會得到私有 IP
- **THEN** 連線只使用先前驗證過的 IP

#### Scenario: 重導向
- **WHEN** 目標回 302 指向內網位址
- **THEN** 不跟隨，視為失敗

### Requirement: 發佈通知簽章、去抖動與限流

hook 請求主體 MUST NOT 含內容，MUST 帶 `X-Content-Timestamp` 與 `X-Content-Signature`（HMAC-SHA256）。同一商家 30 秒內的多次發佈 MUST 合併為一次呼叫；每商家 MUST 有最短間隔與每日上限；手動觸發與測試 MUST 另有限流。發送 MUST 在背景有界佇列中進行，MUST NOT 拖慢發佈請求；連續失敗達上限 MUST 自動停用並在後台顯示。

#### Scenario: 合併
- **WHEN** 同一商家在 30 秒內發佈 10 份文件
- **THEN** hook 只被呼叫一次

#### Scenario: 不拖慢發佈
- **WHEN** hook 目標回應緩慢
- **THEN** 發佈請求仍快速回應

#### Scenario: 每日上限
- **WHEN** 當日觸發次數已達上限
- **THEN** 後續發佈不再呼叫 hook，並記錄為被限流
