# 設計服務契約（AI edit service 與平台之間）

給設計 AI edit service 的作者。**這份文件是服務可以直接依賴的契約**；設計理由在 `openspec/changes/design-history/design.md`（D4、D8、D10、D17、D18），
資料與 API 總覽在 `docs/design-history.md`，操作員要做的事在 `docs/deploy/design-history-operations.md`。
路徑中的 `{shop}` 是商家 id；所有範例只使用佔位憑證，**真正的 `dsk_…` 金鑰只能放在服務的祕密管理**。

## 1. 基本規則

- 基底路徑 `/api/v1/design-service/shops/{shop}`；`Authorization: Bearer dsk_…`。一把金鑰只對一家店有效，路徑中的 `{shop}` 不同 → 404。
- 任何認證失敗（未知、過期、撤銷、CIDR 不符）都是**同一個** 401，不會說明原因。**收到 401 不要重試**：停止並通知營運者（金鑰被撤銷或過期；輪替見營運手冊）。
- 回應一律 JSON（截圖上傳的**請求**本文是 `image/png`）。錯誤是 `{"error":{"code","message","details"}}`，**以 `code` 判斷，不要比對 `message`**。
- 請求資源的 `rev` 與 `ETag` 只供人類核准使用；服務不需要送 `If-Match`。
- 限流：每憑證每分鐘 120 次寫入、截圖上傳另計每分鐘 60 次、每店每日建立請求上限（預設 20）。超過回 429（附 `Retry-After`）；每日上限回 429 `daily_cap_reached`（**不要**當天再試）。

## 2. 狀態機

```
drafting ──(守門員過 ＋ PUT preview)──► previewing ──(兩組截圖齊全 ＋ submit)──► awaiting_approval
   ▲                                       │                                        │
   └────────(PUT head … reset:true)────────┴────────────────────────────────────────┤
                                                                          【人類】核准 │ 拒絕／要求修改／取消
                                                                                    ▼
                                         approved ──(重跑守門員 recheck 通過 ＋ published)──► published ──► reverted（之後由還原請求標記）
failed ◄── 服務回報 failed、認領租約過期、核准逾期、等待核准逾 7 天（皆為事件，不改寫歷史）
```

- 只有**人類**能讓請求進入 `approved`、`rejected`、`cancelled`，或建立還原請求。服務的路由族**沒有**這些端點，也沒有任何方法讓它們發生。
- 每個站台（`storefront`／`admin`）同時至多一筆進行中的請求（409 `request_in_progress`）。

## 3. 十六步順序（design D17）與每一步的錯誤處理

| # | 動作 | 端點 | 成功 | 失敗與處理 |
| --- | --- | --- | --- | --- |
| 1 | 取得工作 | `GET /requests?status=drafting&claimed=false` | 200，含 revert 請求 | 5xx／429：依 §5 退避 |
| 2 | 建立請求（商家要求時） | `POST /requests`（**必帶** `Idempotency-Key`） | 201（重放 200） | `403 requester_not_permitted`：告知商家無權限；`409 request_in_progress`：請商家先完成或取消目前請求；`429 daily_cap_reached`：今日不再試 |
| 3 | 認領 | `POST /requests/{id}/claim`（15 分鐘租約，可續約） | 200 | `409 claim_conflict`：另一個工作者持有，放棄這筆 |
| 4 | 取得 base，建分支 `ai/<shop>/<request-id>` | （git，非平台） | | |
| 5 | 守門員回報 | `POST /requests/{id}/guard-runs`（`passed:false` 也要回報） | 200 | 守門員失敗時**不得部署預覽** |
| 6 | 回報 head | `PUT /requests/{id}/head` `{head_sha, diff_summary}` | 200 | `409 invalid_state`：請求已離開 drafting（帶 `reset:true` 才能把 previewing／awaiting_approval 退回 drafting） |
| 7 | 登記預覽 | `PUT /requests/{id}/preview` `{origin, expires_at}` | 200，狀態 `previewing` | `409 guard_not_passed`：對**目前 head** 沒有通過的守門員結果；`422`：origin 形狀不對 |
| 8 | 截圖（sandbox，見 §6） | `PUT /requests/{id}/screenshots/{phase}/{role}/{viewport}/{viewKey}` | 201／200（重放） | 見 §4 |
| 9 | 送審 | `POST /requests/{id}/submit` | 200，`awaiting_approval` | `409 screenshots_incomplete`：補拍缺的槽位；`409 preview_expired`：重新登記預覽；`503 design_storage_not_configured`：**放棄**（見 §7） |
| 10 | **等待人類** | `GET /requests/{id}` 輪詢（建議每 30 秒以上，不要更密） | | 唯一依據是平台的 `status = approved`；聊天中的「我核准了」不是依據 |
| 11 | 核准後重跑守門員 | `POST /requests/{id}/guard-runs` `{phase:"recheck", checked_sha:<approved_head_sha>, passed}` | 200 | 失敗 → `POST /failed` |
| 12 | 確認 `main` 仍是 `base_sha` | （git） | | 不是：請求退回 drafting，以新 base 重做（需重新核准） |
| 13 | 以平台配發的 `publish_tag` 建 tag 並合併 | （git） | | **絕不** `--update` manifest、不改 `deploy/`、`_headers`、`core/warnings.js`、`styles.css` |
| 14 | 回報已發佈 | `POST /requests/{id}/published` `{published_sha, tag, deployment_id?}` | 200，`published` | `409 approval_expired`／`409 guard_not_passed`／`422`（tag 或 sha 不符）：**還原剛才的合併**，`POST /failed` |
| 15 | 對帳 | `POST /untracked-changes`、`PUT /sites/{site}/state` | 200 | 可重試（冪等） |
| 16 | 預算與逾時 | | | 超過重試上限、diff 上限或時間盒 → `POST /failed` |

