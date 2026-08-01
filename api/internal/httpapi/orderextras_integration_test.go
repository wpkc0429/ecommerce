package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	entorderitem "ksdevworks/ecommerce/api/internal/ent/orderitem"
	"ksdevworks/ecommerce/api/internal/payment"
	"ksdevworks/ecommerce/api/internal/shipping"
)

// Change order-extras: the three things ONDO's checkout needs (per-item gift
// message, per-order requested delivery date, shipping fee inside the payable
// total) and the one product decision that came with them (points are awarded
// on the item subtotal, never on shipping).
//
// These reuse guestEnv because it is the only harness that wires member
// checkout, guest checkout, payments, shipping and points side by side — which
// is exactly what "both entry points behave identically" has to be proved
// against.

// ── helpers ────────────────────────────────────────────────────────────

func (e *guestEnv) newShippingMethod(t *testing.T, shopID int, name string, flatRate int64, active bool) int {
	t.Helper()
	e.seq++
	return e.client.ShippingMethod.Create().
		SetShopID(shopID).SetName(name).SetCarrier("黑貓宅急便").
		SetFlatRate(flatRate).SetIsActive(active).
		SaveX(context.Background()).ID
}

// memberCheckout seeds memberID's cart with one line and checks out through the
// member route, so member-path assertions go through the real HTTP contract.
func (e *guestEnv) memberCheckout(t *testing.T, memberID, skuID int, qty int32, extra map[string]any) (int, map[string]any) {
	t.Helper()
	if _, err := e.cartSvc.AddItem(context.Background(), e.shopA, memberID, skuID, qty, ""); err != nil {
		t.Fatalf("seed cart: %v", err)
	}
	body := map[string]any{
		"recipient_name": "王小明",
		"phone":          "0912345678",
		"line1":          "台北市松山區民生東路一段1號",
	}
	for k, v := range extra {
		body[k] = v
	}
	return e.memberTokenCall(t, memberID, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", body)
}

func (e *guestEnv) newPasswordlessMember(t *testing.T, email string) int {
	t.Helper()
	return e.client.Member.Create().
		SetShopID(e.shopA).SetEmail(email).SetStatus(1).
		SaveX(context.Background()).ID
}

// soleErrorPointer asserts the response is a design D12 envelope carrying
// exactly one validation detail, and returns its JSON pointer.
func soleErrorPointer(t *testing.T, body map[string]any) string {
	t.Helper()
	env, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a D12 error envelope, got %+v", body)
	}
	details, _ := env["details"].([]any)
	if len(details) != 1 {
		t.Fatalf("expected exactly one detail, got %+v", env)
	}
	ptr, _ := details[0].(map[string]any)["pointer"].(string)
	return ptr
}

func amountsOf(t *testing.T, body map[string]any) (subtotal, fee, total float64) {
	t.Helper()
	sub, ok1 := body["subtotal_amount"].(float64)
	f, ok2 := body["shipping_fee"].(float64)
	tot, ok3 := body["total_amount"].(float64)
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("order response must carry all three amounts: %+v", body)
	}
	return sub, f, tot
}

// payAndConfirm drives an order all the way to paid through the real payment
// provider + webhook, so the points assertions react to the same event
// production does.
func (e *guestEnv) payAndConfirm(t *testing.T, token string) {
	t.Helper()
	code, initiated := e.tokenCall(t, token, e.shopADomain, "POST", "/api/v1/shop/guest/order/payments", nil)
	if code != http.StatusCreated {
		t.Fatalf("initiate payment: want 201, got %d: %+v", code, initiated)
	}
	ref, _ := initiated["provider_reference"].(string)
	body, _ := json.Marshal(map[string]string{"provider_reference": ref, "status": "succeeded"})
	req := httptest.NewRequest("POST", "/api/v1/webhooks/payments/mock", bytes.NewReader(body))
	req.Header.Set("X-Payment-Signature", payment.SignMockWebhook(e.webhookSecret, body))
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("payment webhook: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ── amount composition (spec order-management) ─────────────────────────

// Scenario: 未選定運送方式的訂單 — the pre-change behavior, byte for byte.
func TestCheckoutWithoutShippingMethodChargesTheSubtotalOnly(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3600, "TWD", 10)

	code, body := e.memberCheckout(t, e.memberA1, skuID, 1, nil)
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %+v", code, body)
	}
	subtotal, fee, total := amountsOf(t, body)
	if subtotal != 3600 || fee != 0 || total != 3600 {
		t.Fatalf("want 3600/0/3600, got %v/%v/%v", subtotal, fee, total)
	}
	if body["shipping_method_id"] != nil || body["shipping_method_name"] != nil {
		t.Fatalf("no method selected must leave both snapshot fields null: %+v", body)
	}
}

