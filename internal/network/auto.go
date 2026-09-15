package network

import (
	"context"
	"log/slog"
	"strings"

	"github.com/rforced/ostiole/internal/model"
)

// Auto uses systemd-networkd when it is running and does nothing
// otherwise. It lets one daemon unit serve both a freshly installed box
// (still on NetworkManager) and one that has completed the network
// takeover, without editing the unit in between.
type Auto struct {
	Networkd *Networkd
	Log      *slog.Logger
}

// NewAuto returns an Auto backend with production defaults.
func NewAuto() *Auto {
	return &Auto{Networkd: NewNetworkd(), Log: slog.Default()}
}

// Active reports whether systemd-networkd is running.
func (a *Auto) Active(ctx context.Context) bool {
	out, err := a.Networkd.cmd().Run(ctx, "systemctl", "is-active", "systemd-networkd.service")
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

// Name implements Backend.
func (a *Auto) Name() string {
	if a.Active(context.Background()) {
		return a.Networkd.Name()
	}
	return "none"
}

// Render implements Backend; rendering never needs networkd.
func (a *Auto) Render(cfg *model.Config) (Files, error) {
	return a.Networkd.Render(cfg)
}

// Snapshot implements Backend.
func (a *Auto) Snapshot() (Files, error) {
	if !a.Active(context.Background()) {
		return Files{}, nil
	}
	return a.Networkd.Snapshot()
}

// Apply implements Backend. Without networkd the units are not written,
// so a later takeover starts from the configuration, not stale files.
func (a *Auto) Apply(ctx context.Context, files Files) error {
	if !a.Active(ctx) {
		a.Log.Warn("network configuration not applied: systemd-networkd is not running (run `ostiole takeover --network`)",
			"units", len(files))
		return nil
	}
	return a.Networkd.Apply(ctx, files)
}
