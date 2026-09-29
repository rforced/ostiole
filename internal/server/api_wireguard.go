package server

import (
	"net/http"

	"github.com/rforced/ostiole/internal/wg"
)

// wireguardStatus reads each enabled tunnel of the running configuration
// from the kernel: whether its device is up, and each peer's endpoint,
// last handshake and traffic. A router that may not read the devices
// answers 503, which the page says as such.
func (a *api) wireguardStatus(w http.ResponseWriter, _ *http.Request) error {
	var tunnels []wg.Configured
	if cfg := a.engine.Effective(); cfg != nil {
		for _, in := range cfg.Interfaces {
			if !in.Enabled || in.WireGuard == nil {
				continue
			}
			c := wg.Configured{Name: in.Name, Peers: make(map[string]string, len(in.WireGuard.Peers))}
			for _, p := range in.WireGuard.Peers {
				c.Peers[p.PublicKey] = p.Name
			}
			tunnels = append(tunnels, c)
		}
	}
	out, err := wg.Status(tunnels, a.wgDevice)
	if err != nil {
		return &unavailable{err}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
