package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// Delivery sends for real: mail through an SMTP server, webhooks over
// HTTPS.
type Delivery struct {
	// Client posts to webhooks; nil uses one that follows no redirect.
	Client *http.Client
	// TLS is what mail servers are checked against; nil is the system's
	// roots. Tests trust their own server here.
	TLS *tls.Config
}

// Send implements Sender.
func (d Delivery) Send(ctx context.Context, target string, n model.Notifications, m Message) error {
	switch target {
	case TargetEmail:
		return d.mail(ctx, n.Email, m)
	case TargetWebhook:
		return d.post(ctx, n.Webhook, m)
	}
	return fmt.Errorf("no such target %q", target)
}

func (d Delivery) tlsFor(host string) *tls.Config {
	c := &tls.Config{MinVersion: tls.VersionTLS12}
	if d.TLS != nil {
		c = d.TLS.Clone()
	}
	c.ServerName = host
	return c
}

// mail sends m as one mail to every recipient.
func (d Delivery) mail(ctx context.Context, e model.NotifyEmail, m Message) error {
	from, err := mail.ParseAddress(e.From)
	if err != nil {
		return fmt.Errorf("sender: %w", err)
	}
	to := make([]*mail.Address, 0, len(e.To))
	for _, raw := range e.To {
		a, err := mail.ParseAddress(raw)
		if err != nil {
			return fmt.Errorf("recipient: %w", err)
		}
		to = append(to, a)
	}
	body, err := compose(from, to, m, time.Now())
	if err != nil {
		return err
	}

	addr := e.Address()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	var conn net.Conn
	if e.SecurityOr() == model.SMTPTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: d.tlsFor(host)}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	// net/smtp takes no context, so the connection carries the deadline
	// and a cancelled context closes it.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()
	if e.SecurityOr() == model.SMTPStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the mail server does not offer STARTTLS")
		}
		if err := c.StartTLS(d.tlsFor(host)); err != nil {
			return err
		}
	}
	if e.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("the mail server offers no login")
		}
		if err := c.Auth(smtp.PlainAuth("", e.Username, e.Password, host)); err != nil {
			return err
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	for _, a := range to {
		if err := c.Rcpt(a.Address); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// compose writes m as a plain text mail.
func compose(from *mail.Address, to []*mail.Address, m Message, now time.Time) ([]byte, error) {
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	domain := "ostiole"
	if _, d, ok := strings.Cut(from.Address, "@"); ok && d != "" {
		domain = d
	}
	recipients := make([]string, len(to))
	for i, a := range to {
		recipients[i] = a.String()
	}
	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", from.String())
	header("To", strings.Join(recipients, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", m.Subject()))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id[:])+"@"+domain+">")
	// Nobody's out-of-office reply should come back to a router.
	header("Auto-Submitted", "auto-generated")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	// The writer turns each line end into CRLF.
	if _, err := qp.Write([]byte(m.Text() + "\n")); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
