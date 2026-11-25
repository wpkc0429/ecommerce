# 平台後台的設計層：操作手冊與契約

本文件同時是「給 AI agent 的操作手冊」與「給人類設計師的契約」。若你是 agent：改任何東西之前，**先完整讀完第 0 到 3 節**，做能滿足需求的最小變更，最後跑第 9 節的檢查。範本（`merchant-template`）的對應文件是 `admin/docs/DESIGN-LAYER.md`；本文件針對**這個**後台（平台後台與共用的 merchant admin，`ecommerce` monorepo 的 `admin/`）改寫，路由、`ctx` 與警告都不同。來歷與逐檔差異見 [DESIGN-LAYER-PROVENANCE.md](DESIGN-LAYER-PROVENANCE.md)。以下指令一律在 `admin/` 目錄下執行。

## 0. 安全：設計工作階段只能看得到 `admin/`（必讀，最優先）

**`ecommerce` repo 同時含有 Go API 原始碼、migration、部署檔（`docker/`、`docker-compose.yml`、`.github/`）、營運文件與 e2e。** 給 AI 整個 repo 的寫入權，等於給它能改 API 授權邏輯與部署的能力。守門員的範圍檢查（只有 `admin/design/**` 可變）是**事後**的攔截網，不是沙箱；真正的邊界是**讓 AI 的工作目錄裡根本沒有別的東西**。因此：

1. **平台後台的設計工作階段必須在「只含 `admin/`」的 sparse checkout 或 worktree 裡執行。** 絕不在完整的 `ecommerce` 工作樹裡跑，也絕不給 agent 整個 repo 的 checkout。
2. **AI 沒有 repo 範圍的寫入權，也沒有任何憑證**：不給部署金鑰、雲端權杖、`.env`、資料庫連線字串、Cloudflare 存取。它只能改工作目錄裡的檔案；推送、合併、部署都是人類（或經人類核准的服務）做的事。
3. **結果一定要過範圍檢查**：`node scripts/check-design.mjs --base <ref>`（`<ref>` 是這次編輯開始時的 commit）。變更清單裡只要有一個不在 `admin/design/**` 的檔案（含 `admin/` 以外的任何路徑，會顯示成 `../x`）就是違規、整批拒絕。CI 的 `Admin guards` job 對 `design/admin-*` 分支做同樣的檢查。

### 建立只含 `admin/` 的工作目錄（已在拋棄式 clone 實測）

兩種方式擇一。**注意 pattern 要用「非 cone 模式」的 `/admin/`**：cone 模式（`sparse-checkout set admin`）一定會把 repo 根目錄的檔案（`CLAUDE.md`、`Makefile`、`docker-compose.yml`、`README.md`……）也一併檢出，那不符合「只有 `admin/`」。pattern 以 `printf ... | --stdin` 傳入，避免 Windows Git Bash 把開頭的 `/admin/` 當成路徑改寫成 `C:/Program Files/Git/admin/`。

**方式 A：從既有 clone 開一個 sparse worktree（主工作樹不受影響）**

```bash
cd <ecommerce-clone>
BASE=$(git rev-parse origin/main)                      # 這次編輯的起點（守門員用它算 diff）
git worktree add --no-checkout ../ecommerce-admin-design -b design/admin-<request-name> "$BASE"
cd ../ecommerce-admin-design
printf '/admin/\n' | git sparse-checkout set --no-cone --stdin
git checkout                                           # 只展開 admin/（若已展開則無事）
ls                                                     # 只應看到 admin
cd admin && node scripts/check-design.mjs --base "$BASE"
```

**方式 B：獨立的 sparse clone（工作目錄裡甚至沒有其他物件的檔案內容）**

```bash
git clone --filter=blob:none --sparse --no-checkout <ecommerce-remote-url> ecommerce-admin-design
cd ecommerce-admin-design
printf '/admin/\n' | git sparse-checkout set --no-cone --stdin
git checkout -b design/admin-<request-name> origin/main
BASE=$(git rev-parse HEAD)
ls                                                     # 只應看到 admin
```

（`--filter` 對本機路徑的 clone 會被忽略，要用 `file://` URL 才生效；遠端 URL 不受影響。`git sparse-checkout list` 應只印出 `/admin/`。）

驗證要點：`ls` 只看到 `admin`；從 `admin/` 內跑 `node scripts/check-design.mjs --base "$BASE"` 與 `node scripts/test-guard.mjs` 皆通過；改一個 `admin/design/copy.js` 仍是 exit 0；在 `admin/` 外放任何檔案（例如 `../README.md`）會被範圍檢查擋下（exit 1，`scope/outside-design`）。

> 提醒：sparse checkout 隱藏的是**工作目錄**，不是 git 物件庫；完整 clone 的 `.git` 裡仍有 `api/` 的歷史。對不可信的 agent，優先用方式 B 加上 `--filter=blob:none`（blob 在需要時才取），並確保 agent 的沙箱**不含**憑證、也**不能**對遠端寫入（見 [AI-EDIT-SERVICE-NOTES.md](AI-EDIT-SERVICE-NOTES.md)）。

### 長期建議：平台成長後把 `admin/` 拆成獨立 repo

平台後台一旦開始有外部設計者或常態的 AI 編輯服務，應把 `admin/` 搬到自己的 repo（例如 `platform-admin`），讓「給 AI 的存取範圍」等於「整個 repo」，不再依賴 sparse checkout 的紀律。搬遷時要帶走的東西：

