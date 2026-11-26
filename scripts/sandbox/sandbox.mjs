#!/usr/bin/env node
// Sandbox stack driver (change design-history, Q5). See docs/deploy/sandbox-stack.md.
//
//   node scripts/sandbox/sandbox.mjs up | down | reset | status | env | manifest | smoke | selftest
//
// up      build the API, start the sandbox Postgres/Redis/MinIO (own compose project), migrate,
//         seed -demo, start the API and the static admin. Idempotent: re-running reuses a live stack.
// down    stop the processes and destroy the sandbox containers, volumes and generated credentials.
// reset   down + up (a fresh, identical dataset).
// env     print the origins and the synthetic logins as JSON (no infrastructure secrets).
// manifest print the X-Capture-Manifest header value the capture tool sends (dataset sandbox-v1).
// smoke   upload smoke test of an admin screenshot: with the sandbox marker accepted, without refused.
import { spawn, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { cpSync, existsSync, mkdirSync, openSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  BUCKET_DESIGN,
  BUCKET_MEDIA,
  COMPOSE_PROJECT,
  REDIS_DB_INDEX,
  SANDBOX_DB,
  SANDBOX_PERSONAS,
  SandboxGuardError,
  assertCallerEnvNotProduction,
  assertNoDotEnvUpward,
  assertOutsideRepo,
  assertResetTargets,
  buildSandboxEnv,
  captureManifestHeader,
  readMainStackFacts,
  resolvePorts,
} from "./guard.mjs";

const repo = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const composeFile = join(repo, "docker", "sandbox", "docker-compose.yml");
const home = resolve(process.env.SANDBOX_HOME || join(process.env.TEMP || tmpdir() || homedir(), "ecommerce-sandbox"));
const runDir = join(home, "run"); // cwd of every sandbox process; no .env above it (guarded)
const binDir = join(home, "bin");
const logDir = join(home, "logs");
const secretsFile = join(home, "secrets.json");
const pidFile = join(home, "pids.json");
const exe = process.platform === "win32" ? ".exe" : "";

const say = (m) => console.log(`[sandbox] ${m}`);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function guardedContext() {
  assertCallerEnvNotProduction(process.env);
  assertOutsideRepo(home, repo);
  mkdirSync(runDir, { recursive: true });
  assertNoDotEnvUpward(runDir);
  const main = readMainStackFacts(repo);
  const ports = resolvePorts(process.env, main);
  return { main, ports };
}

function loadOrCreateSecrets() {
  if (existsSync(secretsFile)) return JSON.parse(readFileSync(secretsFile, "utf8"));
  const hex = (n) => randomBytes(n).toString("hex");
  const secrets = {
    dbPassword: hex(16),
    redisPassword: hex(16),
    minioUser: `sb${hex(6)}`,
    minioPassword: hex(16),
    adminJwt: hex(32),
    memberJwt: hex(32),
    webhookSecret: hex(24),
    paymentKey: hex(32),
  };
  writeFileSync(secretsFile, JSON.stringify(secrets), { mode: 0o600 });
  return secrets;
}

function composeEnv(secrets, ports) {
  return {
    ...process.env, // docker needs PATH/DOCKER_HOST; the compose file only reads the SANDBOX_* below
    SANDBOX_DB_PASSWORD: secrets.dbPassword,
    SANDBOX_REDIS_PASSWORD: secrets.redisPassword,
    SANDBOX_MINIO_USER: secrets.minioUser,
    SANDBOX_MINIO_PASSWORD: secrets.minioPassword,
    SANDBOX_PG_PORT: String(ports.pg),
    SANDBOX_REDIS_PORT: String(ports.redis),
    SANDBOX_MINIO_PORT: String(ports.minio),
    SANDBOX_MINIO_CONSOLE_PORT: String(ports.minioConsole),
    SANDBOX_ADMIN_ORIGINS: `http://127.0.0.1:${ports.admin},http://localhost:${ports.admin}`,
  };
}

function run(cmd, args, opts = {}) {
  const r = spawnSync(cmd, args, { encoding: "utf8", ...opts });
  if (r.status !== 0 && !opts.allowFail) {
    // Never echo the environment; only the command name and the tail of its own output.
    throw new Error(`${cmd} ${args.slice(0, 3).join(" ")} failed (${r.status}): ${(r.stderr || r.stdout || "").trim().slice(-600)}`);
  }
  return r;
}
const compose = (secrets, ports, args, opts = {}) =>
  run("docker", ["compose", "-p", COMPOSE_PROJECT, "-f", composeFile, ...args], { env: composeEnv(secrets, ports), ...opts });

function readPids() {
  try {
    return JSON.parse(readFileSync(pidFile, "utf8"));
  } catch {
    return {};
  }
}
function alive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}
function stopProcesses() {
  const pids = readPids();
  for (const [name, pid] of Object.entries(pids)) {
    if (pid && alive(pid)) {
      try {
        process.kill(pid);
        say(`stopped ${name} (pid ${pid})`);
      } catch {
        /* already gone */
      }
    }
  }
  rmSync(pidFile, { force: true });
}

