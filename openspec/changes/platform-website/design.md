## Context（已對照程式碼查證）

- `web/`：Next.js 15、`output: "standalone"`、`app/[[...path]]/page.tsx`（依 Host 呼叫 `/api/v1/render/page`）、`app/preview`、`themes/starter/*`、`lib/richtext-allowlist.json`（由 `api/internal/cms/richtext/allowlist.json` 經 `npm run gen:types` 複製）。`web/Dockerfile` 跑 `node server.js`；`.github/workflows/image-web.yml` 建 ghcr 映像；`ci.yml` 的 web job 做 `npm ci`、「Generated types are current」、`npm run build`。
- 開店：`provision.Service.ProvisionShop`（`api/internal/provision/service.go`）自開交易、commit 後才發 `SiteMappingChanged`／`StorefrontClientChanged` 並 `Accounts.Deliver`；輸入 `Name、ThemeID?、Status?、Domain?、PathPrefix?、StorefrontOrigins?、OwnerEmail、ActorID`；owner 已存在則只加入成員資格（`Created=false`、無連結）。HTTP 層 `POST /admin/shop-provisioning` 另外檢查 `account.manage` 等權限。
- 連結交付：`account.LinkDelivery`（`ManualDelivery` 唯一實作）。連結基底是該店 `purpose=admin` origin（最小 id）否則 `ADMIN_PUBLIC_URL`。
- 寄信：`notify.Sender{Send(ctx, Message{To,Subject,TextBody})}`、`SMTPSender`；`SMTP_USERNAME/PASSWORD` 未設定＝`DiscardSender`（wire.go 以真實實作而非 nil，因為接線稽核 ⑤ 的豁免表是空的）。Gmail 額度（約 100 封／日）是 `order-notification-email` D3 已量化的限制，與訂單信共用一個寄件帳號。
- 限流：`ratelimit.Limiter`（Redis 滑動視窗，**Redis 故障時 fail open**）；`httpapi/ratelimitmw.go` 有 `ipRule`、`ipEmailRule`（IP＋email 複合鍵），**沒有「只依 email」的規則**；`peekEmail` 從 JSON body 讀 `email`。客戶端 IP 由 `NewClientIPMiddleware` 決定（`TRUSTED_PROXIES` 內才採信 `CF-Connecting-IP`）。
- 沒有通用稽核表；`admin_credential_tokens` 是 token 表的既有樣板（SHA-256、單次、原子消費）。
- 公開 API 網域對 `^/api/v1/admin` 回 404（tunnel 規則）；平台後台 API 只在 `PLATFORM_ADMIN_HOSTS` ＋ Cloudflare Access 後可達。公開申請端點因此必須在 `/api/v1/public/…`，審核端點在 `/api/v1/admin/…`。
- `CORS_ALLOWED_ORIGINS`＝平台自己的前端清單（預設含 `http://localhost:3000`）。

## Goals / Non-Goals

**Goals**：官網上線；商家能自助申請；操作員在平台後台審核並一鍵開店；濫用面可控；個資有保存期限；沒有信件能力也能完整運作。

**Non-Goals**：自動核准；線上付款／訂閱；多語系；商家自助的後續上線步驟（建 repo、部署、DNS、Access——仍是 `merchant-onboarding` skill 的人工流程）；寄信服務的選型與寄件網域設定（只定義抽象）；申請者登入的「申請進度」頁。

## Decisions

### D1：改造 Next.js `web/`（靜態輸出），而不是另建靜態站
選擇：保留 `web/` 目錄與 Next.js，改為 `output: "export"`（純靜態，建置時預先渲染），清掉租戶渲染程式碼。
理由：①申請表單有真實的用戶端邏輯（Turnstile 小工具、欄位驗證、送出狀態、錯誤對應、驗證連結頁），TS＋React 比手寫 DOM 好維護且 `tsc` 有型別檢查；②已有 CI job（typecheck＋build）、Dockerfile 與 image workflow 骨架可沿用或刪減，不必為官網新建 repo 與 pipeline；③App Router 的 metadata／`sitemap.ts`／`robots.ts` 直接解決 SEO 基礎；④靜態輸出＝執行期零 Node、零 Host 相依，攻擊面與 `merchant-template`（靜態＋Cloudflare）相同。
代價（誠實）：幾頁文案用 Next＋React 是「用大砲」，建置依賴比零建置的範本重；官網文案由人維護、不像範本由 AI 改設計層，所以沒有「鎖定核心」的保護需求，零建置的主要優勢（AI 守門員）不適用。被否決的選項是**範本式零建置靜態站**（新目錄 `site/`）：最輕，但要另建 CI，且表單邏輯沒有型別；使用者若偏好極簡可改選它，tasks 幾乎不變（Q1）。
**關鍵限制**：`output: "export"` 不允許 catch-all 動態 Host 渲染、`headers()`、`cache: "no-store"` 的伺服器 fetch——所以 `web/` 現有租戶渲染路由**無法與官網並存於同一個 build**。見 D9。

