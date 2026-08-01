package notify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"ksdevworks/ecommerce/api/internal/notify"
	"ksdevworks/ecommerce/api/internal/order"
)

// TestNotificationLogsCarryNoPII — design D13.
//
// The email itself necessarily contains the recipient's name, phone, address and
// card message; it is going to the person who typed them. The boundary is the
// log, and the proof has to land on **bytes**, not on a reading of the code —
// the same discipline change member-directory used for password_hash. Two
// implementations (the logger calls here, the sender's error strings) are each
// individually correct today, and either can be undone by one future line.
//
// Both terminal paths are exercised: a successful send and a failed one. The
// failure path is the interesting one, because that is where an error string
// travels into both a log line and order_notifications.error_reason.
func TestNotificationLogsCarryNoPII(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	// Values chosen so a substring match cannot be a coincidence.
	const (
		name        = "ZZRecipientNameZZ"
		phone       = "0900111222333"
		line1       = "ZZAddressLineZZ"
		giftMessage = "ZZGiftMessageZZ"
		email       = "zz-pii-probe@example.com"
	)

	member := f.client.Member.Create().SetShopID(f.shopID).SetEmail(email).SetStatus(1).SaveX(ctx)
	addr, err := json.Marshal(order.ShippingAddress{RecipientName: name, Phone: phone, Line1: line1})
	if err != nil {
		t.Fatal(err)
	}
	o := f.client.Order.Create().
		SetShopID(f.shopID).SetMemberID(member.ID).
		SetStatus(order.StatusCreated).
		SetPaymentStatus(order.PaymentStatusUnpaid).
		SetFulfillmentStatus(order.FulfillmentStatusUnfulfilled).
		SetCurrency("TWD").SetSubtotalAmount(3400).SetTotalAmount(3400).
		SetShippingAddress(addr).
		SaveX(ctx)
	f.client.OrderItem.Create().
		SetShopID(f.shopID).SetOrderID(o.ID).
		SetProductTitle("花束").SetSkuCode("SKU-1").
		SetQuantity(1).SetPriceAmount(3400).SetCurrency("TWD").SetGiftMessage(giftMessage).
		SaveX(ctx)

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Path 1: a successful send.
	ok := &notify.RecordingSender{}
	if err := (&notify.Service{Client: f.client, Sender: ok, Log: logger}).
		DeliverEvent(ctx, placed(f.shopID, o.ID, member.ID)); err != nil {
		t.Fatalf("DeliverEvent: %v", err)
	}
	// Sanity: the message really did contain the PII, so a clean log is a
	// property of the logging and not of an empty email.
	msgs := ok.Messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0].TextBody, name) {
		t.Fatalf("fixture is wrong: the email does not contain the probe values")
	}

	// Path 2: a failed send, with the failure logged by the worker wrapper.
	broken := &notify.RecordingSender{Err: errors.New("notify: smtp RCPT TO rejected: 550 mailbox unavailable")}
	svc := &notify.Service{Client: f.client, Sender: broken, Log: logger}
	if err := svc.DeliverEvent(ctx, paid(f.shopID, o.ID, member.ID)); err != nil {
		logger.Error("notify: delivery failed",
			"shop_id", f.shopID, "order_id", o.ID, "kind", notify.KindPaymentSucceeded, "err", err)
	} else {
		t.Fatal("the broken sender should have failed")
	}

	// Path 3: a customer with no address at all — the skip log.
	silent := f.client.Member.Create().SetShopID(f.shopID).SetStatus(1).SaveX(ctx)
	o2 := f.client.Order.Create().
		SetShopID(f.shopID).SetMemberID(silent.ID).
		SetStatus(order.StatusCreated).
		SetPaymentStatus(order.PaymentStatusUnpaid).
		SetFulfillmentStatus(order.FulfillmentStatusUnfulfilled).
		SetCurrency("TWD").SetSubtotalAmount(1).SetTotalAmount(1).
		SetShippingAddress(json.RawMessage(`{}`)).
		SaveX(ctx)
	if err := svc.DeliverEvent(ctx, placed(f.shopID, o2.ID, silent.ID)); err != nil {
		t.Fatalf("skip path: %v", err)
	}

	out := logs.String()
	for _, secret := range []string{name, phone, line1, giftMessage, email, "訂單成立通知", "應付總額"} {
		if strings.Contains(out, secret) {
			t.Errorf("notification logs leaked %q:\n%s", secret, out)
		}
	}
}