- `admin/` 整個目錄的內容（成為新 repo 的根，或保留 `admin/` 子目錄以不改任何 `--root`／路徑）；
- CI：`ci.yml` 的 `Admin guards` job（`check-design`、`test-guard`、`design/admin-*` 分支的 `--base` 範圍檢查）與 `image-admin.yml`；
- `.claude/skills/platform-admin-design/` 與 `.claude/skills/core-change/`、`admin/CLAUDE.md`；
- 後台的 e2e 與 `e2e/parity.mjs` 所需的部分（它們需要 `ecommerce` 的 API 當拋棄式後端，所以 e2e 可以留在 `ecommerce`，由那邊 checkout 後台來跑）；
- 文件裡指向 `ecommerce` 其他位置的引用（`docs/admin-frontend-integration.md`、`docs/deploy/admin-exposure-runbook.md`）改成連結；
- 分支保護：核心路徑（`admin/` 中除 `design/` 與 `docs/` 外的一切）的變更必須由人類審查（CODEOWNERS），`design/**` 的合併須經核准。

### 設計層仍然做得到的事（誠實的殘餘風險）

守門員、CSP 與核心把「資料外洩、繞過權限、繞過確認」擋在設計層之外，但**外觀與文字本身仍是設計層的**，所以一個惡意或粗心的設計仍可以：

- 做出**外觀或操作體驗上的**變更：版面、配色、字型、導覽順序與標籤、隱藏或搬動非警告類的 UI；
- 用**誤導性的文案**（在固定警告以外的地方，例如標題、按鈕文字、欄位說明、空狀態）讓人誤解一個動作；
- 用版面或 CSS 讓按鈕**難以辨識或點到**（不是隱藏對話框，而是把「刪除」做得像「儲存」、把確認按鈕擠到角落）；
- 重排或隱藏導覽項目（但不能新增、不能讓人進入沒有權限的畫面）；
- 讓某個畫面**壞掉**（核心會退回內建畫面並在 console 警告，但外觀回歸仍可能出現）。

它**做不到**：略過權限檢查（`ctx.data`／`ctx.actions` 在核心內先檢查）、略過不可逆動作的確認與固定警告、略過驗證與狀態規則（商品 PUT 的 SKU 全量保護、出貨與取消規則、金額與點數格式化）、碰 token（access token 只在核心的記憶體，設計層拿不到）、發出任何網路請求、讀寫儲存、載入外部資源、新增路由。因此**人類在預覽上看過每個畫面再核准**是設計的一部分（掃描器不是沙箱）。

## 1. 模型：鎖定的核心 vs 設計層

| 層 | 位置 | 誰能改 |
| --- | --- | --- |
| **設計層** | `admin/design/**`（僅此） | AI agent 與人類設計師 |
| **核心**（鎖定） | `app.js`、`index.html`、`styles.css`、`nginx.conf`、`Dockerfile`、`.dockerignore`、`package.json`、`README.md`、`CLAUDE.md`、`lib/**`（含 `lib/meta/`、`lib/public-api.json`）、`core/**`（含 `core/groups/`、`core/members/`、`core/platform/`）、`ui/**`、`vendor/**`、`design-defaults/**`、`scripts/**`（含 `core.manifest.json`、`__fixtures__/`） | 沒有人，除非是人類審過的「核心變更」（見第 10 節與 `.claude/skills/core-change/SKILL.md`），之後才跑 `--update` |
| 不在雜湊清單內 | `docs/**`、`e2e/**`（`admin/` 下若有） | 文件；不屬於設計層，AI 也不改 |

核心清單的定義（`scripts/check-design.mjs`）是**預設鎖定**：`admin/` 下除了 `design/`、`docs/`、`e2e/`、`node_modules/`、`.git/` 以外的每個檔案都進 `scripts/core.manifest.json` 的 SHA-256 清單，新增、修改、刪除任何一個都是 `manifest/changed`。所以在 `admin/` 隨手加一個新檔（新 lib、新設定）也會被抓到。

核心擁有一切不可被繞過的東西：認證與 token、權限判斷（`can`）、每一個 API 呼叫（`lib/api.js`）、狀態規則、驗證、商品 PUT 的全量保護（`core/groups/catalog.js` 的 `saveProduct`）、上傳上限與 HEIC 轉檔、金額與點數的格式化、一次性連結（token 只留在 `core/auth.js` 的閉包）、以及**每個不可逆動作的確認步驟與固定警告**。設計層只擁有**長相與用字**：標記、結構、版面、顏色、字型、文案、表單顯示哪些欄位與順序。

一個畫面怎麼被建起來：

```
網址 #/<view>/<params>  ->  core/routes.js（來自 lib/meta/*：路由 id、權限節點）  ->  權限閘門（核心）
  ->  design/routes.js（id -> 檔案）  ->  design/views/<檔>.js : export default function (root, ctx)
  ->  核心掛載你放進 root 的東西（失敗就掛 design-defaults/ 的內建畫面）
```

- 路由的權限節點沒有全給，核心掛 `noPermission` 畫面，**你的畫面根本不會執行**。你無法讓人看到沒有權限的畫面。
- 你的畫面不見、載入失敗、丟錯、回傳不可用的東西或 20 秒沒完成，核心改掛 `design-defaults/` 的內建畫面並在 console 警告（`[design] ...`）。**登入永遠不會被壞掉的設計擋住。** 例外：API／動作的**資料錯誤**（`e.status` 是數字，或 `e.isActionError`）不會觸發備援，它們是資料問題，照常顯示給使用者。
- 你的畫面拿到 `ctx`；`ctx` **只含** `lib/public-api.json` 列的名稱，沒有 token、沒有 `fetch`、沒有儲存、沒有核心模組。

## 2. 你可以改的檔案

```
design/
  tokens.css   CSS 變數（目前只有 --brand-primary、--brand-accent；商家品牌色由核心以行內樣式疊上去，會蓋過它）
  theme.css    自由 CSS，最後載入（預設是空的）← 主要的改版面
  copy.js      所有文案："key": "含 {placeholder} 的文字"（純資料）
  routes.js    路由 id -> 畫面檔（純資料）
  views/*.js   一個畫面一個檔，default export 是 render 函式
  parts/*.js   畫面共用的零件（被 views import）
  assets/      圖片、字型、icon（CSS 的 url(assets/x.svg) 或 ctx.asset("x.svg")）；目前沒有這個資料夾，需要時再建
```

