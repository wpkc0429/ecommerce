// Shared E2E helpers (change commerce-demo-seed, design D2/D6/D7).
//
// Both e2e scripts import this module, and — importantly — they share ONE
// failure counter. happy-path.mjs runs the browser CMS flow and then the
// commerce path; if each script owned its own counter and its own exit, the
// first script's failures would be swallowed by the second one's exit(0).
import { createHmac } from "node:crypto";
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

/** Sends a request and always returns {status, json, text} — never throws on 4xx/5xx. */
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
  return { status: res.status, json, text };
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
