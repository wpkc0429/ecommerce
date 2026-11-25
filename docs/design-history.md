# 設計歷史（design history）

> change `design-history` 的**第一個切片**（資料與 API，無截圖、無簽章 permit）。權威來源是
> `openspec/changes/design-history/`（proposal、design 的 D1–D24 與決議 Q1–Q10）；本文件是實作後的操作與契約摘要。
> 服務端的步驟契約（給 AI edit service）見 `docs/design-service-contract.md`；隱私規則 R1–R5 的程式落點見 `docs/design-history-privacy.md`。

## 它是什麼

每一次經 AI edit service 的重新設計是一筆 **設計請求**，由平台（不是服務）保存：誰說了什麼、改了什麼檔（路徑與行數，無內容）、
守門員結果、預覽的 origin 與到期、誰在何時核准、發佈的 tag。紀錄**只能追加**，核准**綁定被檢查與預覽的那個 `head_sha`**。

**真實性分三級**（D1）：平台產生的（id、時間、核准者與核准的 SHA、狀態、tag）＝權威；服務回報的（prompt、head、diff 摘要、
守門員結果、預覽）＝「服務如此聲稱」；人按下的核准＝以第二級為依據的人類決定。畫面必須標示哪些欄位是服務聲稱的。

## 資料模型（六張表）

| 表 | 租戶 | 說明 |
| --- | --- | --- |
| `design_requests` | `tenantOwned` | 請求；`status` 是事件的投影，只由狀態機以條件式 UPDATE 改變 |
| `design_request_events` | `tenantOwned` | 只能追加、每請求內以 SHA-256 串接 |
| `design_screenshots` | `tenantOwned` | 切片 1 只建表與 trigger；上傳在切片 2 |
| `design_site_state` | `tenantOwned` | 各站台目前的設計（被記錄的發佈才會改 `current_*`） |
| `design_untracked_changes` | `tenantOwned` | 對帳回報的、在服務之外進入 main 的提交 |
| `design_service_credentials` | `notTenantOwned` | 認證時先以雜湊查找、之後才知道 shop |

與 design.md 的差異（皆為實作時的決定）：`prompt_flags`／`allowed_cidrs` 存 `jsonb`（ent 的 `[]string` 預設）而非 `text[]`；
請求多了 `change_note`（owner 的「要求修改」說明）、`claimed_by_credential_id`、`idempotency_fingerprint`（同鍵不同內容的判斷）；
事件種類多一個 `changes_requested`；`head_sha` 在建立時等於 `base_sha`；`branch` 省略時由平台給 `ai/shop-{id}/{request id}`，
服務也可以自己帶 `request_id`（uuid）讓它能先決定分支名。

### 只能追加（D3）：誠實地說它防什麼

migration `…_add_design_history_append_only_triggers`（repo 的**第一組 trigger**）：事件表禁止 UPDATE／DELETE；截圖表只允許改 `purged_at`；
未追蹤變更只允許設定一次確認欄；請求列的不可變欄位（`id`、`shop_id`、`kind`、`site`、`requested_by_user_id`、`prompt_sha256`、
`base_sha`、`created_at`、`restores_request_id`）禁止 UPDATE，列只有清除指令能刪。放行條件是交易內
`SELECT set_config('design.retention','on',true)`（只有 `cmd/purge-design-history` 與測試會這麼做）。
**防的是應用程式 bug 與被攻破的應用路徑，不是持有 DDL 權限的人**（能 `DISABLE TRIGGER` 並重算整條鏈）；要防後者需把鏈頭雜湊定期寫到外部（後續）。
事件雜湊串讓「事後插入、刪除、改寫中間一筆」可被偵測：`go run ./cmd/design-sweep -verify`（`make design-sweep` 是 dry run）。

## 狀態機（D4，`internal/design/machine.go` 是唯一定義處）

```
drafting ─(守門員過＋預覽登記)→ previewing ─(截圖齊全＋submit)→ awaiting_approval ─(人：approve)→ approved ─(重檢過＋published)→ published
awaiting_approval ─(人：reject)→ rejected      awaiting_approval ─(人：request-changes／服務 reset)→ drafting
非終態 ─(服務／sweep)→ failed      非終態 ─(人，design.request)→ cancelled      published ─(之後某筆還原發佈)→ reverted
```

