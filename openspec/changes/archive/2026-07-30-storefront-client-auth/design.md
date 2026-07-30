## Context

現行系統有三條「請求 → 商家」的宣告通道，全部誕生於「前端由平台自己託管」的假設：

```
/api/v1/shop/*      TenantMW:  X-Site-Domain（+ X-Site-Path）  優先 ──▶ Resolver ──▶ shop
                               否則 Host
/api/v1/render/*    RenderHandler 自行讀:  X-Site-Domain 優先，否則 Host
CORS                router 最外層:  CORS_ALLOWED_ORIGINS 環境變數 → 靜態 map
```

`X-Site-Domain` 的原始定位是「原生 APP 通道」（phase1 design D5 第 6 點），其安全論證原文為：「不新增安全風險：租戶隔離依資料層 scope 與 token `aud` 綁定，本就不依 Host 可信度」。這個論證在**讀取**面成立，在**未認證的寫入**面不成立——`guest-checkout` 將引入未認證卻會寫入的端點，屆時「任意客戶端可宣告任意租戶」就等於「任意客戶端可對任意商家寫入」。

即將接入的 ONDO 溫度花室是第一個不由平台託管的前端：vanilla JS 靜態站、Cloudflare Pages、瀏覽器直接跨源呼叫 API。它使兩件事同時暴露：靜態站的 `Host` 是 API 自己的網域（協定上無法表達「我是 A 商家」，必然需要某種客戶端宣告），而它的 origin 無法在不改環境變數並重啟 API 的前提下被允許跨源。

倉庫內消費端盤點（破例前提，見 D12）：`web` 僅呼叫 `/api/v1/render/{page,preview}`；`admin` 僅呼叫 `/api/v1/admin/*`；`e2e` 僅於渲染端點使用 `X-Site-Domain`。`bouquet/public/app.js` 全樹零 API 呼叫。

約束：PostgreSQL 16；ent + versioned migrations；Redis 為 cache-only，故障時 degrade 而非 down；`internal/events` 為唯一的快取失效路徑（handler 不得直呼 Redis）；公開 API 一律 `/api/v1`，design D13 規定 v1 僅接受 additive 變更。

## Goals / Non-Goals

**Goals:**

- 讓不由平台託管的前端（靜態站、SPA、原生 APP）以一個**登記過、可撤銷、可歸因**的物件宣告其代表的商家。
- 讓瀏覽器跨源存取以每商家粒度授予，且登記變更即時生效，不需改環境變數或重啟 API。
- 關閉與該物件等效但未登記的通道，使「撤銷」具有實際效力。
- 支援金鑰輪替：新舊金鑰並存的過渡期（先發新金鑰 → 前端更新部署 → 撤銷舊金鑰）。
- 不破壞 `/web` SSR 與渲染 bundle 的既有解析路徑。

**Non-Goals:**

- 不把 publishable key 設計成認證機制：不簽章、不雜湊儲存、不設有效期、不參與任何授權判定。
- 不做 test／live 環境分離、不做 wildcard origin、不新增速率限制維度。
- 不引入商家後端用的機密金鑰（`sk_` 類）或 server-to-server 商家 API——本變更只處理瀏覽器（與同類不受信任的）客戶端。
- 不引入 CSRF 防護：本 API 無 cookie 認證，憑證一律走 `Authorization: Bearer`，跨源強制請求無法取得環境憑證。
- 不變更任何既有端點的路徑、請求／回應形狀、狀態碼或授權規則。

## Decisions

### D1. publishable key 是識別碼，不是憑證

這是本設計最重要的概念區分。publishable key 的作用是宣告「這個前端代表哪家商家」，**不證明任何權限**。由此推出的每一個性質都是刻意的：

