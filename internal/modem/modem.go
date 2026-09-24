// Package modem reads what a cable modem on the WAN link says about
// itself: how far DOCSIS provisioning got, the channels it is locked to
// with their power and error counts, and the link to the router. It is
// what to look at when the WAN will not come up and the question is
// whether the modem is the reason. Nothing is polled; a status is read
// when a page asks for it and kept for a minute.
package modem

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// DefaultAddress is where a DOCSIS modem answers by convention.
const DefaultAddress = "192.168.100.1"

// modemNet is where a cable modem answers: 192.168.100.1 by the DOCSIS
// specification, and the rest of the /24 for one that was moved.
var modemNet = netip.MustParsePrefix("192.168.100.0/24")

var (
	// ErrBadAddress says the address is not one a modem could have. The
	// page reads a modem; it is not a way to probe what else the router
	// can reach.
	ErrBadAddress = errors.New("modem address must be in 192.168.100.0/24, where cable modems answer")
	// ErrNoModem says nothing at the address answered like a modem this
	// package knows.
	ErrNoModem = errors.New("no modem Ostiole can read answered")
)

// Status is what a modem reports.
type Status struct {
	Address  string `json:"address"`
	Vendor   string `json:"vendor"`
	Model    string `json:"model"`
	Hardware string `json:"hardware,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	Serial   string `json:"serial,omitempty"`
	// MAC is the cable-side address, the one the provider knows the modem by.
	MAC    string `json:"mac,omitempty"`
	Uptime string `json:"uptime,omitempty"`
	// Clock is the modem's own idea of the time, as it prints it. A modem
	// that never got past time-of-day shows it here.
	Clock string `json:"clock,omitempty"`
	Link  Link   `json:"link"`
	// Provisioning is the DOCSIS start-up sequence, in order. The first
	// step that is not OK is where the WAN stopped.
	Provisioning []Step       `json:"provisioning"`
	Downstream   []Downstream `json:"downstream"`
	Upstream     []Upstream   `json:"upstream"`
	FetchedAt    time.Time    `json:"fetchedAt"`
}

// Link is the Ethernet side, between the modem and the router.
type Link struct {
	Up     bool   `json:"up"`
	Speed  string `json:"speed,omitempty"`
	Duplex string `json:"duplex,omitempty"`
}

// Step is one stage of DOCSIS provisioning.
type Step struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	OK     bool   `json:"ok"`
}

// Downstream is one receive channel. Kind is "qam" for a single-carrier
// channel and "ofdm" for a DOCSIS 3.1 block.
type Downstream struct {
	Channel    int    `json:"channel"`
	Kind       string `json:"kind"`
	Frequency  int64  `json:"frequency"` // Hz
	Modulation string `json:"modulation,omitempty"`
	Locked     bool   `json:"locked"`
	// Power is the receive level in dBmV; SNR in dB.
	Power         float64 `json:"power"`
	SNR           float64 `json:"snr"`
	Octets        uint64  `json:"octets"`
	Corrected     uint64  `json:"corrected"`
	Uncorrectable uint64  `json:"uncorrectable"`
}

// Upstream is one transmit channel. Kind is "qam" or "ofdma".
type Upstream struct {
	Channel    int    `json:"channel"`
	Kind       string `json:"kind"`
	Frequency  int64  `json:"frequency"`           // Hz
	Bandwidth  int64  `json:"bandwidth,omitempty"` // Hz
	Modulation string `json:"modulation,omitempty"`
	Mode       string `json:"mode,omitempty"`
	// Power is the transmit level in dBmV.
	Power float64 `json:"power"`
}

// driver knows one vendor's pages.
type driver interface {
	// detect says whether the pages at base are this vendor's. A
	// connection failure is an error; a page that is not this vendor's
	// is false, nil.
	detect(ctx context.Context, c *client) (bool, error)
	fetch(ctx context.Context, c *client) (*Status, error)
}

var drivers = []driver{hitron{}}

// client is one modem's base URL and the HTTP client to reach it with.
type client struct {
	base string
	http *http.Client
}

// newHTTPClient trusts whatever certificate the modem presents: it is
// self-signed, for a private address, and there is nothing to verify it
// against. The connection never leaves the WAN link.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
			DialContext:       (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the modem's certificate is self-signed for a private address
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// checkAddress accepts an address in the modem subnet, without a port.
func checkAddress(address string) error {
	ip, err := netip.ParseAddr(address)
	if err != nil || !modemNet.Contains(ip) {
		return fmt.Errorf("%w: %q", ErrBadAddress, address)
	}
	return nil
}

// Fetch reads the modem at address. HTTPS is tried first, then plain
// HTTP: a Hitron answers only the first, an Arris only the second.
func Fetch(ctx context.Context, address string) (*Status, error) {
	if err := checkAddress(address); err != nil {
		return nil, err
	}
	return fetchWith(ctx, newHTTPClient(), address)
}

// fetchWith reads the modem at address, which the caller has checked.
func fetchWith(ctx context.Context, hc *http.Client, address string) (*Status, error) {
	for _, scheme := range []string{"https", "http"} {
		c := &client{base: scheme + "://" + address, http: hc}
		for _, d := range drivers {
			ok, err := d.detect(ctx, c)
			if err != nil {
				break // the scheme is wrong, not the driver
			}
			if !ok {
				continue
			}
			st, err := d.fetch(ctx, c)
			if err != nil {
				return nil, err
			}
			st.Address = address
			st.FetchedAt = time.Now()
			return st, nil
		}
	}
	// Why the connection failed stays here: a refused port reads
	// differently from a filtered one, and that is a port scan.
	return nil, fmt.Errorf("%w at %s", ErrNoModem, address)
}

// Cache keeps the last status read per address for a while, so a page
// that is refreshed does not have the modem read its pages every time.
type Cache struct {
	ttl   time.Duration
	fetch func(ctx context.Context, address string) (*Status, error)
	mu    sync.Mutex
	last  map[string]*Status
}

// NewCache returns a cache that keeps a status for ttl.
func NewCache(ttl time.Duration) *Cache {
	return NewCacheWith(ttl, Fetch)
}

// NewCacheWith is NewCache with the read swapped out, for tests.
func NewCacheWith(ttl time.Duration, fetch func(ctx context.Context, address string) (*Status, error)) *Cache {
	return &Cache{ttl: ttl, fetch: fetch, last: map[string]*Status{}}
}

// Get returns the status for address, reading it again when the kept one
// is older than the ttl or refresh is set. A failed read is not kept.
func (c *Cache) Get(ctx context.Context, address string, refresh bool) (*Status, error) {
	if err := checkAddress(address); err != nil {
		return nil, err
	}
	c.mu.Lock()
	st := c.last[address]
	c.mu.Unlock()
	if st != nil && !refresh && time.Since(st.FetchedAt) < c.ttl {
		return st, nil
	}
	st, err := c.fetch(ctx, address)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.last[address] = st
	c.mu.Unlock()
	return st, nil
}
