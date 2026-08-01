package notify

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// These tests exercise SMTPSender against a local listener only. **No test in
// this package connects to a real mail service**, which is a hard constraint of
// this change — and the reason the sending seam is an interface with a
// RecordingSender behind it for everything above this file.

// plainSMTPServer answers just enough of the protocol to get a client past the
// greeting and EHLO, and deliberately advertises no STARTTLS.
func plainSMTPServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				w := bufio.NewWriter(conn)
				r := bufio.NewReader(conn)
				_, _ = w.WriteString("220 localhost ESMTP test\r\n")
				_ = w.Flush()
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(strings.ToUpper(line), "EHLO"),
						strings.HasPrefix(strings.ToUpper(line), "HELO"):
						// 250-<domain> then a final 250 with no STARTTLS.
						_, _ = w.WriteString("250-localhost\r\n250 SIZE 10240000\r\n")
					case strings.HasPrefix(strings.ToUpper(line), "QUIT"):
						_, _ = w.WriteString("221 Bye\r\n")
						_ = w.Flush()
						return
					default:
						_, _ = w.WriteString("250 OK\r\n")
					}
					_ = w.Flush()
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func hostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	var p int
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	return host, p
}

// TestSendRefusesToTransmitWithoutSTARTTLS — a server that does not offer TLS
// gets no message. Credentials and the customer's address are not worth sending
// in the clear because the other end declined to protect them, and "it worked,
// just unencrypted" is the failure nobody notices.
func TestSendRefusesToTransmitWithoutSTARTTLS(t *testing.T) {
	host, port := hostPort(t, plainSMTPServer(t))
	s := &SMTPSender{
		Host: host, Port: port,
		Username: "user@example.com", Password: "app-password",
		From: "user@example.com", Timeout: 3 * time.Second,
	}
	err := s.Send(context.Background(), Message{To: "buyer@example.com", Subject: "x", TextBody: "y"})
	if err == nil {
		t.Fatal("want an error when the server offers no STARTTLS")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("error should name the missing capability, got: %v", err)
	}
}

// TestSendFailsFastOnAnUnreachableServer — the timeout is the entire point of
// hand-rolling the SMTP conversation instead of calling smtp.SendMail (which
// dials with no timeout at all). Without it, one black-holed route parks a
// worker on the kernel's TCP retry schedule for ~130 seconds.
func TestSendFailsFastOnAnUnreachableServer(t *testing.T) {
	// A listener opened and immediately closed yields a port nothing answers on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	host, port := hostPort(t, addr)

	s := &SMTPSender{Host: host, Port: port, From: "user@example.com", Timeout: 2 * time.Second}
	start := time.Now()
	if err := s.Send(context.Background(), Message{To: "b@example.com", Subject: "x", TextBody: "y"}); err == nil {
		t.Fatal("want an error for an unreachable server")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Send took %s — the deadline is not being applied", elapsed)
	}
}

// TestSendRejectsHeaderInjectionBeforeDialing — encoding happens first, so a
// poisoned header never becomes a network connection either.
func TestSendRejectsHeaderInjectionBeforeDialing(t *testing.T) {
	s := &SMTPSender{
		// An address that would take a long time to fail if it were ever dialled.
		Host: "192.0.2.1", Port: 587, From: "user@example.com", Timeout: time.Minute,
	}
	start := time.Now()
	err := s.Send(context.Background(), Message{
		To: "buyer@example.com\r\nBcc: attacker@example.com", Subject: "x", TextBody: "y",
	})
	if !errors.Is(err, ErrHeaderInjection) {
		t.Fatalf("want ErrHeaderInjection, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Send dialled before validating headers (took %s)", elapsed)
	}
}