- **公開**：它會出現在前端 JS 原始碼裡，任何人都能讀到。這不是缺陷，是它的使用方式。
- **明文儲存並建索引**：不像 refresh token 需要雜湊——雜湊儲存的目的是「資料庫外洩時攻擊者得不到可用憑證」，而 publishable key 本來就公開，雜湊只會讓解析多一道無意義的成本並使後台無法顯示金鑰。
- **洩漏不構成資安事件**：無需輪替、無需通知、無需事故處理。
- **只走 header（`X-Publishable-Key`），不接受 query 參數**：query 參數會進入 access log 與 Referer，形成「它像是秘密」的錯覺，並誘使日後有人在它上面加授權語意。

實際的存取控制仍由既有機制承擔：會員 JWT（`aud=shop:{id}`）、RBAC、CORS、速率限制、以及端點自身的授權規則。比照 Stripe `pk_live_...` 與 Supabase anon key。

**替代方案：把它設計成 API key（雜湊儲存、視為憑證、以其存在與否決定放行）。** 否決。前端 JS 無法保有秘密，任何「放在瀏覽器裡的憑證」都只是把公開值叫成憑證。真正的危害不在名稱，而在後續：一旦它被當成憑證，就會有人在它上面掛授權判定（例如「帶著 A 的金鑰即可讀 A 的訂單」），那才是真正的漏洞。spec 因此以「MUST NOT 作為授權依據」把這個不變式固定下來。

**代價**：需要持續對抗「它看起來像密碼」的直覺。以命名（publishable）、文件、程式碼註解與 spec 需求三處同時記錄。

### D2. 本變更買到什麼、買不到什麼

**買到：**

1. **瀏覽器託管的前端得以正確宣告租戶。** 靜態站送出的 `Host` 是 API 的網域，協定上沒有任何方式表達「我是 A 商家」；某種客戶端宣告是必然的，問題只在於它是不是登記過的物件。
2. **每商家 CORS。** 登記即生效，不需環境變數與重啟（見 D3）。
3. **可撤銷性與歸因。** 金鑰指名的是「哪一個前端部署」，可個別撤銷（濫用、商家解約、金鑰輪替、舊版 APP 下架），並可作為日誌與稽核維度。

**買不到：**

publishable key 是公開的——攻擊者可從 B 商家的網站原始碼直接讀出 B 的金鑰，因而仍能對 B 的公開端點發出請求。**「偽造宣告替別家商家建立訂單」並不會因為引入金鑰而變成不可能**，而且它本來也不可能被阻止：商家的前台本來就對外公開，任何人造訪 B 的網站即可做完全相同的事。針對這類濫用的真正手段是速率限制、`guest-checkout` 自身的防濫用規則、以及撤銷；金鑰的貢獻是讓這些手段**有一個穩定、可指名的作用對象**，而不是面對一群無法區分的匿名宣告。

明文寫下這條邊界是設計的一部分：若讓「引入金鑰＝解決了偽造問題」的印象留存，下一個變更就會把授權掛在它上面。

### D3. per-shop CORS 的關鍵限制：preflight 帶不了自訂 header

瀏覽器的 `OPTIONS` preflight 只送 `Origin` 與 `Access-Control-Request-Method` / `Access-Control-Request-Headers`，**不會送 `X-Publishable-Key`**（自訂 header 在 preflight 階段只以「名稱」出現在 `Access-Control-Request-Headers` 裡，沒有值）。因此「per-shop CORS」在 preflight 階段根本沒有 shop 上下文可用。

**解法**：已登記的 allowed origins 本身就是一張 origin → shop 的索引，preflight 只需判斷「此 origin 是否登記於任一商家」即可決定放行；真正的租戶解析仍由 publishable key 在後續的實際請求上完成。CORS 的允許集合因此是 `平台清單（環境變數） ∪ 全部已登記的商家 origin`。

**代價（明確記錄）**：允許集合是平台級的聯集，不是每商家的。已登記於 A 的 origin，其 JS 也能跨源呼叫並讀取 B 的**公開**端點回應（只要它拿得到 B 的公開金鑰——它拿得到）。這不構成越權：B 的公開端點對任何人本就公開，而需要授權的端點需要 B 的會員 JWT，該 origin 並沒有。

