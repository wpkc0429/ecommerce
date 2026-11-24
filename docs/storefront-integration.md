# 自帶前端接入說明

給**不由平台託管**的前端：商家自己的靜態站、SPA、或原生 APP。
第一位讀者是 ONDO 溫度花室（`bouquet`，零建置流程的 vanilla JS + Cloudflare Pages），
但這份文件寫的是通則，不綁死任何一個消費者。

**分工，請先讀這一段：**

| 你要知道的事 | 看哪裡 |
| --- | --- |
| **每個欄位叫什麼、什麼型別、可不可以是 null** | `api/openapi/openapi.yaml`（**唯一權威**，由四道自動化測試守著） |
| **這些端點怎麼串成一條路徑** | 本文件 |

本文件**不重複列舉欄位表**——那會製造第二個會腐爛的地方，而**本文件不在任何測試的覆蓋範圍內**，
`openapi.yaml` 則是。兩者衝突時以 `openapi.yaml` 為準。

---

## 0. 先讀這一段：金額是「最小貨幣單位的整數」

**這是接入本 API 最容易犯、而且最貴的錯。**

API 回傳的每一個金額都是**最小貨幣單位的整數**，幣別由同一層的 `currency`（ISO 4217）決定：

```jsonc
{ "price_amount": 3400, "currency": "TWD" }   // NT$3,400  ← 不是 NT$34
```

- `TWD` 的最小貨幣單位是**元** → `3400` 就是 **NT$3,400**，直接顯示，**不要除以 100**。
- `JPY` 同理。
- `USD` 的最小貨幣單位是**分** → 同樣一筆錢會是 `340000`。

所以「要不要除以 100」取決於 `currency`，不是一個可以硬編碼的常數。
單一 TWD 商家的正確作法是**把這個整數直接當作元**。

搞錯的後果不會噴任何錯誤，只會**賣錯價**——差 100 倍。

還有兩件事：

- **永遠不會是浮點數，也永遠不會是字串。**
- **不要自己算合計。** 訂單的 `total_amount` 就是應付總額
  （`subtotal_amount − discount_amount + shipping_fee`），也是金流實際請款的金額。
  這個等式由資料庫約束保證恆等，前端重算只會遲早不一致——而不一致的那一邊會是顧客看到的那一邊。
  **`discount_amount` 是會員等級折扣**，見第 9.5 節；沒有折扣時它是 `0`，等式退化成你熟悉的那個。

---

## 1. 平台端要先幫你準備兩樣東西

這兩件事由**平台管理員**在後台完成，不是前端能自助的。

### ① publishable key（識別碼，不是憑證）

```
POST /api/v1/admin/shops/{shopID}/publishable-keys   { "label": "ondo-prod" }
```

回應的 `key` 形如 `pk_e7gagijdzmaxsrxwm3nfoarlrbwf3dav`，就是交付值。

**可以直接硬編碼在前端 JS 裡。** 它是公開識別碼：明文儲存、後台可完整顯示、
出現在原始碼與 git 中是預期用法。**不需要**環境變數注入、**不需要**打包時替換，
洩漏也不是資安事件。它只回答「你代表哪一家商家」，**不決定任何權限**。

**輪替**：先產生第二把 → 前端更新部署 → `DELETE .../publishable-keys/{keyID}` 撤銷舊的。
撤銷**即時生效**，過渡期內兩把都有效。

### ② 允許的瀏覽器 origin（CORS）

```
POST /api/v1/admin/shops/{shopID}/allowed-origins   { "origin": "https://ondo.example.com" }
```

**平台層權限**（`shop.manage_client_origins`），商家不能自助——origin 是全域競爭的命名空間。

- 未登記的 origin 一律拿不到 `Access-Control-Allow-Origin`，瀏覽器端會全數失敗。
- **不支援萬用字元。** 正式網域與需要用到的**固定** preview 網域各登記一筆；
  Cloudflare Pages 那種隨機 hash 的 preview 網域無法涵蓋。
- **判別法**：如果 `curl` 帶同一組標頭會成功、瀏覽器卻失敗，那就是 origin 沒登記。

---

## 2. 每個請求都要帶的東西

```js
const API = "https://api.<platform-domain>";
const PUBLISHABLE_KEY = "pk_…";               // 硬編碼即可

const headers = { "X-Publishable-Key": PUBLISHABLE_KEY };
```

