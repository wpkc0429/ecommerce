// E2E for the merchant application flow (change platform-website, ecommerce side).
//
// No browser: the website lives in another repo, so this talks to the public
// endpoints the way the website's locked core will, and to the platform admin API
// the way the review UI does. (The review UI itself is driven by happy-path.mjs,
// section ㉔b.)
//
// Runs standalone (`node application-path.mjs`, needs only the API) or imported by
// happy-path.mjs, sharing lib.mjs's failure counter. Every run uses unique
// e-mails, so it is repeatable; it creates shops/accounts but touches nothing of
// the demo shop. Turnstile is the dev verifier: the API (APP_ENV development/test,
// no TURNSTILE_SECRET_KEY) accepts only Cloudflare's published dummy token.
import { pathToFileURL } from "node:url";
import { API, brief, check, dotEnvValue, fail, failureCount, getPlatformToken, req, runID, section } from "./lib.mjs";

export const DUMMY_TURNSTILE = "XXXX.DUMMY.TOKEN.XXXX";
const WEBSITE = process.env.E2E_WEBSITE_ORIGIN || dotEnvValue("WEBSITE_URL") || "http://localhost:4000";
const CONSENT = process.env.E2E_CONSENT_VERSION || dotEnvValue("APPLICATION_CONSENT_VERSION") || "placeholder-v0";

export const applicationBody = (email, extra = {}) => ({
  email, contact_name: "E2E 申請人", phone: "0912-345-678", shop_name: `E2E 申請店 ${runID}`,
  business_summary: "端到端測試用申請：販售手作花束。", consent_version: CONSENT,
  turnstile_token: DUMMY_TURNSTILE, website: "", elapsed_seconds: 30, ...extra,
});

export const submitApplication = (body) => req("POST", "/api/v1/public/shop-applications", { body });

/** Extracts the verification token from an outbox message body ({WEBSITE}/apply/verify#token). */
export function tokenFromMessage(text) {
  return /\/apply\/verify#([A-Za-z0-9_-]{20,})/.exec(text ?? "")?.[1] ?? null;
}

export async function platformToken() {
  let platform = getPlatformToken();
  if (platform && (await req("GET", "/api/v1/admin/me", { token: platform })).status === 200) return platform;
  const email = process.env.E2E_ADMIN_EMAIL || process.env.SEED_ADMIN_EMAIL || dotEnvValue("SEED_ADMIN_EMAIL") || "admin@example.com";
  const password = process.env.E2E_ADMIN_PASSWORD || process.env.SEED_ADMIN_PASSWORD || dotEnvValue("SEED_ADMIN_PASSWORD") || "admin-change-me";
  const r = await req("POST", "/api/v1/admin/auth/login", { body: { email, password } });
  if (r.status === 429) throw new Error("platform login is rate limited (429): wait and re-run");
  if (!r.json?.access_token) throw new Error(`platform admin login failed: ${brief(r)}`);
  return r.json.access_token;
}

/** Finds an application by e-mail through the admin list (search) and returns its detail. */
export async function findApplication(token, email, status) {
  const qs = new URLSearchParams({ q: email, page_size: "50" });
  if (status) qs.set("status", status);
  const list = await req("GET", `/api/v1/admin/shop-applications?${qs}`, { token });
  const row = list.json?.items?.find((a) => a.email === email);
  if (!row) return null;
  return (await req("GET", `/api/v1/admin/shop-applications/${row.id}`, { token })).json;
}

