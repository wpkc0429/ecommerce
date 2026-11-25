// Core group "publishing" (CORE, locked): scheduled publishing and the publish hook of the content store.
//  * schedules are bound to the reviewed draft_rev (PUT .../schedule needs If-Match "<draft_rev>"); the Taipei local time is converted
//    to an ISO string WITH offset here, the lead/horizon is checked here, the API stays the authority (422 pointers are mapped).
//  * the hook URL is WRITE-ONLY: no loader returns it, no error text may contain it (scrubHookUrl), nothing is stored or logged.
//    The signing secret is returned once by publishHookRotateSecret to the caller and kept nowhere.
// Every action checks its permission first (content.publish for schedules, content.manage for the hook); irreversible ones ask a
// core-owned confirmation with a FIXED warning (lib/meta/publishing.js).
import * as apiLib from "../../lib/api.js";
import { session } from "../../lib/session.js";
import { need } from "../guard.js";
import { ActionError, invalid, blocked } from "../errors.js";
import { fixedWarning } from "../warnings.js";
import { confirmDialog } from "../../ui/kit.js";
import { t } from "../../lib/design.js";
import {
  checkScheduleTime, scheduleBounds, taipeiDisplay, taipeiInputValue, hookState, hookUrlProblem, scrubHookUrl,
} from "../publishing/logic.js";

const { api, ApiError } = apiLib;
const base = () => `/admin/shops/${session.shopID}/content`;
const KEY_RE = /^[a-z0-9][a-z0-9-]{0,62}$/;
const SCHEDULE_STATUSES = ["pending", "running", "done", "failed", "missed", "cancelled", "superseded"];
const SCHEDULE_RESULTS = ["published", "already_published", "invalid", "permission_revoked", "edited", "published_manually", "document_deleted", "cancelled", "missed", "error"];
const HOOK_RESULTS = ["ok", "http_error", "timeout", "blocked", "network_error", "not_allowed", "rate_limited"];

const keyOf = (k) => {
  const s = String(k ?? "");
  if (!KEY_RE.test(s)) throw invalid({ "/key": t("publishing.err.badKey") });
  return s;
};
const revOf = (r) => {
  const n = Number(r);
  if (!Number.isInteger(n) || n < 1) throw invalid({ "/draft_rev": t("publishing.err.badRev") });
  return n;
};

/** Asks the core-owned confirmation; true when the person agreed. */
const ask = (id, params) => confirmDialog({
  title: t(`confirm.${id}.title`),
  message: t(`confirm.${id}.text`, params),
  warning: fixedWarning(id, params),
  confirmText: t(`confirm.${id}.yes`),
  cancelText: t("common.cancel"),
  danger: true,
});

/** Call with extra request headers (If-Match). apiRaw is the leader's lib/api.js addition (see the report). */
function apiWithHeaders(method, path, body, headers) {
  if (typeof apiLib.apiRaw !== "function") throw new ActionError("internal", t("err.generic"), { reason: "apiRaw" });
  return apiLib.apiRaw(method, path, body, headers);
}

const errCode = (err) => (err && err.body && err.body.error && err.body.error.code) || "";
const detailFields = (err) => {
  const out = {};
  for (const d of err.details || []) {
    const ptr = String(d.pointer || "");
    const text = d.message || t("err.invalid");
    out[ptr] = out[ptr] ? `${out[ptr]}；${text}` : text;
  }
  return out;
};

/** Maps an API failure of a schedule call to an ActionError (non-API errors pass through). */
function scheduleError(err, { cancel = false } = {}) {
  if (!(err instanceof ApiError)) return err;
  switch (err.status) {
    case 412: return new ActionError("stale", t("publishing.err.stale"), { reason: "stale" });
    case 428: return new ActionError("internal", t("publishing.err.precondition"), { reason: "precondition" });
    case 409: return blocked("conflict", t(cancel ? "publishing.err.cancelRunning" : "publishing.err.scheduleConflict", { message: err.message }));
    case 404: return blocked("missing", t(cancel ? "publishing.err.cancelMissing" : "publishing.err.docMissing"));
    case 429: return new ActionError("limit", t("publishing.err.rateLimited"), { reason: "rate" });
    case 422: {
      const fields = detailFields(err);
      return invalid(fields, t("publishing.err.scheduleInvalid", { details: Object.entries(fields).map(([p, m]) => `${p} ${m}`).join("；") }));
    }
    default: return err;
  }
}