// Scenario: 選定運送方式的訂單其應付總額含運費.
func TestCheckoutWithShippingMethodFoldsTheFeeIntoTheTotal(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 3600, "TWD", 10)
	methodID := e.newShippingMethod(t, e.shopA, "台北市當日配送", 200, true)

	code, body := e.memberCheckout(t, e.memberA1, skuID, 1, map[string]any{"shipping_method_id": methodID})
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %+v", code, body)
	}
	subtotal, fee, total := amountsOf(t, body)
	if subtotal != 3600 || fee != 200 || total != 3800 {
		t.Fatalf("want 3600/200/3800, got %v/%v/%v", subtotal, fee, total)
	}
	if body["shipping_method_id"].(float64) != float64(methodID) {
		t.Fatalf("selected method id must be recorded: %+v", body["shipping_method_id"])
	}
	if body["shipping_method_name"].(string) != "台北市當日配送" {
		t.Fatalf("selected method name must be snapshotted: %+v", body["shipping_method_name"])
	}
}

// Scenario: 金額組成不可被破壞 — the invariant is a database fact (design D7),
// not an arithmetic convention some future order-creating path could forget.
func TestOrderAmountCompositionIsEnforcedByTheDatabase(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	memberID := e.newPasswordlessMember(t, "composition-probe@t.dev")

	_, err := e.client.Order.Create().
		SetShopID(e.shopA).SetMemberID(memberID).
		SetStatus(0).SetPaymentStatus(0).SetFulfillmentStatus(0).
		SetCurrency("TWD").
		SetSubtotalAmount(3600).SetShippingFee(200).
		SetTotalAmount(3600). // deliberately wrong: ignores the fee
		SetShippingAddress(json.RawMessage(`{}`)).
		Save(ctx)
	if err == nil {
		t.Fatal("the database must refuse an order whose total does not equal subtotal + shipping fee")
	}
	if !strings.Contains(err.Error(), "orders_total_amount_composition_check") {
		t.Fatalf("expected the composition CHECK to be the thing that refused it, got: %v", err)
	}
}

// ── shipping method validation (spec order-management) ─────────────────

// Scenario: 選定已停用／其他商家／不存在的運送方式被拒 — and, crucially, none
// of them may consume the cart or touch stock: the whole point of validating
// before the transaction opens.
func TestCheckoutRejectsUnusableShippingMethodWithoutTouchingStock(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()

	inactive := e.newShippingMethod(t, e.shopA, "已停用", 150, false)
	foreign := e.newShippingMethod(t, e.shopB, "別家商家的", 150, true)

	cases := map[string]int{
		"nonexistent":  999999,
		"inactive":     inactive,
		"foreign shop": foreign,
	}
	for name, methodID := range cases {
		t.Run(name, func(t *testing.T) {
			skuID := e.newSKU(t, e.shopA, 1000, "TWD", 5)
			memberID := e.newPasswordlessMember(t, fmt.Sprintf("bad-method-%s@t.dev", strings.ReplaceAll(name, " ", "-")))

			code, body := e.memberCheckout(t, memberID, skuID, 2, map[string]any{"shipping_method_id": methodID})
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("want 422, got %d: %+v", code, body)
			}
			if ptr := soleErrorPointer(t, body); ptr != "/shipping_method_id" {
				t.Fatalf("expected the error to point at /shipping_method_id, got %v", ptr)
			}

			if got := e.client.ProductSKU.GetX(ctx, skuID).StockQty; got != 5 {
				t.Fatalf("stock must be untouched, got %d", got)
			}
			view, err := e.cartSvc.GetCartView(ctx, e.shopA, memberID)
			if err != nil {
				t.Fatal(err)
			}
			if view.ID == nil || len(view.Items) != 1 {
				t.Fatalf("the cart must still be active and intact, got %+v", view)
			}
		})
	}
}

