// Regenerates the design layer from the pristine defaults (run by a human after a deliberate change of the defaults):
//   node scripts/sync-design.mjs          copies design-defaults/{views,parts} -> design/, writes design/copy.js and routes.js
// design-defaults/routes.js is generated from lib/meta (route id -> views/<kebab-id>.js).
import { cpSync, existsSync, mkdirSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { ROUTE_IDS } from "../lib/design-schema.js";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const kebab = (id) => id.replace(/[A-Z]/g, (c) => "-" + c.toLowerCase());
const routes = Object.fromEntries(ROUTE_IDS.map((id) => [id, `views/${kebab(id)}.js`]));
const missing = Object.values(routes).filter((p) => !existsSync(path.join(root, "design-defaults", p)));
if (missing.length) console.warn(`sync-design: no default view yet for: ${missing.join(", ")}`);
const present = Object.fromEntries(Object.entries(routes).filter(([, p]) => !missing.includes(p)));
const routesSrc = `// Route id -> view module under this folder (generated from lib/meta by scripts/sync-design.mjs).\nexport default ${JSON.stringify(present, null, 2)};\n`;
writeFileSync(path.join(root, "design-defaults/routes.js"), routesSrc);
writeFileSync(path.join(root, "design/routes.js"), routesSrc.replace("generated from lib/meta by scripts/sync-design.mjs", "edit to point a route at another view file"));

const copy = (await import(pathToFileURL(path.join(root, "design-defaults/copy.js")).href)).default;
const body = Object.entries(copy).map(([k, v]) => `  ${JSON.stringify(k)}: ${JSON.stringify(v)},`).join("\n");
const copySrc = `// All copy of the platform admin (design layer). Edit values freely; keep keys and {placeholders}.\n// Irreversible-action warnings are fixed by the core and have no key here.\nexport default {\n${body}\n};\n`;
writeFileSync(path.join(root, "design/copy.js"), copySrc);

for (const dir of ["views", "parts"]) {
  const src = path.join(root, "design-defaults", dir), dst = path.join(root, "design", dir);
  if (!existsSync(src)) continue;
  rmSync(dst, { recursive: true, force: true });
  mkdirSync(dst, { recursive: true });
  cpSync(src, dst, { recursive: true });
}
console.log(`sync-design: ${Object.keys(present).length} routes, ${Object.keys(copy).length} copy keys`);
