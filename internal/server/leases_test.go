package server

import (
	"slices"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/diag"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/services"
)

func leaseConfig() *model.Config {
	iface := func(name, addr string) model.Interface {
		return model.Interface{
			Name: name, Zone: "lan", Enabled: true,
			IPv4: model.IPv4{Mode: model.AddrStatic, Address: addr},
		}
	}
	cfg := &model.Config{}
	cfg.Interfaces = []model.Interface{
		iface("lan0", "10.0.0.1/24"), iface("guest0", "10.1.0.1/24"), iface("iot0", "10.2.0.1/24"),
	}
	cfg.Services.DHCP = model.DHCPService{
		Enabled: true,
		Servers: []model.DHCPServer{
			{Interface: "lan0", Enabled: true, RangeStart: "10.0.0.100", RangeEnd: "10.0.0.200"},
			{Interface: "guest0", Enabled: true, RangeStart: "10.1.0.100", RangeEnd: "10.1.0.200", LeaseTime: "1h"},
			{Interface: "iot0", Enabled: true, RangeStart: "10.2.0.100", RangeEnd: "10.2.0.200", LeaseTime: "infinite"},
		},
		V6: []model.DHCPv6Server{{Interface: "lan0", Enabled: true, Mode: model.RAManaged, LeaseTime: "12h"}},
		StaticLeases: []model.StaticLease{
			{MAC: "AA:BB:CC:00:00:01", IP: "10.0.0.20", Description: "Printer"},
			{MAC: "aa:bb:cc:00:00:02", IP: "10.0.0.99", Description: "Laptop"},
		},
	}
	return cfg
}

func TestLeaseRows(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC)
	file := []services.Lease{
		{IP: "10.0.0.20", MAC: "aa:bb:cc:00:00:01", Family: 4, Expires: now.Add(23 * time.Hour)},
		{IP: "10.0.0.21", MAC: "aa:bb:cc:00:00:03", Family: 4, Expires: now.Add(12 * time.Hour)},
		{IP: "10.0.0.150", MAC: "aa:bb:cc:00:00:02", Family: 4, Expires: now.Add(20 * time.Hour)},
		{IP: "10.1.0.5", MAC: "aa:bb:cc:00:00:05", Family: 4, Expires: now.Add(30 * time.Minute)},
		{IP: "10.1.0.9", MAC: "aa:bb:cc:00:00:09", Family: 4, Expires: now.Add(5 * time.Hour)},
		{IP: "10.2.0.7", MAC: "aa:bb:cc:00:00:07", Family: 4},
		{IP: "192.168.77.5", MAC: "aa:bb:cc:00:00:08", Family: 4, Expires: now.Add(time.Hour)},
		{IP: "2001:db8:1::20", ClientID: "00:01:00:01", Family: 6, Expires: now.Add(6 * time.Hour)},
	}
	links := []network.Link{
		{Name: "lan0", Addresses: []string{"10.0.0.1/24", "2001:db8:1::1/64", "fe80::1/64"}},
		{Name: "guest0", Addresses: []string{"10.1.0.1/24", "2001:db8:2::1/64"}},
	}
	neighbours := []diag.Neighbour{
		{Address: "10.0.0.20", MAC: "aa:bb:cc:00:00:01", Interface: "lan0", Seen: now.Add(-2 * time.Minute)},
		// The address answered, but at somebody else's MAC.
		{Address: "10.0.0.21", MAC: "aa:bb:cc:00:00:ff", Interface: "lan0", Seen: now},
		{Address: "10.1.0.5", MAC: "aa:bb:cc:00:00:05", Interface: "guest0", Seen: now.Add(-3 * time.Hour)},
		{Address: "2001:db8:1::20", MAC: "aa:bb:cc:00:00:06", Interface: "lan0", Seen: now.Add(-time.Minute)},
	}
	rows := map[string]lease{}
	for _, r := range leaseRows(leaseConfig(), file, links, neighbours, now) {
		rows[r.IP] = r
	}

	printer := rows["10.0.0.20"]
	if printer.Interface != "lan0" || !printer.Static || printer.Description != "Printer" {
		t.Errorf("printer = %+v", printer)
	}
	// The pool names no lease time, so it hands out the default day.
	if !printer.Renewed.Equal(now.Add(-time.Hour)) {
		t.Errorf("printer renewed %v, want an hour ago", printer.Renewed)
	}
	if !printer.Online || !printer.Seen.Equal(now.Add(-2*time.Minute)) {
		t.Errorf("printer online %v, seen %v", printer.Online, printer.Seen)
	}
	if r := rows["10.0.0.21"]; r.Online || !r.Seen.IsZero() {
		t.Errorf("another MAC's answer counted: %+v", r)
	}
	// Pinned somewhere else, so not static here, but it is the laptop.
	if r := rows["10.0.0.150"]; r.Static || r.Description != "Laptop" {
		t.Errorf("laptop = %+v", r)
	}
	guest := rows["10.1.0.5"]
	if guest.Interface != "guest0" || !guest.Renewed.Equal(now.Add(-30*time.Minute)) {
		t.Errorf("guest = %+v, want renewed half an hour into its hour", guest)
	}
	if guest.Online || !guest.Seen.Equal(now.Add(-3*time.Hour)) {
		t.Errorf("guest online %v, seen %v, want offline and seen three hours ago", guest.Online, guest.Seen)
	}
	// Five hours left in a one-hour pool: the pool was cut since.
	if r := rows["10.1.0.9"]; !r.Renewed.IsZero() {
		t.Errorf("renewed in the future: %v", r.Renewed)
	}
	if r := rows["10.2.0.7"]; r.Interface != "iot0" || !r.Renewed.IsZero() || !r.Expires.IsZero() {
		t.Errorf("infinite lease = %+v", r)
	}
	if r := rows["192.168.77.5"]; r.Interface != "" || !r.Renewed.IsZero() {
		t.Errorf("lease in no pool = %+v", r)
	}
	// An IPv6 lease has no MAC: the prefix on the link names the
	// interface, and the address alone finds the neighbour.
	v6 := rows["2001:db8:1::20"]
	if v6.Interface != "lan0" || !v6.Renewed.Equal(now.Add(-6*time.Hour)) || !v6.Online || v6.Static {
		t.Errorf("v6 = %+v", v6)
	}
}

