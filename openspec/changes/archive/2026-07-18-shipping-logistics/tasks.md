## 1. Data model

- [x] 1.1 Add `ShippingMethod` ent schema (`api/internal/ent/schema/shipping.go`): fields `shop_id`, `name` (varchar(100), not empty), `carrier` (varchar(100), not empty), `flat_rate` (int64, >= 0), `is_active` (bool, default true), `TimeMixin`; index `(shop_id, is_active)`; CHECK `flat_rate >= 0` (design D1).
- [x] 1.2 Add `Shipment` ent schema (same file): fields `shop_id`, `order_id`, `carrier` (varchar(100), not empty), `tracking_number` (varchar(191), optional/nillable), `status` (int16), `shipped_at` (time, optional/nillable), `delivered_at` (time, optional/nillable), `TimeMixin`; one-directional required edge to `Order` (no `OnDelete` annotation — default `NO ACTION`, mirrors `Payment.order_id`, design D4); **unique index on `order_id`** (design D4 — the authoritative one-shipment-per-order guard); index `(shop_id, order_id)`; CHECK `status IN (1, 2, 3)`.
- [x] 1.3 Register `ShippingMethod` and `Shipment` in `api/internal/tenant/enttenancy.go` `tenantOwned` map.
- [x] 1.4 Run `go generate ./...` (ent codegen) inside `api/`.
- [x] 1.5 Generate migration via `make migrate-gen name=add_shipping_logistics` against the running dev Postgres; review the generated SQL for the expected tables/indexes/checks (watch for the migrategen caveat about spurious NULLS NOT DISTINCT constraint diffs — strip by hand and re-sum if the diff proposes touching them); apply via `make migrate`. (Diff proposed dropping `uq_role_user_user_role_shop`/`uq_user_permission_user_perm_shop` exactly as the payment-integration precedent warned — stripped by hand from both up/down SQL, `atlas.sum` recomputed via a throwaway `sqltool.NewGolangMigrateDir`+`migrate.WriteSumFile` script.)

## 2. Shipping service (`api/internal/shipping`)

