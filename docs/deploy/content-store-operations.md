# 內容儲存的營運手冊：發佈 hook、排程發佈、稽核清除

給操作員（你）用。設計依據：`openspec/changes/headless-content-store/design.md`（D7、D11、D12 與「第三切片的決定」）；
API 與資料模型：`docs/content-store.md`。repo 沒有排程器，所以排程發佈與清除都需要你自己的 cron／Windows 工作排程。

## 1. 發佈 hook（發佈後讓商家站台重建）

**預設關閉。** 每家商家至多一個 hook（一條 URL），由持有 `content.manage` 的人（`merchant_owner`、`super_admin`）設定。

### 1.1 在 Cloudflare 建立 deploy hook

1. Cloudflare 儀表板 → **Workers & Pages** → 商家站台的專案。
2. Workers：**Settings → Builds → Deploy Hooks**；Pages：**Settings → Builds → Add deploy hook**。
3. 取名（例如 `content-store`）、選要建置的分支 → **Create** → 複製產生的 URL。

URL 形狀（官方文件 <https://developers.cloudflare.com/workers/ci-cd/builds/deploy-hooks/>、
<https://developers.cloudflare.com/pages/configuration/deploy-hooks/>）：

```
Workers Builds  https://api.cloudflare.com/client/v4/workers/builds/deploy_hooks/<ID>
Pages           https://api.cloudflare.com/client/v4/pages/webhooks/deploy_hooks/<ID>
```

**這條 URL 就是憑證**（不需要任何標頭）：任何拿到它的人都能觸發建置。不要貼進聊天、issue 或 repo。
Cloudflare 的限制：每個 Worker 每分鐘 10 次建置、每帳號每分鐘 100 次（本系統遠低於此）。

> Pages 的 URL 形狀在官方文件頁只出現在儀表板截圖裡（文字沒有印出路徑）。**以儀表板給你的 URL 為準**：
> 先用 dry run 驗證（下面），不通過就是 Cloudflare 改了形狀，回報給開發者更新預設 allowlist，不要用環境變數繞過。

### 1.2 設定（只寫，不可讀回）

```bash
BASE=https://<api-host>/api/v1/admin/shops/<shopID>/content/publish-hook
# 1) dry run：只驗證 URL 形狀與 allowlist，不儲存、不呼叫
curl -s -X PUT "$BASE?dry_run=true" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"url":"<貼上 deploy hook URL>"}'
# 2) 儲存（加密後存入，之後任何 API 都讀不回完整 URL）
curl -s -X PUT "$BASE" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"url":"<URL>"}'
# 3) 測試呼叫（真的對 Cloudflare 打一次 POST，會觸發一次建置；每商家每分鐘 3 次）
curl -s -X POST "$BASE/test" -H "Authorization: Bearer $TOKEN"
# 4) 狀態
curl -s "$BASE" -H "Authorization: Bearer $TOKEN"
```

狀態只含主機、遮罩 URL（`https://api.cloudflare.com/…` ＋ 末四碼）與結果類別，**不含完整 URL、不含 secret、不含接收端回應內容**。
管理畫面（下一個 task）會呼叫同樣的端點。

### 1.3 行為

- 發佈或下架文件後，**同一商家 30 秒內的多次發佈合併成一次呼叫**（持續發佈最多拖 5 分鐘）；兩次呼叫至少間隔 60 秒（未到則保留為待送，不會遺失）；
  每商家每日（UTC）最多 30 次（用完的觸發被丟棄並記 `rate_limited`，不算失敗）。
- 失敗（逾時、5xx、429）自動重試 2 次（2 秒、4 秒）；其他錯誤不重試。**連續 5 次失敗自動停用**，狀態 `failed: true`、
  `disabled_reason: consecutive_failures`——修好接收端後 `PUT {"enabled": true}` 重新啟用。
- 手動重建：`POST …/publish-hook/fire`（略過去抖動，仍佔每日上限）。
- 請求主體只有 `{event, shop_id, key, rev, at, coalesced}`，**不含內容**。標頭 `X-Content-Timestamp`、`X-Content-Signature`
  （`sha256=HMAC-SHA256(secret, timestamp + "." + body)`）供自訂主機驗證；**Cloudflare deploy hook 不驗簽也不看主體**。
  要取得 secret：`POST …/publish-hook/signing-secret`（**只顯示一次**）。
