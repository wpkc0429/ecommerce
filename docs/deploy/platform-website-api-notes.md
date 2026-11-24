# 平台官網的 API 端（開店申請）：契約、設定與營運手冊

change `platform-website` 的 **A 部分**（ecommerce repo 這一側）。官網本身是**獨立 repo 的零建置靜態站**（B 部分，尚未建立）：
它沒有後端、沒有資料庫、沒有任何密鑰，只呼叫下面的兩個公開端點。需要後端運算或伺服器端狀態的東西——申請的儲存、
Turnstile 驗證、信箱驗證、審核、通知、限流、稽核、個資保存——全部在這裡。

> 下面標示 **【需要你提供】** 的是我們這邊無法代辦、要由營運者本人提供的東西；程式在它們還沒備齊時照常運作（開發用測試值、
> 人工 outbox 後備），**不會**因為缺少它們而擋住 ecommerce 的任何其他功能。

## 1. 公開端點契約（給官網鎖定核心）

完整 schema 與範例在 `api/openapi/openapi.yaml`（tag `public-application`）。兩條都**無認證、無租戶解析**，掛在
`/api/v1/public/`，**CORS 只放行 `WEBSITE_URL` 這一個 origin**（沒有 `Allow-Credentials`；其他 origin、平台自己的前端、
商家 origin 在這個路徑下一律沒有 CORS，官網在其他任何 API 路徑下也沒有）。

### `POST /api/v1/public/shop-applications`

請求（`Content-Type: application/json`，**body ≤ 16 KiB**）：

```json
{
  "email": "owner@example.com",
  "contact_name": "王小明",
  "phone": "0912-345-678",
  "shop_name": "小明花坊",
  "business_summary": "我們販售手作乾燥花束。",
  "consent_version": "placeholder-v0",
  "turnstile_token": "<Turnstile widget 產生的 token>",
  "website": "",
  "elapsed_seconds": 42.5
}
```

| 欄位 | 規則 |
| --- | --- |
| `email` | 必填，合法位址，≤ 254；伺服器小寫並去空白 |
| `contact_name` | 必填，1–100 字，不得含控制字元（含換行） |
| `shop_name` | 必填，1–100 字；控制字元被移除 |
| `phone` | 選填，只允許數字與 `+ - ( )` 與空白，≤ 20 |
| `business_summary` | 必填，1–1000 字（以字計，非位元組） |
| `consent_version` | 必填，**必須等於 API 目前的 `APPLICATION_CONSENT_VERSION`**，否則 422 |
| `turnstile_token` | 必填，≤ 2048 |
| `website` | **honeypot**：真人看不到這個欄位，必須是空字串或省略；非空 → 422 |
| `elapsed_seconds` | 選填：表單載入到送出的秒數。< 3 秒不拒絕，只在審核畫面標示「填表過快」 |

未知欄位被忽略。**成功一律 `202 {"status":"received"}`，位元組逐一相同**——無論 email 是新的、已有未完成申請、
在 30 天再申請冷卻期內、或已是後台帳號（沒有使用者列舉；整合測試逐位元組比對）。官網的訊息**不得**暗示「這個信箱已申請過」。

| 狀態 | `error.code` | 意義與官網應有的行為 |
| --- | --- | --- |
| 202 | — | 導向 `/apply/received/`（「若信箱收不到，我們會以人工方式聯絡」） |
| 400 | `bad_request` | JSON 壞掉（核心不應發生） |
| 413 | `payload_too_large` | body 超過 16 KiB |
| 422 | `validation_failed` | `error.details[].pointer` 是 `/email`、`/contact_name`、`/shop_name`、`/phone`、`/business_summary`、`/consent_version`、`/website`、`/turnstile_token`；只取決於輸入。`/turnstile_token` → 重置 widget 後重試；`/consent_version` → 官網 `config.js` 與 API 的版本不同步（部署問題） |
| 429 | `rate_limited` | 附 `Retry-After`（秒）；顯示「請稍後再試」 |
| 503 | `verification_unavailable` | Turnstile 驗證服務暫時無法判定（**不放行**）；稍後重試 |
| 500 | `internal` | 通用錯誤訊息 |

### `POST /api/v1/public/shop-applications/verify`

請求 `{"token":"<信中連結 fragment 的 token>"}`（≤ 16 KiB）。驗證連結的形狀：`{WEBSITE_URL}/apply/verify#<token>`
（token 在 **fragment**，不進伺服器日誌與 Referer；官網頁面讀出後立刻 `history.replaceState` 清掉，頁面設 `noindex`、
`Referrer-Policy: no-referrer`）。

| 狀態 | 意義 |
| --- | --- |
| 200 `{"status":"verified"}` | 申請轉為 `pending_review`，才可被審核端核准 |
| 404 `not_found` | token 無效／過期／已使用／未知——**四者一律同一個回應** |
| 429 `rate_limited` | 每 IP 限流 |