export async function runApplicationPath() {
  section("㉖ Merchant application: public intake → e-mail verification → review → provisioning (change platform-website)");
  const token = await platformToken();
  const A = "/api/v1/admin/shop-applications";
  const newEmail = `e2e-apply-${runID}@example.com`;
  const rejectEmail = `e2e-reject-${runID}@example.com`;
  const existingEmail = `e2e-existing-${runID}@example.com`;

  // ── Abuse defences on the public endpoint ──
  const first = await submitApplication(applicationBody(newEmail));
  check("a valid application is accepted with 202 {status: received}",
    first.status === 202 && first.text.trim() === '{"status":"received"}', brief(first));
  const dup = await submitApplication(applicationBody(newEmail));
  check("a duplicate submission gets the byte-identical response (no enumeration)",
    dup.status === first.status && dup.text === first.text, brief(dup));

  const honeypot = await submitApplication(applicationBody(`e2e-bot-${runID}@example.com`, { website: "http://spam.test" }));
  check("a filled honeypot is a 422 and creates nothing",
    honeypot.status === 422 && !(await findApplication(token, `e2e-bot-${runID}@example.com`)), brief(honeypot));
  const consent = await submitApplication(applicationBody(`e2e-consent-${runID}@example.com`, { consent_version: "not-the-current-one" }));
  check("a wrong consent version is a 422 pointing at /consent_version",
    consent.status === 422 && JSON.stringify(consent.json).includes("/consent_version"), brief(consent));
  const badTok = await submitApplication(applicationBody(`e2e-badtok-${runID}@example.com`, { turnstile_token: "nope" }));
  check("a rejected Turnstile token is a 422 pointing at /turnstile_token and creates nothing",
    badTok.status === 422 && JSON.stringify(badTok.json).includes("/turnstile_token") &&
    !(await findApplication(token, `e2e-badtok-${runID}@example.com`)), brief(badTok));
  const invalid = await submitApplication(applicationBody("not-an-email"));
  check("an invalid e-mail is a 422", invalid.status === 422, brief(invalid));
  const big = await submitApplication({ ...applicationBody(`e2e-big-${runID}@example.com`), pad: "x".repeat(17000) });
  check("a body over 16 KiB is a 413", big.status === 413 && big.json?.error?.code === "payload_too_large", brief(big));

  const cors = await fetch(`${API}/api/v1/public/shop-applications`, {
    method: "OPTIONS",
    headers: { Origin: WEBSITE, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "content-type" },
  });
  check("CORS preflight allows the website origin", cors.status === 204 && cors.headers.get("access-control-allow-origin") === WEBSITE,
    `${cors.status} ${cors.headers.get("access-control-allow-origin")}`);
  const evil = await fetch(`${API}/api/v1/public/shop-applications`, {
    method: "OPTIONS",
    headers: { Origin: "https://evil.example", "Access-Control-Request-Method": "POST" },
  });
  check("CORS preflight does not allow any other origin", evil.headers.get("access-control-allow-origin") === null);
  const other = await fetch(`${API}/api/v1/admin/me`, {
    method: "OPTIONS",
    headers: { Origin: WEBSITE, "Access-Control-Request-Method": "GET" },
  });
  check("the website origin gets no CORS on other API surfaces", other.headers.get("access-control-allow-origin") === null);

  // ── Verification through the manual outbox ──
  let app = await findApplication(token, newEmail);
  check("the application is pending e-mail verification, unverified applications are listed but not approvable",
    app?.status === "pending_verification" && app.email_verified === false, brief({ status: 0, text: JSON.stringify(app) }));
  const early = await req("POST", `${A}/${app?.id}/approve`, { token, body: {} });
  check("approving before verification is a 409 and opens no shop", early.status === 409, brief(early));
  const msg = app?.messages?.find((m) => m.kind === "verify_email");
  check("the verification mail waits in the outbox (manual delivery when SMTP is not configured)",
    Boolean(msg) && (msg.delivery === "manual" ? msg.status === "pending_manual" : ["queued", "sent", "failed"].includes(msg.status)), JSON.stringify(msg));
  const tok = tokenFromMessage(msg?.body_text);
  if (!tok) {
    // SMTP configured on this API: the body is gone once sent. Fall back to the operator's manual confirmation.
    console.log("  (verification mail was sent by SMTP; using the operator's manual confirmation)");
    const vm = await req("POST", `${A}/${app.id}/verify-manually`, { token });
    check("manual verification works", vm.status === 200, brief(vm));
  } else {
    const bad = await req("POST", "/api/v1/public/shop-applications/verify", { body: { token: tok.slice(0, -2) + "xx" } });
    check("a wrong token is a 404", bad.status === 404, brief(bad));
    const ok = await req("POST", "/api/v1/public/shop-applications/verify", { body: { token: tok } });
    check("the verification link's token verifies the application", ok.status === 200 && ok.json?.status === "verified", brief(ok));
    const again = await req("POST", "/api/v1/public/shop-applications/verify", { body: { token: tok } });
    check("the token is single-use (same 404 as an unknown token)", again.status === 404 && again.text === bad.text, brief(again));
  }
  app = await findApplication(token, newEmail);
  check("the application is now pending review", app?.status === "pending_review" && app.email_verified === true, app?.status);

  // ── Approval: one transaction, one-time link, no password set ──
  const approve = await req("POST", `${A}/${app.id}/approve`, { token, body: { shop_name: `E2E 申請店 ${runID}` } });
  check("approving opens the shop and returns the owner's one-time setup link",
    approve.status === 201 && approve.json?.shop?.id > 0 && approve.json?.owner?.created === true &&
    approve.json.owner.delivery === "manual" && String(approve.json.owner.setup_url).includes("/#/set-password/"), brief(approve));
  check("the new shop is under review (storefront unreachable) by default", approve.json?.shop?.status === 2, brief(approve));
  const again = await req("POST", `${A}/${app.id}/approve`, { token, body: {} });
  check("approving twice is a 409", again.status === 409, brief(again));
  const detail = (await req("GET", `${A}/${app.id}`, { token })).json;
  const setupToken = String(approve.json?.owner?.setup_url ?? "").split("/").pop();
  check("the application is approved and points at the new shop",
    detail?.status === "approved" && detail.provisioned_shop_id === approve.json?.shop?.id, JSON.stringify(detail).slice(0, 200));
  check("the setup link is written to no message and no audit event",
    !JSON.stringify(detail).includes(setupToken) && setupToken.length > 20);
  check("the audit timeline records submission, verification and approval",
    ["submitted", "verified", "approved"].every((e) => detail?.events?.some((x) => x.event === e) || (e === "verified" && !tok)));
  check("no event detail contains the applicant's e-mail or name",
    !detail?.events?.some((e) => JSON.stringify(e.detail ?? {}).includes("@") || JSON.stringify(e.detail ?? {}).includes("E2E 申請人")));

  const ownerPw = `e2e-apply-owner-${runID}-pw`;
  const set = await req("POST", "/api/v1/admin/auth/password-setup", { body: { token: setupToken, password: ownerPw } });
  check("the owner sets their own password through the one-time link", set.status === 204 || set.status === 200, brief(set));
  const ownerLogin = await req("POST", "/api/v1/admin/auth/login", { body: { email: newEmail, password: ownerPw } });
  check("the owner can log in", ownerLogin.status === 200 && Boolean(ownerLogin.json?.access_token), brief(ownerLogin));
  const ownerTok = ownerLogin.json?.access_token;
  const asOwner = await req("GET", A, { token: ownerTok });
  check("a merchant owner cannot read or review applications", asOwner.status === 403, brief(asOwner));
  const ownerShop = await req("GET", `/api/v1/admin/shops/${approve.json.shop.id}/me`, { token: ownerTok });
  check("the owner holds merchant_owner permissions in the new shop, none of shop_application.*",
    ownerShop.status === 200 && !ownerShop.json.permissions.some((p) => p.startsWith("shop_application.")) &&
    ownerShop.json.permissions.includes("content.manage"), brief(ownerShop));

  // ── Rejection with a visible reason, cooldown ──
  await submitApplication(applicationBody(rejectEmail));
  let rej = await findApplication(token, rejectEmail);
  const noReason = await req("POST", `${A}/${rej.id}/reject`, { token, body: {} });
  check("rejecting without a reason is a 422", noReason.status === 422, brief(noReason));
  const reason = `資料不足 ${runID}`;
  const rejected = await req("POST", `${A}/${rej.id}/reject`, { token, body: { decision_reason: reason, internal_note: "e2e" } });
  check("rejecting with a reason works", rejected.status === 200 && rejected.json?.status === "rejected", brief(rejected));
  rej = await findApplication(token, rejectEmail);
  const rejMsg = rej?.messages?.find((m) => m.kind === "rejected");
  check("the reason reaches the applicant through the outbox; the internal note does not",
    Boolean(rejMsg) && (!rejMsg.body_text || (rejMsg.body_text.includes(reason) && !rejMsg.body_text.includes("e2e"))), JSON.stringify(rejMsg));
  const reject2 = await req("POST", `${A}/${rej.id}/reject`, { token, body: { decision_reason: "again" } });
  check("a decided application cannot be rejected again", reject2.status === 409, brief(reject2));
  const cool = await submitApplication(applicationBody(rejectEmail));
  check("re-applying inside the cooldown gets the identical 202", cool.status === first.status && cool.text === first.text, brief(cool));
  const list = await req("GET", `${A}?q=${encodeURIComponent(rejectEmail)}`, { token });
  check("re-applying inside the cooldown created no new application", list.json?.total === 1, brief(list));

  // ── Existing back-office account: explicit confirmation, password untouched ──
  const acct = await req("POST", "/api/v1/admin/accounts", { token, body: { email: existingEmail } });
  check("a back-office account without a password exists", acct.status === 201, brief(acct));
  await submitApplication(applicationBody(existingEmail));
  const ex = await findApplication(token, existingEmail);
  check("the reviewer sees the email_has_account warning", ex?.email_has_account === true && ex.risk_flags?.email_has_account === true,
    JSON.stringify(ex?.risk_flags));
  const vm = await req("POST", `${A}/${ex.id}/verify-manually`, { token });
  check("manual verification (audited) makes it reviewable", vm.status === 200, brief(vm));
  const unconfirmed = await req("POST", `${A}/${ex.id}/approve`, { token, body: {} });
  check("approving without confirm_existing_account is a 409",
    unconfirmed.status === 409 && unconfirmed.json?.error?.code === "confirm_existing_account_required", brief(unconfirmed));
  const confirmed = await req("POST", `${A}/${ex.id}/approve`, { token, body: { confirm_existing_account: true } });
  check("with confirmation the account is only added as owner: no link, no password",
    confirmed.status === 201 && confirmed.json?.owner?.created === false && !("setup_url" in confirmed.json.owner), brief(confirmed));
  const exLogin = await req("POST", "/api/v1/admin/auth/login", { body: { email: existingEmail, password: "anything-at-all-123" } });
  check("the existing account still has no password (cannot log in)", exLogin.status === 401, brief(exLogin));
}

const isEntryPoint = Boolean(process.argv[1]) && import.meta.url === pathToFileURL(process.argv[1]).href;
if (isEntryPoint) {
  console.log(`E2E application path against ${API} (run ${runID})`);
  try {
    await runApplicationPath();
  } catch (err) {
    fail(`application path aborted: ${err?.stack ?? err}`);
  }
  if (failureCount() > 0) {
    console.error(`\nE2E application path FAILED with ${failureCount()} failed check(s)`);
    process.exit(1);
  }
  console.log("\nE2E application path PASSED");
}
