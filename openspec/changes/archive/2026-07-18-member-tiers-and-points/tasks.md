## 1. Data model

- [x] 1.1 Add `MemberTier` ent schema (`api/internal/ent/schema/points.go`): `shop_id`, `name` (required), `min_points` (int32, >= 0 check), `discount_percent` (int16, optional/nillable, 0–100 check), `TimeMixin`, index `(shop_id, min_points)`, table annotation `member_tiers`.
- [x] 1.2 Add `PointTransaction` ent schema (same file): `shop_id`, `shop_member_id` (required FK edge to `ShopMember`), `order_id` (optional/nillable FK edge to `Order`), `points_delta` (int32), `kind` (int16, check IN (0,1,2)), `reason` (string, required), `TimeMixin`; unique index `(order_id, kind)`; index `(shop_id, shop_member_id)`; table annotation `point_transactions`.
- [x] 1.3 Extend `ShopMember.Edges()` in `api/internal/ent/schema/account.go` with `edge.To("member_tier", MemberTier.Type).Unique().Field("level_id")` annotated `entsql.OnDelete(entsql.SetNull)`; update its doc comment (no longer "reserved for Phase 2"). (`level_id` also changed from `field.Int32` to `field.Int` — required to match `MemberTier`'s bigint id column; ent rejects a type mismatch on a `Field()`-bound edge.)
- [x] 1.4 `go generate ./internal/ent` from `api/`; confirm generated code compiles (`go build ./...`).
- [x] 1.5 Register `MemberTier` and `PointTransaction` as `true` in `tenant.tenantOwned` (`api/internal/tenant/enttenancy.go`), with a comment matching the existing style.
- [x] 1.6 `make migrate-gen name=add_member_tiers_and_points`; review the generated SQL — confirm the `(order_id, kind)` unique index is NOT `NULLS NOT DISTINCT` (default Postgres behavior, required by design D4) and the `member_tier` FK on `shop_member.level_id` is `ON DELETE SET NULL`. Hand-fix only if the diff got either wrong. (Hand-fixed: removed erroneous DROP/re-ADD of the handwritten `uq_role_user_user_role_shop`/`uq_user_permission_user_perm_shop` NULLS NOT DISTINCT constraints per migrategen's documented caveat; recomputed `atlas.sum` for the edited file via a throwaway `migrate.WriteSumFile` script.)
- [x] 1.7 `make migrate` against the dev DB; confirm `make migrate-down` cleanly rolls back, then re-apply.

## 2. Domain events

- [x] 2.1 Add `events.OrderPaymentSucceeded{ShopID, OrderID, MemberID int; TotalAmount int64; Currency string}` and `events.OrderReturned{ShopID, OrderID, MemberID int}` to `api/internal/events/events.go`, each with an `EventName()` method, following the existing event type conventions.
- [x] 2.2 Add optional `Dispatcher *events.Dispatcher` field to `payment.Service` (`api/internal/payment/service.go`); after the existing successful `s.Orders.UpdatePaymentStatus(...)` call inside `HandleWebhook`, publish `events.OrderPaymentSucceeded` using the returned order's fields (nil-check the dispatcher first).
- [x] 2.3 Add optional `Dispatcher *events.Dispatcher` field to `shipping.Service` (`api/internal/shipping/service.go`); after the existing successful `s.Orders.UpdateFulfillmentStatus(..., ReturnedStatus)` call inside `AdvanceShipment`, publish `events.OrderReturned` using the returned order's fields (nil-check the dispatcher first). Do not publish for the `delivered` transition.
- [x] 2.4 Confirm every existing `payment`/`shipping` package test still compiles and passes without setting the new `Dispatcher` field (nil-safety check). (`go test ./internal/payment/... ./internal/shipping/...` green.)

## 3. points package — core service

- [x] 3.1 Create `api/internal/points/service.go`: `Service{ Client *ent.Client }`, `ErrNotFound`, `ValidationError`/`Detail` (mirror `order`/`payment`/`shipping`'s error envelope convention).
- [x] 3.2 Implement tier CRUD methods: `CreateMemberTier`, `GetMemberTier`, `ListMemberTiers`, `UpdateMemberTier`, `DeleteMemberTier` — shop-scoped, validate `name` non-empty, `min_points >= 0`, `discount_percent` 0–100 if present.
- [x] 3.3 Implement `recomputeLevel(ctx, tx, shopID, shopMemberID, newBalance) error` helper: query `member_tiers` for shopID ordered by `min_points` desc, find first `min_points <= newBalance`, `SetLevelID`/`ClearLevelID` on the `shop_member` row — used by every points-changing path.
- [x] 3.4 Implement `HandleOrderPaid(ctx, ev events.OrderPaymentSucceeded)`: compute `points = TotalAmount / 100`; open an `ent.Tx`; pre-check + attempt insert of a `kind=0` `point_transactions` row (`order_id = ev.OrderID`, `points_delta = points`, `reason = "訂單付款回饋"`); on `ent.IsConstraintError` (unique `(order_id, kind)` collision) roll back and return (idempotent no-op); on success, resolve the `shop_member` row by `(shop_id, member_id)`, `AddPoints(points)`, call `recomputeLevel`, commit.
- [x] 3.5 Implement `HandleOrderReturned(ctx, ev events.OrderReturned)`: open a tx; look up the `kind=0` row for `order_id = ev.OrderID` to get `awarded` (0 if none — unique index guarantees at most one); look up the `shop_member` row, `clawback := min(awarded, currentPoints)`; pre-check + attempt insert of a `kind=1` row (`points_delta = -clawback`, `reason = "訂單退貨扣回"`); on unique-constraint collision, roll back and no-op; on success, `AddPoints(-clawback)`, `recomputeLevel`, commit.
- [x] 3.6 Implement `Handle(ctx context.Context, e events.Event)`: type-switches on `events.OrderPaymentSucceeded`/`events.OrderReturned`, calls the corresponding method, logs+swallows any returned error (no error return on `Handle` itself — mirror `render.Invalidator.Handle`'s signature). Needs a `Log *slog.Logger` field on `Service` for this.
- [x] 3.7 Implement `AdjustPoints(ctx, shopID, shopMemberID int, delta int32, reason string) (*ent.ShopMember, error)`: validate `delta != 0` and `reason` non-empty; open a tx; look up the `shop_member` row scoped by shop; reject (`ValidationError`) if `current + delta < 0`; insert a `kind=2` row (`order_id` nil); `AddPoints(delta)`; `recomputeLevel`; commit.
- [x] 3.8 Implement read methods: `GetMemberPointsAdmin`/`GetMemberPointsSelf` (balance + tier), `ListTransactionsAdmin`/`ListTransactionsSelf` (paginated, newest first) — self variants resolve `shop_member_id` from `(shopID, memberID)` internally, never accept a caller-supplied `shop_member_id`.

## 4. Seed / RBAC

- [x] 4.1 Add `member_tier.view/create/edit/delete`, `point.view`, `point.adjust` to `seed.PermissionCatalog` (`api/internal/seed/seed.go`), with Chinese descriptions matching the existing style.
- [x] 4.2 Add the new nodes to `merchant_owner`'s and `editor`'s `Perms` in `roleDefs`, mirroring exactly how `shipping_method.*`/`shipment.*` were added.

## 5. HTTP layer

- [x] 5.1 Create `api/internal/httpapi/points.go`: `PointsHandler{ Client *ent.Client; Service *points.Service; Authz *AuthzMW; Log *slog.Logger }`, JSON serialization helpers for tier/transaction/balance payloads, `writePointsError` mapping (mirror `writeShippingError`).
- [x] 5.2 `MountShopAdmin(r chi.Router)`: `member-tiers` CRUD gated by `member_tier.view/create/edit/delete`; `GET .../members/{shopMemberID}/points` and `GET .../members/{shopMemberID}/points/transactions` gated by `point.view`; `POST .../members/{shopMemberID}/points/adjust` gated by `point.adjust`.
- [x] 5.3 `MountShop(r chi.Router)`: `GET /points`, `GET /points/transactions` — member self-service, resolve `member_id` from JWT via existing `memberFrom(r)` helper, no RBAC.
- [x] 5.4 Add `Points *PointsHandler` to `httpapi.Deps` (`router.go`); mount `MountShopAdmin` inside the existing `/admin/shops/{shopID}` group and `MountShop` inside the existing member `/shop` group, matching exactly how `Shipping`/`Payments`/`Orders` are mounted today.

## 6. Wiring

- [x] 6.1 In `api/internal/app/wire.go`: construct `pointsService := &points.Service{Client: client, Log: a.log}`; `dispatcher.Subscribe(pointsService.Handle)` (after the dispatcher is constructed, alongside the existing `invalidator.Handle` subscription).
- [x] 6.2 Pass the shared `dispatcher` into `paymentService.Dispatcher` and `shippingService.Dispatcher` when constructing those services.
- [x] 6.3 Construct `deps.Points = &httpapi.PointsHandler{Client: client, Service: pointsService, Authz: authz, Log: a.log}`.
- [x] 6.4 `go build ./...` from `api/` to confirm the whole wiring compiles.

## 7. Tests

- [x] 7.1 `points` package unit/integration tests (mirror `shipping`/`payment` test files): award on `HandleOrderPaid` computes the correct point total and updates `shop_member.points`/`level_id`; duplicate `HandleOrderPaid` call for the same `order_id` does not double-award (idempotency via the unique index path); tier threshold crossing updates `level_id` correctly in both directions (up and down); `HandleOrderReturned` claws back capped at current balance and is idempotent; `AdjustPoints` rejects a would-go-negative delta and succeeds otherwise; cross-shop and cross-member isolation on every read/write method. (`api/internal/points/service_integration_test.go`, 15 tests, all green.)
- [x] 7.2 `payment` package test: `HandleWebhook`'s successful path publishes `events.OrderPaymentSucceeded` exactly reflecting the order's fields (use a stub dispatcher/handler to capture the event); repeated webhook delivery publishes the event again (documenting D4's premise) but does not break anything when no dispatcher is set.
- [x] 7.3 `shipping` package test: `AdvanceShipment` to `returned` publishes `events.OrderReturned`; to `delivered` does not publish it.
- [x] 7.4 `httpapi` integration tests for `PointsHandler`: merchant RBAC enforcement (403 without permission, 403 cross-shop, 404 unknown member) on tier CRUD, balance/ledger view, and adjust endpoints; member self-service endpoints return only the caller's own data and reject cross-shop tokens (401 on audience mismatch, mirrors existing order/shipment member tests). (`api/internal/httpapi/points_integration_test.go`.)
- [x] 7.5 End-to-end integration test: seed a shop + member + tiers, drive a real checkout → payment webhook success → assert points awarded and level upgraded, then drive a shipment → returned transition → assert points clawed back and level re-evaluated downward. (`TestPointsCrossesTierThresholdUpgradesLevel`, `TestPointsClawbackOnReturnEndToEnd`.)
- [x] 7.6 `go build ./... && golangci-lint run` and `INTEGRATION=1 go test -p 1 -count=1 ./...` all green from `api/`. (`golangci-lint run`: 0 issues; full suite: all packages `ok`, including 15 new `points` tests, 3 new `payment` event-publish tests, 3 new `shipping` event-publish tests, and 14 new `httpapi` points tests.)

## 8. Docs / cleanup

- [x] 8.1 Update `CLAUDE.md` only if a build/lint/test command actually changed (expected: no changes needed). (No new make targets, env vars, or commands introduced — CLAUDE.md left untouched.)
- [x] 8.2 Re-read the full diff for stray debug code, TODOs, or leftover scaffolding before archiving.
