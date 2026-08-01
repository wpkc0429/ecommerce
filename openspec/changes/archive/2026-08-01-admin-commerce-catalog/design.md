# Design: admin-commerce-catalog

## Context

`admin/` 是 Phase 1 design D1 定案的「無建置步驟薄殼 SPA」：`index.html` + `app.js`（616 行）+ `styles.css`，以原生 ES module `<script type="module">` 載入，`npm run dev` 就是 `python3 -m http.server 3001`，`admin/Dockerfile` 只是 `COPY index.html app.js styles.css` 進 nginx。它承載三件事：登入與 token 輪替、JSON Schema 驅動的 CMS 表單、頁面發佈／預覽。**這個選擇在當時是對的**，而且到今天為止沒有付出過任何代價。

現在要加的東西形狀不同。商品列表要分頁／搜尋／篩選；商品編輯器要巢狀 SKU（一個商品 N 個 SKU，每個 SKU 又有一個 key/value 的 `options` jsonb），還要能新增／移除列。這是薄殼第一次面對真正的複雜表單。

三個既有事實限制了選項空間：

1. **`admin/` 目前沒有任何 CI job**。`.github/workflows/ci.yml` 只有 `api`（lint + test）與 `web`（typecheck + build）兩個 job；admin 只有一個 `image-admin.yml` 負責打 image。也就是說 admin 的 JS 今天沒有型別檢查、沒有 lint、沒有單元測試，**唯一守著它的是 `e2e/happy-path.mjs` 的瀏覽器斷言**。
2. **`API_BASE` 是執行期設定**（`localStorage.getItem("apiBase")`），Dockerfile 的註解明文寫著「not at build time」——同一個 image 可以指到任何 API。
3. **金額是最小貨幣單位整數**，TWD 的最小貨幣單位是元（`openapi.yaml` 的 `MinorUnitAmount`）。`3400` 就是 NT$3,400。這是整個 UI 最容易寫錯、又不會噴任何錯的地方。

下一個 change `admin-commerce-operations`（訂單／出貨／運送方式／會員等級／publishable key 與 origin）會直接繼承本文件 D1 的結論。讀者對象：後續 admin 前端的實作者。

## Goals / Non-Goals

**Goals:**

- **定案 `admin/` 的前端架構**，並把判準寫成可被推翻的條件，而不是一句偏好。
- 巢狀 SKU 編輯的具體實作形態，且證明它不會失控。
- 分類 CRUD、商品 CRUD（列表／建立／編輯／刪除、多對多分類、草稿/發佈）、SKU 就地改庫存。
- 金額顯示與輸入的不縮放規則，且由 E2E 機械化鎖住。
- UI 依呼叫者實際權限決定可見／可操作。
- 為商品鏈補上瀏覽器 E2E。

**Non-Goals:**

- 訂單／出貨／運送方式／會員等級／金鑰 UI（`admin-commerce-operations`）。
- 圖片上傳（平台尚無檔案儲存服務；`meta.images` 目前只能填 URL，維持現狀）。
- 分類的拖拉排序（`position` 以數字欄位編輯，見 `product-catalog` design 的 Open Questions）。
- 前端路由／深層連結、i18n、暗色主題、行動版版面。
- 任何資料庫 schema、migration、事件接線的變更。
- 批次庫存調整介面（D7）。

## Decisions

### D1. `admin/` 維持無建置步驟，但從單檔改為原生 ES module 多檔，並抽出最小元件詞彙（本 change 最重要的決策）

**先把三個選項攤開誠實評估。**

---

**(a) 維持薄殼，且維持現狀寫法。** 也就是把商品／分類的程式碼直接追加進 `app.js`，並沿用既有的收集模式。

`app.js` 目前收集表單值的方式是 `collectNode()`／`buildListField._collect()`：**它不持有輸入元素的參考，而是在儲存時回頭查 DOM，用「`.field-label` 的 textContent 等於 schema 的 title」把值配回鍵名**：

```js
const field = [...row.querySelectorAll(".field")].find(
  (f) => f.querySelector(".field-label").textContent === (items.properties[k].title || k));
```