token：32 bytes 隨機、資料庫只存 SHA-256、24 小時有效、單次、原子消費；重新簽發會讓舊的失效。

## 2. 濫用防線（一覽）

依序：body ≤ 16 KiB → **每 IP**（broad 20／10 分鐘；tight：同 IP＋同 email 5／10 分鐘）→ honeypot → 欄位驗證（先於 Turnstile，
無效輸入不消耗驗證）→ **Cloudflare Turnstile**（無效 422；故障 503）→ **每 email**（不含 IP，預設 3／24 小時，Redis 鍵只含雜湊；
**在 Turnstile 之後**才計，所以沒有 token 的機器人燒不到別人信箱的額度）→ **全站每日上限**（預設 200／24 小時，以資料庫
`COUNT(*)` 計，Redis 掛掉時照樣有效）。

Redis 故障時限流器 **fail open**（既有行為），後盾是資料庫：同一 email 至多一筆未完成申請（部分唯一索引）、同一申請
10 分鐘內只簽一枚 token、每日上限。**Turnstile 則相反：fail closed**（驗證服務不可達 → 503，絕不放行）。

`APP_ENV=production` 時缺 `TURNSTILE_SECRET_KEY` 或 `WEBSITE_URL` **拒絕啟動**；Cloudflare 公布的測試密鑰（`1x…`／`2x…`／`3x…AA`）
與覆寫 `TURNSTILE_VERIFY_URL` 在 production 同樣被拒絕——沒有任何設定組合可以在 production 靜默關掉這道閘門。
development／test 未設定密鑰時使用不連網的本機驗證器，只接受 Cloudflare 官方文件的測試 token `XXXX.DUMMY.TOKEN.XXXX`。

## 3. 環境變數

| 變數 | 預設 | 說明 |
| --- | --- | --- |
| `TURNSTILE_SECRET_KEY` | — | **【需要你提供】** Turnstile 密鑰。production 必填。dev/test 空值＝本機驗證器 |
| `TURNSTILE_SITE_KEY` | — | 公開的 site key，這裡只為文件（官網 `config.js` 持有真正的那份），API 不使用 |
| `TURNSTILE_VERIFY_URL` | Cloudflare siteverify | 測試／本機假服務用；production 禁止覆寫 |
| `WEBSITE_URL` | `http://localhost:4000` | **【需要你提供網域】** 官網 origin：驗證連結基底，也是公開 API 唯一的 CORS origin。production 必填且須 https |
| `APPLICATION_CONSENT_VERSION` | `placeholder-v0` | **【需要你提供條款文字與版本】** 官網 `config.js` 的 `consentVersion` 必須逐字相同 |
| `APPLICATION_DAILY_CAP` | 200 | 全站每 24 小時新申請上限 |
| `APPLICATION_REAPPLY_COOLDOWN` | 720h（30 天） | 被拒絕（或已核准）的 email 在此期間再申請：回 202 但不建新列 |
| `APPLICATION_REJECTED_RETENTION` | 2160h（90 天） | 已拒絕／過期／撤回的個資保存期限（不早於冷卻結束）。**待法務確認** |
| `APPLICATION_APPROVED_RETENTION` | 8760h（1 年） | 已核准的個資保存期限。**待法務確認** |
| `RATE_LIMIT_APPLICATION_BROAD_{LIMIT,WINDOW}` | 20 / 10m | 每 IP |
| `RATE_LIMIT_APPLICATION_TIGHT_{LIMIT,WINDOW}` | 5 / 10m | 每 IP＋每 email 雜湊 |
| `RATE_LIMIT_APPLICATION_EMAIL_{LIMIT,WINDOW}` | 3 / 24h | 每 email（不含 IP） |
| `SMTP_*` | 未設定 | 同訂單通知信那組；設定後驗證信與決定通知改走 SMTP，否則走人工 outbox |

另外 `CORS_ALLOWED_ORIGINS` **不要**加官網 origin：官網 origin 只經 `WEBSITE_URL` 取得公開申請 API 的 CORS，加進平台清單
反而讓它能在其他非 admin 路徑被放行。回滾＝把 `WEBSITE_URL` 改回其他值（表單即失效）；資料表保留。

來源 IP 只存 `HMAC-SHA256(key, ip)`（key 由 `ADMIN_JWT_SECRET` 以固定標籤衍生，輪替 secret 只會讓比對失效，不洩漏任何東西）。

## 4. 通知：SMTP 與人工 outbox

`application.Notifier` 介面有兩個實作，決定點只有一處（`api/internal/app/wire.go`）：

- **SMTP**（`SMTP_USERNAME` 與 `SMTP_PASSWORD` 有設定）：訊息列先寫入（`queued`），commit 後交給有界 worker 非同步寄出
  （不拖慢請求、佇列滿了丟棄並留 `failed` 列；沒有重試，重啟會讓緩衝中的信停在 `queued`）。寄出後內文清空。
  **上線前必須接上交易式 email／SMTP 服務**（已採用的決定）；與訂單信共用同一個寄件帳號（Gmail 約 100 封／日的限制見訂單通知的設計）。
  **【需要你提供】** 寄信服務與寄件網域。