轉移表同時記錄**誰**能走：`approved` 只有 `user` 能到達（單元測試對九個狀態的全矩陣斷言，並斷言 service／system 到不了 `approved`）。
每個轉移＝同一交易內「條件式 UPDATE（`WHERE status IN (…)`＋影響列數）＋追加事件」，`rev` 等於最後事件的序號（ETag 的基礎）。
每個 `(shop, site)` 至多一筆非終態請求（部分唯一索引 `design_requests_one_active`）。

## 核准（D10）

`POST …/requests/{id}/approve`：必帶本文 `head_sha` 與 `If-Match: "<rev>"`；只有 `awaiting_approval`、`head_sha == candidate_sha == head_sha`、
`rev` 吻合才成功（條件式 UPDATE）。成功時平台配發 `publish_tag`（`design-{site}-{YYYYMMDD}-{N}`，UTC，`UNIQUE(shop_id, publish_tag)`）
與 `approval_expires_at`（`DESIGN_APPROVAL_TTL`，預設 1 小時）。單次消耗：同一人同一 head 重送回 200（`Idempotency-Replayed: true`、不產生事件），
他人重送 409。`published` 回報要求：已核准未逾期、核准**之後**對**確切的核准 SHA**有通過的 `guard_rechecked`、`published_sha` 與 `tag` 等於核准時的值。

**誠實的邊界**：平台無法阻止握有 git 寫入權的服務直接推 `main`——「不能發佈」的意思是服務**無法讓平台承認一次未經核准的發佈**。
縱深：分支保護、對帳（未追蹤變更）、以及（Q9 附條件、本切片未做）publish grant。

## API

後台（人）`/api/v1/admin/shops/{shopID}/design/…`：`requests`（列表、詳情＋ETag、`events`、`screenshots`、`approve`、`reject`、
`request-changes`、`cancel`、`revert`）、`state`、`untracked-changes`（＋`{sha}/acknowledge`）。
權限：`design.view`（owner、editor）、`design.request`（owner；取消）、`design.approve`（owner；核准／拒絕／要求修改／確認未追蹤）、`design.revert`（owner）。`staff` 全無。
平台層 `design_service.manage`（只有 `super_admin`）：`/api/v1/admin/design-service/credentials`（列表、簽發、撤銷、輪替）。

服務（機器）`/api/v1/design-service/shops/{shopID}/…`（`Authorization: Bearer dsk_…`）：建立請求（必帶 `Idempotency-Key`）、佇列、唯讀詳情（**不含 prompt 全文**）、
認領、head、guard-runs、preview、submit、published、failed、站台狀態、未追蹤變更。**沒有任何核准、拒絕、取消或還原路由**，
且憑證在 `/api/v1/admin/**` 一律 401。機器路由族**不在 `/admin` 群組下**（不經 user JWT、RBAC、Cloudflare Access、主機綁定）。

錯誤碼見 `openapi.yaml` 的 `Error` 說明表；全部 additive（v1 表面鎖只新增行）。

## 服務憑證（D8）

`dsk_` ＋ 32 bytes `crypto/rand` 的 base32；只在簽發／輪替的回應顯示一次（`Cache-Control: no-store`），資料庫只存 SHA-256。每店一把；預設 90 天、上限 365 天；
撤銷即時生效（驗證不快取）；`last_used_at` 至多每分鐘寫一次；可選 CIDR 允許清單；輪替＝簽發新的並把舊的縮到 7 天重疊。
失敗一律同一個 401（不說明是未知、過期、撤銷或 CIDR）；URL 的 `{shopID}` 與憑證的商家不同＝404。錯誤的金鑰只計入每 IP 每分鐘 30 次的限流並留 WARN（不含金鑰）。
每憑證每分鐘 120 次寫入；每店每日建立請求上限 `DESIGN_DAILY_REQUEST_CAP`（資料庫 `COUNT`，不依賴 Redis）。

## 建立請求（prompt 個資遮蔽，R4）

