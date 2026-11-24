---
name: merchant-onboarding
description: Playbook for onboarding a NEW merchant (shop) onto this multi-tenant e-commerce platform end to end - provisioning the shop and owner account, allowed origins, publishable key, R2 images, shipping/categories/tiers/catalogue, per-shop ECPay credentials, creating the storefront and admin sites from the merchant template (scripts/new-merchant.mjs), Cloudflare Tunnel/Access/DNS, verification, go-live and rollback. Use when asked to onboard, set up, provision, launch or hand over a merchant/shop/store, or to continue or audit a half-finished onboarding. Not for changing platform code, for day-to-day shop operations, or for editing an existing storefront or admin design.
---

# Merchant onboarding

Goal: take a merchant from "signed" to "live, verified, handed over" in a fixed order, with a clear line between what an agent may do and what only the human operator may do. Every endpoint, env var and command named here exists in this repo; request shapes are in `references/`. The OpenAPI contract `api/openapi/openapi.yaml` is the only authority for field names; if a cookbook shape and the contract disagree, the contract wins (and tell the operator).

## Direction (ADR 0001, `docs/decisions/0001-headless-platform-and-merchant-template.md`)

**Headless is the only supported path.** Every merchant's storefront and admin are created from the merchant template repo (`merchant-template`, sibling of this repo; `bouquet` is the original site it was extracted from) and restyled by an AI agent that edits only each site's design layer. There is no platform-hosted storefront choice: do not pick a theme, bind a site domain or use `web/` for a merchant. Those features (hosted rendering, themes, site/domain routing) still exist in the API for now but must not be used for onboarding; the user approved removing them (2026-11-24, change `retire-hosted-storefront-and-themes`, direct removal). **The CMS is NOT removed.** Its content features are being reshaped into a headless content store (see below); nothing in this skill says the CMS is going away.

**Where things live (placement rule, decided 2026-11-24):** anything needing backend computation or server-side state belongs to ecommerce (API and platform admin); content that rarely changes lives in the site's own repo (the merchant's design layer, or the platform website repo); content that the merchant edits often through forms (texts, banners, campaign copy, promo images, store info, FAQ) lives in the ecommerce content store.

**Planned, not built yet (content store):** change `headless-content-store` (a proposal, nothing implemented) turns the CMS content features into a per-shop headless content store that the merchant's site reads with its publishable key. When it exists, onboarding gains: register the site's `content.schema.json` (a human action from the merchant admin, after a dry run), create the initial content documents and publish them, and optionally configure the publish hook (a deploy-hook URL on an allowlisted host) so a publish rebuilds the site. Until it is implemented, merchant copy stays in the design layer (`public/content.js`) and none of these steps can be done; today's `/pages` and `/content` endpoints must not be used as a substitute for a merchant's content.

**Planned, not built yet:** a public application form on the platform website (change `platform-website`, a proposal) will become the intake source: an applicant submits and verifies an email, an operator approves in the platform admin, and approval calls the same one-shot provisioning API as step 2. Until that ships, intake (step 0) is the operator's own decision and nothing in this skill depends on the website.

## Hard rules (always)

1. **Never print, log, commit or paste a secret**: passwords, access/refresh tokens, `ADMIN_JWT_SECRET`, `R2_SECRET_ACCESS_KEY`, ECPay `HashKey`/`HashIV`, `PAYMENT_CREDENTIALS_ENCRYPTION_KEY`, `SMTP_PASSWORD`, Cloudflare tokens, the one-time `setup_url`/`invite_url`/`reset_url`. Read them from the environment or a file the operator names; redirect curl output you do not need. Publishable keys (`pk_...`) are public identifiers and may be shown.
2. **No personal emails in files.** Owner and Access-policy emails live only in the API call / Cloudflare dashboard. Use `owner@example.com` placeholders in anything written down.
3. **Never run destructive commands**: no `docker compose down -v`, no `DROP`/`TRUNCATE`/`DELETE` on a database, no `make migrate-down`, no `FLUSHALL`, no `git push --force`. Never touch the main (production) database or Redis unless the operator explicitly says so for that command.
4. **Never push to a branch that auto-deploys production** (in the merchant template, `main` deploys the storefront) without the human's explicit say-so for that push. Work on a branch, show the diff, stop.
5. **Do not modify the platform repo's code, docs or deploy config** during onboarding. Gaps found go in the report.
6. Confirm the target before any write: print the API base host and shop id you are about to call (not tokens). Production writes need the operator's yes.
7. One-time links (`setup_url`, `invite_url`, `reset_url`) are shown once by the API. Do not store them; hand them to the operator immediately for secure delivery to the owner.

## Who does what

- **Agent (A)**: API calls (with a token the operator provides via env), repo scaffolding, seed/import scripts against throwaway or approved targets, running tests and guards, writing the run log.
- **Human operator (H)**: Cloudflare dashboard (R2, Tunnel, Access, DNS), anything involving secrets or `.env`, ECPay account and credentials, delivering the password link to the owner, production deploys/pushes, DNS and money steps.
- **Owner (O)**: the merchant's owner, via the admin: sets own password, enters ECPay credentials (only `merchant_owner` has `payment_credential.manage`).

In production the platform-level admin API is reachable only from `PLATFORM_ADMIN_HOSTS` behind Cloudflare Access; an agent normally has no way through Access. Unless the operator supplies a working token/route, produce the exact requests from `references/api-cookbook.md` for the operator to run, or run them against a local/staging API.