// Scenario: 運費快照不因運送方式改價／刪除而變動 + 刪除曾被選用的運送方式仍然成功.
func TestShippingFeeAndNameAreSnapshottedOntoTheOrder(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 1000, "TWD", 10)
	methodID := e.newShippingMethod(t, e.shopA, "全台隔日配送", 150, true)

	code, created := e.memberCheckout(t, e.memberA1, skuID, 1, map[string]any{"shipping_method_id": methodID})
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %+v", code, created)
	}
	orderID := int(created["id"].(float64))

	// The merchant re-prices, renames, then retires the method entirely.
	e.client.ShippingMethod.UpdateOneID(methodID).SetFlatRate(300).SetName("改名了").ExecX(ctx)
	if err := e.shippingSvc.DeleteShippingMethod(ctx, e.shopA, methodID); err != nil {
		t.Fatalf("deleting a method that historical orders reference must still succeed: %v", err)
	}

	o := e.client.Order.GetX(ctx, orderID)
	if o.ShippingFee != 150 {
		t.Fatalf("shipping_fee must stay at the price the customer agreed to, got %d", o.ShippingFee)
	}
	if o.TotalAmount != 1150 || o.SubtotalAmount != 1000 {
		t.Fatalf("amounts must not move: subtotal=%d total=%d", o.SubtotalAmount, o.TotalAmount)
	}
	if o.ShippingMethodName == nil || *o.ShippingMethodName != "全台隔日配送" {
		t.Fatalf("the snapshotted name must survive rename+delete, got %v", o.ShippingMethodName)
	}
	// The id may now dangle — that is documented, and nothing may depend on it.
	if o.ShippingMethodID == nil || *o.ShippingMethodID != methodID {
		t.Fatalf("the informational id must be retained, got %v", o.ShippingMethodID)
	}
}

// ── requested delivery date (spec order-management) ────────────────────

func TestRequestedDeliveryDate(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")

	t.Run("accepted and read back as a bare date", func(t *testing.T) {
		skuID := e.newSKU(t, e.shopA, 1000, "TWD", 5)
		memberID := e.newPasswordlessMember(t, "date-ok@t.dev")
		code, body := e.memberCheckout(t, memberID, skuID, 1, map[string]any{"requested_delivery_date": tomorrow})
		if code != http.StatusCreated {
			t.Fatalf("want 201, got %d: %+v", code, body)
		}
		if got := body["requested_delivery_date"]; got != tomorrow {
			t.Fatalf("want %q, got %v (a bare YYYY-MM-DD, never a timestamp)", tomorrow, got)
		}
		stored := e.client.Order.GetX(ctx, int(body["id"].(float64))).RequestedDeliveryDate
		if stored == nil || stored.Format("2006-01-02") != tomorrow {
			t.Fatalf("stored date mismatch: %v", stored)
		}
	})

	t.Run("absent stays null", func(t *testing.T) {
		skuID := e.newSKU(t, e.shopA, 1000, "TWD", 5)
		memberID := e.newPasswordlessMember(t, "date-absent@t.dev")
		code, body := e.memberCheckout(t, memberID, skuID, 1, nil)
		if code != http.StatusCreated {
			t.Fatalf("want 201, got %d: %+v", code, body)
		}
		if got, present := body["requested_delivery_date"]; !present || got != nil {
			t.Fatalf("want an explicit null, got %v (present=%v)", got, present)
		}
	})

	for name, raw := range map[string]string{
		"slashes":         "2026/08/05",
		"nonexistent day": "2026-02-31",
		"long past":       "2020-01-01",
	} {
		t.Run("rejected: "+name, func(t *testing.T) {
			skuID := e.newSKU(t, e.shopA, 1000, "TWD", 5)
			memberID := e.newPasswordlessMember(t, fmt.Sprintf("date-bad-%s@t.dev", strings.ReplaceAll(name, " ", "-")))
			code, body := e.memberCheckout(t, memberID, skuID, 1, map[string]any{"requested_delivery_date": raw})
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("want 422 for %q, got %d: %+v", raw, code, body)
			}
			if ptr := soleErrorPointer(t, body); ptr != "/requested_delivery_date" {
				t.Fatalf("expected a /requested_delivery_date pointer, got %v", ptr)
			}
			if got := e.client.ProductSKU.GetX(ctx, skuID).StockQty; got != 5 {
				t.Fatalf("a rejected date must not consume stock, got %d", got)
			}
		})
	}
}

