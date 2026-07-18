## 1. Data model

- [x] 1.1 Add `api/internal/ent/schema/order.go`: `Order` entity (design D1/D5) — `shop_id`, `member_id` (edge to `Member`), `status`/`payment_status`/`fulfillment_status` (int16, defaults 0, CHECK constraints per design D5), `currency`, `total_amount` (int64, CHECK >= 0), `shipping_address` (jsonb, NOT NULL), `cancelled_at` (nullable timestamp), `TimeMixin`.
- [x] 1.2 Add `OrderItem` entity in the same file — `shop_id`, `order_id` (edge to `Order`, `OnDelete(Cascade)` on the assoc `.To()` side per the ent gotcha), `product_id`/`sku_id` as plain nullable int fields with **no edge/FK** (design D4), `product_title`, `sku_code`, `quantity` (int32, CHECK > 0), `price_amount` (int64, CHECK >= 0), `currency`, `TimeMixin`.
- [x] 1.3 Add indexes: `orders(shop_id, member_id)`, `orders(shop_id, status)`, `order_items(order_id)`, `order_items(shop_id)`.
- [x] 1.4 Register `Order`/`OrderItem` in `api/internal/tenant/enttenancy.go`'s `tenantOwned` map.
- [x] 1.5 Run `cd api && go generate ./...` (or the project's ent codegen command) to regenerate `api/internal/ent`.
- [x] 1.6 Run `make migrate-gen name=add_order_management` to produce the versioned migration; review the generated up/down SQL for correctness (CHECK constraints, indexes, cascade).

## 2. Order package (service layer)

- [x] 2.1 Create `api/internal/order/service.go`: `Service{Client *ent.Client}`, `ErrNotFound`, `ValidationError{Message, Details []Detail}`, `ConflictError{Message}` (design D9).
- [x] 2.2 Define status constants (`StatusCreated`/`StatusCancelled`, `PaymentStatusUnpaid`, `FulfillmentStatusUnfulfilled`) and `ShippingAddress` struct with JSON tags + validation helper (design D5/D8).
- [x] 2.3 Implement `Checkout(ctx, shopID, memberID int, addr ShippingAddress) (*ent.Order, error)` per design D3: load active cart with items/sku/product, reject empty cart, validate purchasability (sku exists/active, product published) without checking stock yet, atomically flip cart status Active→Converted (conflict on 0 rows affected), atomically decrement stock per line via `AddStockQty` + `Where(StockQtyGTE)` (design D2), roll back entire tx on any failure, compute `total_amount`, create `orders`+`order_items` with denormalized snapshots, commit.
- [x] 2.4 Implement `GetOrder(ctx, shopID, memberID, orderID int) (*ent.Order, error)` — member-owned lookup with items loaded, `ErrNotFound` on any mismatch (no existence leakage, mirrors cart's `ownedItem` convention).
- [x] 2.5 Implement `ListOrders(ctx, shopID, memberID int, params ListParams) (*OrderPage, error)` — paginated, member-scoped.
- [x] 2.6 Implement `GetOrderAdmin(ctx, shopID, orderID int) (*ent.Order, error)` and `ListOrdersAdmin(ctx, shopID int, params AdminListParams) (*OrderPage, error)` — shop-scoped only, `AdminListParams` supports optional `Status` filter (and optionally `PaymentStatus`/`FulfillmentStatus`).
- [x] 2.7 Implement `CancelOrder(ctx, shopID, orderID int, memberID *int) (*ent.Order, error)` per design D7: atomic conditional status flip requiring all three axes at initial value, `ConflictError` on mismatch (order exists but not cancellable), `ErrNotFound` if order doesn't exist / doesn't belong to shop / (when memberID != nil) doesn't belong to member; on success, restore stock for every item whose `sku_id` still resolves to a live SKU, within the same tx.
- [x] 2.8 Implement `UpdatePaymentStatus(ctx, shopID, orderID int, status int16) (*ent.Order, error)` and `UpdateFulfillmentStatus(ctx, shopID, orderID int, status int16) (*ent.Order, error)` per design D6 — tenant scoping + existence + `status >= 0` validation only, no cross-axis business rules.
- [x] 2.9 Unit tests for pure helpers (e.g. `ShippingAddress` validation, `normalizePage`-equivalent) that don't require a database.

## 3. HTTP handlers — member self-service

- [x] 3.1 Create `api/internal/httpapi/order.go`: `OrderHandler{Client *ent.Client, Service *order.Service, Log *slog.Logger}` with `writeOrderError` mapping `ValidationError`→422, `ConflictError`→409, `ErrNotFound`→404 (mirrors `writeCartError`/`writeCatalogError`).
- [x] 3.2 `MountShop(r chi.Router)`: `POST /checkout`, `GET /orders`, `GET /orders/{id}`, `POST /orders/{id}/cancel` — mounted inside the existing `MemberMW`-protected group (no RBAC, resolves `member_id` via `auth.MemberFrom`, mirrors `CartHandler`).
- [x] 3.3 JSON serialization helpers: `orderJSON`/`orderItemJSON`/`orderPageJSON` including `status`/`payment_status`/`fulfillment_status`/`total_amount`/`currency`/`shipping_address`/items.
- [x] 3.4 Checkout request body decodes into `order.ShippingAddress`; validation errors surface per-field pointers.

## 4. HTTP handlers — merchant back office

- [x] 4.1 `MountShopAdmin(r chi.Router)` on the same `OrderHandler` (or a distinct method) for `GET /orders`, `GET /orders/{id}`, `POST /orders/{id}/cancel` under `/admin/shops/{shopID}` — gated by `Authz.RequireShopPermission("order.view")` / `"order.cancel"` (mirrors `ProductsHandler.MountShop`).
- [x] 4.2 Admin list supports `status` query filter (`optionalInt16Query`, reused from `products.go`).

## 5. RBAC permission catalog & seed

- [x] 5.1 Add `order.view`/`order.cancel` to `PermissionCatalog` in `api/internal/seed/seed.go`.
- [x] 5.2 Add both nodes to `merchant_owner`'s granted permissions in `roleDefs`; add `order.view` (read-only) to `editor` (no `order.cancel`, mirroring editor's lack of `*.delete`).

## 6. Router & wiring

- [x] 6.1 Add `Orders *OrderHandler` field to `httpapi.Deps` in `router.go`.
- [x] 6.2 Mount member routes inside the existing `MemberMW` group alongside `Cart`.
- [x] 6.3 Mount admin routes inside the existing `/admin/shops/{shopID}` group alongside `Products`/`Categories`.
- [x] 6.4 In `api/internal/app/wire.go`, construct `orderService := &order.Service{Client: client}` and `deps.Orders = &httpapi.OrderHandler{Client: client, Service: orderService, Authz: authz, Log: a.log}`.

## 7. Integration tests

- [x] 7.1 Add `api/internal/httpapi/order_integration_test.go` with a test environment mirroring `cart_integration_test.go` (two shops, multiple members per shop, RBAC-enabled admin identities for the admin-side tests).
- [x] 7.2 Checkout happy path: cart with purchasable items → order created with correct denormalized snapshots, `total_amount`, three-axis initial statuses; source cart transitions to `converted` (verify a subsequent `GET /cart` starts fresh).
- [x] 7.3 Empty cart checkout rejected (422), no order created.
- [x] 7.4 Checkout rejected when a cart item's product is unpublished / SKU inactive / SKU deleted — 422, no order created, no stock touched.
- [x] 7.5 Checkout rejected when stock insufficient for one line among several — 422, no order created, unaffected lines' stock unchanged.
- [x] 7.6 **Concurrent overselling protection (mandatory)**: seed a SKU with `stock_qty = 5`, spin up 10 concurrent checkout requests (10 distinct carts/members, each buying 1 unit of the same SKU), assert exactly 5 succeed (201) and 5 fail (422), assert final `stock_qty == 0` (never negative), assert exactly 5 orders exist referencing that SKU.
- [x] 7.7 Cross-member isolation: member A cannot `GET`/`cancel` member B's order (404).
- [x] 7.8 Cross-shop isolation: shop A member/admin token cannot see or act on shop B's orders.
- [x] 7.9 Cancel restores stock: cancel an unpaid/unfulfilled order, assert stock incremented back per line, order `status` becomes cancelled.
- [x] 7.10 Cancel rejected once already cancelled (409), and (via direct service call or fixture) rejected once `payment_status`/`fulfillment_status` has moved off its initial value.
- [x] 7.11 Admin can only list/get/cancel orders within their own shop (403 cross-shop, RBAC 403 without `order.view`/`order.cancel`).
- [x] 7.12 Admin list pagination and `status` filter.
- [x] 7.13 `UpdatePaymentStatus`/`UpdateFulfillmentStatus` service-level tests: tenant scoping, `ErrNotFound` for foreign shop, rejects negative values.

## 8. Verification

- [x] 8.1 `cd api && go build ./...` passes.
- [x] 8.2 `cd api && golangci-lint run` passes.
- [x] 8.3 `make test-int` (unit + integration, including the concurrency test) passes locally.
- [x] 8.4 Manually confirm the concurrency test is deterministic (run it a few times in a loop) before considering task 7.6 done.

## 9. Archive & ship

- [x] 9.1 `openspec archive order-management --yes`.
- [ ] 9.2 Commit changes (code + archived openspec artifacts) with a message explaining why, `Co-Authored-By` trailer included.
- [ ] 9.3 Push to `origin main`.
- [ ] 9.4 Confirm CI is green (`gh run list --repo wpkc0429/ecommerce --limit 4`); fix and re-push if red, repeat until green.
