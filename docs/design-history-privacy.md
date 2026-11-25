# 設計歷史的隱私規則（R1–R5）與它們在程式中的落點

來源：`openspec/changes/design-history/design.md` D11；決定 Q5（admin 截圖只用 sandbox，storefront 允許 live 公開資料）。
**平台無法證明一張截圖真的來自哪裡**：下表的每一條都是「契約 ＋ 縱深防禦」，不是證明。

| 規則 | 內容 | 程式落點 | 證明（測試） |
| --- | --- | --- | --- |
| **R1** | admin 截圖只從 sandbox 堆疊擷取，絕不對真實商家的 live 後台截圖 | 上傳時 `design.checkCaptureRules`（`api/internal/design/screenshots.go`）：admin 必須 `capture_manifest.dataset = sandbox-v1` 且 `api_origin_class = sandbox`，否則 422 `live_data_capture_refused`，物件與資料列都不寫入；擷取腳本拒絕在非 sandbox origin 啟動（template 任務 T6，**尚未做**） | `TestScreenshotSandboxOnlyAdminRule`、`TestCaptureRulesAdminSandboxOnly`、e2e `design-history-path.mjs` |
| **R2** | storefront 只從 live **公開**資料擷取，不登入、不顯示顧客資料；排除訂單確認頁與帶表單輸入值的頁面 | 同一函式：storefront 只收 `role = anon`；資料集必須是 `sandbox-v1`＋sandbox 來源，或 `public-live`＋public 來源（兩者混搭一律拒絕）。**視圖集（排除哪些頁面）由擷取腳本宣告，平台只記 `set_version`，無法驗證頁面內容** | 同上 |
| **R3** | 縱深：核心對會顯示個資的元素加 `data-pii`，擷取工具拍照前以純色遮罩 | **不在這個 repo**：template 任務 T6／T7（`scripts/design-capture.mjs`、核心的 `data-pii`）。平台這側沒有任何程式能偵測影像內的個資 | 無（template 的 e2e） |
| **R4** | prompt 入庫時在伺服器端掃描並遮蔽 email、台灣手機、疑似卡號（Luhn 13–19 碼）；剔除控制與雙向覆寫字元 | `design.RedactPII`／`SanitizeText`（`api/internal/design/redact.go`），由 `CreateRequest` 在寫入前呼叫；`prompt_sha256` 對遮蔽後文字計算、永久保留；保存期後 prompt 全文遮蔽為 NULL（`cmd/purge-design-history`，365 天） | `TestRedactPII`、`TestSanitizeTextDropsControlAndBidiCharacters`；整合測試 `TestDesignLogsCarryNoSensitiveValues` |
| **R5** | 預覽、簽名網址不入紀錄；日誌只含 id、計數、規則 id | 簽名網址只在 `Service.ListScreenshots` 產生、只寫進回應（`Cache-Control: no-store`），不存任何資料表；回應與日誌不含物件鍵（`screenshotJSON` 不輸出 `object_key`）；上傳失敗的日誌只有 `shop_id` 與 `err_class`；事件 `detail` 不放 prompt、URL、stderr（`design_request_events` 的 CHECK 與 `detail` 內容由 `step()` 呼叫端決定） | `TestScreenshotSignedReads`（日誌 buffer 中找不到物件鍵與簽名網址）、`TestDesignLogsCarryNoSensitiveValues`（prompt、preview origin、憑證金鑰） |

## 其他與隱私有關的邊界

- **私有 bucket**：沒有自訂網域、沒有 r2.dev、專用 token（`DESIGN_R2_*`，production 拒絕與圖片 token 相同或已知預設祕密）。讀取只經 5 分鐘簽名網址。匿名讀取驗證見 `docs/deploy/design-history-operations.md` 第 6 節。
- **重新編碼去除中繼資料**：上傳的 PNG 先完整解碼再由 Go 的編碼器重新編碼（不寫任何輔助區塊），EXIF／tEXt／iCCP 一律消失；`sha256` 對存入位元組計算。
- **保存期限（Q1）**：已發佈請求的截圖 365 天、其餘 30 天；到期物件由 `purge-design-history` 刪除（資料列保留 `purged_at`）。
- **個資請求／商家下架**：`purge-design-history -export-shop N` 匯出（只有中繼資料與物件鍵，不含簽名網址），`-all-for-shop N -apply` 刪除資料列與物件。
- **服務看不到歷史**：機器路由的唯讀詳情不回 prompt 全文，也沒有截圖讀取路由；歷史是資料不是指令，永不餵入 agent 的指令脈絡（`docs/design-service-contract.md`）。
- **殘留風險**：服務可能上傳假的 `dataset` 宣告（無法驗真）；影像內容未經個資偵測。緩解：sandbox 是唯一接受 admin 截圖的環境、核准畫面並排即時預覽、R3 的遮罩。
