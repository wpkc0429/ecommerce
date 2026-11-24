package httpapi_test

// change order-and-invitation-polish, design D4: order items carry the SKU's
// option values as a checkout-time snapshot.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	entorderitem "ksdevworks/ecommerce/api/internal/ent/orderitem"
	entproductsku "ksdevworks/ecommerce/api/internal/ent/productsku"
)

// checkoutOrderWithOptions creates a SKU with the given options, checks it out
// and returns the order id plus the SKU id.
func (e *orderEnv) checkoutOrderWithOptions(t *testing.T, options string) (int, int) {
	t.Helper()
	_, skuID := e.newSKU(t, e.shopA, 1000, "TWD", 10, true, true)
	if options != "" {
		e.client.ProductSKU.UpdateOneID(skuID).SetOptions(json.RawMessage(options)).ExecX(context.Background())
	}
	e.addToCart(t, e.shopA, e.memberA1, skuID, 1)
	code, body := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "POST", "/api/v1/shop/checkout", validShippingAddress())
	if code != http.StatusCreated {
		t.Fatalf("checkout: %d %+v", code, body)
	}
	return int(body["id"].(float64)), skuID
}

func firstItemOptions(t *testing.T, ord map[string]any) any {
	t.Helper()
	items := ord["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("order has no items: %+v", ord)
	}
	item := items[0].(map[string]any)
	v, present := item["options"]
	if !present {
		t.Fatalf("item has no options key (must be present, possibly null): %+v", item)
	}
	return v
}

func TestOrderItemOptionsSnapshotAtCheckout(t *testing.T) {
	e := newOrderEnv(t)
	ctx := context.Background()
	orderID, skuID := e.checkoutOrderWithOptions(t, `{"size":"M","color":"red"}`)

	check := func(label string, ord map[string]any) {
		t.Helper()
		opts, ok := firstItemOptions(t, ord).(map[string]any)
		if !ok || opts["size"] != "M" || opts["color"] != "red" || len(opts) != 2 {
			t.Errorf("%s: options = %v, want {size:M,color:red}", label, opts)
		}
	}

	// Member (public) and admin responses both carry it.
	_, mine := e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", fmt.Sprintf("/api/v1/shop/orders/%d", orderID), nil)
	check("member get", mine)
	_, admin := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d", orderID)), nil)
	check("admin get", admin)

	// A later SKU edit, and even deleting the SKU, must not change history.
	e.client.ProductSKU.UpdateOneID(skuID).SetOptions(json.RawMessage(`{"size":"XL"}`)).ExecX(ctx)
	_, admin = e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d", orderID)), nil)
	check("admin get after SKU edit", admin)
	e.client.ProductSKU.Delete().Where(entproductsku.IDEQ(skuID)).ExecX(ctx)
	_, mine = e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", fmt.Sprintf("/api/v1/shop/orders/%d", orderID), nil)
	check("member get after SKU delete", mine)
}

func TestOrderItemOptionsEmptyObjectWhenSKUHasNone(t *testing.T) {
	e := newOrderEnv(t)
	orderID, _ := e.checkoutOrderWithOptions(t, "")
	_, admin := e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d", orderID)), nil)
	opts, ok := firstItemOptions(t, admin).(map[string]any)
	if !ok || len(opts) != 0 {
		t.Fatalf("options = %v, want {} (null is reserved for pre-snapshot orders)", firstItemOptions(t, admin))
	}
}

// An order created before the column existed reads options: null. It neither
// fails nor invents a value.
func TestOrderItemOptionsNullForLegacyRows(t *testing.T) {
	e := newOrderEnv(t)
	orderID, _ := e.checkoutOrderWithOptions(t, `{"size":"S"}`)
	e.client.OrderItem.Update().Where(entorderitem.OrderIDEQ(orderID)).ClearOptions().ExecX(context.Background())

	for label, fetch := range map[string]func() (int, map[string]any){
		"admin": func() (int, map[string]any) {
			return e.adminCallJSON(t, e.ownerA, "GET", e.shopPath(e.shopA, fmt.Sprintf("/orders/%d", orderID)), nil)
		},
		"member": func() (int, map[string]any) {
			return e.memberCallJSON(t, e.memberA1, e.shopA, e.shopADomain, "GET", fmt.Sprintf("/api/v1/shop/orders/%d", orderID), nil)
		},
	} {
		code, body := fetch()
		if code != http.StatusOK {
			t.Fatalf("%s get: %d", label, code)
		}
		if v := firstItemOptions(t, body); v != nil {
			t.Errorf("%s: legacy row options = %v, want null", label, v)
		}
	}
}