- 內容儲存的發佈**不會因為 hook 失敗而失敗**；hook 在背景佇列中進行。
- 稽核：`hook.set`／`hook.test`／`hook.fire`／`hook.disable`／`hook.delete`／`hook.secret_rotate` 寫進 `content_audit_log`，只含主機與結果類別，不含 URL。

### 1.4 平台層設定（環境變數）

| 變數 | 預設 | 說明 |
| --- | --- | --- |
| `CONTENT_HOOK_ALLOWED_HOSTS` | 空 | **只新增**：操作員核准的額外主機（逗號分隔，精確名稱或 `*.suffix`；對它們不限路徑）。內建的只有 `api.cloudflare.com` 的 deploy hook 路徑。**不能**放寬私有／loopback／link-local／中繼資料位址的封鎖。 |
| `CONTENT_HOOK_DEBOUNCE` | `30s` | 去抖動視窗 |
| `CONTENT_HOOK_MAX_WAIT` | `5m` | 連續發佈時最久延後多久 |
| `CONTENT_HOOK_MIN_INTERVAL` | `60s` | 同商家兩次呼叫的最短間隔 |
| `CONTENT_HOOK_DAILY_CAP` | `30` | 同商家每日（UTC）上限 |
| `CONTENT_HOOK_QUEUE_SIZE` | `256` | 事件到寫入器之間的有界佇列 |
| `CONTENT_HOOK_FLUSH_INTERVAL` | `5s` | 掃描到期 hook 的週期 |
| `CONTENT_HOOK_DEV_INSECURE` | 關 | **只給本機 e2e**（允許 http、任意埠、loopback；主機仍須在 allowlist）；`APP_ENV=production` 時拒絕啟動 |

給商家加自訂主機（例如它自己的 CI webhook）是**平台管理員的決定**：把主機加到 `CONTENT_HOOK_ALLOWED_HOSTS` 並重啟 API。
那個主機仍然只能解析到公網位址（解析結果每次發送都重新檢查，連線釘在檢查過的 IP）、不跟隨重導向、5 秒逾時。

### 1.5 部署

主 DB 需要 `make migrate`（`20261125081316_add_content_publish_hooks`：純新增一張表，down 直接 `DROP TABLE`，可逆）。
沒有新權限節點，**不需要 re-seed**。沒有設定 hook 的商家行為與之前完全相同。

## 2. 排程發佈（誰在每分鐘執行排程）

商家在後台把草稿排定在未來發佈（語意見 `docs/content-store.md` §8）。**要有人每分鐘跑一次排程器**，否則排程只會躺在資料庫。兩種方式，可以並存
（Postgres advisory lock 讓第二個整輪跳過，每筆排程另以條件式 UPDATE 認領，所以不會發佈兩次）：

### 2.1 一次性指令（建議：cron／Windows 工作排程）

```bash
cd api && go run ./cmd/content-scheduler        # 跑一次就結束；make content-scheduler 等價
go build -o content-scheduler ./cmd/content-scheduler   # 建議編譯一次，排程呼叫二進位檔
./content-scheduler -every 1m                    # 或讓它自己常駐，每分鐘一次（用 systemd／NSSM 之類管理）
```

它需要與 API 相同的環境變數（`DATABASE_URL`、`REDIS_*`、`PAYMENT_CREDENTIALS_ENCRYPTION_KEY`、`CONTENT_HOOK_*`、`CONTENT_SCHEDULE_*`）。
**Redis 不可達時它在認領任何排程之前就失敗退出**（沒有失效渲染快取的發佈會讓舊內容留在快取最多 24 小時；排程維持 `pending`，下一輪再試）。
結束前它會把這一輪累積的發佈 hook 呼叫送出（受最短間隔與每日上限約束，被擋住的留在待送，由 API 的 flusher 補送）。
輸出只有計數：`due=… published=… failed=… missed=…`；退出碼非 0 代表「這一輪本身做不了」（資料庫或 Redis），個別排程失敗記在資料裡與 API 裡，不影響退出碼。

