#!/usr/bin/env node
// Writes lib/public-api.json from the REAL ctx surface (the whitelist of what design code may use).
// HUMAN-REVIEWED: any diff of that file is a change of the core/design contract. Run: node scripts/gen-public-api.mjs
import { writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { realSurface } from "./lib/ctx-surface.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const real = await realSurface(root);
const out = { _comment: "EXACTLY what the design layer may use from ctx. scripts/check-design.mjs builds the real ctx (core/context.js) and fails if this file and the real surface differ, and fails any design code that uses a name not listed here. Regenerate with scripts/gen-public-api.mjs and review the diff. ctx.route.params/state are plain data (free keys)." };
for (const k of Object.keys(real).sort()) out[k] = real[k];
writeFileSync(path.join(root, "lib/public-api.json"), JSON.stringify(out, null, 2) + "\n");
console.log(`wrote lib/public-api.json (${Object.keys(real).length} namespaces, ${Object.values(real).reduce((n, l) => n + l.length, 0)} names)`);
