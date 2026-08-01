package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"time"
)

// SMTPSender submits messages over SMTP. Configured against Gmail by default
// (design D9), but there is nothing Gmail-specific in here.
//
// ── Gmail operational notes, recorded so the next person does not lose an
//    afternoon to them ────────────────────────────────────────────────────
//
//   - Google removed "less secure app access", so Password MUST be an **App
//     Password** generated after enabling 2-step verification. An account
//     password fails authentication with a message that does not say so.
//   - Gmail rewrites From to the authenticated account unless the address is a
//     verified alias. So From defaults to Username; setting it to anything else
//     does not take effect, it only looks like it did.
//   - The daily cap for SMTP submission from a free gmail.com account is about
//     **100 messages**, not the 500 quoted for the web interface. Google
//     Workspace is 2,000/day/user. Exceeding it locks the whole account for
//     1–24 hours — every message, not just the one over the line. Design D9
//     turns those into two observable thresholds.
type SMTPSender struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
	// Timeout bounds the entire submission: dial, TLS handshake, auth and DATA.
	Timeout time.Duration
}

// Send submits one message.
//
// **smtp.SendMail is deliberately not used.** It dials with net.Dial, which has
// no timeout, so a black-holed route would park a worker on the kernel's TCP
// retry schedule (~130s on Linux) — with four workers that is the whole sending
// chain stalled for two minutes on one bad packet. Everything below exists to
// put a hard ceiling on that (design D5/D14).
func (s *SMTPSender) Send(ctx context.Context, msg Message) error {
	raw, err := msg.Encode(s.From, s.FromName)
	if err != nil {
		return err
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	dialer := &net.Dialer{Deadline: deadline}

	var conn net.Conn
	// Port 465 is implicit TLS ("SMTPS"); everything else negotiates STARTTLS
	// below. Gmail offers both; 587 is the default and the documented one.
	if s.Port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("notify: dial smtp: %w", err)
	}
	defer func() { _ = conn.Close() }()
	// One deadline covering every subsequent read and write. Without it, a
	// server that accepts the connection and then says nothing is
	// indistinguishable from a hang.
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("notify: set smtp deadline: %w", err)
	}

	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("notify: smtp handshake: %w", err)
	}
	defer func() { _ = c.Close() }()

	if s.Port != 465 {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			// Refusing is the point: credentials and the customer's address must
			// not cross the network in the clear because a server declined to
			// offer TLS.
			return fmt.Errorf("notify: smtp server %s does not offer STARTTLS", addr)
		}
		if err := c.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("notify: starttls: %w", err)
		}
	}

	if s.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("notify: smtp auth: %w", err)
		}
	}
	if err := c.Mail(s.From); err != nil {
		return fmt.Errorf("notify: smtp MAIL FROM: %w", err)
	}
	if err := c.Rcpt(msg.To); err != nil {
		// The recipient address is NOT included in the error (design D13): this
		// value ends up on order_notifications.error_reason and in a log line.
		return fmt.Errorf("notify: smtp RCPT TO rejected: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("notify: smtp DATA: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		_ = w.Close()
		return fmt.Errorf("notify: smtp write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("notify: smtp close body: %w", err)
	}
	return c.Quit()
}
