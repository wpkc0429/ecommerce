// Unit tests of the sandbox guards: node --test scripts/sandbox/guard.test.mjs
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import {
  DEFAULT_PORTS,
  SandboxGuardError,
  assertCallerEnvNotProduction,
  assertNoDotEnvUpward,
  assertOutsideRepo,
  assertResetTargets,
  assertSandboxDatabaseUrl,
  assertSandboxEnv,
  buildSandboxEnv,
  captureManifestHeader,
  resolvePorts,
} from "./guard.mjs";

const main = { ports: new Set([5432, 6379, 9000, 9001, 8080, 3001]), dbs: new Set(["ecommerce", "ecommerce_e2e", "postgres"]) };
const secrets = { dbPassword: "p", redisPassword: "r", minioUser: "u", minioPassword: "m", adminJwt: "a", memberJwt: "b", webhookSecret: "w", paymentKey: "k" };
const ports = DEFAULT_PORTS;
const refused = (fn) => assert.throws(fn, SandboxGuardError);

test("database: only ecommerce_sandbox on the sandbox port", () => {
  const ok = `postgres://sandbox:p@127.0.0.1:${ports.pg}/ecommerce_sandbox`;
  assertSandboxDatabaseUrl(ok, ports, main);
  refused(() => assertSandboxDatabaseUrl(`postgres://sandbox:p@127.0.0.1:${ports.pg}/ecommerce`, ports, main));
  refused(() => assertSandboxDatabaseUrl(`postgres://sandbox:p@127.0.0.1:5432/ecommerce_sandbox`, ports, main));
  refused(() => assertSandboxDatabaseUrl(`postgres://sandbox:p@db.example.com:${ports.pg}/ecommerce_sandbox`, ports, main));
  refused(() => assertSandboxDatabaseUrl(`postgres://ecommerce:p@127.0.0.1:${ports.pg}/ecommerce_sandbox`, ports, main));
  refused(() => assertSandboxDatabaseUrl(`postgres://sandbox:p@127.0.0.1:${ports.pg}/ecommerce_sandbox2`, ports, main));
  refused(() => assertSandboxDatabaseUrl("not a url", ports, main));
});

test("production-like callers are refused", () => {
  for (const v of ["production", "prod", "staging", "live"]) refused(() => assertCallerEnvNotProduction({ APP_ENV: v }));
  assertCallerEnvNotProduction({ APP_ENV: "development" });
  assertCallerEnvNotProduction({});
});

test("ports may not collide with the main stack or each other", () => {
  assert.deepEqual(resolvePorts({}, main), ports);
  refused(() => resolvePorts({ SANDBOX_PG_PORT: "5432" }, main));
  refused(() => resolvePorts({ SANDBOX_API_PORT: "8080" }, main));
  refused(() => resolvePorts({ SANDBOX_API_PORT: String(ports.pg) }, main));
  refused(() => resolvePorts({ SANDBOX_API_PORT: "abc" }, main));
});

test("the environment is a whitelist with no real credentials", () => {
  const env = buildSandboxEnv({ secrets, ports, systemEnv: { SMTP_PASSWORD: "leak", ECPAY_HASH_KEY: "leak", R2_ENDPOINT: "https://x.r2.cloudflarestorage.com", PATH: "x" } });
  for (const k of ["SMTP_PASSWORD", "ECPAY_HASH_KEY", "PATH", "CF_ACCESS_AUD"]) assert.equal(k in env, false, k);
  assert.equal(env.APP_ENV, "development");
  assert.equal(env.PAYMENT_DEFAULT_PROVIDER, "mock");
  assert.match(env.R2_ENDPOINT, /^http:\/\/127\.0\.0\.1:/);
  refused(() => assertSandboxEnv({ ...env, APP_ENV: "production" }, ports, main));
  refused(() => assertSandboxEnv({ ...env, SMTP_USERNAME: "x" }, ports, main));
  refused(() => assertSandboxEnv({ ...env, R2_ENDPOINT: "https://a.r2.cloudflarestorage.com" }, ports, main));
  refused(() => assertSandboxEnv({ ...env, DATABASE_URL: "postgres://sandbox:p@127.0.0.1:5432/ecommerce" }, ports, main));
  refused(() => assertSandboxEnv({ ...env, REDIS_DB: "0" }, ports, main));
});

test("a .env above the run directory is refused", () => {
  const root = mkdtempSync(join(tmpdir(), "sbx-"));
  const run = join(root, "a", "run");
  mkdirSync(run, { recursive: true });
  assertNoDotEnvUpward(run);
  writeFileSync(join(root, ".env"), "X=1\n");
  refused(() => assertNoDotEnvUpward(run));
});

test("the state directory may not live in the repo", () => {
  const repo = join(tmpdir(), "r", "repo");
  refused(() => assertOutsideRepo(join(repo, ".sandbox"), repo));
  assertOutsideRepo(join(tmpdir(), "r", "other"), repo);
  assertOutsideRepo(join(tmpdir(), "r", "repo-sandbox"), repo);
});

test("reset only touches the sandbox compose project's own resources", () => {
  assertResetTargets({ project: "ecommerce-sandbox", containers: ["ecommerce-sandbox-postgres-1"], volumes: ["ecommerce-sandbox_sandbox_pgdata"] });
  refused(() => assertResetTargets({ project: "ecommerce", containers: [], volumes: [] }));
  refused(() => assertResetTargets({ project: "ecommerce-sandbox", containers: ["ecommerce-postgres-1"], volumes: [] }));
  refused(() => assertResetTargets({ project: "ecommerce-sandbox", containers: [], volumes: ["ecommerce_pgdata"] }));
});

test("the capture manifest declares sandbox-v1 / sandbox", () => {
  const m = JSON.parse(Buffer.from(captureManifestHeader(), "base64").toString());
  assert.equal(m.dataset, "sandbox-v1");
  assert.equal(m.api_origin_class, "sandbox");
});