/** Same for the hook. Nothing the API says is trusted not to contain the URL: it is scrubbed. */
function hookError(err, url = "") {
  if (!(err instanceof ApiError)) return err;
  const clean = (s) => scrubHookUrl(s, url);
  switch (err.status) {
    case 404: return blocked("missing", t("publishing.err.hookMissing"));
    case 409: return blocked("disabled", t("publishing.err.hookDisabled"));
    case 429: return new ActionError("limit", t(errCode(err) === "hook_daily_cap_reached" ? "publishing.err.hookCap" : "publishing.err.rateLimited"), { reason: "rate" });
    case 422: {
      const fields = {};
      for (const [p, m] of Object.entries(detailFields(err))) fields[p || "/url"] = clean(m);
      if (!Object.keys(fields).length) fields["/url"] = clean(err.message) || t("err.invalid");
      return invalid(fields, t("publishing.err.urlRejected", { details: Object.values(fields).join("；") }));
    }
    default: return Object.assign(new Error(clean(err.message)), { status: err.status });
  }
}

const schedule = (s) => ({
  id: s.id, key: s.key, draftRev: s.draft_rev, publishAt: s.publish_at, status: s.status, result: s.result || "",
  failurePointers: Array.isArray(s.failure && s.failure.pointers) ? s.failure.pointers.slice(0, 5).map(String) : [],
  attempts: s.attempts || 0, lateSeconds: s.late_seconds || 0, createdAt: s.created_at, finishedAt: s.finished_at || null,
});

const hook = (s) => ({
  configured: !!s.configured, enabled: !!s.enabled, host: s.host || "", urlMasked: s.url_masked || "",
  lastStatus: s.last_status || "", lastHttpStatus: s.last_http_status || 0, lastAttemptAt: s.last_attempt_at || null,
  lastSuccessAt: s.last_success_at || null, consecutiveFailures: s.consecutive_failures || 0, disabledReason: s.disabled_reason || "",
  failed: !!s.failed, pending: !!s.pending, pendingCount: s.pending_count || 0, dailyCount: s.daily_count || 0, dailyCap: s.daily_cap || 0,
  updatedAt: s.updated_at || null,
});

const listSchedules = async (scope, limit) => {
  need("content.publish");
  const n = Math.min(200, Math.max(1, Math.floor(Number(limit)) || 50));
  const d = await api("GET", `${base()}/schedules?scope=${scope}&limit=${n}`);
  return (d.schedules || []).map(schedule);
};