這在 CMS 的場景勉強成立（每個 list row 內的標籤在該 row 內唯一）。**放到巢狀 SKU 上會直接壞掉**：SKU 表格有 N 列，每列的「SKU 代碼」「價格」「庫存」標籤字面完全相同；每列裡面還有一個 `options` 的 key/value 子列表，「鍵」「值」兩個標籤在單一列內就重複 M 次。要讓 label 比對在這種 N×M 的巢狀下正確，得引入 `:scope >` 限定、每層各自的容器 class、以及「同名標籤取第幾個」的位置假設——那是把一個本來就脆弱的機制推到它的斷裂點之外。估計行數不是問題（追加約 500 行），**問題是那 500 行裡會有 80 行是為了讓 DOM 查詢在巢狀下不出錯的膠水，而且沒有任何自動化檢查會告訴你它出錯了**（admin 無 CI、無型別）。錯的表現形式是「儲存後某個 SKU 的庫存變成別一列的值」，靜默、難查、直接損害商家資料。

`app.js` 也會從 616 行長到 1100 行，而 `admin-commerce-operations` 還要再加 5 個視圖——單檔 2500 行、`switchTab()` 裡 8 行硬編碼的 `$("#tab-x").hidden = ...`。**(a) 撐不住，理由是收集機制而不是行數。**

---

**(b) 引入建置步驟與框架**（Vite + React/Vue/Svelte）。誠實列出代價：

- **新 toolchain**：`admin/package.json` 從 8 行（一個 `python3 -m http.server`）變成一份有 lockfile 與 `node_modules` 的專案。
- **CI 變更**：admin 今天**完全沒有 CI job**。框架帶來的主要技術收益是型別安全，但那要靠一個新的 `admin` job（`npm ci` + `tsc` + `build`）才兌現得了；只加 toolchain 不加 job 等於只付成本。
- **部署變更**：`admin/Dockerfile` 從 6 行的單階段 nginx 變成 node build + nginx 的多階段；`image-admin.yml` 的 build cache 語意隨之改變。
- **與既有定位衝突**：bundler 的預設姿勢是把設定在 build time 內聯（`import.meta.env.VITE_API_BASE`）。現行 image 之所以能同時服務任何環境，正是因為 `API_BASE` 是執行期讀 localStorage。這個性質不是不能保住，但它從「唯一可能」變成「需要有人記得別破壞的約定」。
- **`make admin-dev` 的契約改變**：從「開一個靜態伺服器」變成「跑一個 dev server」。E2E 仍指向 `:3001`，可以維持，但多了一層。

**收益呢？** 誠實地說，框架真正買到的是三樣：宣告式渲染與自動 re-render、元件與生態系、型別。本 change 需要的「反應性」的全部內容是——列表資料變了就重抓重畫一次列表；表單存檔後回列表。那是一個函式呼叫。元件生態系在一個只有 6 個視圖、全部是「表格 + 表單」的後台上，價值遠低於它在產品前台的價值。型別確實有價值，但它替代不了 E2E：`tsc` 不會知道 `price_amount` 是最小貨幣單位、不會知道 PUT products 未列出的 SKU 會被刪除——**本 change 最危險的兩件事都是型別系統看不見的語意**。

**現在採用 (b) 是為了一個尚未出現的問題付出確定的成本。** 不採用。

---

**(c) 維持無建置步驟，但把單檔拆成原生 ES module，並抽出最小可重用詞彙。** 採用。

關鍵事實是：**「無建置步驟」不等於「一個檔案」**。瀏覽器原生支援 `import`／`export`，`admin/index.html` 早就是 `<script type="module">`。拆檔完全不需要打包器，只需要靜態伺服器把新目錄一起服務出去（`python3 -m http.server` 本來就會；`Dockerfile` 的 `COPY` 加一行）。

結構：

```
admin/
  index.html   app.js（shell：登入、商家切換、視圖註冊表）
  lib/api.js        token 輪替、api()、ApiError（自 app.js 逐字搬移）
  lib/dom.js        el()、$、clear()、repeater()（D2）
  lib/money.js      formatMoney()、parseAmount()（D4）
  lib/rbac.js       權限集合、can()、gate()（D5）
  lib/schemaform.js CMS schema 驅動表單（自 app.js 逐字搬移，僅此處使用）
  views/pages.js  views/content.js       （逐字搬移）
  views/categories.js  views/products.js （新增）
```

