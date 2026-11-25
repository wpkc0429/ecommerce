// Quick static scan of design-layer view files (same scanner the guard uses): node scripts/scan-views.mjs <file|dir>...
import { readFileSync, statSync, readdirSync } from "node:fs";
import path from "node:path";
import { scanCode, scanCss } from "./lib/design-scan.mjs";

const files = [];
const walk = (p0) => { const p = p0.split(String.fromCharCode(92)).join("/"); if (statSync(p).isDirectory()) readdirSync(p).forEach((n) => walk(p + "/" + n)); else files.push(p); };
process.argv.slice(2).forEach(walk);
let bad = 0;
for (const f of files) {
  if (f.endsWith(".js") && !/(copy|routes)\.js$/.test(f)) for (const v of scanCode(readFileSync(f, "utf8"), f)) { bad++; console.log(`${v.file}:${v.line} [${v.rule}] ${v.msg}`); }
  if (f.endsWith(".css")) for (const v of scanCss(readFileSync(f, "utf8"), f)) { bad++; console.log(`${v.file}:${v.line} [${v.rule}] ${v.msg}`); }
}
console.log(bad ? `${bad} violation(s)` : "design scan: clean");
process.exit(bad ? 1 : 0);
