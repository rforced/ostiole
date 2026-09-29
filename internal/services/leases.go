package services

import (
	"bufio"
	"errors"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Lease is one entry of the dnsmasq lease file.
type Lease struct {
	// Expires is zero for a lease that never expires, which dnsmasq
	// writes as 0.
	Expires time.Time `json:"expires,omitzero"`
	// MAC is empty for IPv6 leases: DHCPv6 identifies clients by DUID.
	MAC      string `json:"mac,omitempty"`
	IP       string `json:"ip"`
	Hostname string `json:"hostname,omitempty"`
	ClientID string `json:"clientId,omitempty"`
	Family   int    `json:"family"` // 4 or 6
}

// ReadLeases parses the lease file. A missing file is an empty list.
func (d *Dnsmasq) ReadLeases() ([]Lease, error) {
	f, err := os.Open(d.leases())
	if errors.Is(err, os.ErrNotExist) {
		return []Lease{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ParseLeases(f)
}

// ParseLeases reads dnsmasq's lease file. IPv4 lines are
// "expiry mac ip hostname clientid"; IPv6 lines put the client's IAID
// where the MAC would be and its DUID where the client id would be.
func ParseLeases(r interface{ Read([]byte) (int, error) }) ([]Lease, error) {
	out := []Lease{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || strings.HasPrefix(fields[0], "duid") {
			continue
		}
		epoch, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		ip, err := netip.ParseAddr(fields[2])
		if err != nil {
			continue
		}
		l := Lease{IP: fields[2], Family: 4}
		if ip.Is6() && !ip.Is4In6() {
			l.Family = 6
		} else {
			l.MAC = fields[1]
		}
		if epoch != 0 {
			l.Expires = time.Unix(epoch, 0).UTC()
		}
		if len(fields) > 3 && fields[3] != "*" {
			l.Hostname = fields[3]
		}
		if len(fields) > 4 && fields[4] != "*" {
			l.ClientID = fields[4]
		}
		out = append(out, l)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		a, aerr := netip.ParseAddr(out[i].IP)
		b, berr := netip.ParseAddr(out[j].IP)
		if aerr != nil || berr != nil {
			return out[i].IP < out[j].IP
		}
		return a.Less(b)
	})
	return out, nil
}
