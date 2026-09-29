package modem

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestLiveModem reads a real modem when OSTIOLE_MODEM names its address.
// It is how a new firmware or vendor is checked from the router itself:
//
//	OSTIOLE_MODEM=192.168.100.1 go test ./internal/modem -run Live -v
func TestLiveModem(t *testing.T) {
	address := os.Getenv("OSTIOLE_MODEM")
	if address == "" {
		t.Skip("OSTIOLE_MODEM is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := Fetch(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.MarshalIndent(st, "", "  ")
	t.Logf("%s", raw)
	if st.Vendor == "" || len(st.Downstream) == 0 {
		t.Errorf("a modem with no vendor or no downstream channel: %+v", st)
	}
}
