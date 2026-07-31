package httpapi_test

// Integration coverage for change checkout-idempotency.
//
// These tests reuse guestEnv (guest_integration_test.go), which now wires an
// idempotency.Service onto both checkout handlers. Every pre-existing test in
// that file sends no Idempotency-Key header and passes unmodified — that is the
// operational definition of this change being additive, and it is deliberately
// left as evidence rather than restated as an assertion.
//
// The two load-bearing tests here are the -race ones at the bottom:
//
//	TestIdempotentGuestCheckoutConcurrentSameKey  exactly one order, stock -1 once
//	TestIdempotentCheckoutDoesNotWeakenOversell   the existing guarantee, with keys
//
// Everything above them is behavior; those two are the safety proof.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	entidem "ksdevworks/ecommerce/api/internal/ent/checkoutidempotencykey"
	"ksdevworks/ecommerce/api/internal/httpapi"
)

// ── helpers ────────────────────────────────────────────────────────────

// idemRequest issues one request with an optional Idempotency-Key, returning the
// status, the parsed body and the response headers. It returns errors instead of
// calling t.Fatal so it is safe to call from goroutines (mirrors guestRequest).
func (e *guestEnv) idemRequest(domain, method, path, key, bearer string, body any) (int, map[string]any, http.Header, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return 0, nil, nil, err
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set(httpapi.IdempotencyKeyHeader, key)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if domain != "" {
		req.Host = domain
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var parsed map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	return rec.Code, parsed, rec.Result().Header, nil
}

func (e *guestEnv) guestIdem(t *testing.T, key string, body any) (int, map[string]any, http.Header) {
	t.Helper()
	code, parsed, hdr, err := e.idemRequest(e.shopADomain, "POST", "/api/v1/shop/guest/checkout", key, "", body)
	if err != nil {
		t.Fatal(err)
	}
	return code, parsed, hdr
}

func orderIDOf(t *testing.T, body map[string]any) int {
	t.Helper()
	o, ok := body["order"].(map[string]any)
	if !ok {
		t.Fatalf("response has no order: %+v", body)
	}
	return int(o["id"].(float64))
}

func errCode(body map[string]any) string {
	e, ok := body["error"].(map[string]any)
	if !ok {
		return ""
	}
	c, _ := e["code"].(string)
	return c
}

func (e *guestEnv) orderCount(t *testing.T) int {
	t.Helper()
	return e.client.Order.Query().CountX(context.Background())
}

func (e *guestEnv) stockOf(t *testing.T, skuID int) int32 {
	t.Helper()
	return e.client.ProductSKU.GetX(context.Background(), skuID).StockQty
}

// ── replay ─────────────────────────────────────────────────────────────

func TestIdempotentGuestCheckoutReplaysTheSameOrder(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 10)
	body := guestBody("replay@t.dev", guestItem(skuID, 1))
	const key = "k-replay-0001"

	code, first, hdr := e.guestIdem(t, key, body)
	if code != http.StatusCreated {
		t.Fatalf("first checkout: want 201, got %d: %+v", code, first)
	}
	if got := hdr.Get(httpapi.IdempotencyReplayedHeader); got != "" {
		t.Errorf("the first response MUST NOT be marked as a replay, got %q", got)
	}
	stockAfterFirst := e.stockOf(t, skuID)

	code, second, hdr := e.guestIdem(t, key, body)
	if code != http.StatusCreated {
		t.Fatalf("replay: want 201 (indistinguishable from the original), got %d: %+v", code, second)
	}
	if got := hdr.Get(httpapi.IdempotencyReplayedHeader); got != "true" {
		t.Errorf("replay must carry %s: true, got %q", httpapi.IdempotencyReplayedHeader, got)
	}
	if a, b := orderIDOf(t, first), orderIDOf(t, second); a != b {
		t.Fatalf("replay returned order %d, want the original %d", b, a)
	}
	if n := e.orderCount(t); n != 1 {
		t.Errorf("a replay MUST NOT create a second order, got %d orders", n)
	}
	if got := e.stockOf(t, skuID); got != stockAfterFirst {
		t.Errorf("a replay MUST NOT deduct stock again: %d → %d", stockAfterFirst, got)
	}
	// The token is re-issued, not stored and returned — and it must actually
	// work, still bounded to this one order.
	tok, _ := second["order_access_token"].(string)
	if tok == "" {
		t.Fatal("replay must carry a usable order access token")
	}
	code, got := e.tokenCall(t, tok, e.shopADomain, "GET", "/api/v1/shop/guest/order", nil)
	if code != http.StatusOK {
		t.Fatalf("re-issued token: want 200, got %d: %+v", code, got)
	}
	if int(got["id"].(float64)) != orderIDOf(t, first) {
		t.Error("the re-issued token must name the same single order")
	}
	// ...and it grants nothing extra: order history is still a member capability.
	if code, _ := e.tokenCall(t, tok, e.shopADomain, "GET", "/api/v1/shop/orders", nil); code != http.StatusUnauthorized {
		t.Errorf("re-issued order token must not reach order history, got %d", code)
	}
}