「最小詞彙」是刻意的下限：`el()`（已存在）、`repeater()`（D2，約 20 行）、`formatMoney()`、`can()`。**不做**虛擬 DOM、不做 observable、不做模板語言、不做路由器——每加一個都要能指出它替換掉了哪一段重複程式碼。視圖切換從三行硬編碼改為一行 `document.querySelectorAll(".tab-panel").forEach(p => p.hidden = p.id !== id)` 加一張 `{ tab: loadFn }` 註冊表，這樣第 8 個分頁的成本與第 4 個相同。

**這個決策什麼時候該被推翻（寫給 `admin-commerce-operations` 及之後的人）。** 出現下列任一情形時，重開 (b) 的評估，不必再爭論：

1. **需要跨視圖的即時衍生狀態**——例如一個畫面上多個小元件必須隨同一份資料同步重算（出貨介面若要即時反映「已出貨數／剩餘數」跨多列連動，就會踩到）。手動同步兩處還好，四處就是 bug 農場。
2. **同一個小元件被第三次重寫**。前兩次是巧合，第三次是缺少抽象。屆時先問「能不能加進 `lib/`」，答案是否定的（因為需要生命週期或反應性）時，那就是框架的訊號。
3. **需要型別安全甚於 E2E 覆蓋**——亦即 E2E 已經抓不到的錯誤類型開始出現在 production。
4. **admin 的總行數超過約 3000 行**，或有非本專案成員需要在不學 `el()` 的前提下貢獻畫面。

本 change 結束時 admin 約 1200 行、9 個模組、4 個視圖，四條都沒觸發。

### D2. 巢狀 SKU：每一列自己持有 `read()`，狀態住在 closure，DOM 只是輸出

這是 D1 能成立的技術核心，也是與 CMS 那條路徑的分水嶺。

```js
// lib/dom.js
export function repeater(host, renderRow) {
  const entries = [];
  function add(value) {
    let entry;
    const remove = () => { entry.node.remove(); entries.splice(entries.indexOf(entry), 1); };
    entry = renderRow(value, remove);   // → { node, read }
    entries.push(entry);
    host.append(entry.node);
    return entry;
  }
  return {
    add,
    reset(values) { host.textContent = ""; entries.length = 0; (values ?? []).forEach(add); },
    read() { return entries.map((e) => e.read()); },
  };
}
```

`renderRow` 回傳 `{ node, read }`：`node` 是要插進 DOM 的元素，`read()` 是一個**閉包在該列自己的 input 參考上**的函式。收集時走 `entries` 陣列呼叫每一列的 `read()`，**一次 DOM 查詢都不做**。

SKU 列因此長成這樣（略去屬性）：

```js
function skuRow(sku, remove) {
  const code = el("input", { value: sku?.sku_code ?? "" });
  const price = el("input", { type: "number", step: "1", min: "0", value: sku?.price_amount ?? 0 });
  const stock = el("input", { type: "number", step: "1", min: "0", value: sku?.stock_qty ?? 0 });
  ...
  const opts = repeater(optionsHost, optionRow);      // ← 巢狀：列裡面又是一個 repeater
  opts.reset(Object.entries(sku?.options ?? {}).map(([k, v]) => ({ k, v })));
  return {
    node: el("tr", {}, ...),
    read: () => ({
      id: sku?.id ?? undefined,          // 有 id → 更新既有 SKU；無 → 新建
      sku_code: code.value.trim(),
      options: Object.fromEntries(opts.read().filter((o) => o.k).map((o) => [o.k, o.v])),
      price_amount: parseAmount(price),  // 整數，不縮放（D4）
      ...
    }),
  };
}
```

巢狀之所以不會失控，是因為 `repeater` 對「列裡面有什麼」一無所知——它只認 `{ node, read }`。巢狀第二層與第一層用的是同一個 20 行函式，第三層也是。**與 (a) 的差別不在行數而在資訊流向**：(a) 把值寫進 DOM，再從 DOM 猜回來；(c) 讓值的來源始終是 JS 參考，DOM 只是它的投影。

`repeater` 直接服務下一個 change：出貨的品項列、運送方式列表、會員等級階梯全是同一個形狀。

### D3. 商品編輯**不**複用 schema 驅動表單

**決定**：`lib/schemaform.js` 原封不動只給 CMS 用；商品／SKU／分類用手寫的固定形狀表單。

**理由**：CMS 表單之所以 schema 驅動，是因為**它的形狀在撰寫 admin 時是未知的**——頁型 schema 由主題作者定義，admin 只能在執行期收到 JSON Schema 再長出控件。商品不是這樣：它的形狀是 `catalog.ProductInput` 這個 Go struct 與 `openapi.yaml` 裡的請求 schema，在撰寫 admin 時完全已知且穩定。

