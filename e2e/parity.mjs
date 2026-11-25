// Visual parity check for the admin layer split (throwaway stack only; read-only against the API).
//   node parity.mjs capture <outDir>      screenshots of every main view at 390px and 1280px for a PLATFORM role (super admin)
//                                         and a MERCHANT OWNER role (demo-owner), plus the logged-out screens
//   node parity.mjs compare <dirA> <dirB> pixel-compares two captures (byte-equal files are identical)
// Run "capture" once against the OLD admin (pre-change commit, e.g. a git worktree) and once against the NEW admin,
// serving each on E2E_ADMIN_BASE_URL (default http://localhost:3001) while the database is unchanged between the runs.
// Modelled on merchant-template admin/e2e/parity.mjs (docs/DESIGN-LAYER-PROVENANCE.md).
import { mkdirSync, readFileSync, readdirSync } from "node:fs";
import { resolve } from "node:path";
import { chromium } from "playwright";
import { API, dotEnvValue } from "./lib.mjs";

const ADMIN = (process.env.E2E_ADMIN_BASE_URL ?? "http://localhost:3001").replace(/\/+$/, "");
const SUPER = {
  email: process.env.E2E_ADMIN_EMAIL || process.env.SEED_ADMIN_EMAIL || dotEnvValue("SEED_ADMIN_EMAIL") || "admin@example.com",
  password: process.env.E2E_ADMIN_PASSWORD || process.env.SEED_ADMIN_PASSWORD || dotEnvValue("SEED_ADMIN_PASSWORD") || "admin-change-me",
};
const OWNER = { email: "demo-owner@example.com", password: process.env.E2E_OWNER_PASSWORD || "demo-owner-change-me" };

async function apiJson(path, token) {
  const r = await fetch(`${API}/api/v1${path}`, { headers: { Authorization: `Bearer ${token}` } });
  return r.json();
}
async function apiLogin({ email, password }) {
  const r = await fetch(`${API}/api/v1/admin/auth/login`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ email, password }) });
  if (r.status !== 200) throw new Error(`login failed (${r.status}) for ${email}`);
  return (await r.json()).access_token;
}

