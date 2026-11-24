# API cookbook (onboarding order)

Field names and permissions come from `api/openapi/openapi.yaml` (operationIds in brackets). Placeholders: `$API` = platform API base (for platform-level calls in production this must be the platform-admin domain behind Cloudflare Access), `$TOKEN` = an access token held in an environment variable (never echo it), `$SHOP` = the numeric shop id from step 2. Amounts are integers in the minor unit (TWD: whole dollars, `3400` = NT$3,400).

Set up once per session without printing anything:

```bash
# $PLATFORM_EMAIL / $PLATFORM_PASSWORD come from the environment, set by the operator.
TOKEN=$(curl -s -X POST "$API/api/v1/admin/auth/login" -H 'Content-Type: application/json' \
  -d "$(printf '{"email":"%s","password":"%s"}' "$PLATFORM_EMAIL" "$PLATFORM_PASSWORD")" \
  | node -pe 'JSON.parse(require("fs").readFileSync(0,"utf8")).access_token')   # access token lives 15 minutes (ACCESS_TOKEN_TTL)
auth=(-H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json')
```

Login failures are a single 401 (no hint); 10 failures per email in 15 minutes lock the account (429, even with the right password), so do not retry in a loop.

## 1. Who am I (sanity check before writing)

```bash
curl -s "${auth[@]}" "$API/api/v1/admin/me"          # shows platform_permissions; expect shop.create, account.manage, shop.manage_client_origins
```

## 2. Provision the shop [provisionShop]

Needs platform `shop.create` + `account.manage`; `domain` also needs `shop.manage_domains`; `storefront_origins` also needs `shop.manage_client_origins`. One transaction: shop (with an automatic home page), theme, optional domain binding, owner, origins. Any failure rolls everything back (409 on a conflict).

```bash
curl -s -X POST "${auth[@]}" "$API/api/v1/admin/shop-provisioning" -d '{
  "name": "<Shop Name>",
  "owner_email": "owner@example.com",
  "theme_id": 1,
  "status": 1,
  "storefront_origins": ["https://shop.example.com"]
}'
```

- `theme_id`, `status`, `domain`, `path_prefix`, `storefront_origins` are optional. `status`: 0 disabled (storefront 503), 1 active, 2 under review (storefront 404). Use `status: 2` while the shop is being built if you do not want it reachable, then set 1 at go-live.
- `domain` (+ optional `path_prefix`) is only for a platform-hosted (Next.js `web/`) storefront. A headless storefront (the bouquet template) does not need it; it identifies itself with a publishable key.
- A new owner email creates a password-less account and returns `owner.setup_url` and `owner.expires_at` (`delivery:"manual"`). An existing email is added as `merchant_owner` and its password is left alone (`owner.created:false`, no link).
- Response also has `shop.id` (use as `$SHOP`), `binding`, `storefront_origins`. The `setup_url` is shown once: hand it to the operator without logging it.
- Origins registered here are `purpose: storefront`. Register the admin origin separately (step 5).

## 3. Setup-status checklist [getShopSetupStatus]

```bash
curl -s "${auth[@]}" "$API/api/v1/admin/shops/$SHOP/setup-status"
```

Returns booleans derived from live data (nothing stored): `theme_applied, domain_bound, owner_assigned, owner_password_set, home_published, shipping_method_active, payment_credentials_set, publishable_key_active`. A headless shop will not need `domain_bound` or a published CMS home; the others are the real onboarding gates. Re-read after every step.

## 4. Owner password link

The link from step 2 has the form `{base}/#/set-password/{token}` where `{base}` is the shop's admin-purpose origin if one is registered (lowest id), otherwise `ADMIN_PUBLIC_URL`. Because it is built when issued, register the admin origin (step 5) before provisioning if you want the link to point at the merchant's own admin; otherwise reissue:

```bash
# platform account.manage; invalidates earlier unused reset links  [issueAccountPasswordReset]
curl -s -X POST "${auth[@]}" "$API/api/v1/admin/accounts/$OWNER_USER_ID/password-reset"   # delivery:"manual" -> reset_url (shown once)
```

