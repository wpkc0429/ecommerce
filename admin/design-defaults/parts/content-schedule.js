// Scheduling panel of the document editor (used by the content group's editor view):
//   await renderSchedulePanel(ctx, slot, { key, draftRev, status, canPublish })
// It loads the document's schedule, shows its status and publish time (Asia/Taipei), and when canPublish offers
// schedule / reschedule / cancel. The schedule is bound to the reviewed draft_rev. It re-renders itself after every action.
const seqs = new WeakMap();

export async function renderSchedulePanel(ctx, slot, { key, draftRev, status, canPublish }, notice = "") {
  const { h, clear } = ctx.ui;
  const t = ctx.t;
  const seq = (seqs.get(slot) || 0) + 1;
  seqs.set(slot, seq);
  const fresh = () => ctx.alive() && seqs.get(slot) === seq;
  const again = (msg) => renderSchedulePanel(ctx, slot, { key, draftRev, status, canPublish }, msg || "");

  const msgBox = h("p", { class: "hint", id: "content-schedule-message", hidden: !notice }, notice);
  const errBox = h("p", { class: "error", id: "content-schedule-error", hidden: true });
  const showErr = (text) => { errBox.textContent = text || ""; errBox.hidden = !text; };

  let sch;
  try {
    sch = await ctx.data.contentDocumentSchedule(key);
  } catch (e) {
    if (!fresh()) return;
    clear(slot);
    slot.append(h("div", { id: "content-schedule-panel", class: "card" },
      h("h3", {}, t("publishing.schedule.title")),
      h("p", { class: "error" }, ctx.errorMessage(e, {}) || t("publishing.schedule.loadError"))));
    return;
  }
  if (!fresh()) return;

  const statusLabel = (s) => (ctx.enums.contentScheduleStatuses.includes(s) ? t(`publishing.status.${s}`) : String(s));
  const resultLabel = (r) => (r && ctx.enums.contentScheduleResults.includes(r) ? t(`publishing.result.${r}`) : "");
  const active = sch && (sch.status === "pending" || sch.status === "running");
  const pending = sch && sch.status === "pending";

  const lines = [];
  if (!sch) {
    lines.push(h("p", { id: "content-schedule-state", class: "hint" }, t("publishing.schedule.none")));
  } else if (active) {
    lines.push(h("p", { id: "content-schedule-state", "data-status": sch.status },
      h("span", { class: "badge warn" }, statusLabel(sch.status)), " ",
      t(pending ? "publishing.schedule.pending" : "publishing.schedule.running", { time: ctx.format.contentTaipeiTime(sch.publishAt), rev: sch.draftRev })));
  } else {
    lines.push(h("p", { id: "content-schedule-state", "data-status": sch.status },
      t("publishing.schedule.last", {
        time: ctx.format.contentTaipeiTime(sch.publishAt), status: statusLabel(sch.status), result: resultLabel(sch.result),
      })));
    if (sch.status === "superseded") lines.push(h("p", { class: "hint", id: "content-schedule-superseded" }, t("publishing.schedule.superseded")));
    if (sch.failurePointers.length) lines.push(h("p", { class: "error" }, t("publishing.schedule.pointers", { pointers: sch.failurePointers.join("、") })));
  }

  let form = "";
  if (canPublish) {
    const bounds = ctx.logic.contentScheduleBounds();
    const input = h("input", { type: "datetime-local", id: "content-schedule-time", required: true, min: bounds.min, max: bounds.max });
    if (pending) input.value = ctx.logic.contentScheduleInputValue(sch.publishAt);
    const submit = h("button", { type: "submit", class: "primary", id: "content-schedule-submit" }, t(pending ? "publishing.schedule.resubmit" : "publishing.schedule.submit"));
    const cancel = pending
      ? h("button", { type: "button", class: "danger", id: "content-schedule-cancel", onclick: doCancel }, t("publishing.schedule.cancel"))
      : "";
    form = h("form", { id: "content-schedule-form", class: "inline-form", onsubmit: doSchedule },
      h("label", { class: "inline" }, t("publishing.schedule.inputLabel"), " ", input), submit, cancel);

    async function doSchedule(e) {
      e.preventDefault();
      showErr("");
      submit.disabled = true;
      try {
        const r = await ctx.actions.contentSchedulePublish({ key, localDateTime: input.value, draftRev });
        const when = ctx.format.contentTaipeiTime(r.schedule.publishAt);
        await again(t(r.created ? "publishing.schedule.saved" : "publishing.schedule.rescheduled", { time: when }));
      } catch (err) {
        if (!fresh()) return;
        submit.disabled = false;
        showErr(ctx.errorMessage(err));
      }
    }

    async function doCancel() {
      showErr("");
      cancel.disabled = true;
      try {
        const r = await ctx.actions.contentScheduleCancel(key);
        if (r.cancelled) { if (fresh()) cancel.disabled = false; return; }
        await again(t("publishing.schedule.cancelled"));
      } catch (err) {
        if (!fresh()) return;
        cancel.disabled = false;
        showErr(ctx.errorMessage(err));
      }
    }
  }

  clear(slot);
  slot.append(h("div", { id: "content-schedule-panel", class: "card", "data-key": key },
    h("h3", {}, t("publishing.schedule.title")),
    ...lines,
    canPublish ? "" : h("p", { class: "hint" }, t("publishing.schedule.readOnly")),
    form,
    msgBox,
    errBox,
    h("p", { class: "hint" }, t("publishing.schedule.hint"))));
}