function sandboxCwdEnv(env) {
  return { cwd: runDir, env };
}

async function waitFor(what, fn, tries = 60) {
  for (let i = 0; i < tries; i++) {
    try {
      if (await fn()) return;
    } catch {
      /* not yet */
    }
    await sleep(1000);
  }
  throw new Error(`timed out waiting for ${what}`);
}

async function up() {
  const { ports } = guardedContext();
  mkdirSync(binDir, { recursive: true });
  mkdirSync(logDir, { recursive: true });
  const secrets = loadOrCreateSecrets();
  const env = buildSandboxEnv({ secrets, ports, systemEnv: process.env });

  say("building the API binaries (server, migrate, seed)");
  for (const name of ["server", "migrate", "seed"]) run("go", ["build", "-o", join(binDir, name + exe), `./cmd/${name}`], { cwd: join(repo, "api") });

  say(`starting Postgres/Redis/MinIO (compose project ${COMPOSE_PROJECT})`);
  compose(secrets, ports, ["up", "-d", "--wait"]);

  const bins = (n) => join(binDir, n + exe);
  say("migrating the sandbox database");
  // The migrate command takes "file://<path>", which Windows drive paths break: run it from the run
  // directory against a copy of the migrations (relative path, same files).
  rmSync(join(runDir, "migrations"), { recursive: true, force: true });
  cpSync(join(repo, "api", "migrations"), join(runDir, "migrations"), { recursive: true });
  run(bins("migrate"), ["-direction", "up"], sandboxCwdEnv(env));
  say("seeding the synthetic demo dataset (seed -demo)");
  run(bins("seed"), ["-demo"], sandboxCwdEnv(env));

  const pids = readPids();
  if (!pids.api || !alive(pids.api)) {
    const out = openSync(join(logDir, "api.log"), "a");
    const child = spawn(bins("server"), [], { cwd: runDir, env, detached: true, stdio: ["ignore", out, out], windowsHide: true });
    child.unref();
    pids.api = child.pid;
  }
  if (!pids.admin || !alive(pids.admin)) {
    const out = openSync(join(logDir, "admin.log"), "a");
    const child = spawn(process.execPath, [join(repo, "admin", "scripts", "serve.mjs")], {
      cwd: join(repo, "admin"),
      env: { PATH: process.env.PATH ?? "", SystemRoot: process.env.SystemRoot ?? "", PORT: String(ports.admin), HOST: "127.0.0.1", CSP_CONNECT_EXTRA: `http://127.0.0.1:${ports.api} http://127.0.0.1:${ports.minio}` },
      detached: true,
      stdio: ["ignore", out, out],
      windowsHide: true,
    });
    child.unref();
    pids.admin = child.pid;
  }
  writeFileSync(pidFile, JSON.stringify(pids));
  await waitFor("the sandbox API", async () => (await fetch(`http://127.0.0.1:${ports.api}/healthz`)).ok);
  await waitFor("the sandbox admin", async () => (await fetch(`http://127.0.0.1:${ports.admin}/`)).ok);
  say("up. origins:");
  console.log(JSON.stringify(describe(ports), null, 2));
}

function describe(ports) {
  return {
    sandbox: true,
    dataset: "sandbox-v1",
    api_origin_class: "sandbox",
    api_origin: `http://127.0.0.1:${ports.api}`,
    admin_origin: `http://127.0.0.1:${ports.admin}`,
    allowed_api_origins: [`http://127.0.0.1:${ports.api}`, `http://localhost:${ports.api}`],
    media_origin: `http://127.0.0.1:${ports.minio}`,
    database: SANDBOX_DB,
    redis_db: REDIS_DB_INDEX,
    buckets: [BUCKET_MEDIA, BUCKET_DESIGN],
    publishable_key: SANDBOX_PERSONAS.publishableKey,
    logins: { platform: SANDBOX_PERSONAS.platform, owner: SANDBOX_PERSONAS.owner, staff: SANDBOX_PERSONAS.staff, customer: SANDBOX_PERSONAS.customer },
    ports,
  };
}