把商品塞進 schema 驅動表單需要：(1) 憑空發明一份商品 JSON Schema 並從伺服器送來——那等於把一個已經有 OpenAPI 契約的東西再描述一次，兩份會漂移；(2) 讓 CMS 的表單建構器長出只有目錄需要的控件（幣別感知的金額輸入、分類多選、SKU 表格）——CMS 從此扛著它用不到的複雜度；(3) 沿用 D1(a) 那條 label 比對的收集路徑，回到已經論證會壞的地方。

**`x-editor-order` 的反證**：change `cms-editor-field-order` 之所以要發明這個關鍵字，是因為 schema 存在 Postgres jsonb，而 jsonb 不保留物件鍵順序，撰寫順序無法還原。手寫表單的欄位順序就是 JS 原始碼的順序——**這個問題在固定形狀表單上根本不存在**。用 schema 驅動去做固定形狀的表單，等於把一個不存在的問題連同它的解法一起買進來。

兩者共用的只有 `el()` 與 `showValidationErrors()`（422 的 `details[].pointer` → 欄位標紅），後者對兩邊都成立，因為它依賴的是伺服器回的 pointer 而不是 schema。

### D4. 金額：任何路徑都不縮放，並由 E2E 機械化鎖住

**決定**：顯示 `formatMoney(3400, "TWD")` → `NT$3,400`；輸入 `<input type="number" step="1" min="0">` 收到的整數原封不動送出。**程式碼裡不存在任何 `/100` 或 `*100`。**

```js
export function formatMoney(amount, currency) {
  const n = new Intl.NumberFormat("zh-Hant-TW").format(amount);  // 只加千分位，不縮放
  return currency === "TWD" ? `NT$${n}` : `${n} ${currency}`;
}
```

**為什麼不用 `Intl.NumberFormat(..., { style: "currency" })`**：它會依 CLDR 的幣別小數位自行決定格式——TWD 在 CLDR 的預設小數位是 2，`format(3400)` 會產出 `NT$3,400.00`，看起來只是多兩個零，但它暗示了一個與本系統相反的單位語意（把 3400 當成 3400.00 元固然數值相同，卡在它下面的心智模型卻是「這是有小數的金額」，下一個人就會去補除法）。用 `style: "decimal"` 只求千分位、幣別符號自己加，語意就無從被誤解。

**為什麼不做 exponent 對照表**：`product-catalog` design D1 已定案——零小數貨幣（TWD/JPY）`price_amount` 就是元，二小數貨幣（USD）才是分；而「若後續真要支援非零小數貨幣，屆時建立一個 `money` package 集中處理，不要各 proposal 各自土法煉鋼」。前端自己長一張對照表正是那句話禁止的事。目前全系統只有 TWD（seed、demo、ONDO 皆然），非 TWD 一律顯示 `3,400 USD` 這種**不假裝知道小數位**的形式，比猜錯更誠實。

**機械化鎖住**：E2E 建立一個 `price_amount = 3400` 的 SKU，然後斷言列表頁的文字**包含 `NT$3,400`**，且**不包含 `34`（作為完整金額）或 `340000`**。除以 100 會產出 `NT$34`，乘以 100 會產出 `NT$340,000`，兩者都會讓斷言轉紅。這是本 change 唯一一個「寫錯不會噴錯」的地方，因此它必須有一條專屬斷言。

### D5. RBAC 感知 UI 需要一個新端點：`GET /api/v1/admin/shops/{shopID}/me`

**先確認是否真的缺**：`GET /api/v1/admin/me` 只回 `{id, email, sids}`（`adminauth.go`）。`GET /api/v1/admin/permissions` 是**平台層**端點（`RequirePlatformPermission`），商家 owner 呼叫會 403，而且它回的是權限**目錄**，不是呼叫者的有效權限。RBAC 的三層判定（個人覆蓋 → 角色聯集 → 預設拒絕）全在伺服器端，`rbac.Snapshot` 從不外洩。**確認缺。**

沒有它，UI 只剩兩條路：(1) 全部按鈕都顯示，讓伺服器回 403——正是任務要求排除的壞體驗，且對「刪除」這種破壞性操作特別糟（商家點下去、看到紅字、不知道是自己沒權限還是系統壞了）；(2) 靠「列表 GET 是否 403」反推——只推得出 `*.view`，推不出 create/edit/delete，除非真的發一次會改資料的請求。