### D2：公開申請端點與回應的「不洩漏」規則
`POST /api/v1/public/shop-applications`，body：`email`（必填）、`contact_name`、`shop_name`、`phone?`、`business_summary`（≤ 1000 rune）、`consent_version`（必填，等於目前條款版本，否則 422）、`turnstile_token`、`website`（honeypot，必須為空）。
- **一律回 `202 {"status":"received"}`**，無論 email 是新的、已有未完成申請、冷卻中、或已是後台帳號。差異只在伺服器內部（D4 狀態機），回應位元組與時序無法區分（整合測試比對位元組；耗時差異以「請求內只做入庫＋寫 outbox、不寄信」消除）。
- 驗證失敗（欄位格式、同意版本、Turnstile）回 422／400——只取決於輸入，不洩漏既有資料。限流超限回 429＋`Retry-After`。
- body 上限 16 KiB；未知欄位忽略；email 以 `auth.NormalizeEmail` 正規化；`shop_name` 去控制字元、≤ 100；`phone` 僅允許數字與 `+-() ` ≤ 20；所有會進通知信主旨／內文的欄位拒絕 `\r\n`（沿用 `notify.ErrHeaderInjection` 的立場）。

### D3：濫用防護（多層，任一層失效不致全面失守）
1. **Turnstile**：`internal/turnstile.Verifier` 介面，正式實作呼叫 Cloudflare siteverify（逾時 3 秒、帶 `remoteip`＝`clientIP(r)`）；`APP_ENV=production` 且未設定 `TURNSTILE_SECRET_KEY` ＝ 拒絕啟動（fail closed，與 `validateSecrets` 一致）；dev／test 使用 Cloudflare 文件載明的測試金鑰，**測試用 httptest 替身，不連網**。siteverify 本身故障（網路／5xx）→ 503，**不放行**（與限流器的 fail open 相反：這是唯一的機器人閘門）。
2. **限流**（沿用 `newAuthRateLimitMW`）：per-IP broad（預設 20／10 分鐘）＋ per-IP tight（5／10 分鐘）＋ **新增 `emailRule`**（只依 normalized email、不含 IP，預設 3／24 小時，擋「換 IP 轟炸同一信箱」）＋ 全站每日上限 `APPLICATION_DAILY_CAP`（預設 200／日）。**Redis 故障時限流 fail open**（既有行為），所以另有不依賴 Redis 的 DB 後盾：D4 的部分唯一索引使同一 email 至多一筆未完成申請；寄驗證信以 `shop_application_tokens.created_at` 為準（同一申請 10 分鐘內只簽一枚新 token）；日上限以近 24 小時 `COUNT(*)` 降級檢查。
3. **Honeypot**＋**填表過快旗標**（前端送出載入至送出的秒數；< 3 秒不拒絕，只在審核畫面標示）。
4. `verify` 端點以 IP 限流，無效／過期／已用一律同一個 404。

