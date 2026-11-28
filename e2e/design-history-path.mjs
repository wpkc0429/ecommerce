// E2E for the design history (change design-history, slices 1 and 2). No browser.
//
// A FAKE SERVICE walks design D17's order against the real API: create -> claim -> head -> guard
// -> preview -> screenshots (before/after, real PNGs into the PRIVATE MinIO bucket) -> submit ->
// a person approves -> recheck -> published; then a second change and a revert that goes through
// the same steps. Along the way it proves the refusals: missing screenshots, a live-data admin
// capture, a head that moved, the service trying to approve, another shop's credential, and that
// the private bucket cannot be read anonymously (task 9.3).
//
// It needs the API to be started with a private bucket for the screenshots (a normal dev API has
// none and the section is skipped, like content-ops-path.mjs):
//
//   DESIGN_R2_BUCKET=ecommerce-design-history-e2e
//   DESIGN_R2_ACCESS_KEY_ID / DESIGN_R2_SECRET_ACCESS_KEY   (the MinIO root credentials, locally)
//   R2_ENDPOINT=http://127.0.0.1:9000                         (shared with media)
//
// Nothing here talks to Cloudflare. Runs standalone (`node design-history-path.mjs`) or imported
// by happy-path.mjs. Throwaway stack only.
import { deflateSync } from "node:zlib";
import { pathToFileURL } from "node:url";
import { API, brief, check, fail, failureCount, req, runID, section } from "./lib.mjs";
import { platformToken } from "./application-path.mjs";
import { runVersionPreviewPath } from "./design-version-preview-path.mjs";

// ── a tiny PNG encoder (RGB, filter 0) so the script needs no dependency ─────────────────
const crcTable = (() => {
  const t = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c >>> 0;
  }
  return t;
})();
const crc32 = (buf) => {
  let c = 0xffffffff;
  for (const b of buf) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
};
const chunk = (type, data) => {
  const head = Buffer.alloc(8);
  head.writeUInt32BE(data.length, 0);
  head.write(type, 4, "ascii");
  const tail = Buffer.alloc(4);
  tail.writeUInt32BE(crc32(Buffer.concat([head.subarray(4), data])), 0);
  return Buffer.concat([head, data, tail]);
};
export function makePng(width, height, seed = 0, extraChunks = []) {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 2; // RGB
  const stride = width * 3 + 1;
  const raw = Buffer.alloc(stride * height);
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const o = y * stride + 1 + x * 3;
      raw[o] = (x + seed) & 255;
      raw[o + 1] = y & 255;
      raw[o + 2] = seed & 255;
    }
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    ...extraChunks,
    chunk("IDAT", deflateSync(raw)),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

const manifest = (dataset = "sandbox-v1", origin = "sandbox") =>
  Buffer.from(JSON.stringify({ tool: "e2e-fake-service", tool_version: "1", dataset, api_origin_class: origin, ...(process.env.E2E_SANDBOX_ORIGIN ? { origin: process.env.E2E_SANDBOX_ORIGIN } : {}), captured_at: new Date().toISOString(), set_version: 1 })).toString("base64");

const hex = (n) => n.toString(16).padStart(2, "0").repeat(20); // a 40-hex commit id
const idem = () => `e2e-design-${runID}-${Math.random().toString(36).slice(2, 12)}`;