// orderOptionsBackfillSQL returns the backfill part of the migration, read from
// the file itself and never copied here (same discipline as shipping's backfill
// test): everything after the ADD COLUMN statement.
func orderOptionsBackfillSQL(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "20261124142354_add_order_item_options.up.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	const marker = `ADD COLUMN "options" jsonb NULL;`
	idx := strings.Index(string(raw), marker)
	if idx < 0 {
		t.Fatalf("migration no longer contains %q; if the backfill moved, this test must follow it", marker)
	}
	return string(raw)[idx+len(marker):]
}

// The migration's backfill: legacy order items whose SKU still exists get its
// current options, items whose SKU is gone stay NULL, and succeeded payments
// without paid_at get their updated_at while an existing paid_at and
// non-succeeded rows are untouched.
func TestMigrationBackfillOrderItemOptionsAndPaidAt(t *testing.T) {
	e := newOrderEnv(t)
	ctx := context.Background()

	withSKU, skuID := e.checkoutOrderWithOptions(t, `{"size":"L"}`)
	e.client.OrderItem.Update().Where(entorderitem.OrderIDEQ(withSKU)).ClearOptions().ExecX(ctx)
	e.client.ProductSKU.UpdateOneID(skuID).SetOptions(json.RawMessage(`{"size":"L","edited":"yes"}`)).ExecX(ctx)

	gone, goneSKU := e.checkoutOrderWithOptions(t, `{"size":"S"}`)
	e.client.OrderItem.Update().Where(entorderitem.OrderIDEQ(gone)).ClearOptions().ExecX(ctx)
	e.client.ProductSKU.Delete().Where(entproductsku.IDEQ(goneSKU)).ExecX(ctx)

	// payments: succeeded/no paid_at, succeeded/with paid_at, failed/no paid_at.
	mk := func(ref string, status int16) int {
		return e.client.Payment.Create().SetShopID(e.shopA).SetOrderID(withSKU).
			SetProvider("mock").SetProviderReference(ref).SetAmount(1000).SetCurrency("TWD").SetStatus(status).
			SaveX(ctx).ID
	}
	succNull := mk("bf-succ-null", 1)
	succSet := mk("bf-succ-set", 1)
	failed := mk("bf-failed", 2)
	existing := e.client.Payment.GetX(ctx, succSet).CreatedAt.AddDate(-1, 0, 0)
	e.client.Payment.UpdateOneID(succSet).SetPaidAt(existing).ExecX(ctx)

	if _, err := e.client.ExecContext(ctx, orderOptionsBackfillSQL(t)); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	item := e.client.OrderItem.Query().Where(entorderitem.OrderIDEQ(withSKU)).OnlyX(ctx)
	if got := string(item.Options); !strings.Contains(got, `"edited"`) || !strings.Contains(got, `"L"`) {
		t.Errorf("backfilled options = %s, want the SKU's current options", got)
	}
	goneItem := e.client.OrderItem.Query().Where(entorderitem.OrderIDEQ(gone)).OnlyX(ctx)
	if goneItem.Options != nil {
		t.Errorf("item whose SKU is gone got options %s, want NULL", goneItem.Options)
	}

	a := e.client.Payment.GetX(ctx, succNull)
	if a.PaidAt == nil || !a.PaidAt.Equal(a.UpdatedAt) {
		t.Errorf("succeeded/NULL paid_at = %v, want updated_at %v", a.PaidAt, a.UpdatedAt)
	}
	b := e.client.Payment.GetX(ctx, succSet)
	if b.PaidAt == nil || !b.PaidAt.Equal(existing) {
		t.Errorf("existing paid_at changed: %v -> %v", existing, b.PaidAt)
	}
	if c := e.client.Payment.GetX(ctx, failed); c.PaidAt != nil {
		t.Errorf("failed payment got paid_at %v", c.PaidAt)
	}
}
