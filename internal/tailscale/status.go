package tailscale

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// Backend states, as tailscaled reports them.
const (
	StateNoState          = "NoState"
	StateNeedsLogin       = "NeedsLogin"
	StateNeedsMachineAuth = "NeedsMachineAuth"
	StateStopped          = "Stopped"
	StateStarting         = "Starting"
	StateRunning          = "Running"
)

// Status is the part of `tailscale status --json` the UI shows. The rest
// of that document is large and moves between releases; everything here
// has been stable since 1.x.
type Status struct {
	BackendState   string   `json:"BackendState"`
	AuthURL        string   `json:"AuthURL"`
	TailscaleIPs   []string `json:"TailscaleIPs"`
	Self           *Peer    `json:"Self"`
	CurrentTailnet *struct {
		Name           string `json:"Name"`
		MagicDNSSuffix string `json:"MagicDNSSuffix"`
	} `json:"CurrentTailnet"`
	Version        string          `json:"Version"`
	Health         []string        `json:"Health"`
	ExitNodeStatus *ExitNodeStatus `json:"ExitNodeStatus"`
	Peer           map[string]Peer `json:"Peer"`
}

// ExitNodeStatus is the exit node tailscaled is currently using, if any.
type ExitNodeStatus struct {
	ID           string   `json:"ID"`
	Online       bool     `json:"Online"`
	TailscaleIPs []string `json:"TailscaleIPs"`
}

// Peer is one node on the tailnet, this router included: Self has the same
// shape.
type Peer struct {
	HostName       string     `json:"HostName"`
	DNSName        string     `json:"DNSName"`
	OS             string     `json:"OS"`
	TailscaleIPs   []string   `json:"TailscaleIPs"`
	PrimaryRoutes  []string   `json:"PrimaryRoutes"`
	Online         bool       `json:"Online"`
	LastSeen       *time.Time `json:"LastSeen"`
	KeyExpiry      *time.Time `json:"KeyExpiry"`
	Expired        bool       `json:"Expired"`
	ExitNodeOption bool       `json:"ExitNodeOption"`
	// ExitNode is true on the peer currently selected as this router's exit node.
	ExitNode bool `json:"ExitNode"`
	// Active is whether traffic is flowing right now. Only then do Relay
	// and CurAddr say anything about the path.
	Active bool `json:"Active"`
	// Relay is the peer's DERP home region. It is set whether or not
	// anything is relayed, so it means "reached through this region" only
	// when the connection is Active and CurAddr is empty.
	Relay string `json:"Relay"`
	// CurAddr is the direct endpoint, set while a direct path is carrying
	// an active connection and cleared when it goes idle.
	CurAddr string `json:"CurAddr"`
	RxBytes int64  `json:"RxBytes"`
	TxBytes int64  `json:"TxBytes"`
}

// ParseStatus reads what `tailscale status --json` printed.
func ParseStatus(raw []byte) (*Status, error) {
	var s Status
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse tailscale status: %w", err)
	}
	return &s, nil
}

// ActiveExitNode returns the display name of the exit node tailscaled is
// currently using, or ("", false) when none is active.
func (s *Status) ActiveExitNode() (string, bool) {
	if s == nil {
		return "", false
	}
	for _, p := range s.Peer {
		if p.ExitNode {
			return peerDisplayName(p), true
		}
	}
	if s.ExitNodeStatus == nil || (s.ExitNodeStatus.ID == "" && len(s.ExitNodeStatus.TailscaleIPs) == 0) {
		return "", false
	}
	for _, raw := range s.ExitNodeStatus.TailscaleIPs {
		ip := stripCIDR(raw)
		for _, p := range s.Peer {
			if peerMatches(p, ip) {
				return peerDisplayName(p), true
			}
		}
	}
	if len(s.ExitNodeStatus.TailscaleIPs) > 0 {
		return stripCIDR(s.ExitNodeStatus.TailscaleIPs[0]), true
	}
	return s.ExitNodeStatus.ID, true
}

// MatchesExitNode reports whether tailscaled is currently using the exit
// node named by node (a hostname, MagicDNS name or label, or tailnet IP).
func (s *Status) MatchesExitNode(node string) bool {
	node = strings.TrimSpace(node)
	if s == nil || node == "" {
		return false
	}
	for _, p := range s.Peer {
		if p.ExitNode && peerMatches(p, node) {
			return true
		}
	}
	if s.ExitNodeStatus != nil {
		for _, raw := range s.ExitNodeStatus.TailscaleIPs {
			if strings.EqualFold(stripCIDR(raw), node) {
				return true
			}
		}
	}
	return false
}

// ExitNodeIPv4 resolves the exit node named by node to its tailnet IPv4
// address. It returns ("", false) unless tailscaled is Running and knows a
// peer offering an exit node (or currently active as one) that matches node.
func (s *Status) ExitNodeIPv4(node string) (string, bool) {
	node = strings.TrimSpace(node)
	if s == nil || s.BackendState != StateRunning || node == "" {
		return "", false
	}
	for _, p := range s.Peer {
		if (!p.ExitNodeOption && !p.ExitNode) || !peerMatches(p, node) {
			continue
		}
		for _, raw := range p.TailscaleIPs {
			if addr, err := netip.ParseAddr(stripCIDR(raw)); err == nil && addr.Is4() {
				return addr.String(), true
			}
		}
	}
	return "", false
}

func stripCIDR(s string) string {
	ip, _, _ := strings.Cut(s, "/")
	return ip
}

func peerDisplayName(p Peer) string {
	if p.DNSName != "" {
		label, _, _ := strings.Cut(strings.TrimSuffix(p.DNSName, "."), ".")
		if label != "" {
			return label
		}
	}
	if p.HostName != "" {
		return p.HostName
	}
	if len(p.TailscaleIPs) > 0 {
		return stripCIDR(p.TailscaleIPs[0])
	}
	return ""
}

func peerMatches(p Peer, node string) bool {
	want := strings.TrimSuffix(strings.TrimSpace(node), ".")
	if want == "" {
		return false
	}
	if p.HostName != "" && strings.EqualFold(p.HostName, want) {
		return true
	}
	if p.DNSName != "" {
		fqdn := strings.TrimSuffix(p.DNSName, ".")
		if strings.EqualFold(fqdn, want) {
			return true
		}
		label, _, _ := strings.Cut(fqdn, ".")
		if label != "" && strings.EqualFold(label, want) {
			return true
		}
	}
	for _, raw := range p.TailscaleIPs {
		if strings.EqualFold(stripCIDR(raw), want) {
			return true
		}
	}
	return false
}
