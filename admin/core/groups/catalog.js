// Core group "catalog" (CORE, locked): categories, products (nested SKUs) and product images.
//
// THE PRODUCT RULE (read before editing saveProduct): the product PUT has full-replacement semantics. The server's nested
// upsert updates SKUs that carry an id, creates SKUs without one and DELETES existing SKUs missing from the payload;
// category_ids is replaced wholesale too. Therefore saveProduct
//   * always sends the complete skus array (a missing/non-array `skus` is refused, never defaulted to nothing);
//   * re-reads the product right before the PUT and refuses (stale) when it holds a SKU that is neither in the payload
//     nor explicitly listed in removedSkuIds (a screen that forgot a row, or a SKU added elsewhere since loading, must
//     never silently delete data), or when the payload names a SKU id the product no longer has;
//   * sends category_ids only when the caller may see categories (otherwise the wholesale replacement would wipe them);
//   * checks product.create / product.edit before any request.
// Images are a sub-resource with their own endpoints; they are never part of the product payload.
import { api } from "../../lib/api.js";
import { session } from "../../lib/session.js";
import { can } from "../../lib/rbac.js";
import { mediaConfig, mediaEnabled, uploadImage } from "../../lib/upload.js";
import { t } from "../../lib/design.js";
import { confirmDialog } from "../../ui/kit.js";
import { need } from "../guard.js";
import { ActionError, invalid } from "../errors.js";
import { fixedWarning } from "../warnings.js";

const PAGE_SIZE = 20;
const DEFAULT_MAX_IMAGES = 20;
const CANCELLED = Object.freeze({ cancelled: true });
const shop = () => `/admin/shops/${session.shopID}`;
const intOrZero = (v) => { const n = parseInt(v, 10); return Number.isFinite(n) ? n : 0; };
const posId = (v, what) => {
  const n = Number(v);
  if (!Number.isInteger(n) || n < 1) throw new ActionError("invalid", t("err.invalid"), { reason: what });
  return n;
};

const confirmDelete = (id, params) => confirmDialog({
  title: t(`confirm.${id}.title`, params),
  message: t(`confirm.${id}.text`, params),
  warning: fixedWarning(id, params),
  confirmText: t(`confirm.${id}.yes`, params),
  cancelText: t("common.cancel"),
  danger: true,
});

// ── data ───────────────────────────────────────────────────────────────
async function fetchFlatCategories() {
  const data = await api("GET", `${shop()}/categories?flat=1`);
  return data.categories || [];
}
async function listCategories() {
  need("category.view");
  return fetchFlatCategories();
}
/** The category choices for the product filter and the editor's category boxes: [] without category.view or on any failure. */
async function categoryChoices() {
  if (!can("category.view")) return [];
  try { return await fetchFlatCategories(); } catch { return []; }
}
async function categoryProductOrder(categoryId) {
  need("category.edit");
  const data = await api("GET", `${shop()}/categories/${posId(categoryId, "category")}/product-order`);
  return data.products || [];
}
/** {products, total, page, pageSize}. filters: {page, q, categoryId, status} (status "" = all). */
async function listProducts({ page = 1, q = "", categoryId = "", status = "" } = {}) {
  need("product.view");
  const params = new URLSearchParams({ page: String(Math.max(1, intOrZero(page) || 1)), page_size: String(PAGE_SIZE) });
  const keyword = String(q ?? "").trim();
  if (keyword) params.set("q", keyword);
  if (categoryId !== "" && categoryId !== null && categoryId !== undefined) params.set("category_id", String(categoryId));
  if (status !== "" && status !== null && status !== undefined) params.set("status", String(status));
  const data = await api("GET", `${shop()}/products?${params}`);
  return { products: data.products || [], total: data.total ?? 0, page: data.page ?? 1, pageSize: PAGE_SIZE };
}
async function getProduct(id) {
  need("product.view");
  return api("GET", `${shop()}/products/${posId(id, "product")}`);
}
async function listProductImages(productId) {
  need("product.view");
  return (await api("GET", `${shop()}/products/${posId(productId, "product")}/images`)).images || [];
}