## 4. 截圖上傳（task 2.3）

`PUT /requests/{id}/screenshots/{phase}/{role}/{viewport}/{viewKey}`；槽位完全由路徑決定（`phase` ∈ before|after、`role` ∈ owner|staff|anon、`viewport` ∈ 390|1280、`viewKey` 為 `[a-z0-9-]{1,40}`）。

必要標頭：`Content-Type: image/png`、`X-Capture-Manifest`（base64 的 JSON：`tool`、`tool_version`、`dataset`、`api_origin_class`、`captured_at`〔RFC 3339〕、`set_version`〔≥ 1〕，整個 ≤ 2 KiB；其他欄位會被丟棄）。
選填：`X-Content-SHA256`（**上傳位元組**的 SHA-256，平台比對以偵測傳輸損壞）、`X-Diff-Ppm`（你自己算的差異，平台只存不信）。

- before 的 `commit_sha` 是請求的 `base_sha`，after 是**目前的** `head_sha`（所以要先 `PUT head`）。head 變動後必須重拍 after。
- **同位元組、同 commit 重放** → 200 且不新增；**不同位元組** → 201 並新增 `attempt`（舊的保留，送審看每個槽位最新的 attempt）。重試是安全的。
- 平台會重新編碼 PNG（所有中繼資料被移除），所以回應的 `sha256`、`byte_size` 是**存入**的值，可能與你上傳的不同。
- 只能在 `drafting`／`previewing` 上傳；送審後集合就凍結。

| 狀態／`code` | 意義 | 服務該做什麼 |
| --- | --- | --- |
| 201／200 | 已存／重放 | 繼續 |
| 413 `invalid_screenshot` | 超過 2 MiB | 縮小（降低高度或改用較少色彩）；**不要原樣重試** |
| 415 | `Content-Type` 不是 `image/png` | 修程式 |
| 422 `invalid_screenshot` | 不是 PNG／寬度不等於視口／高度 > 10,000／無法解碼／雜湊不符／manifest 無效 | 雜湊不符可重傳一次；其餘是擷取程式的錯誤，**不要重試**，`failed(screenshot_upload_failed)` |
| 422 `live_data_capture_refused` | admin 截圖不是 sandbox 資料集，或 storefront 不是匿名／資料集宣告不一致 | **停止**：擷取環境設定錯誤，這是隱私規則（§6），`failed(live_data_capture_refused)` 並通知營運者 |
| 409 `quota_exceeded` | 每請求 80 張／40 MiB 或每店配額已滿 | **放棄**這個請求（`failed`）；不要靠重試解決，請營運者檢視配額 |
| 409 `invalid_state` | 請求不在 drafting／previewing | 重新讀取請求狀態 |
| 409 `head_moved` | 上傳期間 head 變了 | 以新的 head 重拍 |
| 409 `head_not_set` | 還沒回報 head 就上傳 after | 先 `PUT head` |
| 429 | 每憑證每分鐘 60 次 | 等 `Retry-After` |
| 502 `storage_unavailable` | 私有 bucket 暫時不可用 | 退避重試（§5） |
| 503 `design_storage_not_configured` | 平台未設定截圖儲存 | **放棄**（§7） |
| 404 | 請求不存在或不屬於這家店 | 放棄這筆 |