### D4：資料模型與狀態機
`shop_applications`：`id`、`status`（CHECK：`pending_verification｜pending_review｜approved｜rejected｜expired｜withdrawn`）、`email`（normalized）、`email_verified_at`、`contact_name`、`phone`、`shop_name`、`business_summary`、`consent_version`、`consented_at`、`risk_flags`（jsonb：honeypot／填表過快／email 已有後台帳號／手動確認等，不含 PII）、`submitted_ip_hmac`（`HMAC(key, ip)`，**不存原始 IP**）、`reviewed_by`、`reviewed_at`、`decision_reason`（拒絕時**對申請者可見**）、`internal_note`（僅內部）、`provisioned_shop_id`（nullable、UNIQUE，**刻意不叫 `shop_id`**，避免 `enttenancy_internal_test` 要求把這張表分類成租戶表）、`purge_after`、`created_at`、`updated_at`。
部分唯一索引：`UNIQUE(email) WHERE status IN ('pending_verification','pending_review')`。
`shop_application_tokens`：`id`、`application_id`（FK）、`token_hash`（SHA-256，unique）、`expires_at`（24h）、`used_at`、`created_at`；原子消費沿用 `admin_credential_tokens` 的 `UPDATE … WHERE used_at IS NULL AND expires_at > now()` ＋影響列數；簽新 token 時舊的失效。
`shop_application_events`（稽核）：`id`、`application_id`、`actor_user_id`（nullable）、`event`（`submitted｜verification_sent｜verified｜verified_manually｜approved｜approve_failed｜rejected｜note｜message_marked_sent｜expired｜purged`）、`detail`（jsonb，**禁止放 email／姓名／電話**）、`created_at`。事件列在 `purged` 後仍保留（不含 PII）。
`shop_application_messages`（通知 outbox，D5）：`id`、`application_id`、`kind`、`recipient_email`、`subject`、`body_text`、`delivery`（`manual｜email`）、`status`（`pending_manual｜sent｜failed`）、`error_reason`、`created_at`、`sent_at`、`marked_by`。
狀態轉移全部以**條件式 UPDATE ＋影響列數**仲裁，不做應用層 read-then-write。
重複與重新申請：同 email 已有未完成申請 → 回 202，內部：仍 `pending_verification` 且距上枚 token ≥ 10 分鐘則重寄，否則不動。被拒絕者在 `APPLICATION_REAPPLY_COOLDOWN`（預設 30 天，Q6）內再申請 → 回 202 但不建新列。`pending_verification` 超過 7 天 → `expired`（由 purge 指令處理）。

### D5：通知抽象與人工後備
`application.Notifier`：`Notify(ctx, Notification) (Delivery, error)`，`Delivery{Mode: "manual"|"email"}`——與 `account.LinkDelivery` 同一哲學：**呼叫端看 `Mode`，不看欄位在不在**。
- `OutboxNotifier`（預設）：寫一列 `shop_application_messages`（`manual`/`pending_manual`），後台視圖顯示待人工寄出的訊息（主旨、內文、收件人、複製鈕、「標記為已寄出」）。**驗證信也走這裡**：沒有 SMTP 時申請者收不到驗證連結，所以操作員看得到驗證連結並以其他管道轉交，或選擇「人工確認聯絡方式」（`POST …/{id}/verify-manually`，記稽核事件與風險旗標 `manually_verified`）。這是**刻意的後備**，也是 Q3。
- `SMTPNotifier`：包 `notify.Sender`（`Message{To,Subject,TextBody}`；標頭注入檢查已在 `Message.Encode`）。僅在 `cfg.SMTP.Enabled()` 時啟用；寄送在 worker 非同步（沿用 `notify.Service` 的有界佇列思路：不拖慢請求、滿了丟棄並留 `failed` 列）。
- 訊息種類：`verify_email`、`approved`（下一步說明，**不含**一次性連結）、`rejected`（含 `decision_reason`）；給操作員的「新申請」提示在人工模式＝後台徽章數字。
- **owner 密碼設定連結不進 outbox**（持有即可使用的憑證，不該落在長期儲存的 `body_text`）：核准回應一次性回傳（`delivery:"manual"`，同 `shop-provisioning`），操作員當場複製；日後 `LinkDelivery` 換成 email 實作時由它負責。

### D6：Email 驗證（沒有帳號、沒有登入）
32 bytes `crypto/rand` base64url token，DB 只存 SHA-256，TTL 24h，連結 `{WEBSITE_URL}/apply/verify#<token>`（fragment，不進日誌與 Referer）；頁面以 `POST /api/v1/public/shop-applications/verify {token}` 消費。成功：`pending_verification → pending_review`、記稽核。**驗證前審核端看得到申請（標示未驗證）但不可核准**（核准條件 `email_verified_at IS NOT NULL`）。驗證信只含店名與連結。

### D7：託管、安全標頭
建議：靜態輸出部署到 Cloudflare（Workers static assets／Pages，與店面同一種），不經 VPS 與 Tunnel；`CORS_ALLOWED_ORIGINS` 加官網 origin。安全標頭放 `web/public/_headers`：`Content-Security-Policy`（`default-src 'self'`；`script-src 'self' https://challenges.cloudflare.com`；`frame-src https://challenges.cloudflare.com <demo origin>`；`connect-src 'self' <api origin>`；`img-src 'self' data:`；`frame-ancestors 'none'`；`base-uri 'none'`；`form-action 'self'`；Next 靜態輸出的內嵌 script 以 hash 處理，**不得放 `'unsafe-inline'` script**，需以建置產物驗證）、`Strict-Transport-Security: max-age=31536000; includeSubDomains`、`X-Content-Type-Options: nosniff`、`Referrer-Policy: strict-origin-when-cross-origin`、`Permissions-Policy`（全關）。若改採 Docker＋nginx（沿用 `image-web.yml`），同一組標頭寫在 nginx 設定。**本 change 不改 Cloudflare 與部署設定本身**，只提供檔案與 runbook（`docs/deploy/platform-website-runbook.md`）。