- **不要送 `X-Site-Domain` / `X-Site-Path`。** 前台 API 已不再讀取它們（送了不會出錯，也不會有任何作用）。
- 會員登入後的請求**同時**帶 `Authorization: Bearer <access_token>` **與**金鑰——
  金鑰決定「哪一家商家」，token 決定「哪一位顧客」，兩者不互相取代。

---

## 3. 讀商品

```js
const res = await fetch(`${API}/api/v1/shop/products`, { headers });
const { products, page, page_size, total } = await res.json();
```

- **無需任何認證。**
- 只回傳**已發佈**的商品。
- 每個商品帶 `skus`（**依 `id` 遞增排序**，公開與後台一致）。
  **下單要用的是 `products[].skus[].id`**，不是商品 id，也不是你自己前端的購物車 key。
- 單一商品：`GET /api/v1/shop/products/{slug}`。
- `subtitle`：商品卡片的第二行（例如英文品名），沒有時是空字串 `""`。語言由商家決定，平台不解讀。
- `categories`：`[{id, slug, name}]`，依分類 `position`、`id` 排序；`category_ids` 是同一個集合的 id 版本。

### 3.1 分類與「依分類列商品」

```js
// 整棵分類樹（無需認證、不分頁）：{ categories: [{ id, name, slug, parent_id, position, children: [...] }] }
const tree = await (await fetch(`${API}/api/v1/shop/categories`, { headers })).json();

// 某分類的商品（含它的所有子分類）
const res2 = await fetch(`${API}/api/v1/shop/products?category=new-arrivals`, { headers });
```

- `category` 的值是分類的 **slug**。回傳屬於該分類**或其任何子孫分類**的已發佈商品，每個商品只出現一次；
  `total` 與分頁都是篩選後的數字。
- **排序**：先列直接屬於該分類的商品（依商家在後台「商品排序」設定的順序），再列只經由子分類到達的商品（依 `id`）。
  不帶 `category` 時照舊依 `id`。
- slug 不存在（或拼錯）**不是錯誤**：回 200 與空列表，建置時拼錯 slug 會得到空頁而不是失敗。
- **「本季新品」「母親節限定」這類區塊就是分類**，不是商品上的旗標：商家建一個分類、把商品掛上去，
  你用 `?category=<slug>` 取用即可；換季時商家把商品移出分類。

---

## 4. 讀運送方式與運費

```js
const res = await fetch(`${API}/api/v1/shop/shipping-methods`, { headers });
const { shipping_methods } = await res.json();
// [{ id: 1, name: "台北市當日配送", carrier: "自有車隊", flat_rate: 200 }, …]
```

- **無需任何認證**——買家必須在還沒有任何身分之前就看得到運費。
- **只回傳啟用中的方式**。商家在後台停用某方式，前端下次抓就看不到它（不必自行過濾）。
- **不要硬編碼運費。** `flat_rate` 是最小貨幣單位整數，幣別同商品。
- API 沒有「12:00 前下單當日送達」這類文案欄位——那是文案，留在前端；
  `carrier`（如「黑貓冷藏」）可以拿來顯示。
- 前端的選擇狀態要存 API 的 `id`，送出時送 `shipping_method_id`。

---

## 5. 訪客結帳（不需要登入）

購物車可以完全留在前端，**不需要任何 `/cart/*` 呼叫**。按下「送出訂單」時做一次請求：

```js
const res = await fetch(`${API}/api/v1/shop/guest/checkout`, {
  method: "POST",
  headers: {
    ...headers,
    "Content-Type": "application/json",           // 沒有 Authorization
    "Idempotency-Key": state.submitKey,           // 選填但**強烈建議**，見第 5.5 節
  },
  body: JSON.stringify({
    email: form.email,                       // 必填：這是顧客身分的鍵
    items: cart.map(l => ({
      sku_id: l.skuId,                       // ← products[].skus[].id
      quantity: l.qty,
      gift_message: l.msg || "",             // ← 卡片訊息掛在「每個品項」上
    })),
    shipping_address: {
      recipient_name: form.name,             // 必填
      phone: form.phone,                     // 必填
      line1: form.addr,                      // 必填
      // city / postal_code / country 皆為選填，台灣的單行地址直接放 line1
    },
    shipping_method_id: state.shipId,        // 選填，訂單層
    requested_delivery_date: "2026-08-05",   // 選填，訂單層，嚴格 YYYY-MM-DD
  }),
});
```

