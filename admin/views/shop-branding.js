// 商家設定表單的品牌欄位（change admin-api-public-exposure, design D8）。
// 住在報表視圖旁的「商家設定」區（與時區同屬一處）；寫入走既有的
// PUT /settings（shop.update）。只送 admin_branding，所以時區不受影響。
import { api } from "../lib/api.js";
import { $ } from "../lib/dom.js";
import { can } from "../lib/rbac.js";
import { applyBranding } from "../lib/branding.js";

export function createBrandingForm(ctx, showError) {
  const path = (suffix) => `/admin/shops/${ctx.getShopID()}${suffix}`;
  const form = $("#branding-form");

  async function load() {
    form.hidden = !can("shop.update");
    if (form.hidden) return;
    try {
      const b = (await api("GET", path("/branding"))).branding;
      $("#branding-logo").value = b.logo_url;
      $("#branding-primary").value = b.primary_color;
      $("#branding-accent").value = b.accent_color;
    } catch (e) {
      showError(e.message);
    }
  }

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    $("#branding-saved").hidden = true;
    const admin_branding = {
      logo_url: $("#branding-logo").value.trim(),
      primary_color: $("#branding-primary").value.trim(),
      accent_color: $("#branding-accent").value.trim(),
    };
    try {
      const res = await api("PUT", path("/settings"), { admin_branding });
      applyBranding(res.admin_branding);
      $("#branding-saved").hidden = false;
    } catch (err) {
      showError(err.details?.[0]?.message ? `${err.message}：${err.details[0].message}` : err.message);
    }
  });

  return { load };
}