Find `$OWNER_USER_ID` from `owner.user_id` in the step 2 response (or `GET /api/v1/admin/accounts?q=<email>`). The owner opens the link, sets a password of at least 12 characters, and is sent back to the login page (not logged in automatically). Links expire (setup 72h, reset 24h by default). The operator delivers the link out of band.

## 5. Allowed origins [createAllowedOrigin]

Platform `shop.manage_client_origins`. Origin = `scheme://host[:port]`, no path, no wildcard, globally unique across shops, and one origin has exactly one purpose.

```bash
# storefront (CORS for /api/v1/shop/*, /api/v1/render/*; none for /api/v1/admin/*)
curl -s -X POST "${auth[@]}" "$API/api/v1/admin/shops/$SHOP/allowed-origins" -d '{"origin":"https://shop.example.com","purpose":"storefront"}'
# the merchant's own admin site (binds that host to this shop server-side; invitation/password links point at it)
curl -s -X POST "${auth[@]}" "$API/api/v1/admin/shops/$SHOP/allowed-origins" -d '{"origin":"https://admin.shop.example.com","purpose":"admin"}'
curl -s "${auth[@]}" "$API/api/v1/admin/shops/$SHOP/allowed-origins"        # list to confirm
```

Do not register the shared merchant admin domain (`ADMIN_PUBLIC_URL`) or the platform admin domain under any shop. Fixed preview domains need their own entry; random-hash preview domains cannot be covered. For local admin testing over http also register `https://localhost:3001` (see `admin/README.md` in the bouquet repo).

## 6. Publishable key [createPublishableKey]

```bash
curl -s -X POST "${auth[@]}" "$API/api/v1/admin/shops/$SHOP/publishable-keys" -d '{"label":"<shop>-prod"}'   # -> {"key":"pk_..."}
curl -s "${auth[@]}" "$API/api/v1/admin/shops/$SHOP/publishable-keys"                                           # list (revoked ones included)
```

The `pk_...` value is a public identifier and is hard-coded in the storefront. Rotate: create a second key, deploy the storefront, then `DELETE .../publishable-keys/{keyID}` (takes effect immediately) [revokePublishableKey]. Use a different label for stage and production keys if you keep both.

## 7. Shipping methods [createShippingMethod]

At least one active method is required for checkout (`shipping_method_active`).

```bash
curl -s -X POST "${auth[@]}" "$API/api/v1/admin/shops/$SHOP/shipping-methods" \
  -d '{"name":"<Home delivery>","carrier":"<carrier>","flat_rate":120,"is_active":true}'
```

## 8. Categories [createCategory]

```bash
curl -s -X POST "${auth[@]}" "$API/api/v1/admin/shops/$SHOP/categories" -d '{"name":"<Name>","slug":"<slug>","parent_id":null,"position":0}'
```

The bouquet storefront reads the category slug in `ONDO_CONFIG.featuredCategory` (default `new-arrivals`) for its home-page featured block; create that category if the template is used unchanged, or change that setting (a locked-layer change).

## 9. Member tiers [createMemberTier]

```bash
curl -s -X POST "${auth[@]}" "$API/api/v1/admin/shops/$SHOP/member-tiers" -d '{"name":"<Tier>","min_points":0,"discount_percent":null}'
```

`discount_percent` (0-100) really reduces the order total at checkout (see `docs/storefront-integration.md` section 9.5). Only create tiers the merchant intends to honour. Points are earned on the item subtotal, not shipping.

## 10. ECPay per-shop credentials [putECPayCredentials]

Needs the merchant-level node `payment_credential.manage`, which only `merchant_owner` has. The operator or the owner enters it (logged in as the owner, in the admin or via this call); an agent must not handle real HashKey/HashIV.

```bash
curl -s -X PUT "${auth[@]}" "$API/api/v1/admin/shops/$SHOP/ecpay-credentials" \
  -d '{"merchant_id":"<MerchantID>","hash_key":"<HASH_KEY>","hash_iv":"<HASH_IV>","choose_payment":null}'
curl -s "${auth[@]}" "$API/api/v1/admin/shops/$SHOP/ecpay-credentials"     # masked: only configured true/false; keys are never returned
```

