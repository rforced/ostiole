// Package fetch reads what a configuration names from elsewhere: address
// feeds and DNS blocklists. A URL is read from where it says, within
// three limits. Plain http reaches only this network, since anyone on
// the internet's path could add an entry to a list read in the clear. A
// redirect is followed here, hop by hop, so each is held to the same
// rules and none goes back from https to http. And nothing is read from
// this router's own addresses, loopback or link-local, where its own
// services and a cloud's metadata answer.
package fetch

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"syscall"
	"time"

	"ostiole/internal/model"
)

// Reach is where a request may connect.
type Reach int

const (
	// Named is a URL the configuration names.
	Named Reach = iota
	// Internet is a URL somebody typed who may not probe what else the
	// router reaches: public addresses only.
	Internet
)

var (
	// ErrNotPublic is an address Internet may not reach.
	ErrNotPublic = errors.New("not a public address; the router reads it once the alias is applied")
	// ErrNotAllowed is an address no list is read from.
	ErrNotAllowed = errors.New("this router's own address, loopback or link-local, which no list is read from")
	// ErrPlainHTTP is plain http to somewhere it may not go.
	ErrPlainHTTP = errors.New("plain http reaches only this network; use https")
)

// maxRedirects is where a chain stops, as Go's own client does.
const maxRedirects = 10

// rule is what one connection may reach.
type rule int

const (
	// anywhere is an https URL the configuration names.
	anywhere rule = iota
	// inside is a plain http one: this network only.
	inside
	// public is the internet only.
	public
	// typedPlain is plain http from somebody who may only read the
	// internet: never connected, but resolved first so the refusal can
	// say why.
	typedPlain
	rules
)

// Getter makes the requests. Each rule has its own client and its own
// connections, so one left open under a looser rule is never reused
// under a stricter one.
type Getter struct {
	allow   func(rule, netip.Addr) error
	clients [rules]*http.Client
}

var std = newGetter(allowedHere, nil)

// Inside is a Getter for tests, whose servers listen on the loopback: it
// takes the loopback for an address inside this network and holds it to
// the rules as that.
func Inside() *Getter {
	stand := netip.MustParseAddr("192.168.1.10")
	return newGetter(func(r rule, ip netip.Addr) error {
		if ip.Unmap().IsLoopback() {
			return allowed(r, stand, nil)
		}
		return allowedHere(r, ip)
	}, nil)
}

// newGetter checks each address with allow. A nil tlsConfig trusts the
// system's roots.
func newGetter(allow func(rule, netip.Addr) error, tlsConfig *tls.Config) *Getter {
	g := &Getter{allow: allow}
	for r := range rules {
		t := http.DefaultTransport.(*http.Transport).Clone()
		if tlsConfig != nil {
			t.TLSClientConfig = tlsConfig.Clone()
		}
		// A proxy would make the connection, past the check.
		t.Proxy = nil
		t.DialContext = (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			Control:   func(_, address string, _ syscall.RawConn) error { return g.check(r, address) },
		}).DialContext
		g.clients[r] = &http.Client{
			Transport:     t,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return g
}

func (g *Getter) check(r rule, address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	return g.allow(r, ip)
}

// Get asks for raw and answers with the response its redirects end at;
// the caller closes its body, and the context bounds reading it too. A
// nil Getter is the one with this router's own addresses.
func (g *Getter) Get(ctx context.Context, raw, userAgent string, reach Reach) (*http.Response, error) {
	if g == nil {
		g = std
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%q is not a URL", model.RedactURL(raw))
	}
	secure := false
	for hop := 0; ; hop++ {
		var r rule
		switch {
		case u.Scheme == "https" && reach == Internet:
			r = public
		case u.Scheme == "https":
			r = anywhere
		case u.Scheme == "http" && secure:
			return nil, ErrPlainHTTP
		case u.Scheme == "http" && reach == Internet:
			r = typedPlain
		case u.Scheme == "http":
			r = inside
		default:
			return nil, fmt.Errorf("%q is not an http or https URL", model.RedactURL(u.String()))
		}
		secure = secure || u.Scheme == "https"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		if userAgent != "" {
			req.Header.Set("User-Agent", userAgent)
		}
		resp, err := g.clients[r].Do(req)
		if err != nil {
			// Go hides a password in the URL it repeats, not a token
			// in the user part or the query.
			if ue, ok := errors.AsType[*url.Error](err); ok {
				ue.URL = model.RedactURL(ue.URL)
			}
			return nil, err
		}
		next, err := resp.Location()
		if !redirect(resp.StatusCode) || err != nil {
			return resp, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		if hop == maxRedirects {
			return nil, fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		u = next
	}
}

func redirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// allowedHere is allowed against this router's addresses as they are now.
func allowedHere(r rule, ip netip.Addr) error {
	own, err := ownAddrs()
	if err != nil {
		return err
	}
	return allowed(r, ip, own)
}

// allowed says whether a connection under r may go to ip, one of own
// being this router's.
func allowed(r rule, ip netip.Addr, own []netip.Addr) error {
	ip = ip.Unmap().WithZone("")
	if !ip.IsGlobalUnicast() || slices.Contains(own, ip) {
		return ErrNotAllowed
	}
	inner := ip.IsPrivate() || ip.Is4() && cgnat.Contains(ip)
	switch {
	case (r == public || r == typedPlain) && inner:
		return ErrNotPublic
	case (r == inside || r == typedPlain) && !inner:
		return ErrPlainHTTP
	}
	return nil
}

// cgnat is carrier-grade NAT, which a tailnet numbers itself from too.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// ownAddrs is every address on this router's interfaces. A connection to
// its public one would reach its own services.
func ownAddrs() ([]netip.Addr, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("list this router's addresses: %w", err)
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, p.Addr().Unmap())
		}
	}
	return out, nil
}