// ── categories ─────────────────────────────────────────────────────────
/** form: {name, slug, position (raw text), parentId ("" = root | id)}. id null = create. */
async function saveCategory(id, form) {
  const editing = id !== null && id !== undefined;
  need(editing ? "category.edit" : "category.create");
  const body = {
    name: String(form?.name ?? "").trim(),
    slug: String(form?.slug ?? "").trim(),
    position: intOrZero(form?.position),
  };
  const f = {};
  if (!body.name) f["/name"] = t("categories.v.name");
  if (!/^[a-z0-9-]+$/.test(body.slug)) f["/slug"] = t("categories.v.slug");
  const parentRaw = form?.parentId === null || form?.parentId === undefined ? "" : String(form.parentId);
  if (parentRaw !== "" && !(Number.isInteger(Number(parentRaw)) && Number(parentRaw) > 0)) f["/parent_id"] = t("err.invalid");
  if (editing && parentRaw !== "" && Number(parentRaw) === Number(id)) f["/parent_id"] = t("categories.v.parentSelf");
  if (Object.keys(f).length) throw invalid(f);
  if (editing) {
    // parent_id omitted = unchanged; clear_parent = move to the root (the service's three-state semantics).
    if (parentRaw === "") body.clear_parent = true;
    else body.parent_id = Number(parentRaw);
    return api("PUT", `${shop()}/categories/${posId(id, "category")}`, body);
  }
  if (parentRaw !== "") body.parent_id = Number(parentRaw);
  return api("POST", `${shop()}/categories`, body);
}
async function deleteCategory(id) {
  need("category.delete");
  const cid = posId(id, "category");
  const found = (await fetchFlatCategories()).find((c) => c.id === cid);
  if (!(await confirmDelete("deleteCategory", { name: found ? found.name : `#${cid}` }))) return CANCELLED;
  await api("DELETE", `${shop()}/categories/${cid}`);
  return { ok: true };
}
/** Saves the manual product order of one category. The submitted ids must be a permutation of the server's current members. */
async function saveCategoryProductOrder(categoryId, productIds) {
  need("category.edit");
  const cid = posId(categoryId, "category");
  const ids = (Array.isArray(productIds) ? productIds : []).map(Number);
  if (ids.some((n) => !Number.isInteger(n) || n < 1)) throw new ActionError("invalid", t("err.invalid"), { reason: "order" });
  const fresh = (await api("GET", `${shop()}/categories/${cid}/product-order`)).products || [];
  const have = new Set(fresh.map((p) => Number(p.product_id)));
  if (ids.length !== have.size || new Set(ids).size !== ids.length || ids.some((n) => !have.has(n))) {
    throw new ActionError("stale", t("categories.order.stale"), { reason: "stale" });
  }
  await api("PUT", `${shop()}/categories/${cid}/product-order`, { product_ids: ids });
  return { ok: true };
}

// ── products ───────────────────────────────────────────────────────────
const plainOptions = (o) => (o && typeof o === "object" && !Array.isArray(o) ? o : {});
function normalizeSku(s) {
  if (!s || typeof s !== "object") throw new ActionError("invalid", t("err.invalid"), { reason: "sku" });
  const out = {
    sku_code: String(s.sku_code ?? "").trim(),
    options: plainOptions(s.options),
    price_amount: intOrZero(s.price), // integer minor units, never scaled
    currency: String(s.currency ?? "").trim().toUpperCase() || "TWD",
    stock_qty: intOrZero(s.stock),
    is_active: Boolean(s.is_active),
  };
  // A row that already exists keeps its id, else the server treats it as "absent" and deletes it.
  if (s.id !== undefined && s.id !== null) out.id = posId(s.id, "sku");
  return out;
}
/**
 * form: {title, subtitle, slug, description, status, skus: [{id?, sku_code, options, price, currency, stock, is_active}],
 *        removedSkuIds: number[] (existing SKUs the person deliberately removed), category_ids: number[] | null}.
 * productId null = create. Returns the saved product.
 */
