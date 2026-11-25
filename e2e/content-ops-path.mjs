// E2E for the third slice of the headless content store (change
// headless-content-store): the publish hook, scheduled publishing and the audit
// purge. No browser: the admin screens are a later task.
//
// It needs the API to be started for it (a normal dev API is not enough):
//
//   CONTENT_HOOK_DEV_INSECURE=1        (http + loopback for the stub receiver; refused in production)
//   CONTENT_HOOK_ALLOWED_HOSTS=localhost
//   CONTENT_HOOK_DEBOUNCE=1s CONTENT_HOOK_MIN_INTERVAL=0s CONTENT_HOOK_FLUSH_INTERVAL=500ms
//   CONTENT_SCHEDULE_MIN_LEAD=0s       (a schedule may be set for the next minute)
//
// and, for the scheduler/purge commands, E2E_DATABASE_URL (the database of that
// API) plus the same REDIS_DB. The scheduler's in-process ticker is NOT enabled
// on purpose: the run proves that a due schedule waits for the command.
//
// The receiver is a stub this script starts on a loopback port (E2E_HOOK_STUB_PORT,
// default 8190). Nothing here ever talks to Cloudflare.
//
// Runs standalone (`node content-ops-path.mjs`) or imported by happy-path.mjs.
import { createHmac } from "node:crypto";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { createServer } from "node:http";
import { fileURLToPath, pathToFileURL } from "node:url";
import { API, brief, check, fail, failureCount, req, runID, section } from "./lib.mjs";
import { platformToken } from "./application-path.mjs";

const STUB_PORT = Number(process.env.E2E_HOOK_STUB_PORT ?? 8190);
const API_DIR = fileURLToPath(new URL("../api/", import.meta.url));
const execFileAsync = promisify(execFile);
// The commands run asynchronously on purpose: the stub receiver lives in this process and must keep answering while they run.
const runGo = async (args, env) => (await execFileAsync("go", args, { cwd: API_DIR, env, encoding: "utf8", timeout: 180000 })).stdout;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function waitFor(fn, ms, step = 250) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) return v;
    await sleep(step);
  }
}

function startStub() {
  const received = [];
  const state = { status: 200 };
  const server = createServer((rq, rs) => {
    const chunks = [];
    rq.on("data", (c) => chunks.push(c));
    rq.on("end", () => {
      received.push({ method: rq.method, url: rq.url, headers: rq.headers, body: Buffer.concat(chunks).toString("utf8") });
      rs.writeHead(state.status, { "Content-Type": "application/json" });
      rs.end(JSON.stringify({ success: true, internal: "STUB-INTERNAL-DETAIL" }));
    });
  });
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(STUB_PORT, "127.0.0.1", () => resolve({ server, received, state }));
  });
}

const noteSchema = {
  type: "object", additionalProperties: false,
  properties: { title: { type: "string", maxLength: 100, default: "" } },
};