### D8：個資與保存期限（台灣個資法視角，**需法律審閱**）
蒐集：email、姓名、電話（選填）、店名、業務摘要、同意版本與時間、來源 IP 的 HMAC。目的僅限開店審核與聯絡。保存：①`pending_verification` 未驗證 7 天後整列刪除；②`rejected`／`expired`／`withdrawn` 的 PII 於 `APPLICATION_REJECTED_RETENTION`（預設 90 天，且不早於再申請冷卻結束）後清空；③`approved`：PII 於開店後 `APPLICATION_APPROVED_RETENTION`（預設 1 年，Q5）清空，owner 的 email 已是 `users` 資料，由既有帳號生命週期管。事件與訊息列不含 PII 或隨申請清空。實作：`api/cmd/purge-applications`（同 `cmd/sanitize-content`：**預設 dry run**，`-apply` 才寫入，冪等，只印 id），由操作員排程（repo 內沒有排程器）；另有「依 email 刪除」指令供個資請求。`/privacy` 載明蒐集項目、目的、保存期間、權利與聯絡方式——文字為**佔位**，上線前須人工審閱。日誌只記 `application_id`／事件類別，不記 email／姓名／電話（以 PII 整合測試把 slog 導進 buffer 斷言，同 `notify`）。

### D9：核准＝一次性開店，以及 `web/` 的處理
**核准**：`POST /admin/shop-applications/{id}/approve`（`shop_application.review`，並要求與 `POST /admin/shop-provisioning` 相同的 `shop.create`＋`account.manage`，**不降低任何權限要求**）。body 可覆寫 `shop_name`、`status`（預設 `2` 審核中，店面不可達；上線時再改 1）、須確認 `confirm_existing_account`（見下）。不帶 `theme_id`、`domain`（headless），`owner_email` ＝ 申請 email。
交易邊界：現行 `ProvisionShop` 自開交易，無法與申請列更新同交易。做法（同 `CreateShop`／`CreateShopTx` 的先例）：拆出 `ProvisionShopTx(ctx, tx, in)` 與 `AfterCommit(ctx, res, issued)`，`ProvisionShop` 改為呼叫兩者、**行為逐位元組不變**（既有整合測試即回歸）。核准在**單一交易**內：條件式 UPDATE `pending_review → approved`（且 `email_verified_at IS NOT NULL`；0 列＝409）→ `ProvisionShopTx` → 寫 `provisioned_shop_id`、稽核事件 → commit → `AfterCommit`。任何失敗整筆回滾，申請列保持 `pending_review`，並在交易外追記 `approve_failed` 事件。
**owner 已有後台帳號**：`ProvisionShop` 只加入成員資格、不動密碼、無連結；審核畫面在核准前以 `risk_flags.email_has_account` 警示並要求 `confirm_existing_account:true`，否則 409。
**`web/`**：官網需要靜態輸出，租戶渲染需要動態 Host 渲染，二者不能在同一個 Next build 並存。選項：(A) 本 change 在 `web/` 內移除租戶渲染（`app/[[...path]]`、`app/preview`、`themes/`、`lib/api.ts`、`sanitize.ts`、`richtext-allowlist.json`、`scripts/gen-types.mjs`），並同步移除 e2e 中依賴 `WEB` 的斷言與 CI 的 gen:types 檢查；API 與 admin 的 CMS 端點／視圖不動，等 `retire-tenant-rendering`。(B) 先 `git mv web web-legacy`（保留可建置）、官網另放 `web/`，`web-legacy` 在 `retire-tenant-rendering` 刪除；CI 與 image workflow 暫時雙份。**建議 A**：目前沒有商家使用 `web/` 渲染（project.md「目前零消費者」），B 只是把死碼多養一段時間。代價：e2e ⑤⑥⑥b⑦ 要在本 change 內移除，而 ①–④ 仍打 admin 的 CMS 視圖，留到 `retire-tenant-rendering`；`make web-dev` 不再有租戶店面可看（API 的 Host 解析仍在）。Q2。