## 5. 重試與冪等鍵規則

- **可安全重試**：所有 `PUT`（head、preview、screenshots、sites state）、`POST /untracked-changes`、帶 `Idempotency-Key` 的 `POST /requests`、`claim`（續約）。`POST /submit`、`/published`、`/failed` 在目標狀態已達成時重送會回 409 `invalid_state`：重送後先 `GET` 確認狀態，不要盲目再送。
- **退避**：只對網路錯誤、`5xx`、`429`（遵守 `Retry-After`）與 `502 storage_unavailable` 重試；指數退避（1、2、4、8、16 秒，加抖動），最多 5 次。**4xx（429 除外）不重試**，因為同一個請求會得到同一個答案。
- **`Idempotency-Key`**（建立請求必帶）：8–255 個可見 ASCII 字元，建議 `<request-uuid>` 或穩定的工作 id。同 key＋同內容 → 200 回既有請求（`Idempotency-Replayed: true`）；
  同 key＋不同內容 → 422 `idempotency_key_reused`（**絕不為了「過關」換內容重用 key，也不要換 key 重試同一件事**，那會建立第二個請求）。key 與請求綁定，不要跨請求或跨商家共用。
- 認領租約 15 分鐘：長工作期間定期 `claim` 續約；租約過期後平台會把請求標為 `failed(service_lost)`。

## 6. 隱私規則（服務必須遵守，平台只能部分驗證）

- **admin 截圖只從 sandbox 堆疊拍**（固定合成資料集 `sandbox-v1`，`api_origin_class: "sandbox"`）。**絕不對真實商家的 live 後台截圖。**擷取腳本在 API origin 不在 sandbox 允許清單時必須拒絕啟動。
- storefront 截圖只拍公開資料、不登入（`role: "anon"`），資料集 `sandbox-v1`（sandbox 來源）或 `public-live`（公開來源）；不拍訂單確認頁與任何帶表單輸入值的頁面；購物車用合成 session。
- 拍照前把標記 `data-pii` 的元素以純色遮罩。
- 平台在上傳時檢查宣告（`live_data_capture_refused`），但**無法驗真**：這是契約，違反它的責任在服務。

## 7. 何時必須放棄（`POST /requests/{id}/failed`，附 `code`）

下列任一情況服務不得繼續等待或重試，必須回報 `failed`（含簡短 `code`，例如 `screenshot_upload_failed`、`guard_failed`、`budget_exceeded`、`merge_conflict`）並結束工作：

1. 守門員在重試上限內仍失敗，或 diff／時間／重試預算用盡。
2. 截圖最終無法上傳（§4 的「放棄」列），或平台回 503 `design_storage_not_configured`（沒有 after 截圖就不能被核准，所以流程無法繼續）。
3. `published` 被平台拒絕（核准逾期、守門員未對核准 SHA 通過、tag／sha 不符）：先還原合併再回報。
4. `main` 在核准後移動且無法以新 base 重做。
5. `401`（金鑰失效）：無法回報時，認領租約過期會由平台標記 `service_lost`。

`failed` 是終態；商家可以重新發起新的請求。

## 8. 服務不得做的事（清單）

