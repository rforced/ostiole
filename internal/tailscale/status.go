package tailscale

import (
	"encoding/json"
	"fmt"
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
	Version string          `json:"Version"`
	Health  []string        `json:"Health"`
	Peer    map[string]Peer `json:"Peer"`
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
