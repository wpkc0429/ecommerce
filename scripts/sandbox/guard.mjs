// Safety guards of the sandbox stack (change design-history, Q5). Pure functions, no I/O except
// the reader of the main stack's names/ports, so they can be unit-tested (guard.test.mjs).
//
// The sandbox exists so admin screenshots never show real merchants' data. Every guard below
// closes one way a real data path could appear: the main database, a production-like
// environment, a shared port, a .env that config.Load() would walk up to, a real credential
// (SMTP, payment, R2), or a reset that could drop something that is not the sandbox's own.
import { existsSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";

export const SANDBOX_DB = "ecommerce_sandbox";
export const SANDBOX_DB_USER = "sandbox";
export const COMPOSE_PROJECT = "ecommerce-sandbox";
export const BUCKET_MEDIA = "sandbox-media";
export const BUCKET_DESIGN = "sandbox-design-history";
export const REDIS_DB_INDEX = 7;

export const DEFAULT_PORTS = Object.freeze({ pg: 55432, redis: 56379, minio: 59000, minioConsole: 59001, api: 18080, admin: 13001 });

// Ports the main local stack and the e2e stack use. The sandbox must never share one.
const BASE_MAIN_PORTS = [5432, 6379, 9000, 9001, 8080, 8180, 3001, 3000, 4000, 8090, 8081, 4010];
// Database names that belong to someone else.
const BASE_MAIN_DBS = ["ecommerce", "ecommerce_e2e", "ecommerce_test", "postgres"];

const LOOPBACK = new Set(["127.0.0.1", "localhost", "::1", "[::1]"]);

export class SandboxGuardError extends Error {
  constructor(message) {
    super(`sandbox guard: ${message}`);
    this.name = "SandboxGuardError";
  }
}
const refuse = (m) => {
  throw new SandboxGuardError(m);
};

/** Names and ports (never values of secrets) of the main stack, read from the repo .env. */
export function readMainStackFacts(repoRoot) {
  const facts = { ports: new Set(BASE_MAIN_PORTS), dbs: new Set(BASE_MAIN_DBS) };
  const file = join(repoRoot, ".env");
  if (!existsSync(file)) return facts;
  for (const raw of readFileSync(file, "utf8").split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq < 0) continue;
    const key = line.slice(0, eq).trim();
    const val = line.slice(eq + 1).trim().replace(/^["']|["']$/g, "");
    if (["POSTGRES_PORT", "REDIS_PORT", "MINIO_PORT", "MINIO_CONSOLE_PORT", "ADMIN_PORT"].includes(key) && /^\d+$/.test(val)) facts.ports.add(Number(val));
    if (key === "POSTGRES_DB" && val) facts.dbs.add(val);
    if (["DATABASE_URL", "TEST_DATABASE_URL", "DEV_DATABASE_URL"].includes(key)) {
      try {
        const u = new URL(val);
        const name = decodeURIComponent(u.pathname.replace(/^\//, ""));
        if (name) facts.dbs.add(name);
        if (u.port) facts.ports.add(Number(u.port));
      } catch {
        /* an unparsable line cannot name a database for us */
      }
    }
  }
  return facts;
}

/** A shell that already says production (or staging/live) is refused. */
export function assertCallerEnvNotProduction(callerEnv) {
  for (const key of ["APP_ENV", "SANDBOX_APP_ENV", "NODE_ENV"]) {
    const v = String(callerEnv[key] ?? "").trim().toLowerCase();
    if (["production", "prod", "staging", "live"].includes(v)) refuse(`${key}=${v} looks like a real deployment; the sandbox only runs where nothing real is reachable`);
  }
}

export function resolvePorts(callerEnv, main) {
  const ports = { ...DEFAULT_PORTS };
  const names = { pg: "SANDBOX_PG_PORT", redis: "SANDBOX_REDIS_PORT", minio: "SANDBOX_MINIO_PORT", minioConsole: "SANDBOX_MINIO_CONSOLE_PORT", api: "SANDBOX_API_PORT", admin: "SANDBOX_ADMIN_PORT" };
  for (const [k, envName] of Object.entries(names)) {
    const raw = callerEnv[envName];
    if (raw === undefined || raw === "") continue;
    if (!/^\d{4,5}$/.test(raw) || Number(raw) > 65535) refuse(`${envName} must be a port number`);
    ports[k] = Number(raw);
  }
  const seen = new Set();
  for (const [k, p] of Object.entries(ports)) {
    if (main.ports.has(p)) refuse(`port ${p} (${k}) is used by the main/e2e stack`);
    if (seen.has(p)) refuse(`port ${p} is assigned twice`);
    seen.add(p);
  }
  return ports;
}

export function sandboxDatabaseUrl(secrets, ports) {
  return `postgres://${SANDBOX_DB_USER}:${secrets.dbPassword}@127.0.0.1:${ports.pg}/${SANDBOX_DB}?sslmode=disable`;
}

/** The only database URL the sandbox binaries may be given. */
export function assertSandboxDatabaseUrl(rawUrl, ports, main) {
  let u;
  try {
    u = new URL(rawUrl);
  } catch {
    return refuse("DATABASE_URL does not parse");
  }
  const name = decodeURIComponent(u.pathname.replace(/^\//, ""));
  if (u.protocol !== "postgres:" && u.protocol !== "postgresql:") refuse("DATABASE_URL is not postgres");
  if (!LOOPBACK.has(u.hostname)) refuse(`database host ${u.hostname} is not loopback`);
  if (name !== SANDBOX_DB) refuse(`database name must be exactly ${SANDBOX_DB} (got ${name || "empty"})`);
  if (main.dbs.has(name)) refuse(`database ${name} is a main-stack database`);
  if (Number(u.port) !== ports.pg) refuse(`database port ${u.port} is not the sandbox Postgres port ${ports.pg}`);
  if (u.username !== SANDBOX_DB_USER) refuse(`database user must be ${SANDBOX_DB_USER}`);
}

/**
 * config.Load() reads the first .env it finds in ".", "..", ... "../../../.." for every variable
 * the process does not set, and an EMPTY variable does not count as set. A sandbox process run
 * inside the repo would silently inherit the main stack's SMTP, payment or R2 credentials.
 */
export function assertNoDotEnvUpward(cwd) {
  let dir = resolve(cwd);
  for (let i = 0; i < 5; i++) {
    if (existsSync(join(dir, ".env"))) refuse(`a .env exists at ${dir}; config.Load() would read it. Choose another SANDBOX_HOME (outside any project)`);
    const up = resolve(dir, "..");
    if (up === dir) break;
    dir = up;
  }
}

/** The run directory must not sit under the repo. */
export function assertOutsideRepo(dir, repoRoot) {
  const norm = (p) => resolve(p).replace(/\//g, "\\").toLowerCase().replace(/\\+$/, "") + "\\";
  if (norm(dir).startsWith(norm(repoRoot))) refuse(`state directory ${dir} is inside the repository`);
}

/**
 * The complete environment given to every sandbox process (server, seed, migrate). It is a
 * WHITELIST: nothing is inherited from the invoking shell, so exported SMTP_*, ECPAY_*, R2_*,
 * CF_*, DATABASE_URL etc. cannot leak in.
 */
/**
 * The exact origins of the sandbox stack, the value the platform's
 * DESIGN_SANDBOX_ORIGINS must hold for admin screenshots to be accepted from it
 * (an admin capture declares the admin SPA's origin; the API's is listed too).
 */
export function sandboxOrigins(ports) {
  return [
    `http://127.0.0.1:${ports.admin}`,
    `http://localhost:${ports.admin}`,
    `http://127.0.0.1:${ports.api}`,
    `http://localhost:${ports.api}`,
  ];
}

/**
 * The origin the sandbox API treats as a read-only PREVIEW origin (ADMIN_PREVIEW_ORIGINS). Nothing
 * listens there: the e2e only sends it as the Origin header of a login, the way a browser on a
 * preview host would. Distinct from every admin/CORS origin (the config refuses an overlap).
 */
export function sandboxPreviewOrigin(ports) {
  return `http://127.0.0.1:${ports.admin + 1}`;
}

export function buildSandboxEnv({ secrets, ports, systemEnv = {} }) {
  const adminOrigins = `http://127.0.0.1:${ports.admin},http://localhost:${ports.admin}`;
  const env = {
    // Windows needs these to resolve addresses and temp paths; nothing else is carried over.
    ...(systemEnv.SystemRoot ? { SystemRoot: systemEnv.SystemRoot } : {}),
    ...(systemEnv.TEMP ? { TEMP: systemEnv.TEMP, TMP: systemEnv.TEMP } : {}),
    APP_ENV: "development", // the only non-production mode; PAYMENT_DEFAULT_PROVIDER=mock needs it
    LOG_LEVEL: "info",
    API_ADDR: `127.0.0.1:${ports.api}`,
    DATABASE_URL: sandboxDatabaseUrl(secrets, ports),
    REDIS_ADDR: `127.0.0.1:${ports.redis}`,
    REDIS_PASSWORD: secrets.redisPassword,
    REDIS_DB: String(REDIS_DB_INDEX),
    ADMIN_JWT_SECRET: secrets.adminJwt,
    MEMBER_JWT_SECRET: secrets.memberJwt,
    JWT_ISSUER: "ecommerce-sandbox",
    PAYMENT_DEFAULT_PROVIDER: "mock",
    PAYMENT_MOCK_WEBHOOK_SECRET: secrets.webhookSecret,
    PAYMENT_CREDENTIALS_ENCRYPTION_KEY: secrets.paymentKey,
    SEED_ADMIN_EMAIL: SANDBOX_PERSONAS.platform.email,
    SEED_ADMIN_PASSWORD: SANDBOX_PERSONAS.platform.password,
    ADMIN_PUBLIC_URL: `http://127.0.0.1:${ports.admin}`,
    CORS_ALLOWED_ORIGINS: adminOrigins,
    // Storage: the sandbox's own MinIO. Never an R2 endpoint.
    R2_ACCOUNT_ID: "sandbox",
    R2_ACCESS_KEY_ID: secrets.minioUser,
    R2_SECRET_ACCESS_KEY: secrets.minioPassword,
    R2_BUCKET: BUCKET_MEDIA,
    R2_ENDPOINT: `http://127.0.0.1:${ports.minio}`,
    IMAGE_PUBLIC_BASE_URL: `http://127.0.0.1:${ports.minio}/${BUCKET_MEDIA}`,
    DESIGN_R2_BUCKET: BUCKET_DESIGN,
    DESIGN_R2_ACCESS_KEY_ID: secrets.minioUser,
    DESIGN_R2_SECRET_ACCESS_KEY: secrets.minioPassword,
    DESIGN_SANDBOX_ORIGINS: sandboxOrigins(ports).join(","),
    ADMIN_PREVIEW_ORIGINS: sandboxPreviewOrigin(ports),
    // SMTP_* is deliberately absent: no credentials = the API sends no mail at all (the e-mail sink).
    // ECPAY_* is deliberately absent: the mock provider is the default and ECPay stays unconfigured.
  };
  assertSandboxEnv(env, ports, { dbs: new Set(BASE_MAIN_DBS), ports: new Set() });
  return env;
}

/** Last check on the exact environment about to be handed to a process. */
export function assertSandboxEnv(env, ports, main) {
  if (env.APP_ENV !== "development") refuse(`APP_ENV must be development in the sandbox (got ${env.APP_ENV})`);
  assertSandboxDatabaseUrl(env.DATABASE_URL, ports, main);
  if (env.PAYMENT_DEFAULT_PROVIDER !== "mock") refuse("PAYMENT_DEFAULT_PROVIDER must be mock");
  for (const key of Object.keys(env)) {
    if (/^(SMTP_|ECPAY_|CF_|CLOUDFLARE|STRIPE|LINE_|SENTRY)/.test(key)) refuse(`${key} must not be set: no real third-party credentials in the sandbox`);
  }
  for (const key of ["R2_ENDPOINT", "IMAGE_PUBLIC_BASE_URL"]) {
    let host;
    try {
      host = new URL(env[key]).hostname;
    } catch {
      return refuse(`${key} does not parse`);
    }
    if (!LOOPBACK.has(host)) refuse(`${key} points at ${host}, not at the sandbox's own MinIO`);
  }
  if (!/^127\.0\.0\.1:\d+$/.test(env.REDIS_ADDR) || !/^127\.0\.0\.1:\d+$/.test(env.API_ADDR)) refuse("REDIS_ADDR and API_ADDR must be loopback");
  if (Number(env.REDIS_DB) !== REDIS_DB_INDEX) refuse(`REDIS_DB must be ${REDIS_DB_INDEX}`);
}

/**
 * `reset` destroys volumes. It may do so only for resources that belong to the sandbox compose
 * project; one stray name (the main stack's volumes, any other project) aborts.
 * `containers`/`volumes` are the names docker lists for the sandbox project label.
 */
export function assertResetTargets({ project, containers, volumes }) {
  if (project !== COMPOSE_PROJECT) refuse(`reset runs only for the ${COMPOSE_PROJECT} compose project (got ${project})`);
  for (const c of containers) if (!c.startsWith(`${COMPOSE_PROJECT}-`)) refuse(`container ${c} is not part of ${COMPOSE_PROJECT}`);
  for (const v of volumes) if (!v.startsWith(`${COMPOSE_PROJECT}_`)) refuse(`volume ${v} is not part of ${COMPOSE_PROJECT}`);
}

export const SANDBOX_PERSONAS = Object.freeze({
  // Synthetic and public: they exist only inside a database that is destroyed with the stack.
  platform: { email: "sandbox-platform@example.com", password: "sandbox-platform-pw-0001" },
  owner: { email: "demo-owner@example.com", password: "demo-owner-change-me" },
  staff: { email: "demo-staff@example.com", password: "demo-staff-change-me" },
  customer: { email: "demo-customer@example.com", password: "demo-customer-change-me" },
  publishableKey: "pk_demoseedstorefrontkeynotsecret",
});

export function captureManifestHeader(now = new Date(), origin = undefined) {
  return Buffer.from(
    JSON.stringify({ tool: "design-capture", tool_version: "1", dataset: "sandbox-v1", api_origin_class: "sandbox", ...(origin ? { origin } : {}), captured_at: now.toISOString(), set_version: 1 }),
  ).toString("base64");
}
