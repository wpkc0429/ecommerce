// Core group "cms" (CORE, locked): pages (list + editor) and the shop's global content. Every action checks permission
// first, the destructive one (deletePage) asks its own FIXED-warning confirmation, and uploads go through the core's
// media limits (lib/upload.js prepareImage) so the schema-form builder never sees a shop id, URL or token.
import { api, API_BASE } from "../../lib/api.js";
import { session } from "../../lib/session.js";
import { uploadContentImage } from "../../lib/upload.js";
import { need, needAny } from "../guard.js";
import { confirmDialog } from "../../ui/kit.js";
import { fixedWarning } from "../warnings.js";
import { t } from "../../lib/design.js";
import * as schemaForm from "../../ui/schemaform.js";

const base = () => `/admin/shops/${session.shopID}`;

const data = {
  /** [{id,title,slug,type_key,status,incompatible}] */
  async cmsPages() {
    need("page.view");
    return (await api("GET", `${base()}/pages`)).pages;
  },
  /** One page incl. page_schema / content_json / meta / status / incompatible. */
  async cmsPage(id) {
    need("page.view");
    return api("GET", `${base()}/pages/${Number(id)}`);
  },
  /** [{type_key}] for the new-page dialog. */
  async cmsPageTypes() {
    need("page.create");
    return (await api("GET", `${base()}/page-types`)).page_types;
  },
  /** {config_schema, content} */
  async cmsContent() {
    need("shop.view");
    return api("GET", `${base()}/content`);
  },
};

const actions = {
  async cmsCreatePage({ type_key, title, slug }) {
    need("page.create");
    return api("POST", `${base()}/pages`, { type_key, title, slug });
  },
  /** Returns the saved page (server-sanitized content_json). */
  async cmsSavePage(id, { content, meta }) {
    need("page.edit");
    return api("PUT", `${base()}/pages/${Number(id)}`, { content, meta });
  },
  async cmsPublishPage(id) {
    need("page.publish");
    return api("POST", `${base()}/pages/${Number(id)}/publish`);
  },
  async cmsUnpublishPage(id) {
    need("page.publish");
    return api("POST", `${base()}/pages/${Number(id)}/unpublish`);
  },
  /** Asks the confirmation itself; declined -> {cancelled:true} and no request. */
  async cmsDeletePage(id) {
    need("page.delete");
    const ok = await confirmDialog({
      title: t("confirm.deletePage.title"),
      message: t("confirm.deletePage.text"),
      warning: fixedWarning("deletePage", {}),
      confirmText: t("confirm.deletePage.yes"),
      cancelText: t("common.cancel"),
      danger: true,
    });
    if (!ok) return { cancelled: true };
    await api("DELETE", `${base()}/pages/${Number(id)}`);
    return { cancelled: false };
  },
  /** Opens the draft's render bundle in a new tab (old behaviour). */
  async cmsPreviewPage(id) {
    need("page.view");
    const d = await api("GET", `${base()}/pages/${Number(id)}/preview-token`);
    window.open(`${API_BASE}/api/v1/render/preview?token=${encodeURIComponent(d.preview_token)}`, "_blank");
  },
  async cmsSaveContent(content) {
    need("shop.update");
    return api("PUT", `${base()}/content`, { content });
  },
  /** Uploads an image for an `x-editor: image` field; media limits / HEIC conversion are enforced by the core. */
  async cmsUploadImage(file) {
    needAny("page.edit", "shop.update");
    return uploadContentImage(file);
  },
};

export default {
  data, actions, logic: {}, enums: {},
  ui: { schemaForm: { build: schemaForm.build, collect: schemaForm.collect, showValidationErrors: schemaForm.showValidationErrors } },
  format: {},
};