func TestIdempotentReplayReflectsCurrentOrderState(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 5)
	body := guestBody("cancel-then-replay@t.dev", guestItem(skuID, 1))
	const key = "k-replay-cancelled"

	code, first, _ := e.guestIdem(t, key, body)
	if code != http.StatusCreated {
		t.Fatalf("first checkout: want 201, got %d: %+v", code, first)
	}
	orderID := orderIDOf(t, first)

	// Cancel it out of band (merchant side), then replay.
	if _, err := e.orderSvc.CancelOrder(context.Background(), e.shopA, orderID, nil); err != nil {
		t.Fatal(err)
	}

	code, second, _ := e.guestIdem(t, key, body)
	if code != http.StatusCreated {
		t.Fatalf("replay: want 201, got %d: %+v", code, second)
	}
	o := second["order"].(map[string]any)
	if int(o["status"].(float64)) != 1 {
		t.Errorf("replay must show the order as it is NOW (cancelled), got status %v", o["status"])
	}
	if n := e.orderCount(t); n != 1 {
		t.Errorf("replaying a cancelled order MUST NOT create a new one, got %d orders", n)
	}
}

func TestIdempotentReplayDoesNotAwardPointsTwice(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 100000, "TWD", 5) // 100000/100 = 1000 points
	body := guestBody("points-replay@t.dev", guestItem(skuID, 1))
	const key = "k-replay-points"

	code, first, _ := e.guestIdem(t, key, body)
	if code != http.StatusCreated {
		t.Fatalf("first checkout: want 201, got %d: %+v", code, first)
	}
	e.payAndConfirm(t, first["order_access_token"].(string))
	before := e.identity(t, e.shopA, "points-replay@t.dev").Points
	if before == 0 {
		t.Fatal("precondition: points must have been awarded before the replay is meaningful")
	}

	if code, second, _ := e.guestIdem(t, key, body); code != http.StatusCreated {
		t.Fatalf("replay: want 201, got %d: %+v", code, second)
	}
	if after := e.identity(t, e.shopA, "points-replay@t.dev").Points; after != before {
		t.Errorf("a replay MUST NOT re-award points: %d → %d", before, after)
	}
}

// ── same key, different request ────────────────────────────────────────

func TestIdempotentKeyReusedWithDifferentBody(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 10)
	const key = "k-reused-0001"

	code, first, _ := e.guestIdem(t, key, guestBody("reuse@t.dev", guestItem(skuID, 1)))
	if code != http.StatusCreated {
		t.Fatalf("first checkout: want 201, got %d: %+v", code, first)
	}

	code, body, _ := e.guestIdem(t, key, guestBody("reuse@t.dev", guestItem(skuID, 2)))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("same key, different body: want 422, got %d: %+v", code, body)
	}
	if got := errCode(body); got != "idempotency_key_reused" {
		t.Errorf("error code = %q, want idempotency_key_reused", got)
	}
	if n := e.orderCount(t); n != 1 {
		t.Errorf("a rejected reuse MUST NOT create an order, got %d orders", n)
	}
	// The original is untouched and still replayable.
	code, replay, _ := e.guestIdem(t, key, guestBody("reuse@t.dev", guestItem(skuID, 1)))
	if code != http.StatusCreated || orderIDOf(t, replay) != orderIDOf(t, first) {
		t.Errorf("the original must remain replayable after a rejected reuse: %d %+v", code, replay)
	}
}