**決定**：新增 `GET /api/v1/admin/shops/{shopID}/me` → `{"shop_id": 1, "permissions": ["category.view", ...]}`（排序後的節點名稱陣列）。

- **守衛只有「已認證 ＋ 跨店存取守衛」**，不要求任何權限節點。新增 `AuthzMW.RequireShopAccess()`：取 `Snapshot`、`CanAccessShop()` 為假回 403，然後放行。理由：**「我在這家店能做什麼」是自我資訊**，任何進得了這家店的人都必須答得出來；若改用某個既有節點（例如 `shop.view`）當守衛，一個只被授予 `product.view` 的自訂角色就會拿到 403 而看到一個空白後台——那是用權限機制打敗權限機制。它也不洩漏任何東西：呼叫者本來就能用「試一次」得到同樣的答案。
- **實作**：讀 `permissions` 表的節點目錄，對每個節點跑 `snap.Allows(name)`，回傳為真者。不硬編碼清單——新增權限節點時這個端點自動跟上。
- **為什麼不擴充 `/admin/me`**：`/admin/me` 不在 `{shopID}` 之下，而權限的答案依商家而異；要嘛加一個 `?shop_id=` 讓一個無 shop 概念的端點長出 shop 語意，要嘛回一張「每個 shop 一份」的地圖（對平台管理員是全表掃描）。掛在 `/shops/{shopID}` 之下語意最直、守衛最自然、也和既有的每一條商家層端點同構。
- **為什麼是 `/me` 而不是 `/permissions`**：`/shops/{shopID}/permissions` 讀起來像「這家店的權限目錄」；`/me` 明確是「在這個商家上下文中的我」，且日後要加該商家的角色名稱、顯示名等，有地方放。

`admin/lib/rbac.js` 在登入後與**每次切換商家後**呼叫它，把結果放進一個 `Set`；`can(node)` 就是 `set.has(node)`。UI 規則：無 `*.view` → 該分頁不出現；無 `*.create` → 新增按鈕不出現；無 `*.edit` → 表單唯讀且無儲存鍵；無 `*.delete` → 刪除鍵不出現。**這是體驗層，不是安全層**——伺服器端的 `RequireShopPermission` 依然是唯一的權威。

### D6. 商品列表新增選填 `q`

**先確認是否真的缺**：`catalog.ProductListParams` 只有 `Page`／`PageSize`／`CategoryID`／`Status`；`openapi.yaml` 的 `listProductsAdmin` 也只有這四個參數；`v1-surface.txt` 同樣只鎖了這四個。**確認缺。**

**為什麼不在前端過濾**：列表是分頁的，前端只拿得到當頁。對當頁做過濾會產生一個**主動誤導**的失敗模式——商家搜尋「母親節康乃馨」，它在第 3 頁，畫面顯示「找不到」，商家於是新建一個，撞上 slug 重複的 422，然後花二十分鐘搞清楚系統在說什麼。一個會說謊的搜尋框比沒有搜尋框更糟。

**決定**：新增選填 `q`，對 `title`、`slug`、以及該商品任一 SKU 的 `sku_code` 做大小寫不敏感子字串比對（ent 的 `ContainsFold` + `HasSkusWith`）。納入 `sku_code` 是因為商家對商品的另一個日常識別符就是 SKU 代碼。不帶 `q` 時查詢與變更前完全相同（`if params.Q != nil` 之外一行不動）。

**additive 的論證**：新增一個選填 query 參數對 v1 是 additive——舊呼叫端不受任何影響，`v1-surface.txt` 只增兩行（`… query q` 與 `… query q optional`），零刪除。這不使用 D13 的破例窗口（該窗口針對的是破壞性變更，已在 `order-extras` 用盡並關閉）。

**已知限制**：`ILIKE '%…%'` 走不到索引。單一商家的目錄規模（數十到數千）下不構成問題；真的需要時的路徑是 pg_trgm GIN 索引或全文檢索，屆時 API 契約不變。寫在這裡免得日後有人以為沒人想過。

### D7. 庫存就地在 SKU 列改，不做獨立的批次調整介面

**決定**：庫存是 SKU 列上的一個數字輸入，隨商品一起儲存。不做「批次調庫存」畫面。

