// 圖片上傳：媒體設定、HEIC 轉檔、presign → 直傳（change product-images-r2）。
//
// 流程（每一個 admin 前端都要照做，見 docs/admin-frontend-integration.md）：
//
//   1. 選檔 → 若是 HEIC/HEIF，**先在瀏覽器內轉成 JPEG**（design D10）。伺服器只收
//      JPEG／PNG／WebP，HEIC 會被 422 拒絕。
//   2. POST /admin/shops/{id}/media/uploads（宣告 content_type、size_bytes、purpose）
//      → 取得 upload_id 與 presigned PUT 網址。
//   3. 瀏覽器 fetch PUT 到該網址，**原樣帶回傳的 headers**。不帶 Authorization：
//      這是直傳到物件儲存，不經過 API，簽章已經在網址裡。
//   4. 呼叫端再以 upload_id 建立商品圖片或 CMS 內容圖片（轉 WebP 由伺服器完成）。
//
// 本檔案**不做縮放、不轉 WebP**：輸出規格是伺服器的責任（design D4）；前端唯一的轉檔
// 是 HEIC→JPEG（品質 0.92、保留原始解析度），因為伺服器沒有 HEIC 解碼器。
import { api } from "./api.js";

const HEIC_TYPES = new Set(["image/heic", "image/heif", "image/heic-sequence", "image/heif-sequence"]);
const JPEG_QUALITY = 0.92;

let config = { enabled: false, public_base_url: "", max_bytes: 0, accepted_types: [] };

/** 開機／切換商家時呼叫；失敗視為「未設定」，上傳控件隱藏而不是報錯。 */
export async function loadMediaConfig() {
  try {
    config = await api("GET", "/admin/media/config");
  } catch {
    config = { enabled: false, public_base_url: "", max_bytes: 0, accepted_types: [] };
  }
  return config;
}

export const mediaConfig = () => config;
export const mediaEnabled = () => Boolean(config.enabled);

function isHeic(file) {
  return HEIC_TYPES.has((file.type || "").toLowerCase()) || /\.(heic|heif)$/i.test(file.name || "");
}

async function heicToJpegNative(file) {
  // Safari 可直接解碼 HEIC；其他瀏覽器的 createImageBitmap 會 reject。
  const bitmap = await createImageBitmap(file);
  const canvas = document.createElement("canvas");
  canvas.width = bitmap.width;
  canvas.height = bitmap.height;
  canvas.getContext("2d").drawImage(bitmap, 0, 0);
  bitmap.close?.();
  const blob = await new Promise((resolve) => canvas.toBlob(resolve, "image/jpeg", JPEG_QUALITY));
  if (!blob) throw new Error("canvas toBlob failed");
  return blob;
}

let heicLoader = null;
function loadHeic2any() {
  if (window.heic2any) return Promise.resolve(window.heic2any);
  if (!heicLoader) {
    heicLoader = new Promise((resolve, reject) => {
      const s = document.createElement("script");
      s.src = new URL("../vendor/heic2any.min.js", import.meta.url).href;
      s.onload = () => (window.heic2any ? resolve(window.heic2any) : reject(new Error("heic2any missing")));
      s.onerror = () => { heicLoader = null; reject(new Error("heic2any load failed")); };
      document.head.append(s);
    });
  }
  return heicLoader;
}

async function convertHeic(file) {
  try {
    return await heicToJpegNative(file);
  } catch {
    // fall through to the vendored decoder
  }
  const heic2any = await loadHeic2any();
  const out = await heic2any({ blob: file, toType: "image/jpeg", quality: JPEG_QUALITY });
  return Array.isArray(out) ? out[0] : out;
}

/**
 * 把使用者選的檔案變成「可以直接送去 presign 的 Blob」。失敗時丟出帶說明的 Error，
 * **不送任何請求**（spec admin-catalog-ui：HEIC 轉檔失敗時顯示說明）。
 */
export async function prepareImage(file) {
  let blob = file;
  let type = (file.type || "").toLowerCase();
  if (isHeic(file)) {
    try {
      blob = await convertHeic(file);
    } catch {
      throw new Error("無法轉換這張 HEIC 照片，請先在手機或電腦上把它轉成 JPEG 後再上傳。");
    }
    type = "image/jpeg";
  }
  const accepted = config.accepted_types?.length ? config.accepted_types : ["image/jpeg", "image/png", "image/webp"];
  if (!accepted.includes(type)) {
    throw new Error(`不支援的圖片格式（${type || "未知"}）。請使用 JPEG、PNG 或 WebP。`);
  }
  // 轉檔後重新檢查上限：HEIC→JPEG 可能比原檔大。
  if (config.max_bytes && blob.size > config.max_bytes) {
    throw new Error(`圖片過大：${(blob.size / 1048576).toFixed(1)} MiB，上限為 ${(config.max_bytes / 1048576).toFixed(0)} MiB。`);
  }
  return { blob, type };
}

/**
 * 完整的「選檔 → 轉檔 → presign → PUT」，回傳 upload_id，由呼叫端決定要拿去建立
 * 商品圖片還是 CMS 內容圖片。
 */
export async function uploadImage(shopID, purpose, file) {
  const { blob, type } = await prepareImage(file);
  const intent = await api("POST", `/admin/shops/${shopID}/media/uploads`, {
    content_type: type, size_bytes: blob.size, purpose,
  });
  // 刻意不用 api()：這是直傳到物件儲存，不能帶 Authorization，也不是 JSON。
  const res = await fetch(intent.upload_url, { method: "PUT", headers: intent.headers, body: blob });
  if (!res.ok) throw new Error(`上傳到儲存空間失敗（HTTP ${res.status}）`);
  return intent.upload_id;
}

// ── CMS 圖片欄位（schemaform 的 x-editor: image）────────────────────────
//
// schemaform 是從 schema 遞迴長出來的，沒有 ctx 可傳；目前商家由 shell 提供。
let shopIDSource = () => null;
export function setShopIDSource(fn) { shopIDSource = fn; }

/** 上傳一張 CMS 內容圖片，回傳要填進欄位的網址（large 變體）。 */
export async function uploadContentImage(file) {
  const shopID = shopIDSource();
  const uploadID = await uploadImage(shopID, "content_image", file);
  const res = await api("POST", `/admin/shops/${shopID}/media/content-images`, { upload_id: uploadID });
  return res.urls.large;
}
