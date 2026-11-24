// 商品編輯器的「圖片」區塊（change product-images-r2）。
//
// 由 products.js 匯入（比照 order-shipments.js 的拆法，避免 products.js 越過
// tripwire #5 的約 400 行）。沒有新增任何 lib/ 抽象——用的是既有的 el()／clear()
// 與 can()，外加 lib/upload.js 的上傳流程（那是 CMS 圖片欄位也要用的共用部分）。
//
// 三件事在這個檔案裡是不可協商的：
//
//  1. **圖片是商品的子資源，操作即時生效**（design D6）。上傳、改 alt、重排、刪除
//     各自呼叫自己的端點，**不依賴商品的「儲存」鍵，也絕不放進商品 PUT 酬載**——
//     商品 PUT 是全量語意，圖片若進了那個酬載，任何沒載入圖片的舊版前端一存檔就會
//     靜默刪光圖片。
//  2. **重排送完整排列**。`PUT .../images/order` 的 image_ids 必須恰好是目前全部
//     圖片的一個排列，否則 422；所以這裡永遠從伺服器剛讀到的清單算新順序，
//     不是從畫面上的 DOM 反推。
//  3. **每次操作後整份重讀**（GET .../images），畫面是伺服器狀態的一次性投影，
//     沒有需要同步的第二份真相。
import { api } from "../lib/api.js";
import { el, clear } from "../lib/dom.js";
import { can } from "../lib/rbac.js";
import { mediaEnabled, mediaConfig, uploadImage } from "../lib/upload.js";

/**
 * 畫出（並維護）一個商品的圖片區塊，回傳 fieldset。
 * product 為 null（新建、尚未儲存）時只顯示說明——圖片需要商品 id。
 */
export function productImagesSection(product, shopID) {
  const box = el("fieldset", { class: "obj product-images", id: "product-images" }, el("legend", {}, "圖片"));
  const body = el("div", { class: "images-body" });
  box.append(body);

  if (!mediaEnabled()) {
    body.append(el("p", { class: "hint", id: "images-disabled" },
      "媒體儲存尚未設定，無法上傳圖片。請洽平台管理員設定物件儲存（R2）。"));
    return box;
  }
  if (!product) {
    body.append(el("p", { class: "hint" }, "請先儲存商品，才能新增圖片。"));
    return box;
  }

  const editable = can("product.edit");
  const base = `/admin/shops/${shopID}/products/${product.id}/images`;
  const errorBox = el("p", { class: "error", hidden: "" });
  const list = el("div", { class: "image-list" });
  let images = (product.images || []).slice();

  const fail = (e) => {
    errorBox.textContent = e?.message || String(e);
    errorBox.hidden = false;
  };

  async function reload() {
    try {
      images = (await api("GET", base)).images;
    } catch (e) {
      fail(e);
    }
    draw();
  }

  // 每個操作都包這一層：清錯誤 → 做 → 失敗顯示訊息 → 整份重讀。
  async function act(fn) {
    errorBox.hidden = true;
    try {
      await fn();
    } catch (e) {
      fail(e);
    }
    await reload();
  }

  function move(index, delta) {
    const ids = images.map((im) => im.id);
    const j = index + delta;
    if (j < 0 || j >= ids.length) return;
    [ids[index], ids[j]] = [ids[j], ids[index]];
    return act(() => api("PUT", `${base}/order`, { image_ids: ids }));
  }

  function draw() {
    clear(list);
    if (images.length === 0) {
      list.append(el("p", { class: "hint" }, "尚無圖片。"));
      return;
    }
    images.forEach((im, i) => {
      const alt = el("input", { type: "text", class: "image-alt", maxlength: "300", value: im.alt, placeholder: "替代文字（描述這張圖片）" });
      alt.disabled = !editable;
      alt.addEventListener("change", () => act(() => api("PUT", `${base}/${im.id}`, { alt: alt.value })));
      list.append(el("div", { class: "image-row", "data-image-id": im.id },
        el("img", { src: im.urls.thumb, alt: im.alt, class: "image-thumb", width: "96" }),
        el("div", { class: "image-meta" }, alt,
          el("span", { class: "hint" }, `${im.width} × ${im.height}`)),
        editable ? el("div", { class: "image-actions" },
          el("button", { type: "button", class: "tiny image-up", disabled: i === 0 ? "" : null, onclick: () => move(i, -1) }, "↑"),
          el("button", { type: "button", class: "tiny image-down", disabled: i === images.length - 1 ? "" : null, onclick: () => move(i, 1) }, "↓"),
          el("button", { type: "button", class: "tiny danger image-remove", onclick: () => {
            if (confirm("確定刪除這張圖片？")) act(() => api("DELETE", `${base}/${im.id}`));
          } }, "刪除")) : ""));
    });
  }

  body.append(errorBox, list);

  if (editable) {
    const max = mediaConfig().max_images_per_product || 20;
    const input = el("input", {
      type: "file", id: "product-image-input", multiple: "",
      // 刻意允許 HEIC：前端會先轉成 JPEG（lib/upload.js）。
      accept: "image/jpeg,image/png,image/webp,image/heic,image/heif,.heic,.heif",
    });
    const status = el("span", { class: "hint", id: "image-upload-status" });
    input.addEventListener("change", async () => {
      const files = Array.from(input.files || []);
      input.value = "";
      errorBox.hidden = true;
      for (const [n, file] of files.entries()) {
        if (images.length >= max) { fail(new Error(`每個商品最多 ${max} 張圖片。`)); break; }
        status.textContent = `上傳中（${n + 1}/${files.length}）：${file.name}`;
        try {
          const uploadID = await uploadImage(shopID, "product_image", file);
          await api("POST", base, { upload_id: uploadID, alt: "" });
          images = (await api("GET", base)).images;
          draw();
        } catch (e) {
          fail(new Error(`${file.name}：${e.message}`));
        }
      }
      status.textContent = "";
      await reload();
    });
    body.append(el("div", { class: "image-upload" },
      el("label", { class: "file-button" }, "＋ 上傳圖片", input), status,
      el("span", { class: "hint" }, `JPEG／PNG／WebP／HEIC，單檔上限 ${Math.round((mediaConfig().max_bytes || 0) / 1048576)} MiB，最多 ${max} 張。圖片操作即時生效，不需按「儲存」。`)));
  }

  draw();
  return box;
}