- **人工 outbox**（預設／後備）：訊息列為 `pending_manual`，平台後台「開店申請」詳情的「通知訊息」顯示收件人與內文，可複製，
  以其他管道（LINE、電話、自己的信箱）轉交後按「標記為已寄出」。沒有 SMTP 時**申請者收不到驗證信**，所以操作員要代為轉交驗證連結，
  或在確認過聯絡方式後使用「人工確認聯絡方式」（記稽核事件與風險旗標 `manually_verified`）。

訊息種類：`verify_email`、`approved`、`rejected`（含對申請者可見的拒絕理由）。**owner 的密碼設定連結永遠不進 outbox**
（那是持有即可使用的憑證）：它只在核准回應中一次性回傳（`delivery: "manual"`），操作員當場複製。

## 5. 操作員手冊

### 日常審核（平台後台 → 開店申請）

權限：只有 `super_admin`（`shop_application.view`／`.review`；核准另需 `shop.create` ＋ `account.manage`，與一鍵開店相同）。
商家層角色沒有任何 `shop_application.*`。徽章數字＝待審核申請 ＋ 待人工寄出的訊息。

1. 列表可依狀態、搜尋（email／店名／聯絡人）篩選。`待驗證信箱` 的申請**不可核准**。
2. 無 SMTP 時：開「待驗證信箱」的申請 → 複製驗證信內文轉給申請者 → 標記已寄出；申請者點連結後狀態變「待審核」。
   聯絡不到信箱但已用其他方式確認身分 → 「人工確認聯絡方式」。
3. **核准**：確認商家名稱與狀態（預設「審核中」：店面不可達，上線時再改為啟用）→ 核准即單一交易開店（商家、首頁、owner 帳號）。
   回應會**只顯示一次** owner 的密碼設定連結（72 小時有效、單次）——當場複製，以安全管道交給本人；沒有第二次機會
   （只能由帳號管理重新產生）。若 owner email 已是後台帳號，畫面會警示且須勾選確認：該帳號只被加入為 owner，密碼不動，不產生連結。
4. 核准**不會**建立 publishable key、origin、內容 schema 或金流憑證；接下來照 `merchant-onboarding` skill 的步驟 3 起進行。
5. **拒絕**需填理由（申請者看得到，會寫進通知訊息）；內部備註只有操作員看得到。被拒絕的 email 在冷卻期內無法重新申請。
6. 失敗的核准（開店服務出錯）整筆回滾、申請維持「待審核」，時間線有「核准失敗（已回滾）」。

### 個資保存與清除（需排程）

repo 內沒有排程器：請在你自己的 cron／工作排程每日執行（先 dry run 看一次）：

```bash
cd api && go run ./cmd/purge-applications            # dry run，只印 id
cd api && go run ./cmd/purge-applications -apply     # 寫入（冪等）
cd api && go run ./cmd/purge-applications -email someone@example.com -apply   # 個資請求：依 email 刪除
make purge-applications                              # 等同第一行
```

規則：未驗證超過 7 天 → 整列刪除（連同 token 與 outbox 訊息）；已拒絕／過期／撤回 → 保存期限後清空個資欄位
（且不早於再申請冷卻結束）；已核准 → 保存期限後清空；待審核永不自動過期。稽核事件列永遠保留且不含個資。
**保存期限是實作預設，上線前需法務確認**（`/privacy` 的文字亦然）。

### 部署順序與回滾

API（`make migrate`；`make seed` 以建立兩個新權限節點並授予 `super_admin`）→ 平台後台靜態檔 → 官網。
**主資料庫需要執行的遷移：`20261124200226_add_shop_applications`（純新增四張表，down 直接 DROP）**，之後 `make seed`。
回滾：把 `WEBSITE_URL` 改掉讓表單失效；資料表保留。

## 6. 仍需要你提供的東西

| 項目 | 用途 |
| --- | --- |
| 官網網域與託管（建議 Cloudflare Workers static assets／Pages） | `WEBSITE_URL`、CORS、驗證連結 |
| Turnstile 帳號、site key、secret | `TURNSTILE_SECRET_KEY`（API）、site key（官網 `config.js`） |
| 條款與隱私文字、版本號 | `APPLICATION_CONSENT_VERSION` ＝ 官網 `consentVersion` |
| 寄信服務（交易式 email／SMTP）與寄件網域 | 驗證信與決定通知；在那之前走人工 outbox |
| 法務對保存期限（90 天／1 年）的確認 | `APPLICATION_*_RETENTION` |
| 官網 repo 名稱、擁有者與腳手架來源 | B 部分 |
