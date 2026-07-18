## Why

The platform has no loyalty mechanism: merchants cannot reward repeat purchases or segment members by spend. `shop_member.points`/`level_id` were reserved for exactly this in Phase 1 but have never been populated. This is the 9th and final proposal in the Phase 2 e-commerce core sequence — order-management, payment-integration, and shipping-logistics are all already in place and give us a clean, already-idempotent hook point (`payment_status` turning paid) to drive point issuance from.

## What Changes

- Add `member_tiers`: a shop-scoped, merchant-configurable list of loyalty tiers (`name`, `min_points` threshold, optional unused `discount_percent` reserved field). Full admin CRUD, RBAC-gated (`member_tier.view/create/edit/delete`).
- Add `point_transactions`: an append-only, shop-scoped ledger (`shop_member_id`, `order_id` nullable, `points_delta`, `reason`, `created_at`) — the source of truth for point history, mirroring the audit style of `payments`/`shipments`.
- Reuse the existing reserved `shop_member.points`/`level_id` columns as a maintained cache: `points` is incremented/decremented in lockstep with every `point_transactions` insert; `level_id` is re-evaluated against `member_tiers` after every change.
- Add a new domain event `events.OrderPaymentSucceeded`, published by `payment.Service.HandleWebhook` immediately after it advances `orders.payment_status` to paid. This reuses the existing generic `api/internal/events` dispatcher (today used only for cache invalidation) rather than a new mechanism.
- Add a new `api/internal/points` package that subscribes to `events.OrderPaymentSucceeded` and idempotently awards points (1 point per 100 minor currency units of `orders.total_amount`, integer division), then recomputes `shop_member.level_id`. This package never writes `ent.Order` and is never imported by `order`/`payment`/`shipping`.
- Add a merchant manual point-adjustment endpoint (`point.adjust` RBAC node) for customer-service use, and admin/member read endpoints for a member's balance, current tier, and paginated transaction history.
- Point clawback on return is explicitly out of scope for this change (see design.md Non-Goals) — returns leave previously-awarded points untouched.

## Capabilities

### New Capabilities
- `member-tiers-and-points`: shop-scoped member loyalty tiers and a points ledger, auto-awarded on payment success via a domain event, with merchant CRUD/manual-adjustment and member self-service read APIs.

### Modified Capabilities
(none — `payment-integration`'s existing requirements/scenarios are unchanged; it gains one additional post-commit event publish that is pure plumbing, not a behavioral change to any documented payment requirement.)

## Impact

- New ent schemas: `MemberTier`, `PointTransaction` (`api/internal/ent/schema`), registered in `api/internal/tenant.tenantOwned`.
- New package `api/internal/points` (service + errors, mirrors `order`/`payment`/`shipping.Service` conventions).
- New event type `events.OrderPaymentSucceeded` in `api/internal/events/events.go`.
- `api/internal/payment/service.go`: `Service` gains an optional `Dispatcher *events.Dispatcher` field; `HandleWebhook` publishes the new event after a successful `UpdatePaymentStatus` call.
- New `api/internal/httpapi` handler(s) for member-tiers/points admin + member routes, mounted in `router.go` alongside existing admin/member groups.
- `api/internal/seed/seed.go`: new `member_tier.*`/`point.adjust`/points-view RBAC nodes added to the permission catalog and to `merchant_owner`/`editor` role definitions.
- New migration under `api/migrations` for the two new tables.
- `api/internal/app/wire.go`: construct `points.Service`, subscribe its handler to the dispatcher, wire the new handler(s) into `httpapi.Deps`.