**已評估並否決的收緊方案**：在實際請求上（此時金鑰在）改以「金鑰所屬商家的 origin 集合」計算 `Access-Control-Allow-Origin`。preflight 仍只能做聯集判斷，因此攻擊者只需讓請求落在 simple request 範圍即可完全避開這道收緊；實益有限而複雜度實在。

**同時否決：把 origin 檢查升級為請求層授權**（「帶金鑰時 `Origin` 必須屬於該金鑰的商家，否則 403」）。`Origin` 只有在瀏覽器裡才不可偽造，而瀏覽器內的攻擊本就被 CORS 擋住（讀不到回應）；非瀏覽器客戶端只要不送 `Origin` 就繞過了。它換來的是一種假的安全感，外加一個新的故障模式（金鑰登記了但 origin 忘了登記 → 全部 403）。CORS 是傳輸層的瀏覽器政策，不是本 API 的授權層——這條界線不模糊化。

### D4. 獨立資料表，撤銷為軟刪除

`publishable_keys(id, shop_id, key UNIQUE, label, created_at, revoked_at)`。

**替代方案：`shops` 的一個欄位。** 否決：金鑰輪替需要新舊並存的過渡期（先發新金鑰 → 前端更新部署完成 → 撤銷舊金鑰），一店一欄做不到；輪替只能變成「改欄位那一刻起舊前端全掛」。

**撤銷為軟刪除（`revoked_at`）而非 DELETE**：`key` 為全域唯一，軟刪除確保已撤銷的字串不會因日後的隨機碰撞或人工重建而復活，同時保留「這把金鑰曾屬於誰」的稽核答案（濫用調查時這正是要問的問題）。已撤銷金鑰的解析一律失敗，與不存在的金鑰同構。

`label` 供人辨識部署（`ondo-prod`、`ondo-preview`），純標註、不參與任何判定。

### D5. allowed origins 掛在 shop，不掛在 key

**替代方案：掛在 key 上**（一把金鑰＝一個部署，origin 是該部署的屬性）。語意上更貼，但輪替期間同一組 origin 會同時出現在新舊兩把金鑰上，全域 `UNIQUE(origin)` 因此不可行，只能退為 `UNIQUE(shop_id, origin)`，preflight 的 origin 反查隨之可能多筆命中，而且每次輪替都要把 origin 清單複製一份。

**選擇**：shop 層，全域 `UNIQUE(origin)`。「這家商家的前端住在哪裡」與「它有幾把金鑰」正交；輪替不必動 origin。

**全域唯一的代價**：平台共用的 origin 無法登記給任一商家（例如未來平台代管的多租戶前端 SPA，一個 origin 服務所有商家）。這類 origin 屬於平台自己的前端，留在 `CORS_ALLOWED_ORIGINS` 環境變數——該清單刻意保留不動，`admin` SPA 與 `web` dev 正是它的既有內容。界線因此清楚且可解釋：**平台自己的前端在環境變數，商家自己的前端在資料表。**

### D6. `X-Site-Domain` 的處置（本變更的核心決策）

**決定：於 `/api/v1/shop/*` 完全移除 `X-Site-Domain` 與 `X-Site-Path`；於 `/api/v1/render/page` 保留 `X-Site-Domain`（並額外接受 `X-Publishable-Key`）。**

考慮過的四個選項與否決理由：

**選項 2：僅在無金鑰時生效。** 最誘人，也最沒有意義。撤銷一把金鑰之後，該前端只要改送 `X-Site-Domain` 就能以同一商家身分繼續呼叫——一個可被任意繞過的撤銷機制不是撤銷機制。本變更的全部安全效益（D2 的第 3 點）建立在「登記過的通道是唯一的通道」之上。

**選項 4：標記 deprecated 並設過渡期。** 同樣的問題：過渡期內撤銷無效。而過渡期唯一可能的受益者是既有外部消費者，而它不存在（D12）。為一個空集合支付安全代價。

