// Shared E2E helpers (change commerce-demo-seed, design D2/D6/D7).
//
// Both e2e scripts import this module, and — importantly — they share ONE
// failure counter. happy-path.mjs runs the browser CMS flow and then the
// commerce path; if each script owned its own counter and its own exit, the
// first script's failures would be swallowed by the second one's exit(0).
import { createHash, createHmac } from "node:crypto";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

export const API = process.env.E2E_API_BASE_URL ?? "http://localhost:8080";

let failures = 0;

export function failureCount() {
  return failures;
}

export function check(name, cond, extra = "") {
  if (cond) {
    console.log(`  ✓ ${name}`);
  } else {
    failures++;
    console.error(`  ✗ ${name} ${extra}`);
  }
  return Boolean(cond);
}

/** Records a failure that is not a boolean assertion (a hard abort, a bad env). */
export function fail(message) {
  failures++;
  console.error(`  ✗ ${message}`);
}

export function section(title) {
  console.log(`\n${title}`);
}

/** Sends a request and always returns {status, json, text, headers} — never throws on 4xx/5xx. */
export async function req(method, path, { headers = {}, body, token, pk } = {}) {
  const h = { ...headers };
  if (body !== undefined) h["Content-Type"] = "application/json";
  if (token) h.Authorization = `Bearer ${token}`;
  if (pk) h["X-Publishable-Key"] = pk;
  const res = await fetch(`${API}${path}`, {
    method,
    headers: h,
    body: body === undefined ? undefined : typeof body === "string" ? body : JSON.stringify(body),
  });
  const text = await res.text();
  let json = null;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    /* non-JSON body: callers fall back to .text */
  }
  return { status: res.status, json, text, headers: res.headers };
}

/** Compact one-line rendering of a response, for assertion failure messages. */
export function brief(r) {
  return `${r.status} ${(r.text ?? "").slice(0, 300)}`;
}

// ── mock payment webhook signing (design D6) ────────────────────────────
//
// The secret MUST be resolved through exactly the sources the API server
// itself uses, in the same order:
//
//   process.env.PAYMENT_MOCK_WEBHOOK_SECRET   ← Makefile's `include .env; export`
//   the repo's .env                            ← what `make dev` actually loads
//   the literal default in config.Load()       ← when .env omits the key
//
// .env.example is deliberately NOT consulted. It is a template, not a config
// source, and it currently holds a *different* value ("…-change-me") from the
// built-in default. A .env created before payment-integration landed has no
// such key at all, so the server falls back to the built-in default — and an
// E2E that signed with the .env.example value would get a 401 on every webhook
// while every other assertion looked fine.
const CONFIG_DEFAULT_WEBHOOK_SECRET = "dev-only-mock-payment-webhook-secret";

