---
name: platform-admin-design
description: Operating manual for an AI session that edits the PLATFORM admin's DESIGN layer (admin/design/**: tokens.css, theme.css, copy.js, routes.js, views/, parts/, assets/) in the ecommerce monorepo, using only the whitelisted ctx in admin/lib/public-api.json. Use when asked to change the look, wording, layout, navigation, colours, logo or screens of the platform admin / shared merchant admin (orders, products, members, reports, team, platform shops, accounts, applications, login). Requires a sparse checkout or worktree that contains ONLY admin/. Includes the guard commands, the fixed-warning rule, the escalate-and-stop rule, and the report format. Not for API (Go) work, not for core changes (use core-change), not for the merchant-template admin.
---

# Platform admin design layer

You change how the platform admin LOOKS and READS. You cannot change what its buttons DO, who may use them, or how data is fetched: those live in the locked core and reach your code only through `ctx`. The full contract is `admin/docs/DESIGN-LAYER.md` (read sections 0 to 3 completely before editing). This skill is the short procedure around it.

## Preconditions (check first; stop if any is false)

1. The working directory contains **only `admin/`** (plus `.git`). Run `ls` at the repo root: you must see `admin` and nothing like `api`, `docker`, `docker-compose.yml`, `.github`, `.env*`. If you can see them, STOP and tell the human: a design session must run in a sparse checkout or worktree. The human creates it with (non-cone mode on purpose; cone mode would also bring the root files):

   ```bash
   BASE=$(git rev-parse origin/main)
   git worktree add --no-checkout ../ecommerce-admin-design -b design/admin-<request-name> "$BASE"
   cd ../ecommerce-admin-design
   printf '/admin/\n' | git sparse-checkout set --no-cone --stdin
   git checkout
   ```
2. You have no credentials and no push rights. You never push, merge, deploy or open a PR.
3. Record the starting point: `BASE=$(git rev-parse HEAD)` (the guard diffs against it). Work on a branch named `design/admin-<short-name>`, never on `main`.

All commands below run from `admin/` (`cd admin`).

## What you may edit (only these)