**選項 3：要求與金鑰一致，否則拒絕。** 此時 header 不承載任何資訊：要嘛與金鑰一致（多餘），要嘛不一致（被拒）。純粹的實作與文件成本，零收益。

**選項 1：完全移除，包含渲染端點。** 這是唯一有實質理由的競爭方案，但它在渲染路徑上做不到：`/web` SSR 呼叫渲染 bundle 時，API 看到的 `Host` 是 API 自己的網域，訪客實際造訪的網域只能由 SSR 以某種形式中繼。改成 query 參數只是換個名字，偽造面完全相同。給 SSR 一把 publishable key 也不可行——SSR 是多租戶的，它服務所有商家，必須先由訪客 Host 決定商家才知道要用哪一把金鑰，等於在同一個值上多繞一層並多一次查詢。

**保留於渲染端點的正當性**：`/api/v1/render/page` 回傳的是**已發佈的公開內容**，偽造所得等同直接造訪對方網域，無任何額外取得；它不寫入、不簽發 token、不接受認證身分（preview 走獨立的短期簽章 token，與此 header 無關）。因此該 header 的偽造性在此處無害。

**代價**：同一個 header 在系統中從此有了「僅限渲染通道」的窄語意，一個讀過舊碼的人可能誤以為它在別處也有效。以三處明文記錄對沖：multi-tenancy spec 的需求本文、`tenantmw.go` 與 `render.go` 的註解、以及本決定。

**原生 APP 通道的處置**：改用 publishable key。APP 與靜態站在威脅模型上是同一類客戶端——不由平台託管、宣告不可驗證。金鑰對 APP 嚴格更好：可個別撤銷（舊版 APP 下架）、可歸因、且不必把商家網域寫死在 APP 二進位檔裡（換網域不必發版）。原「APP 通道」的能力沒有減少，只是換了一個登記過的載體。

### D7. 未帶金鑰的請求回退 `Host`，不拒絕

解析順序：`X-Publishable-Key` 有值 → 以金鑰解析（金鑰無效／已撤銷即失敗，**不回退**）；無金鑰 → `Host` 解析（路徑取 `/`，即該站的預設商家）。

**不採「無金鑰一律拒絕」**：會使渲染管線、`/web` SSR、`e2e` 與全部既有整合測試改寫，換得零安全增益。回退不構成新的偽造面：在瀏覽器裡 `Host` 由瀏覽器依 URL 設定，前端 JS 無法偽造；非瀏覽器客戶端能偽造 `Host`，但它同樣能讀取並使用任何公開金鑰（D2），故不增加攻擊者能力。

**金鑰有值時不做 Host 一致性檢查**：理由同 D6 選項 3。金鑰勝出，句號。

**`X-Site-Path` 的消失不留替代品**：它原本讓客戶端以 header 指定路徑前綴，供「一個網域多家商家」的消歧。帶金鑰的客戶端直接解析到商家，本就不需要消歧；不帶金鑰的客戶端落到該站預設商家。以路徑前綴共用網域的商家若要接自帶前端，**必須**用金鑰——這正是我們要的方向。

### D8. 金鑰格式：`pk_` + 32 字元小寫 base32，不做 test／live 分離

格式為 `pk_` 加上 `crypto/rand` 20 bytes（160 bits）的小寫 base32（RFC 4648 字母表 `a-z2-7`，無 padding）編碼，共 35 字元。

- **熵不是為了保密**（金鑰是公開的），而是為了**不可枚舉**：避免可猜測的金鑰讓任何人列舉平台上有哪些商家。
- **隨機段刻意排除 `_`**：任何未來的環境中綴（`pk_test_`）都能以 `_` 為界被無歧義解析。這是「現在不做 test／live 分離」唯一需要現在付的成本，而它接近於零。

**不做 test／live 分離的理由**：平台目前不存在可分離的東西——單一資料庫、單一商家資料集，mock 金流是正式佈線的參考實作而非沙盒。此刻的 `pk_test_` 與 `pk_live_` 行為完全相同，是一個承諾了自己做不到的隔離的標籤，比沒有標籤更危險（商家會拿「測試金鑰」對正式資料做測試）。真正需要它的時機是出現「沙盒資料集」或「不對真實金流收費的模式」，屆時該決策的主體是那個模式本身，金鑰前綴只是它的附屬品。