// With no configuration there is nothing to name an interface or a pool
// by, but the neighbour table still says who answered.
func TestLeaseRowsWithoutAConfig(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC)
	rows := leaseRows(nil,
		[]services.Lease{{IP: "10.0.0.20", MAC: "aa:bb:cc:00:00:01", Family: 4, Expires: now.Add(time.Hour)}},
		nil,
		[]diag.Neighbour{{Address: "10.0.0.20", MAC: "AA:BB:CC:00:00:01", Interface: "lan0", Seen: now.Add(-10 * time.Minute)}},
		now)
	if len(rows) != 1 || rows[0].Interface != "" || !rows[0].Renewed.IsZero() || !rows[0].Online {
		t.Errorf("rows = %+v", rows)
	}
	if rows := leaseRows(nil, nil, nil, nil, now); rows == nil || len(rows) != 0 {
		t.Errorf("no leases = %#v, want an empty list", rows)
	}
}

func TestRecentLeasesNewestFirst(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	row := func(ip string, renewed, expires time.Time) lease {
		return lease{Lease: services.Lease{IP: ip, Expires: expires}, Renewed: renewed}
	}
	rows := []lease{
		// A day's lease renewed an hour ago still expires last.
		row("10.0.0.2", base.Add(-time.Hour), base.Add(23*time.Hour)),
		row("10.0.0.3", base.Add(-time.Minute), base.Add(59*time.Minute)),
		row("10.0.0.4", time.Time{}, base.Add(2*time.Hour)),
		row("10.0.0.5", base.Add(-2*time.Hour), base.Add(22*time.Hour)),
		row("10.0.0.6", time.Time{}, time.Time{}),
	}
	var got []string
	for _, r := range recentLeases(rows, 4) {
		got = append(got, r.IP)
	}
	if want := []string{"10.0.0.3", "10.0.0.2", "10.0.0.5", "10.0.0.4"}; !slices.Equal(got, want) {
		t.Errorf("recentLeases = %v, want %v", got, want)
	}
	if rows[0].IP != "10.0.0.2" {
		t.Error("recentLeases reordered the caller's slice")
	}
}