允許的檔案：`design/` 下任何位置的 `.js`、`.css`（直接放在 `design/` 的 `.js` 只能是 `copy.js` 與 `routes.js`，程式碼放在 `views/` 或 `parts/`）、`.md`／`.txt` 筆記（每檔 ≤ 64 KiB）；`design/assets/` 底下只能 `svg png jpg jpeg webp gif ico avif woff2`（每檔 ≤ 512 KiB、內容必須真的是該格式、SVG 必須是被動的）。至多 300 個檔案、總計 4 MiB、單一 JS ≤ 128 KiB、CSS ≤ 256 KiB、檔名只用字母數字與 `. _ -`、資料夾至多 6 層、不可有符號連結。

### `design/copy.js`

```js
export default {
  "nav.orders": "訂單",
  "common.pageInfo": "第 {page} / {pages} 頁 ｜ 共 {total} 筆",
};
```

- 純資料：不能有呼叫、模板運算式、spread、字串相加。
- 只能用**已知的 key**（`design-defaults/copy.js` 與 `design-defaults/copy/*.js` 定義的，約 895 個）。未知 key 會被忽略並在 console 警告，且**守門員判定失敗**。
- 一個字串的 `{placeholder}` 可以比預設**少**，不能**多**。每字串至多 600 字元，key 至多 80 字元，格式 `^[a-z][A-Za-z0-9]*(\.[A-Za-z0-9_-]+)*$`。
- 缺漏、無效或非字串的項目，逐項安靜地退回內建文字。
- key 的家族（前綴）：`common` `err` `auth` `login` `setPassword` `acceptInvite` `shell` `notFound` `noPermission` `bootError` `nav` `cms` `confirm` `categories` `products` `productEditor` `productImages` `orders` `shippingMethods` `memberTiers` `members` `points` `reports` `settings` `team` `platform`。找某句話的 key：`grep "訂單" design/copy.js`。
- **新增一個 key 是核心變更**（要同時進 `design-defaults/copy/<檔>.js`，見第 10 節）；你只能改既有 key 的值。

#### 不可逆動作的確認與固定警告

不可逆動作的確認對話框是**核心**的：`<dialog data-testid="confirm-dialog">`，按鈕 `confirm-yes`／`confirm-no`，由核心動作自己呼叫（`ui/kit.js` 的 `confirmDialog`）。對話框 = 你可以改的部分 + **固定警告**：

- 你可以改：`confirm.<id>.title`、`confirm.<id>.text`（前導句，可為空）、`confirm.<id>.yes`／`.no`（按鈕字樣）。
- **固定、核心擁有、你不能定義／改字／縮短／移除**：警告文字，位於 `lib/meta/<group>.js` 的 `warned`（由 `core/warnings.js` 讀取），**沒有 copy key**。對話框永遠把它畫在 `data-testid="confirm-warning"` 裡。
- 你只能**調外觀**（顏色、粗細）。任何 `confirm.*.*warn*` 的 key 都會讓守門員失敗（`schema/copy`，「FIXED by the core」），隱藏／縮小／停用對話框或警告的 CSS 會失敗於 `css/hides-dialog`。
- 需要不同的警告？那是核心變更（人類改 `lib/meta/<group>.js` 的 `warned`），不是設計變更。

目前固定警告涵蓋的 id（16 個，權威清單是 `lib/meta/*.js` 的 `warned`）：

| group | id（`confirm.<id>.*`） |
| --- | --- |
| cms | `deletePage` |
| catalog | `deleteProduct` `deleteCategory` `deleteImage` |
| orders | `cancelOrder` `shipDelivered` `shipReturned` `deleteShippingMethod` |
| members | `deleteMemberTier` `changeTimezone` |
| settings | `revokeKey` `removeOrigin` `disableMember` |
| platform | `disableAccount` `removeMembership` `confirmApplicantContact` |

（`disableMember` 對應團隊頁的停用成員。舊版 `confirm.<id>.message`／`messageSub` 之類的 key 對這些 id 已退役：會被忽略並在 console／守門員輸出一則「retired」備註，把前導文字搬到 `.text`、不要再寫警告。）

不可逆動作之外的確認（例如公開金鑰標籤的小對話框 `#publishable-key-dialog`）同樣在核心，不受你控制，只能調文案與外觀。

### `design/routes.js`

純資料，把路由 id 對到 `design/views/` 下的檔案（路徑必須長得像 `views/name.js`）：

```js
export default { orders: "views/orders.js", platformShops: "views/platform-shops.js", /* ... */ };
```

兩個 id 可以共用一個檔。路由 id、網址、權限由核心固定（第 5 節），你不能新增路由或改網址。想換一個畫面實作：在 `views/` 新增檔案，把該 id 指過去；`design-defaults/` 永遠留著原廠版本作為備援。

### `design/tokens.css` 與 `design/theme.css`