### 三個容易踩的點

**① `email` 是必填，而且它就是顧客身分。**
系統會在**本商家**找出或建立一個**無密碼身分**綁定這筆訂單。顧客日後以同一 email 註冊即「認領」它
（見第 8 節）。同一個 email 在別家商家是完全不相干的另一個身分。

**② 卡片訊息是購物車合併鍵的一部分。**
API 的合併鍵是 `(sku_id, gift_message)`：**同一款花配兩張不同卡片是兩個品項，不是數量 2**。
前端的去重鍵**必須把訊息算進去**，否則第二張卡片會覆蓋第一張。
上限 **500 個字元**（以字元計，中英文一樣算 500）；沒有卡片就送空字串或整個省略，回應裡永遠是字串（不會是 `null`）。

**③ 送出按鈕要在請求期間停用，而且要帶 `Idempotency-Key`。**
停用按鈕擋的是雙擊；**擋不住網路逾時後的重試**，而那才是真正會建立兩筆訂單的路徑。
兩者都要做，見第 5.5 節。

### 還有

- **卡號絕對不要送到本 API。** 付款一律走第 6 節的 `redirect_url` 導向金流。
- `requested_delivery_date` 必須是嚴格的 `YYYY-MM-DD`（建議用 `<input type="date">`）。
  伺服器端只有一條規則：不得是「在任何時區都已過去」的日期。**沒有**前置天數、截單時刻、
  最遠可預訂日——那些是你自己的營業規則，要擋請在前端擋。
- `shipping_method_id` 不送 ＝ 免運（`shipping_fee = 0`）。

### 回應（201）

```jsonc
{
  "order": {
    "id": 1,
    "status": 0, "payment_status": 0, "fulfillment_status": 0,
    "currency": "TWD",
    "subtotal_amount": 6800,   // 品項小計（折扣前）
    "discount_amount": 0,      // 會員等級折扣的折抵金額（第 9.5 節），無折扣時為 0
    "discount_percent": null,  // 當時套用的折扣率；不屬於任何等級時為 null
    "member_tier_name": null,  // 折扣所依據的等級名稱；同上
    "shipping_fee": 200,       // 運費
    "total_amount": 7000,      // 應付總額 ＝ 小計 − 折抵 ＋ 運費 ＝ 金流請款金額
    "shipping_method_name": "台北市當日配送",
    "requested_delivery_date": "2026-08-05",
    "items": [{ "gift_message": "給媽媽", /* … */ }]
  },
  "order_access_token": "eyJhbGciOiJIUzI1NiIs…",
  "token_type": "Bearer",
  "expires_in": 86400
}
```

- `order.id` 就是訂單編號（可取代前端自己隨機產生的訂單號）。
- `order_access_token` 請存起來（`sessionStorage` / `localStorage` 皆可），有效 24 小時。
- **`order_access_token` 在極少數情況下可能不存在**：訂單已成立但簽發權杖失敗時，
  伺服器仍回 201 並附上訂單（告訴顧客「你的訂單沒成立」是更糟的謊）。前端要容忍這個鍵缺席——
  此時顧客要查訂單，路徑是以同一 email 註冊。

### 金額怎麼顯示才會跟 API 一致

- **送出前**（結帳畫面）：API 還不知道這張單，只能由前端預估——用自己算的小計 ＋
  第 4 節拿到的 `flat_rate`。這是「顯示」，不是「權威」。
  **訪客結帳無法預估等級折扣**（原因見第 9.5 節），所以結帳畫面不要承諾折扣。
- **送出後的任何畫面**（訂單確認頁、訂單查詢、寄給顧客的訊息）：**一律用回應的 `total_amount`**，
  並在有折扣時（`discount_amount > 0`）把折抵列出來——否則顧客會發現金額對不上他算的。

---

## 5.5 安全地重送：`Idempotency-Key`

### 為什麼需要它（不是為了雙擊）

雙擊由「送出中就停用按鈕」解決。真正的問題是另一個：**回應遺失時，你分不出
「請求根本沒送到」與「請求送到了、訂單建好了、只有回應在回程掉了」。**

顧客看到「訂單送出失敗，請檢查網路」，唯一合理的動作就是再按一次——
而你沒有任何辦法先確認訂單存不存在（你沒有訂單 id，那正是遺失的那個回應要帶回來的東西；
而訪客也沒有可以拿去查訂單的登入 session）。

