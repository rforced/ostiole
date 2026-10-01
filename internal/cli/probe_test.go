package cli

import "testing"

// The probe after an update reaches the daemon where its unit says it
// listens, a service's name or a number.
func TestProbeURL(t *testing.T) {
	t.Parallel()
	for listen, want := range map[string]string{
		":9443":         "https://127.0.0.1:9443/api/v1/health",
		"10.0.0.1:8443": "https://127.0.0.1:8443/api/v1/health",
		":https":        "https://127.0.0.1:443/api/v1/health",
		"":              "https://127.0.0.1:9443/api/v1/health",
		":nonsense":     "https://127.0.0.1:9443/api/v1/health",
	} {
		if got := probeURL(listen, true); got != want {
			t.Errorf("probeURL(%q) = %q, want %q", listen, got, want)
		}
	}
}
