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
