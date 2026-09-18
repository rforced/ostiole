package services

import (
	"context"
	"strings"

	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
)

// Bundle applies several service backends as one, so the engine keeps a
// single services hook and one snapshot to revert to. File names are
// prefixed with the backend's name ("dnsmasq/ostiole.conf"), and the
// backends are applied in order: the resolver comes up before the
// forwarder that points at it.
type Bundle struct {
	backends []network.Backend
}

var _ network.Backend = (*Bundle)(nil)

// NewBundle returns the production services: the dialled sessions first,
// because an interface has to exist before anything serves on it, then
// the resolver, then the blocklist, then the forwarder that reads the
// blocklist and points at the resolver. Tailscale follows it, because the
// forward for tailnet names has to be in place first. The mapping service
// comes last, because it restarts into a table that has just been rebuilt.
func NewBundle(blocklists *dnsblock.Cache, configDir string) *Bundle {
	return &Bundle{backends: []network.Backend{
		NewPPPoE(), NewUnbound(), NewDNSBlock(blocklists), New(),
		NewTailscale(configDir), NewUPnP(),
	}}
}

// Preflighter is a backend that can refuse a plan before anything has been
// applied. It is the same shape as the engine's, declared here so the
// services do not have to import it.
type Preflighter interface {
	Preflight(ctx context.Context, files network.Files) error
}

// Preflight implements the engine's Preflighter, asking every backend that
// has an opinion.
func (b *Bundle) Preflight(ctx context.Context, files network.Files) error {
	for _, back := range b.backends {
		p, ok := back.(Preflighter)
		if !ok {
			continue
		}
		if err := p.Preflight(ctx, split(files, back.Name())); err != nil {
			return err
		}
	}
	return nil
}

// NewBundleOf composes the given backends, in apply order.
func NewBundleOf(backends ...network.Backend) *Bundle {
	return &Bundle{backends: backends}
}

// Name implements network.Backend.
func (b *Bundle) Name() string {
	names := make([]string, 0, len(b.backends))
	for _, back := range b.backends {
		names = append(names, back.Name())
	}
	return strings.Join(names, "+")
}

// Render implements network.Backend.
func (b *Bundle) Render(cfg *model.Config) (network.Files, error) {
	out := network.Files{}
	for _, back := range b.backends {
		files, err := back.Render(cfg)
		if err != nil {
			return nil, err
		}
		merge(out, back.Name(), files)
	}
	return out, nil
}

// Snapshot implements network.Backend.
func (b *Bundle) Snapshot() (network.Files, error) {
	out := network.Files{}
	for _, back := range b.backends {
		files, err := back.Snapshot()
		if err != nil {
			return nil, err
		}
		merge(out, back.Name(), files)
	}
	return out, nil
}

// Apply implements network.Backend, handing each backend its own files.
func (b *Bundle) Apply(ctx context.Context, files network.Files) error {
	for _, back := range b.backends {
		if err := back.Apply(ctx, split(files, back.Name())); err != nil {
			return err
		}
	}
	return nil
}

func merge(dst network.Files, prefix string, src network.Files) {
	for name, content := range src {
		dst[prefix+"/"+name] = content
	}
}

func split(files network.Files, prefix string) network.Files {
	out := network.Files{}
	for name, content := range files {
		if after, ok := strings.CutPrefix(name, prefix+"/"); ok {
			out[after] = content
		}
	}
	return out
}