function down() {
  const { ports } = guardedContext();
  stopProcesses();
  const secrets = existsSync(secretsFile) ? JSON.parse(readFileSync(secretsFile, "utf8")) : { dbPassword: "x", redisPassword: "x", minioUser: "x", minioPassword: "x" };
  // The reset guard: list what docker says belongs to the project and refuse if anything is foreign.
  const names = (kind, fmt) =>
    run("docker", [kind, "ls", ...(kind === "container" ? ["-a"] : []), "--filter", `label=com.docker.compose.project=${COMPOSE_PROJECT}`, "--format", fmt], { allowFail: true })
      .stdout.split(/\r?\n/)
      .filter(Boolean);
  assertResetTargets({ project: COMPOSE_PROJECT, containers: names("container", "{{.Names}}"), volumes: names("volume", "{{.Name}}") });
  compose(secrets, ports, ["down", "-v", "--remove-orphans"]);
  rmSync(secretsFile, { force: true });
  rmSync(binDir, { recursive: true, force: true });
  say("down: containers, volumes, credentials and binaries removed");
}

async function smoke() {
  const { ports } = guardedContext();
  const base = `http://127.0.0.1:${ports.api}`;
  const secrets = loadOrCreateSecrets();
  // 1. The API really is on the sandbox database (asked of the server inside the sandbox container).
  const db = compose(secrets, ports, ["exec", "-T", "postgres", "psql", "-U", "sandbox", "-d", SANDBOX_DB, "-Atc", "select current_database()"]).stdout.trim();
  if (db !== SANDBOX_DB) throw new Error(`unexpected database ${db}`);
  say(`database: ${db}`);
  // 2. The design-history path of the e2e (fake service): sandbox marker accepted, live refused,
  //    plus the no-marker refusal, against the sandbox API with sandbox-only credentials.
  const r = spawnSync(process.execPath, [join(repo, "e2e", "design-history-path.mjs")], {
    cwd: join(repo, "e2e"),
    encoding: "utf8",
    env: {
      PATH: process.env.PATH ?? "",
      SystemRoot: process.env.SystemRoot ?? "",
      E2E_API_BASE_URL: base,
      E2E_ADMIN_EMAIL: SANDBOX_PERSONAS.platform.email,
      E2E_ADMIN_PASSWORD: SANDBOX_PERSONAS.platform.password,
      E2E_REQUIRE_DESIGN_STORAGE: "1",
    },
  });
  const lines = (r.stdout + r.stderr).split(/\r?\n/).filter((l) => /sandbox|manifest|live data|✗|PASSED|FAILED|screenshot is stored|stored/i.test(l));
  console.log(lines.join("\n"));
  if (r.status !== 0) throw new Error("sandbox smoke FAILED");
  say("smoke PASSED (sandbox-v1 admin upload accepted; live / missing marker refused)");
}

async function selftest() {
  const r = spawnSync(process.execPath, ["--test", join(repo, "scripts", "sandbox", "guard.test.mjs")], { stdio: "inherit" });
  if (r.status !== 0) throw new Error("guard tests failed");
}

const cmd = process.argv[2];
try {
  if (cmd === "up") await up();
  else if (cmd === "down") down();
  else if (cmd === "reset") {
    down();
    await up();
  } else if (cmd === "status") {
    const { ports } = guardedContext();
    const pids = readPids();
    console.log(JSON.stringify({ api: Boolean(pids.api && alive(pids.api)), admin: Boolean(pids.admin && alive(pids.admin)), ports }, null, 2));
  } else if (cmd === "env") console.log(JSON.stringify(describe(guardedContext().ports), null, 2));
  else if (cmd === "manifest") console.log(captureManifestHeader());
  else if (cmd === "smoke") await smoke();
  else if (cmd === "selftest") await selftest();
  else {
    console.error("usage: sandbox.mjs up | down | reset | status | env | manifest | smoke | selftest");
    process.exit(2);
  }
} catch (err) {
  if (err instanceof SandboxGuardError) console.error(`REFUSED: ${err.message}`);
  else console.error(`[sandbox] ${err.message}`);
  process.exit(1);
}
