#!/usr/bin/env node
// Alias: node scripts/check-scope.mjs --base <ref>  ==  only the scope check (only admin/design/** may differ from <ref>).
import { fileURLToPath } from "node:url";
import path from "node:path";
import { scopeViolations } from "./lib/design-scan.mjs";
import { changedFiles } from "./check-design.mjs";

const argv = process.argv.slice(2);
const val = (n) => { const i = argv.indexOf(n); return i >= 0 ? argv[i + 1] : null; };
const base = val("--base");
if (!base) { console.error("usage: node scripts/check-scope.mjs --base <git ref> [--root <admin dir>]"); process.exit(2); }
const root = path.resolve(val("--root") || path.join(path.dirname(fileURLToPath(import.meta.url)), ".."));
let changed;
try { changed = changedFiles(root, base); } catch (e) { console.error(`cannot read the diff against "${base}": ${(e.stderr || e.message).toString().trim()}`); process.exit(2); }
const bad = scopeViolations(changed);
if (bad.length) { for (const x of bad) console.error(`  x [${x.rule}] ${x.file}  ${x.msg}`); process.exit(1); }
console.log(`scope ok (${changed.length} changed file(s), all under design/)`);
