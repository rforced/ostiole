package notify

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// testCert is a certificate for 127.0.0.1 and the pool that trusts it.
func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mail.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// received is one mail the fake server took.
type received struct {
	from string
	to   []string
	auth string
	tls  bool
	data string
}

// smtpServer speaks just enough SMTP for net/smtp: EHLO, STARTTLS, AUTH
// PLAIN, MAIL, RCPT, DATA and QUIT.
type smtpServer struct {
	addr     string
	starttls bool
	cfg      *tls.Config
	mu       sync.Mutex
	got      []received
}

func newSMTP(t *testing.T, implicit, starttls bool) (*smtpServer, *x509.CertPool) {
	t.Helper()
	cert, pool := testCert(t)
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	var ln net.Listener
	var err error
	if implicit {
		ln, err = tls.Listen("tcp", "127.0.0.1:0", cfg)
	} else {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := &smtpServer{addr: ln.Addr().String(), starttls: starttls, cfg: cfg}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn, implicit)
		}
	}()
	return s, pool
}

func (s *smtpServer) serve(conn net.Conn, secure bool) {
	defer func() { _ = conn.Close() }()
	r, w := bufio.NewReader(conn), conn
	say := func(line string) { _, _ = io.WriteString(w, line+"\r\n") }
	var m received
	m.tls = secure
	say("220 mail.test ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO":
			if s.starttls && !m.tls {
				say("250-mail.test")
				say("250-STARTTLS")
			} else {
				say("250-mail.test")
			}
			say("250 AUTH PLAIN")
		case "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(conn, s.cfg)
			if tc.Handshake() != nil {
				return
			}
			conn, r, w, m.tls = tc, bufio.NewReader(tc), tc, true
		case "AUTH":
			_, b64, _ := strings.Cut(arg, " ")
			raw, _ := base64.StdEncoding.DecodeString(b64)
			m.auth = string(raw)
			say("235 ok")
		case "MAIL":
			m.from = arg
			say("250 ok")
		case "RCPT":
			m.to = append(m.to, arg)
			say("250 ok")
		case "DATA":
			say("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			m.data = b.String()
			s.mu.Lock()
			s.got = append(s.got, m)
			s.mu.Unlock()
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 what")
		}
	}
}

func (s *smtpServer) mails() []received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]received(nil), s.got...)
}

func sendMail(t *testing.T, e model.NotifyEmail, pool *x509.CertPool) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := Delivery{TLS: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	return d.Send(ctx, TargetEmail, model.Notifications{Email: e}, down)
}

// The mail goes over STARTTLS with a login, to everyone, and reads as the
// notice.
func TestMailOverSTARTTLS(t *testing.T) {
	t.Parallel()
	srv, pool := newSMTP(t, false, true)
	e := model.NotifyEmail{
		Enabled: true, Server: srv.addr, Username: "router", Password: "hunter2",
		From: "Router <router@example.net>", To: []string{"a@example.net", "Bee <b@example.net>"},
	}
	if err := sendMail(t, e, pool); err != nil {
		t.Fatal(err)
	}
	got := srv.mails()
	if len(got) != 1 {
		t.Fatalf("mails = %d", len(got))
	}
	m := got[0]
	if !m.tls || m.auth != "\x00router\x00hunter2" || m.from != "FROM:<router@example.net>" || len(m.to) != 2 {
		t.Errorf("envelope = %+v", m)
	}
	msg, err := mail.ReadMessage(strings.NewReader(m.data))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if subject != "fw: Gateway wan is down" || msg.Header.Get("Auto-Submitted") != "auto-generated" ||
		!strings.Contains(msg.Header.Get("To"), "b@example.net") || !strings.HasPrefix(string(body), "Gateway wan is down\r\n") {
		t.Errorf("mail:\n%s", m.data)
	}
}

// Port 465 is TLS from the first byte, and a relay needs no login.
func TestMailOverTLSWithoutALogin(t *testing.T) {
	t.Parallel()
	srv, pool := newSMTP(t, true, false)
	e := model.NotifyEmail{Enabled: true, Server: srv.addr, Security: model.SMTPTLS, From: "r@example.net", To: []string{"a@example.net"}}
	if err := sendMail(t, e, pool); err != nil {
		t.Fatal(err)
	}
	if got := srv.mails(); len(got) != 1 || got[0].auth != "" || !got[0].tls {
		t.Errorf("mails = %+v", got)
	}
}

// A server that cannot encrypt is not sent a password, and one whose
// certificate does not check out is not sent anything.
func TestMailRefusesAnUnprotectedServer(t *testing.T) {
	t.Parallel()
	plain, pool := newSMTP(t, false, false)
	e := model.NotifyEmail{Enabled: true, Server: plain.addr, Username: "router", Password: "hunter2", From: "r@example.net", To: []string{"a@example.net"}}
	if err := sendMail(t, e, pool); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("err = %v", err)
	}
	strange, _ := newSMTP(t, false, true)
	e.Server = strange.addr
	if err := sendMail(t, e, x509.NewCertPool()); err == nil {
		t.Error("a certificate nobody vouches for was accepted")
	}
	if len(plain.mails())+len(strange.mails()) != 0 {
		t.Error("a mail went through")
	}
}