**Linux cron**（每分鐘）：

```cron
* * * * * cd /srv/ecommerce/api && /srv/ecommerce/api/content-scheduler >> /var/log/content-scheduler.log 2>&1
```

**Windows 工作排程器**（每分鐘，不論是否登入）：

```powershell
$action  = New-ScheduledTaskAction -Execute "C:\ecommerce\api\content-scheduler.exe" -WorkingDirectory "C:\ecommerce\api"
$trigger = New-ScheduledTaskTrigger -Once -At (Get-Date) -RepetitionInterval (New-TimeSpan -Minutes 1)
Register-ScheduledTask -TaskName "ecommerce-content-scheduler" -Action $action -Trigger $trigger -User "SYSTEM" -RunLevel Highest
```

（`.env` 由工作目錄往上找；也可在系統環境變數設定。工作排程器的重複間隔最小 1 分鐘，剛好就是粒度。）

### 2.2 API 內的 ticker（選用，預設關閉）

`CONTENT_SCHEDULER_ENABLED=1`（`CONTENT_SCHEDULER_INTERVAL` 預設 `1m`）。適合單一 API 實例、不想另外管 cron 的環境；多實例時每個都可以開，advisory lock 保證同一時刻只有一個在處理。
API 行程停機的那段時間不會發佈；恢復後依寬限規則處理（見下）。

### 2.3 停機與時鐘

- 「到點」由資料庫 `now()` 判斷，不看各台機器的時鐘。
- 排程器（或 API）停機之後恢復：遲到在 `CONTENT_SCHEDULE_GRACE`（預設 1 小時）內的照常發佈，超過的標 `missed`、**不發佈**（由人決定是否補排）。
  所以排程器掛超過 1 小時要看一下 `GET …/content/schedules?scope=recent` 有沒有 `missed`。
- `running` 超過 10 分鐘視為上一輪崩潰，下一輪重置並重試（已發佈的不會再寫一次 revision）。

| 變數 | 預設 | 說明 |
| --- | --- | --- |
| `CONTENT_SCHEDULER_ENABLED` | 關 | API 內 ticker |
| `CONTENT_SCHEDULER_INTERVAL` | `1m` | ticker 週期（一次性指令不看它） |
| `CONTENT_SCHEDULE_MIN_LEAD` | `2m` | `publish_at` 至少是現在之後多久 |
| `CONTENT_SCHEDULE_MAX_AHEAD` | `90d`（`2160h`） | `publish_at` 最遠 |
| `CONTENT_SCHEDULE_GRACE` | `1h` | 遲到超過這個就 `missed` |
| `CONTENT_SCHEDULE_RETENTION` | `90d`（`2160h`） | 已結束的排程列保留多久（由 purge 指令清除） |

### 2.4 部署

主 DB 需要 `make migrate`（`20261125083157_add_content_publish_schedules`：純新增一張表，down 直接 `DROP TABLE`，可逆）。沒有新權限節點、不需要 re-seed。
**順序**：先 `20261125081316_add_content_publish_hooks`，再 `20261125083157_add_content_publish_schedules`（`make migrate` 依序套用兩支）；之後部署 API，再設定排程器。

## 3. 稽核與排程列的清除

repo 沒有排程器，請每日跑一次（先 dry run 看一次）：

```bash
cd api && go run ./cmd/purge-content-audit            # dry run：只印計數與時間範圍
cd api && go run ./cmd/purge-content-audit -apply     # 寫入（冪等、分批）
cd api && go run ./cmd/purge-content-audit -shop 3    # 只處理一家
make purge-content-audit                              # 等同第一行
```

規則：`content_audit_log` 超過 `CONTENT_AUDIT_RETENTION`（預設 180 天）的列刪除；已結束（done／failed／missed／cancelled／superseded）的排程列超過
`CONTENT_SCHEDULE_RETENTION`（預設 90 天）刪除；等待中與處理中的排程永不清除。稽核列本來就不含個資（只有 id、計數、結果類別、主機），輸出也只列計數與日期範圍。
與 `cmd/purge-applications`（商家開店申請，見 `platform-website-api-notes.md` §5）是兩支獨立的指令，各自排程。
