package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// tlsHandshake is the record type every TLS connection opens with.
const tlsHandshake = 0x16

// errRedirected fails the TLS handshake once the redirect is written, so
// the server closes the connection.
var errRedirected = errors.New("plain HTTP request redirected to HTTPS")

// serverLog takes net/http's own messages at error level, except the
// handshake a redirect ends, which is routine.
type serverLog struct{ *slog.Logger }

func (l serverLog) Write(p []byte) (int, error) {
	msg := strings.TrimSuffix(string(p), "\n")
	level := slog.LevelError
	if strings.HasSuffix(msg, errRedirected.Error()) {
		level = slog.LevelDebug
	}
	l.Log(context.Background(), level, msg)
	return len(p), nil
}

// redirectListener goes under the TLS listener and answers a plain HTTP
// request with a redirect to HTTPS on the same port, where Go would
// answer 400.
type redirectListener struct{ net.Listener }

func (l redirectListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &redirectConn{Conn: c}, nil
}

// redirectConn tells TLS from HTTP by the first byte. The TLS handshake
// makes that first read, so it runs in the connection's own goroutine and
// under the server's handshake deadline.
type redirectConn struct {
	net.Conn
	sniffed bool
}

func (c *redirectConn) Read(p []byte) (int, error) {
	if c.sniffed || len(p) == 0 {
		return c.Conn.Read(p)
	}
	if _, err := io.ReadFull(c.Conn, p[:1]); err != nil {
		return 0, err
	}
	c.sniffed = true
	if p[0] == tlsHandshake {
		return 1, nil
	}
	return 0, c.redirect(p[0])
}

// redirect reads the rest of the plain request and sends the browser to
// the same host, port and path over HTTPS.
func (c *redirectConn) redirect(first byte) error {
	r := io.MultiReader(bytes.NewReader([]byte{first}), c.Conn)
	req, err := http.ReadRequest(bufio.NewReader(io.LimitReader(r, http.DefaultMaxHeaderBytes)))
	if err != nil {
		return fmt.Errorf("neither TLS nor HTTP: %w", err)
	}
	// Host carries the port the browser used, which a port forward may
	// have changed. Without a usable one, use the address this connection
	// reached.
	host := req.Host
	if host == "" || !httpguts.ValidHostHeader(host) {
		host = c.LocalAddr().String()
	}
	path := req.URL.RequestURI()
	if !strings.HasPrefix(path, "/") {
		path = "/"
	}
	if _, err := fmt.Fprintf(c.Conn, "HTTP/1.1 307 Temporary Redirect\r\nLocation: https://%s%s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", host, path); err != nil {
		return err
	}
	return errRedirected
}
