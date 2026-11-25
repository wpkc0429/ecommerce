// Pure logic of the publishing group (no DOM, no network, no copy lookups): Taipei <-> ISO time conversion for the
// scheduler, the lead/horizon checks and the hook URL masking/scrubbing. Unit-tested by scripts/test-publishing.mjs.

/** The admin shows and takes schedule times in Asia/Taipei (UTC+8, no daylight saving). */
export const TAIPEI_OFFSET_MS = 8 * 3600 * 1000;
export const TAIPEI_OFFSET_TEXT = "+08:00";
/** Mirrors the API's defaults (CONTENT_SCHEDULE_MIN_LEAD 2 minutes, CONTENT_SCHEDULE_MAX_AHEAD 90 days). The API stays the authority. */
export const MIN_LEAD_MS = 2 * 60 * 1000;
export const MAX_AHEAD_MS = 90 * 24 * 3600 * 1000;

const LOCAL_RE = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(?:\.\d+)?)?$/;
const p2 = (n) => String(n).padStart(2, "0");

/** "YYYY-MM-DDTHH:mm[:ss]" read as a Taipei wall-clock time -> epoch ms, or null when malformed / not a real date. */
export function parseTaipeiLocal(s) {
  const m = LOCAL_RE.exec(String(s ?? "").trim());
  if (!m) return null;
  const [y, mo, d, h, mi, se] = [m[1], m[2], m[3], m[4], m[5], m[6] || "0"].map(Number);
  const wall = Date.UTC(y, mo - 1, d, h, mi, se);
  const back = new Date(wall);
  if (back.getUTCFullYear() !== y || back.getUTCMonth() !== mo - 1 || back.getUTCDate() !== d || back.getUTCHours() !== h || back.getUTCMinutes() !== mi) return null;
  return wall - TAIPEI_OFFSET_MS;
}

/** epoch ms -> "YYYY-MM-DDTHH:mm:ss+08:00" (always WITH the offset: the API rejects offset-less times with 422). */
export function taipeiISO(ms) {
  const d = new Date(ms + TAIPEI_OFFSET_MS);
  return `${d.getUTCFullYear()}-${p2(d.getUTCMonth() + 1)}-${p2(d.getUTCDate())}T${p2(d.getUTCHours())}:${p2(d.getUTCMinutes())}:${p2(d.getUTCSeconds())}${TAIPEI_OFFSET_TEXT}`;
}

/** Taipei local datetime-local text -> ISO string with +08:00, or null. */
export function taipeiLocalToISO(s) {
  const ms = parseTaipeiLocal(s);
  return ms === null ? null : taipeiISO(ms);
}

/**
 * Local lead/horizon check. Returns {ok:true, ms, iso} or {ok:false, reason:"format"|"tooSoon"|"tooFar"}.
 * The API rounds up to the next whole minute and stays the authority; this only saves a round trip.
 */
export function checkScheduleTime(local, nowMs = Date.now()) {
  const ms = parseTaipeiLocal(local);
  if (ms === null) return { ok: false, reason: "format" };
  if (ms < nowMs + MIN_LEAD_MS) return { ok: false, reason: "tooSoon" };
  if (ms > nowMs + MAX_AHEAD_MS) return { ok: false, reason: "tooFar" };
  return { ok: true, ms, iso: taipeiISO(ms) };
}

/** Any ISO instant -> "YYYY-MM-DDTHH:mm" in Taipei (datetime-local value), or "" when not a date. */
export function taipeiInputValue(iso) {
  const ms = Date.parse(iso);
  if (Number.isNaN(ms)) return "";
  return taipeiISO(ms).slice(0, 16);
}

/** Any ISO instant -> "YYYY-MM-DD HH:mm" in Taipei for display; "—" for empty, the raw text when unparsable. */
export function taipeiDisplay(iso) {
  if (!iso) return "—";
  const ms = Date.parse(iso);
  if (Number.isNaN(ms)) return String(iso);
  return taipeiISO(ms).slice(0, 16).replace("T", " ");
}

/** min / max for the datetime-local input (Taipei wall clock; min rounded UP to the minute). */
export function scheduleBounds(nowMs = Date.now()) {
  const minMs = Math.ceil((nowMs + MIN_LEAD_MS) / 60000) * 60000;
  return { min: taipeiISO(minMs).slice(0, 16), max: taipeiISO(nowMs + MAX_AHEAD_MS).slice(0, 16) };
}

// ── publish hook URL: write-only ───────────────────────────────────────────────────────────────
/** "https://host/…abcd" (host + last four characters), never the path. "" when it is not an http(s) URL. */
export function maskHookUrl(url) {
  const m = /^https?:\/\/([^/\s?#:]+)/i.exec(String(url ?? "").trim());
  if (!m) return "";
  const s = String(url).trim();
  return `https://${m[1].toLowerCase()}/…${s.slice(-4)}`;
}

/** Shape check before asking the API (it stays the authority): non-empty, <= 512, https. Returns an error code or "". */
export function hookUrlProblem(url) {
  const s = String(url ?? "").trim();
  if (!s) return "required";
  if (s.length > 512) return "tooLong";
  if (!/^https:\/\/[^/\s?#@]+/i.test(s)) return "https";
  return "";
}

/** Removes the URL (and its secret-bearing path) from any text before it can reach a message, toast or log. */
export function scrubHookUrl(text, url) {
  let out = String(text ?? "");
  const s = String(url ?? "").trim();
  if (!s) return out;
  const parts = new Set([s]);
  const m = /^https?:\/\/[^/\s?#]+(\/[^\s?#]*)/i.exec(s);
  if (m && m[1].length > 3) parts.add(m[1]);
  for (const p of parts) out = out.split(p).join("…");
  return out;
}

/** "none" (not configured) | "failed" (auto-disabled after consecutive failures: FINAL) | "disabled" | "enabled". */
export function hookState(st) {
  if (!st || !st.configured) return "none";
  if (st.failed) return "failed";
  return st.enabled ? "enabled" : "disabled";
}
