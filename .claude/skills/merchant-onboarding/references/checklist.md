# Checklists: intake, sites, verification, go-live, rollback

Tick items in a run log (a scratch file outside the repos, no secrets). Mark each item A (agent), H (human operator), O (owner).

## Intake (step 0)

- [ ] Shop display name; owner email (kept out of files); storefront kind: headless storefront from the bouquet template, or platform-hosted theme (`web/`, needs `theme_id` and a `domain`).
- [ ] Hostnames decided: storefront (`shop.example.com`), the merchant's admin (`admin.shop.example.com`), whether images share the platform image domain.
- [ ] ECPay: merchant has an account; stage credentials first; who enters them (the owner).
- [ ] Target API: local throwaway, staging or production; who holds the platform token and through which route (platform admin domain behind Access).
- [ ] Catalogue source (list of products, SKUs, prices as integers, stock, images in JPEG/PNG/WebP), shipping methods and fees, tiers (name, min points, discount percent).
- [ ] Branch plan: storefront and admin work happens on a branch; nothing is pushed to `main` without the operator.

## Storefront site from the template (step 11)

Agent, in the new merchant repo (a copy of the bouquet template; a dedicated template repo is planned):

- [ ] Replace brand content only in the design layer: `public/content.js` (copy), `public/assets/**`, `public/styles.css`, `public/index.html` (title, og tags with the merchant's absolute URLs).
- [ ] Connect to the platform: set `apiBase` and `publishableKey` in `public/config.js`. **Conflict to resolve with the operator first:** `scripts/check-layers.mjs` fails when `public/config.js` no longer contains the `REPLACE_WITH_*` placeholders and when its hash differs from `scripts/commerce.manifest.json`. A real deployment therefore needs a reviewed, deliberate locked-layer change (edit `config.js`, relax the placeholder rule in the merchant repo, then `node scripts/check-layers.mjs --update`) or a deploy-time config step. Do not silently disable the guard. For local work use the gitignored `public/config.local.js` (loaded only on localhost) with `Object.assign(window.ONDO_CONFIG, { apiBase, publishableKey })`.
- [ ] Preview locally: `python3 -m http.server 8080 --directory public` (the `--directory public` is required). Backend: the platform's `make openapi-mock` (port 4010, no DB) or a throwaway API.
- [ ] Seed or import the catalogue with a fork of `scripts/seed-test-shop.mjs` (real data in its constants; env `SEED_API_BASE`, `SEED_ADMIN_EMAIL`, `SEED_ADMIN_PASSWORD`, `SEED_SHOP_ID`, `SEED_ORIGIN`; no `SEED_PRUNE` on real data).
- [ ] Guards: `node scripts/check-layers.mjs --check` and `node scripts/check-scope.mjs storefront --base <ref>`.
- [ ] H: Cloudflare Workers Git integration, custom domain; `wrangler.jsonc` `assets.directory` stays `./public`.

## Admin site (step 12)

Agent: the admin ships inside the same repo (`admin/`), static, no build.

- [ ] Design only in `admin/design/**` (see the repo's `admin-design` skill). Do not edit the locked core.
- [ ] Local run: `cd admin && node scripts/serve.mjs --proxy http://127.0.0.1:8080` and gitignored `admin/config.local.js` (`window.BQA_CONFIG.apiBase = location.origin;`).
- [ ] Guards from `admin/`: `node scripts/check-design.mjs --base <ref>`, `node scripts/check-layers.mjs`, `node scripts/test-guard.mjs`; from the repo root `node scripts/check-scope.mjs admin --base <ref>`.
- [ ] H: static hosting on the local port the tunnel points at, with the CSP from `admin/README.md`; tunnel, Access, DNS (`cloudflare-steps.md`); admin origin registered `purpose:"admin"` (cookbook 5) and added to R2 CORS.
- [ ] The admin must handle `#/set-password/{token}` and `#/accept-invite/{token}` at its root (the template does).

## Verification (step 15)

Platform side (agent, against local/throwaway stack unless the operator authorises production reads):

- [ ] `make openapi` (contract checks; no infra) and `make test` pass if the platform code was touched (it should not be).
- [ ] `GET .../setup-status`: `owner_assigned`, `owner_password_set`, `shipping_method_active`, `payment_credentials_set`, `publishable_key_active` all true (plus `theme_applied`, `domain_bound`, `home_published` for a platform-hosted shop).
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