export async function runContentOpsPath() {
  section("⑯ Content ops: publish hook (local stub receiver), scheduled publishing, audit purge dry run");
  const hookURL = `http://localhost:${STUB_PORT}/hook-e2e-secret-${runID}`;
  const platform = await platformToken();

  // A brand-new shop with a registered schema (same recipe as section ⑮).
  const ownerEmail = `e2e-ops-${runID}@example.com`;
  const ownerPassword = `e2e-ops-owner-${runID}-pw`;
  const prov = await req("POST", "/api/v1/admin/shop-provisioning", {
    token: platform, body: { name: `E2E ops ${runID}`, owner_email: ownerEmail, status: 1 },
  });
  if (prov.status !== 201) throw new Error(`shop provisioning failed: ${brief(prov)}`);
  const shop = prov.json.shop.id;
  const sp = (p) => `/api/v1/admin/shops/${shop}${p}`;
  const setupToken = String(prov.json.owner?.setup_url ?? "").split("/").pop();
  const setPw = await req("POST", "/api/v1/admin/auth/password-setup", { body: { token: setupToken, password: ownerPassword } });
  if (setPw.status !== 204) throw new Error(`owner password setup failed: ${brief(setPw)}`);
  const login = await req("POST", "/api/v1/admin/auth/login", { body: { email: ownerEmail, password: ownerPassword } });
  const owner = login.json?.access_token;
  if (!owner) throw new Error(`owner login failed: ${brief(login)}`);
  const keyRes = await req("POST", sp("/publishable-keys"), { token: owner, body: { label: `e2e-ops-${runID}` } });
  const pk = keyRes.json?.key;
  const schema = {
    format: 2,
    types: { note: { title: "Note", schema: noteSchema } },
    documents: { banner: { type: "note" }, promo: { type: "note" }, extra: { type: "note" } },
    collections: {},
  };
  const reg = await req("PUT", sp("/content/schema"), { token: owner, body: schema });
  if (reg.status !== 200) throw new Error(`schema registration failed: ${brief(reg)}`);

  const draft = async (key, title) => {
    const cur = await req("GET", sp(`/content/documents/${key}`), { token: owner });
    const r = await req("PUT", sp(`/content/documents/${key}`), {
      token: owner, headers: { "If-Match": `"${cur.json.draft_rev}"` }, body: { content: { title } },
    });
    return r;
  };
  const publish = (key) => req("POST", sp(`/content/documents/${key}/publish`), { token: owner });

  // ── the publish hook ────────────────────────────────────────────────────
  const probe = await req("PUT", sp("/content/publish-hook?dry_run=true"), { token: owner, body: { url: hookURL } });
  if (probe.status === 422) {
    const msg = "the API is not configured for the content-ops e2e (CONTENT_HOOK_DEV_INSECURE=1 CONTENT_HOOK_ALLOWED_HOSTS=localhost): section ⑯ skipped";
    if (process.env.E2E_REQUIRE_CONTENT_OPS === "1") fail(msg);
    else console.log(`  - ${msg}`);
    return;
  }
  const stub = await startStub();
  try {
    check("a URL on an unlisted host is refused by dry run (422) and the error does not echo it",
      await (async () => {
        const r = await req("PUT", sp("/content/publish-hook?dry_run=true"), { token: owner, body: { url: "https://evil.test/hook-e2e-secret-x" } });
        return r.status === 422 && !r.text.includes("hook-e2e-secret-x");
      })());
    check("a private-address literal is refused (422)",
      (await req("PUT", sp("/content/publish-hook"), { token: owner, body: { url: "https://10.0.0.5/x" } })).status === 422);
    check("dry run of the stub URL passes (host on the allowlist) and stores nothing",
      probe.status === 200 && probe.json?.host === "localhost" &&
        (await req("GET", sp("/content/publish-hook"), { token: owner })).json?.configured === false, brief(probe));

    const editor = await req("GET", sp("/me"), { token: owner });
    check("the owner holds content.manage", (editor.json?.permissions ?? []).includes("content.manage"));

    const set = await req("PUT", sp("/content/publish-hook"), { token: owner, body: { url: hookURL } });
    check("the hook is set (write-only)", set.status === 200 && set.json?.configured === true && set.json?.enabled === true, brief(set));
    const st = await req("GET", sp("/content/publish-hook"), { token: owner });
    check("the status carries the host and a masked URL but never the URL or a secret",
      st.status === 200 && st.json?.host === "localhost" && !st.text.includes(`hook-e2e-secret-${runID}`) &&
        !st.text.includes("signing_secret") && st.json?.url_masked?.startsWith("http"), st.text);
    const rot = await req("POST", sp("/content/publish-hook/signing-secret"), { token: owner });
    const secret = rot.json?.signing_secret;
    check("the signing secret is shown once", rot.status === 200 && String(secret).length === 64, brief(rot));
    check("it is not readable afterwards",
      !(await req("GET", sp("/content/publish-hook"), { token: owner })).text.includes(secret));

    const test = await req("POST", sp("/content/publish-hook/test"), { token: owner });
    check("a test call reaches the stub and reports only the category",
      test.status === 200 && test.json?.ok === true && test.json?.result === "ok" && !test.text.includes("STUB-INTERNAL-DETAIL"), brief(test));
    const t = stub.received.at(-1);
    const verify = (call) => {
      const ts = call.headers["x-content-timestamp"];
      const want = `sha256=${createHmac("sha256", secret).update(`${ts}.${call.body}`).digest("hex")}`;
      return call.headers["x-content-signature"] === want;
    };
    check("the receiver can verify the signature and the body carries no content",
      t && JSON.parse(t.body).event === "content.test" && verify(t) && t.url === `/hook-e2e-secret-${runID}` && !t.body.includes("title"),
      JSON.stringify(t));

    // A burst of publishes is merged into one call.
    stub.received.length = 0;
    for (const [key, title] of [["banner", "hello"], ["promo", "promo"], ["extra", "extra"], ["banner", "hello 2"]]) {
      const d = await draft(key, title);
      check(`draft ${key} saved`, d.status === 200, brief(d));
      const p = await publish(key);
      check(`published ${key} (not slowed by the hook)`, p.status === 200, brief(p));
    }
    const got = await waitFor(() => stub.received.length >= 1, 20000);
    await sleep(4000); // a second call would show up by now
    const posts = stub.received.filter((c) => JSON.parse(c.body).event === "content.published");
    check("four publishes produced exactly one debounced call", got && posts.length === 1, `calls=${stub.received.length}`);
    const body = JSON.parse(posts[0]?.body ?? "{}");
    check("the call is signed, names the shop and merges the burst, with no content",
      verify(posts[0]) && body.shop_id === shop && body.coalesced >= 2 && !posts[0].body.includes("hello"), posts[0]?.body);
    const pubRead = await req("GET", "/api/v1/shop/content/banner", { pk });
    check("the content itself is public at once (the hook only triggers a rebuild)", pubRead.status === 200 && pubRead.json?.content?.title === "hello 2");

    // A failing receiver is reported, not hidden.
    stub.state.status = 500;
    const fire = await req("POST", sp("/content/publish-hook/fire"), { token: owner });
    check("a manual rebuild against a failing receiver reports http_error",
      fire.status === 200 && fire.json?.ok === false && fire.json?.result === "http_error", brief(fire));
    const after = await req("GET", sp("/content/publish-hook"), { token: owner });
    check("the status shows the failure", after.json?.last_status === "http_error" && after.json?.consecutive_failures >= 1, after.text);
    stub.state.status = 200;
    const fire2 = await req("POST", sp("/content/publish-hook/fire"), { token: owner });
    check("and recovers on the next success", fire2.json?.ok === true &&
      (await req("GET", sp("/content/publish-hook"), { token: owner })).json?.consecutive_failures === 0, brief(fire2));

    // ── scheduled publishing ─────────────────────────────────────────────
    const dbURL = process.env.E2E_DATABASE_URL || process.env.DATABASE_URL;
    const cmdEnv = { ...process.env, ...(dbURL ? { DATABASE_URL: dbURL } : {}) };
    const when = new Date(Date.now() + 5000).toISOString();
    const dBanner = await draft("banner", "scheduled title");
    const dPromo = await draft("promo", "to be edited");
    const sBanner = await req("PUT", sp("/content/documents/banner/schedule"), {
      token: owner, headers: { "If-Match": `"${dBanner.json.draft_rev}"` }, body: { publish_at: when },
    });
    check("banner is scheduled for the next minute (UTC, whole minute)",
      sBanner.status === 201 && sBanner.json?.status === "pending" && /:00(\.0+)?Z$/.test(sBanner.json?.publish_at ?? ""), brief(sBanner));
    const sPromo = await req("PUT", sp("/content/documents/promo/schedule"), {
      token: owner, headers: { "If-Match": `"${dPromo.json.draft_rev}"` }, body: { publish_at: when },
    });
    check("promo is scheduled too", sPromo.status === 201, brief(sPromo));
    const noRev = await req("PUT", sp("/content/documents/extra/schedule"), { token: owner, body: { publish_at: when } });
    check("a schedule without If-Match is a 428", noRev.status === 428, brief(noRev));
    const noZone = await req("PUT", sp("/content/documents/extra/schedule"), {
      token: owner, headers: { "If-Match": `"1"` }, body: { publish_at: "2099-01-01T10:00:00" },
    });
    check("a time without a UTC offset is a 422", noZone.status === 422, brief(noZone));
    const listed = await req("GET", sp("/content/documents"), { token: owner });
    check("the document list shows scheduled_publish_at",
      listed.json?.documents?.find((d) => d.key === "banner")?.scheduled_publish_at === sBanner.json.publish_at, listed.text.slice(0, 300));
    const upcoming = await req("GET", sp("/content/schedules"), { token: owner });
    check("the upcoming list has both", upcoming.json?.schedules?.length === 2, brief(upcoming));
    // Editing promo after scheduling invalidates its schedule at once.
    const edited = await draft("promo", "edited after scheduling");
    const sPromo2 = await req("GET", sp("/content/documents/promo/schedule"), { token: owner });
    check("an edit after scheduling supersedes the schedule immediately",
      edited.status === 200 && sPromo2.json?.status === "superseded" && sPromo2.json?.result === "edited", brief(sPromo2));

    // Wait for the time to pass; the API's ticker is off, so nothing happens by itself.
    const dueMs = Date.parse(sBanner.json.publish_at) - Date.now();
    console.log(`  … waiting ${Math.max(0, Math.ceil(dueMs / 1000)) + 3}s for the scheduled minute`);
    await sleep(Math.max(0, dueMs) + 3000);
    const early = await req("GET", "/api/v1/shop/content/banner", { pk });
    check("the in-process ticker is off by default: the due schedule is still waiting", early.json?.content?.title === "hello 2",
      brief(early));
    if (!dbURL) {
      console.log("  - E2E_DATABASE_URL/DATABASE_URL not set: scheduler and purge commands skipped");
      return;
    }
    stub.received.length = 0;
    const out = await runGo(["run", "./cmd/content-scheduler"], cmdEnv);
    check("the scheduler command published exactly the valid schedule", /due=\d+ published=1 /.test(out), out.trim());
    const pubd = await req("GET", "/api/v1/shop/content/banner", { pk });
    check("the scheduled content is live and the cache was invalidated by the command",
      pubd.status === 200 && pubd.json?.content?.title === "scheduled title", brief(pubd));
    const promoRead = await req("GET", "/api/v1/shop/content/promo", { pk });
    check("the edited (superseded) draft was NOT published", promoRead.json?.content?.title === "promo", brief(promoRead));
    const done = await req("GET", sp("/content/documents/banner/schedule"), { token: owner });
    check("the schedule is done", done.json?.status === "done" && done.json?.result === "published", brief(done));
    const again = await runGo(["run", "./cmd/content-scheduler"], cmdEnv);
    check("a second run does nothing", /due=0 published=0 /.test(again), again.trim());
    const hooked = await waitFor(() => stub.received.some((c) => JSON.parse(c.body).event === "content.published"), 15000);
    check("the scheduler's publish also triggered the publish hook (sent by the command itself, signed)",
      Boolean(hooked) && /hook_sent=1 /.test(out), `calls=${stub.received.length} ${out.trim()}`);
    const doc = await req("GET", sp("/content/documents/banner"), { token: owner });
    check("the document list no longer shows a pending schedule", doc.json?.scheduled_publish_at === null, brief(doc));

    // ── audit purge (dry run) ───────────────────────────────────────────
    const purge = await runGo(["run", "./cmd/purge-content-audit"], cmdEnv);
    check("the purge command is a dry run by default and prints counts only",
      purge.includes("dry run; pass -apply to delete") && /\d+ audit rows to delete/.test(purge) && !purge.includes(hookURL), purge.trim());
  } finally {
    await new Promise((r) => stub.server.close(r));
  }
}

const isEntryPoint = Boolean(process.argv[1]) && import.meta.url === pathToFileURL(process.argv[1]).href;
if (isEntryPoint) {
  console.log(`E2E content ops path against ${API} (run ${runID})`);
  try {
    await runContentOpsPath();
  } catch (err) {
    fail(`content ops path aborted: ${err?.stack ?? err}`);
  }
  if (failureCount() > 0) {
    console.error(`\nE2E content ops path FAILED with ${failureCount()} failed check(s)`);
    process.exit(1);
  }
  console.log("\nE2E content ops path PASSED");
}
