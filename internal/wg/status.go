package wg

import (
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"ostiole/internal/netlink"
)

// Tunnel is what the kernel says of one configured tunnel.
type Tunnel struct {
	Name string `json:"name"`
	// Up is whether the device exists: a tunnel not yet applied, or whose
	// device networkd has not made, has none.
	Up         bool   `json:"up"`
	ListenPort int    `json:"listenPort,omitempty"`
	Peers      []Peer `json:"peers"`
}

// Peer is one peer as its tunnel's device sees it.
type Peer struct {
	// Name is the configured peer with this key. It is empty for a key
	// the configuration does not hold, which something other than Ostiole
	// set.
	Name      string `json:"name,omitempty"`
	PublicKey string `json:"publicKey"`
	// Endpoint is where the peer was last heard from, or where it is
	// dialled.
	Endpoint      string    `json:"endpoint,omitempty"`
	LastHandshake time.Time `json:"lastHandshake,omitzero"`
	RxBytes       uint64    `json:"rxBytes"`
	TxBytes       uint64    `json:"txBytes"`
}

// Configured is a tunnel as the configuration has it: its name, and the
// names of its peers by their public keys.
type Configured struct {
	Name  string
	Peers map[string]string
}

// Device reads a WireGuard device by name; found is false when there is
// none.
type Device func(name string) (dev netlink.WireGuardDevice, found bool, err error)

// Status reads each tunnel through read. A nil read asks the kernel.
func Status(tunnels []Configured, read Device) ([]Tunnel, error) {
	if read == nil {
		read = kernelDevice
	}
	out := make([]Tunnel, 0, len(tunnels))
	for _, c := range tunnels {
		t := Tunnel{Name: c.Name, Peers: []Peer{}}
		dev, found, err := read(c.Name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.Name, err)
		}
		if found {
			t.Up, t.ListenPort = true, dev.ListenPort
			for _, p := range dev.Peers {
				key := base64.StdEncoding.EncodeToString(p.PublicKey)
				peer := Peer{Name: c.Peers[key], PublicKey: key, LastHandshake: p.LastHandshake,
					RxBytes: p.RxBytes, TxBytes: p.TxBytes}
				if p.Endpoint.IsValid() {
					peer.Endpoint = p.Endpoint.String()
				}
				t.Peers = append(t.Peers, peer)
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// kernelDevice asks the kernel. Whether the device exists needs no
// privilege to learn, so only a device that does is dumped, which is what
// needs CAP_NET_ADMIN.
func kernelDevice(name string) (netlink.WireGuardDevice, bool, error) {
	if _, err := netlink.LinkByName(name); err != nil {
		if errors.Is(err, netlink.ErrLinkNotFound) {
			return netlink.WireGuardDevice{}, false, nil
		}
		return netlink.WireGuardDevice{}, false, err
	}
	dev, err := netlink.WireGuard(name)
	return dev, err == nil, err
}