const mode = process.argv[2];
if (mode === "capture") {
  const out = resolve(process.argv[3]);
  mkdirSync(out, { recursive: true });
  const tok = await apiLogin(OWNER);
  const orders = (await apiJson("/admin/shops/1/orders?page=1&page_size=5", tok)).orders || [];
  const members = (await apiJson("/admin/shops/1/members?page=1&page_size=5", tok)).members || [];
  const merchantViews = [
    ["pages", "pages"], ["content", "content"], ["orders", "orders"],
    ...(orders[0] ? [["order-detail", `orders/${orders[0].id}`]] : []),
    ["products", "products"], ["categories", "categories"], ["shipping-methods", "shipping-methods"],
    ["member-tiers", "member-tiers"], ["members", "members"],
    ...(members[0] ? [["member-detail", `members/${members[0].id}`], ["points-detail", `points/${members[0].id}`]] : []),
    ["points", "points"], ["reports", "reports"], ["storefront-clients", "storefront-clients"], ["team", "team"],
  ];
  const platformViews = [...merchantViews, ["platform-shops", "platform-shops"], ["platform-accounts", "platform-accounts"], ["platform-applications", "platform-applications"]];
  const browser = await chromium.launch();
  const settle = async (page) => {
    await page.waitForLoadState("networkidle").catch(() => {});
    await page.evaluate(() => { document.activeElement && document.activeElement.blur && document.activeElement.blur(); return document.fonts && document.fonts.ready; });
    await page.waitForTimeout(600);
  };
  const newPage = async (vw, vh) => {
    const ctx = await browser.newContext({ viewport: { width: vw, height: vh } });
    const page = await ctx.newPage();
    await page.addInitScript((api) => { try { localStorage.setItem("apiBase", api); } catch {} }, API);
    return { ctx, page };
  };
  for (const [vw, vh, tag] of [[390, 844, "m"], [1280, 860, "d"]]) {
    let { ctx, page } = await newPage(vw, vh);
    await page.goto(`${ADMIN}/`);
    await page.waitForSelector("#login-email");
    await settle(page);
    await page.screenshot({ path: `${out}/${tag}-login.png`, fullPage: true });
    await page.goto(`${ADMIN}/#/set-password/not-a-real-token`);
    await page.waitForSelector("#sp-invalid:not([hidden])").catch(() => {});
    await settle(page);
    await page.screenshot({ path: `${out}/${tag}-set-password-invalid.png`, fullPage: true });
    await ctx.close();
    for (const [who, creds, list] of [["platform", SUPER, platformViews], ["owner", OWNER, merchantViews]]) {
      ({ ctx, page } = await newPage(vw, vh));
      await page.goto(`${ADMIN}/`);
      await page.fill("#login-email", creds.email);
      await page.fill("#login-password", creds.password);
      await page.click("#login-form button[type=submit]");
      await page.waitForSelector("#view-main:not([hidden])", { timeout: 15000 });
      for (const [name, hash] of list) {
        console.log("  ", tag, who, name);
        await page.goto(`${ADMIN}/#/${hash}`);
        await page.waitForSelector("#view-main:not([hidden])");
        await settle(page);
        await page.screenshot({ path: `${out}/${tag}-${who}-${name}.png`, fullPage: true });
      }
      await ctx.close();
    }
  }
  await browser.close();
  console.log(`captured ${readdirSync(out).length} screenshots in ${out}`);
} else if (mode === "compare") {
  const [a, b] = [resolve(process.argv[3]), resolve(process.argv[4])];
  const names = [...new Set([...readdirSync(a), ...readdirSync(b)])].filter((n) => n.endsWith(".png")).sort();
  const browser = await chromium.launch();
  const page = await browser.newPage();
  let diffs = 0;
  for (const n of names) {
    let ba, bb;
    try { ba = readFileSync(`${a}/${n}`); bb = readFileSync(`${b}/${n}`); } catch { console.log(`  MISSING ${n}`); diffs++; continue; }
    if (ba.equals(bb)) { console.log(`  same      ${n}`); continue; }
    const r = await page.evaluate(async ([x, y]) => {
      const load = (b64) => new Promise((res) => { const i = new Image(); i.onload = () => res(i); i.src = "data:image/png;base64," + b64; });
      const [ia, ib] = await Promise.all([load(x), load(y)]);
      if (ia.width !== ib.width || ia.height !== ib.height) return { size: [ia.width, ia.height, ib.width, ib.height] };
      const get = (i) => { const c = document.createElement("canvas"); c.width = i.width; c.height = i.height; const g = c.getContext("2d"); g.drawImage(i, 0, 0); return g.getImageData(0, 0, i.width, i.height).data; };
      const da = get(ia), db = get(ib);
      let n = 0, minY = 1e9, maxY = -1;
      for (let p = 0; p < da.length; p += 4) {
        if (Math.abs(da[p] - db[p]) + Math.abs(da[p + 1] - db[p + 1]) + Math.abs(da[p + 2] - db[p + 2]) > 6) { n++; const y = Math.floor(p / 4 / ia.width); if (y < minY) minY = y; if (y > maxY) maxY = y; }
      }
      return { pixels: n, total: da.length / 4, minY, maxY };
    }, [ba.toString("base64"), bb.toString("base64")]);
    if (r.size) console.log(`  DIFF-SIZE ${n} ${JSON.stringify(r.size)}`);
    else if (r.pixels === 0) console.log(`  same(px)  ${n} (bytes differ, pixels identical)`);
    else console.log(`  DIFF      ${n} ${r.pixels} px (${((100 * r.pixels) / r.total).toFixed(3)}%) rows ${r.minY}-${r.maxY}`);
    if (r.size || r.pixels) diffs++;
  }
  await browser.close();
  console.log(diffs ? `\n${diffs} of ${names.length} screenshots differ` : `\nall ${names.length} screenshots are pixel-identical`);
  process.exit(diffs ? 1 : 0);
} else {
  console.error("usage: parity.mjs capture <dir> | compare <a> <b>");
  process.exit(2);
}
