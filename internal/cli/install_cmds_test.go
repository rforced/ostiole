package cli

import (
	"testing"

	"github.com/rforced/ostiole/internal/journald"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/timezone"
)

// `ostiole repair` runs the install again, so anything the install takes
// from a default rather than from the configuration is a setting the
// operator chose and the router quietly puts back.
func TestInstallTakesSettingsFromTheConfiguration(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{}
	cfg.System.JournalMaxUseGB = 25
	cfg.System.Timezone = "Europe/London"

	if got := journalCeiling(cfg); got != 25 {
		t.Errorf("journal ceiling = %d, want the configured 25", got)
	}
	if got := installZone("", cfg); got != "Europe/London" {
		t.Errorf("zone = %q, want the configured one", got)
	}

	// A router with no configuration yet gets the defaults.
	if got := journalCeiling(nil); got != journald.DefaultMaxUseGB {
		t.Errorf("journal ceiling = %d, want the default", got)
	}
	if got := installZone("", nil); got != timezone.Default {
		t.Errorf("zone = %q, want %q", got, timezone.Default)
	}

	// A configuration that says nothing about them still gets defaults.
	empty := &model.Config{}
	if got := journalCeiling(empty); got != journald.DefaultMaxUseGB {
		t.Errorf("journal ceiling = %d, want the default", got)
	}

	// The flag wins over both, and "-" still means "leave the clock".
	if got := installZone("Asia/Tokyo", cfg); got != "Asia/Tokyo" {
		t.Errorf("zone = %q, want the flag", got)
	}
	if got := installZone("-", cfg); got != "-" {
		t.Errorf("zone = %q, want the clock left alone", got)
	}
}