入庫時剔除控制字元與雙向覆寫字元（U+202A–202E、U+2066–2069），把 email、台灣手機（`09xxxxxxxx`、`+8869xxxxxxxx`，容許空白與連字號）、
通過 Luhn 的 13–19 位數字換成 `[redacted]` 並記入 `prompt_flags`；`prompt_sha256` 對遮蔽後的文字計算。`requested_by_user_id` 必須是該店**啟用中的成員**且持 `design.request`（403 `requester_not_permitted`）。

## 還原（D13）

`POST …/requests/{id}/revert` 建立**新的** `kind=revert` 請求（歷史不改寫）；目標必須曾發佈、不得是目前設計（`already_current`）、
不得低於站台的 core 相容下限（`revert_below_core_floor`，版本以點分數字比較）、站台目前 commit 必須已被服務回報（`site_state_unknown`）。
還原請求 `published` 後，目標之後、還原之前的已發佈請求被標為 `reverted`（`reverted_by_request_id` 指向它）。**內容儲存完全不受影響。**

## 保存與清除（D21）

`cmd/purge-design-history`（`make purge-design-history`＝dry run；`-apply`、`-shop N`、`-export-shop N`、`-all-for-shop N -apply`）。預設：prompt 全文 365 天後遮蔽、
已發佈截圖 365 天、其餘截圖 30 天、請求與事件 730 天、未追蹤變更 730 天、撤銷或過期的憑證 90 天；非終態請求永不清除；仍被別的請求引用的列等到引用者被清除。
分批 1000、冪等、只印計數與時間範圍；物件在 commit 之後 best-effort 刪除。設定：`DESIGN_*_RETENTION`（見 `.env.example`，皆有上下限）。
`cmd/design-sweep`（dry run 預設）：認領租約過期 → `failed(service_lost)`、`awaiting_approval` 逾 7 天 → `failed(approval_timeout)`、核准逾期 → `failed(approval_expired)`；皆為 `actor_type=system` 的事件。
**repo 沒有排程器**，需營運者排程（`docs/deploy/design-history-operations.md`）。

## 截圖（切片 2，D5／D6／D11）

私有的第二個 bucket（`DESIGN_R2_BUCKET`，自己的 token，不綁網域）；程式重用 `media.S3Store`（`ObjectStore` 新增 `PresignGet`），bucket、鍵、設定分開。
鍵 `shops/{shop}/design/{request}/{id}.png`；讀取**只經** `GET …/screenshots` 的簽名網址（預設 5 分鐘、`Cache-Control: no-store`，不寫日誌、不入任何資料表、回應不含物件鍵）。
上傳 `PUT …/requests/{id}/screenshots/{phase}/{role}/{viewport}/{viewKey}`（機器路由）：`image/png` ≤ 2 MiB、寬必須等於視口（390／1280）、高 ≤ 10,000、完整解碼後**重新編碼**
（去除全部中繼資料）、`sha256` 對存入位元組、`X-Content-SHA256` 對上傳位元組、`X-Capture-Manifest`（base64 JSON，只存已知欄位）；同位元組同 commit 重放回 200，不同位元組新增 `attempt`；
before 的 `commit_sha`＝`base_sha`、after＝目前 head；只在 `drafting`／`previewing` 可上傳。每請求 80 張（含所有 attempt）／40 MiB、每店 `DESIGN_SHOP_QUOTA_BYTES`（2 GiB）→ 409 `quota_exceeded`；
每憑證每分鐘 60 次上傳；解碼沿用同一種信號量（`MEDIA_PROCESS_CONCURRENCY`）。**admin 截圖必須來自 sandbox**（`live_data_capture_refused`，見操作員手冊第 7 節與 `docs/design-history-privacy.md`）。
`submit` 現在檢查 before／after 完整性，未設定儲存才回 503。清除指令刪物件，`cmd/design-sweep -orphans` 報告 bucket 與資料表的差異。
服務側的完整契約：`docs/design-service-contract.md`。

## 尚未做

publish grant（Q9）與通知信（Q10）（附條件決議）、sandbox 堆疊的部署（操作員手冊第 7 節）、template 後續任務 T1–T8
（`openspec/changes/design-history/tasks.md` 列出每一項需要什麼）。
