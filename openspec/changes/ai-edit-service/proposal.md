## Why

平台端的設計歷史（change `design-history`）與 template 端的守門員、唯讀預覽、截圖工具都已完成，但**真正動手改設計的 AI edit service 還不存在**——只有 `admin/docs/AI-EDIT-SERVICE-NOTES.md` 的需求描述與 `docs/design-service-contract.md` 的契約。擁有者要讓每家商家用對話請 AI 重新設計自己的店面與後台（每個畫面都可被 AI 重設計，只要功能接得上），並已決定：AI 服務**只推分支、由人合併**（Q9，不建簽章 publish permit）。

擁有者要求「**先把設計完整寫下來，再實作**」。AI 服務會同時碰到：不可信的輸入（商家對話、商品資料、repo 內容）、可寫入商家 repo 的憑證、會花錢的模型呼叫、會執行 AI 產生之程式碼的預覽主機。每一項在沒有設計時都是事故來源。本 change 只有提案（規格、設計、任務），**不含任何程式碼、帳號、金鑰、部署或付費呼叫**。實作之後另走 `/opsx:apply`，且「擁有者專屬行動」（見 design.md 第 14 節）完成之前不得開工。

## What Changes

**本 change 只有文件。** 設計內容（詳見 design.md）：

- **架構**：以 Claude Managed Agents（雲端沙盒）當「改檔的手」，外面包一個**我們自己的協調器（orchestrator）**當「保管所有憑證、做所有有後果的事」的一方。agent 的沙盒裡**沒有**平台服務金鑰、**沒有**可寫入 GitHub 的權杖、沒有對外網路（除模型與 git proxy）；它只能讀 repo、改設計層檔案、跑本機守門員，並把結果以補丁（patch）交出。
- **信任邊界與威脅模型**：六個區域（商家輸入、agent 沙盒、協調器、可信驗證器、預覽主機與擷取環境、平台）與 16 條具名威脅（提示詞注入、惡意 repo 內容、憑證外洩、預算耗盡、跨店、預覽主機逃逸等）及對策。
- **端到端流程與九態對應**：每一步對應到既有服務契約的端點與狀態；核准後由**人**在 GitHub 以 merge commit 合併，「對帳器」確認合併內容恰等於核准的 `head_sha` 後才向平台回報 `published`（取代契約第 11–14 步「服務自己合併」）。
- **守門員閘門**：agent 在沙盒內自跑；**可信驗證器**再用 base 版本的守門員腳本在乾淨環境重跑（防止分支改寫守門員自己）；平台 CI 是第三道；合併一定要人。
- **預覽部署**：使用 template 的同一個 nginx 映像（嚴格 CSP、唯讀模式、獨立預覽 origin）；列出必須存在的東西與由誰建立。
- **成本模型與預算**：以文件列價（Opus 5.5 $4/$20、Sonnet 5.5 $2/$10 每百萬 token；工作階段執行 $0.08/小時）估算每請求成本；四層上限（單一工作階段的硬性美元預算、每店每日、全域每月、請求次數與時間盒）、總開關與每店開關。
- **失敗與重試、觀測、多租戶隔離、分階段上線（先 bouquet-next）、測試計畫（含對守門員的對抗性提示詞）**。
- **24 項編號決議（AD1–AD24）**，每項有選項、建議、理由與不採用的代價；擁有者於 2026-11-28 表示「全部照建議」，狀態為已採用。
- **擁有者專屬行動清單**：Anthropic 帳號與額度、Cloudflare、GitHub App、法務與服務條款——**不因「全部照建議」而自動採用**，其中會花錢者已標明。

### 與既有文件的差異（需要改文件的地方，列於 tasks）

- `docs/design-service-contract.md` 第 3 節第 11–14 步、第 8 節、`design.md` D17 第 11–12 步：「服務自己合併與打 tag」改為「人合併，對帳器回報」（AD7）。
- 分支名稱：契約與平台預設 `ai/shop-{id}/{request id}`，但 template 的 `guards.yml` 只對 `design/*`、`design/admin-*`、`design/storefront-*` 分支啟動範圍檢查（AD6，改用 `design/<site>-<request-id>`）。
- `DESIGN_APPROVAL_TTL` 預設 1 小時，與「人合併」不相容（AD8，建議 24 小時，上限本來就是 24 小時）。
- template `AI-EDIT-SERVICE-NOTES.md` 流程第 1、5 步（給服務一把能寫入 repo 的權杖、由服務合併）與本設計的「補丁交出、協調器推分支」不同（AD4）。

## Capabilities

### New Capabilities
- `ai-edit-orchestration`：請求的受理、協調器與 agent 工作階段的分工、九態對應、可信驗證、預覽與擷取、人合併後的對帳與回報。
- `ai-edit-isolation`：憑證保管、沙盒網路與權限、提示詞注入處理、租戶隔離、補丁範圍檢查、預覽主機的限制。
- `ai-edit-cost-and-operations`：預算與上限、總開關、稽核與觀測、失敗處理、分階段上線的出口條件。

### Modified Capabilities
無（不改動 `design-history` 等既有 capability 的規格；對 `docs/design-service-contract.md` 的修訂列在 tasks 第 1 組，屬文件修訂，平台程式碼的小幅 additive 調整列在 tasks 第 2 組，**皆待實作階段**）。

## Impact

- **本 change 本身**：只新增 `openspec/changes/ai-edit-service/` 下的文件。
- **後續實作（提案，未做）**：新的協調器程式（建議 `ecommerce/cmd/ai-edit-orchestrator`，獨立進程、獨立祕密、**不連主資料庫**）、可信驗證器與擷取環境、預覽主機、對帳器、Managed Agents 的 agent／environment 定義檔；平台側 additive 小改（核准 TTL 設定、可能的「合併確認」欄位，見 tasks 第 2 組）；template 側（核心）新增「描述你想要的改版」入口屬**人工審查的核心變更**。
- **會花錢／需帳號的事**：Anthropic API 額度（模型 token 與工作階段執行時間）、Cloudflare 預覽網域與（可能的）Workers／Access、GitHub App 與（若要用規則集保護私有 repo）GitHub 付費方案、法務審查。全部列於 design.md 第 14 節，**由擁有者自己動手**。
- **相依**：`design-history`（已實作）、template v0.5.0（守門員、`design-capture.mjs`、預覽模式）、`docs/deploy/sandbox-stack.md`（admin 截圖唯一來源）。
