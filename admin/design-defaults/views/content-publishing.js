// Content publishing: scheduled publishing overview (content.publish) and the publish hook settings (content.manage).
// The hook URL is write-only: it is typed into the input, sent once and the input is cleared; it is never rendered back.
// The signing secret is shown ONCE after it is generated (in this panel only) and is never stored.
export default async function (root, ctx) {
  const { h, clear, rows } = ctx.ui;
  const t = ctx.t;
  const canSchedules = ctx.can("content.publish");
  const canHook = ctx.can("content.manage");

  const label = (list, prefix, v) => (list.includes(v) ? t(`${prefix}.${v}`) : String(v));
  const statusBadge = (s) => h("span", { class: s === "failed" || s === "missed" ? "badge warn" : (s === "done" ? "badge ok" : "badge") },
    label(ctx.enums.contentScheduleStatuses, "publishing.status", s));
  const time = (iso) => ctx.format.contentTaipeiTime(iso);

  // ── scheduled publishing ──────────────────────────────────────────────
  let schedSeq = 0;
  const schedErr = h("p", { id: "publishing-schedules-error", class: "error", hidden: true });
  const upcomingBody = h("tbody", { id: "publishing-upcoming-tbody" });
  const recentBody = h("tbody", { id: "publishing-recent-tbody" });
  const th = (k) => h("th", {}, t(`publishing.schedules.col.${k}`));
  const schedBlock = h("div", { id: "publishing-schedules-block", hidden: !canSchedules },
    h("div", { class: "toolbar" },
      h("h2", {}, t("publishing.schedules.title")),
      h("button", { type: "button", id: "publishing-schedules-refresh", onclick: () => loadSchedules() }, t("publishing.schedules.refresh"))),
    h("p", { class: "hint" }, t("publishing.schedules.hint")),
    schedErr,
    h("h3", {}, t("publishing.schedules.upcoming")),
    h("table", { class: "table" },
      h("thead", {}, h("tr", {}, th("key"), th("publishAt"), th("rev"), th("status"), th("actions"))),
      upcomingBody),
    h("h3", {}, t("publishing.schedules.recent")),
    h("table", { class: "table" },
      h("thead", {}, h("tr", {}, th("key"), th("publishAt"), th("status"), th("result"), th("failure"), th("finished"))),
      recentBody));

  async function loadSchedules() {
    if (!canSchedules) return;
    const seq = ++schedSeq;
    schedErr.hidden = true;
    let up;
    let recent;
    try {
      [up, recent] = await Promise.all([ctx.data.contentSchedulesUpcoming({}), ctx.data.contentSchedulesRecent({})]);
    } catch (e) {
      if (seq !== schedSeq || !ctx.alive()) return;
      schedErr.textContent = ctx.errorMessage(e);
      schedErr.hidden = false;
      return;
    }
    if (seq !== schedSeq || !ctx.alive()) return;
    rows(upcomingBody, up, (s) => h("tr", { "data-key": s.key, "data-status": s.status },
      h("td", { class: "mono" }, s.key),
      h("td", {}, time(s.publishAt)),
      h("td", {}, t("publishing.schedules.rev", { rev: s.draftRev })),
      h("td", {}, statusBadge(s.status)),
      h("td", {},
        h("a", { href: `#/documents/${s.key}` }, t("publishing.schedules.reschedule")), " ",
        s.status === "pending" ? h("button", { type: "button", class: "danger", onclick: () => cancelSchedule(s.key) }, t("publishing.schedules.cancel")) : "")),
    { empty: t("publishing.schedules.emptyUpcoming"), colspan: 5 });
    rows(recentBody, recent, (s) => h("tr", { "data-key": s.key, "data-status": s.status },
      h("td", {}, h("a", { href: `#/documents/${s.key}` }, s.key)),
      h("td", {}, time(s.publishAt), s.lateSeconds > 0 ? t("publishing.schedules.late", { seconds: s.lateSeconds }) : ""),
      h("td", {}, statusBadge(s.status)),
      h("td", {}, label(ctx.enums.contentScheduleResults, "publishing.result", s.result)),
      h("td", { class: "mono" }, s.failurePointers.length ? s.failurePointers.join("、") : t("publishing.schedules.noFailure")),
      h("td", {}, time(s.finishedAt))),
    { empty: t("publishing.schedules.emptyRecent"), colspan: 6 });
  }

  async function cancelSchedule(key) {
    schedErr.hidden = true;
    try {
      const r = await ctx.actions.contentScheduleCancel(key);
      if (r.cancelled) return;
    } catch (e) {
      schedErr.textContent = ctx.errorMessage(e);
      schedErr.hidden = false;
    }
    await loadSchedules();
  }

  // ── publish hook ──────────────────────────────────────────────────────
  let hookSeq = 0;
  const hookErr = h("p", { id: "publishing-hook-error", class: "error", hidden: true });
  const hookMsg = h("p", { id: "publishing-hook-message", class: "hint", hidden: true });
  const statusBox = h("div", { id: "publishing-hook-status", class: "card" });
  const secretBox = h("div", { id: "publishing-hook-secret", class: "card", hidden: true });
  const urlIn = h("input", { type: "url", id: "publishing-hook-url", autocomplete: "off", spellcheck: false, placeholder: t("publishing.hook.urlPlaceholder") });
  const validateBtn = h("button", { type: "button", id: "publishing-hook-validate", onclick: doValidate }, t("publishing.hook.validate"));
  const saveBtn = h("button", { type: "submit", id: "publishing-hook-save", class: "primary" }, t("publishing.hook.save"));
  const testBtn = h("button", { type: "button", id: "publishing-hook-test", onclick: doTest }, t("publishing.hook.test"));
  const fireBtn = h("button", { type: "button", id: "publishing-hook-fire", onclick: doFire }, t("publishing.hook.fire"));
  const toggleBtn = h("button", { type: "button", id: "publishing-hook-toggle", onclick: () => doEnable(toggleBtn.dataset.next === "1") }, t("publishing.hook.disable"));
  const rotateBtn = h("button", { type: "button", id: "publishing-hook-rotate", onclick: doRotate }, t("publishing.hook.rotate"));
  const deleteBtn = h("button", { type: "button", id: "publishing-hook-delete", class: "danger", onclick: doDelete }, t("publishing.hook.delete"));
  const hookForm = h("form", { id: "publishing-hook-form", class: "inline-form", onsubmit: doSave },
    h("label", { class: "inline" }, t("publishing.hook.urlLabel"), " ", urlIn), validateBtn, saveBtn);
  const hookBlock = h("div", { id: "publishing-hook-block", hidden: !canHook },
    h("div", { class: "toolbar" }, h("h2", {}, t("publishing.hook.title"))),
    h("p", { class: "hint" }, t("publishing.hook.hint")),
    hookErr,
    statusBox,
    hookForm,
    h("div", { class: "toolbar-actions" }, testBtn, fireBtn, toggleBtn, rotateBtn, deleteBtn),
    h("p", { class: "hint" }, t("publishing.hook.testHint"), " ", t("publishing.hook.fireHint")),
    hookMsg,
    secretBox);

  const showHookErr = (text) => { hookErr.textContent = text || ""; hookErr.hidden = !text; };
  const showHookMsg = (text) => { hookMsg.textContent = text || ""; hookMsg.hidden = !text; };
  const busy = (on) => { for (const b of [validateBtn, saveBtn, testBtn, fireBtn, toggleBtn, rotateBtn, deleteBtn]) b.disabled = on; };

  function kv(labelKey, value) {
    return h("tr", {}, h("th", {}, t(labelKey)), h("td", {}, value));
  }

  function drawStatus(st) {
    const state = ctx.logic.publishHookState(st);
    clear(statusBox);
    statusBox.dataset.state = state;
    const badgeClass = { enabled: "badge ok", disabled: "badge", failed: "badge warn", none: "badge" }[state];
    statusBox.append(h("p", {}, h("span", { id: "publishing-hook-state", class: badgeClass }, t(`publishing.hook.state.${state}`))));
    if (state === "none") {
      statusBox.append(h("p", { class: "hint" }, t("publishing.hook.notConfigured")));
    } else {
      if (state === "failed") {
        statusBox.append(h("div", { id: "publishing-hook-failed", class: "error" },
          h("strong", {}, t("publishing.hook.failedTitle")), " ",
          t("publishing.hook.failedText", { count: st.consecutiveFailures }), " ",
          h("button", { type: "button", id: "publishing-hook-reenable", class: "primary", onclick: () => doEnable(true) }, t("publishing.hook.reenable"))));
      }
      const last = st.lastStatus
        ? label(ctx.enums.publishHookResults, "publishing.hook.result", st.lastStatus) + (st.lastHttpStatus ? t("publishing.hook.http", { code: st.lastHttpStatus }) : "")
        : t("publishing.hook.neverCalled");
      statusBox.append(h("table", { class: "table" }, h("tbody", {},
        kv("publishing.hook.k.host", h("span", { class: "mono" }, st.host)),
        kv("publishing.hook.k.url", h("span", { class: "mono" }, st.urlMasked)),
        kv("publishing.hook.k.lastResult", last),
        kv("publishing.hook.k.lastAttempt", time(st.lastAttemptAt)),
        kv("publishing.hook.k.lastSuccess", time(st.lastSuccessAt)),
        kv("publishing.hook.k.failures", String(st.consecutiveFailures)),
        kv("publishing.hook.k.daily", t("publishing.hook.dailyValue", { count: st.dailyCount, cap: st.dailyCap })),
        kv("publishing.hook.k.pending", t(st.pending ? "publishing.hook.pendingYes" : "publishing.hook.pendingNo")))));
    }
    const configured = state !== "none";
    testBtn.hidden = !configured;
    fireBtn.hidden = state !== "enabled";
    rotateBtn.hidden = !configured;
    deleteBtn.hidden = !configured;
    toggleBtn.hidden = state === "none" || state === "failed";
    toggleBtn.dataset.next = state === "enabled" ? "0" : "1";
    toggleBtn.textContent = t(state === "enabled" ? "publishing.hook.disable" : "publishing.hook.enable");
  }

  async function loadHook() {
    if (!canHook) return;
    const seq = ++hookSeq;
    let st;
    try {
      st = await ctx.data.publishHookStatus();
    } catch (e) {
      if (seq !== hookSeq || !ctx.alive()) return;
      showHookErr(ctx.errorMessage(e));
      return;
    }
    if (seq !== hookSeq || !ctx.alive()) return;
    drawStatus(st);
  }

  /** Runs one hook operation: clears messages, disables the buttons, shows the error text, reloads the status afterwards. */
  async function run(fn) {
    showHookErr("");
    showHookMsg("");
    busy(true);
    try {
      await fn();
    } catch (e) {
      if (ctx.alive()) showHookErr(ctx.errorMessage(e));
    } finally {
      if (ctx.alive()) { busy(false); await loadHook(); }
    }
  }

  function doValidate() {
    return run(async () => {
      const r = await ctx.actions.publishHookValidate({ url: urlIn.value });
      showHookMsg(t("publishing.hook.validated", { masked: r.urlMasked }));
    });
  }

  function doSave(e) {
    e.preventDefault();
    const url = urlIn.value;
    urlIn.value = ""; // write-only: the field is emptied the moment it is submitted, whatever the outcome
    return run(async () => {
      await ctx.actions.publishHookSave({ url });
      showHookMsg(t("publishing.hook.saved"));
    });
  }

  function doEnable(on) {
    return run(async () => {
      await ctx.actions.publishHookSetEnabled(on);
      showHookMsg(t(on ? "publishing.hook.enabledDone" : "publishing.hook.disabledDone"));
    });
  }

  const callText = (r) => {
    const http = r.httpStatus ? t("publishing.hook.http", { code: r.httpStatus }) : "";
    return r.ok
      ? t("publishing.hook.callOk", { http })
      : t("publishing.hook.callFailed", { result: label(ctx.enums.publishHookResults, "publishing.hook.result", r.result), http });
  };

  function doTest() {
    return run(async () => { showHookMsg(callText(await ctx.actions.publishHookTest())); });
  }

  function doFire() {
    return run(async () => {
      const r = await ctx.actions.publishHookFire();
      if (!r.cancelled) showHookMsg(callText(r));
    });
  }

  function doDelete() {
    return run(async () => {
      const r = await ctx.actions.publishHookDelete();
      if (!r.cancelled) { urlIn.value = ""; hideSecret(); showHookMsg(t("publishing.hook.deleted")); }
    });
  }

  function hideSecret() { clear(secretBox); secretBox.hidden = true; }

  function doRotate() {
    return run(async () => {
      const r = await ctx.actions.publishHookRotateSecret();
      if (r.cancelled) return;
      // Shown once, in this box only: never stored, never put into copy or attributes; the box is emptied on dismiss or reload.
      const code = h("code", { id: "publishing-hook-secret-value", class: "mono" }, r.secret);
      const copyBtn = h("button", { type: "button", id: "publishing-hook-secret-copy" }, t("publishing.hook.secretCopy"));
      copyBtn.addEventListener("click", async () => {
        if (await ctx.ui.copyText(r.secret)) copyBtn.textContent = t("publishing.hook.secretCopied");
      });
      clear(secretBox);
      secretBox.hidden = false;
      secretBox.append(
        h("h3", {}, t("publishing.hook.secretTitle")),
        h("p", { class: "hint" }, t("publishing.hook.secretOnce")),
        h("p", {}, code), copyBtn, " ",
        h("button", { type: "button", id: "publishing-hook-secret-done", class: "primary", onclick: hideSecret }, t("publishing.hook.secretDone")));
    });
  }

  root.append(h("div", { id: "tab-content-publishing", class: "tab-panel" },
    schedBlock,
    hookBlock));

  await Promise.all([loadSchedules(), loadHook()]);
}