- `tokens.css`：`:root` 的自訂屬性。目前定義的是 `--brand-primary` 與 `--brand-accent`（`styles.css` 使用它們）。商家若在平台設了品牌色，核心的 `applyBranding` 會以行內樣式覆寫這兩個變數，**勝過** `tokens.css`。
- `theme.css`：任何 CSS，在 `styles.css` **之後**載入，同權重時勝出。規則：不可 `@import`、`url()` 只能指向 `design/assets/` 的檔案（或 `data:image/...`）、不可 `expression()`、`behavior:`／`-moz-binding`、字串以外不可有 CSS escape、不可有隱藏或停用對話框／確認控制項的規則（`dialog`、`.dlg*`、`::backdrop`、含 `confirm` 的選擇器搭配 `display:none`、`visibility:hidden`、`opacity:0`、`pointer-events:none`、零尺寸……）。CSP 不允許第三方字型與樣式：要用網頁字型，把 `woff2` 放進 `design/assets/` 並 `@font-face { src: url(assets/x.woff2) }`。
- 核心的 CSS class（`.tab`、`.topbar`、`.view`、`.badge`、表單與表格等）都在 `styles.css`。你可以重用或在 `theme.css` 覆寫；**`styles.css` 是鎖定的**，只能透過 `tokens.css` 與 `theme.css` 調外觀；若做不到，說明並升級（第 10 節的核心變更流程），不要想別的辦法碰 `styles.css`。

## 3. 寫一個畫面

```js
// design/views/shipping-methods.js
export default async function shippingMethods(root, ctx) {
  const { h } = ctx.ui;
  const { t, data, actions } = ctx;
  root.append(h("p", {}, t("common.empty")));
  const list = await data.shippingMethodsList();       // 純資料；API 錯誤會丟出
  if (!ctx.alive()) return;                            // 使用者已離開
  // ... 用 h() 畫 DOM；寫入用 actions.saveShippingMethod(...)
}
```

- `root` 是空的元素（shell、login、set-password、accept-invite、bootError 是 `DocumentFragment`）。把 DOM 加進去。函式可以是 `async`；除了 **shell** 以外不用回傳東西。
- 只用 `ctx.ui.h(tag, attrs, ...children)` 建 DOM；文字子節點一律被轉義。`h` 會丟掉（並在 console 警告）`innerHTML`、`outerHTML`、`srcdoc`、字串的 `on*` 屬性、`action`／`formaction`、類 script 標籤（`script iframe object embed link meta style base ...`）、字串型的 `style`（要傳物件），以及任何不是相對路徑、`#`、`tel:`／`mailto:`、`data:image/...`、同源或允許的圖片來源的 `src`／`href`。事件處理器是函式：`h("button", { onclick: fn })`。SVG 用 `ctx.ui.svg(tag, attrs, text)`（有標籤白名單）。
- **只能** import `design/` 底下其他檔案（相對路徑，例如 `import { x } from "../parts/x.js"`）。其他一切經由 `ctx`。
- 動作或資料改變後自己重畫（`clear(el)` 後再 `append`，或重新 render）；沒有框架。
- 競態守衛：每次 `await` 之後、碰 DOM 之前檢查 `ctx.alive()`（`false` 表示使用者已經導航離開）。
- **shell** 畫面（路由 id `shell`）必須回傳 `{ main, setActive(navKey), setNoAccess(text) }`（缺任何一個，核心就改用內建的 shell）；選擇性回傳 `refreshBadge()`（核心在路由渲染與會改變數字的寫入之後呼叫，用來更新「開店申請」的數量徽章，資料來自 `ctx.data.applicationsBadge()`）。`main` 是 `root` 內核心掛載每個畫面的元素。shell 畫 `ctx.nav.items`（`[{key, copy}]`，已依權限過濾；`copy` 是標籤的 copy key，用 `ctx.t(item.copy)` 顯示）。可以重排、改標籤、改外觀或省略項目；寫死連到沒有權限的畫面只會落到核心的「沒有權限」畫面。
- 路由資料：`ctx.route.id`、`ctx.route.params`（路徑片段陣列，例如 `#/orders/1234` 的 `["1234"]`）、`ctx.route.state`（登入畫面的 `{email, notice}`、bootError 的 `{error}`）。**一次性連結的 token（set-password／accept-invite）不在 `params`**，它留在核心，只由 `ctx.auth.*` 使用。
- 表單的必填仍然必填，因為**動作**會驗證，不是表單。漏掉必填欄位的表單會收到核心的驗證錯誤（`err.fields`，`{pointer: message}`；用 `ctx.fieldErrors(err)`／`ctx.errorMessage(err)` 顯示）。
- 不可逆動作（第 5 節與上表）自己顯示核心的確認對話框；使用者拒絕時，動作回傳一個「已取消」的結果（或丟出 `ActionError` 並帶 `reason: "cancelled"`，依動作而定，看預設畫面怎麼處理）。**不要**自己做一個確認去取代它，也不需要。
- 錯誤：函式會丟出。API 錯誤有 `.status`（數字）；核心拒絕是 `ActionError`（`e.isActionError`，`e.kind` ＝ `permission | state | invalid | limit | stale | internal`，`e.message` 已本地化，`e.fields` 是表單用）。`ctx.errorMessage(e, { 409: "..." })` 取得訊息文字。
- 預設畫面（`design-defaults/views/*.js`）是最好的範例：想知道一個畫面怎麼用 `ctx`，讀對應的預設畫面。

### 最小的可運作範例（換掉 not-found 畫面）

```js
export default function notFound(root, ctx) {
  root.append(ctx.ui.h("p", { class: "error" }, ctx.t("notFound.message")));
}
```

## 4. 公開 API：`ctx` 提供的一切

**權威清單是 `lib/public-api.json`**，由 `node scripts/gen-public-api.mjs` 從真正的 `ctx` 產生（人類審查它的 diff；它的任何變動就是核心／設計契約的變動）。`scripts/check-design.mjs` 會建出真正的 `ctx`，比對兩者必須相等，並讓使用了清單以外名稱的設計程式碼失敗（`js/ctx-unknown`）。下面只描述命名空間；要看目前的完整名稱，開那個 JSON 檔。權限在**每個函式內**、任何請求之前檢查。