**這個分辨只有伺服器做得到。** 帶上 `Idempotency-Key`，重送就變成安全操作。

> 這條端點不帶 key 時**每次都建一筆新訂單**，這是刻意保留的預設行為。
> 兩條結帳端點都接受它：`POST /api/v1/shop/guest/checkout` 與 `POST /api/v1/shop/checkout`。

### key 怎麼產生

```js
// 進入結帳流程時產生一次，整個流程（含所有重試）沿用同一把。
function newSubmitKey() {
  return crypto.randomUUID?.()
    ?? `k-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
}
```

- **8–255 個可見 ASCII 字元**，不符會得到 `422 invalid_idempotency_key`。
- **不要求 UUID**，上面兩種形式都可以。
- **不要用固定值、不要用遞增數字。** 太短或太可預測會與同商家其他顧客相撞，
  而相撞的後果會落在**對方**身上（他結不了帳）。
- **每次進入結帳產生新的一把；下單成功後丟掉。**
  存進 `localStorage` 忘了清，會讓顧客隔天的真實訂單被靜默地當成重試——
  沒有訂單、沒有錯誤，兩邊都看不出來。

### 重試邏輯

```js
async function submitOrder(key, payload) {
  const bodyText = JSON.stringify(payload);   // ← 重試時必須送「同一串字」
  const res = await fetch(`${API}/api/v1/shop/guest/checkout`, {
    method: "POST",
    headers: { ...headers, "Content-Type": "application/json", "Idempotency-Key": key },
    body: bodyText,
    signal: AbortSignal.timeout(15000),
  });

  if (res.status === 201) {
    // 第一次成功，或是一次回放——兩者刻意無法分辨，而且你不需要分辨。
    return { ok: true, order: (await res.json()).order };
  }
  if (res.status === 409) {
    // idempotency_key_in_progress：同一把 key 的另一個請求還在跑。
    // 稍等 1–2 秒重試（同一把 key），會拿到回放或一次全新的結帳。
    return { ok: false, retryable: true };
  }
  if (res.status === 422) {
    // 欄位錯誤：讓顧客修正後**沿用同一把 key** 再送（失敗不佔用 key）。
    return { ok: false, retryable: false, error: await res.json() };
  }
  // 逾時、5xx、或 fetch 直接 reject：結果未知 → 重試是安全的（同一把 key）。
  return { ok: false, retryable: true };
}
```

**重試 MUST 送出逐位元組相同的請求主體。** 從同一份 state 重新 `JSON.stringify` 就是相同的；
若你在重試前改了任何內容，那已經是另一筆訂單意圖，請產生新的 key。
用舊 key 配新內容會得到 `422 idempotency_key_reused`。

### 回放時你會看到什麼

**和第一次一模一樣**：`201` ＋ 同樣形狀的主體 ＋ 同一個 `order.id`。這是刻意的——
冪等的定義就是「重試的表現要像原本那次請求」，所以你的 `if (status === 201)` 分支不必改。

兩個差異：

- 回應多一個 **`Idempotency-Replayed: true`** 標頭（只用來讓你驗證接線，不必據此改變邏輯）。
- `order_access_token` 是**重新簽發**的一枚（原本那枚不被儲存，且可能已過期）。
  它指向同一筆訂單、能做的事完全相同，`expires_in` 永遠描述**這次回應裡這一枚**。
- 訂單內容是**當下**的狀態：期間若已付款或已被取消，回放會如實反映。

### 失敗不會佔用 key

驗證失敗（422）、庫存不足、JSON 壞掉（400）之後，**同一把 key 立刻可以重用**。
「顧客改好欄位再送一次」就是靠這個運作的。

### 有效期

預設 **24 小時**，唯一性以**商家**為界（同一把 key 在別家商家是完全不相干的一列）。
超過之後同一把 key 會建立一筆**新**訂單——那時它已經不是重試了。

### 常見錯誤

| 現象 | 原因 |
| --- | --- |
| 瀏覽器 CORS 錯誤，提到 `Idempotency-Key` | 該 origin 沒登記（第 1 節②）。平台已在允許的標頭清單中包含它 |
| `422 invalid_idempotency_key` | key 太短（< 8）、有空白／換行／中文 |
| `422 idempotency_key_reused` | 重試時主體變了（或換了顧客／換了端點）。要嘛送回原本的位元組，要嘛用新 key |
| `409 idempotency_key_in_progress` | 同一把 key 的另一個請求還在跑。等一下再用**同一把** key 重試 |
| 重送卻建了第二筆訂單 | 沒帶標頭，或每次重試都產生了新的 key |
| 顧客隔天下單卻拿回舊訂單 | key 被跨訂單重用了（例如存進 `localStorage` 沒清） |

---

## 6. 付款

```js
const pay = await fetch(`${API}/api/v1/shop/guest/order/payments`, {
  method: "POST",
  headers: { ...headers, "Content-Type": "application/json", "Authorization": `Bearer ${orderAccessToken}` },
  body: JSON.stringify({ provider: "ecpay" }),   // 省略則使用平台預設
});
const { redirect_url, redirect_form, amount } = await pay.json();   // 201
```

- **路徑裡沒有訂單 id**——權杖已經指名了那一筆訂單。傳 id 也沒有用（沒有這個參數）。
- `amount` 等於訂單的 `total_amount`（**已扣折扣、已含運費**）。金額由伺服器決定，請求無法指定。
- **付款結果不會經由這個轉導回來。** 權威是金流服務商送到平台的伺服器回呼。
  顧客轉導回你的網站後，請**重新查一次訂單**看 `payment_status`。

### 送顧客過去有兩種形式，**先看 `redirect_form`**

金流服務商有兩種收單方式，回應同時帶了兩個鍵，只有一個有意義：

```js
if (redirect_form) {
  submitForm(redirect_form);        // 表單 POST（綠界 ECPay 走這條）
} else {
  window.location.href = redirect_url;   // 單純轉導
}
```

**順序不能顛倒。** 需要表單 POST 的服務商其 `redirect_url` 是**空字串**——先讀它會得到
`window.location.href = ""`，也就是重新整理當前頁面，顧客永遠到不了收銀台。

### `submitForm` 的完整實作（可直接複製）

```js
function submitForm({ method, action, fields }) {
  const form = document.createElement("form");
  form.method = method;          // "POST"
  form.action = action;          // 金流服務商的收銀台網址
  for (const [name, value] of Object.entries(fields)) {
    const input = document.createElement("input");
    input.type = "hidden";
    input.name = name;
    input.value = value;
    form.appendChild(input);
  }
  document.body.appendChild(form);
  form.submit();
}
```

- **`fields` 原樣送出，一個都不能少、一個都不能改。** 裡面有一個檢查碼欄位涵蓋其餘所有欄位；
  少送、多送或改動任何一個值，服務商都會拒絕這筆交易。**不要自己重排、過濾或轉型。**
- **API 刻意不回傳一段現成的 HTML。** 你的框架已經處理好逸出，而且自帶前端不一定是網頁
  （原生 APP 拿欄位集可以直接丟給系統瀏覽器或 WebView）。
- **不要用 iframe。** 綠界明列 iframe 會導致交易失敗；iOS 上 FB／LINE 的 in-app 瀏覽器同樣不支援。
- 若你的站有 CSP，記得 `form-action` 要允許收銀台的網域。

---

## 7. 查訂單與出貨

```
GET /api/v1/shop/guest/order             → 該筆訂單（含 payment_status / fulfillment_status）
GET /api/v1/shop/guest/order/shipments   → 該筆訂單的**所有**出貨（清單；尚未出貨時 200 ＋ 空陣列）
GET /api/v1/shop/guest/order/shipment    → **最近一筆**出貨（單一物件；尚未出貨時 404）
```

同樣帶 `X-Publishable-Key` ＋ `Authorization: Bearer <order_access_token>`，同樣**不帶訂單 id**。

### 一筆訂單可以有多箱（change partial-shipment）

商家可以分多次出貨（花瓶今天走、鮮花週五走），所以：

- **請用複數的 `/shipments`。** 它回 `{"shipments": [...]}`，**依出貨編號遞減**（最新的在最前面）。每一筆帶 `items`（`order_item_id` ＋ `quantity`），也就是「這箱裝了什麼」；品項名稱請用 `order_item_id` 回訂單的 `items` 裡查，出貨紀錄刻意不重複一份商品文案。
- **單數的 `/shipment` 仍然可用**，語意是「最近一筆」。單箱訂單的回應與過去逐位元組相同；多箱時它只看得到最新那箱。
- 訂單的 `items[]` 新增 `shipped_quantity`：這一行**已經出貨的數量**。`quantity - shipped_quantity` 就是還沒出的。

### `fulfillment_status` 新增了值 `4`

| 值 | 意思 |
| --- | --- |
| 0 | 未出貨 |
| 1 | 已出貨（**全部**品項都出了） |
| 2 | 已送達（**每一箱**都送達了） |
| 3 | 已退貨（**任一箱**被標記退貨） |
| **4** | **部分出貨**（有些品項出了，有些還沒）← change partial-shipment 新增 |

既有四個值的意思**一個字都沒變**，`4` 是新增的。若你的前端用陣列索引取標籤，請補上第五個元素——**索引 4 沒補會顯示 `undefined`，不會崩，但也不會正確**。`4` 排在最後而不是插在 `1` 之前，因為既有值的數值不可以動。

### 這枚權杖能做什麼、不能做什麼

| 能 | 不能 |
| --- | --- |
| 讀取它指名的那一筆訂單 | 列出訂單歷史（`GET /api/v1/shop/orders` 回 401） |
| 對那一筆訂單發起付款 | 取消訂單 |
| 讀那一筆訂單的出貨資訊（單數與複數皆可） | 讀取／修改顧客資料、密碼 |
| | 讀取或兌換點數 |
| | 換發或延長自己（沒有 refresh） |

**它不是登入。** 引入訪客結帳不會讓你的前端獲得會員 session。

---

## 8. 24 小時後顧客要查訂單：叫他用同一個 email 註冊

```
POST /api/v1/shop/auth/register   { "email": "…", "password": "…" }   → TokenResponse
```

訂單從建立的那一刻就綁在這個 email 的顧客身分上，所以註冊完成後
`GET /api/v1/shop/orders` 與 `GET /api/v1/shop/points` **立刻**看得到全部歷史訂單與累積點數，
不需要任何「綁定訂單」的動作。這是訪客結帳自動建身分的全部意義。

**必須知道的一件事：** 目前系統**沒有 email 驗證能力**，所以「以同一 email 註冊即取得先前的訪客訂單」
是**未經驗證**的關聯——理論上猜到 email 又搶先註冊的人會看到那些訂單的收件資訊。
**顧客說明文案不要寫成「我們驗證了你的 email」。**

註冊失敗一律是 `422 registration_failed` 且**不說明原因**（不洩漏該 email 是否已是本店顧客）。
唯二會說明原因的是格式問題：email 格式、密碼長度不足 8。

### 會員路徑（可選）

若要做完整會員體驗，還有：登入 `POST /api/v1/shop/auth/login`、
購物車 `/api/v1/shop/cart*`、會員結帳 `POST /api/v1/shop/checkout`（品項來自伺服器端購物車）、
訂單列表 `/api/v1/shop/orders`、點數 `/api/v1/shop/points`。形狀見 `openapi.yaml`。

`refresh_token` 是**一次性**的：每次換發都會旋轉，舊值立即失效。

---

## 9. 點數要怎麼跟顧客說

**點數以「實付商品金額」計算：`subtotal_amount − discount_amount`。運費不計點，等級折抵也不計點。**

- 6800 的花 ＋ 200 運費、無折扣 → **68 點**（不是 70）。門市自取與當日配送拿到的點數一樣多。
- 6800 的花、銀卡 5%（折抵 340）＋ 200 運費 → `6800 − 340 = 6460` → **64 點**（不是 68，也不是 70）。

文案**不要**寫「消費 100 元 1 點」——那會被理解成含運費。
寫「**實付商品金額**每 100 元 1 點（運費不計）」。

顧客會問「為什麼我升級了點數反而變少」：因為折扣本身就是那個等級的回饋，
點數是按他實際付的商品金額算的。這句話要先寫進 FAQ，不要等客服被問。

---

## 9.5 會員等級折扣

商家可以在後台為每個會員等級設定 `discount_percent`（0–100）。顧客結帳時，
**伺服器**會依他當下所屬的等級自動套用，前端不需要（也無法）做任何事。

### 規則，一次講完

```
discount_amount = floor(subtotal_amount × discount_percent / 100)
total_amount    = subtotal_amount − discount_amount + shipping_fee
```

- **只吃商品金額，運費不打折。** 同一束花不論選哪種配送，折抵金額都一樣。
- **整數運算，無條件捨去。** `3333 × 10% = 333.3` → 折 **333**（不是 334）。
  這與點數的 `floor(⋯/100)` 是同一個方向——本 API 只有一種捨入方式。
- **一筆訂單至多一個等級折扣**，不疊加、沒有優惠券、沒有滿額折扣。
- **訂單上的折扣是快照。** 商家事後調整或刪除該等級、顧客升降級，
  **已成立訂單的金額一個字都不會變**。

### 顧客怎麼拿到等級

點數累積到門檻就自動升級（第 9 節）。**這對訪客結帳同樣成立**：
同一個 email 的第二筆訂單就可能帶折扣，因為第一筆付款已經替他累積了點數。
自帶前端沒有登入 UI 也沒關係——折扣認的是 email 對應的顧客身分，不是登入狀態。

### 結帳前要不要顯示折扣

| 情境 | 做法 |
| --- | --- |
| **會員（已登入）** | `GET /api/v1/shop/points` 回應的 `level.discount_percent` 就是他的折扣率。用**上面那條一模一樣的整數公式**預估，結果會與伺服器完全一致。 |
| **訪客（未登入）** | **沒有辦法，而且是刻意的。** 平台不提供「用 email 查這個人折幾折」的端點——那會讓任何人用一個請求就問出某個 email 在這家店是什麼等級。 |

訪客的落差是顧客**付得比預估的少**，方向與「少收運費」相反，不會讓人覺得被多收。
建議在結帳畫面寫一句「會員回饋將於訂單成立後套用」，然後在確認頁如實顯示 `discount_amount`。

### 交付給顧客的文案

「折扣以**商品金額**計算，運費不折」與「點數以**實付商品金額**計算」要一起寫，
兩句都缺一不可——只寫其中一句，另一個數字就會變成客服問題。

---

## 10. 錯誤對照表

所有錯誤共用同一個 envelope：

```jsonc
{ "error": { "code": "validation_failed", "message": "…", "details": [ { "pointer": "/items/1/gift_message", "message": "…" } ] } }
```

`details[].pointer` 是**相對於請求主體**的 JSON Pointer——這是你定位錯誤欄位唯一的資訊。

### 依狀態碼

| 狀態 | `code` | 意義與處置 |
| --- | --- | --- |
| 400 | `bad_request` | JSON 壞掉、body 超過 1 MiB。程式 bug，不是使用者問題。 |
| 401 | `unauthorized` | token 缺少／過期／`aud` 不符（跨商家重用）。金鑰沒問題時才會走到這裡。 |
| 404 | `unknown_publishable_key` | **金鑰打錯、未帶、或已被撤銷。** |
| 404 | `not_found` | 資源不存在；訪客端點帶了 token 仍 404，多半是路徑打錯（訪客端點**不接受**訂單 id），或該訂單尚未出貨。 |
| 409 | `conflict` | 與訂單當下狀態衝突（對已取消／已付款的訂單發起付款）。 |
| 409 | `idempotency_key_in_progress` | 同一把 `Idempotency-Key` 的另一個請求還在跑。**稍後以同一把 key 重試是安全的**（無 `Retry-After`）。 |
| 422 | `validation_failed` | 見下表的 pointer 對照。 |
| 422 | `registration_failed` | 註冊失敗，**刻意不說明原因**。 |
| 422 | `checkout_unavailable` | 該 email 的顧客身分已被商家停用，**刻意不說明原因**。 |
| 422 | `invalid_idempotency_key` | `Idempotency-Key` 不是 8–255 個可見 ASCII 字元。 |
| 422 | `idempotency_key_reused` | 同一把 key 配上**不同的**請求主體（第 5.5 節）。 |
| 429 | `rate_limited` | 短時間內太多次；`Retry-After` 標頭是秒數。 |
| 500 | `internal` | 平台端問題，**任何端點都可能出現**。 |
| 503 | `shop_disabled` | 商家被停用（金鑰與網域同時失效）。前台應呈現維護頁而非錯誤頁。 |

### 依 `details[].pointer`（`validation_failed`）

| pointer | 意義 |
| --- | --- |
| `/email` | email 缺漏或格式不合法 |
| `/items/{i}/sku_id` | 該 SKU 不屬於本店／已停用／商品未發佈／不存在 |
| `/items/{i}/quantity` | 數量 ≤ 0 或超過現有庫存 |
| `/items/{i}/gift_message` | 卡片訊息超過 500 個字元 |
| `/shipping_address/recipient_name`<br>`/shipping_address/phone`<br>`/shipping_address/line1` | 這三個是必填（其餘地址欄位皆選填） |
| `/shipping_method_id` | 運送方式不存在／屬於別家商家／**已被商家停用**（重新抓一次第 4 節） |
| `/requested_delivery_date` | 不是 `YYYY-MM-DD`、不是真實存在的日期（如 `2026-02-31`）、或明顯是過去的日期 |
| `/cart` | 購物車是空的 |

### 其他症狀

| 現象 | 意義 |
| --- | --- |
| 瀏覽器 CORS 錯誤，但伺服器沒有回錯 | **origin 未登記**（第 1 節②）。`curl` 帶同一組標頭會成功，這是判別依據。 |
| 合計比預期少了運費 | 忘了送 `shipping_method_id`（不送＝免運）。 |
| 合計比自己算的少 | 顧客有會員等級折扣（第 9.5 節）。看 `discount_amount`／`member_tier_name`，**不要重算**。 |
| 商家設了折扣但顧客沒享到 | 該顧客還沒累積到那一級的點數門檻（`GET /shop/points` 看 `level`），或他是全新 email。 |
| 折扣少了一塊錢 | 無條件捨去（第 9.5 節）。`3333 × 10%` 折 333 不是 334。 |
| 顧客抱怨升級後點數變少 | 點數以**折扣後**的商品金額計算（第 9 節），這是規則不是 bug。 |
| 金額差了 100 倍 | 讀第 0 節。 |
| 第二張卡片覆蓋了第一張 | 前端的購物車去重鍵沒有把 `gift_message` 算進去（第 5 節②）。 |
| 網路逾時後重送，結果建了兩筆訂單 | 沒帶 `Idempotency-Key`，或每次重試都產生新 key（第 5.5 節）。 |
| 顧客隔天下單卻拿回昨天那筆訂單 | key 被跨訂單重用了（第 5.5 節）。 |

---

## 11. 離線開發：用 mock server，不必起整套後端

```bash
make openapi-mock     # npx @stoplight/prism-cli mock api/openapi/openapi.yaml → :4010
```

把 `API` 指到 `http://localhost:4010` 就能開發，不需要 Go、PostgreSQL 或 Redis。
Prism 依契約的 schema 與 `example` 產生回應，並且會**驗證你送進去的 request**——
少一個必填欄位、型別錯了，它會直接告訴你。

