// 商家全域內容（原 app.js task 9.3，行為逐字搬移）。
import { api } from "../lib/api.js";
import { $ } from "../lib/dom.js";
import { buildForm, collectForm, showValidationErrors } from "../lib/schemaform.js";
import { can } from "../lib/rbac.js";

export function createContentView(ctx) {
  async function load() {
    const data = await api("GET", `/admin/shops/${ctx.getShopID()}/content`);
    const form = $("#content-form");
    $("#content-save").hidden = !can("shop.update");
    if (data.config_schema) {
      buildForm(data.config_schema, data.content || {}, form, data.config_schema);
    } else {
      form.textContent = "商家尚未套用主題。";
    }
  }

  $("#content-save").addEventListener("click", async () => {
    const form = $("#content-form");
    const errBox = $("#content-error");
    errBox.hidden = true;
    try {
      await api("PUT", `/admin/shops/${ctx.getShopID()}/content`, { content: collectForm(form) });
      alert("已儲存，前台立即生效。");
    } catch (err) {
      showValidationErrors(form, errBox, err);
    }
  });

  return { load };
}
