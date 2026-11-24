// CMS 頁面清單與編輯器（原 app.js task 9.2/9.3，行為逐字搬移；只把「取得
// shopID」與「切換分頁」改為由 shell 注入的 ctx，並補上 page.* 權限閘）。
import { api, WEB_BASE } from "../lib/api.js";
import { $, el, clear } from "../lib/dom.js";
import { buildForm, collectForm, showValidationErrors } from "../lib/schemaform.js";
import { can, whenCan } from "../lib/rbac.js";

export function createPagesView(ctx) {
  const state = { editingPage: null };

  async function load() {
    const data = await api("GET", `/admin/shops/${ctx.getShopID()}/pages`);
    const tbody = clear($("#pages-tbody"));
    $("#new-page-btn").hidden = !can("page.create");
    for (const p of data.pages) {
      const status = p.status === 1
        ? el("span", { class: "badge ok" }, "已發佈")
        : el("span", { class: "badge" }, "草稿");
      const row = el("tr", {},
        el("td", {}, p.id),
        el("td", {}, p.title || "—"),
        el("td", {}, p.slug),
        el("td", {}, p.incompatible
          ? el("span", { class: "badge warn", title: "目前主題不支援此頁型" }, `${p.type_key} ⚠ 不相容`)
          : p.type_key),
        el("td", {}, status),
        el("td", {},
          el("button", { onclick: () => openEditor(p.id) }, "編輯"),
          el("button", { onclick: () => previewPage(p.id) }, "預覽"),
          whenCan("page.publish", () => p.status === 1
            ? el("button", { onclick: () => pageAction(p.id, "unpublish") }, "下架")
            : el("button", { class: "publish", onclick: () => pageAction(p.id, "publish") }, "發佈")),
          p.slug === "home" ? "" :
            whenCan("page.delete", () =>
              el("button", { class: "danger", onclick: () => deletePage(p.id) }, "刪除"))));
      tbody.append(row);
    }
  }

  async function pageAction(id, action) {
    try {
      await api("POST", `/admin/shops/${ctx.getShopID()}/pages/${id}/${action}`);
      await load();
    } catch (err) { alert(err.message); }
  }

  async function deletePage(id) {
    if (!confirm("確定刪除此頁面？")) return;
    try {
      await api("DELETE", `/admin/shops/${ctx.getShopID()}/pages/${id}`);
      await load();
    } catch (err) { alert(err.message); }
  }

  async function previewPage(id) {
    try {
      const data = await api("GET", `/admin/shops/${ctx.getShopID()}/pages/${id}/preview-token`);
      window.open(`${WEB_BASE}/preview?token=${encodeURIComponent(data.preview_token)}`, "_blank");
    } catch (err) { alert(err.message); }
  }

  async function openEditor(pageID) {
    const p = await api("GET", `/admin/shops/${ctx.getShopID()}/pages/${pageID}`);
    state.editingPage = p;
    ctx.showTab("editor");
    $("#editor-title").textContent = `編輯：${p.title || p.slug}`;
    $("#editor-status").textContent =
      (p.status === 1 ? "已發佈" : "草稿") + (p.incompatible ? " ｜ ⚠ 目前主題不支援此頁型" : "");
    $("#editor-error").hidden = true;
    const meta = p.meta || {};
    $("#seo-title").value = meta.seo_title || "";
    $("#seo-keywords").value = meta.seo_keywords || "";
    $("#seo-description").value = meta.seo_description || "";
    $("#editor-save").hidden = !can("page.edit");
    $("#editor-publish").hidden = !can("page.publish");
    $("#editor-unpublish").hidden = !can("page.publish");
    const form = $("#editor-form");
    if (p.page_schema) {
      buildForm(p.page_schema, p.content_json || {}, form, p.page_schema);
    } else {
      form.textContent = "此頁型不受目前主題支援，無法編輯內容。";
    }
  }

  // ── 新增頁面 dialog ──
  $("#new-page-btn").addEventListener("click", async () => {
    const types = await api("GET", `/admin/shops/${ctx.getShopID()}/page-types`);
    const sel = clear($("#new-page-type"));
    for (const t of types.page_types) sel.append(el("option", { value: t.type_key }, t.type_key));
    $("#new-page-error").hidden = true;
    $("#new-page-dialog").showModal();
  });
  $("#new-page-cancel").addEventListener("click", () => $("#new-page-dialog").close());
  $("#new-page-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
      const created = await api("POST", `/admin/shops/${ctx.getShopID()}/pages`, {
        type_key: $("#new-page-type").value,
        title: $("#new-page-title").value,
        slug: $("#new-page-slug").value,
      });
      $("#new-page-dialog").close();
      await load();
      openEditor(created.id);
    } catch (err) {
      const box = $("#new-page-error");
      box.textContent = err.message + (err.details?.length ? `（${err.details.map((d) => d.pointer).join(", ")}）` : "");
      box.hidden = false;
    }
  });

  // ── 編輯器動作 ──
  $("#editor-back").addEventListener("click", () => ctx.showTab("pages"));
  $("#editor-save").addEventListener("click", async () => {
    const p = state.editingPage;
    const form = $("#editor-form");
    const errBox = $("#editor-error");
    errBox.hidden = true;
    try {
      const saved = await api("PUT", `/admin/shops/${ctx.getShopID()}/pages/${p.id}`, {
        content: collectForm(form),
        meta: {
          seo_title: $("#seo-title").value,
          seo_keywords: $("#seo-keywords").value,
          seo_description: $("#seo-description").value,
        },
      });
      // 以伺服器實際存下的值重建表單：rich text 會在儲存時被消毒改寫
      // （change cms-rich-text-sanitization design D2），畫面必須顯示存下的那一份。
      if (p.page_schema && saved?.content_json) {
        p.content_json = saved.content_json;
        buildForm(p.page_schema, saved.content_json, form, p.page_schema);
      }
      $("#editor-status").textContent = "草稿已儲存 ✓（發佈後才會反映到前台）";
    } catch (err) {
      showValidationErrors(form, errBox, err);
    }
  });
  $("#editor-publish").addEventListener("click", async () => {
    const p = state.editingPage;
    try {
      await api("POST", `/admin/shops/${ctx.getShopID()}/pages/${p.id}/publish`);
      $("#editor-status").textContent = "已發佈 ✓";
    } catch (err) {
      showValidationErrors($("#editor-form"), $("#editor-error"), err);
    }
  });
  $("#editor-unpublish").addEventListener("click", async () => {
    const p = state.editingPage;
    try {
      await api("POST", `/admin/shops/${ctx.getShopID()}/pages/${p.id}/unpublish`);
      $("#editor-status").textContent = "已下架";
    } catch (err) { alert(err.message); }
  });
  $("#editor-preview").addEventListener("click", () => previewPage(state.editingPage.id));

  return { load };
}
