## 1. Provider abstraction + mock provider

- [x] 1.1 Create `api/internal/payment` package skeleton with `Provider` interface, `InitiateRequest`/`InitiateResult`/`WebhookRequest`/`WebhookResult`/`Outcome` types (design D1).
- [x] 1.2 Implement `MockProvider` (`mockprovider.go`): `InitiatePayment` generates `mock_<uuid>` reference + fake redirect URL; `VerifyWebhook` does real HMAC-SHA256 verification via `hmac.Equal` against `X-Payment-Signature` header, parses `{provider_reference, status}` body (design D2).
- [x] 1.3 Export `SignMockWebhook(secret string, body []byte) string` test helper.
- [x] 1.4 Unit tests (`mockprovider_test.go`, no DB required): missing signature rejected, wrong signature rejected, correct signature accepted for both succeeded/failed status, malformed JSON body rejected, unknown status value rejected.

## 2. Data model

- [x] 2.1 Add `Payment` ent schema (`api/internal/ent/schema/payment.go`): fields `shop_id`, `order_id`, `provider`, `provider_reference`, `amount` (int64), `currency` (char(3)), `status` (int16, default 0), `TimeMixin`; one-directional required edge to `Order` (no `OnDelete` annotation — default `NO ACTION`, design D3); unique index on `(provider, provider_reference)`; index on `(shop_id, order_id)`; CHECK `amount > 0` and `status IN (0,1,2,3)`.
- [x] 2.2 Register `Payment` in `api/internal/tenant/enttenancy.go` `tenantOwned` map.
- [x] 2.3 Run `go generate ./...` (ent codegen) inside `api/`.
- [x] 2.4 Generate migration via `make migrate-gen name=add_payment_integration`; review generated SQL for the expected table/indexes/checks; apply via `make migrate`. (Hand-stripped the spurious `uq_role_user_user_role_shop`/`uq_user_permission_user_perm_shop` DROP/ADD CONSTRAINT statements the Atlas diff proposed — per migrategen's documented caveat, those NULLS NOT DISTINCT constraints aren't representable in ent schema and must never round-trip through a diff; rewrote `migrations/atlas.sum` to match the hand-edited file via `dir.Checksum()`/`migrate.WriteSumFile`.)

## 3. Payment service

- [x] 3.1 Define `payment.ValidationError`/`ConflictError`/`ErrNotFound` mirroring `order`/`cart`/`catalog` conventions (design D10).
- [x] 3.2 Define `payment.OrderPaymentStatusPaid`/`OrderPaymentStatusRefunded`/`OrderPaymentStatusPartiallyRefunded int16` constants (design D4) and `payment.Status{Pending,Succeeded,Failed,Refunded}` constants (design D5).
- [x] 3.3 Implement `Service{Client *ent.Client; Orders *order.Service; Providers map[string]Provider; DefaultProvider string}`.
- [x] 3.4 Implement `InitiatePayment(ctx, shopID, memberID, orderID int, providerName string) (*ent.Payment, *InitiateResult, error)`: loads order via `Orders.GetOrder` (member ownership check, 404 passthrough), rejects unknown provider name (422), rejects cancelled order or non-unpaid `payment_status` (409), calls `provider.InitiatePayment` with order's amount/currency snapshot, creates a `pending` `Payment` row with `amount = order.TotalAmount`.
- [x] 3.5 Implement `HandleWebhook(ctx, providerName string, result WebhookResult) (*ent.Payment, error)`: look up payment by `(provider, provider_reference)` (`ErrNotFound` if absent, no tenant scoping since ctx may have none — design D7), CAS `pending -> target` via conditional `Update().Where(id, status=pending)`, on win call `Orders.UpdatePaymentStatus` only when target is succeeded, on loss re-fetch and defensively re-call `Orders.UpdatePaymentStatus` only when the *current* status is succeeded and target is succeeded (design D6 idempotency algorithm — implement exactly as specified, this is the core correctness requirement of the whole change).
- [x] 3.6 Implement `ListForOrderAdmin(ctx, shopID, orderID int) ([]*ent.Payment, error)`: verifies order exists in shop via `Orders.GetOrderAdmin` (404 passthrough), returns payments ordered newest first.
- [x] 3.7 Unit/integration tests for `HandleWebhook`'s CAS/idempotency algorithm using a fake in-memory `order.Service`-compatible seam or the real DB-backed services (whichever is simpler given `order.Service` needs an `*ent.Client`) — cover: fresh succeeded webhook updates order once; fresh failed webhook does not touch order; duplicate succeeded webhook is a safe no-op that leaves order paid; a failed-then-succeeded conflicting sequence never marks the order paid from the second call once already terminal-failed (per design D6, only re-assert when current status already equals target). (`internal/payment/service_integration_test.go`, service-level, direct DB fixtures — complements the httpapi black-box tests in section 7.)