const group = {
  data: {
    contentSchedulesUpcoming: ({ limit = 50 } = {}) => listSchedules("upcoming", limit),
    contentSchedulesRecent: ({ limit = 20 } = {}) => listSchedules("recent", limit),
    /** The document's current schedule (pending/running) or its latest finished one; null when it never had one. */
    contentDocumentSchedule: async (key) => {
      need("content.view");
      try {
        return schedule(await api("GET", `${base()}/documents/${keyOf(key)}/schedule`));
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return null;
        throw e;
      }
    },
    /** Hook status: host, MASKED url and result categories only. The stored URL / secret can never be read back. */
    publishHookStatus: async () => {
      need("content.manage");
      return hook(await api("GET", `${base()}/publish-hook`));
    },
  },

  actions: {
    /** Schedules (or re-schedules) the reviewed draft. localDateTime = Taipei wall clock "YYYY-MM-DDTHH:mm". */
    contentSchedulePublish: async ({ key, localDateTime, draftRev }) => {
      need("content.publish");
      const k = keyOf(key);
      const rev = revOf(draftRev);
      const check = checkScheduleTime(localDateTime);
      if (!check.ok) throw invalid({ "/publish_at": t(`publishing.err.time.${check.reason}`) });
      try {
        const r = await apiWithHeaders("PUT", `${base()}/documents/${k}/schedule`, { publish_at: check.iso }, { "If-Match": `"${rev}"` });
        return { schedule: schedule(r.data), created: r.status === 201 };
      } catch (e) { throw scheduleError(e); }
    },
    contentScheduleCancel: async (key) => {
      need("content.publish");
      const k = keyOf(key);
      if (!(await ask("cancelSchedule", { key: k }))) return { cancelled: true };
      try {
        await api("DELETE", `${base()}/documents/${k}/schedule`);
        return {};
      } catch (e) { throw scheduleError(e, { cancel: true }); }
    },

    /** Validates the URL shape and the host allowlist without storing or calling it. */
    publishHookValidate: async ({ url }) => {
      need("content.manage");
      const u = String(url ?? "").trim();
      const problem = hookUrlProblem(u);
      if (problem) throw invalid({ "/url": t(`publishing.err.url.${problem}`) });
      try {
        const d = await api("PUT", `${base()}/publish-hook?dry_run=true`, { url: u });
        return { host: d.host || "", urlMasked: d.url_masked || "" };
      } catch (e) { throw hookError(e, u); }
    },
    /** Sets or replaces the hook URL (new URL = enabled, failure counters cleared). The URL is sent once and never kept. */
    publishHookSave: async ({ url }) => {
      need("content.manage");
      const u = String(url ?? "").trim();
      const problem = hookUrlProblem(u);
      if (problem) throw invalid({ "/url": t(`publishing.err.url.${problem}`) });
      try {
        return hook(await api("PUT", `${base()}/publish-hook`, { url: u }));
      } catch (e) { throw hookError(e, u); }
    },
    /** Enable / disable. Enabling again is how a FINAL failure (failed: true) is recovered; it clears the failure counter. */
    publishHookSetEnabled: async (enabled) => {
      need("content.manage");
      try {
        return hook(await api("PUT", `${base()}/publish-hook`, { enabled: !!enabled }));
      } catch (e) { throw hookError(e); }
    },
    /** New signing secret. It is returned ONCE here ({secret}); the caller must show it once and keep it nowhere. */
    publishHookRotateSecret: async () => {
      need("content.manage");
      if (!(await ask("rotateHookSecret", {}))) return { cancelled: true };
      try {
        const d = await api("POST", `${base()}/publish-hook/signing-secret`);
        return { secret: String(d.signing_secret || ""), note: String(d.note || "") };
      } catch (e) { throw hookError(e); }
    },
    publishHookDelete: async () => {
      need("content.manage");
      if (!(await ask("deletePublishHook", {}))) return { cancelled: true };
      try {
        await api("DELETE", `${base()}/publish-hook`);
        return {};
      } catch (e) { throw hookError(e); }
    },
    /** One attempt against the stored URL; changes no counters. */
    publishHookTest: async () => {
      need("content.manage");
      try {
        const d = await api("POST", `${base()}/publish-hook/test`);
        return { ok: !!d.ok, result: d.result || "", httpStatus: d.http_status || 0 };
      } catch (e) { throw hookError(e); }
    },
    /** Manual rebuild: skips the debounce, counts toward the daily cap. */
    publishHookFire: async () => {
      need("content.manage");
      if (!(await ask("fireHook", {}))) return { cancelled: true };
      try {
        const d = await api("POST", `${base()}/publish-hook/fire`);
        return { ok: !!d.ok, result: d.result || "", httpStatus: d.http_status || 0 };
      } catch (e) { throw hookError(e); }
    },
  },

  logic: {
    /** {min, max} for the datetime-local input, in Taipei wall clock. */
    contentScheduleBounds: () => scheduleBounds(),
    /** ISO instant -> datetime-local value in Taipei ("" when not a date). */
    contentScheduleInputValue: (iso) => taipeiInputValue(iso),
    /** "none" | "failed" | "disabled" | "enabled" for a publishHookStatus() result. */
    publishHookState: (st) => hookState(st),
  },
  enums: {
    contentScheduleStatuses: SCHEDULE_STATUSES,
    contentScheduleResults: SCHEDULE_RESULTS,
    publishHookResults: HOOK_RESULTS,
  },
  ui: {},
  format: {
    /** ISO instant -> "YYYY-MM-DD HH:mm" in Asia/Taipei. */
    contentTaipeiTime: (iso) => taipeiDisplay(iso),
  },
};

export default group;