// ── gift message (spec shopping-cart, order-management) ────────────────

func TestGiftMessageTravelsFromCartToOrderItem(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 1000, "TWD", 10)
	memberID := e.newPasswordlessMember(t, "gift-member@t.dev")
	tok, err := e.issuer.IssueMember(memberID, e.shopA)
	if err != nil {
		t.Fatal(err)
	}

	const msg = "生日快樂，願你今天所有的願望都成真。"
	code, cartBody := e.tokenCall(t, tok, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": skuID, "quantity": 1, "gift_message": msg})
	if code != http.StatusCreated {
		t.Fatalf("add item: %d %+v", code, cartBody)
	}
	items := cartBody["items"].([]any)
	if got := items[0].(map[string]any)["gift_message"]; got != msg {
		t.Fatalf("cart must echo the message, got %v", got)
	}

	code, orderBody := e.tokenCall(t, tok, e.shopADomain, "POST", "/api/v1/shop/checkout", map[string]any{
		"recipient_name": "王小明", "phone": "0912345678", "line1": "台北市",
	})
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %+v", code, orderBody)
	}
	orderItems := orderBody["items"].([]any)
	if got := orderItems[0].(map[string]any)["gift_message"]; got != msg {
		t.Fatalf("order item must carry the snapshot, got %v", got)
	}

	stored := e.client.OrderItem.Query().
		Where(entorderitem.OrderIDEQ(int(orderBody["id"].(float64)))).
		AllX(ctx)
	if len(stored) != 1 || stored[0].GiftMessage != msg {
		t.Fatalf("the snapshot must be a column on order_items, got %+v", stored)
	}
}

func TestGiftMessageLengthLimitIsCountedInCharacters(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 1000, "TWD", 10)
	memberID := e.newPasswordlessMember(t, "gift-length@t.dev")
	tok, err := e.issuer.IssueMember(memberID, e.shopA)
	if err != nil {
		t.Fatal(err)
	}

	// 500 CJK characters = 1500 bytes. A byte-counted limit would reject this,
	// which would mean the same field holds a third as much Chinese as English.
	code, body := e.tokenCall(t, tok, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": skuID, "quantity": 1, "gift_message": strings.Repeat("花", 500)})
	if code != http.StatusCreated {
		t.Fatalf("500 CJK characters must be accepted, got %d: %+v", code, body)
	}

	code, body = e.tokenCall(t, tok, e.shopADomain, "POST", "/api/v1/shop/cart/items",
		map[string]any{"sku_id": skuID, "quantity": 1, "gift_message": strings.Repeat("a", 501)})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("501 characters must be rejected, got %d: %+v", code, body)
	}
	view, err := e.cartSvc.GetCartView(context.Background(), e.shopA, memberID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Items) != 1 {
		t.Fatalf("the rejected add must not have created a truncated line: %d items", len(view.Items))
	}
}