### D10：稽核與可觀測
每個狀態轉移與操作員動作寫 `shop_application_events`（含 `actor_user_id`）；審核畫面顯示時間線。`/admin/shop-applications*` 在平台後台＋Access 之後；沒有匯出鍵（理由同 `member-directory`：需先有讀取稽核）。

### D11：官網頁面與 live demo
頁面：`/`（價值主張、三步驟、CTA）、`/how-it-works`（申請→審核→開店→AI 設計→上線；誠實寫出審核為人工、上線步驟有人工）、`/demo`、`/pricing`（佔位）、`/terms`、`/privacy`（佔位）、`/apply`、`/apply/verify`、`/apply/received`（告知「若信箱收不到，我們會以人工方式聯絡」）。
live demo：一個**真的 merchant-template 部署**（以 `scripts/new-merchant.mjs` 建、指向專用 demo shop 與其 publishable key），`/demo` 以連結＋ `sandbox` iframe 呈現。需決定結帳是否啟用（建議 `mock` 付款或停用並標示「示範店」）、資料重置方式、demo 站是否允許被內嵌（`frame-ancestors`，依賴範本 CSP）——Q4。**不使用** ecommerce 自己 `web/` 渲染 demo。

### D12：SEO 基礎
每頁獨立 `<title>`／description／canonical／`og:*`／`twitter:*`；`<html lang="zh-Hant">`；`app/sitemap.ts` 與 `app/robots.ts`（`force-static`）；`/apply/verify`、`/apply/received` `noindex`；Organization JSON-LD（無個資）；語意化標題層級、圖片 `alt`；Turnstile 只在 `/apply` 載入。

## Risks / Trade-offs

- 審核佇列被灌爆：Turnstile＋限流＋日上限＋驗證後才可核准；`risk_flags` 供排序。
- 無 SMTP 時驗證信箱形同「操作員代轉」，體驗差、有人力負擔（Q3）。Gmail 免費額度（約 100 封／日）與訂單信共用，申請量上來前要換寄信服務。
- 限流 fail open：Redis 故障時 DB 後盾涵蓋同 email 與日上限，不涵蓋多 email 灌入；Turnstile 是主要防線。
- 方案 A 讓 `web/` 租戶渲染早於 `retire-tenant-rendering` 消失，repo 暫時處於「API 有 render 端點、沒有前端消費」的狀態——本來就沒有消費者。
- `ProvisionShop` 拆分改動核心服務，回歸由既有整合測試承擔。
- 個資法遵循需律師確認；本 change 只實作機制。

## Migration Plan

純新增表與端點，無資料遷移。down migration 直接 DROP 四表。部署順序：API（migration、設定、`make seed` 新權限）→ 後台 → 官網。回滾：把官網 origin 從 `CORS_ALLOWED_ORIGINS` 拿掉即讓表單失效；表保留。

## Open Questions（**需要使用者回答，實作前必須有答案**）

1. **Q1 技術路線**：Next.js 靜態輸出（建議）或零建置靜態站？
2. **Q2 `web/` 處理**：A（本 change 內移除租戶渲染，建議）或 B（`web-legacy`）？
3. **Q3 寄信**：上線時是否有寄信服務？沒有的話接受「操作員人工轉交驗證連結／人工確認聯絡方式」作為正式流程嗎？（建議上線前接上寄信服務，並先評估 Gmail 額度。）
4. **Q4 live demo**：用哪個 demo shop？結帳啟用與否（mock 付款？）？網域、是否允許被官網內嵌？
5. **Q5 保存期限**：已拒絕 90 天、已核准 1 年是否可接受？有無法務／會計要求？
6. **Q6 重新申請冷卻**：被拒絕後多久可再申請（建議 30 天）？拒絕理由是否對申請者可見（建議可見，由操作員撰寫）？
7. **Q7 申請欄位**：是否需要網站類型／預期商品數／現有網址／統編？（越多＝越多個資。）
8. **Q8 審核 SLA 與通知**：新申請如何通知操作員（後台徽章、email、LINE）？未處理申請是否自動過期？
9. **Q9 網域與託管**：官網網域、Cloudflare Workers／Pages 或現有 Docker？Turnstile 帳號與 site key 由誰建立？
10. **Q10 核准後預設**：預設 `status` 2 是否合適？核准是否同時建立 publishable key（`provision` 目前不建）？
11. **Q11 條款／隱私**：誰提供正式文字與版本號？在那之前是否只上線佔位並標示「測試中」？
