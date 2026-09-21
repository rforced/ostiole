package smart

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
)

// The hourly poll keeps one verdict per drive: the one that failed is a
// warning, the one that is asleep keeps what it last said, and a drive
// that is no longer there is forgotten.
func TestMonitorKeepsTheLastVerdict(t *testing.T) {
	t.Parallel()
	answers := map[string]Health{}
	devices := []Device{
		{Name: "sda", Type: "sat", Protocol: "ATA", path: "/dev/sda"},
		{Name: "sdb", Type: "sat", Protocol: "ATA", path: "/dev/sdb"},
	}
	m := &Monitor{Log: slog.New(slog.DiscardHandler)}
	m.Client = &Client{Bin: "smartctl", Run: func(_ context.Context, _ string, args ...string) ([]byte, int, error) {
		if slices.Contains(args, "--scan-open") {
			return scanDoc(devices), 0, nil
		}
		name := args[len(args)-1]
		switch h := answers[name]; {
		case h.Skipped:
			return []byte(`{"smartctl":{"version":[7,5],"exit_status":2,"messages":[` +
				`{"string":"Device is in STANDBY mode, exit(2)","severity":"error"}]}}`), 2, nil
		case h.Model == "":
			return nil, 0, errors.New("no answer")
		default:
			return healthDoc(h), 0, nil
		}
	}}

	answers["/dev/sda"] = Health{Model: "GOOD", Passed: true}
	answers["/dev/sdb"] = Health{Model: "DYING", Passed: false}
	m.Check(t.Context())
	if got := names(m.Failing()); !slices.Equal(got, []string{"sdb"}) {
		t.Errorf("failing = %v, want the one that said so", got)
	}
	if got := len(m.Latest()); got != 2 {
		t.Errorf("latest = %d drives, want 2", got)
	}

	// Asleep at the top of the hour: the verdict stands, and the drive is
	// not spun up to repeat it.
	answers["/dev/sdb"] = Health{Skipped: true}
	m.Check(t.Context())
	if got := names(m.Failing()); !slices.Equal(got, []string{"sdb"}) {
		t.Errorf("failing after a skip = %v, want the kept verdict", got)
	}

	// A drive that will not answer at all has not said it is failing.
	answers["/dev/sda"] = Health{}
	m.Check(t.Context())
	if got := names(m.Failing()); !slices.Equal(got, []string{"sdb"}) {
		t.Errorf("failing = %v, want sdb only", got)
	}

	// Pulled out: nothing left to warn about.
	devices = devices[:1]
	answers["/dev/sda"] = Health{Model: "GOOD", Passed: true}
	m.Check(t.Context())
	if got := m.Failing(); len(got) != 0 {
		t.Errorf("failing = %+v, want none once the drive is gone", got)
	}
	if got := names(m.Latest()); !slices.Equal(got, []string{"sda"}) {
		t.Errorf("latest = %v", got)
	}
}

// The crons page says when the drives were last asked, which only works
// if every pass reports itself, including one that found nothing.
func TestMonitorNotesEveryPass(t *testing.T) {
	t.Parallel()
	ticks := 0
	m := &Monitor{
		Client: &Client{},
		Log:    slog.New(slog.DiscardHandler),
		OnTick: func() { ticks++ },
	}
	m.Check(t.Context())
	m.Check(t.Context())
	if ticks != 2 {
		t.Errorf("ticks = %d, want 2", ticks)
	}
	if got := m.Latest(); len(got) != 0 {
		t.Errorf("latest = %+v, want none without a tool", got)
	}
}

func names(hs []Health) []string {
	out := make([]string, 0, len(hs))
	for _, h := range hs {
		out = append(out, h.Name)
	}
	return out
}

func scanDoc(devs []Device) []byte {
	out := `{"smartctl":{"version":[7,5],"exit_status":0},"devices":[`
	for i, d := range devs {
		if i > 0 {
			out += ","
		}
		out += `{"name":"` + d.path + `","type":"` + d.Type + `","protocol":"` + d.Protocol + `"}`
	}
	return []byte(out + `]}`)
}

func healthDoc(h Health) []byte {
	passed := "false"
	if h.Passed {
		passed = "true"
	}
	return []byte(`{"smartctl":{"version":[7,5],"exit_status":0},"model_name":"` + h.Model +
		`","smart_status":{"passed":` + passed + `}}`)
}