function dotEnvValue(key) {
  try {
    const path = fileURLToPath(new URL("../.env", import.meta.url));
    for (const line of readFileSync(path, "utf8").split("\n")) {
      const trimmed = line.trim();
      if (!trimmed || trimmed.startsWith("#")) continue;
      const eq = trimmed.indexOf("=");
      if (eq < 0) continue;
      if (trimmed.slice(0, eq).trim() !== key) continue;
      return trimmed.slice(eq + 1).trim().replace(/^["']|["']$/g, "");
    }
  } catch {
    /* no .env: fall through to the built-in default */
  }
  return null;
}

export function webhookSecret() {
  const key = "PAYMENT_MOCK_WEBHOOK_SECRET";
  if (process.env[key]) return { value: process.env[key], source: "process.env" };
  const fromFile = dotEnvValue(key);
  if (fromFile) return { value: fromFile, source: "repo .env" };
  return { value: CONFIG_DEFAULT_WEBHOOK_SECRET, source: "config.Load() built-in default" };
}

/**
 * Delivers a signed mock-provider webhook. Returns the response.
 *
 * A 401 here is the one failure mode where every assertion is correct and the
 * environment is not: it cascades into "order unpaid → 0 points → a dozen red
 * checks" and sends the reader hunting in the wrong package. Say so explicitly.
 */
export async function deliverWebhook(providerReference, status = "succeeded") {
  const secret = webhookSecret();
  const body = JSON.stringify({ provider_reference: providerReference, status });
  const res = await req("POST", "/api/v1/webhooks/payments/mock", {
    headers: { "X-Payment-Signature": createHmac("sha256", secret.value).update(body).digest("hex") },
    body,
  });
  if (res.status === 401) {
    fail(
      `payment webhook rejected the signature (401). The API server and this E2E resolved ` +
        `different PAYMENT_MOCK_WEBHOOK_SECRET values — E2E used the one from "${secret.source}". ` +
        `Set PAYMENT_MOCK_WEBHOOK_SECRET identically for both, or add it to the repo .env.`,
    );
  }
  return res;
}

// ── 綠界 ECPay (change ecpay-payment-provider) ──────────────────────────

/**
 * ECPay's HashKey/HashIV, resolved the same way the mock webhook secret is:
 * process env → repo .env → the built-in default config.Load() uses.
 */
function ecpayKeys() {
  const pick = (key, fallback) =>
    process.env[key] || dotEnvValue(key) || fallback;
  return {
    hashKey: pick("ECPAY_HASH_KEY", "pwFHCqoQZGmho4w6"),
    hashIV: pick("ECPAY_HASH_IV", "EkRm7iFT261dpevs"),
    merchantID: pick("ECPAY_MERCHANT_ID", "3002607"),
  };
}

/**
 * .NET-compatible URL encoding, per 綠界's URLEncode 轉換表 (/2904/).
 *
 * Written as an explicit byte loop rather than encodeURIComponent + fixups on
 * purpose: this file is the SECOND implementation of the algorithm (the first
 * is api/internal/payment/ecpay), and its whole value is being independent. If
 * both sides shared the same shortcuts they would agree on the same mistake.
 *
 * Passthrough is alphanumerics plus `-_.!*()`; space becomes `+`; everything
 * else — including `~`, which is the one character .NET encodes but neither Go
 * nor JS does by default — becomes lowercase %XX over the UTF-8 bytes.
 */
function ecpayEncode(s) {
  const passthrough = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.!*()";
  const bytes = new TextEncoder().encode(s);
  let out = "";
  for (const b of bytes) {
    const ch = String.fromCharCode(b);
    if (ch === " ") out += "+";
    else if (b < 128 && passthrough.includes(ch)) out += ch;
    else out += "%" + b.toString(16).padStart(2, "0");
  }
  return out;
}

/**
 * 綠界's CheckMacValue: case-insensitive key sort, HashKey/HashIV wrapping,
 * .NET URL encoding, lowercase, SHA256, uppercase.
 */
export function ecpayCheckMac(fields) {
  const { hashKey, hashIV } = ecpayKeys();
  const names = Object.keys(fields)
    .filter((k) => k !== "CheckMacValue")
    .sort((a, b) => {
      const la = a.toLowerCase();
      const lb = b.toLowerCase();
      return la < lb ? -1 : la > lb ? 1 : a < b ? -1 : a > b ? 1 : 0;
    });
  const raw =
    `HashKey=${hashKey}` +
    names.map((n) => `&${n}=${fields[n]}`).join("") +
    `&HashIV=${hashIV}`;
  return createHash("sha256").update(ecpayEncode(raw).toLowerCase()).digest("hex").toUpperCase();
}

/**
 * Delivers a 綠界-shaped payment result notification and returns the response.
 *
 * `outcome` is one of:
 *   "success"  RtnCode=1, SimulatePaid=0 — the money really moved
 *   "simulate" RtnCode=1, SimulatePaid=1 — 綠界's 模擬付款 button; NO money moved
 *   "fail"     a real failure code
 *
 * Like deliverWebhook, this signs its own request rather than driving the
 * cashier simulator, so the ECPay chain still needs nothing but the API.
 *
 * shopID targets the per-shop webhook route (change
 * ecpay-per-shop-credentials, design D2) — credentials are resolved per shop
 * now, so the shop id has to travel in the URL the same way it travels in the
 * ReturnURL the API itself hands back at InitiatePayment time. The demo
 * shop's seeded credentials (internal/seed) use the same 綠界 sandbox sample
 * values this function's defaults assume, so ECPAY_HASH_KEY/ECPAY_HASH_IV
 * only need setting if that seed data was ever changed.
 */
export async function deliverECPayCallback(shopID, merchantTradeNo, amount, outcome = "success") {
  const { merchantID } = ecpayKeys();
  // 綠界 sends Taipei local time in this exact format, with no offset marker —
  // so the fixture has to be Taipei time too, or paid_at would be off by the
  // very 8 hours the parser is there to apply.
  const stamp = new Date(Date.now() + 8 * 3600 * 1000)
    .toISOString().slice(0, 19).replace("T", " ").replace(/-/g, "/");
  const codes = {
    success: { RtnCode: "1", RtnMsg: "交易成功", SimulatePaid: "0" },
    simulate: { RtnCode: "1", RtnMsg: "交易成功", SimulatePaid: "1" },
    fail: { RtnCode: "10100252", RtnMsg: "額度不足", SimulatePaid: "0" },
  }[outcome];
  const fields = {
    MerchantID: merchantID,
    MerchantTradeNo: merchantTradeNo,
    StoreID: "",
    // 綠界 issues ONE TradeNo per transaction and repeats it on every delivery
    // for that transaction (which is why the API records the first one it sees
    // and never overwrites it). Deriving it from the MerchantTradeNo reproduces
    // that; a fresh random value per call would be a fiction that happens to
    // make a lookup-by-TradeNo assertion fail for the wrong reason.
    TradeNo: `E${createHash("sha256").update(merchantTradeNo).digest("hex").slice(0, 15).toUpperCase()}`,
    TradeAmt: String(amount),
    PaymentDate: stamp,
    PaymentType: "Credit_CreditCard",
    PaymentTypeChargeFee: "10",
    TradeDate: stamp,
    ...codes,
  };
  fields.CheckMacValue = ecpayCheckMac(fields);
  const body = new URLSearchParams(fields).toString();
  const res = await req("POST", `/api/v1/webhooks/payments/ecpay/${shopID}`, {
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body,
  });
  if (res.status === 401 || (res.text ?? "").includes("CheckMacValue Error")) {
    fail(
      `綠界 callback rejected the checksum. The API server and this E2E resolved different ` +
        `ECPAY_HASH_KEY/ECPAY_HASH_IV values. Set them identically for both, or leave both unset ` +
        `to use the published test-store defaults.`,
    );
  }
  return { ...res, tradeNo: fields.TradeNo };
}

// ── misc ────────────────────────────────────────────────────────────────

/**
 * Mirrors points.recomputeLevel: the highest-min_points tier whose min_points
 * is at most `points`; null when none qualifies. Derived from the ladder the
 * API returns rather than hardcoded, so re-tuning the seeded thresholds does
 * not silently invalidate the assertions (design D7).
 */
export function tierFor(tiers, points) {
  let best = null;
  for (const t of tiers) {
    if (t.min_points <= points && (best === null || t.min_points > best.min_points)) best = t;
  }
  return best;
}

export function tierName(level) {
  return level ? level.name : "(無等級)";
}

/** A UTC calendar date `days` ahead, as YYYY-MM-DD. */
export function isoDate(days = 1) {
  return new Date(Date.now() + days * 86_400_000).toISOString().slice(0, 10);
}

/** Unique-per-run suffix so repeated runs never collide on an identity. */
export const runID = `${Date.now().toString(36)}`;
