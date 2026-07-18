## Context

`shop_member` (`api/internal/ent/schema/account.go`) has carried two unused columns since Phase 1: `points INT DEFAULT 0` and `level_id INT NULL`, explicitly reserved for "Phase 2 loyalty features". This change is that feature.

Order state is tracked across three independent axes on `orders`: `status` (created/cancelled), `payment_status` (owned by payment-integration; `0` unpaid → `1` paid is the only transition this phase performs), `fulfillment_status` (owned by shipping-logistics; `0` unfulfilled → `1` shipped → `2` delivered / `3` returned). Both status axes are written exclusively through `order.Service.UpdatePaymentStatus`/`UpdateFulfillmentStatus` — no other package may write `ent.Order` directly, and this change does not touch that rule.

`api/internal/events` is an existing generic, synchronous, post-commit domain-event dispatcher (`Event interface{ EventName() string }`, `Dispatcher.Subscribe(Handler)`, `Dispatcher.Publish(ctx, evs...)`, panics recovered+logged). Today its only consumer is `render.Invalidator` (cache invalidation), and its type doc comment describes it that way, but the `Event`/`Dispatcher` types themselves carry no cache-specific assumptions — `Publish` takes any `Event`, `Subscribe` takes any `Handler`. `cms.Service`/`httpapi/sites.go` already establish the pattern of a domain service holding an optional `Dispatcher *events.Dispatcher` field and calling `Publish` right after a successful write.

## Goals / Non-Goals

**Goals:**
- Award loyalty points automatically when an order's payment succeeds.
- Maintain a shop-scoped, merchant-configurable tier ladder (`member_tiers`) and keep `shop_member.level_id` in sync with the member's points balance.
- Keep a complete, append-only audit ledger (`point_transactions`) as the source of truth, with `shop_member.points` as a maintained read cache.
- Claw back points when a paid order is later returned, without ever driving a member's balance negative.
- Give merchants a manual adjustment tool (customer-service compensation) and both merchant and member-facing read APIs.
- Wire all of this without `order`/`payment`/`shipping` importing the new `points` package — points only ever *reacts* to their state transitions.

**Non-Goals:**
- No point redemption/spending mechanism (no checkout discount, no "pay with points") — this phase only earns and claws back points. `discount_percent` on `member_tiers` is a reserved, unused field for a future redemption/pricing feature.
- No point expiry.
- No point transfer between members.
- No per-shop configurable award formula — the 1-point-per-100-minor-units ratio is a single hardcoded platform-wide constant.
- No handling of `orders.payment_status` values `2`/`3` (refunded/partial-refund) — payment-integration's own spec states no code path in this phase ever writes those values, so there is nothing for this change to react to there.
- No backfill/rebuild-cache-from-ledger admin tool — `shop_member.points`/`level_id` are only ever touched by this package's own write paths, so no reconciliation tool is needed yet.

## Decisions

### D1 — Integrate via the existing events dispatcher, not a direct import

**Investigated**: `api/internal/events/events.go` in full, plus every existing publisher (`cms.Service`, `httpapi/sites.go`) and the one subscriber (`render.Invalidator`, wired in `app/wire.go`). Conclusion: the mechanism is generic — `Event` is a one-method marker interface, `Dispatcher.Publish` has no cache-specific behavior, and `Handler` is `func(ctx, Event)`. Its doc comment frames it around cache invalidation only because that has been its sole use case so far, not because of any structural constraint.

**Decision**: add `events.OrderPaymentSucceeded{ShopID, OrderID, MemberID, TotalAmount, Currency}` and `events.OrderReturned{ShopID, OrderID, MemberID}`. `payment.Service` and `shipping.Service` each gain an optional `Dispatcher *events.Dispatcher` field (nil-safe — `if s.Dispatcher != nil { s.Dispatcher.Publish(...) }`, so every existing test that constructs these services without a dispatcher keeps compiling and passing unchanged). `payment.Service.HandleWebhook` publishes `OrderPaymentSucceeded` right after a successful `Orders.UpdatePaymentStatus` call; `shipping.Service.AdvanceShipment` publishes `OrderReturned` right after a successful `Orders.UpdateFulfillmentStatus(..., ReturnedStatus)` call. Both calls already return the updated `*ent.Order`, which carries every field the event needs — no extra query.

`api/internal/points.Service` subscribes a `Handle(ctx, events.Event)` method to the dispatcher in `wire.go`, exactly like `render.Invalidator.Handle` is subscribed today. `points.Service` depends on `*ent.Client` only — it never imports `order`/`payment`/`shipping`, and nothing in those packages imports `points`.

**Alternative considered**: give `points.Service` a public method and have `payment.Service`/`shipping.Service` call it directly after their status updates (the fallback the parent task allowed). Rejected because the events mechanism is a proven, already-adopted pattern in this codebase for exactly this shape of problem ("service A's write should trigger service B's reaction without A knowing B exists"), and reusing it keeps the dependency graph identical to today's (nothing new imports `points`; `points` imports nothing domain-specific).