First-time setup requires both `hash_key` and `hash_iv`; later calls may omit them to keep the stored values. `choose_payment: null` or `""` clears back to the default `Credit`. Stage vs production is decided platform-wide by `ECPAY_CHECKOUT_URL` (see `env-reference.md` and `known-gaps.md`), not per shop: stage credentials only work while the deployment points at the stage URL. The payment webhook base is `ECPAY_RETURN_URL` plus `/{shopID}`; it must be reachable by ECPay on port 80/443.

## 11. Products with SKUs [createProduct]

```bash
curl -s -X POST "${auth[@]}" "$API/api/v1/admin/shops/$SHOP/products" -d '{
  "title":"<Title>","slug":"<slug>","subtitle":"<optional, max 200 chars>","description":"<text>",
  "status":1,"category_ids":[<categoryId>],
  "skus":[{"sku_code":"<SKU-S>","options":{"size":"S"},"price_amount":1200,"currency":"TWD","stock_qty":10,"is_active":true}]
}'
```

Updating a product with PUT replaces its SKU list wholesale; never drop an existing SKU (set `is_active:false` instead). For a real catalogue there is no generic importer in the platform; fork `scripts/seed-test-shop.mjs` from the bouquet template (see its header: edit the `CATEGORIES`/`PRODUCTS`/`SHIPPING` constants, set `SEED_API_BASE`, `SEED_ADMIN_EMAIL`, `SEED_ADMIN_PASSWORD`, `SEED_SHOP_ID`; it is idempotent; never use `SEED_PRUNE=1` on real data).

## 12. Product images (needs R2 configured) [createMediaUpload, createProductImage]

Four ordered steps; the PUT goes directly to R2/MinIO.

```bash
# a. upload intent (JPEG/PNG/WebP only, up to 15 MiB; HEIC must be converted to JPEG first)
curl -s -X POST "${auth[@]}" "$API/api/v1/admin/shops/$SHOP/media/uploads" -d '{"content_type":"image/webp","size_bytes":<N>,"purpose":"product_image"}'
#    -> {upload_id, upload_url, headers, expires_at}
# b. PUT the file bytes to upload_url with exactly the returned headers (size must equal size_bytes; valid 10 minutes)
curl -s -X PUT "<upload_url>" -H "Content-Type: image/webp" --data-binary @<file>
# c. attach (server converts to thumb 480 and large 1600 WebP and deletes the original)
curl -s -X POST "${auth[@]}" "$API/api/v1/admin/shops/$SHOP/products/$PRODUCT_ID/images" -d '{"upload_id":"<uuid>","alt":"<alt text>"}'
```

Max 20 images per product. A 503 `media_not_configured` means the five R2 variables are not set (see `env-reference.md`). Public check: `GET /api/v1/shop/products/{slug}` with `X-Publishable-Key` shows `images[].urls.thumb/large`.

## 13. Public read-back (what the storefront will see)

```bash
curl -s -H "X-Publishable-Key: $PK" "$API/api/v1/shop/products"
curl -s -H "X-Publishable-Key: $PK" "$API/api/v1/shop/categories"
curl -s -H "X-Publishable-Key: $PK" "$API/api/v1/shop/shipping-methods"
```

## Rollback-side calls

```bash
curl -s -X DELETE "${auth[@]}" "$API/api/v1/admin/shops/$SHOP/publishable-keys/$KEY_ID"        # revoke key, immediate
curl -s -X PUT "${auth[@]}" "$API/api/v1/admin/shops/$SHOP" -d '{"status":0}'                    # disable shop [updateShop, platform shop.update; send only the keys to change]
curl -s -X POST "${auth[@]}" "$API/api/v1/admin/accounts/$OWNER_USER_ID/disable"                 # disable the owner account
```

Prefer these reversible calls to deleting anything.