| 命名空間 | 內容 |
| --- | --- |
| `ctx.route` | `id`、`params`（路徑片段陣列）、`state`（純資料，key 不固定） |
| `ctx.alive()` | 使用者還在這個畫面嗎（await 之後檢查） |
| `ctx.t(key, params)` | 文案查詢（設計層 copy -> 內建 copy -> key 本身，`{name}` 由 params 填入） |
| `ctx.can(node)`／`ctx.canAny(...nodes)`／`ctx.canPlatform(node)` | 權限提示，**只是 UI 提示**，核心動作會重新檢查，API 才是授權 |
| `ctx.navigate(name, ...segments)` | 換畫面，更新 hash：`ctx.navigate("orders", 1234)` -> `#/orders/1234`；name 必須是小寫連字號路由名（第 5 節），否則拒絕並回傳 `false`；hash 沒變時回傳 `false`，呼叫端要自己重畫 |
| `ctx.scrollTop()` | 捲到頂端 |
| `ctx.asset(name)` | `design/assets/<name>` 的 URL（名稱受限制，`[A-Za-z0-9][A-Za-z0-9._-]{0,80}`） |
| `ctx.errorMessage(err, overrides)`／`ctx.fieldErrors(err)` | 把錯誤轉成已本地化的訊息／`{pointer: message}` |
| `ctx.session` | `userEmail`、`shopId`、`shopIds`、`hasShopList`、`selectShop(id)`；**沒有 token** |
| `ctx.nav.items` | `[{key, copy}]`，已依權限過濾的導覽 |
| `ctx.auth` | 僅在 login／set-password／accept-invite／shell 等畫面提供：`login`、`logout`、`goLogin`、`passwordSetup.inspect/submit`、`invitation.inspect/acceptNew/acceptAsCurrent/loginAndJoin`。**token 永遠不到你手上** |
| `ctx.ui` | 展示用原件：`h svg append clear $ $$`、`rows renderPager fillSelect repeater copyText`、`schemaForm.{build, collect, showValidationErrors}`（JSON Schema 驅動的內容表單，驗證在核心）、`platformLinkOnce`（核心的一次性連結盒，URL 只存在該 DOM，不儲存） |
| `ctx.format` | `money`（TWD 整數元，**絕不縮放**）、`points`／`pointsDelta`／`percent`（**點數不是金額，不可經 `money`**）、`parseAmount`、`priceRange`、`time`、報表與平台狀態的格式化（`reportCount`、`platformShopStatus`、`platformApplicationStatus`、……） |
| `ctx.data` | 查詢，各自先檢查權限並綁定目前的 shop，回傳純資料：頁面與內容（`cmsPages`、`cmsPage`、`cmsContent`、`cmsPageTypes`）、商品與分類（`listProducts`、`getProduct`、`listCategories`、`categoryChoices`、`categoryProductOrder`、`listProductImages`）、訂單（`ordersList`、`orderDetail`、`orderPayments`、`orderShipments`）、運送方式（`shippingMethodsList`）、顧客與點數（`memberRoster`、`memberDetail`、`memberOrders`、`memberTiers`、`pointsBalance`、`pointsLedger`）、報表（`reportLoad`）、設定與團隊（`settingsPublishableKeys`、`settingsAllowedOrigins`、`settingsEcpay`、`teamOverview`）、內容儲存（`contentTypes`、`contentDocuments`、`contentDocument`、`contentRevisions`、`contentRevision`、`contentSchemaStatus`、`dryRunContentSchema`）、排程與發佈 hook（`contentSchedulesUpcoming`、`contentSchedulesRecent`、`contentDocumentSchedule`、`publishHookStatus`）、品牌（`adminBranding`）、平台層（`platformShopsList`、`platformShopSetupStatus`、`platformAccountsList`、`platformAccount`、`platformApplicationsList`、`platformApplication`、`platformMerchantRoles`、`applicationsBadge`） |
| `ctx.logic` | 與狀態規則同步的純函式，用來決定「要提供什麼」：`canCancelOrder`、`hasReturnedShipment`、`outstandingItems`、`shipmentRemaining`、`productMedia`、`memberLabel`、`reportDefaultPeriod`、`settingsErrorText` |
| `ctx.enums` | 常數：`orderStatus`、`paymentStatus`、`paymentRecordStatus`、`fulfillmentStatus`、`shipmentStatus`、`shipmentCodes`、`platformShopStatuses`、`platformSetupChecks` |
| `ctx.actions` | 寫入動作，每個都在核心內檢查權限＋狀態規則＋驗證，不可逆者自己顯示固定警告的確認：頁面（`cmsCreatePage`、`cmsSavePage`、`cmsPublishPage`、`cmsUnpublishPage`、`cmsPreviewPage`、`cmsDeletePage`、`cmsSaveContent`、`cmsUploadImage`）、商品（`saveProduct`、`deleteProduct`、`uploadProductImage`、`moveProductImage`、`setProductImageAlt`、`deleteProductImage`）、分類（`saveCategory`、`saveCategoryProductOrder`、`deleteCategory`）、訂單與出貨（`cancelOrder`、`createShipment`、`advanceShipment`）、運送方式（`saveShippingMethod`、`deleteShippingMethod`）、會員（`saveMemberTier`、`deleteMemberTier`、`adjustMemberPoints`）、設定（`setReportTimezone`、`saveAdminBranding`、`settingsCreatePublishableKey`、`settingsRevokePublishableKey`、`settingsAddOrigin`、`settingsRemoveOrigin`、`settingsSaveEcpay`）、團隊（`teamInvite`、`teamRevokeInvite`、`teamSetEnabled`、`teamSetRole`）、平台層（`platformProvisionShop`、`platformCreateAccount`、`platformDisableAccount`、`platformEnableAccount`、`platformResetAccountPassword`、`platformAddMembership`、`platformRemoveMembership`、`platformApproveApplication`、`platformRejectApplication`、`platformVerifyApplicationManually`、`platformMarkMessageSent`） |

重點行為：