// TestIdempotentKeyCollisionBetweenMembersLeaksNothing is the security case
// design D7's caller scope exists for: member checkout's body is nothing but a
// shipping address, so two customers shipping to the same address send
// byte-identical bodies.
func TestIdempotentKeyCollisionBetweenMembersLeaksNothing(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 10)
	const key = "k-collision-two-members"

	memberB := e.client.Member.Create().
		SetShopID(e.shopA).SetEmail("collision-b@t.dev").SetStatus(1).SaveX(ctx).ID
	for _, m := range []int{e.memberA1, memberB} {
		if _, err := e.cartSvc.AddItem(ctx, e.shopA, m, skuID, 1, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Byte-identical bodies: same recipient, same address.
	addr := map[string]any{"recipient_name": "王小明", "phone": "0912345678", "line1": "台北市信義區信義路五段7號"}

	tokA, err := e.issuer.IssueMember(e.memberA1, e.shopA)
	if err != nil {
		t.Fatal(err)
	}
	tokB, err := e.issuer.IssueMember(memberB, e.shopA)
	if err != nil {
		t.Fatal(err)
	}

	code, first, _, err := e.idemRequest(e.shopADomain, "POST", "/api/v1/shop/checkout", key, tokA, addr)
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusCreated {
		t.Fatalf("member A checkout: want 201, got %d: %+v", code, first)
	}
	firstOrderID := int(first["id"].(float64))

	code, second, _, err := e.idemRequest(e.shopADomain, "POST", "/api/v1/shop/checkout", key, tokB, addr)
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("member B colliding on A's key: want 422, got %d: %+v", code, second)
	}
	if got := errCode(second); got != "idempotency_key_reused" {
		t.Errorf("error code = %q, want idempotency_key_reused", got)
	}
	if _, leaked := second["id"]; leaked {
		t.Errorf("member B MUST NOT receive any order payload, got %+v", second)
	}
	if n := e.orderCount(t); n != 1 {
		t.Errorf("only member A's order may exist, got %d orders", n)
	}
	if o := e.client.Order.GetX(ctx, firstOrderID); o.MemberID != e.memberA1 {
		t.Errorf("order %d belongs to member %d, want %d", firstOrderID, o.MemberID, e.memberA1)
	}
}

// ── cross-shop isolation ───────────────────────────────────────────────

