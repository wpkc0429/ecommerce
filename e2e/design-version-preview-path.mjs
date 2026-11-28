// E2E for the version preview platform side (change design-history, task 10.7, design D25), run by
// design-history-path.mjs after the design history has a few published releases. No browser. It is
// NOT part of CI (the owner's rule: CI has no e2e job); run it against a THROWAWAY stack only:
//
//   make sandbox-up && make sandbox-smoke        (the sandbox smoke runs design-history-path.mjs, which calls this)
//   or point E2E_API_BASE_URL at the e2e stack; it needs the same DESIGN_R2_* private bucket as the design history path.
//
// Optional environment (each unset one skips only its own checks, loudly):
//   E2E_PREVIEW_ORIGIN   an origin listed in the API's ADMIN_PREVIEW_ORIGINS (the sandbox sets it);
//                        enables the read-only preview session checks. E2E_REQUIRE_PREVIEW_ORIGIN=1 makes a skip a failure.
//   E2E_API_LOG          the API's log file (the sandbox sets it); enables "the logs never contain the origin".
//
// What it proves (the fake service plays the pull model: list -> claim -> ready | failed):
//   request (202, queued, read_only, no origin), TTL 30 minutes, pull-claim-ready, origin only for the
//   initiator, no slot taken while a request awaits approval, no events / requests / screenshots written,
//   a newer preview supersedes the older, a real time-based expiry, a guard failure visible to the initiator,
//   refusals that create nothing (revert_below_core_floor, already_current, not a published design),
//   staff 403 / cross-shop / the service cannot create, 6 per hour per user and 30 per day per shop (429 + Retry-After),
//   and the read-only preview session on a preview origin.
import { brief, check, fail, req, runID, section, API } from "./lib.mjs";
import { readFileSync } from "node:fs";

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const STATUSES = ["queued", "claimed", "ready", "failed", "expired", "superseded"];

/**
 * @param c context from design-history-path.mjs: { platform, shop, owner, ownerEmail, ownerPassword, A, S, svc, human,
 *          targetRid (a published, non-current release), currentRid (the current design), driveToAwaiting(label) -> request id | null }
 */