- **商品 PUT 是全量語意**：`ctx.actions.saveProduct` 由核心保證永遠送完整的 `skus` 與 `category_ids`，漏送一列會靜默刪掉 SKU 的價格與庫存，所以核心負責重組完整酬載，不信任表單（見 `core/groups/catalog.js` 檔頭）。畫面省略某個欄位或 SKU 列不會導致它被刪除；但**不要**因此移除必填欄位。
- **點數不是金額**：點數一律 `ctx.format.points`／`pointsDelta`，不要經 `ctx.format.money`。
- **一次性連結**（開通、重設密碼、邀請）：核心動作回傳後由 `ctx.ui.platformLinkOnce` 顯示一次，不儲存、不記錄；重畫就消失。
- `ctx.auth` 在 `login` 畫面成功後進入 app；token 僅在核心記憶體。

### 要找某個畫面該用哪些名稱？

讀對應的預設畫面（`design-defaults/views/<route>.js`），它就是用 `ctx` 蓋出整個畫面的實例。

## 5. 路由（由核心固定）

網址是 hash router：`#/<view>[/<param>...]`（`lib/router.js`）。`ctx.navigate("orders", 1234)` 寫 `#/orders/1234`。深層連結只涵蓋「這一筆」（`#/orders/1234`、`#/members/5`、`#/points/57`），篩選條件不入 hash。開機時 hash 指到不存在、沒有權限或 `transient` 的畫面，會安靜回落到第一個有權限的畫面。路由定義來自 `lib/meta/*.js`（純資料），`design/routes.js` 只決定每個 id 用哪個檔。

| 路由 id | hash 名稱 | 需要 | 預設檔 |
| --- | --- | --- | --- |
| `shell` | （外框） | 已登入 | `views/shell.js` |
| `login` `setPassword` `acceptInvite` | `#/login` `#/set-password/{token}` `#/accept-invite/{token}` | 公開 | `login.js` `set-password.js` `accept-invite.js` |
| `notFound` `noPermission` `bootError` | （系統畫面） | 無 | `not-found.js` `no-permission.js` `boot-error.js` |
| `pages` | `#/pages` | `page.view` | `pages.js` |
| `content` | `#/content` | `shop.view` | `content.js` |
| `orders` | `#/orders[/{id}]` | `order.view` | `orders.js` |
| `shippingMethods` | `#/shipping-methods` | `shipping_method.view` | `shipping-methods.js` |
| `products` | `#/products` | `product.view` | `products.js` |
| `categories` | `#/categories` | `category.view` | `categories.js` |
| `memberTiers` | `#/member-tiers` | `member_tier.view` | `member-tiers.js` |
| `members` | `#/members[/{id}]` | `member.view` | `members.js` |
| `points` | `#/points[/{id}]` | `point.view` | `points.js` |
| `reports` | `#/reports` | `report.view` | `reports.js` |
| `storefrontClients` | `#/storefront-clients` | `publishable_key.view`、`shop.manage_client_origins`、`payment_credential.manage` 任一 | `storefront-clients.js` |
| `team` | `#/team` | `user.manage_members` | `team.js` |
| `platformShops` | `#/platform-shops` | 平台 `shop.list` | `platform-shops.js` |
| `platformAccounts` | `#/platform-accounts` | 平台 `account.view` | `platform-accounts.js` |
| `platformApplications` | `#/platform-applications` | 平台 `shop_application.view` | `platform-applications.js` |
| `documents` | `#/documents[/{key}[/revisions]]` | `content.view`（儲存／還原／上傳需 `content.edit`，發佈／下架／刪除需 `content.publish`） | `documents.js` + `parts/content-*.js` |
| `contentSchema` | `#/content-schema` | `content.manage` | `content-schema.js` |
| `contentPublishing` | `#/content-publishing` | `content.publish` 或 `content.manage` 任一（區塊各自再判） | `content-publishing.js` + `parts/content-schedule.js` |

（權威：`lib/meta/*.js`。頁面的編輯器與商品編輯器是 `pages`／`products` 畫面**內**的面板，不是獨立路由、不改 hash。導覽項目與順序也在 `lib/meta`，`ctx.nav.items` 依權限過濾；你可以重排或省略，不能新增。）

## 6. 你絕對不能做的事

- 改 `admin/design/` 以外的任何東西。範圍檢查會整批拒絕。
- 使用 `fetch`、`XMLHttpRequest`、`WebSocket`、`navigator.sendBeacon`、`localStorage`、`sessionStorage`、`indexedDB`、`document.cookie`、`eval`、`Function`、`import()`、`importScripts`、`Worker`、`window`、`document`、`globalThis`、`location`、`navigator`、`innerHTML`／`outerHTML`／`insertAdjacentHTML`、`setAttribute`、`createElement`、`new Image()`、`constructor`、`Reflect`、`Proxy`、裸的 `self`／`top`／`parent`／`frames`／`opener`。
- import 核心模組（`../../lib/...`、`../../ui/...`、`../../core/...`）或 `design/` 以外的任何東西。
- 建立 `<script>`、`<iframe>`、`<object>`、`<embed>`、`<link>`、`<meta>`、`<style>`，或任何有表單 `action` 的元素。
- 用字串設定 `on*` 處理器、指派 `.onclick = ...`、呼叫 `setTimeout("code")`。
- 在字串、連結、圖片或 CSS 放外部網址（`https://...`、`//...`、`javascript:`）。
- 混淆：字串字面值相加、含樣板或呼叫的計算式成員存取（`ctx.ui["h"+x]`）、對 `ctx` 的計算式存取。
- 使用 `lib/public-api.json` 沒有的 `ctx` 名稱。
- 弱化確認的意義、用 CSS 隱藏對話框或確認按鈕、做假的確認、**定義或改寫固定警告**。
- 想辦法顯示使用者沒有權限看的資料：`ctx.data.*` 在核心就會拒絕，空的區塊是正確結果。
- 編輯 `design-defaults/`（原廠備援）、`lib/public-api.json`、`scripts/core.manifest.json`、`styles.css`，或本文件。
- 執行 `node scripts/check-design.mjs --update` 或 `node scripts/sync-design.mjs`（見第 10 節）。