**理由**：既有 API 沒有庫存專用端點——改庫存的唯一路徑是 `PUT /products/{id}` 帶完整 `skus`。要做批次介面就得新增 `PATCH /skus/{id}/stock` 之類的端點，那是**為了 UI 方便而擴張 API**，正是本 change 的硬約束禁止的事。而且庫存的真正需求（進貨單、盤點、負庫存告警、異動歷史）是一個獨立領域，不是一個表格加上「全選」按鈕；把它縮成一個批次表格會做出一個看起來夠用、實際上沒有稽核軌跡的東西。留給日後專屬的 change。

### D8. 商品 PUT 的全量語意是 UI 的第二個陷阱：永遠送完整的 `skus`

`UpdateProduct` 的巢狀 upsert 語意是：**帶 `id` 的更新、不帶 `id` 的新建、既有但未出現在酬載中的直接刪除**（`upsertSKUs`）。`category_ids` 同樣是全量取代。

這代表 UI 只要在儲存時漏送任何一列既有 SKU，那些 SKU 連同它們的價格與庫存就**靜默消失**——不會有錯誤、不會有警告。對應的實作紀律：

- 表單一律送 `repeater.read()` 的**完整結果**，`id` 從載入的資料原封帶回；絕不做「只送有變動的列」這種最佳化。
- 移除一列 = 從陣列消失 = 伺服器刪除。這正是我們要的語意，但 UI 上必須讓它看起來像刪除（`移除` 按鈕 + 儲存後生效），不能讓人以為只是「暫時隱藏」。
- 因此**不提供**「只儲存基本資料」的分開儲存鍵——一個不含 `skus` 的 PUT 是合法的（欄位省略即不變），但一個含部分 `skus` 的 PUT 會刪東西，兩者長得太像。一顆儲存鍵、一次送全部，最不容易錯。

### D9. 分類用平坦清單 + 父分類下拉，不做樹狀元件

`GET /categories` 預設回樹、`?flat=1` 回平坦陣列——**這個 `flat` 參數當初就是為了 admin 而存在的**（`categories.go` 的註解明說 "offered for admin UIs that prefer to build their own tree view"）。本 change 用 `?flat=1`：表格一列一個分類，父分類欄顯示父的名稱，編輯時父分類是一個 `<select>`（排除自己以避免明顯的環；真正的防環由伺服器 422 守著）。

不做樹狀縮排／拖拉排序：排序由 `position` 數字欄位表達（已存在），拖拉是 `product-catalog` design 明確留給「admin 前端後續迭代」的項目。刪除失敗（409：仍有子分類或仍有商品）直接把伺服器訊息顯示出來——那則訊息本來就寫成給人看的。

### D10. E2E 掛在既有 `happy-path.mjs`，不開第三個入口

`commerce-demo-seed` design D2 已經定案這件事：「一個沒人被告知要跑的第二入口，就是一套沒人跑的測試」。商品鏈需要瀏覽器（它驗的是 UI），所以放在 `happy-path.mjs` 的瀏覽器區段內，接在既有 CMS 流程之後、`runCommercePath()` 之前，共用 `lib.mjs` 的同一個失敗計數器。

覆蓋的路徑：登入 → 建立分類 → 建立含 2 個 SKU 的商品（**斷言金額顯示未被縮放**）→ 編輯其中一個 SKU 的庫存並儲存 → 驗證重新載入後庫存為新值（證明 D8 的全量送出沒有把另一個 SKU 弄丟）→ 發佈 → 打 `/api/v1/shop/products` 確認公開端點看得到、`stock_qty` 是新值。

**可重複執行**：identifier 帶 `runID`（`lib.mjs` 已提供），避免第二次執行撞 slug 唯一性。既有 CMS 區段本來就依賴乾淨的 seed 狀態，這一段不加深該依賴。

### D11. 契約與表面鎖

`openapi.yaml` 新增：`/api/v1/admin/shops/{shopID}/me`（含 `ShopPermissions` 具名 schema）、`listProductsAdmin` 的 `q` 參數。`v1-surface.txt` 以 `-openapi.update-surface` 重生後**逐行檢查 diff 只有新增**：預期新增 7 行（1 路由 + 1 path 參數 + `q` 的 2 行 + `ShopPermissions` 的 3 個屬性），零刪除。四道檢查中的 ①（路由覆蓋）會強制新端點必須入契約；②（金額 `$ref`）不受影響（新端點無金額欄位）；③（回應驗證）沿用既有取樣原則，新端點不強制入樣本。

