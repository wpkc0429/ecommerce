package notify

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// bodyOf decodes the base64 body out of an encoded message, so assertions can be
// made against what the recipient actually reads rather than against the wire
// form.
func bodyOf(t *testing.T, raw []byte) string {
	t.Helper()
	_, body, ok := strings.Cut(string(raw), "\r\n\r\n")
	if !ok {
		t.Fatalf("encoded message has no header/body separator:\n%s", raw)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(body, "\r\n", ""))
	if err != nil {
		t.Fatalf("body is not valid base64: %v", err)
	}
	return string(decoded)
}

// TestEncodeChineseSubjectIsRFC2047 — a raw Chinese subject is 8-bit data in a
// header field defined as ASCII. It renders as mojibake or gets the message
// rejected, and neither shows up on a developer's machine.
func TestEncodeChineseSubjectIsRFC2047(t *testing.T) {
	msg := Message{To: "buyer@example.com", Subject: "【花店】訂單成立通知（訂單編號 #1234）", TextBody: "您好"}
	raw, err := msg.Encode("shop@example.com", "")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, "Subject: =?UTF-8?") {
		t.Errorf("subject is not RFC 2047 encoded:\n%s", got)
	}
	if strings.Contains(got, "Subject: 【") {
		t.Error("subject was written raw into the header")
	}
}

// TestEncodeBodyIsBase64AndRoundTrips — SMTP treats the body as 7-bit unless
// 8BITMIME is negotiated end to end; one relay without it corrupts every
// Chinese character.
func TestEncodeBodyIsBase64AndRoundTrips(t *testing.T) {
	body := "您好，\n\n感謝您在「花店」下單。\n訂單編號：#1234\n應付總額：NT$3,400\n"
	raw, err := Message{To: "buyer@example.com", Subject: "x", TextBody: body}.Encode("shop@example.com", "")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(string(raw), "Content-Transfer-Encoding: base64") {
		t.Error("body encoding is not declared as base64")
	}
	if !strings.Contains(string(raw), "Content-Type: text/plain; charset=\"UTF-8\"") {
		t.Error("content type is not text/plain UTF-8")
	}
	if got := bodyOf(t, raw); got != body {
		t.Errorf("body did not round-trip.\n got: %q\nwant: %q", got, body)
	}
}

// TestEncodeFoldsBase64Lines — RFC 2045 caps encoded lines at 76 characters and
// some MTAs enforce it. A large basket's confirmation easily exceeds any such
// limit in one unbroken line.
func TestEncodeFoldsBase64Lines(t *testing.T) {
	raw, err := Message{To: "b@example.com", Subject: "x", TextBody: strings.Repeat("訂單內容 ", 200)}.
		Encode("shop@example.com", "")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	_, body, _ := strings.Cut(string(raw), "\r\n\r\n")
	for i, line := range strings.Split(strings.TrimRight(body, "\r\n"), "\r\n") {
		if len(line) > 76 {
			t.Fatalf("base64 line %d is %d characters (max 76)", i, len(line))
		}
	}
}

// TestEncodeRejectsHeaderInjection — shops.name is merchant-entered free text
// and reaches both the subject and the From display name. A bare CRLF there
// would let a merchant append headers (Bcc, say) to every email their shop
// sends.
func TestEncodeRejectsHeaderInjection(t *testing.T) {
	for name, msg := range map[string]Message{
		"recipient": {To: "buyer@example.com\r\nBcc: attacker@example.com", Subject: "x", TextBody: "y"},
		"subject":   {To: "buyer@example.com", Subject: "訂單\r\nBcc: attacker@example.com", TextBody: "y"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := msg.Encode("shop@example.com", ""); !errors.Is(err, ErrHeaderInjection) {
				t.Fatalf("want ErrHeaderInjection, got %v", err)
			}
		})
	}
	t.Run("from display name", func(t *testing.T) {
		msg := Message{To: "buyer@example.com", Subject: "x", TextBody: "y"}
		if _, err := msg.Encode("shop@example.com", "花店\r\nBcc: attacker@example.com"); !errors.Is(err, ErrHeaderInjection) {
			t.Fatalf("want ErrHeaderInjection, got %v", err)
		}
	})
}

func TestEncodeRejectsEmptyRecipient(t *testing.T) {
	if _, err := (Message{Subject: "x", TextBody: "y"}).Encode("shop@example.com", ""); err == nil {
		t.Fatal("want an error for a message with no recipient")
	}
}

// TestMessageStringOmitsRecipientAndBody — %v on a Message must not become an
// accidental PII leak in a test failure or a log line (design D13).
func TestMessageStringOmitsRecipientAndBody(t *testing.T) {
	s := Message{To: "buyer@example.com", Subject: "訂單成立", TextBody: "王小明 0912345678"}.String()
	for _, secret := range []string{"buyer@example.com", "王小明", "0912345678"} {
		if strings.Contains(s, secret) {
			t.Errorf("Message.String() leaked %q: %s", secret, s)
		}
	}
}