## 7. 在本機預覽

```bash
# 1. 起靜態伺服器（不含 proxy；後台的 API 位址在 app 內設定，localhost 預設 http://localhost:8080）
node scripts/serve.mjs                      # http://0.0.0.0:3001（PORT／HOST 環境變數可改）
node scripts/serve.mjs --csp                # 同上，並送出正式環境的 CSP（預覽與審查一律用這個）
# 2. 改 design/ 下的檔案，重新整理瀏覽器（無建置、不快取）
# 3. 看瀏覽器 console：每個設計層問題都是一條 "[design] ..." 警告
```

檢查兩種寬度：390px（手機）與 1280px（桌機），不得水平捲動。後台需要一個拋棄式的 API（見 README「測試」，自己的資料庫、自己的 Redis DB，不碰主資料庫）。若 API 與後台不同源，後台在 localhost 時用 body 模式的 refresh token；正式拓撲是同源（cookie 模式）。

`--csp` 模式的 CSP 與 `nginx.conf` 同源（`connect-src` 預設只有 `'self'` 與 `https://*.r2.cloudflarestorage.com`）。本機若用 MinIO 或其他 S3 主機上傳圖片，要加：`CSP_CONNECT_EXTRA="http://127.0.0.1:9000" node scripts/serve.mjs --csp`。

## 8. CSP（部署要求，不是選項）

`nginx.conf` 對所有回應（`always`）送出嚴格的 Content-Security-Policy：`default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self'; img-src 'self' https: data: blob:; connect-src 'self' https://*.r2.cloudflarestorage.com; worker-src 'self' blob:; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`。沒有行內 script／style，所以一個漏過掃描的設計仍然不能送資料到 API 與 R2 以外的地方、不能執行動態程式碼（`eval`／`Function` 被擋）、不能被嵌入框架。`wasm-unsafe-eval` 與 `worker-src blob:` 只為 vendored 的 HEIC 解碼器。`img-src https:` 是因為商品與 logo 圖片在圖片網域上。`check-design.mjs` 的 "deploy" 步驟檢查 `nginx.conf`（帶 `always`、`default-src 'self'`、`script-src` 只含 `'self'` 與 `'wasm-unsafe-eval'`、有 `form-action`……）、`Dockerfile`（把 `nginx.conf` 裝成 `default.conf`、之後不得移除或改寫）與 `index.html`；任何一項缺漏或被弱化就失敗（`deploy/csp-*`、`deploy/index-html`）。這不是設計的問題：它們是核心。

## 9. 交付前的檢查（守門員）

```bash
node scripts/check-design.mjs --base <ref>      # 一次檢查全部（ref = 這次編輯開始的 commit）
node scripts/test-guard.mjs                      # 守門員自己的測試（179 項，離線）
node scripts/scan-views.mjs design               # 只掃設計層程式碼與 CSS（快速，不含其他檢查）
node scripts/check-layers.mjs                    # 只跑分層檢查（check-design 也會跑）
```

`check-design.mjs` 的結束碼：**0** 全數通過；**1** 至少一項違規（報告列出 rule、檔案、行號、原因，修正後重跑）；**2** 用法或環境錯誤（例如 ref 不存在、無法讀取 checkout，**什麼都沒有被判定**）。不帶 `--base` 時略過範圍檢查；`--files a b c` 檢查一份明列的檔案清單（相對於 `admin/`）；`--json` 輸出機器可讀的報告；`--root <dir>` 檢查另一份 `admin/` checkout；`--no-layers` 略過分層檢查。

它依序執行：

| 步驟 | 內容 |
| --- | --- |
| scope | `git diff --name-only --no-renames <ref>`＋未追蹤檔；只有 `design/**` 可變，`admin/` 以外的路徑顯示為 `../x` 同樣違規（`scope/outside-design`） |
| design scan | 檔案盤點（數量、大小、檔名、符號連結、位置、型別）、JS 掃描、CSS 掃描、素材（大小、型別、魔術位元組、被動 SVG）：`files/*`、`js/*`、`css/*`、`asset/*` |
| schema | `copy.js` 與 `routes.js` 用載入器自己的程式碼驗證：`schema/copy`、`schema/routes`、`warnings/copy-key` |
| surface | `lib/public-api.json` 必須等於真正的 `ctx`（`ctx/manifest`）；設計程式碼只能用清單內的名稱（`js/ctx-unknown`） |
| defaults | `design-defaults/` 本身也過掃描，且每個路由都有內建畫面（`defaults/routes`、`schema/defaults`） |
| copy keys | 畫面用到的 key 都有定義（`copy/missing`） |
| fixed warnings | 每個不可逆確認都有固定警告、copy 沒有定義警告 key（`warnings/*`） |
| deploy | `nginx.conf` CSP、`Dockerfile`、`index.html`（第 8 節） |
| core manifest | `scripts/core.manifest.json`（CRLF 正規化後的 SHA-256）；任何核心檔被改、加、刪就是 `manifest/changed` |
| layers | `scripts/check-layers.mjs`：`lib/` 不得 import `ui/`、`core/`、`design/`、`app.js` 也不得建 DOM；`ui/`、`core/`、`app.js` 不得 `fetch`／XHR／碰 token 或儲存／寫 `/api/v1`，也不得靜態 import `design/` |
| drift（僅備註） | `design/` 與 `design-defaults/` 的差異報告，不算失敗 |

