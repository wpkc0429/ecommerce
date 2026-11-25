// Content schema status (store owner only: content.manage). Shows the active version, lets the human paste or load the
// schema JSON, runs the READ-ONLY dry run and offers the registration. Registering is a core action that always dry-runs
// first and shows the report and a fixed warning in a confirmation dialog (and a second one when the platform asks for
// removals to be confirmed); this screen cannot register without them.
export default async function contentSchema(root, ctx) {
  const { h, clear } = ctx.ui;
  const { t, data, actions, format } = ctx;
  let seq = 0;

  const err = h("p", { id: "schema-error", class: "error", hidden: true });
  const ok = h("p", { id: "schema-ok", class: "hint", role: "status", hidden: true });
  const status = h("div", { id: "schema-status" });
  const text = h("textarea", { id: "schema-text", class: "mono", rows: 16, spellcheck: "false", placeholder: t("content.schema.placeholder") });
  const fileErr = h("span", { class: "hint" });
  const file = h("input", { type: "file", id: "schema-file", accept: ".json,application/json", onchange: onFile });
  const report = h("div", { id: "schema-report" });
  const dryBtn = h("button", { id: "schema-dryrun", onclick: (e) => run(e.currentTarget, dryRun) }, t("content.schema.dryrun"));
  const regBtn = h("button", { id: "schema-register", class: "primary", onclick: (e) => run(e.currentTarget, register) }, t("content.schema.register"));
  const tbody = h("tbody", { id: "schema-versions-tbody" });

  root.append(h("div", { id: "tab-content-schema", class: "tab-panel" },
    h("div", { class: "toolbar" }, h("h2", {}, t("content.schema.title")),
      h("div", { class: "toolbar-actions" }, h("button", { id: "schema-back", onclick: () => ctx.navigate("documents") }, t("content.back")))),
    h("p", { class: "hint" }, t("content.schema.intro")),
    err, ok, status,
    h("div", { class: "schema-form wide" },
      h("label", {}, t("content.schema.textLabel"), text),
      h("label", {}, t("content.schema.fileLabel"), file, fileErr),
      h("div", { class: "toolbar-actions" }, dryBtn, regBtn)),
    report,
    h("h3", {}, t("content.schema.versions")),
    h("table", { class: "table", id: "schema-versions-table" },
      h("thead", {}, h("tr", {},
        h("th", {}, t("content.schema.col.version")), h("th", {}, t("content.schema.col.format")), h("th", {}, t("content.schema.col.sha")),
        h("th", {}, t("content.schema.col.source")), h("th", {}, t("content.schema.col.when")))),
      tbody)));

  async function run(btn, fn) {
    btn.disabled = true;
    err.hidden = true;
    ok.hidden = true;
    try { await fn(); } finally { btn.disabled = false; }
  }
  function say(e) {
    const fields = e && e.fields ? Object.entries(e.fields).filter(([p]) => p !== "") : [];
    err.replaceChildren(ctx.errorMessage(e), fields.length ? h("pre", { class: "dlg__detail" }, fields.map(([p, m]) => `${p}: ${m}`).join("\n")) : "");
    err.hidden = false;
  }

  async function load() {
    const my = ++seq;
    let s;
    try { s = await data.contentSchemaStatus(); } catch (e) { if (ctx.alive() && my === seq) say(e); return; }
    if (!ctx.alive() || my !== seq) return;
    clear(status).append(s.active
      ? h("p", { id: "schema-active", "data-version": String(s.active.version) },
        t("content.schema.active", { version: s.active.version }), " ｜ ",
        t("content.schema.sourceLine", { source: s.active.source, format: s.active.format }), " ｜ ",
        h("span", { class: "mono", title: s.active.sha256 }, `sha256 ${s.active.sha256.slice(0, 12)}`))
      : h("p", { id: "schema-active", "data-version": "0" }, t("content.schema.none")));
    clear(tbody);
    if (!s.versions.length) tbody.append(h("tr", {}, h("td", { colspan: "5", class: "empty" }, t("content.schema.none"))));
    for (const v of s.versions) {
      tbody.append(h("tr", { "data-version": String(v.version) },
        h("td", {}, h("strong", {}, `v${v.version}`)), h("td", {}, String(v.format)),
        h("td", { class: "mono", title: v.sha256 }, v.sha256.slice(0, 12)), h("td", {}, v.source), h("td", {}, format.time(v.createdAt))));
    }
  }

  async function onFile(ev) {
    const f = ev.currentTarget.files && ev.currentTarget.files[0];
    fileErr.textContent = "";
    if (!f) return;
    try { text.value = await actions.readContentSchemaFile(f); } catch (e) { fileErr.textContent = ctx.errorMessage(e); }
  }

  function showReport(r) {
    report.replaceChildren(
      h("h3", {}, t("content.schema.reportTitle")),
      h("p", { id: "schema-dryrun-summary" }, r.changed ? t("content.schema.dryrunChange", { version: r.wouldRegisterVersion }) : t("content.schema.dryrunSame")),
      h("pre", { id: "schema-dryrun-report", class: "dlg__detail" }, r.lines.length ? r.lines.join("\n") : t("content.schema.reportNone")));
  }

  async function dryRun() {
    try { showReport(await data.dryRunContentSchema(text.value)); } catch (e) { say(e); }
  }

  async function register() {
    try {
      const r = await actions.registerContentSchema(text.value);
      if (r.cancelled) return;
      ok.textContent = r.unchanged ? t("content.schema.unchanged") : t("content.schema.registered", { version: r.version });
      ok.hidden = false;
      clear(report);
      await load();
    } catch (e) { say(e); }
  }

  await load();
}
