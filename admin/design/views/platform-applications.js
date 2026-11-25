// Platform view: shop application review (list, filters, detail, outbox messages, approve, reject).
import { pager } from "../parts/platform-common.js";

export default async function (root, ctx) {
  const { h, clear, rows } = ctx.ui;
  const t = ctx.t;
  const fmt = ctx.format;
  let page = 1;
  let status = "";
  let q = "";
  let detailID = null;
  let detail = null;
  let rejectID = 0;
  let loadSeq = 0;

  const badge = (s) => {
    const { label, cls } = fmt.platformApplicationStatus(s);
    return h("span", { class: `badge ${cls}`.trim() }, label);
  };

  const filter = h("select", { id: "platform-applications-filter", onchange: (e) => { status = e.target.value; page = 1; loadList(); } },
    h("option", { value: "" }, t("platform.apps.allStatuses")),
    ...["pending_review", "pending_verification", "approved", "rejected", "expired", "withdrawn"]
      .map((s) => h("option", { value: s }, t(`platform.appStatus.${s}`))));
  const qIn = h("input", { type: "search", id: "platform-applications-q", placeholder: t("platform.apps.searchPlaceholder") });
  const summary = h("p", { id: "platform-applications-summary", class: "hint" });
  const errBox = h("p", { id: "platform-applications-error", class: "error", hidden: true });
  const result = h("div", { id: "platform-application-result", class: "card", hidden: true });
  const tbody = h("tbody", { id: "platform-applications-tbody" });
  const pg = pager(ctx, "platform-applications", {
    onPrev: () => { page = Math.max(1, page - 1); loadList(); },
    onNext: () => { page += 1; loadList(); },
  });
  const detailHost = h("div", { id: "platform-application-detail", class: "card", hidden: true });

  // approve dialog
  const aName = h("input", { type: "text", id: "platform-application-approve-name", required: true });
  const aStatus = h("select", { id: "platform-application-approve-status" },
    h("option", { value: "2" }, t("platform.apps.approve.status2")),
    h("option", { value: "1" }, t("platform.apps.approve.status1")),
    h("option", { value: "0" }, t("platform.apps.approve.status0")));
  const aConfirm = h("input", { type: "checkbox", id: "platform-application-approve-confirm" });
  const aExisting = h("div", { id: "platform-application-approve-existing", hidden: true },
    h("p", { class: "error" }, t("platform.apps.approve.existing")),
    h("label", { class: "inline" }, aConfirm, " ", t("platform.apps.approve.confirm")));
  const aErr = h("p", { id: "platform-application-approve-error", class: "error", hidden: true });
  const approveDlg = h("dialog", { id: "platform-application-approve-dialog" },
    h("form", { id: "platform-application-approve-form", method: "dialog", onsubmit: submitApprove },
      h("h3", {}, t("platform.apps.approve.title")),
      h("label", {}, t("platform.apps.approve.name"), " ", aName),
      h("label", {}, t("platform.apps.approve.statusLabel"), aStatus),
      aExisting,
      h("p", { class: "hint" }, t("platform.apps.approve.hint")),
      aErr,
      h("div", { class: "dialog-actions" },
        h("button", { type: "button", id: "platform-application-approve-cancel", onclick: () => approveDlg.close() }, t("common.cancel")),
        h("button", { type: "submit", class: "primary" }, t("platform.apps.approve.submit")))));

  // reject dialog
  const rReason = h("textarea", { id: "platform-application-reject-reason", rows: "4", required: true, maxlength: "1000" });
  const rNote = h("textarea", { id: "platform-application-reject-note", rows: "2", maxlength: "2000" });
  const rErr = h("p", { id: "platform-application-reject-error", class: "error", hidden: true });
  const rejectDlg = h("dialog", { id: "platform-application-reject-dialog" },
    h("form", { id: "platform-application-reject-form", method: "dialog", onsubmit: submitReject },
      h("h3", {}, t("platform.apps.reject.title")),
      h("label", {}, t("platform.apps.reject.reason"), " ", rReason),
      h("label", {}, t("platform.apps.reject.note"), " ", rNote),
      h("p", { class: "hint" }, t("platform.apps.reject.hint")),
      rErr,
      h("div", { class: "dialog-actions" },
        h("button", { type: "button", id: "platform-application-reject-cancel", onclick: () => rejectDlg.close() }, t("common.cancel")),
        h("button", { type: "submit", class: "danger" }, t("platform.apps.reject.submit")))));

  root.append(h("div", { id: "tab-platform-applications", class: "tab-panel" },
    h("div", { class: "toolbar" },
      h("h2", {}, t("platform.apps.title")),
      h("div", { class: "filters" },
        filter, qIn,
        h("button", { id: "platform-applications-search", onclick: () => { q = qIn.value.trim(); page = 1; loadList(); } }, t("platform.accounts.search")))),
    h("p", { class: "hint" }, t("platform.apps.hint")),
    summary,
    errBox,
    result,
    h("table", { class: "table" },
      h("thead", {}, h("tr", {},
        ...["id", "status", "shop", "contact", "flags", "created", "actions"].map((k) => h("th", {}, t(`platform.apps.col.${k}`))))),
      tbody),
    pg.node,
    detailHost,
    approveDlg,
    rejectDlg));

  async function loadList() {
    const seq = ++loadSeq;
    errBox.hidden = true;
    let data;
    try {
      data = await ctx.data.platformApplicationsList({ page, status, q });
    } catch (e) {
      if (seq !== loadSeq || !ctx.alive()) return;
      errBox.textContent = e.message;
      errBox.hidden = false;
      return;
    }
    if (seq !== loadSeq || !ctx.alive()) return;
    pg.update(data.page, data.total, data.pageSize);
    summary.textContent = t("platform.apps.summary", {
      review: data.counts.pending_review || 0, verify: data.counts.pending_verification || 0, messages: data.pending_messages });
    rows(tbody, data.items, (a) => h("tr", { "data-id": a.id, "data-email": a.email },
      h("td", {}, a.id),
      h("td", {}, badge(a.status), " ",
        a.status === "pending_verification" || a.email_verified ? "" : h("span", { class: "badge" }, t("platform.apps.unverified"))),
      h("td", {}, a.shop_name || t("platform.apps.purged")),
      h("td", {}, a.contact_name || "", h("br"), h("small", {}, a.email)),
      h("td", {}, fmt.platformApplicationFlags(a.risk_flags)),
      h("td", {}, fmt.time(a.created_at)),
      h("td", {}, h("button", { "data-act": "open", onclick: () => openDetail(a.id) }, t("platform.apps.viewBtn")))),
    { empty: t("platform.apps.empty"), colspan: 7 });
  }

  async function openDetail(id) {
    detailID = id;
    const host = detailHost;
    clear(host).hidden = false;
    let d;
    try {
      d = await ctx.data.platformApplication(id);
    } catch (e) {
      host.append(h("p", { class: "error" }, e.message));
      return;
    }
    detail = d;
    const statusBox = h("p", { class: "error", hidden: true });
    const act = async (fn) => {
      statusBox.hidden = true;
      try { await fn(); } catch (e) { statusBox.textContent = fmt.platformErrorText(e); statusBox.hidden = false; }
    };
    const review = ctx.canPlatform("shop_application.review");
    const open = d.status === "pending_verification" || d.status === "pending_review";

    host.append(
      h("h3", {}, t("platform.apps.detail.title", { id: d.id }), badge(d.status)),
      ...[
        [t("platform.apps.detail.shop"), d.shop_name],
        [t("platform.apps.detail.contact"), d.contact_name],
        ["Email", `${d.email}${d.email_verified ? t("platform.apps.detail.verified") : t("platform.apps.detail.notVerified")}`],
        [t("platform.apps.detail.phone"), d.phone || "—"],
        [t("platform.apps.detail.summary"), d.business_summary],
        [t("platform.apps.detail.consent"), `${d.consent_version}（${fmt.time(d.consented_at)}）`],
        [t("platform.apps.detail.flags"), fmt.platformApplicationFlags(d.risk_flags) || "—"],
        ...(d.decision_reason ? [[t("platform.apps.detail.reason"), d.decision_reason]] : []),
        ...(d.internal_note ? [[t("platform.apps.detail.note"), d.internal_note]] : []),
        ...(d.provisioned_shop_id ? [[t("platform.apps.detail.provisioned"), t("platform.apps.detail.provisionedShop", { id: d.provisioned_shop_id })]] : []),
      ].map(([k, v]) => h("div", { class: "kv" }, h("span", { class: "kv-k" }, k), h("span", { class: "kv-v" }, v))),
      d.email_has_account && open ? h("p", { class: "error" }, t("platform.apps.detail.existingWarning")) : "",
      statusBox);

    if (review && open) {
      const bar = h("div", { class: "toolbar" });
      if (d.status === "pending_verification") {
        bar.append(h("button", { "data-act": "verify-manually", onclick: () => act(async () => {
          const r = await ctx.actions.platformVerifyApplicationManually(d.id);
          if (r.cancelled) return;
          await refresh();
        }) }, t("platform.apps.verifyBtn")));
      }
      if (d.status === "pending_review" && d.email_verified) {
        bar.append(h("button", { class: "primary", "data-act": "approve", onclick: () => openApprove(d) }, t("platform.apps.approve.submit")));
      }
      bar.append(h("button", { class: "danger", "data-act": "reject", onclick: () => openReject(d) }, t("platform.apps.reject.submit")));
      host.append(bar);
    }

    host.append(h("h4", {}, t("platform.apps.messages")));
    if (!d.messages.length) host.append(h("p", { class: "hint" }, t("platform.apps.noMessages")));
    for (const m of d.messages) {
      const copy = h("button", { type: "button" }, t("platform.apps.copyBody"));
      copy.addEventListener("click", async () => {
        if (await ctx.ui.copyText(m.body_text)) copy.textContent = t("platform.link.copied");
      });
      host.append(h("div", { class: "card", "data-message": m.id },
        h("p", {}, h("strong", {}, m.subject), "　→ ", m.recipient_email || t("platform.apps.purged"),
          "　", h("span", { class: m.status === "sent" ? "badge ok" : "badge warn" }, fmt.platformMessageStatus(m.status))),
        m.body_text ? h("pre", {}, m.body_text) : h("p", { class: "hint" }, t("platform.apps.bodyPurged")),
        m.error_reason ? h("p", { class: "error" }, m.error_reason) : "",
        m.body_text ? copy : "",
        review && m.status !== "sent"
          ? h("button", { "data-act": "mark-sent", onclick: () => act(async () => {
            await ctx.actions.platformMarkMessageSent({ applicationId: d.id, messageId: m.id });
            await refresh();
          }) }, t("platform.apps.markSent")) : ""));
    }

    host.append(h("h4", {}, t("platform.apps.timeline")));
    const tb = h("tbody", {});
    host.append(h("table", { class: "table" },
      h("thead", {}, h("tr", {}, h("th", {}, t("platform.apps.ev.time")), h("th", {}, t("platform.apps.ev.event")), h("th", {}, t("platform.apps.ev.actor")))), tb));
    rows(tb, d.events, (ev) => h("tr", { "data-event": ev.event },
      h("td", {}, fmt.time(ev.created_at)), h("td", {}, fmt.platformApplicationEvent(ev.event)),
      h("td", {}, ev.actor_user_id ? t("platform.apps.ev.account", { id: ev.actor_user_id }) : t("platform.apps.ev.system"))),
    { empty: t("platform.apps.ev.empty"), colspan: 3 });
  }

  async function refresh() {
    await openDetail(detailID);
    await loadList();
  }

  function openApprove(d) {
    aName.value = d.shop_name;
    aStatus.value = "2";
    aConfirm.checked = false;
    aExisting.hidden = !d.email_has_account;
    aErr.hidden = true;
    approveDlg.showModal();
  }

  async function submitApprove(e) {
    e.preventDefault();
    aErr.hidden = true;
    const d = detail;
    let res;
    try {
      res = await ctx.actions.platformApproveApplication({
        id: d.id, shopName: aName.value, status: aStatus.value, confirmExisting: aConfirm.checked });
    } catch (err) {
      aErr.textContent = fmt.platformErrorText(err);
      aErr.hidden = false;
      return;
    }
    approveDlg.close();
    const host = clear(result);
    host.hidden = false;
    host.append(h("h3", {}, t("platform.apps.approved", { name: res.shop.name, id: res.shop.id })));
    const linkHost = h("div", { class: "card" });
    host.append(linkHost);
    if (res.owner.created) {
      ctx.ui.platformLinkOnce(linkHost, { title: t("platform.owner.linkTitle", { email: res.owner.email }), link: res.owner });
    } else {
      linkHost.append(h("p", {}, t("platform.owner.existing", { email: res.owner.email })));
    }
    host.append(h("p", { class: "hint" }, t("platform.apps.approvedHint")));
    await refresh();
  }

  function openReject(d) {
    rReason.value = "";
    rNote.value = "";
    rErr.hidden = true;
    rejectID = d.id;
    rejectDlg.showModal();
  }

  async function submitReject(e) {
    e.preventDefault();
    rErr.hidden = true;
    try {
      await ctx.actions.platformRejectApplication({ id: rejectID, reason: rReason.value, note: rNote.value });
    } catch (err) {
      rErr.textContent = fmt.platformErrorText(err);
      rErr.hidden = false;
      return;
    }
    rejectDlg.close();
    await refresh();
  }

  loadList();
}