func TestIdempotentKeysAreScopedPerShop(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuA := e.newSKU(t, e.shopA, 3400, "TWD", 5)
	skuB := e.newSKU(t, e.shopB, 4400, "TWD", 5)
	const key = "k-shared-across-shops"

	codeA, bodyA, _, err := e.idemRequest(e.shopADomain, "POST", "/api/v1/shop/guest/checkout", key, "",
		guestBody("cross@t.dev", guestItem(skuA, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if codeA != http.StatusCreated {
		t.Fatalf("shop A checkout: want 201, got %d: %+v", codeA, bodyA)
	}

	// Shop B, same key string, different body. If the key were global this would
	// be a 422 reuse (or worse, a replay of shop A's order).
	codeB, bodyB, hdrB, err := e.idemRequest(e.shopBDomain, "POST", "/api/v1/shop/guest/checkout", key, "",
		guestBody("cross@t.dev", guestItem(skuB, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if codeB != http.StatusCreated {
		t.Fatalf("shop B must be able to use the same key string: want 201, got %d: %+v", codeB, bodyB)
	}
	if hdrB.Get(httpapi.IdempotencyReplayedHeader) != "" {
		t.Error("shop B MUST NOT receive a replay of shop A's key")
	}
	orderA, orderB := orderIDOf(t, bodyA), orderIDOf(t, bodyB)
	if orderA == orderB {
		t.Fatal("the two shops must have produced two distinct orders")
	}
	if o := e.client.Order.GetX(ctx, orderB); o.ShopID != e.shopB {
		t.Errorf("shop B's order belongs to shop %d, want %d", o.ShopID, e.shopB)
	}

	// Each shop replays its own, and only its own.
	_, replayA, _, _ := e.idemRequest(e.shopADomain, "POST", "/api/v1/shop/guest/checkout", key, "",
		guestBody("cross@t.dev", guestItem(skuA, 1)))
	if orderIDOf(t, replayA) != orderA {
		t.Errorf("shop A replayed order %d, want its own %d", orderIDOf(t, replayA), orderA)
	}
	_, replayB, _, _ := e.idemRequest(e.shopBDomain, "POST", "/api/v1/shop/guest/checkout", key, "",
		guestBody("cross@t.dev", guestItem(skuB, 1)))
	if orderIDOf(t, replayB) != orderB {
		t.Errorf("shop B replayed order %d, want its own %d", orderIDOf(t, replayB), orderB)
	}
}

// ── failures do not consume the key (design D6) ────────────────────────

func TestIdempotentKeyIsFreeAfterValidationFailure(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 5)
	const key = "k-fix-and-resend"

	// The exact ONDO flow: a field is wrong, the customer fixes it, the client
	// resends with the SAME key (and therefore a different body).
	broken := guestBody("fixme@t.dev", guestItem(skuID, 1))
	broken["shipping_address"] = map[string]any{"recipient_name": "王小明", "line1": "地址"} // no phone
	code, body, _ := e.guestIdem(t, key, broken)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("missing phone: want 422, got %d: %+v", code, body)
	}

	code, fixed, _ := e.guestIdem(t, key, guestBody("fixme@t.dev", guestItem(skuID, 1)))
	if code != http.StatusCreated {
		t.Fatalf("the corrected resend with the same key must succeed, got %d: %+v", code, fixed)
	}
	if n := e.orderCount(t); n != 1 {
		t.Errorf("want exactly 1 order, got %d", n)
	}
}

func TestIdempotentKeyIsFreeAfterInsufficientStock(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 0)
	body := guestBody("restock@t.dev", guestItem(skuID, 1))
	const key = "k-restock-retry"

	code, got, _ := e.guestIdem(t, key, body)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("out of stock: want 422, got %d: %+v", code, got)
	}
	// Merchant restocks; the customer retries the identical request.
	e.client.ProductSKU.UpdateOneID(skuID).SetStockQty(3).SaveX(context.Background())

	if code, got, _ := e.guestIdem(t, key, body); code != http.StatusCreated {
		t.Fatalf("after restock the same key must work: want 201, got %d: %+v", code, got)
	}
}

func TestIdempotentKeyIsFreeAfterMalformedBody(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 5)
	const key = "k-broken-json"

	req := httptest.NewRequest("POST", "/api/v1/shop/guest/checkout", bytes.NewBufferString("{not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(httpapi.IdempotencyKeyHeader, key)
	req.Host = e.shopADomain
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON: want 400, got %d", rec.Code)
	}

	if code, got, _ := e.guestIdem(t, key, guestBody("brokenjson@t.dev", guestItem(skuID, 1))); code != http.StatusCreated {
		t.Fatalf("the same key must still be usable: want 201, got %d: %+v", code, got)
	}
}

// ── key format ─────────────────────────────────────────────────────────

func TestIdempotentKeyFormatIsRejectedBeforeAnythingHappens(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 5)
	body := guestBody("badkey@t.dev", guestItem(skuID, 1))

	for _, key := range []string{"short", "with space", "with\nnewline"} {
		code, got, _ := e.guestIdem(t, key, body)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("key %q: want 422, got %d: %+v", key, code, got)
		}
		if c := errCode(got); c != "invalid_idempotency_key" {
			t.Errorf("key %q: error code = %q, want invalid_idempotency_key", key, c)
		}
	}
	if n := e.orderCount(t); n != 0 {
		t.Errorf("a rejected key MUST NOT create anything, got %d orders", n)
	}
	if got := e.stockOf(t, skuID); got != 5 {
		t.Errorf("a rejected key MUST NOT touch stock, got %d", got)
	}
}

// ── absence of the header is the default and is unchanged ──────────────

func TestCheckoutWithoutKeyStillCreatesTwoOrders(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 5)
	body := guestBody("nokey@t.dev", guestItem(skuID, 1))

	for i := 0; i < 2; i++ {
		if code, got, _ := e.guestIdem(t, "", body); code != http.StatusCreated {
			t.Fatalf("checkout %d: want 201, got %d: %+v", i, code, got)
		}
	}
	if n := e.orderCount(t); n != 2 {
		t.Errorf("without a key the behavior MUST be unchanged (two orders), got %d", n)
	}
	if got := e.stockOf(t, skuID); got != 3 {
		t.Errorf("stock must have been deducted twice (5 → 3), got %d", got)
	}
	if e.client.CheckoutIdempotencyKey.Query().CountX(context.Background()) != 0 {
		t.Error("a request without the header MUST NOT write any idempotency row")
	}
}