### D2 — `member_tiers.min_points` compares against the current cached balance, not lifetime-earned points

This phase has no redemption/spending feature (see Non-Goals), so every point-decreasing operation is either a return clawback (bounded by what was actually awarded) or a merchant manual deduction (rare, deliberate). Under this feature set, "current balance" and "lifetime earned" are numerically identical in the common case and diverge only after a clawback or manual deduction — exactly the cases where re-evaluating a member's tier *downward* is the correct, intuitive behavior (a member who returned everything they bought should not keep a tier they no longer qualify for). Comparing against the cheap, already-maintained `shop_member.points` column also avoids a `SUM(points_delta)` scan over the ledger on every tier recompute.

**Known trade-off**: if point redemption/spending is added in a future phase, spending points would also demote a member's tier under this semantic — a real product decision, but out of scope here and cheap to revisit later (switching to lifetime-earned only requires filtering the recompute's input to `SUM(points_delta) WHERE points_delta > 0` instead of reading `shop_member.points`).

### D3 — Award formula: `points = total_amount / 100` (integer division, floor), platform-wide constant

Mirrors the parent task's suggested default and keeps the formula trivially auditable. `total_amount` is already in minor currency units (product-catalog design D1 convention), so this reads as "1 point per 100 minor units spent" for every shop, every currency, uniformly. A `$0` order yields `0` points — the award transaction is still recorded (see D4) so the idempotency guard has something to key off, but it is a true no-op on the member's balance.

### D4 — Idempotency via a DB unique index, not a bare SELECT-then-INSERT

`point_transactions` gains a `kind` column (`0` = order award, `1` = order-return clawback, `2` = manual adjustment) alongside the human-readable `reason` text the data model calls for. A unique index on `(order_id, kind)` is the actual idempotency arbiter — mirrors the existing convention of `payments(provider, provider_reference)` and `shipments(order_id)` being the final concurrency guard rather than an application-level read-then-write check. Because `order_id` is nullable and Postgres treats each `NULL` as distinct in a standard (non-`NULLS NOT DISTINCT`) unique index, manual adjustments (`order_id = NULL`, `kind = 2`) are never constrained by this index — only award/clawback rows, which always carry a real `order_id`, are.

This matters in practice, not just in theory: `payment.Service.HandleWebhook`'s existing re-assertion branch (`if target == StatusSucceeded && p.Status == StatusSucceeded { ... }`) runs on **every** repeated successful webhook delivery for an already-succeeded payment, by design (see its doc comment) — so `OrderPaymentSucceeded` really is published more than once for the same order under duplicate delivery, not just hypothetically. `points.Service.HandleOrderPaid` pre-checks existence (cheap early-exit for the common case) and then attempts the insert; a unique-constraint violation (`ent.IsConstraintError`) is treated as "another delivery already won" and swallowed as a no-op — same pattern `shipping.Service.CreateShipment` already uses against its own `shipments.order_id` unique index race.

### D5 — Return clawback is implemented, capped at the current balance

The parent task explicitly left the shipment-`returned` transition as the natural clawback trigger and permitted scoping it out if too complex; it turned out not to be. `shipping.Service.AdvanceShipment` already performs a single terminal CAS transition (`shipped → returned`) exactly once — unlike the payment webhook, there is no re-assertion branch here, so `OrderReturned` is a genuinely single-delivery event in practice. Still, the same `(order_id, kind=1)` unique index guards it defensively.

On `OrderReturned`, `points.Service`:
1. Looks up the order's award row (`order_id`, `kind = 0`) to find how many points were originally granted (`0` if the order was never paid — shipping-logistics allows shipping an unpaid order, e.g. COD, so a return with no prior award is a valid, harmless case).
2. Computes `clawback = min(originally_awarded, shop_member.points)` — never more than the member currently holds, so the balance can never go negative from a clawback.
3. Inserts a `kind = 1` row with `points_delta = -clawback` (possibly `0`, e.g. if the member already spent/lost the points some other way — recorded anyway so the unique index keeps guarding against reprocessing) and decrements `shop_member.points` by the same amount, then re-evaluates `level_id`.

**Known limitation**: if a future phase adds point spending/redemption and a member spends points before returning the order, the clawback is capped and will not "claw back" what was already spent — the shop simply eats that difference. This is the deliberate, documented boundary the parent task asked for ("避免點數變成不合理的負數"), not an oversight.

### D6 — `shop_member.points`/`level_id` are a maintained cache, updated transactionally with the ledger insert

Every points-changing operation (award, clawback, manual adjustment) runs inside a single `ent.Tx`: insert the `point_transactions` row, `AddPoints(delta)` on the `shop_member` row, recompute and set/clear `level_id`, commit. This mirrors `order.Service.Checkout`/`CancelOrder`'s existing convention of wrapping a multi-statement invariant-preserving sequence in one transaction (a stricter guarantee than `payment.Service.HandleWebhook`'s own two-separate-statements pattern, which this change does not need to match since it is new code, not an existing convention to preserve).

