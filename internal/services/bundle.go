package services

import (
	"context"
	"strings"

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
// the resolver, then the forwarder that points at it.
func NewBundle() *Bundle {
	return &Bundle{backends: []network.Backend{NewPPPoE(), NewUnbound(), New()}}
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
