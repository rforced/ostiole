package server

import (
	"context"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"ostiole/internal/install"
	"ostiole/internal/model"
	"ostiole/internal/openvpn"
	"ostiole/internal/services"
)

// openvpnStatus is what the page shows. A router without the unit, or a
// tunnel that is stopped, answers with the flags false and the rest
// empty: "not set up" is a state of the page, not an error.
type openvpnStatus struct {
	SetUp bool `json:"setUp"`
	// SecureBootSkipped is the install leaving the kernel's data channel
	// out because Secure Boot would refuse it.
	SecureBootSkipped bool            `json:"secureBootSkipped"`
	Tunnels           []openvpnTunnel `json:"tunnels"`
}

// openvpnTunnel is one configured tunnel, in name order.
type openvpnTunnel struct {
	Name          string     `json:"name"`
	SetUp         bool       `json:"setUp"`
	Running       bool       `json:"running"`
	State         string     `json:"state"`
	Connected     bool       `json:"connected"`
	Since         *time.Time `json:"since,omitempty"`
	TunnelAddress string     `json:"tunnelAddress"`
	Remote        string     `json:"remote"`
	// DataChannel is "kernel", "userspace", or empty with no device.
	DataChannel string `json:"dataChannel"`
	BytesIn     uint64 `json:"bytesIn"`
	BytesOut    uint64 `json:"bytesOut"`
	// Note is the daemon's last word on why the data channel stayed in
	// userspace.
	Note string `json:"note"`
}

// openvpnReader reads the tunnels; *openvpn.Client is one.
type openvpnReader interface {
	Status(ctx context.Context, name string) (openvpn.State, error)
	DataChannel(name string) string
	Note(ctx context.Context, name string) string
}

func (a *api) registerOpenVPN(mux *router) {
	mux.HandleFunc("GET /api/v1/openvpn/status", a.readNoEngine(a.openvpnStatus))
}

// openvpnStatus asks every configured tunnel what it is doing. Every way
// of having nothing to say is a tunnel with the flags false rather than
// an error, because the page's own text covers each of them.
func (a *api) openvpnStatus(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, a.readOpenVPN(r.Context()))
	return nil
}

func (a *api) readOpenVPN(ctx context.Context) openvpnStatus {
	out := openvpnStatus{Tunnels: []openvpnTunnel{}}
	var names []string
	if a.engine != nil {
		if cfg := a.engine.Effective(); cfg != nil {
			names = openvpnNames(cfg)
		}
	}
	units := []string{services.OpenVPNUnit}
	for _, n := range names {
		units = append(units, services.OpenVPNUnitFor(n))
	}
	var states install.UnitStates
	if a.units != nil {
		states = install.ReadUnits(ctx, a.units, units...)
	}
	_, out.SetUp = states.File(services.OpenVPNUnit)
	if out.SetUp {
		skipped := a.secureBootSkipped
		if skipped == nil {
			skipped = openvpn.SecureBootSkipped
		}
		out.SecureBootSkipped = skipped()
	}
	reader := a.ovpn
	if reader == nil {
		reader = openvpn.Default
	}
	for _, n := range names {
		t := openvpnTunnel{Name: n, SetUp: out.SetUp}
		t.Running = out.SetUp && states.Running(services.OpenVPNUnitFor(n))
		if t.Running {
			if st, err := reader.Status(ctx, n); err == nil {
				t.State, t.Connected = st.State, st.Connected
				t.Since = whenSet(&st.Since)
				t.TunnelAddress = st.TunnelAddress
				if st.RemoteAddress != "" {
					t.Remote = st.RemoteAddress
					if st.RemotePort != 0 {
						t.Remote = net.JoinHostPort(st.RemoteAddress, strconv.Itoa(st.RemotePort))
					}
				}
				t.BytesIn, t.BytesOut = st.BytesIn, st.BytesOut
			}
			t.DataChannel = reader.DataChannel(n)
			// An earlier start's complaint is history once the kernel
			// carries the tunnel.
			if t.DataChannel != "kernel" {
				t.Note = reader.Note(ctx, n)
			}
		}
		out.Tunnels = append(out.Tunnels, t)
	}
	return out
}

// openvpnNames lists the configured OpenVPN interfaces, switched off or
// not, in name order.
func openvpnNames(cfg *model.Config) []string {
	var out []string
	for _, in := range cfg.Interfaces {
		if in.OpenVPN != nil {
			out = append(out, in.Name)
		}
	}
	slices.SortFunc(out, strings.Compare)
	return out
}