## Risks / Trade-offs

- [拆檔搬移 CMS 程式碼可能弄壞既有 CMS 流程，而 admin 沒有型別檢查也沒有單元測試] → 搬移逐字進行、不順手重構；`happy-path.mjs` 既有的 CMS 斷言（sections 編輯器、422 定位、發佈、全域內容）正是這次搬移的迴歸測試，且它們在同一次執行中先於新斷言跑完。
- [新增目錄後 `admin/Dockerfile` 的 `COPY` 若漏改，image 會缺檔而後台在 production 白畫面，但本機 `python3 -m http.server` 完全正常] → Dockerfile 改為 `COPY . /usr/share/nginx/html/`，從此新增模組不需要記得改 Dockerfile；這個失敗模式因此被結構性消除而不是靠紀律。非靜態資源以 `admin/.dockerignore` 排除（`nginx.conf` 必須留在 context 供 `COPY` 到 nginx 設定目錄，改為在同一份 Dockerfile 內自 web root 刪除）。
- [`GET /shops/{shopID}/me` 的權限集合被前端快取，管理員在別處調整權限後 UI 不即時反映] → 只在登入與切換商家時抓；權限變更後重新登入或切換商家即更新。伺服器端 `RequireShopPermission` 永遠是權威，UI 過期只會顯示一顆按下去回 403 的按鈕，不會造成越權。
- [D8 的全量 SKU 語意仍是一把上膛的槍：任何日後新增的「快速編輯」入口只要送了部分 `skus` 就會靜默刪資料] → design 明文記載 + E2E 有一條專門驗「改了 SKU A 的庫存後 SKU B 仍在」的斷言；`admin-commerce-operations` 若要做批次操作，必須先讀 D8。
- [`q` 的 `ILIKE '%…%'` 無索引] → 已於 D6 記錄規模判斷與日後路徑；不在本 change 過早最佳化。
- [D1 的結論可能在 `admin-commerce-operations` 就被推翻，等於本 change 的拆檔白做] → 不會白做：D1 的四條推翻條件全都指向「改用框架」，而框架改寫的起點是一份已經按職責分好模組、狀態流向明確的程式碼，遠優於一份 2500 行的單檔。拆檔是任何一條路的共同前置。

## Migration Plan

純前端 + 兩處 additive API，無資料庫變更、無資料遷移。實作順序（對應 tasks.md）：

1. API additive：`AuthzMW.RequireShopAccess()`、`GET /shops/{shopID}/me`、`ListProducts` 的 `q`。
2. `openapi.yaml` + `v1-surface.txt` 重生，確認 diff 零刪除；`make openapi` 四道全綠。
3. `admin/` 拆檔（逐字搬移），`Dockerfile` 改 `COPY .`；跑既有 E2E 確認 CMS 流程未壞。
4. `lib/` 新詞彙（`repeater`、`money`、`rbac`）。
5. `views/categories.js`、`views/products.js`，以及 `index.html`／`styles.css`。
6. E2E 商品鏈。
7. 全套驗證（`make test`、`make test-int`、`make lint`、`make openapi`、重置 DB 後跑完整 E2E）。

回滾：admin 為靜態檔，回退 commit 即回滾；API 的兩處變更皆為 additive，回退不影響任何既有呼叫端（但 `v1-surface.txt` 的行會一起消失，那是回滾而非破壞）。

## Open Questions

- 前端路由／深層連結（`#/products/12`）——本 change 刻意不做（視圖只有 4 個，且 E2E 以點擊導航）。當商家開始需要把「某個商品的編輯畫面」貼給同事時就該做，屆時是 `lib/router.js` 約 30 行，不需要框架。
- 商品圖片——`meta.images` 目前只能填 URL，因為平台還沒有檔案儲存服務。這是 UI 的明顯缺口，但它的前置是一個 storage change，不是前端問題。
- 分類的拖拉排序與樹狀縮排——`position` 數字欄位可用但不好用；等 ONDO 實際的分類深度出來再決定值不值得。
- `q` 是否該一併給公開端點 `/api/v1/shop/products`（自帶前端的商品搜尋）——本 change 不做，因為沒有已知消費者提出需求；若 ONDO 的前台需要，那是一個獨立的 additive change。