export async function runVersionPreviewPath(c) {
  section("㉘ Version preview (D25, task 10.7): pull model, TTL, limits, no history written, read-only sessions");
  const { shop, owner, A, S, svc, human, targetRid: target, currentRid: current } = c;
  const uniq = Math.random().toString(36).slice(2, 8);
  const secretHost = `vp-e2e-${uniq}.preview.example`;
  const originOf = (h = secretHost) => `https://${h}`;
  const vpCreate = (token, rid = target) => req("POST", A(`/requests/${rid}/version-preview`), { token });
  const vpGet = (token, pid) => req("GET", A(`/version-previews/${pid}`), { token });
  const queue = async (status) => (await svc("GET", S(`/version-previews?status=${status}`))).json?.version_previews ?? [];
  const rowCount = async () => (await Promise.all(STATUSES.map(queue))).reduce((n, l) => n + l.length, 0);
  const claim = (pid) => svc("POST", S(`/version-previews/${pid}/claim`));
  const ready = (pid, origin, ms) => svc("PUT", S(`/version-previews/${pid}/ready`), { origin, expires_at: new Date(Date.now() + ms).toISOString() });

  let ownerMade = 0; // previews created by the owner / by anyone in this shop (the table is ours alone)
  let shopMade = 0;
  const made = async (token, rid, who) => {
    const r = await vpCreate(token, rid);
    if (r.status === 202) { shopMade++; if (who === "owner") ownerMade++; }
    return r;
  };

  // ── people: an editor holds design.view, staff does not ────────────────────────────────────
  const roles = (await req("GET", `/api/v1/admin/shops/${shop}/roles`, { token: owner })).json?.roles ?? [];
  const invite = async (roleName, n) => {
    const role = roles.find((r) => r.name === roleName);
    if (!role) throw new Error(`no ${roleName} role in the new shop`);
    const email = `e2e-vp-${roleName}${n}-${uniq}@example.com`;
    const password = `e2e-vp-${roleName}${n}-${uniq}-pw`;
    const inv = await req("POST", `/api/v1/admin/shops/${shop}/invitations`, { token: owner, body: { email, role_id: role.id } });
    const tok = String(inv.json?.invite_url ?? "").split("/#/accept-invite/")[1];
    const acc = await req("POST", "/api/v1/admin/auth/invitations/accept", { body: { token: tok, password } });
    const login = await req("POST", "/api/v1/admin/auth/login", { body: { email, password } });
    if (inv.status !== 201 || ![200, 204].includes(acc.status) || !login.json?.access_token) throw new Error(`inviting ${roleName} failed: ${brief(inv)} ${brief(acc)} ${brief(login)}`);
    return login.json.access_token;
  };
  const editors = [];
  for (let i = 1; i <= 5; i++) editors.push(await invite("editor", i));
  const staff = await invite("staff", 1);

  // ── a request waiting for approval does not block a preview, and nothing of the history moves ──
  const busy = await c.driveToAwaiting("vp-busy");
  check("a request is awaiting approval (it holds the site's only slot)", Boolean(busy));
  if (!busy) return;
  const watched = [target, current, busy];
  const snapshot = async () => {
    const out = {};
    for (const rid of watched) {
      const ev = await human("GET", A(`/requests/${rid}/events`));
      const det = await human("GET", A(`/requests/${rid}`));
      out[rid] = { events: ev.json?.events?.length, etag: det.headers.get("etag"), status: det.json?.status };
    }
    const all = await human("GET", A("/requests?page_size=100"));
    out.total = all.json?.total ?? all.json?.requests?.length;
    out.shots = (await human("GET", A(`/requests/${busy}/screenshots`))).json?.screenshots?.length;
    return JSON.stringify(out);
  };
  const before = await snapshot();
  check("the busy request is awaiting_approval before the preview", JSON.parse(before)[busy].status === "awaiting_approval", before);

  // ── request, pull, claim, ready ───────────────────────────────────────────────────────────
  const cr = await made(owner, target, "owner");
  const pid = cr.json?.id;
  check("the owner requests a version preview: 202, queued, read_only, no origin yet",
    cr.status === 202 && cr.json?.status === "queued" && cr.json?.read_only === true && !("origin" in (cr.json ?? {})), brief(cr));
  const ttl = Date.parse(cr.json?.expires_at) - Date.parse(cr.json?.created_at);
  check("the TTL is 30 minutes", Math.abs(ttl - 30 * 60e3) < 2000, `${ttl} ms`);
  check("the descriptor names the target tag and the current core, not the requester",
    typeof cr.json?.target_tag === "string" && cr.json.target_tag.length > 0 && !("requested_by_user_id" in cr.json) && !cr.text.includes("X-Amz-"), cr.text);

  const q = await queue("queued");
  check("the service finds it by pulling (queued list) and the view has no requester and no origin",
    q.length === 1 && q[0].id === pid && q[0].read_only === true && !("requested_by_user_id" in q[0]) && !("origin" in q[0]), JSON.stringify(q));
  const cl = await claim(pid);
  check("the service claims it (5 minute lease)", cl.status === 200 && cl.json?.status === "claimed" &&
    Math.abs(Date.parse(cl.json?.claim_expires_at) - Date.now() - 5 * 60e3) < 30e3, brief(cl));
  const badPath = await svc("PUT", S(`/version-previews/${pid}/ready`), { origin: `${originOf()}/admin?x=1`, expires_at: new Date(Date.now() + 25 * 60e3).toISOString() });
  check("an origin with a path or query is refused (422) and the row stays claimed", badPath.status === 422 && (await vpGet(owner, pid)).json?.status === "claimed", brief(badPath));
  const tooLong = await ready(pid, originOf(), 3 * 3600e3);
  check("an expiry beyond the TTL is refused (422)", tooLong.status === 422, brief(tooLong));
  const rd = await ready(pid, originOf(), 25 * 60e3);
  check("a clean origin makes it ready, and the service's own view does not echo the origin", rd.status === 200 && rd.json?.status === "ready" && !rd.text.includes(secretHost), brief(rd));

  const mine = await vpGet(owner, pid);
  check("only the initiator reads the origin (ready, read_only)", mine.status === 200 && mine.json?.origin === originOf() && mine.json?.status === "ready" && mine.json?.read_only === true, brief(mine));
  const ed = await vpGet(editors[0], pid);
  const pl = await vpGet(c.platform, pid);
  check("another member with design.view does not get the origin", ed.status === 200 && !("origin" in ed.json) && !ed.text.includes(secretHost), brief(ed));
  check("a super admin does not get it either", pl.status === 200 && !("origin" in pl.json) && !pl.text.includes(secretHost), brief(pl));
  check("the service cannot read the human descriptor (401)", (await svc("GET", A(`/version-previews/${pid}`))).status === 401);

  check("no event, request, screenshot or approval state changed, the busy request is untouched (task 10.7: events count unchanged)", (await snapshot()) === before, `${before} -> ${await snapshot()}`);

  // ── a newer preview of the same site supersedes the older ────────────────────────────────────
  const second = await made(owner, target, "owner");
  const old = await vpGet(owner, pid);
  check("a new preview supersedes the old one, which loses its origin", second.status === 202 && old.json?.status === "superseded" && !("origin" in old.json), brief(old));
  const sup = await queue("superseded");
  check("the service is told which deployment to tear down (superseded list)", sup.some((p) => p.id === pid));
  const live = [...(await queue("queued")), ...(await queue("claimed")), ...(await queue("ready"))];
  check("exactly one live preview per site", live.length === 1 && live[0].id === second.json?.id, JSON.stringify(live));

  // ── a real expiry (the service reports a short-lived deployment) ─────────────────────────────
  const pid2 = second.json.id;
  await claim(pid2);
  const short = await ready(pid2, originOf(`vp-e2e-short-${uniq}.preview.example`), 4000);
  check("a short expiry is accepted", short.status === 200, brief(short));
  check("it reads ready (with its origin) before the expiry", (await vpGet(owner, pid2)).json?.status === "ready");
  await sleep(5200);
  const gone = await vpGet(owner, pid2);
  check("after expires_at it reads expired and shows no origin", gone.json?.status === "expired" && !("origin" in gone.json), brief(gone));
  check("an expired preview cannot be claimed (409)", (await claim(pid2)).status === 409);

  // ── a failure the initiator can read ────────────────────────────────────────────────────────
  const third = await made(owner, target, "owner");
  const pid3 = third.json?.id;
  await claim(pid3);
  const failed = await svc("POST", S(`/version-previews/${pid3}/failed`), { code: "guard_failed", detail: "the current core refuses this layer", rule_ids: ["no-core-import"] });
  const fseen = await vpGet(owner, pid3);
  check("a guard failure is reported and the initiator sees the code and the rule ids",
    failed.status === 200 && fseen.json?.status === "failed" && fseen.json?.failure_code === "guard_failed" && fseen.json?.failure_rule_ids?.[0] === "no-core-import" && !("origin" in fseen.json), brief(fseen));

  // ── refusals create nothing ─────────────────────────────────────────────────────────────
  const rowsBefore = await rowCount();
  const cur = await vpCreate(owner, current);
  check("the current design cannot be previewed (409 already_current)", cur.status === 409 && cur.json?.error?.code === "already_current", brief(cur));
  const bump = await svc("PUT", S("/sites/admin/state"), { main_sha: "d".repeat(40), core_version: "0.9.0", design_floor_version: "0.9.0" });
  const low = await vpCreate(owner, target);
  check("a design older than a core floor is refused with a reason (409 revert_below_core_floor)",
    bump.status === 200 && low.status === 409 && low.json?.error?.code === "revert_below_core_floor" && /core/i.test(low.json?.error?.message ?? ""), `${brief(bump)} ${brief(low)}`);
  await svc("PUT", S("/sites/admin/state"), { main_sha: "d".repeat(40), core_version: "0.3.0", design_floor_version: "0.2.0" });
  const notPub = await vpCreate(owner, busy);
  check("a request that is not a published design cannot be previewed (409 invalid_state)", notPub.status === 409 && notPub.json?.error?.code === "invalid_state", brief(notPub));
  check("none of the refusals created a row or used the allowance", (await rowCount()) === rowsBefore, `${rowsBefore} -> ${await rowCount()}`);

  // ── who may, and who may not ───────────────────────────────────────────────────────────────
  const sCreate = await vpCreate(staff);
  const sGet = await vpGet(staff, pid3);
  check("staff has no design.view: 403 on create and on read", sCreate.status === 403 && sGet.status === 403, `${sCreate.status} ${sGet.status}`);
  const edCreate = await made(editors[0], target, "editor");
  check("an editor (design.view) may preview", edCreate.status === 202, brief(edCreate));
  const mkOther = await req("POST", "/api/v1/admin/auth/login", { body: { email: "demo-owner@example.com", password: process.env.E2E_OWNER_PASSWORD || "demo-owner-change-me" } });
  if (mkOther.json?.access_token) {
    const o = mkOther.json.access_token;
    const cross = await req("GET", A(`/version-previews/${edCreate.json.id}`), { token: o });
    const own = await req("GET", `/api/v1/admin/shops/1/design/version-previews/${edCreate.json.id}`, { token: o });
    const xcreate = await req("POST", `/api/v1/admin/shops/1/design/requests/${target}/version-preview`, { token: o });
    check("another shop's owner: 403 on this shop, 404 for this shop's ids on theirs",
      cross.status === 403 && own.status === 404 && xcreate.status === 404, `${cross.status} ${own.status} ${xcreate.status}`);
  } else console.log("  - the demo shop's owner cannot log in here: cross-shop checks skipped");
  const svcCreate = await svc("POST", S(`/requests/${target}/version-preview`));
  const svcCreate2 = await svc("POST", S("/version-previews"), { target_request_id: target });
  const svcOnHuman = await svc("POST", A(`/requests/${target}/version-preview`));
  check("the service cannot create one (no route in its family) and a service key on the human route is 401",
    [404, 405].includes(svcCreate.status) && [404, 405].includes(svcCreate2.status) && svcOnHuman.status === 401, `${svcCreate.status} ${svcCreate2.status} ${svcOnHuman.status}`);
  if (c.otherKey) {
    const xclaim = await req("POST", `/api/v1/design-service/shops/${shop}/version-previews/${pid3}/claim`, { token: c.otherKey });
    check("another shop's credential cannot reach this shop's previews (404)", xclaim.status === 404, brief(xclaim));
  }

  // ── limits: 6 per hour per user, 30 per day per shop ─────────────────────────────────────────
  while (ownerMade < 6) { const r = await made(owner, target, "owner"); if (r.status !== 202) { fail(`owner preview within the hourly allowance failed: ${brief(r)}`); break; } }
  const rows6 = await rowCount();
  const over = await vpCreate(owner);
  check("the owner's 7th preview within the hour is refused: 429 hourly_cap_reached with Retry-After, no row",
    over.status === 429 && over.json?.error?.code === "hourly_cap_reached" && Number(over.headers.get("retry-after")) > 0 && (await rowCount()) === rows6, brief(over));
  const editorMade = [1, 0, 0, 0, 0]; // editors[0] already made one
  let dayHit = null;
  for (let guard = 0; guard < 40 && !dayHit; guard++) {
    const i = editorMade.findIndex((n) => n < 6);
    if (i < 0) break;
    const r = await vpCreate(editors[i], target);
    if (r.status === 202) { editorMade[i]++; shopMade++; } else dayHit = { r, shopMadeAtHit: shopMade };
  }
  check("another member has their own hourly allowance (editors made previews up to 6 each)", editorMade.some((n) => n === 6));
  check("the shop's 31st preview of the day is refused: 429 daily_cap_reached with Retry-After",
    dayHit?.r.status === 429 && dayHit.r.json?.error?.code === "daily_cap_reached" && Number(dayHit.r.headers.get("retry-after")) > 0 && dayHit.shopMadeAtHit === 30,
    dayHit ? `${brief(dayHit.r)} after ${dayHit.shopMadeAtHit}` : "no daily cap reached");

  // ── still no history, even after all that ───────────────────────────────────────────────────
  check("after dozens of previews the history is unchanged (events, requests, screenshots, approval state)", (await snapshot()) === before, `${before} -> ${await snapshot()}`);

  // ── read-only preview sessions on a preview origin (platform-enforced) ──────────────────────────
  const po = process.env.E2E_PREVIEW_ORIGIN;
  if (!po) {
    const msg = "E2E_PREVIEW_ORIGIN is not set (the API's ADMIN_PREVIEW_ORIGINS): read-only session checks skipped";
    if (process.env.E2E_REQUIRE_PREVIEW_ORIGIN === "1") fail(msg); else console.log(`  - ${msg}`);
  } else {
    // Logins are rate limited per client; this section logs in more than the limit allows in a minute, so wait it out.
    const login = async (origin, email, password) => {
      for (let i = 0; ; i++) {
        const r = await req("POST", "/api/v1/admin/auth/login", { headers: origin ? { Origin: origin } : {}, body: { email, password } });
        if (r.status !== 429 || i >= 3) return r;
        await sleep(Math.min(65, Number(r.headers.get("retry-after")) || 20) * 1000);
      }
    };
    const pv = await login(po, c.ownerEmail, c.ownerPassword);
    check("logging in from the preview origin gives a read-only session (scope preview_ro)", pv.status === 200 && pv.json?.scope === "preview_ro", brief(pv));
    const ro = pv.json?.access_token;
    const claims = ro ? JSON.parse(Buffer.from(ro.split(".")[1], "base64url").toString()) : {};
    check("the session lasts at most 30 minutes (absolute)", claims.exp && claims.iat && claims.exp - claims.iat <= 30 * 60, JSON.stringify({ d: claims.exp - claims.iat }));
    const viaPreview = (method, path, body, token = ro, origin = po) => req(method, path, { token, body, headers: origin ? { Origin: origin } : {} });
    check("the preview session can read the history", (await viaPreview("GET", A("/state"))).status === 200);
    const w1 = await viaPreview("POST", A(`/requests/${target}/version-preview`));
    const w2 = await viaPreview("POST", A(`/requests/${busy}/cancel`), {});
    const w3 = await viaPreview("POST", A(`/requests/${target}/revert`), {});
    check("every write from it is refused: 403 preview_read_only (version preview, cancel, revert)",
      [w1, w2, w3].every((r) => r.status === 403 && r.json?.error?.code === "preview_read_only"), `${w1.status} ${w2.status} ${w3.status}`);
    check("the busy request survived the attempted cancel", (await human("GET", A(`/requests/${busy}`))).json?.status === "awaiting_approval");
    const sig = await viaPreview("GET", A(`/requests/${target}/screenshots`));
    check("it cannot fetch signed screenshot URLs either (403)", sig.status === 403 && !sig.text.includes("X-Amz-"), brief(sig));
    const normalFromPreview = await viaPreview("GET", A("/state"), undefined, owner, po);
    check("an ordinary admin token is refused from the preview origin (403)", normalFromPreview.status === 403, brief(normalFromPreview));
    const elsewhere = await viaPreview("GET", A("/state"), undefined, ro, "https://elsewhere.example");
    const noOrigin = await viaPreview("GET", A("/state"), undefined, ro, null);
    check("the read-only token is useless from any other origin or without an Origin (401)", elsewhere.status === 401 && noOrigin.status === 401, `${elsewhere.status} ${noOrigin.status}`);
    const up = await req("POST", "/api/v1/admin/auth/refresh", { body: { refresh_token: pv.json?.refresh_token } });
    check("a read-only refresh token cannot be exchanged on an ordinary origin (no upgrade)", up.status === 401, brief(up));
    const mkSvc = await viaPreview("GET", S("/version-previews"), undefined, ro, po);
    check("the preview origin never reaches the service family (denied)", [401, 403].includes(mkSvc.status), brief(mkSvc));
    const normal = await login(null, c.ownerEmail, c.ownerPassword);
    check("the same user's ordinary login is unaffected (no scope, can still write)", normal.status === 200 && !normal.json?.scope, brief(normal));
  }

  // ── the logs never contain the origin ───────────────────────────────────────────────────────
  const logFile = process.env.E2E_API_LOG;
  if (!logFile) console.log("  - E2E_API_LOG is not set: the log check is skipped");
  else {
    let text = "";
    try { text = readFileSync(logFile, "utf8"); } catch (e) { fail(`cannot read E2E_API_LOG: ${e.message}`); }
    if (text) {
      check("the API log recorded the previews (usage lines) ...", /design version preview/.test(text));
      check("... but never the preview origin, a signed URL or a service key", !text.includes(secretHost) && !text.includes(`vp-e2e-short-${uniq}`) && !/X-Amz-Signature/.test(text) && !/dsk_[A-Za-z0-9]{20,}/.test(text));
    }
  }

  // Leave the shop as found: the busy request is cancelled by its owner.
  await human("POST", A(`/requests/${busy}/cancel`), {});
  void API; void runID;
}
