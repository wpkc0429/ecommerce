# Cloudflare steps (human operator only)

An agent must not perform these. It may prepare the exact values (hostnames, the ingress block, the CORS JSON) for the operator to apply, and verify the result afterwards with the commands in `checklist.md`. Source: `docs/media-r2-setup.md`, `docs/deploy/admin-exposure-runbook.md`, `docs/deploy/cloudflared-admin.example.yml`. Emails go only into the Cloudflare dashboard, never into a file.

Placeholders: `shop.example.com` storefront, `admin.shop.example.com` the merchant's admin, `merchant.example.com` shared merchant admin, `platform-admin.example.com` platform admin, `api.example.com` public API, `img.example.com` image domain.

## A. R2 images (do once per platform; per merchant only the CORS line)

1. Dashboard, R2, create bucket (no `r2.dev` public URL).
2. R2 API token: Object Read & Write, limited to that bucket. Record Access Key ID, Secret (shown once), Account ID. Put them only in the server's secret store / `.env`.
3. Bucket, Settings, Custom Domains: connect `img.example.com` (same Cloudflare account). That origin is `IMAGE_PUBLIC_BASE_URL`.
4. Bucket CORS policy: allow method `PUT`, header `Content-Type`, `MaxAgeSeconds` 3600, from EVERY admin origin that uploads. **Each new merchant admin origin must be added here** or its uploads fail in the browser with a CORS error while everything else works:
   ```json
   [{ "AllowedOrigins": ["https://admin.shop.example.com"], "AllowedMethods": ["PUT"], "AllowedHeaders": ["Content-Type"], "MaxAgeSeconds": 3600 }]
   ```
5. Lifecycle rule: prefix `incoming/`, delete after 1 day.
6. Set the five variables (see `env-reference.md`), restart the API.
7. Verify: `GET /api/v1/admin/media/config` returns `{"enabled": true, ...}`; upload one product image in the admin; the public `images[0].urls.large` opens as `image/webp` with `Cache-Control: public, max-age=31536000, immutable`; no leftover under `incoming/`.

If the merchant's admin or storefront uses a strict CSP, `img-src` needs the image domain and `connect-src` the R2 direct-upload origin (`docs/admin-frontend-integration.md` section 7, `admin/docs/AI-EDIT-SERVICE-NOTES.md` CSP block of the merchant template).

## B. Static hosting for the merchant admin (origin server)

The admin is a build-less static folder (`admin/` of the merchant template; publish only `index.html app.js styles.css config.js config-loader.js lib/ core/ ui/ design/ design-defaults/ vendor/`; never `config.local.js`). The template ships the hosting files: `admin/deploy/nginx.conf.template` (strict CSP, origins from `ADMIN_IMG_ORIGIN` / `ADMIN_UPLOAD_ORIGIN`), `admin/deploy/Dockerfile` (build from `admin/`) and `admin/deploy/cloudflared-ingress.example.yml`. Serve it on a local port (the example config uses `127.0.0.1:3001`). Each distinct admin build needs its own port or directory; do not point two different merchants' tunnel hostnames at one folder unless they are meant to share the same design.

## C. Tunnel ingress (your tunnel config file, outside the repos)

Merge by hand from `docs/deploy/cloudflared-admin.example.yml`. For every admin hostname two rules, the `path` rule first (first match wins):

```yaml
  - hostname: admin.shop.example.com
    path: ^/api/v1/admin
    service: http://127.0.0.1:8080        # the API
  - hostname: admin.shop.example.com
    service: http://127.0.0.1:3001        # the static admin
```

Leave the public API hostname rule (`^/api/v1/admin` to `http_status:404`) untouched. Create a DNS CNAME to the tunnel for each new hostname (`cloudflared tunnel route dns <tunnel> <hostname>` or the dashboard), then restart cloudflared. The storefront needs none of this: the merchant template storefront is a Cloudflare Workers static-assets deployment, with its custom domain attached in the Workers dashboard.

## D. Cloudflare Access (Zero Trust, dashboard)

Two Self-hosted applications; the dashboard is the only place for emails.

- **Merchant admin application** (one for all merchants): add each merchant admin hostname (path empty) and the shared merchant domain; login method only One-time PIN; policy Allow, Include Everyone (the PIN proves the mailbox; authorization is application RBAC). Record its Application Audience Tag as `AUD_MERCHANT`. Adding a merchant means adding its hostname to this application.
- **Platform admin application**: hostname `platform-admin.example.com`; policy Allow, Include Emails, the platform owner only. Record `AUD_PLATFORM`.
- Team domain `<team>.cloudflareaccess.com`.
- Then set on the API: `CF_ACCESS_TEAM_DOMAIN`, `CF_ACCESS_AUD=AUD_MERCHANT,AUD_PLATFORM`, `PLATFORM_ADMIN_HOSTS`, `ADMIN_PUBLIC_URL` (shared merchant domain) and restart. Access verification is fail-closed (403 if the JWKS cannot be fetched).

## E. Storefront hosting (merchant template)

Workers static assets via Git integration: production branch `main`, build command empty, deploy command `npx wrangler deploy`, publish boundary `public/` (`wrangler.jsonc`). **A push to `main` is a production deploy: only the operator approves it.** Attach the custom domain in the Cloudflare dashboard. `og:url` and `og:image` in `public/index.html` are absolute URLs and must be changed to the merchant's domain. Register the final storefront origin with the platform (cookbook 5) before testing in a browser.
