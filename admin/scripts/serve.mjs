// Dev static server for the platform admin (no dependencies; not used in production, nginx serves the image). Default host 0.0.0.0.
//   node scripts/serve.mjs                 # http://127.0.0.1:3001 (PORT / HOST env override)
//   node scripts/serve.mjs --csp           # also sends the production CSP of nginx.conf (for checking that a design respects it)
//   CSP_CONNECT_EXTRA="http://127.0.0.1:9000" adds hosts to connect-src (e.g. local MinIO for image uploads)
// Only the files the admin needs are served; repo files (docs, scripts, e2e, git) are not.
// Adapted from merchant-template admin/scripts/serve.mjs (docs/DESIGN-LAYER-PROVENANCE.md).
import { createServer } from "node:http";
import { readFile, stat } from "node:fs/promises";
import { extname, join, normalize, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(fileURLToPath(new URL("..", import.meta.url)));
const port = Number(process.env.PORT || 3001);
const host = process.env.HOST || "0.0.0.0";
const withCsp = process.argv.includes("--csp");
const extra = process.env.CSP_CONNECT_EXTRA ? ` ${process.env.CSP_CONNECT_EXTRA}` : "";
const CSP = `default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self'; img-src 'self' https: data: blob:; connect-src 'self' https://*.r2.cloudflarestorage.com${extra}; worker-src 'self' blob:; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`;

const ALLOW = /^\/(index\.html|app\.js|styles\.css|(lib|ui|core|vendor)\/([A-Za-z0-9._-]+\/)*[A-Za-z0-9._-]+|(core\/groups|lib\/meta)\/[A-Za-z0-9._-]+|(design|design-defaults)\/([a-z0-9_-]+\/)*[A-Za-z0-9._-]+\.(js|css|svg|png|jpe?g|webp|gif|ico|avif|woff2|txt|md))$/;
const TYPES = { ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".json": "application/json; charset=utf-8", ".css": "text/css; charset=utf-8", ".md": "text/plain; charset=utf-8", ".txt": "text/plain; charset=utf-8", ".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif", ".ico": "image/x-icon", ".avif": "image/avif", ".woff2": "font/woff2" };

createServer(async (req, res) => {
  const url = new URL(req.url, "http://x");
  let path = normalize(decodeURIComponent(url.pathname)).replace(/\\/g, "/");
  if (path === "/") path = "/index.html";
  if ((!process.env.SERVE_ALL && !ALLOW.test(path)) || path.includes("..")) { res.writeHead(404); res.end("not found"); return; }
  try {
    const file = join(root, path);
    await stat(file);
    const headers = { "Content-Type": TYPES[extname(file)] || "application/octet-stream", "Cache-Control": "no-store" };
    if (withCsp) headers["Content-Security-Policy"] = CSP;
    res.writeHead(200, headers);
    res.end(await readFile(file));
  } catch {
    res.writeHead(404); res.end("not found");
  }
}).listen(port, host, () => console.log(`platform admin on http://${host}:${port}${withCsp ? " (CSP on)" : ""}`));
