# Platform admin: locked core + AI-editable design layer (provenance and inventory)

Stage A of the split (2026-11-25). The platform admin (`admin/`) follows the architecture of the merchant template
(`merchant-template`, v0.2.0 on `main`, content-store parts from the reviewed `core/content-store-0.3.0` branch): every screen is
presentation code in `design/views/` that only calls the whitelisted `ctx` surface; everything security-sensitive or functional is in
the locked core. The template repo was NOT edited; files were copied and adapted. Adaptation notes per file:

| This repo | Copied/adapted from (merchant-template/admin) | What changed |
| --- | --- | --- |
| `lib/design.js` | `lib/design.js` | unchanged logic (copy lookup, route registry, per-entry fallback to `design-defaults/`) |
| `lib/design-schema.js` | `lib/design-schema.js` | `ROUTE_IDS` and `WARNED_CONFIRM_IDS` are derived from `lib/meta/*` (pure data) instead of hand lists; icon list reduced |
| `core/host.js`, `core/errors.js` | same | unchanged (mount with fallback + 20 s timeout; `ActionError`) |
| `core/guard.js` | `core/guard.js` | `need`, `needAny`, `needPlatform` (platform-level nodes exist here) |
| `core/context.js` | `core/context.js` | ctx is assembled from `core/groups/*` (one module per domain) instead of one `actions.js`/`data.js`; navigation is hash-based (`ctx.navigate(name, ...segments)`) |
| `core/warnings.js` | `core/warnings.js` | fixed warnings live in `lib/meta/<group>.js` (`warned`) so Node guard scripts can read them |
| `core/auth.js` | `core/auth.js` | this admin's link flows (set-password, accept-invite, login-and-join); tokens stay in closures |
| `ui/kit.js` | `ui/kit.js` | safe `h()` builder, `svg()`, `confirmDialog` copied; the pre-split `lib/dom.js` helpers (`rows`, `renderPager`, `repeater`, `fillSelect`) moved here; `value` is also set as an attribute on `<input>` (the e2e selects by attribute) |
| `scripts/lib/design-scan.mjs`, `scripts/check-design.mjs`, `scripts/test-guard.mjs`, `scripts/lib/ctx-surface.mjs` | same | adapted to this layout (see Stage B) |
| `scripts/serve.mjs` | `scripts/serve.mjs` | no proxy; optional `--csp` sends the production CSP |
| `e2e/parity.mjs` | `e2e/parity.mjs` | platform (super admin) and merchant owner roles, this admin's ids |

The routing (`#/<view>[/<param>]`), the API client (`lib/api.js`, cookie/body refresh modes, Access reload), money/format helpers,
upload (`lib/upload.js`, HEIC) and RBAC (`lib/rbac.js`) are the pre-split code, unchanged.

## Inventory of the pre-split admin (nothing dropped)

Pre-split registry (`app.js`) -> route in `lib/meta/*`:

| route id | hash | gate | group / default view |
| --- | --- | --- | --- |
| login, setPassword, acceptInvite | `#/login`, `#/set-password/{token}`, `#/accept-invite/{token}` | public | auth: `login.js`, `set-password.js`, `accept-invite.js` |
| (shell) | topbar, shop selector, tabs, no-access notice | n/a | auth: `shell.js` (+ `not-found.js`, `no-permission.js`, `boot-error.js`, new fallbacks) |
| pages | `#/pages` (list + editor `editor` panel inside the view) | page.view | cms: `pages.js` |
| content | `#/content` (global content) | shop.view | cms: `content.js` |
| orders | `#/orders[/{id}]` (list, detail, partial shipments) | order.view | orders: `orders.js` + `parts/order-shipments.js` |
| shippingMethods | `#/shipping-methods` | shipping_method.view | orders: `shipping-methods.js` |
| products | `#/products` (list + `product-editor` panel + images) | product.view | catalog: `products.js` + `parts/catalog-product-images.js` |
| categories | `#/categories` (+ order dialog) | category.view | catalog: `categories.js` |
| memberTiers | `#/member-tiers` | member_tier.view | members: `member-tiers.js` |
| members | `#/members[/{id}]` | member.view | members: `members.js` |
| points | `#/points[/{id}]` | point.view | members: `points.js` |
| reports | `#/reports` (+ timezone and admin branding forms) | report.view | members: `reports.js` |
| storefrontClients | `#/storefront-clients` | any of publishable_key.view, shop.manage_client_origins, payment_credential.manage | settings: `storefront-clients.js` |
| team | `#/team` | user.manage_members | settings: `team.js` |
| platformShops | `#/platform-shops` (list, wizard, setup checklist) | platform shop.list | platform: `platform-shops.js` |
| platformAccounts | `#/platform-accounts` | platform account.view | platform: `platform-accounts.js` |
| platformApplications | `#/platform-applications` (review, outbox, manual confirm) | platform shop_application.view | platform: `platform-applications.js` |

Old transient routes `#/editor` and `#/product-editor` were never nav items; their panels now live inside `pages` / `products` and no longer change the hash.

Irreversible actions (core-owned confirmation + FIXED warning, `lib/meta/*` `warned`): deletePage, deleteProduct, deleteCategory, deleteImage,
cancelOrder, shipDelivered, shipReturned, deleteShippingMethod, deleteMemberTier, changeTimezone, revokeKey, removeOrigin, disableMember,
disableAccount, removeMembership, confirmApplicantContact. They used native `confirm()` before; they are now in-page `<dialog data-testid="confirm-dialog">` with
`confirm-yes` / `confirm-no`. The publishable-key label prompt (native `prompt()`) became a small dialog (`#publishable-key-dialog`).