export async function runDesignHistoryPath() {
  section("㉗ Design history: fake service walks D17, screenshots in a private bucket, human approval, revert");
  const platform = await platformToken();

  // A fresh shop with an owner (same recipe as ⑯).
  // A suffix of our own: other sections of happy-path.mjs share runID and an e-mail collision would turn the owner into an existing account (no setup link).
  const uniq = `${runID}-${Math.random().toString(36).slice(2, 8)}`;
  const ownerEmail = `e2e-dhist-${uniq}@example.com`;
  const ownerPassword = `e2e-dhist-owner-${uniq}-pw`;
  const prov = await req("POST", "/api/v1/admin/shop-provisioning", { token: platform, body: { name: `E2E design history ${uniq}`, owner_email: ownerEmail, status: 1 } });
  if (prov.status !== 201) throw new Error(`shop provisioning failed: ${brief(prov)}`);
  const shop = prov.json.shop.id;
  const ownerId = prov.json.owner.user_id;
  const setupToken = String(prov.json.owner?.setup_url ?? "").split("/").pop();
  const setPw = await req("POST", "/api/v1/admin/auth/password-setup", { body: { token: setupToken, password: ownerPassword } });
  if (setPw.status !== 204) throw new Error(`owner password setup failed: ${brief(setPw)} (owner keys: ${Object.keys(prov.json.owner ?? {}).join(",")}, created: ${prov.json.owner?.created}, link length: ${setupToken.length})`);
  const login = await req("POST", "/api/v1/admin/auth/login", { body: { email: ownerEmail, password: ownerPassword } });
  const owner = login.json?.access_token;
  if (!owner) throw new Error(`owner login failed: ${brief(login)}`);

  // The platform (not the shop) issues the service credential; the key is shown once.
  const issued = await req("POST", "/api/v1/admin/design-service/credentials", { token: platform, body: { shop_id: shop, name: `e2e-${runID}` } });
  const key = issued.json?.key;
  check("the platform issues a service credential and shows the key once", issued.status === 201 && String(key).startsWith("dsk_"), brief(issued));
  const listed = await req("GET", `/api/v1/admin/design-service/credentials?shop_id=${shop}`, { token: platform });
  check("listing credentials never shows a key", listed.status === 200 && listed.json?.credentials?.length === 1 && !listed.text.includes(key), brief(listed));
  const other = await req("POST", "/api/v1/admin/design-service/credentials", { token: platform, body: { shop_id: 1, name: `e2e-other-${runID}` } });

  const A = (p) => `/api/v1/admin/shops/${shop}/design${p}`;
  const S = (p) => `/api/v1/design-service/shops/${shop}${p}`;
  const svc = (method, path, body, headers = {}) => req(method, path, { token: key, body, headers });
  const human = (method, path, body, headers = {}) => req(method, path, { token: owner, body, headers });

  // Is a private bucket configured on this API? (A GET on an unknown request answers 404 before 503, so probe an upload.)
  const probe = await svc("POST", S("/requests"), {
    site: "admin", requested_by_user_id: ownerId, prompt: "Make the order screen calmer", base_sha: hex(0xa1), core_version: "0.3.0", design_floor_version: "0.2.0",
  }, { "Idempotency-Key": idem() });
  if (probe.status !== 201) throw new Error(`creating a request failed: ${brief(probe)}`);
  const rid1 = probe.json.id;

  const upload = (rid, phase, role, vp, view, body, headers = {}) =>
    fetch(`${API}${S(`/requests/${rid}/screenshots/${phase}/${role}/${vp}/${view}`)}`, {
      method: "PUT",
      headers: { Authorization: `Bearer ${key}`, "Content-Type": "image/png", "X-Capture-Manifest": manifest(), ...headers },
      body,
    }).then(async (r) => ({ status: r.status, text: await r.text(), headers: r.headers, get json() { try { return JSON.parse(this.text); } catch { return null; } } }));

  const claim = await svc("POST", S(`/requests/${rid1}/claim`));
  check("the service claims the request", claim.status === 200, brief(claim));
  const head1 = hex(0xb1);
  const hd = await svc("PUT", S(`/requests/${rid1}/head`), {
    head_sha: head1,
    diff_summary: { files_changed: 1, additions: 3, deletions: 1, files: [{ path: "admin/design/theme.css", status: "modified", additions: 3, deletions: 1 }] },
  });
  check("the head is reported", hd.status === 200, brief(hd));
  const gr = await svc("POST", S(`/requests/${rid1}/guard-runs`), { checked_sha: head1, passed: true, rule_ids: [] });
  check("a passing guard result is recorded", gr.status === 200, brief(gr));
  const pv = await svc("PUT", S(`/requests/${rid1}/preview`), { origin: "https://p-e2e.preview.example", expires_at: new Date(Date.now() + 3 * 3600e3).toISOString() });
  check("the preview is registered", pv.status === 200 && pv.json?.status === "previewing", brief(pv));

  const first = await upload(rid1, "before", "owner", 390, "home", makePng(390, 30, 1));
  if (first.status === 503) {
    const msg = "the API has no private design bucket configured (DESIGN_R2_*): section ㉗ skipped";
    if (process.env.E2E_REQUIRE_DESIGN_STORAGE === "1") fail(msg);
    else console.log(`  - ${msg}`);
    return;
  }

  // ── screenshots ────────────────────────────────────────────────────────────────
  check("a before screenshot is stored (201) at the base commit", first.status === 201 && first.json?.commit_sha === hex(0xa1) && first.json?.attempt === 1, `${first.status} ${first.text}`);
  check("the response carries no object key and no URL", !/"object_key"|"url"/.test(first.text));
  const replay = await upload(rid1, "before", "owner", 390, "home", makePng(390, 30, 1));
  check("the same bytes again answer 200 and add nothing", replay.status === 200 && replay.json?.id === first.json?.id, `${replay.status} ${replay.text}`);
  const nextAttempt = await upload(rid1, "before", "owner", 390, "home", makePng(390, 30, 2));
  check("different bytes for the slot become attempt 2", nextAttempt.status === 201 && nextAttempt.json?.attempt === 2, `${nextAttempt.status} ${nextAttempt.text}`);

  const withMeta = makePng(390, 30, 3, [chunk("tEXt", Buffer.from("Author\0e2e-secret-metadata@example.com"))]);
  const meta = await upload(rid1, "before", "owner", 390, "orders", withMeta);
  check("a PNG with a metadata chunk is accepted (the chunk is dropped on re-encode)", meta.status === 201, `${meta.status} ${meta.text}`);
  check("a wrong-width image is refused (422 invalid_screenshot)", (await upload(rid1, "before", "owner", 1280, "home", makePng(390, 30, 1))).json?.error?.code === "invalid_screenshot");
  check("a non-PNG body is refused (422)", (await upload(rid1, "before", "owner", 390, "home", Buffer.from("not a png"))).status === 422);
  check("a body over 2 MiB is refused (413)", (await upload(rid1, "before", "owner", 390, "big", Buffer.alloc(2 * 1024 * 1024 + 1024))).status === 413);
  check("a checksum that does not match the upload is refused (422)", (await upload(rid1, "before", "owner", 390, "sum", makePng(390, 20, 5), { "X-Content-SHA256": "0".repeat(64) })).status === 422);
  const live = await upload(rid1, "before", "owner", 390, "orders", makePng(390, 30, 4), { "X-Capture-Manifest": manifest("public-live", "public") });
  check("an admin screenshot from live data is refused (422 live_data_capture_refused)", live.status === 422 && live.json?.error?.code === "live_data_capture_refused", `${live.status} ${live.text}`);

  const noMarker = await fetch(`${API}${S(`/requests/${rid1}/screenshots/before/owner/390/orders`)}`, {
    method: "PUT",
    headers: { Authorization: `Bearer ${key}`, "Content-Type": "image/png" }, // no X-Capture-Manifest at all
    body: makePng(390, 30, 4),
  });
  check("an admin screenshot with no capture manifest (no sandbox marker) is refused (422)", noMarker.status === 422, `${noMarker.status}`);

  // Not complete yet: only before exists.
  const early = await svc("POST", S(`/requests/${rid1}/submit`));
  check("submit without the after set is refused (409 screenshots_incomplete)", early.status === 409 && early.json?.error?.code === "screenshots_incomplete", brief(early));

  // The slots: home + orders at 390 and home at 1280, before and after.
  await upload(rid1, "before", "owner", 1280, "home", makePng(1280, 30, 1));
  await upload(rid1, "after", "owner", 390, "home", makePng(390, 30, 9));
  await upload(rid1, "after", "owner", 390, "orders", makePng(390, 30, 8));
  const aft = await upload(rid1, "after", "owner", 1280, "home", makePng(1280, 30, 9));
  check("an after screenshot is stored at the head", aft.status === 201 && aft.json?.commit_sha === head1, `${aft.status} ${aft.text}`);

  // ── signed reads and the private bucket (task 9.3) ───────────────────────────────
  const shots = await human("GET", A(`/requests/${rid1}/screenshots`));
  check("the owner lists the screenshots with signed URLs; no-store", shots.status === 200 && shots.headers.get("cache-control") === "no-store" && shots.json?.screenshots?.length === 6, brief(shots));
  const one = shots.json?.screenshots?.find((s) => s.url);
  const signed = one ? await fetch(one.url) : null;
  const body = signed ? Buffer.from(await signed.arrayBuffer()) : Buffer.alloc(0);
  check("the signed URL serves the PNG", signed?.status === 200 && body.subarray(1, 4).toString() === "PNG" && !body.includes("e2e-secret-metadata"), `${signed?.status}`);
  if (one) {
    const u = new URL(one.url);
    const bare = await fetch(`${u.origin}${u.pathname}`);
    check("the same object WITHOUT the signature is refused (403/404), anonymously", bare.status === 403 || bare.status === 404, `got ${bare.status}`);
    const root = await fetch(`${u.origin}/${u.pathname.split("/")[1]}/`);
    check("anonymous listing of the private bucket is refused", root.status === 403 || root.status === 404, `got ${root.status}`);
    const tampered = new URL(one.url);
    tampered.searchParams.set("X-Amz-Signature", "0".repeat(64));
    check("a tampered signature is refused", (await fetch(tampered)).status === 403);
    const exp = Date.parse(one.url_expires_at) - Date.now();
    check("the signed URL lives about 5 minutes", exp > 3 * 60e3 && exp < 6 * 60e3, `${exp}`);
  }
  check("a service credential cannot read screenshots", (await svc("GET", A(`/requests/${rid1}/screenshots`))).status === 401);
  check("another shop's credential gets 404 on this shop's upload", other.status !== 201 ||
    (await req("PUT", `/api/v1/design-service/shops/${shop}/requests/${rid1}/screenshots/before/owner/390/home`, { token: other.json.key })).status === 404);

  // ── submit, approval, publish ───────────────────────────────────────────────────
  const sub = await svc("POST", S(`/requests/${rid1}/submit`));
  check("with both sets the request is submitted (awaiting_approval)", sub.status === 200 && sub.json?.status === "awaiting_approval" && sub.json?.candidate_sha === head1, brief(sub));
  const detail = await human("GET", A(`/requests/${rid1}`));
  const etag = detail.headers.get("etag");
  check("the service cannot approve: no route in its family, and a service key on the human route is 401",
    (await svc("POST", S(`/requests/${rid1}/approve`), { head_sha: head1 })).status === 404 &&
      (await svc("POST", A(`/requests/${rid1}/approve`), { head_sha: head1 }, { "If-Match": etag })).status === 401);
  const wrong = await human("POST", A(`/requests/${rid1}/approve`), { head_sha: hex(0xee) }, { "If-Match": etag });
  check("approving a head other than the reviewed one is refused (409)", wrong.status === 409, brief(wrong));
  const noMatch = await human("POST", A(`/requests/${rid1}/approve`), { head_sha: head1 });
  check("approving without If-Match is refused (428)", noMatch.status === 428, brief(noMatch));
  const premature = await svc("POST", S(`/requests/${rid1}/published`), { published_sha: head1, tag: "design-admin-20260101-1" });
  check("publishing before approval is refused (409): the service cannot self-approve", premature.status === 409, brief(premature));
  const ok = await human("POST", A(`/requests/${rid1}/approve`), { head_sha: head1 }, { "If-Match": etag });
  const tag = ok.json?.publish_tag;
  check("the owner approves the exact head; the platform assigns the tag", ok.status === 200 && ok.json?.status === "approved" && ok.json?.approved_head_sha === head1 && /^design-admin-\d{8}-\d+$/.test(tag), brief(ok));
  const replayApprove = await human("POST", A(`/requests/${rid1}/approve`), { head_sha: head1 }, { "If-Match": etag });
  check("approval is single-use (a replay does not approve twice)", [200, 409, 412].includes(replayApprove.status), brief(replayApprove));
  const badTag = await svc("POST", S(`/requests/${rid1}/published`), { published_sha: head1, tag: "design-admin-19990101-1" });
  check("publishing without a recheck, or with another tag, is refused", badTag.status === 409 || badTag.status === 422, brief(badTag));
  await svc("POST", S(`/requests/${rid1}/guard-runs`), { phase: "recheck", checked_sha: head1, passed: true });
  const pub = await svc("POST", S(`/requests/${rid1}/published`), { published_sha: head1, tag, deployment_id: "e2e-dep-1" });
  check("after the recheck the service reports published and the platform accepts it", pub.status === 200 && pub.json?.status === "published" && pub.json?.published_tag === tag, brief(pub));

  const ev = await human("GET", A(`/requests/${rid1}/events`));
  const kinds = (ev.json?.events ?? []).map((e) => e.kind).join(",");
  check("the event log shows the whole order, ending in published",
    ev.status === 200 && kinds.startsWith("created,claimed,head_updated,guard_run,preview_registered,screenshots_complete,submitted,approved,guard_rechecked,published"), kinds);
  check("every event carries a hash (the chain)", (ev.json?.events ?? []).every((e) => /^[0-9a-f]{64}$/.test(e.hash)));
  const st = await human("GET", A("/state"));
  const adminState = st.json?.sites?.find((s) => s.site === "admin");
  check("the site's current design is now this request", adminState?.current_request_id === rid1 && adminState?.current_tag === tag && adminState?.current_sha === head1, brief(st));

  // A storefront request: public-live capture is fine for anonymous shots, a missing set blocks submit.
  const sf = await svc("POST", S("/requests"), { site: "storefront", requested_by_user_id: ownerId, prompt: "Bigger photos", base_sha: hex(0xc1), core_version: "0.3.0", design_floor_version: "0.2.0" }, { "Idempotency-Key": idem() });
  check("a storefront request can run beside the admin one", sf.status === 201, brief(sf));
  if (sf.status === 201) {
    const live2 = await upload(sf.json.id, "before", "anon", 390, "home", makePng(390, 20, 1), { "X-Capture-Manifest": manifest("public-live", "public") });
    check("a storefront screenshot of public live data is accepted", live2.status === 201, `${live2.status} ${live2.text}`);
    const loggedIn = await upload(sf.json.id, "before", "owner", 390, "home", makePng(390, 20, 2), { "X-Capture-Manifest": manifest("public-live", "public") });
    check("a logged-in storefront capture is refused (422 live_data_capture_refused)", loggedIn.status === 422 && loggedIn.json?.error?.code === "live_data_capture_refused", `${loggedIn.status} ${loggedIn.text}`);
    const cancel = await human("POST", A(`/requests/${sf.json.id}/cancel`), {});
    check("the person cancels it", cancel.status === 200 && cancel.json?.status === "cancelled", brief(cancel));
  }

  // ── a second published change, then a revert of the first ──────────────────────
  const driveToPublished = async (rid, head, label) => {
    await svc("POST", S(`/requests/${rid}/claim`));
    await svc("PUT", S(`/requests/${rid}/head`), { head_sha: head, diff_summary: { files_changed: 1, additions: 1, deletions: 0, files: [{ path: "admin/design/theme.css", status: "modified", additions: 1, deletions: 0 }] } });
    await svc("POST", S(`/requests/${rid}/guard-runs`), { checked_sha: head, passed: true, rule_ids: [] });
    await svc("PUT", S(`/requests/${rid}/preview`), { origin: "https://p-e2e2.preview.example", expires_at: new Date(Date.now() + 3 * 3600e3).toISOString() });
    const r = (await human("GET", A(`/requests/${rid}`))).json;
    for (const vp of [390, 1280]) {
      const a = await upload(rid, "before", "owner", vp, "home", makePng(vp, 20, 1));
      const b = await upload(rid, "after", "owner", vp, "home", makePng(vp, 20, 2));
      if (a.status !== 201 || b.status !== 201) { fail(`${label}: screenshot upload failed ${a.status} ${b.status} ${a.text}`); return null; }
    }
    const s = await svc("POST", S(`/requests/${rid}/submit`));
    if (s.status !== 200) { fail(`${label}: submit failed ${brief(s)}`); return null; }
    const tg = (await human("GET", A(`/requests/${rid}`)));
    const ap = await human("POST", A(`/requests/${rid}/approve`), { head_sha: head }, { "If-Match": tg.headers.get("etag") });
    await svc("POST", S(`/requests/${rid}/guard-runs`), { phase: "recheck", checked_sha: head, passed: true });
    const p = await svc("POST", S(`/requests/${rid}/published`), { published_sha: head, tag: ap.json?.publish_tag, deployment_id: `e2e-${label}` });
    return { approve: ap, published: p, base: r.base_sha };
  };

  const second = await svc("POST", S("/requests"), { site: "admin", requested_by_user_id: ownerId, prompt: "Warmer colours", base_sha: head1, core_version: "0.3.0", design_floor_version: "0.2.0" }, { "Idempotency-Key": idem() });
  check("a second admin change starts once the first is finished", second.status === 201, brief(second));
  const head2 = hex(0xb2);
  const d2 = second.status === 201 ? await driveToPublished(second.json.id, head2, "second") : null;
  check("the second change is published", d2?.published.status === 200 && d2.published.json?.status === "published", d2 ? brief(d2.published) : "not run");

  const rev = await human("POST", A(`/requests/${rid1}/revert`), { note: "back to the first design" });
  check("the person restores the first release: a NEW revert request is opened", rev.status === 201 && rev.json?.kind === "revert" && rev.json?.restores_request_id === rid1 && rev.json?.base_sha === head2, brief(rev));
  const original = await human("GET", A(`/requests/${rid1}`));
  check("history is not rewritten: the first request is still published", original.json?.status === "published" && original.json?.published_tag === tag);
  const queue = await svc("GET", S("/requests?status=drafting&claimed=false"));
  check("the service discovers the revert in its queue", queue.status === 200 && queue.json?.requests?.some((r) => r.id === rev.json?.id), brief(queue));
  check("a second revert while one is in progress is refused (409)", (await human("POST", A(`/requests/${rid1}/revert`), {})).status === 409);
  if (rev.status === 201) {
    const head3 = hex(0xb3);
    const d3 = await driveToPublished(rev.json.id, head3, "revert");
    check("the revert goes through the same screenshots and approval, and publishes", d3?.published.status === 200, d3 ? brief(d3.published) : "not run");
    const mid = await human("GET", A(`/requests/${second.json.id}`));
    check("the release published after the restored one is marked reverted and links to the revert request",
      mid.json?.status === "reverted" && mid.json?.reverted_by_request_id === rev.json.id, brief(mid));
    const now = await human("GET", A("/state"));
    check("the site's current design is the revert request", now.json?.sites?.find((x) => x.site === "admin")?.current_request_id === rev.json.id, brief(now));
  }

  // ── version previews (task 10.7): a request waits for approval while they run ───────────────
  const driveToAwaiting = async (label) => {
    const base = (await human("GET", A("/state"))).json?.sites?.find((x) => x.site === "admin")?.current_sha ?? hex(0xb3);
    const c = await svc("POST", S("/requests"), { site: "admin", requested_by_user_id: ownerId, prompt: "Version preview companion", base_sha: base, core_version: "0.3.0", design_floor_version: "0.2.0" }, { "Idempotency-Key": idem() });
    if (c.status !== 201) { fail(`${label}: create failed ${brief(c)}`); return null; }
    const rid = c.json.id, head = hex(0xb4);
    await svc("POST", S(`/requests/${rid}/claim`));
    await svc("PUT", S(`/requests/${rid}/head`), { head_sha: head, diff_summary: { files_changed: 1, additions: 1, deletions: 0, files: [{ path: "admin/design/theme.css", status: "modified", additions: 1, deletions: 0 }] } });
    await svc("POST", S(`/requests/${rid}/guard-runs`), { checked_sha: head, passed: true, rule_ids: [] });
    await svc("PUT", S(`/requests/${rid}/preview`), { origin: "https://p-e2e3.preview.example", expires_at: new Date(Date.now() + 3 * 3600e3).toISOString() });
    for (const vp of [390, 1280]) {
      const a = await upload(rid, "before", "owner", vp, "home", makePng(vp, 20, 1));
      const b = await upload(rid, "after", "owner", vp, "home", makePng(vp, 20, 2));
      if (a.status !== 201 || b.status !== 201) { fail(`${label}: screenshot upload failed ${a.status} ${b.status}`); return null; }
    }
    const sub = await svc("POST", S(`/requests/${rid}/submit`));
    if (sub.status !== 200) { fail(`${label}: submit failed ${brief(sub)}`); return null; }
    return rid;
  };
  if (rev.status === 201) {
    await runVersionPreviewPath({
      platform, shop, owner, ownerEmail, ownerPassword, A, S, svc, human,
      targetRid: rid1, currentRid: rev.json.id, driveToAwaiting, otherKey: other.json?.key,
    });
  }

  // ── what is left behind ─────────────────────────────────────────────────────────
  const all = await human("GET", A("/requests?page_size=50"));
  check("the list shows an excerpt, never the prompt body", all.status === 200 && all.json?.requests?.every((r) => !("prompt_text" in r)), brief(all));
  const svcRead = await svc("GET", S(`/requests/${rid1}`));
  check("the service reads state without the prompt", svcRead.status === 200 && !svcRead.text.includes("calmer"));
  check("no storage key or signed URL appears in a request detail", !JSON.stringify(original.json).includes("X-Amz-"));
  const revoke = await req("POST", `/api/v1/admin/design-service/credentials/${issued.json.credential?.id ?? issued.json.id}/revoke`, { token: platform });
  check("revoking the credential takes effect at once", revoke.status === 200 && (await svc("GET", S("/requests"))).status === 401, brief(revoke));
  if (other.status === 201) await req("POST", `/api/v1/admin/design-service/credentials/${other.json.credential?.id ?? other.json.id}/revoke`, { token: platform });
}

const isEntryPoint = Boolean(process.argv[1]) && import.meta.url === pathToFileURL(process.argv[1]).href;
if (isEntryPoint) {
  console.log(`E2E design history path against ${API} (run ${runID})`);
  try {
    await runDesignHistoryPath();
  } catch (err) {
    fail(`design history path aborted: ${err?.stack ?? err}`);
  }
  if (failureCount() > 0) {
    console.error(`\nE2E design history path FAILED with ${failureCount()} failed check(s)`);
    process.exit(1);
  }
  console.log("\nE2E design history path PASSED");
}
