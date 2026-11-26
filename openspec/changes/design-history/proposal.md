## Why

使用者問：「template 會不會記錄 UI 的每一次重新設計，之後可以追溯？」**誠實的現況**（已對照 `C:/Users/ks/Projects/merchant-template` 與本 repo）：

- git 會保存程式碼差異——這是唯一確實存在的紀錄，但它不是商家看得懂的東西（沒有「他當時說了什麼」「看起來像什麼」「誰按了同意」）。
- AI edit service **還沒蓋**，只存在 `admin/docs/AI-EDIT-SERVICE-NOTES.md` 的需求描述；其中 `Audit` 一條只寫了「每個請求要存：商家、prompt、base／head SHA、diff、守門員報告、預覽網址、誰何時同意」，**沒有任何地方可以存**。
- 平台（ecommerce）**沒有**任何資料表、API 或儲存空間記錄設計請求；沒有截圖、沒有核准紀錄、沒有歷史畫面；template 的後台（`ctx.data`／`ctx.actions`）沒有任何歷史或還原功能。
- 「一鍵還原」今天只是 notes 裡的一句話（「保留前一個 commit，undo 是一次 revert」），沒有 UI、沒有紀錄、沒有檢查。

商家會把店面與後台的外觀交給 AI 改。如果出了事（改壞、改醜、商家說「我沒有同意」、客訴「你們前天改版後我找不到按鈕」），平台必須能回答：**誰、在什麼時候、說了什麼、改了什麼、改完長怎樣、誰核准的、現在是哪一版、怎麼回到某一版**。這份紀錄必須由平台持有（不是由可能被攻破的 AI 服務自己持有），且只能追加。

## What Changes

**本 change 只有提案（規格、設計、任務），不含任何程式碼、migration、OpenAPI 變更、測試或部署。** 實作之後另走 `/opsx:apply`。

- **設計請求紀錄**：每一次經 AI edit service 的重新設計是一筆 `design_request`（商家、請求人、站台〔storefront／admin〕、商家的原話、base／head SHA 與分支、diff 摘要〔檔案與行數，不含全文〕、守門員報告摘要、預覽的存在與到期、前後截圖、狀態流轉、核准者與時間、發佈 tag 與部署識別碼；還原請求另記指向哪一筆）。狀態：`drafting`、`previewing`、`awaiting_approval`、`approved`、`published`、`rejected`、`failed`、`cancelled`、`reverted`。
- **只能追加**：另有 `design_request_events`（每次狀態變更與重要動作一列，每請求內以 SHA-256 雜湊串接，資料庫 trigger 擋 UPDATE／DELETE，僅保存期限清除可通過）。請求列上的「目前狀態」是事件的投影。
- **截圖**：存在**新的私有 R2 bucket**（不是現有的公開影像 bucket——現有 bucket 以自訂網域整桶公開，前綴無法私有，見 design D5），讀取一律由 API 簽發 5 分鐘有效的網址。標準化：桌面 1280 與手機 390、固定的視圖清單、固定資料集（**admin 截圖只從合成的 sandbox 資料集擷取，不碰真實店鋪資料**）、PNG、大小與張數上限、SHA-256。
- **誰寫、誰核准**：新的**設計服務憑證**（每店一把、只存雜湊、90 天輪替、可撤銷）只能建立／更新請求與上傳截圖，**永遠不能核准或標記為核准**；核准是人類（商家 owner）的動作，**綁定到被守門員檢查並預覽過的那個 `head_sha`**（TOCTOU 防線），單次使用、有時效；發佈前服務必須對該 SHA 重跑守門員，平台才接受 `published`。
- **權限節點**：`design.view`、`design.request`、`design.approve`、`design.revert`（商家層；`merchant_owner` 全有、`editor` 只有 `design.view`、`staff` 無，`super_admin` 經 `*` 全有）與平台層 `design_service.manage`（只有 `super_admin`，管理服務憑證）。
- **還原**：商家從歷史挑一個過去已發佈的設計 → 建立**新的** `revert` 請求（絕不改寫歷史）→ 服務產生 revert commit，走同一條守門員、預覽、前後截圖、再核准 → 歷史畫面顯示兩筆的連結。內容儲存的內容不受影響；若目標設計早於該店的「core 相容下限」，拒絕還原。
- **版本預覽（D25）**：歷史列表中每個過去已發佈的設計有「預覽此版本」按鈕：部署一份**用完即丟**的預覽＝該 tag 的設計層＋**目前的 core**，以目前資料由商家自己登入後即時觀看。名詞：既有的核准前預覽稱「**改版預覽**」（屬於進行中的請求）；新的稱「**版本預覽**」（不屬於任何請求）。部署前先做 Q7 的相容檢查（設計不得低於 core 的設計下限），不相容就拒絕並說明原因、不部署。版本預覽**不佔**每站台一筆非終態請求的名額、**不寫**正式歷史事件（只有一張輕量的使用紀錄表與限流）；不拍也不存任何截圖。畫面上的「改成這個版本」只是進入既有的還原請求流程（仍須核准）。**誠實的限制**：即時畫面用來「決定」，不是「當時核准了什麼」的紀錄，歷史紀錄仍是截圖。
- **API**：後台（人）`/api/v1/admin/shops/{shopID}/design/…`（列表、詳情＋ETag、事件、截圖簽名網址、核准、拒絕、取消、請求還原）；服務（機器）`/api/v1/design-service/shops/{shopID}/…`（建立、認領、更新 head、守門員結果、預覽登記、截圖上傳、送審、回報已發佈／失敗、未追蹤變更）。皆 additive；支援 `Idempotency-Key`、分頁、`If-Match`／ETag、限流、一致的錯誤碼。
- **保存與清除**：新指令 `cmd/purge-design-history`（dry run 預設、`-apply`、`-shop`、批次、冪等，與 `purge-content-audit` 同型）；建議保存期：prompt 全文 365 天後遮蔽（保留雜湊）、已發佈請求截圖 365 天、其餘截圖 30 天、請求與事件 730 天。另有 `cmd/design-sweep`（逾時請求轉 `failed`、核准過期、孤兒物件偵測；dry run 預設）。
- **未追蹤變更**：在服務之外直接進 git 的設計變更不在歷史裡——由服務端的對帳工作比對 git 與歷史，上報 `design_untracked_changes`，歷史畫面標示為「未追蹤的變更」。
- **後續（template repo，不在本 change 實作，只列任務）**：後台核心新增 `ctx.data`／`ctx.actions` 函式、核心擁有的前後對照元件、預設設計層畫面（歷史列表、詳情並排截圖與請求原文、還原按鈕與固定警告確認）；依 template 的 `core-change` 流程，新增 `ctx` 函式是**人工審查的核心變更**。
- **服務契約**：以有序清單寫明 AI edit service 使用本 API 時必須遵守的步驟（design D17），服務本身不在範圍。