// Scenario: 同一 SKU 不同贈禮訊息成為兩個品項 / 同訊息累加 / 跨訊息的庫存加總.
func TestCartMergeKeyIncludesTheGiftMessage(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 1000, "TWD", 3)
	memberID := e.newPasswordlessMember(t, "merge-key@t.dev")

	add := func(qty int32, msg string) error {
		_, err := e.cartSvc.AddItem(ctx, e.shopA, memberID, skuID, qty, msg)
		return err
	}

	if err := add(1, "給媽媽"); err != nil {
		t.Fatal(err)
	}
	if err := add(1, "給岳母"); err != nil {
		t.Fatal(err)
	}
	view, err := e.cartSvc.GetCartView(ctx, e.shopA, memberID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Items) != 2 {
		t.Fatalf("two cards means two lines, got %d: %+v", len(view.Items), view.Items)
	}

	// Same SKU AND same message accumulates, exactly as before this change.
	if err := add(1, "給媽媽"); err != nil {
		t.Fatal(err)
	}
	view, err = e.cartSvc.GetCartView(ctx, e.shopA, memberID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Items) != 2 {
		t.Fatalf("same message must merge, got %d lines", len(view.Items))
	}
	var total int32
	for _, it := range view.Items {
		total += it.Quantity
	}
	if total != 3 {
		t.Fatalf("want 3 units held, got %d", total)
	}

	// Stock belongs to the SKU, so a fourth unit under a third message is still
	// over the ceiling of 3.
	if err := add(1, "給老師"); err == nil {
		t.Fatal("the stock ceiling must apply across every line of the SKU, not per message")
	}

	// And the two lines stay two lines through checkout.
	code, body := e.memberTokenCallCheckout(t, memberID)
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %+v", code, body)
	}
	items := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("want two order items, got %d", len(items))
	}
	seen := map[string]float64{}
	for _, raw := range items {
		it := raw.(map[string]any)
		seen[it["gift_message"].(string)] = it["quantity"].(float64)
	}
	if seen["給媽媽"] != 2 || seen["給岳母"] != 1 {
		t.Fatalf("per-message quantities must survive checkout: %+v", seen)
	}
}

func (e *guestEnv) memberTokenCallCheckout(t *testing.T, memberID int) (int, map[string]any) {
	t.Helper()
	return e.memberTokenCall(t, memberID, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", map[string]any{
		"recipient_name": "王小明", "phone": "0912345678", "line1": "台北市",
	})
}

// ── points basis (spec member-tiers-and-points) ────────────────────────

// Scenario: 運費不計入核發點數 / 配送方式不影響核發點數 / 含運費訂單退貨只扣回
// 商品所核發的點數. This is design D5 made executable.
func TestPointsAreAwardedOnTheSubtotalNotOnShipping(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 3600, "TWD", 10)
	sameDay := e.newShippingMethod(t, e.shopA, "台北市當日配送", 200, true)
	pickup := e.newShippingMethod(t, e.shopA, "門市自取", 0, true)

	// Same bouquet, two delivery choices.
	shippedOrder, shippedToken := e.checkoutAsGuestWithExtras(t, "points-shipped@t.dev", skuID,
		map[string]any{"shipping_method_id": sameDay})
	pickupOrder, pickupToken := e.checkoutAsGuestWithExtras(t, "points-pickup@t.dev", skuID,
		map[string]any{"shipping_method_id": pickup})

	if got := e.client.Order.GetX(ctx, shippedOrder).TotalAmount; got != 3800 {
		t.Fatalf("shipped order total must include the fee, got %d", got)
	}
	e.payAndConfirm(t, shippedToken)
	e.payAndConfirm(t, pickupToken)

	shippedPoints := e.pointsOf(t, shippedOrder)
	pickupPoints := e.pointsOf(t, pickupOrder)
	if shippedPoints != 36 {
		t.Fatalf("want 36 points (3600/100), got %d — shipping must not earn points", shippedPoints)
	}
	if shippedPoints != pickupPoints {
		t.Fatalf("the delivery choice must not change the reward: %d vs %d", shippedPoints, pickupPoints)
	}

	// Returning the goods claws back exactly what the goods earned.
	memberID := e.client.Order.GetX(ctx, shippedOrder).MemberID
	sh, err := e.shippingSvc.CreateShipment(ctx, e.shopA, shippedOrder, "黑貓宅急便", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.shippingSvc.AdvanceShipment(ctx, e.shopA, shippedOrder, sh.ID, shipping.ReturnedStatus); err != nil {
		t.Fatal(err)
	}
	if got := e.client.Member.GetX(ctx, memberID).Points; got != 0 {
		t.Fatalf("clawback must remove exactly the 36 awarded, leaving 0, got %d", got)
	}
}

// checkoutAsGuestWithExtras is checkoutAsGuest plus the order-level extras.
func (e *guestEnv) checkoutAsGuestWithExtras(t *testing.T, email string, skuID int, extra map[string]any) (int, string) {
	t.Helper()
	body := guestBody(email, guestItem(skuID, 1))
	for k, v := range extra {
		body[k] = v
	}
	code, parsed := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout", body)
	if code != http.StatusCreated {
		t.Fatalf("guest checkout: want 201, got %d: %+v", code, parsed)
	}
	o := parsed["order"].(map[string]any)
	return int(o["id"].(float64)), parsed["order_access_token"].(string)
}