// ── expiry (design D8) ─────────────────────────────────────────────────

func TestExpiredIdempotencyKeyStartsAFreshOrder(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 5)
	body := guestBody("expiry@t.dev", guestItem(skuID, 1))
	const key = "k-expiring-0001"

	code, first, _ := e.guestIdem(t, key, body)
	if code != http.StatusCreated {
		t.Fatalf("first checkout: want 201, got %d: %+v", code, first)
	}

	// Simulate time passing by moving the row's own deadline into the past —
	// expiry is a stored column precisely so it can be reasoned about (and
	// tested) without a clock abstraction.
	n := e.client.CheckoutIdempotencyKey.Update().
		Where(entidem.ShopIDEQ(e.shopA), entidem.IdempotencyKeyEQ(key)).
		SetExpiresAt(time.Now().Add(-time.Minute)).
		SaveX(ctx)
	if n != 1 {
		t.Fatalf("expected exactly one idempotency row to age, got %d", n)
	}

	code, second, hdr := e.guestIdem(t, key, body)
	if code != http.StatusCreated {
		t.Fatalf("post-expiry checkout: want 201, got %d: %+v", code, second)
	}
	if hdr.Get(httpapi.IdempotencyReplayedHeader) != "" {
		t.Error("an expired key MUST NOT replay")
	}
	if orderIDOf(t, first) == orderIDOf(t, second) {
		t.Fatal("an expired key must produce a NEW order")
	}
	if n := e.orderCount(t); n != 2 {
		t.Errorf("want 2 orders after expiry, got %d", n)
	}
	// The takeover reuses the row rather than accumulating one per attempt.
	if n := e.client.CheckoutIdempotencyKey.Query().CountX(ctx); n != 1 {
		t.Errorf("the expired row must be taken over in place, got %d rows", n)
	}
}

// ── member checkout parity (design D10) ────────────────────────────────

func TestIdempotentMemberCheckoutReplaysTheSameOrder(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", 10)
	if _, err := e.cartSvc.AddItem(ctx, e.shopA, e.memberA1, skuID, 1, ""); err != nil {
		t.Fatal(err)
	}
	tok, err := e.issuer.IssueMember(e.memberA1, e.shopA)
	if err != nil {
		t.Fatal(err)
	}
	addr := map[string]any{"recipient_name": "王小明", "phone": "0912345678", "line1": "台北市信義區信義路五段7號"}
	const key = "k-member-replay"

	code, first, hdr, err := e.idemRequest(e.shopADomain, "POST", "/api/v1/shop/checkout", key, tok, addr)
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusCreated {
		t.Fatalf("member checkout: want 201, got %d: %+v", code, first)
	}
	if hdr.Get(httpapi.IdempotencyReplayedHeader) != "" {
		t.Error("the first member checkout MUST NOT be marked as a replay")
	}
	stockAfterFirst := e.stockOf(t, skuID)

	// Without a key this exact retry would be a 409 "cart has already been
	// checked out" — correct, but it never tells the client which order exists.
	code, second, hdr, err := e.idemRequest(e.shopADomain, "POST", "/api/v1/shop/checkout", key, tok, addr)
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusCreated {
		t.Fatalf("member replay: want 201, got %d: %+v", code, second)
	}
	if hdr.Get(httpapi.IdempotencyReplayedHeader) != "true" {
		t.Error("member replay must carry the replay header")
	}
	if int(first["id"].(float64)) != int(second["id"].(float64)) {
		t.Error("member replay must return the same order")
	}
	if n := e.orderCount(t); n != 1 {
		t.Errorf("member replay MUST NOT create a second order, got %d", n)
	}
	if got := e.stockOf(t, skuID); got != stockAfterFirst {
		t.Errorf("member replay MUST NOT deduct stock again: %d → %d", stockAfterFirst, got)
	}
}

// ── concurrency: THE proof (design D11) ────────────────────────────────

