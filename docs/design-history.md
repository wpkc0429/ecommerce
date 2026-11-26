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
before 的 `commit_sha`＝`base_sha`、after＝目前 head；只在 `drafting`／`previewing` 可上傳。每請求 80 個**槽位**（重拍同一槽位只算一個，超過 409 `slot_cap_exceeded`）、另有較寬的**嘗試總數**上限（預設 240＝80×3，`DESIGN_MAX_SCREENSHOT_ATTEMPTS`，超過 409 `attempt_cap_exceeded`）；每請求 40 MiB（被取代但仍存放的 attempt 照算）、每店 `DESIGN_SHOP_QUOTA_BYTES`（2 GiB）→ 409 `quota_exceeded`；
每憑證每分鐘 60 次上傳；解碼沿用同一種信號量（`MEDIA_PROCESS_CONCURRENCY`）。**admin 截圖必須來自 sandbox**（`live_data_capture_refused`，見操作員手冊第 7 節與 `docs/design-history-privacy.md`）。
`submit` 現在檢查 before／after 完整性，未設定儲存才回 503。清除指令刪物件，`cmd/design-sweep -orphans` 報告 bucket 與資料表的差異。
服務側的完整契約：`docs/design-service-contract.md`。

## 版本預覽（切片 4，D25）

商家可以在歷史列表對任一曾發佈的設計按「預覽此版本」：部署一份**過去的設計層＋目前的 core**、用完即丟的預覽，用自己的身分登入、以目前的資料即時觀看。它**不屬於任何請求**：不佔「每站台一筆進行中請求」的名額（進行中的請求與版本預覽可同時存在）、不寫 `design_request_events`、不改任何 `design_requests` 列、**不拍也不存任何截圖**。唯一的紀錄是輕量的 `design_version_previews`（`tenantOwned`，無 trigger、無雜湊串、`requested_by_user_id` 與 `target_request_id` 無外鍵，保留 90 天，不是核准依據）。

- **名詞**：「改版預覽」＝核准前、屬於某請求的預覽（D4）；「版本預覽」＝本節。UI 按鈕「預覽此版本」，預覽上的 CTA「改成這個版本」只導向既有的 `POST …/requests/{id}/revert`（`design.revert`；完整還原流程與人類核准一個步驟都不少）。
- **權限**：`design.view`（owner、editor；`super_admin` 經 `*`；staff 不行）。他店的請求 404。
- **API**（additive；`api/openapi/openapi.yaml`）：人 `POST /admin/shops/{id}/design/requests/{id}/version-preview`（202）、`GET …/design/version-previews/{pid}`；服務 `GET /design-service/shops/{id}/version-previews`、`POST …/{pid}/claim`（租約 5 分鐘）、`PUT …/{pid}/ready`、`POST …/{pid}/failed`。**沒有任何服務端的建立或核准路由。**
- **相容檢查先於一切**（與還原是同一個函式 `checkRestorable`）：目標早於 core 下限 → 409 `revert_below_core_floor`（附原因）、目標已是目前設計 → 409 `already_current`、不是已發佈設計 → 409 `invalid_state`、站台狀態未知 → 409 `site_state_unknown`。這些都**不建立列、不用限流額度、不讓服務部署任何東西**。通過後服務在部署前仍以目前 core 跑守門員，失敗回報 `failed(guard_failed, rule_ids)`、不部署，發起者讀得到原因與規則 id。
- **一份有效預覽**：同一 `(shop, site)` 至多一份 `queued｜claimed｜ready`（部分唯一索引；每店以 advisory lock 序列化建立）；新的請求使舊的轉 `superseded`，服務以 `?status=superseded` 找到該拆除的部署。
- **TTL 與清掃**：預設 30 分鐘（`DESIGN_VERSION_PREVIEW_TTL`，1 分鐘～2 小時）。`expires_at` 一過，讀取就回 `expired`（不等清掃）並不再回 `origin`；`cmd/design-sweep -apply` 把逾時列寫成 `expired`、把逾租約的 `claimed` 寫成 `failed(service_lost)`，**只碰這張表**。
- **限流**（資料庫 COUNT，不依賴 Redis）：每 `(shop, user)` 每小時 6（`…_HOURLY_CAP`，429 `hourly_cap_reached`）、每店每 UTC 日 30（`…_DAILY_CAP`，429 `daily_cap_reached`），都附 `Retry-After`。偏離 D25 字面之處：兩種上限用兩個錯誤碼，讓畫面能說清楚是哪一種。
- **`origin` 只回給發起者本人**（且 `ready`、未逾期）；同店其他成員讀同一列時鍵不存在；服務路由族完全不回 origin。登記時 origin 只能是 scheme＋主機（含路徑／查詢／片段 422，列維持 `claimed`）。日誌只含 id、站台與狀態。
- **唯讀（`read_only`）**：2026-11-26 決定預覽模式唯讀——核心在版本預覽與改版預覽下停用所有寫入型 `ctx.actions`（template 任務 T12）。平台把這個事實放進每一份預覽描述：版本預覽的 `read_only: true`（人與服務兩種回應）、請求詳情與服務的請求回應新增 `preview_read_only: true`。恆為 true；服務部署時必須以唯讀預覽模式部署。
- **誠實的限制**：版本預覽用的是**現在**的 core 與資料，與該版當時的樣子可能不同，會過期、不被保存；它不是核准當時的紀錄。歷史畫面必須在按鈕附近以固定文字說明，並維持並排截圖才是「當時」的證據。
- **保存**：`DESIGN_VERSION_PREVIEW_RETENTION`（90 天）由 `cmd/purge-design-history` 清除；`-export-shop` 輸出使用列（不含 origin）；`-all-for-shop` 一併刪除。
- **Template 端還要做的事（不在本 repo）**見 `openspec/changes/design-history/tasks.md` 的 T9–T12：`ctx.actions.requestVersionPreview`／`ctx.data.getVersionPreview` 與錯誤對應、歷史畫面按鈕與固定說明、預覽主機的嚴格 CSP＋`noindex`＋核心橫幅（版本預覽、tag、到期）、預覽模式唯讀。

## 尚未做

publish grant（Q9）與通知信（Q10）（附條件決議）、sandbox 堆疊的部署（操作員手冊第 7 節）、template 後續任務 T1–T8
（`openspec/changes/design-history/tasks.md` 列出每一項需要什麼）。