### D7 — Level recompute: highest-qualifying tier wins, no tier qualifies ⇒ `level_id` cleared

After any balance change, query `member_tiers` for this shop ordered by `min_points DESC`, take the first row with `min_points <= new_balance`. If none qualifies (including the common case of no tiers configured yet), `level_id` is cleared to `NULL` — a member is never left pinned to a tier they no longer qualify for. `ShopMember.Edges()` gains `edge.To("member_tier", MemberTier.Type).Unique().Field("level_id")` with `entsql.OnDelete(entsql.SetNull)`: deleting a `member_tiers` row a member currently holds clears their `level_id` rather than blocking the delete (no reassignment busywork forced on the merchant) or cascading (which would be destructive to the wrong table).

### D8 — RBAC node naming

- `member_tier.view` / `member_tier.create` / `member_tier.edit` / `member_tier.delete` — tier *configuration* CRUD, named exactly like `shipping_method.*`.
- `point.view` — read a member's balance/tier/ledger from the back office. A new node rather than reusing `member_tier.view`, because viewing a *member's* point data is a different resource than editing tier *configuration* — same reasoning that keeps `payment.view` separate from `shipping_method.view`. Named `point.<verb>` to match the existing singular-noun convention (`order.view`, `payment.view`, `shipment.view`).
- `point.adjust` — the manual adjustment endpoint.

Both new nodes are added to `merchant_owner` and `editor` in `seed.go`'s `roleDefs`, following exactly how `shipping_method.*`/`shipment.*` were added for those two roles in shipping-logistics.

### D9 — Award timing: on payment success, not on delivery

Awarding at `payment_status → paid` (not waiting for `fulfillment_status → delivered`) gives members immediate feedback tied to a webhook-driven transition the system already treats as authoritative and (mostly) idempotent, and avoids a confusing UX where a member who has paid sees a zero balance for the days/weeks until delivery. The risk this trades in — awarding for an order that is later returned — is covered by D5's clawback. Waiting for delivery was considered (the parent task raised it explicitly) and rejected: it does not eliminate the need for clawback logic (an order can still be returned after delivery) while adding real UX latency for the common non-returned case.

### D10 — Manual adjustment negativity guard

Unlike clawback (D5, always succeeds, capped), a manual adjustment is a direct merchant action and is rejected outright (422 `ValidationError`) if `current_balance + points_delta < 0` — the merchant must choose a smaller deduction rather than have it silently clamped, since a manual adjustment's exact magnitude is presumably meaningful to whatever customer-service process is driving it.

## Risks / Trade-offs

- **[Risk]** `points.Service.Handle`'s signature (`func(ctx, events.Event)`) has no error return — a transient DB failure while awarding/clawing back points is only logged, not retried, and the triggering payment/shipment write has already committed regardless. → **Mitigation**: this is the same limitation `render.Invalidator` already has today (cache invalidation can silently fail too); no new failure mode is introduced. Accepted as consistent with the established pattern; a retry/DLQ mechanism is out of scope for this phase.
- **[Risk]** `payment.Service`/`shipping.Service` each gain a new field; any call site constructing them with a struct literal that doesn't set `Dispatcher` continues to compile (Go zero-value nil), and `Publish` is skipped — but this means points silently doesn't get awarded/clawed back if the running binary forgets to wire the dispatcher. → **Mitigation**: `app/wire.go` is the single construction site in production code; it is updated as part of this change, and every test that cares about points wiring constructs the dispatcher explicitly.
- **[Trade-off]** Clawback amount is capped at the current balance (D5) rather than ever going negative — a member who spends/loses awarded points through some future mechanism before returning the order keeps the "debt" uncollected. Accepted and documented; matches the parent task's explicit guidance.
- **[Trade-off]** `min_points` semantics (D2) are current-balance-based; revisit if/when point redemption ships.

## Migration Plan

1. Add `MemberTier`/`PointTransaction` ent schemas; extend `ShopMember.Edges()` with the new `member_tier` edge.
2. `go generate ./internal/ent`, then `make migrate-gen name=add_member_tiers_and_points`; review the generated SQL (the `(order_id, kind)` unique index must not be `NULLS NOT DISTINCT` — the default ent/Postgres behavior is exactly what D4 relies on, so no handwritten migration override should be needed here, unlike the RBAC tables' `NULLS NOT DISTINCT` case).
3. Register `MemberTier`/`PointTransaction` in `tenant.tenantOwned`.
4. Apply via `make migrate` in CI/dev as usual — purely additive (two new tables, one new nullable FK edge on an existing table), no backfill needed since `points`/`level_id` already default to `0`/`NULL`.

Rollback: `make migrate-down` drops the new tables/constraint; `shop_member.points`/`level_id` revert to their pre-existing (already-there, unused) shape.

## Open Questions

None blocking. Point redemption/spending is explicitly deferred to a future phase (see Non-Goals).
