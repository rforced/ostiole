package feeds

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"syscall"
	"time"
)

// ErrNotPublic is an operator's URL that leads somewhere other than the
// internet. What the configuration names is fetched from anywhere.
var ErrNotPublic = errors.New("not a public address; the router reads it once the alias is applied")

// publicClient reads a URL an operator typed. The address is checked on
// every connection, so neither a name that resolves inward nor a redirect
// gets past it, and it keeps its own connections: one a configured fetch
// left open to a private address is never reused here.
var publicClient = newPublicClient()

func newPublicClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	// A proxy would make the connection to the list, past the check.
	t.Proxy = nil
	t.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: dialPublic}).DialContext
	return &http.Client{Timeout: DefaultTimeout, Transport: t}
}

// dialPublic refuses a connection to an address that is not public, or is
// this router's own.
func dialPublic(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	own, err := ownAddrs()
	if err != nil {
		return err
	}
	if !public(ip, own) {
		return ErrNotPublic
	}
	return nil
}

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

// public reports whether ip is on the internet and not one of own: not
// private, unique local, loopback, link-local or carrier-grade NAT, which
// the tailnet uses too.
func public(ip netip.Addr, own []netip.Addr) bool {
	ip = ip.Unmap()
	switch {
	case !ip.IsGlobalUnicast(), ip.IsPrivate(), ip.Is4() && cgnat.Contains(ip):
		return false
	}
	return !slices.Contains(own, ip.WithZone(""))
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")
