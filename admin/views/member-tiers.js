// 會員等級 CRUD（change admin-commerce-operations）。
//
// **這個畫面上沒有任何金額。** min_points 是「點」、discount_percent 是「%」，
// 兩者都不得經過 formatMoney（design D10）——點數與金額之間有一個真實換算
// （核發是 floor(subtotal_amount / 100)），所以把 34 點畫成 NT$34 會完全合理地
// 錯下去，而且不會有任何東西噴錯。
//
// discount_percent 本階段只儲存、不參與任何計價（spec member-tiers-and-points），
// 畫面必須說出這件事，否則商家會以為設了就會打折。
import { api, ApiError } from "../lib/api.js";
import { $, el, rows } from "../lib/dom.js";
import { formatPoints, formatPercent } from "../lib/money.js";
import { can, whenCan } from "../lib/rbac.js";

export function createMemberTiersView(ctx) {
  const state = { list: [], editing: null };

  async function load() {
    const err = $("#member-tiers-error");
    err.hidden = true;
    $("#new-member-tier-btn").hidden = !can("member_tier.create");
    try {
      const data = await api("GET", `/admin/shops/${ctx.getShopID()}/member-tiers`);
      state.list = data.member_tiers || [];
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
      return;
    }
    // 階梯的語意是「門檻最高且不超過餘額的那一級」，由低到高排讀起來才像階梯。
    const ladder = [...state.list].sort((a, b) => a.min_points - b.min_points);
    rows($("#member-tiers-tbody"), ladder, (t) => el("tr", { "data-tier": t.name },
      el("td", {}, t.id),
      el("td", {}, t.name),
      el("td", { class: "points" }, formatPoints(t.min_points)),
      el("td", {}, formatPercent(t.discount_percent ?? 0)),
      el("td", {},
        whenCan("member_tier.edit", () => el("button", { onclick: () => openDialog(t) }, "編輯")),
        whenCan("member_tier.delete", () =>
          el("button", { class: "danger", onclick: () => remove(t) }, "刪除")))),
    { empty: "尚未建立任何會員等級。顧客累積點數後不會被歸級。", colspan: 5 });
  }

  function openDialog(tier) {
    state.editing = tier ?? null;
    $("#member-tier-dialog-title").textContent = tier ? `編輯等級 #${tier.id}` : "新增會員等級";
    $("#member-tier-name").value = tier?.name ?? "";
    $("#member-tier-min-points").value = tier?.min_points ?? 0;
    $("#member-tier-discount").value = tier?.discount_percent ?? 0;
    $("#member-tier-error").hidden = true;
    $("#member-tier-dialog").showModal();
  }

  async function submit(e) {
    e.preventDefault();
    const box = $("#member-tier-error");
    box.hidden = true;
    const body = {
      name: $("#member-tier-name").value.trim(),
      min_points: parseInt($("#member-tier-min-points").value, 10) || 0,
      discount_percent: parseInt($("#member-tier-discount").value, 10) || 0,
    };
    try {
      const base = `/admin/shops/${ctx.getShopID()}/member-tiers`;
      if (state.editing) await api("PUT", `${base}/${state.editing.id}`, body);
      else await api("POST", base, body);
      $("#member-tier-dialog").close();
      await load();
    } catch (err) {
      box.textContent = err.message +
        (err instanceof ApiError && err.details.length
          ? `（${err.details.map((d) => `${d.pointer} ${d.message}`).join("；")}）` : "");
      box.hidden = false;
    }
  }

  async function remove(tier) {
    if (!confirm(`確定刪除等級「${tier.name}」？目前屬於此等級的會員會在下次點數異動時重新歸級。`)) return;
    const err = $("#member-tiers-error");
    err.hidden = true;
    try {
      await api("DELETE", `/admin/shops/${ctx.getShopID()}/member-tiers/${tier.id}`);
      await load();
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
    }
  }

  $("#new-member-tier-btn").addEventListener("click", () => openDialog(null));
  $("#member-tier-cancel").addEventListener("click", () => $("#member-tier-dialog").close());
  $("#member-tier-form").addEventListener("submit", submit);

  return { load };
}