契約裡的範例值一律使用**真實量級**（花藝品項 3200–4400），所以量級搞錯會在畫面上一眼看穿。

想看可瀏覽的文件：

```bash
make openapi-ui       # npx @redocly/cli preview-docs api/openapi/openapi.yaml → :8081
```

兩者都是開發時選用，不在 CI、不在建置、不進任何相依清單。

---

## 12. 這份契約不會腐爛（以及它擋不住什麼）

`api/openapi/openapi.yaml` 由四道自動化檢查守著，全部在平台的 CI 裡：

| 檢查 | 擋住 |
| --- | --- |
| 路由覆蓋 | 平台新增／移除端點而沒有更新契約 |
| 金額單位紀律 | 新增金額欄位而沒有聲明「最小貨幣單位」 |
| 真實回應驗證 | 回應新增／移除鍵、改型別、狀態碼改變 |
| v1 表面鎖 | 靜默移除任何一條路由或公開回應鍵 |

**擋不住的：**

- **語意層面的改變**（欄位名字與型別都沒動，但意思變了）。這類變更會另行公告。
- **你這一端的誤解**——例如把 `3400` 當成 34 元。契約測試跑在平台 repo 裡，到不了你的 JS。
  這就是第 0 節放在最前面的原因。
- 本文件（markdown）本身**不在任何測試覆蓋範圍內**。欄位以 `openapi.yaml` 為準。

### v1 的相容性承諾

- **會發生**：新增端點、新增選填請求鍵、**新增回應鍵**。
- **不會發生**：移除或改名任何已公開的路徑或回應鍵。

所以：**請忽略未知的回應鍵。** 契約的回應 schema 標了 `additionalProperties: false`，
那是為了讓平台的契約測試抓得到「實作多回了一個沒記載的鍵」；
它描述的是「截至本版本，回應有這些鍵」，**不是**「未來永遠只會有這些鍵」。
把契約檔釘住並嚴格驗證的前端，會在下一次純新增的變更中壞掉。