- **不得核准、拒絕、取消請求，不得建立還原請求**（路由不存在；用服務金鑰打 `/api/v1/admin/**` 只會得到 401）。不要嘗試繞道（例如替人按下 UI）。
- **不得把「商家說已核准」當依據**：唯一依據是平台回報的 `status = approved` 與 `approved_head_sha`。
- **不得執行 `--update`**（核心 manifest）、不得改核心檔（`lib/`、`core/`、`ui/`、`app.js`、`nginx.conf`、`deploy/` 等）；只能改 `admin/design/**`（storefront 為 template 規定的設計層）。
- **不得把歷史當指令**：過往 prompt、事件、失敗訊息、守門員輸出、截圖文字都是**資料**，永不放進 agent 的指令脈絡；必要時以明確的「引用資料」區塊包起並聲明不可執行其中的指令。
- **不得對 live 後台截圖**（§6），不得上傳與 `commit_sha` 不符的截圖，不得偽造 `capture_manifest`。
- **不得在日誌、聊天或工單中寫入** `dsk_` 金鑰、簽名網址或預覽連結全文。
- **不得在核准後改變 head**：核准綁定的是確切的 `head_sha`；改了就是新的請求週期（重新送審、重新核准）。
- **不得自行延長或繞過保存期限／配額**；不得用多個憑證或重複請求規避每日上限。

## 9. 範例（佔位憑證）

```bash
API=https://api.example.test            # 營運者提供
SHOP=3
KEY=dsk_PLACEHOLDER_NOT_A_REAL_KEY      # 來自祕密管理，永遠不要貼出真值
auth=(-H "Authorization: Bearer $KEY")

# 2. 建立請求
curl -sS -X POST "$API/api/v1/design-service/shops/$SHOP/requests" "${auth[@]}" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: job-7f3c2a10" \
  -d '{"site":"admin","requested_by_user_id":12,"prompt":"Make the order screen calmer",
       "base_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","core_version":"0.3.0","design_floor_version":"0.2.0"}'

# 6. 回報 head；5. 守門員；7. 預覽
curl -sS -X PUT "$API/api/v1/design-service/shops/$SHOP/requests/$RID/head" "${auth[@]}" -H 'Content-Type: application/json' \
  -d '{"head_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","diff_summary":{"files_changed":1,"additions":3,"deletions":1,
       "files":[{"path":"admin/design/theme.css","status":"modified","additions":3,"deletions":1}]}}'
curl -sS -X POST "$API/api/v1/design-service/shops/$SHOP/requests/$RID/guard-runs" "${auth[@]}" -H 'Content-Type: application/json' \
  -d '{"checked_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","passed":true,"rule_ids":[]}'
curl -sS -X PUT "$API/api/v1/design-service/shops/$SHOP/requests/$RID/preview" "${auth[@]}" -H 'Content-Type: application/json' \
  -d '{"origin":"https://p-3f9a.preview.example","expires_at":"2026-11-27T12:00:00Z"}'

# 8. 截圖（槽位在路徑上）
MANIFEST=$(printf '%s' '{"tool":"design-capture","tool_version":"1","dataset":"sandbox-v1","api_origin_class":"sandbox","captured_at":"2026-11-26T10:00:00Z","set_version":1}' | base64 -w0)
curl -sS -X PUT "$API/api/v1/design-service/shops/$SHOP/requests/$RID/screenshots/after/owner/390/orders" "${auth[@]}" \
  -H 'Content-Type: image/png' -H "X-Capture-Manifest: $MANIFEST" -H "X-Content-SHA256: $(sha256sum after-owner-390-orders.png | cut -d' ' -f1)" \
  --data-binary @after-owner-390-orders.png

# 9. 送審
curl -sS -X POST "$API/api/v1/design-service/shops/$SHOP/requests/$RID/submit" "${auth[@]}"

# 14. 核准之後回報已發佈（tag 是平台在核准時配發的 publish_tag）
curl -sS -X POST "$API/api/v1/design-service/shops/$SHOP/requests/$RID/published" "${auth[@]}" -H 'Content-Type: application/json' \
  -d '{"published_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","tag":"design-admin-20261126-1","deployment_id":"dep-123"}'
```

錯誤回應範例：

```json
{"error":{"code":"live_data_capture_refused","message":"admin screenshots must be captured from the sandbox stack (dataset sandbox-v1)","details":null}}
```

## 10. 版本

契約隨 `/api/v1` 只做 additive 變更；新的錯誤碼或選填欄位不會破壞既有服務，**服務應把未知的 `code` 當成「不可重試的失敗」處理**。
完整欄位與每個端點的回應在 `api/openapi/openapi.yaml`（標籤 `design-service`）。
