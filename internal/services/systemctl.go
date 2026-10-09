package services

import (
	"context"
	"fmt"
	"strings"

	"ostiole/internal/network"
)

// systemctl runs one systemctl verb on a unit and names the step in the
// error, followed by what systemctl said.
func systemctl(ctx context.Context, c network.Commander, what string, args ...string) error {
	out, err := c.Run(ctx, "systemctl", args...)
	if err == nil {
		return nil
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return fmt.Errorf("%s: %w: %s", what, err, msg)
	}
	return fmt.Errorf("%s: %w", what, err)
}