`admin/design/**`: `tokens.css` (CSS variables; today only `--brand-primary`, `--brand-accent`, and a shop's branding overrides them at runtime), `theme.css` (free CSS loaded last), `copy.js` (all wording, data only, known keys only), `routes.js` (route id to view file, data only), `views/*.js` (one screen per file, `export default function (root, ctx)`), `parts/*.js` (shared pieces), `assets/` (svg png jpg jpeg webp gif ico avif woff2; create the folder if needed; reference with `ctx.asset("x.svg")` or CSS `url(assets/x.svg)`).

## Locked (never edit, never "fix")

Everything else: `admin/app.js index.html styles.css nginx.conf Dockerfile .dockerignore package.json README.md CLAUDE.md lib/ core/ ui/ vendor/ design-defaults/ scripts/ docs/ e2e/`, and anything outside `admin/`. In particular `lib/public-api.json`, `lib/meta/*.js` (routes and the FIXED warnings) and `scripts/core.manifest.json`. **Never run** `node scripts/check-design.mjs --update`, `node scripts/gen-public-api.mjs`, `node scripts/sync-design.mjs`. **Never edit the manifest.** `styles.css` is locked on purpose: restyle only through `design/tokens.css` and `design/theme.css`.

## How to build or change a screen

- Use only `ctx` (names in `admin/lib/public-api.json`, described in DESIGN-LAYER.md section 4). To learn how a screen uses it, read the matching `admin/design-defaults/views/<route>.js` (read-only reference). Build DOM with `ctx.ui.h(tag, attrs, ...children)`; reads via `ctx.data.*`; writes via `ctx.actions.*`; wording via `ctx.t(key, params)`.
- Import only other files under `design/` by relative path. No `fetch`, storage, `window`/`document`, `innerHTML`, `eval`, external URLs (full list: DESIGN-LAYER.md section 6).
- After an `await`, check `ctx.alive()`. Show errors with `ctx.errorMessage(e, {...})`.
- Routes are fixed: `#/<view>[/<param>]`, ids and permissions in `lib/meta/*.js`; `design/routes.js` only maps an id to a file. To add a **view variant**: create `design/views/<new>.js` (start from a copy of the current view, keep calling the same `ctx.actions`), then point the route id at it in `design/routes.js` (`"orders": "views/orders-table.js"`); leave the old file in place. The `shell` view must still return `{ main, setActive, setNoAccess }`.
- Required fields stay required: the core action validates them. Do not remove title/slug/SKU/amount fields from a form to "simplify" it; you may move or restyle them.
- **Irreversible-action warnings are FIXED.** The 16 ids (`deletePage deleteProduct deleteCategory deleteImage cancelOrder shipDelivered shipReturned deleteShippingMethod deleteMemberTier changeTimezone revokeKey removeOrigin disableMember disableAccount removeMembership confirmApplicantContact`) show the core's confirmation dialog with a warning from `lib/meta/*.js` (`data-testid="confirm-warning"`). You may edit only `confirm.<id>.title`, `.text` (lead-in, may be empty), `.yes`, `.no`. Never add a `confirm.*.warning` / `*warn*` key, never hide, shrink or disable the dialog or the warning in CSS (`css/hides-dialog`), never build a replacement confirmation. Wanting a different warning is an escalation.
- Money is whole TWD (`ctx.format.money`, never scaled). Points are not money (`ctx.format.points`, never `money`).

## Copy editing rules

Edit values in `design/copy.js` only (keys come from `design-defaults/copy.js` and `design-defaults/copy/*.js`; find a key with `grep "<current text>" design/copy.js`). Plain data: no calls, templates, spreads or concatenation. Known keys only (a new key is a core change). A string may use fewer `{placeholders}` than the default, never new ones. Max 600 characters. Keep nav labels short (390px wide).

## Restyling

`tokens.css` for the two colour variables, `theme.css` for everything else (it loads after `styles.css`, so equal specificity wins). Rules: no `@import`, `url()` only to `design/assets/` or `data:image/`, no `expression()` / `behavior`, no CSS escapes outside strings, nothing that hides or disables dialogs. Fonts only as `woff2` in `design/assets/` via `@font-face`. Read `admin/styles.css` to see the core classes (`.tab`, `.topbar`, `.view`, `.badge`, forms, tables) to override.

## Workflow

1. Read the request. Needs a core change or a `ctx` function that does not exist? Go to **Escalate** now.
2. Branch from `BASE` (see Preconditions). Smallest change first: tokens, then `copy.js`, then `theme.css`, then a view.
3. Run the guards after every meaningful edit and always before reporting (from `admin/`):
   ```bash
   node scripts/scan-views.mjs design                 # quick scan of design JS/CSS; exit 0 prints "design scan: clean"
   node scripts/check-design.mjs --base "$BASE"       # THE gate; exit 0 ok / 1 violations / 2 usage error (nothing judged)
   node scripts/test-guard.mjs                        # the guard's own tests (179); a failure means a damaged checkout
   ```
   `check-design.mjs --base` also fails if anything outside `admin/design/**` differs (even a stray file at the repo root, shown as `../x`). Keep scratch output outside the repository. Fix every reported line (rule, file, line) and re-run. Without `--base` the scope check is skipped, so always pass it. Optional: `--json` for a machine report; `--files design/views/x.js design/theme.css` to check an explicit list.
4. Preview with the production CSP (a design that only works without it is broken):
   ```bash
   node scripts/serve.mjs --csp          # http://localhost:3001 (PORT=3002 to change)
   ```
   It needs an API: a THROWAWAY one (own database, own Redis DB), configured in the app (default `http://localhost:8080`, `localStorage.apiBase` overrides). Never point it at real data. Local MinIO for image uploads needs `CSP_CONNECT_EXTRA="http://127.0.0.1:9000" node scripts/serve.mjs --csp`. If no throwaway API is available, say so and skip the visual check (do not improvise one with real credentials). Look at the browser console: every design problem is a `[design] ...` warning; a silent fallback to the built-in screen means your view threw or hung (20 s). Check 390px and 1280px, no horizontal scroll, as a platform admin and as a merchant owner when navigation or permissions are involved.
5. **Stop.** Report (format below). Wait for the human. You never merge, push or deploy.

If the guard fails on `manifest/changed`, `ctx/manifest`, `layers`, `deploy/*` or the defaults, you changed something outside `design/` or the checkout is damaged: restore the file (`git checkout "$BASE" -- <file>`), do not `--update`, and if it persists escalate.

## Escalate (say so and stop; never work around)

- Adding a function/action/namespace to `ctx`, a new copy key, a new route or URL, a new or changed warning, a changed validation, state rule or permission, a new dependency, an external script/font/image, tracking, anything touching `nginx.conf`/CSP, the guards, or anything outside `admin/design/**`: a human-reviewed core change (`.claude/skills/core-change/SKILL.md`).
- Showing data the person lacks permission for; weakening a confirmation, the guard or the CSP.
- Anything that would need credentials, the network, or repo-wide access.

Reply with: what is needed, why it is core, the smallest proposal for a developer, and what you can do within the design layer now. Nothing is edited when you escalate.

## Known limits (state them when relevant)

The scan is defence in depth, not a sandbox. A design can still change appearance and wording, hide non-warned UI, reorder navigation and obscure buttons, so a human must look at the preview before approving. It cannot skip permission checks, confirmations or validation, or touch tokens.

## Report format (what you send back)

1. One-paragraph summary in the operator's terms.
2. Files changed (all under `admin/design/`), one line each.
3. Guard result: the final lines of `check-design.mjs --base "$BASE"` (and the `test-guard.mjs` pass count), or the failures you could not fix.
4. How to preview (branch, `node scripts/serve.mjs --csp`), and what you looked at (widths, roles, `[design]` console warnings seen).
5. Limits and escalations (what you did not do and why).
6. State clearly: not merged, not pushed, not deployed.

Single sources of truth (read them rather than relying on memory): `admin/docs/DESIGN-LAYER.md`, `admin/lib/public-api.json`, `admin/CLAUDE.md`, `admin/README.md`, `admin/docs/AI-EDIT-SERVICE-NOTES.md`, the header of `admin/scripts/check-design.mjs`.