## Sequence (order and dependencies matter)

| # | Step | Who | Depends on | Detail |
|---|------|-----|-----------|--------|
| 0 | Intake: shop name, owner email, domains (storefront, admin), ECPay stage or production, tier plan (today the operator supplies these; the platform-website application flow is planned, see Direction) | A+H | - | `references/checklist.md` Intake |
| 1 | Platform preconditions: API up, migrations applied, `ADMIN_PUBLIC_URL` = shared merchant admin domain, `PAYMENT_CREDENTIALS_ENCRYPTION_KEY` private | H | - | `references/env-reference.md` |
| 2 | Provision shop: `POST /api/v1/admin/shop-provisioning` (shop, owner, `storefront_origins` in one transaction; no theme, no domain) | A | 1 | cookbook 2 |
| 3 | Read `GET /api/v1/admin/shops/{id}/setup-status`; keep as the live checklist (ignore `theme_applied` and `domain_bound`, which belong to the retired hosted storefront; `home_published` reflects the CMS home page and is only meaningful once the content store ships) | A | 2 | cookbook 3 |
| 4 | Hand the owner's one-time password link to the operator (`delivery:"manual"`); owner sets password at `/#/set-password/{token}` | A->H->O | 2, admin site reachable (steps 12-13) | cookbook 4 |
| 5 | Allowed origins: storefront origin(s) as `purpose:"storefront"`, admin origin as `purpose:"admin"` (different origin for each; no wildcards) | A | 2 | cookbook 5 |
| 6 | Publishable key: `POST .../publishable-keys` | A | 2 | cookbook 6 |
| 7 | R2 / image domain: bucket, token, custom domain, CORS incl. the new admin origin, lifecycle, env vars, API restart | H | - | `references/cloudflare-steps.md` R2 |
| 8 | Shipping methods, categories, member tiers | A | 2, 4 (or a platform token holding the nodes) | cookbook 7-9 |
| 9 | ECPay per-shop credentials (stage first, production only when the operator says) | O or H | 2, owner password set | cookbook 10 |
| 10 | Import catalogue (products with SKUs, then images through the upload flow) | A | 5-8 | cookbook 11-12 |
| 11 | Create the merchant repo with `node scripts/new-merchant.mjs` (storefront + admin in one repo); API base and publishable key go into `public/config.js`; run guards | A | 6 | `references/checklist.md` Storefront |
| 11a | **PLANNED, not built (change `headless-content-store`)**: register the site's `content.schema.json` (human-triggered, dry run first), create and publish the initial content documents, optionally set the publish hook. Skip until the change is implemented | A+H | 6, 11, and the change implemented | `references/checklist.md` Content store (planned) |
| 12 | Admin site (the repo's `admin/`) and its static hosting (`admin/deploy/`) | A (repo) + H (hosting) | 5 | `references/checklist.md` Admin |
| 13 | Tunnel ingress, Access application, DNS CNAMEs, API env (`CF_ACCESS_*`, `PLATFORM_ADMIN_HOSTS`) | H | 12 | `references/cloudflare-steps.md` |
| 14 | Register the final admin origin (if not done in 5) and add it to the R2 CORS list | A+H | 13 | cookbook 5, R2 CORS |
| 15 | Verify (all of `references/checklist.md` Verification) | A | all | |
| 16 | Go-live: switch ECPay to production credentials and checkout URL, production push of the storefront, final owner walkthrough | H | 15 | `references/checklist.md` Go-live |

Steps 7, 9 (credentials) and 13 are the usual critical path: start them first because they wait on the human.

## Stop and ask when

- `setup-status` flags stay false after the step that should flip them.
- A request returns 403 (missing permission node, or the request came from a host bound to another shop or not in `PLATFORM_ADMIN_HOSTS`), 409 (domain or origin already taken: origins are globally unique) or 422.
- Anything would touch production data, credentials, DNS, or deploy.
- The merchant needs something the platform cannot do (see `references/known-gaps.md`).

## Rollback in one paragraph

Each step is reversible without data loss except credential rotation and deleted rows: disable the shop (`status` 0), revoke publishable keys, remove the tunnel rules for the admin domain, clear `CF_ACCESS_*`. Full list and commands: `references/checklist.md` Rollback.

## Files

- `references/api-cookbook.md` - copy-pasteable requests with placeholders, in sequence order
- `references/env-reference.md` - every environment variable that matters for onboarding, who sets it
- `references/cloudflare-steps.md` - R2, Tunnel, Access, DNS steps (human)
- `references/checklist.md` - intake, per-site setup, verification commands, go-live, rollback
- `references/known-gaps.md` - verified platform limitations and doc inaccuracies

Related docs in this repo: `docs/storefront-integration.md`, `docs/admin-frontend-integration.md`, `docs/media-r2-setup.md`, `docs/deploy/admin-exposure-runbook.md`, `docs/deploy/cloudflared-admin.example.yml`, `docs/decisions/0001-headless-platform-and-merchant-template.md`, `docs/PLATFORM-ROADMAP.md`. Paths in this skill that start with `docs/`, `api/` or `Makefile` mean this platform repo; paths such as `public/`, `admin/`, `scripts/`, `catalog/` mean the sibling merchant template (or the merchant repo made from it), whose README documents `new-merchant.mjs`, `import-catalog.mjs` and `sync-core.mjs`. The design rules for later restyling are in the template's `.claude/skills/storefront-design` and `.claude/skills/admin-design`.
