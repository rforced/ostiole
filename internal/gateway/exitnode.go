package gateway

import (
	"context"

	"ostiole/internal/tailscale"
)

// ExitNodes resolves a Tailscale exit node's tailnet IPv4 address and keeps
// tailscaled pointed at the configured exit node.
type ExitNodes interface {
	// Resolve returns the tailnet IPv4 address of the peer matching node,
	// whether tailscaled is Running, and whether node is currently selected
	// as the active exit node.
	Resolve(ctx context.Context, node string) (ipv4 string, running, active bool)
	// Set points tailscaled at node with LAN access allowed.
	Set(ctx context.Context, node string) error
}

type tailscaleExitNodes struct {
	client *tailscale.Client
}

// NewTailscaleExitNodes returns an ExitNodes implementation backed by c.
func NewTailscaleExitNodes(c *tailscale.Client) ExitNodes {
	if c == nil {
		c = tailscale.New()
	}
	return tailscaleExitNodes{client: c}
}

func (t tailscaleExitNodes) Resolve(ctx context.Context, node string) (string, bool, bool) {
	st, err := t.client.Status(ctx)
	if err != nil || st.BackendState != tailscale.StateRunning {
		return "", false, false
	}
	ipv4, _ := st.ExitNodeIPv4(node)
	return ipv4, true, st.MatchesExitNode(node)
}

func (t tailscaleExitNodes) Set(ctx context.Context, node string) error {
	return t.client.Set(ctx, []string{"--exit-node=" + node, "--exit-node-allow-lan-access=true"})
}