### D9. 速率限制維度不變

金鑰**不**參與速率限制的鍵值。既有規則以 `shop_id` + IP（+ email）為界，而金鑰與商家是多對一——per-key 配額等於讓商家自助擴額（多產生幾把金鑰即可），是一個現成的繞過。金鑰解析完成後 `tenant.ShopID` 照常設定，限流中介層排在 TenantMW 之後，既有 shop-scoped 規則因此原樣運作，無需改動。

**另評估並否決：對「無效金鑰」的請求限流。** 枚舉金鑰的所得僅是「某商家存在」，與該商家的公開網域等價；為此新增規則不划算。

### D10. 快取與失效

比照既有 `route:{host}` 模式：

- `pkey:{key}` → `{shop_id, shop_status}`，TTL 5 分鐘。
- `corsorigin:{origin}` → 已登記／未登記，TTL 5 分鐘，**含否定快取**——未登記的 origin 會被每一個 preflight 查一次，否定快取避免以未知 origin 直接打 DB。

失效一律走 `internal/events`（CLAUDE.md 的硬規則）：新增 `StorefrontClientChanged{ShopID, Keys, Origins}`，由金鑰／origin 的建立與撤銷發布。商家 `status` 變更沿用既有 `SiteMappingChanged`（platform-shop-crud 已規定 status 變更必須即時失效路由快取）——新增的訂閱者收到該事件時，查出該商家全部金鑰並刪除其快取條目，使停用商家的金鑰與其網域同時失效。

Redis 故障一律 degrade 到 DB（既有慣例）。金鑰與 origin 的查詢皆為唯一索引上的 point lookup。

### D11. RBAC：金鑰是商家層，origin 是平台層

- **publishable key → 商家層**：新增 `publishable_key.view` / `publishable_key.create` / `publishable_key.revoke`，授予 `merchant_owner`。金鑰的命名空間不是競爭資源（隨機產生、不可能與他人衝突），輪替是商家自己的維運節奏，需要自助。不授予 `editor`：金鑰是整合／維運面的動作，不是內容編輯。
- **allowed origin → 平台層**：新增 `shop.manage_client_origins`，僅平台角色持有（`super_admin` 以 `*` 自動取得）。origin 是**競爭的全域命名空間**，與 `sites.domain` 綁定同性質——後者正是平台層的 `shop.manage_domains`。在全域 `UNIQUE(origin)`（D5）之下，允許商家自助登記等於允許先搶先贏地佔用他人的 origin，並讓任一商家能把任意網站加進平台的 CORS 允許集合。平台層管理一次消除這兩者。

**代價**：自帶前端的接入不是全自助，需要平台介入一次登記 origin。可接受：綁網域本來就需要平台介入（`shop.manage_domains`），origin 登記與網域綁定是同一次接入作業的兩面。

### D12. 破壞性 v1 變更的正當性與範圍

design D13 規定 v1 僅接受 additive 變更。本變更破例，破壞性面向**只有一項**：`/api/v1/shop/*` 不再讀 `X-Site-Domain` 與 `X-Site-Path`。

**正當性**：v1 的消費端僅有本 monorepo 內與 API 同步部署的 `admin`、`web`、`e2e`，且三者皆不於前台群組使用該 header（`web` 只呼叫渲染端點，`admin` 只呼叫 `/admin/*`，`e2e` 的 `X-Site-Domain` 用在渲染端點）。外部消費者 ONDO 尚未接入。破例成本因此為零。姊妹 change `member-identity-per-shop` 以同一理由破例一次；本變更是**同一個窗口的最後一次使用**——ONDO 接入後此條件即消失，屆時同等變更需走 v2 或 additive 遷移路徑。