最後四項（surface、defaults、core manifest、layers）與你的編輯無關；若它們失敗，代表你動到了 `design/` 以外的東西或 checkout 損壞：還原，**不要 `--update`**。`--update` 會重寫核心雜湊清單：**只有在有意且經審查的核心變更之後，由人類執行，絕不是設計編輯的一部分。**

### 常見失敗與修法

| rule | 修法 |
| --- | --- |
| `scope/outside-design` | 還原該檔（`git checkout <ref> -- <file>`），移除多餘的未追蹤檔；若需求真的要改那個檔，升級 |
| `js/forbidden-identifier`、`js/banned-string`、`js/html-or-url-in-string` | 改用 `ctx.data`／`ctx.actions`／`ctx.ui.h`；沒有對應的 ctx 函式就升級 |
| `js/ctx-unknown` | 只用清單內的名稱；缺的名稱是核心變更，升級 |
| `js/import` | 只能相對路徑 import `design/` 內的檔案 |
| `schema/copy` | 刪除該 key；未知 key、非字串、過長、多出的 placeholder 都會被丟掉 |
| `css/url`、`css/import`、`css/hides-dialog` ... | 把檔案放進 `design/assets/` 用 `url(assets/x.svg)`；不要隱藏對話框，只調外觀 |
| `manifest/changed`、`ctx/manifest`、`layers` | 你動到了核心或 checkout 壞了：還原，**永不 `--update`** |

## 10. 核心變更（只有人類）：怎麼新增一個 `ctx` 函式

設計層需要一個 `ctx` 沒有的資料或動作時，**停下來並回報**；那是核心變更，由人類在分支上做，流程見 `.claude/skills/core-change/SKILL.md`。摘要：

1. 在 `core/groups/<group>.js` 實作（權限用 `core/guard.js` 的 `need`／`needAny`／`needPlatform`，在任何請求之前；所有 API 呼叫經 `lib/api.js`；不可逆動作走確認並加固定警告，見下）。
2. 重新產生清單：`node scripts/gen-public-api.mjs`（人類審查 `lib/public-api.json` 的 diff）。
3. 需要新文案 key：加進 `design-defaults/copy/<檔>.js`；預設畫面在 `design-defaults/views|parts/`。
4. 用 `node scripts/sync-design.mjs` 把 `design-defaults/` 同步到 `design/`（它**重寫** `design/copy.js`、`design/routes.js`、`design/views/`、`design/parts/`，所以先確認 `design/` 沒有尚待保留的設計；見 `core-change` skill）。
5. 先不帶 `--update` 跑 `node scripts/check-design.mjs`，讀每一條 `manifest/changed`，確認全都是你要改的檔案，**然後**才由人類跑 `node scripts/check-design.mjs --update` 重鎖。
6. 更新本文件第 4 節（若命名空間或語意變了）、跑 `node scripts/test-guard.mjs`，以及依情況跑 e2e。

新增**不可逆動作**：動作走核心的 `confirmDialog({ ..., warning })`；在對應的 `lib/meta/<group>.js` 的 `warned` 加 `{ <id>: "固定警告文字" }`（`ROUTE_IDS`、`WARNED_CONFIRM_IDS` 都從 `lib/meta` 衍生，單一來源）；兩份 copy 加 `confirm.<id>.title`、`.text`（前導，可為空）、`.yes`；絕不加 `.warning`／`.message`。

## 11. 出問題時

- console 的 `[design] ...` 警告會說出哪個項目被拒絕、改用了什麼。
- 畫面丟錯、不見或卡住：顯示的是內建畫面（那是備援在運作，不是核心的 bug）。修正該畫面，或 revert 該 commit。
- 守門員失敗：報告會指名規則。常見修法：移除禁用的識別字、用 `ctx.ui.h` 取代原生 DOM、把邏輯搬到既有的 `ctx.actions`、把字串寫成一整個（不要相加）。
- 想要的功能需要 `ctx` 沒有的東西（新的資料欄位、新的動作）：**停下來並說明**；那是給人類的核心變更，不要繞過。

## 內容儲存畫面（stage C）

- 核心：`lib/content.js`（API；ETag／`If-Match`／`If-None-Match` 走 `lib/api.js` 的 `apiRaw`）、`lib/content-schema.js`（schema 走訪與鎖定的驗證）、`core/content/actions.js`、`core/groups/content.js`、`ui/content-form.js`（schema 驅動表單建構器，`ctx.ui.contentForm`／`contentField`）、`core/groups/publishing.js`＋`core/publishing/logic.js`（排程與發佈 hook）。
- 儲存一律帶前置條件：412 → `ActionError` `kind "stale"`（請重新載入）、428 → `kind "internal"`（程式錯誤，不給使用者看）、422 → `kind "invalid"` 與 `e.fields`（JSON Pointer → 欄位）。還原只放回草稿（畫面說「已放回草稿，尚未發佈」）。
- 註冊 schema 是人類動作：`ctx.actions.registerContentSchema(text)` 只收人類貼上的文字，永遠先 dry run、再顯示核心確認（報告＋固定警告；平台回 409 `removals_require_confirmation` 時第二次確認）。需要 `content.manage`。
- 排程綁定審閱過的 `draft_rev`；hook 網址只寫不讀（只顯示遮罩的主機與狀態）、簽章 secret 只在輪替當下顯示一次、`failed: true` 是最終失敗狀態（畫面提供重新啟用）。固定警告 id：publishContent、unpublishContent、deleteContent、restoreContent、registerContentSchema、registerContentSchemaRemovals、deletePublishHook、rotateHookSecret、cancelSchedule、fireHook。
- e2e：`e2e/content-ui-path.mjs`（內容畫面，含權限與樂觀鎖）、`e2e/design-path.mjs`（設計層：不可跳過確認／權限、壞設計回退、惡意設計被守門員擋下、嚴格 CSP）。
