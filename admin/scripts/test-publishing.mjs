#!/usr/bin/env node
// Offline unit tests for the pure logic of the publishing group (core/publishing/logic.js): Taipei -> ISO with offset,
// lead/horizon checks, display/input conversion, hook URL masking and scrubbing, hook state. Run: node scripts/test-publishing.mjs
import assert from "node:assert/strict";
import {
  parseTaipeiLocal, taipeiLocalToISO, taipeiISO, checkScheduleTime, taipeiInputValue, taipeiDisplay, scheduleBounds,
  maskHookUrl, hookUrlProblem, scrubHookUrl, hookState, MIN_LEAD_MS, MAX_AHEAD_MS,
} from "../core/publishing/logic.js";

let n = 0;
const ok = (name, fn) => { fn(); n++; console.log(`  ok ${name}`); };

ok("Taipei local -> ISO with +08:00 offset", () => {
  assert.equal(taipeiLocalToISO("2026-12-01T09:00"), "2026-12-01T09:00:00+08:00");
  assert.equal(taipeiLocalToISO("2026-12-01T09:00:30"), "2026-12-01T09:00:30+08:00");
  assert.equal(Date.parse(taipeiLocalToISO("2026-12-01T09:00")), Date.UTC(2026, 11, 1, 1, 0, 0));
  assert.equal(taipeiLocalToISO("2026-01-01T00:00"), "2026-01-01T00:00:00+08:00"); // crosses the UTC date backwards
  assert.equal(Date.parse(taipeiLocalToISO("2026-01-01T00:00")), Date.UTC(2025, 11, 31, 16, 0, 0));
});
ok("malformed or impossible local times are rejected", () => {
  for (const bad of ["", null, undefined, "2026-12-01", "2026-12-01 09:00", "2026-13-01T09:00", "2026-02-30T09:00", "2026-12-01T25:00", "2026-12-01T09:00+08:00", "abc"]) {
    assert.equal(taipeiLocalToISO(bad), null, String(bad));
  }
  assert.equal(parseTaipeiLocal("2028-02-29T10:00") !== null, true); // leap day is real
  assert.equal(parseTaipeiLocal("2027-02-29T10:00"), null);
});
ok("every produced ISO string carries an offset", () => {
  for (const s of ["2026-12-01T09:00", "2027-06-30T23:59", "2026-03-01T00:00"]) assert.match(taipeiLocalToISO(s), /[+-]\d\d:\d\d$/);
});
ok("lead time: at least 2 minutes", () => {
  const now = Date.UTC(2026, 10, 25, 4, 0, 0); // 12:00 Taipei
  assert.deepEqual(checkScheduleTime("2026-11-25T12:01", now), { ok: false, reason: "tooSoon" });
  assert.equal(checkScheduleTime("2026-11-25T12:02", now).ok, true); // exactly the minimum
  assert.equal(checkScheduleTime("2026-11-25T11:00", now).reason, "tooSoon"); // in the past
});
ok("horizon: at most 90 days", () => {
  const now = Date.UTC(2026, 10, 25, 4, 0, 0);
  const edge = taipeiISO(now + MAX_AHEAD_MS).slice(0, 16);
  assert.equal(checkScheduleTime(edge, now).ok, true);
  assert.equal(checkScheduleTime(taipeiISO(now + MAX_AHEAD_MS + 60000).slice(0, 16), now).reason, "tooFar");
  assert.equal(checkScheduleTime("not a time", now).reason, "format");
  assert.equal(MIN_LEAD_MS, 120000);
});
ok("check returns the iso with offset and the instant", () => {
  const now = Date.UTC(2026, 10, 25, 4, 0, 0);
  const r = checkScheduleTime("2026-11-26T09:30", now);
  assert.equal(r.iso, "2026-11-26T09:30:00+08:00");
  assert.equal(r.ms, Date.UTC(2026, 10, 26, 1, 30, 0));
});
ok("UTC instants are shown and edited in Taipei", () => {
  assert.equal(taipeiInputValue("2026-12-01T01:00:00Z"), "2026-12-01T09:00");
  assert.equal(taipeiInputValue("2026-12-01T16:30:00Z"), "2026-12-02T00:30");
  assert.equal(taipeiInputValue("nope"), "");
  assert.equal(taipeiDisplay("2026-12-01T01:00:00Z"), "2026-12-01 09:00");
  assert.equal(taipeiDisplay(null), "—");
  assert.equal(taipeiDisplay("garbage"), "garbage");
  assert.equal(taipeiInputValue(taipeiLocalToISO("2026-12-01T09:00")), "2026-12-01T09:00"); // round trip
});
ok("input bounds: min rounded up to the minute, max 90 days", () => {
  const now = Date.UTC(2026, 10, 25, 4, 0, 30);
  const b = scheduleBounds(now);
  assert.equal(b.min, "2026-11-25T12:03");
  assert.equal(b.max, taipeiISO(now + MAX_AHEAD_MS).slice(0, 16));
  assert.equal(checkScheduleTime(b.min, now).ok, true);
});
ok("hook URL masking keeps host and the last four characters only", () => {
  const url = "https://api.cloudflare.com/client/v4/workers/builds/deploy_hooks/aaaa-bbbb-SECRETabcd";
  const m = maskHookUrl(url);
  assert.equal(m, "https://api.cloudflare.com/…abcd");
  assert.ok(!m.includes("client") && !m.includes("SECRET"));
  assert.equal(maskHookUrl("ftp://x"), "");
  assert.equal(maskHookUrl(""), "");
});
ok("hook URL shape problems", () => {
  assert.equal(hookUrlProblem(""), "required");
  assert.equal(hookUrlProblem("   "), "required");
  assert.equal(hookUrlProblem("http://a.example/x"), "https");
  assert.equal(hookUrlProblem("a.example/x"), "https");
  assert.equal(hookUrlProblem("https://" + "a".repeat(520)), "tooLong");
  assert.equal(hookUrlProblem("https://api.cloudflare.com/client/v4/workers/builds/deploy_hooks/x"), "");
});
ok("scrubbing removes the URL and its path from any text", () => {
  const url = "https://api.cloudflare.com/client/v4/workers/builds/deploy_hooks/TOPSECRETID";
  const text = `bad ${url} and path /client/v4/workers/builds/deploy_hooks/TOPSECRETID too`;
  const out = scrubHookUrl(text, url);
  assert.ok(!out.includes("TOPSECRETID") && !out.includes("deploy_hooks"), out);
  assert.equal(scrubHookUrl("plain", ""), "plain");
});
ok("hook state", () => {
  assert.equal(hookState(null), "none");
  assert.equal(hookState({ configured: false }), "none");
  assert.equal(hookState({ configured: true, enabled: true }), "enabled");
  assert.equal(hookState({ configured: true, enabled: false }), "disabled");
  assert.equal(hookState({ configured: true, enabled: false, failed: true }), "failed"); // FINAL failure wins over "disabled"
});

console.log(`publishing logic: ${n} groups passed`);
