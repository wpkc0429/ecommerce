# Environment variable reference (onboarding-relevant)

Source of truth: `.env.example` (comments explain each), `api/internal/config/config.go`, `docs/media-r2-setup.md`, `docs/deploy/admin-exposure-runbook.md`. Values live only in the server's `.env` / secret store, which only the human operator edits. An agent may read variable NAMES and tell the operator what to set, never print values.

"Restart" means restart the API process after changing (the config is read at start).

## Per-deployment (set once for the platform, not per merchant)

| Variable | Purpose | Notes |
|---|---|---|
| `APP_ENV` | `production` turns on extra startup refusals and WARN lines | with production and no `CF_ACCESS_*` / `PLATFORM_ADMIN_HOSTS` the API logs a WARN each |
| `DATABASE_URL`, `REDIS_ADDR`, `REDIS_PASSWORD`, `REDIS_DB` | main infra | a second API on the same Redis must use a different `REDIS_DB` |
| `ADMIN_JWT_SECRET`, `MEMBER_JWT_SECRET` | token signing | generate, different from each other; never print |
| `TRUSTED_PROXIES` | peers whose `CF-Connecting-IP` is believed (cloudflared on the same host: `127.0.0.1/32,::1/128`) | login lockout and rate limits depend on it |
| `CORS_ALLOWED_ORIGINS` | CORS for the platform's own frontends (platform admin domain, platform web) | default `http://localhost:3001,http://localhost:3000`; merchant origins are NOT listed here, they are registered per shop in the database |
| `ADMIN_PUBLIC_URL` | base of password-setup / invitation links when a shop has no admin-purpose origin | MUST be the shared MERCHANT admin domain, not the platform admin domain; `http(s)://host`, no path/slash/fragment |
| `ADMIN_SETUP_TOKEN_TTL` / `ADMIN_RESET_TOKEN_TTL` / `ADMIN_INVITE_TOKEN_TTL` | link validity | defaults 72h / 24h / 168h |
| `PAYMENT_CREDENTIALS_ENCRYPTION_KEY` | AES-256-GCM key for stored per-shop ECPay credentials | 32 bytes hex or base64 (`openssl rand -hex 32`); if unset a public dev key is used, so set a private one before any real credential is stored; losing/rotating it means every shop re-enters credentials |
| `ECPAY_CHECKOUT_URL` | which ECPay environment the whole deployment talks to | stage `https://payment-stage.ecpay.com.tw/Cashier/AioCheckOut/V5`, production `https://payment.ecpay.com.tw/Cashier/AioCheckOut/V5`, default is the local simulator |
| `ECPAY_RETURN_URL` | base of ECPay's payment notification; per-shop path `/{shopID}` is appended | must be absolute, public, port 80/443, no internationalised domain |
| `ECPAY_CLIENT_BACK_URL` | optional "return to shop" target | the payment result never arrives this way |
| `ECPAY_HASH_KEY`, `ECPAY_HASH_IV` | read ONLY by the local simulator binary `api/cmd/ecpay-sim`, not by the API | not production credentials |
| `PAYMENT_DEFAULT_PROVIDER` | default `mock` (dev/e2e) | a real deployment sets `ecpay` (`.env.example`) |
| `PAYMENT_MOCK_WEBHOOK_SECRET` | HMAC key of the always-mounted mock webhook route | outside development/test it must be a real random value, or anyone could forge "payment succeeded" |
| `REPORT_DEFAULT_TIMEZONE` | fallback timezone for reports | default `Asia/Taipei`; a shop can set its own via `PUT .../settings` |
| `SMTP_HOST` / `SMTP_PORT` / `SMTP_USERNAME` / `SMTP_PASSWORD` / `SMTP_FROM` / `SMTP_FROM_NAME` | customer order emails (order placed, payment succeeded) | without `SMTP_USERNAME`/`SMTP_PASSWORD` nothing is sent and nothing errors; platform-wide single sender; does NOT deliver admin account links (those are `delivery:"manual"`) |

## Cloudflare Access and admin exposure (runbook sections 5)

| Variable | Value | Notes |
|---|---|---|
| `CF_ACCESS_TEAM_DOMAIN` | `<team>.cloudflareaccess.com` | must be set together with `CF_ACCESS_AUD` or the API refuses to start |
| `CF_ACCESS_AUD` | `AUD_MERCHANT,AUD_PLATFORM` | comma-separated AUD tags of the two Access applications |
| `CF_ACCESS_BIND_EMAIL` | default off | when `true` the Access JWT email must equal the admin account email; off by default because the merchant application admits anyone who passes the one-time PIN |
| `PLATFORM_ADMIN_HOSTS` | `platform-admin.example.com` | platform-level admin API only answers on these hosts |
| `RATE_LIMIT_LOGIN_ACCOUNT_FAILURES` / `_WINDOW` | 10 / 15m | per-email lockout |

Access verification is fail-closed: if the API cannot fetch the team's JWKS, every admin request returns 403.

Note: `CORS_ALLOWED_ORIGINS`, `PLATFORM_ADMIN_HOSTS` and `CF_ACCESS_*` are not listed in `.env.example` (they are in the runbook and `config.go`).

## Images (R2): all five or none (`docs/media-r2-setup.md` step 6)

| Variable | Value |
|---|---|
| `R2_ACCOUNT_ID` | Cloudflare account id |
| `R2_ACCESS_KEY_ID` / `R2_SECRET_ACCESS_KEY` | R2 API token (Object Read & Write, scoped to the one bucket); secret shown once |
| `R2_BUCKET` | bucket name |
| `IMAGE_PUBLIC_BASE_URL` | `https://img.example.com` (custom domain, https, no trailing slash); changing it needs no data migration |

Optional: `R2_ENDPOINT` (MinIO only), `IMAGE_EXTRA_ALLOWED_ORIGINS`, `MEDIA_MAX_BYTES`, `MEDIA_MAX_PIXELS`, `MEDIA_PROCESS_CONCURRENCY` (set `1` on a VPS under 1 GB), `MEDIA_WEBP_QUALITY`, `MEDIA_UPLOAD_URL_TTL`. A partial set makes the API refuse to start and name the missing keys. Rollback: unset them and restart (routes answer 503, public `images` become `[]`).

## Seed / bootstrap only

`SEED_ADMIN_EMAIL`, `SEED_ADMIN_PASSWORD` (`make seed` creates the platform super admin). Not used for merchants.

## Merchant template variables (not platform env)

- Storefront `public/config.js` (`SHOP_CONFIG`): `apiBase`, `publishableKey`, `showPrices`, `featuredCategory`. Local overrides only in the gitignored `public/config.local.js`.
- Catalogue importer `scripts/import-catalog.mjs`: `IMPORT_API_BASE`, `IMPORT_ADMIN_EMAIL`, `IMPORT_ADMIN_PASSWORD` (required for a real run), `IMPORT_SHOP_ID`, `IMPORT_ORIGIN`. Admin container (`admin/deploy/Dockerfile`): `ADMIN_IMG_ORIGIN`, `ADMIN_UPLOAD_ORIGIN`.
- Admin: gitignored `admin/config.local.js` sets `ADMIN_CONFIG.apiBase` on localhost only; off localhost the base is always `location.origin`.
