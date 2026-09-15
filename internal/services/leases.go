package services

import (
	"bufio"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Lease is one entry of the dnsmasq lease file.
type Lease struct {
	Expires  time.Time `json:"expires"`
	MAC      string    `json:"mac"`
	IP       string    `json:"ip"`
	Hostname string    `json:"hostname,omitempty"`
	ClientID string    `json:"clientId,omitempty"`
	Static   bool      `json:"static"`
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

// ParseLeases reads dnsmasq's "expiry mac ip hostname clientid" lines.
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
		l := Lease{MAC: fields[1], IP: fields[2], Static: epoch == 0}
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
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out, nil
}