async function saveProduct(productId, form) {
  const creating = productId === null || productId === undefined;
  need(creating ? "product.create" : "product.edit");
  if (!form || !Array.isArray(form.skus)) throw new ActionError("internal", t("products.err.skusMissing"), { reason: "skus" });
  const skus = form.skus.map(normalizeSku);
  const body = {
    title: String(form.title ?? "").trim(),
    subtitle: String(form.subtitle ?? "").trim(),
    slug: String(form.slug ?? "").trim(),
    description: String(form.description ?? ""),
    status: Number(form.status) === 1 ? 1 : 0,
    skus,
  };
  if (Array.isArray(form.category_ids) && can("category.view")) body.category_ids = form.category_ids.map(Number);
  if (creating) {
    if (skus.some((s) => s.id !== undefined)) throw new ActionError("invalid", t("err.invalid"), { reason: "sku" });
    return api("POST", `${shop()}/products`, body);
  }
  const pid = posId(productId, "product");
  const fresh = await api("GET", `${shop()}/products/${pid}`);
  const freshIds = new Set((fresh.skus || []).map((s) => s.id));
  const sentIds = new Set(skus.filter((s) => s.id !== undefined).map((s) => s.id));
  const removed = new Set((Array.isArray(form.removedSkuIds) ? form.removedSkuIds : []).map(Number));
  const unknown = [...sentIds].some((i) => !freshIds.has(i));
  const dropped = [...freshIds].some((i) => !sentIds.has(i) && !removed.has(i));
  if (unknown || dropped) throw new ActionError("stale", t("products.err.staleSkus"), { reason: "stale" });
  return api("PUT", `${shop()}/products/${pid}`, body);
}
async function deleteProduct(id) {
  need("product.delete");
  const pid = posId(id, "product");
  const p = await api("GET", `${shop()}/products/${pid}`);
  if (!(await confirmDelete("deleteProduct", { title: p.title }))) return CANCELLED;
  await api("DELETE", `${shop()}/products/${pid}`);
  return { ok: true };
}

// ── product images (sub-resource; limits and HEIC conversion live in lib/upload.js) ──
const imagesBase = (productId) => `${shop()}/products/${posId(productId, "product")}/images`;
const freshImages = async (productId) => (await api("GET", imagesBase(productId))).images || [];
/** Static media facts for the images section. */
function productMedia() {
  const cfg = mediaConfig();
  return { enabled: mediaEnabled(), maxImages: cfg.max_images_per_product || DEFAULT_MAX_IMAGES, maxMiB: Math.round((cfg.max_bytes || 0) / 1048576) };
}
/** One file chosen via <input type=file>: limit check against the server's current count, HEIC->JPEG, presign, direct PUT, create. */
async function uploadProductImage(productId, file) {
  need("product.edit");
  if (!mediaEnabled()) throw new ActionError("state", t("productImages.disabled"), { reason: "media" });
  if (!file || typeof file.size !== "number" || typeof file.name !== "string") throw new ActionError("invalid", t("err.invalid"), { reason: "file" });
  const max = productMedia().maxImages;
  if ((await freshImages(productId)).length >= max) throw new ActionError("limit", t("productImages.limit", { max }), { reason: "limit" });
  const uploadID = await uploadImage(session.shopID, "product_image", file);
  await api("POST", imagesBase(productId), { upload_id: uploadID, alt: "" });
  return { ok: true };
}
async function setProductImageAlt(productId, imageId, alt) {
  need("product.edit");
  await api("PUT", `${imagesBase(productId)}/${posId(imageId, "image")}`, { alt: String(alt ?? "") });
  return { ok: true };
}
/** Moves one image one step (delta < 0 up, > 0 down). The new full order is computed from the server's fresh list. */
async function moveProductImage(productId, imageId, delta) {
  need("product.edit");
  const ids = (await freshImages(productId)).map((im) => im.id);
  const i = ids.indexOf(Number(imageId));
  const j = i + (Number(delta) < 0 ? -1 : 1);
  if (i < 0 || j < 0 || j >= ids.length) return { ok: false };
  [ids[i], ids[j]] = [ids[j], ids[i]];
  await api("PUT", `${imagesBase(productId)}/order`, { image_ids: ids });
  return { ok: true };
}
async function deleteProductImage(productId, imageId) {
  need("product.edit");
  const iid = posId(imageId, "image");
  if (!(await confirmDelete("deleteImage", {}))) return CANCELLED;
  await api("DELETE", `${imagesBase(productId)}/${iid}`);
  return { ok: true };
}

export default {
  data: { listCategories, categoryChoices, categoryProductOrder, listProducts, getProduct, listProductImages },
  actions: {
    saveCategory, deleteCategory, saveCategoryProductOrder,
    saveProduct, deleteProduct,
    uploadProductImage, setProductImageAlt, moveProductImage, deleteProductImage,
  },
  logic: { productMedia },
  enums: {}, ui: {}, format: {},
};