- [x] 2.1 Define `shipping.ValidationError`/`ConflictError`/`ErrNotFound` mirroring `order`/`payment`/`catalog` conventions (design D5).
- [x] 2.2 Define `shipping.ShippedStatus/DeliveredStatus/ReturnedStatus int16 = 1/2/3` constants (design D2) — the same numeric values `order.Service.UpdateFulfillmentStatus` is called with.
- [x] 2.3 Implement `Service{Client *ent.Client; Orders *order.Service}`.
- [x] 2.4 Implement `ShippingMethod` CRUD: `CreateShippingMethod`, `GetShippingMethod`, `ListShippingMethods`, `UpdateShippingMethod`, `DeleteShippingMethod` (shop-scoped, plain ent CRUD mirroring `catalog.Service`'s category methods).
- [x] 2.5 Implement `CreateShipment(ctx, shopID, orderID int, carrier string, trackingNumber *string) (*ent.Shipment, error)`: loads order via `Orders.GetOrderAdmin` (404 passthrough), rejects cancelled order or non-unfulfilled `fulfillment_status` (409), rejects empty `carrier` (422), calls `Orders.UpdateFulfillmentStatus(ctx, shopID, orderID, ShippedStatus)`, then inserts the `shipments` row (`status=ShippedStatus`, `shipped_at=now`) — on unique-index conflict on `order_id`, return `ConflictError` (409) without rolling back the already-applied `fulfillment_status` update (design D4 — the concurrent winner's row is the source of truth).
- [x] 2.6 Implement `AdvanceShipment(ctx, shopID, orderID, shipmentID int, target int16) (*ent.Shipment, error)`: loads the shipment scoped by `shopID`+`orderID` (404 if absent/foreign), rejects (409) unless current `status == ShippedStatus` and `target` is `DeliveredStatus` or `ReturnedStatus`, updates `status` (+`delivered_at` when target is delivered), then calls `Orders.UpdateFulfillmentStatus(ctx, shopID, orderID, target)` (design D3).
- [x] 2.7 Implement `ListShipmentsAdmin(ctx, shopID, orderID int) ([]*ent.Shipment, error)`: verifies order exists in shop via `Orders.GetOrderAdmin` (404 passthrough), returns shipments for that order (0 or 1 row).
- [x] 2.8 Implement `GetShipmentForMember(ctx, shopID, memberID, orderID int) (*ent.Shipment, error)`: verifies order ownership via `Orders.GetOrder` (404 passthrough on not-found/not-owned), returns `ErrNotFound` if no shipment row exists yet.
- [x] 2.9 Service-level integration tests (`api/internal/shipping/service_integration_test.go`, direct DB fixtures, no HTTP layer): create-shipment advances `fulfillment_status`; create on a cancelled order is rejected; create on an already-shipped order is rejected; delivered/returned transitions from shipped succeed and advance `fulfillment_status` to the matching value; advancing a non-shipped or already-terminal shipment is rejected; cross-member `GetShipmentForMember` returns `ErrNotFound`.

## 3. HTTP layer

- [x] 3.1 Add `ShippingHandler` (`api/internal/httpapi/shipping.go`) mirroring `PaymentHandler`'s/`CategoriesHandler`'s structure: `Client`, `Service *shipping.Service`, `Authz *AuthzMW`, `Log`.
- [x] 3.2 `MountShopAdmin(r chi.Router)`: shipping_methods CRUD (`shipping_method.view/create/edit/delete`) at `/shipping-methods` and `/shipping-methods/{shippingMethodID}`; `POST /orders/{orderID}/shipments` (`shipment.create`); `PUT /orders/{orderID}/shipments/{shipmentID}` (`shipment.update`); `GET /orders/{orderID}/shipments` (`shipment.view`).
- [x] 3.3 `MountShop(r chi.Router)`: `GET /orders/{orderID}/shipment` (member-authenticated, mounted in the existing `mr` MemberMW group alongside Orders/Payments; no RBAC).
- [x] 3.4 Wire `writeShippingError` mirroring `writeOrderError`/`writePaymentError`.
- [x] 3.5 JSON serialization helpers: `shippingMethodJSON(sm *ent.ShippingMethod)`, `shipmentJSON(s *ent.Shipment)`.
- [x] 3.6 Add `Shipping *ShippingHandler` to `httpapi.Deps`; mount `Shipping.MountShopAdmin` in `/admin/shops/{shopID}`, `Shipping.MountShop` in the `/shop` MemberMW group, in `router.go`.

## 4. RBAC + seed

- [x] 4.1 Add `shipping_method.view`/`shipping_method.create`/`shipping_method.edit`/`shipping_method.delete` and `shipment.view`/`shipment.create`/`shipment.update` to `seed.PermissionCatalog`.
- [x] 4.2 Grant all seven new nodes to `merchant_owner`; grant `shipping_method.view/create/edit`, `shipment.view/create/update` to `editor` (mirrors the existing `category.*`/`order.*`/`payment.*` breadth split between the two roles).

## 5. Wiring

- [x] 5.1 Wire `shipping.Service{Client: client, Orders: orderService}` + `ShippingHandler` in `api/internal/app/wire.go` (design D9 — no new config values needed).

## 6. Integration tests (`api/internal/httpapi/shipping_integration_test.go`)

- [x] 6.1 Test env setup mirroring `orderEnv`/`paymentEnv` (shops A/B, members, RBAC roles with/without the new permission nodes).
- [x] 6.2 shipping_methods CRUD happy path + RBAC checks (403 without permission, 403 cross-shop) + basic validation (empty `name`/`carrier` rejected, negative `flat_rate` rejected).
- [x] 6.3 End-to-end shipment flow: merchant creates a shipment on a member's unfulfilled order → order `fulfillment_status` becomes 1 → merchant advances to delivered → order `fulfillment_status` becomes 2 → member's self-service GET reflects the final state.
- [x] 6.4 Illegal transitions rejected: create shipment on a cancelled order (409); create a second shipment on an already-shipped order (409); advance a shipment straight to delivered without it ever existing in shipped state is impossible by construction — instead test advancing an already-delivered shipment again (409) and advancing an already-returned shipment to delivered (409).
- [x] 6.5 Cross-shop isolation: admin without `shipment.view`/`shipment.create`/`shipment.update`/`shipping_method.*` gets 403; admin from shop B gets 403 operating on shop A's orders/shipping methods.
- [x] 6.6 Cross-member isolation: member A's self-service GET on member B's order shipment returns 404; member's self-service GET on their own order with no shipment yet returns 404.
- [x] 6.7 RBAC role coverage: `merchant_owner`/`editor` seeded roles can perform their granted operations (smoke test against the seed catalog, mirrors existing `order`/`payment` RBAC smoke tests if present).

## 7. Verification

- [x] 7.1 `cd api && go build ./...` passes.
- [x] 7.2 `cd api && golangci-lint run` passes (or repo's lint command if different).
- [x] 7.3 `cd api && INTEGRATION=1 go test -p 1 -count=1 ./...` passes (full suite, including new shipping tests and unaffected existing packages — in particular re-run `./internal/order/...` unmodified to confirm the checkout/-race path was not touched).
- [x] 7.4 Confirm no code path outside `order.Service` writes `orders.fulfillment_status` (grep for direct `ent.Order` mutation of that field in the new `shipping`/`httpapi` code).
- [x] 7.5 Update `openspec/specs/order-management/spec.md`'s cross-references only if archiving reveals drift (expected: no change needed — `fulfillment_status` enumeration is finalized in the new `shipping-logistics` spec, not by modifying order-management's existing requirement text). Confirmed: order-management's "Order three-axis status model" requirement only asserts the initial value 0 and the controlled-entry-point contract, neither of which this change alters — no drift, no edit needed.