// pointsOf returns the points awarded for one order, read from the ledger.
func (e *guestEnv) pointsOf(t *testing.T, orderID int) int32 {
	t.Helper()
	rows := e.client.PointTransaction.Query().AllX(context.Background())
	for _, r := range rows {
		if r.OrderID != nil && *r.OrderID == orderID && r.PointsDelta > 0 {
			return r.PointsDelta
		}
	}
	t.Fatalf("no award transaction found for order %d", orderID)
	return 0
}

// ── public shipping methods (spec shipping-logistics) ──────────────────

func TestPublicShippingMethodsEndpoint(t *testing.T) {
	e := newGuestEnv(t)
	activeA := e.newShippingMethod(t, e.shopA, "台北市當日配送", 200, true)
	e.newShippingMethod(t, e.shopA, "已停用的方式", 999, false)
	e.newShippingMethod(t, e.shopB, "別家商家的方式", 111, true)

	code, body, err := e.guestRequest(e.shopADomain, "GET", "/api/v1/shop/shipping-methods", nil)
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusOK {
		t.Fatalf("the endpoint must be readable with no credential at all, got %d: %s", code, body)
	}
	var parsed struct {
		ShippingMethods []map[string]any `json:"shipping_methods"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.ShippingMethods) != 1 {
		t.Fatalf("want only shop A's one active method, got %+v", parsed.ShippingMethods)
	}
	got := parsed.ShippingMethods[0]
	if int(got["id"].(float64)) != activeA || got["name"] != "台北市當日配送" || got["flat_rate"].(float64) != 200 {
		t.Fatalf("unexpected payload: %+v", got)
	}
	// The public shape is deliberately minimal (design D4): every key here is a
	// permanent v1 commitment.
	for _, key := range []string{"is_active", "created_at", "updated_at", "shop_id"} {
		if _, present := got[key]; present {
			t.Fatalf("%q must not be part of the public contract: %+v", key, got)
		}
	}
}

// ── guest parity (spec guest-checkout) ─────────────────────────────────

// Scenario: 訪客結帳附帶運送方式與指定配送日 / 品項各自附帶贈禮訊息 /
// 應付總額即為請款金額.
func TestGuestCheckoutSupportsTheSameOrderExtras(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()
	skuID := e.newSKU(t, e.shopA, 3600, "TWD", 10)
	methodID := e.newShippingMethod(t, e.shopA, "台北市當日配送", 200, true)
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")

	body := map[string]any{
		"email": "ondo-buyer@t.dev",
		"items": []map[string]any{
			{"sku_id": skuID, "quantity": 1, "gift_message": "給媽媽"},
			{"sku_id": skuID, "quantity": 1, "gift_message": "給岳母"},
		},
		"shipping_address": map[string]any{
			"recipient_name": "王小明", "phone": "0912345678", "line1": "台北市松山區民生東路一段1號",
		},
		"shipping_method_id":      methodID,
		"requested_delivery_date": tomorrow,
	}
	code, parsed := e.guestCall(t, e.shopADomain, "POST", "/api/v1/shop/guest/checkout", body)
	if code != http.StatusCreated {
		t.Fatalf("guest checkout: %d %+v", code, parsed)
	}
	o := parsed["order"].(map[string]any)

	subtotal, fee, total := amountsOf(t, o)
	if subtotal != 7200 || fee != 200 || total != 7400 {
		t.Fatalf("want 7200/200/7400, got %v/%v/%v", subtotal, fee, total)
	}
	if o["requested_delivery_date"] != tomorrow {
		t.Fatalf("delivery date must round-trip, got %v", o["requested_delivery_date"])
	}
	items := o["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("two cards on the same SKU means two order items, got %d", len(items))
	}

	// What the payment provider is asked to charge is the payable total, which
	// is the bug this change exists to fix.
	token := parsed["order_access_token"].(string)
	payCode, initiated := e.tokenCall(t, token, e.shopADomain, "POST", "/api/v1/shop/guest/order/payments", nil)
	if payCode != http.StatusCreated {
		t.Fatalf("initiate payment: %d %+v", payCode, initiated)
	}
	p := e.client.Payment.GetX(ctx, int(initiated["id"].(float64)))
	if p.Amount != 7400 {
		t.Fatalf("payment must charge total_amount (7400), got %d", p.Amount)
	}
}

// Scenario: 不同下單入口對同一輸入產生相同的金額組成.
func TestMemberAndGuestCheckoutComposeAmountsIdentically(t *testing.T) {
	e := newGuestEnv(t)
	skuID := e.newSKU(t, e.shopA, 1290, "TWD", 10)
	methodID := e.newShippingMethod(t, e.shopA, "全台隔日配送", 150, true)

	memberID := e.newPasswordlessMember(t, "parity-member@t.dev")
	code, memberOrder := e.memberCheckout(t, memberID, skuID, 1, map[string]any{"shipping_method_id": methodID})
	if code != http.StatusCreated {
		t.Fatalf("member checkout: %d %+v", code, memberOrder)
	}

	guestID, _ := e.checkoutAsGuestWithExtras(t, "parity-guest@t.dev", skuID, map[string]any{"shipping_method_id": methodID})
	guestOrder := e.client.Order.GetX(context.Background(), guestID)

	mSub, mFee, mTotal := amountsOf(t, memberOrder)
	if int64(mSub) != guestOrder.SubtotalAmount || int64(mFee) != guestOrder.ShippingFee || int64(mTotal) != guestOrder.TotalAmount {
		t.Fatalf("the two entry points disagreed: member %v/%v/%v vs guest %d/%d/%d",
			mSub, mFee, mTotal, guestOrder.SubtotalAmount, guestOrder.ShippingFee, guestOrder.TotalAmount)
	}
	if mTotal != 1440 {
		t.Fatalf("want 1290 + 150 = 1440, got %v", mTotal)
	}
}

// ── concurrency (spec order-management) ────────────────────────────────

// The amount/shipping logic added by this change all sits outside the checkout
// transaction; this proves it did not smuggle a new race into it. Structurally
// identical to TestCheckoutConcurrentOversellingProtection, which is kept
// unchanged, but every request now also resolves a shipping method.
func TestConcurrentCheckoutWithShippingMethodStillNeverOversells(t *testing.T) {
	e := newGuestEnv(t)
	ctx := context.Background()

	const stock = 5
	const n = 10
	skuID := e.newSKU(t, e.shopA, 500, "TWD", stock)
	methodID := e.newShippingMethod(t, e.shopA, "台北市當日配送", 200, true)
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")

	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := guestBody(fmt.Sprintf("race-extras-%d@t.dev", i), guestItem(skuID, 1))
			body["shipping_method_id"] = methodID
			body["requested_delivery_date"] = tomorrow
			code, _, err := e.guestRequest(e.shopADomain, "POST", "/api/v1/shop/guest/checkout", body)
			if err != nil {
				t.Error(err)
				return
			}
			codes[i] = code
		}(i)
	}
	wg.Wait()

	var succeeded int
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			succeeded++
		case http.StatusUnprocessableEntity:
		default:
			t.Errorf("unexpected status code: %d", c)
		}
	}
	if succeeded != stock {
		t.Errorf("want exactly %d successes, got %d (codes=%v)", stock, succeeded, codes)
	}
	if got := e.client.ProductSKU.GetX(ctx, skuID).StockQty; got != 0 {
		t.Errorf("final stock_qty: want 0, got %d", got)
	}

	// Every order that did succeed carries a consistent, fee-inclusive total —
	// the composition CHECK guarantees it, but assert it anyway since this is
	// the path where a partially-applied amount would show up.
	orders := e.client.Order.Query().AllX(ctx)
	for _, o := range orders {
		if o.TotalAmount != o.SubtotalAmount+o.ShippingFee || o.ShippingFee != 200 {
			t.Errorf("order %d has inconsistent amounts: %d + %d != %d", o.ID, o.SubtotalAmount, o.ShippingFee, o.TotalAmount)
		}
	}
}
