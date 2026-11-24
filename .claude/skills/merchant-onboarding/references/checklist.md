# Checklists: intake, sites, verification, go-live, rollback

Tick items in a run log (a scratch file outside the repos, no secrets). Mark each item A (agent), H (human operator), O (owner).

## Intake (step 0)

- [ ] Shop display name; owner email (kept out of files); the storefront is always headless, created from the merchant template (ADR 0001: there is no platform-hosted option; planned intake from the platform website application form is not built yet).
- [ ] Hostnames decided: storefront (`shop.example.com`), the merchant's admin (`admin.shop.example.com`), whether images share the platform image domain.
- [ ] ECPay: merchant has an account; stage credentials first; who enters them (the owner).
- [ ] Target API: local throwaway, staging or production; who holds the platform token and through which route (platform admin domain behind Access).
- [ ] Catalogue source (list of products, SKUs, prices as integers, stock, images in JPEG/PNG/WebP), shipping methods and fees, tiers (name, min points, discount percent).
- [ ] Branch plan: storefront and admin work happens on a branch; nothing is pushed to `main` without the operator.

## Storefront site from the template (step 11)

A human (or the agent with the operator's go-ahead) creates the merchant repo from the template checkout: `node scripts/new-merchant.mjs <dir> --name "<Shop name>" --slug <slug> [--api-base <url>] [--publishable-key <pk_...>] [--site-url <storefront origin>] [--admin-origin <admin origin>]`. It sets the brand in the storefront and admin metadata and copy, writes `config.js` when the values are given, re-locks the hash manifests and makes the initial commit (no remote, no push). Then, in the new repo:

- [ ] Replace brand content only in the design layer: `public/content.js` (copy), `public/assets/**`, `public/styles.css`, `public/index.html` (title, og tags with the merchant's absolute URLs).
- [ ] Connect to the platform: `public/config.js` holds `apiBase` and `publishableKey`. It is LOCKED against AI edits and hash-protected, but merchant-owned: `new-merchant.mjs --api-base --publishable-key` writes it, or a HUMAN edits it and then runs `node scripts/check-layers.mjs --update`. In the template `isTemplate` is true and the guard demands placeholders; in a merchant repo `template.json` has `isTemplate: false`, only the manifest hash matters, and leftover `REPLACE_WITH_*` values are a warning. `sync-core.mjs` never overwrites it. For local work use the gitignored `public/config.local.js` (copy `public/config.local.js.example`; loaded only on localhost) with `Object.assign(window.SHOP_CONFIG, { apiBase, publishableKey })`.
- [ ] Preview locally: `python3 -m http.server 8080 --directory public` (the `--directory public` is required). Backend: the platform's `make openapi-mock` (port 4010, no DB) or a throwaway API.
- [ ] Import the catalogue: copy `catalog/catalog.sample.json` to `catalog/catalog.json`, replace the sample data, run `node scripts/import-catalog.mjs catalog/catalog.json --dry-run`, then without `--dry-run` (env `IMPORT_API_BASE`, `IMPORT_ADMIN_EMAIL`, `IMPORT_ADMIN_PASSWORD`, `IMPORT_SHOP_ID`, `IMPORT_ORIGIN`; it never deletes; the sample data is placeholder data and must not reach a real shop).
- [ ] Guards: `node scripts/check-layers.mjs --check` and `node scripts/check-scope.mjs storefront --base <ref>`.
- [ ] H: Cloudflare Workers Git integration, custom domain; `wrangler.jsonc` `assets.directory` stays `./public`.

## Admin site (step 12)

Agent: the admin ships inside the same repo (`admin/`), static, no build.

- [ ] Design only in `admin/design/**` (see the repo's `admin-design` skill). Do not edit the locked core.
- [ ] Local run: `cd admin && node scripts/serve.mjs --proxy http://127.0.0.1:8080` and gitignored `admin/config.local.js` (copy `admin/config.local.js.example`: `window.ADMIN_CONFIG.apiBase = location.origin;`).
- [ ] Guards from `admin/`: `node scripts/check-design.mjs --base <ref>`, `node scripts/check-layers.mjs`, `node scripts/test-guard.mjs`; from the repo root `node scripts/check-scope.mjs admin --base <ref>`.
- [ ] H: static hosting on the local port the tunnel points at (`admin/deploy/`: nginx config with the strict CSP, Dockerfile, ingress example); tunnel, Access, DNS (`cloudflare-steps.md`); admin origin registered `purpose:"admin"` (cookbook 5) and added to R2 CORS.
- [ ] The admin must handle `#/set-password/{token}` and `#/accept-invite/{token}` at its root (the template does).

## Verification (step 15)

Platform side (agent, against local/throwaway stack unless the operator authorises production reads):

- [ ] `make openapi` (contract checks; no infra) and `make test` pass if the platform code was touched (it should not be).
- [ ] `GET .../setup-status`: `owner_assigned`, `owner_password_set`, `shipping_method_active`, `payment_credentials_set`, `publishable_key_active` all true (`theme_applied`, `domain_bound`, `home_published` are deprecated hosted-storefront flags and stay false for a headless shop: ignore them).
- [ ] Storefront read-back with the key: `curl -s -H "X-Publishable-Key: $PK" "$API/api/v1/shop/products"`, `/categories`, `/shipping-methods` return the expected catalogue; images present.
- [ ] CORS: from the browser at the storefront origin the three calls above succeed. If `curl` works but the browser fails, the origin is not registered (or has the wrong purpose).
- [ ] Guest checkout end to end on stage: place an order, follow the ECPay stage redirect, confirm the payment webhook turns the order paid (`ECPAY_RETURN_URL` reachable), check the order in the admin. (`cd e2e && node commerce-path.mjs` is the platform regression chain: it needs a throwaway stack with `make seed-demo` data and tests the demo shop, not the new merchant; run it only if platform code changed, never against production or the main database.)
- [ ] Customer email: with SMTP configured the order and payment emails arrive; without it nothing is sent (expected).

Admin side (runbook section 6, all five are required):

- [ ] `curl -i https://api.example.com/api/v1/admin/me` returns 404.
- [ ] Incognito open of `https://admin.shop.example.com/` goes to the Access login; `curl -i https://admin.shop.example.com/api/v1/admin/me` without cookie is stopped by Access (302/403).
- [ ] After the one-time PIN the admin login works for the owner; `__Secure-admin_refresh` is HttpOnly and localStorage holds no token.
- [ ] From the merchant admin domain, a `fetch` to `/api/v1/admin/shops/{another shop id}/me` returns 403.
- [ ] From the same session, `/api/v1/admin/accounts` returns 403 (and 200 for the platform owner on the platform admin domain).
- [ ] `GET https://admin.shop.example.com/api/v1/admin/branding` returns 200 with the shop name; on an unregistered domain 404.
- [ ] Owner flows: password link works once and a reused link is 404; owner can open orders, products, upload an image (R2 CORS), invite a staff member (link shown once, `delivery:"manual"`).
- [ ] Design guards green on the exact commit being published.

## Go-live (step 16, human)

- [ ] Operator approves, in writing in the conversation, each of: switching `ECPAY_CHECKOUT_URL` to the production URL (platform-wide: affects every shop), the owner entering production MerchantID/HashKey/HashIV, the production push of the storefront.
- [ ] Shop `status` set to 1 (`PUT .../shops/{id}` `{"status":1}`) if it was held at 2.
- [ ] Stage publishable key revoked if a separate stage key was used; production key present in the storefront.
- [ ] One low-value real payment, then refund/cancel by the merchant per their process.
- [ ] Hand-over note to the owner: where to log in, how staff are invited (links are manual), who to call. No secrets in it.

## Rollback

Reversible, in order of least damage:

1. Storefront: revert the merchant repo commit (an approved push is needed to redeploy), or remove the custom domain in Workers.
2. Block access: remove the admin hostname's tunnel rules (or the whole hostname) to return to "back office only on localhost"; the public API still returns 404 for `/api/v1/admin`.
3. Platform: revoke the publishable key (`DELETE .../publishable-keys/{keyID}`, immediate); set the shop `status` to 0 (storefront 503) or 2 (404); `POST .../accounts/{userID}/disable` for the owner; `DELETE .../allowed-origins/{originID}` to remove CORS.
4. Auth layer: clear `CF_ACCESS_TEAM_DOMAIN` and `CF_ACCESS_AUD` and restart the API (this removes the Access check; only with the operator's decision).
5. Images: unset the five `R2_*`/`IMAGE_PUBLIC_BASE_URL` variables and restart (routes 503, public `images` `[]`; objects and rows remain).
6. Migration rollback exists (`make migrate-down`) but is destructive for data in those columns: operator only, never an agent.

Not reversible: a shop's deleted rows, a rotated `PAYMENT_CREDENTIALS_ENCRYPTION_KEY` (every shop re-enters ECPay credentials), a consumed one-time link (issue a new one with `password-reset`).