**範圍限制**：破例僅及於上述解析輸入。`/api/v1/shop/*` 全部端點的路徑、請求／回應形狀、狀態碼與授權規則 MUST 不變；渲染 bundle 端點的行為 MUST 完全不變（`web` 與 `e2e` 的既有呼叫一行都不必改）。

## Risks / Trade-offs

- **[publishable key 被誤認為安全邊界，日後有人在其上掛授權判定]** → D1／D2 明文記錄「買到什麼、買不到什麼」；spec 以「MUST NOT 作為授權依據、MUST NOT 影響任何授權判定」的需求把不變式固定下來；解析點程式碼註解同步記錄。

- **[移除 `X-Site-Domain` 使既有整合測試中以該 header 指定租戶的用法全數失效]** → 使用真實 `TenantMW` 的測試（cart／order／payment／shipping／points／categories／tenancy）改以 `req.Host` 或金鑰指定租戶；使用假 TenantMW 的測試（authflow／ratelimit）不受影響，但一併檢視確認其模擬的仍是有效通道。

- **[CORS 中介層新增每請求 DB 查詢，成為所有路由的熱路徑]** → 平台清單先行短路（`admin`/`web` 的流量不查 DB）；Redis 快取含否定快取；Redis 故障時 degrade 到 DB 的唯一索引 point lookup。無 `Origin` header 的請求（伺服器對伺服器）完全不進入這條路徑。

- **[origin 全域唯一使平台共用的多租戶前端無法登記]** → 平台自己的前端留在環境變數清單，界線寫入 spec（D5）。若日後真的出現平台代管的多租戶 SPA，它的 origin 屬於平台清單，與本表無關。

- **[撤銷後最長 5 分鐘的快取視窗]** → 撤銷發領域事件即時刪除快取條目，TTL 僅為兜底。且金鑰非憑證，該視窗本身不是安全事件（D1）。

- **[平台層 origin 管理成為接入流程的人工步驟，商家無法自助]** → 與既有網域綁定同屬平台操作，合併於同一次接入作業（D11）。若日後接入量大到成為瓶頸，可加「商家提出、平台核可」的流程，而不是把全域命名空間的寫入權下放。

- **[trade-off：`X-Site-Domain` 在系統中留下一個窄語意的例外]** → 接受。替代方案（完全移除）需要為 SSR 另造一個等價通道，換到的只是名字不同（D6 選項 1）。

## Migration Plan

單一 versioned migration，**純新增**兩張表，無資料搬移、無既有欄位改動：

1. `CREATE TABLE publishable_keys`（`shop_id` FK → `shops`，`key` UNIQUE，`label`，`created_at`，`revoked_at` NULL）。
2. `CREATE TABLE shop_allowed_origins`（`shop_id` FK → `shops`，`origin` UNIQUE，`created_at`）。

**部署**：無正式環境，隨 `make migrate` 一次套用。既有商家不需登記任何金鑰即可維持現狀（`Host` 回退，D7），因此 migration 與程式部署之間沒有必須協調的順序。

**接入順序（ONDO）**：平台登記 origin → 商家後台產生金鑰 → 前端帶 `X-Publishable-Key` 部署。三步之間無時序耦合（登記 origin 不影響既有流量，產生金鑰在被使用前無作用）。

**Rollback**：`.down.sql` DROP 兩張表。無既有資料依賴，回滾的唯一損失是已登記的金鑰與 origin 本身。程式面回滾則使前台客戶端退回 `Host` 解析——ONDO 尚未接入時無影響。

## Open Questions

- Cloudflare Pages 的 preview deployment 會產生隨機子網域（`<hash>.<project>.pages.dev`）。本變更不支援 wildcard origin，因此 preview 部署無法跨源呼叫正式 API。目前處置：ONDO 以正式 origin 加上少數固定的 preview origin 登記。若實務證明不可行，再評估「每商家 origin pattern」——需連帶決定 pattern 語法、比對成本，以及如何避免 `*.example.com` 誤配 `evil-example.com` 這類經典錯誤。刻意不在本變更中預先設計。