## 4. HTTP layer

- [x] 4.1 Add `PaymentHandler` (`api/internal/httpapi/payment.go`) mirroring `OrderHandler`'s structure: `Client`, `Service *payment.Service`, `Authz *AuthzMW`, `Log`.
- [x] 4.2 `MountShop(r chi.Router)`: `POST /orders/{orderID}/payments` (member-authenticated, mounted in the existing `mr` MemberMW group alongside Orders).
- [x] 4.3 `MountShopAdmin(r chi.Router)`: `GET /orders/{orderID}/payments` gated by `RequireShopPermission("payment.view")`.
- [x] 4.4 `Webhook(w, r)` handler: reads raw body via `http.MaxBytesReader` (do not use the shared `decodeJSON` helper — signature verification needs the exact raw bytes, design D1), resolves `{provider}` chi URL param against `Service.Providers` (404 if unknown), calls `provider.VerifyWebhook` (401 on `ErrInvalidSignature`, 400 on malformed body), then `Service.HandleWebhook`, returns 200 on success/no-op, maps `ErrNotFound` to a 200 no-op response per design D7 (unknown reference is not an error to the caller — do not leak information, but do not 500 either) — write this precisely per design D6/D7, it is the security/correctness core of the change.
- [x] 4.5 Wire `writePaymentError` mirroring `writeOrderError`.
- [x] 4.6 JSON serialization helpers: `paymentJSON(p *ent.Payment)`, `paymentInitiateJSON(p *ent.Payment, res *payment.InitiateResult)`.
- [x] 4.7 Add `Payments *PaymentHandler` to `httpapi.Deps`; mount `Payments.MountShop` in the `/shop` MemberMW group, `Payments.MountShopAdmin` in `/admin/shops/{shopID}`, and a new unauthenticated `POST /api/v1/webhooks/payments/{provider}` route directly under `/api/v1` in `router.go` (design D7 — no TenantMW/MemberMW/AdminMW).

## 5. RBAC + seed

- [x] 5.1 Add `payment.view` to `seed.PermissionCatalog`.
- [x] 5.2 Grant `payment.view` to `merchant_owner` and `editor` role defs (mirrors `order.view` breadth).

## 6. Config + wiring

- [x] 6.1 Add `PaymentMockWebhookSecret` to `config.Config`, env var `PAYMENT_MOCK_WEBHOOK_SECRET`, dev fallback `dev-only-mock-payment-webhook-secret` (design D9, no production hard-require).
- [x] 6.2 Document the new env var in `.env.example`.
- [x] 6.3 Wire `payment.Service` + `MockProvider` + `PaymentHandler` in `api/internal/app/wire.go` (design D9).

## 7. Integration tests (`api/internal/httpapi/payment_integration_test.go`)

- [x] 7.1 Test env setup mirroring `orderEnv`/`newOrderEnv` (shops A/B, members, RBAC roles with/without `payment.view`, mock provider wired with a known test secret).
- [x] 7.2 End-to-end success flow: member initiates payment on their own unpaid order → webhook with valid signature reports succeeded → order `payment_status` becomes 1, payment record `status` becomes succeeded.
- [x] 7.3 Webhook signature verification failure is rejected (missing and wrong signature cases) and does not mutate payment/order state.
- [x] 7.4 Webhook idempotency: the same succeeded webhook (same `provider_reference`) delivered twice results in the order being marked paid exactly once, no error on the second call.
- [x] 7.5 Failed webhook does not change order `payment_status`, and member can subsequently retry (initiate a second payment on the same order, resulting in two `payments` rows).
- [x] 7.6 Cross-member isolation: member A cannot initiate payment against member B's order (404).
- [x] 7.7 Cross-shop isolation: admin without `payment.view` gets 403; admin from shop B gets 403 querying shop A's order payments; shop A admin with `payment.view` sees only shop A's payment records.
- [x] 7.8 Initiating payment against a cancelled or already-paid order is rejected (409) and does not create a payment record.
- [x] 7.9 Unknown `{provider}` path segment on the webhook route returns a clean error (404), not a panic/500.

## 8. Verification

- [x] 8.1 `cd api && go build ./...` passes.
- [x] 8.2 `cd api && golangci-lint run` passes (or repo's lint command if different).
- [x] 8.3 `cd api && INTEGRATION=1 go test -p 1 -count=1 ./...` passes (full suite, including new payment tests and unaffected existing packages).
- [x] 8.4 Confirm no code path outside `order.Service` writes `orders.payment_status` (grep for direct `ent.Order` mutation of that field in the new `payment`/`httpapi` code).