// TestIdempotentGuestCheckoutConcurrentSameKey is the load-bearing test of this
// change. Run with -race.
//
// N identical requests carrying one key are fired at once against ample stock.
// Exactly one order may exist and stock may move by exactly one unit; every
// other request must land on a deterministic answer (409 in-progress, or a 201
// replay if the winner had already committed) and never on a 5xx.
func TestIdempotentGuestCheckoutConcurrentSameKey(t *testing.T) {
	e := newGuestEnv(t)
	const n = 12
	const stock = 20
	skuID := e.newSKU(t, e.shopA, 3400, "TWD", stock)
	body := guestBody("race@t.dev", guestItem(skuID, 1))
	const key = "k-concurrent-same-key"

	codes := make([]int, n)
	replayed := make([]bool, n)
	orderIDs := make([]int, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, parsed, hdr, err := e.idemRequest(e.shopADomain, "POST", "/api/v1/shop/guest/checkout", key, "", body)
			if err != nil {
				errs <- err
				return
			}
			codes[i] = code
			replayed[i] = hdr.Get(httpapi.IdempotencyReplayedHeader) == "true"
			if o, ok := parsed["order"].(map[string]any); ok {
				orderIDs[i] = int(o["id"].(float64))
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	var created, inProgress, replays int
	for i, c := range codes {
		switch {
		case c == http.StatusCreated && replayed[i]:
			replays++
		case c == http.StatusCreated:
			created++
		case c == http.StatusConflict:
			inProgress++
		default:
			t.Errorf("request %d: unexpected status %d", i, c)
		}
	}
	if created != 1 {
		t.Errorf("exactly one request may create the order, got %d (codes=%v replayed=%v)", created, codes, replayed)
	}
	if created+inProgress+replays != n {
		t.Errorf("every request must land on a defined outcome: %d created + %d in-progress + %d replays != %d",
			created, inProgress, replays, n)
	}
	if got := e.orderCount(t); got != 1 {
		t.Errorf("exactly one order must exist, got %d", got)
	}
	if got := e.stockOf(t, skuID); got != stock-1 {
		t.Errorf("stock must be deducted exactly once (%d → %d), got %d", stock, stock-1, got)
	}
	// Every successful response — original or replay — names that one order.
	var theOrder int
	for i, c := range codes {
		if c != http.StatusCreated {
			continue
		}
		if theOrder == 0 {
			theOrder = orderIDs[i]
		}
		if orderIDs[i] != theOrder {
			t.Fatalf("request %d returned order %d, want %d", i, orderIDs[i], theOrder)
		}
	}
	if n := e.client.CheckoutIdempotencyKey.Query().CountX(context.Background()); n != 1 {
		t.Errorf("exactly one reservation row must exist, got %d", n)
	}
	t.Logf("concurrent same key: %d created, %d in-progress(409), %d replays, 1 order, stock %d→%d",
		created, inProgress, replays, stock, e.stockOf(t, skuID))
}

// TestIdempotentCheckoutDoesNotWeakenOversell re-runs the existing oversell
// scenario with every request carrying its OWN key: the idempotency machinery
// must be invisible to the stock guard. Run with -race.
func TestIdempotentCheckoutDoesNotWeakenOversell(t *testing.T) {
	e := newGuestEnv(t)
	const stock = 5
	const n = 10
	skuID := e.newSKU(t, e.shopA, 500, "TWD", stock)

	codes := make([]int, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := guestBody(fmt.Sprintf("keyed-rush-%d@t.dev", i), guestItem(skuID, 1))
			code, _, _, err := e.idemRequest(e.shopADomain, "POST", "/api/v1/shop/guest/checkout",
				fmt.Sprintf("k-rush-%08d", i), "", body)
			if err != nil {
				errs <- err
				return
			}
			codes[i] = code
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	var succeeded, failed int
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			succeeded++
		case http.StatusUnprocessableEntity:
			failed++
		default:
			t.Errorf("unexpected status code: %d", c)
		}
	}
	if succeeded != stock {
		t.Errorf("want %d successes, got %d (codes=%v)", stock, succeeded, codes)
	}
	if failed != n-stock {
		t.Errorf("want %d failures, got %d (codes=%v)", n-stock, failed, codes)
	}
	if got := e.stockOf(t, skuID); got != 0 {
		t.Errorf("stock must land exactly at 0 (never negative), got %d", got)
	}
	if got := e.orderCount(t); got != stock {
		t.Errorf("want exactly %d orders, got %d", stock, got)
	}
	// The five losers never created an order, so their keys must be free again.
	if n := e.client.CheckoutIdempotencyKey.Query().CountX(context.Background()); n != stock {
		t.Errorf("only the %d winning reservations may remain, got %d", stock, n)
	}
}