## Capabilities

### New Capabilities
- `design-history`：設計請求紀錄、狀態機、只能追加的事件、核准綁定 SHA、發佈與失敗、還原請求、未追蹤變更、後台與服務 API。
- `design-screenshots`：截圖的標準（視圖、視口、角色、格式、上限、決定性）、私有儲存與簽名讀取、隱私規則（sandbox 資料集）、提示詞個資遮蔽。
- `design-service-access`：設計服務憑證的生命週期與權限邊界、跨店隔離、核准與發佈的不可偽造條件、濫用情境對應。
- `design-history-retention`：各類資料的保存期限、清除指令的行為、匯出與刪除流程。
- `design-version-preview`：版本預覽（過去設計＋目前 core 的用完即丟預覽）的請求、相容檢查、權限、限流、用量紀錄、TTL、與還原流程的銜接，以及不佔名額、不寫歷史事件的邊界。

### Modified Capabilities
- `rbac`：新增 `design.*` 與 `design_service.manage` 節點及其角色授予（以 ADDED 條目表達，不改既有條目）。

## Impact

- **資料**（皆為提案，本 change 不建立）：新表 `design_requests`、`design_request_events`、`design_screenshots`、`design_service_credentials`、`design_site_state`、`design_untracked_changes`；一個手寫的 append-only trigger（repo 目前**沒有**任何 trigger，這是第一個，design D3 說明理由）；`internal/tenant/enttenancy_internal_test.go` 的 `tenantOwned`／`notTenantOwned` 登記（憑證表因認證時先於租戶解析而列入 `notTenantOwned`，需寫理由）。
- **程式碼**（提案）：新增 `api/internal/design/`（service、state machine、hash chain、purge）、`api/internal/httpapi/design_admin.go`、`design_service.go`、`design_service_auth.go`；`internal/media/store.go` 的 `ObjectStore` 增加 `PresignGet`（additive，`FakeStore`／`NullStore` 同步）；`internal/seed/seed.go`（節點與角色）；`internal/config/design.go`；`cmd/purge-design-history`、`cmd/design-sweep`；`router.go`／`wire.go`／`Makefile`。
- **基礎設施**：第二個 R2 bucket（私有，**不綁自訂網域、不開 r2.dev**），專用 API token（只限該 bucket）；本機 MinIO 增加一個私有 bucket；sandbox 資料集的 API 堆疊（design D12，**需使用者決定**）。
- **API 契約**：預計新增約 24 條路由與數個 schema；`v1-surface.txt` 只新增行；`openapi.yaml` 為 additive。沒有任何既有路由或欄位被修改（唯一碰到既有介面的是內部的 `ObjectStore` Go 介面，不是 HTTP 契約）。
- **版本預覽（提案）**：新表 `design_version_previews`（一般租戶表，無 trigger、無雜湊串）、約 6 條 additive 路由（人 2＋服務 4）、`DESIGN_VERSION_PREVIEW_*` 設定、清掃與清除指令各加一段；template 端見任務 T9–T12。
- **文件**：`docs/design-history.md`、`docs/design-service-contract.md`、`docs/design-history-privacy.md`、`docs/deploy/design-history-operations.md`、`docs/admin-frontend-integration.md` 新章節、`CLAUDE.md`、`docs/PLATFORM-ROADMAP.md`（本 change 已加指標）。
- **template repo（後續任務，只列不做）**：`admin/core/`、`admin/lib/`、`admin/ui/`、`admin/lib/public-api.json`、`admin/docs/DESIGN-LAYER.md`、`admin/docs/AI-EDIT-SERVICE-NOTES.md`、新的截圖擷取腳本（由 `admin/e2e/parity.mjs` 的 capture 泛化而來）、`docs/CORE-CHANGELOG.md`（`Design-layer action:` 行）。
- **相依**：沿用 `product-images-r2`（`ObjectStore`、`FakeStore`、MinIO、處理併發信號量）、`merchant-staff-role`（角色與權限 seed 的相等比對測試）、`headless-content-store`（清除指令、`If-Match`／ETag、限流形狀、稽核表「無 FK」的理由）、`platform-account-provisioning`（token 只存 SHA-256 的模式）。**不相依**任何尚未實作的 change；AI edit service 本身不存在，故本 change 可以先於它實作，並以 e2e 的「假服務」腳本驗證契約。
