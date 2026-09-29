package traffic

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/logfile"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/netlink"
)

// lines is what waits in a record ring, oldest first.
func lines[T any](r *records[T]) []T {
	buf := make([]record[T], recordsKept)
	n, _ := r.after(0, buf)
	out := make([]T, 0, n)
	for _, rec := range buf[:n] {
		out = append(out, rec.v)
	}
	return out
}

// A minute goes to the files once it is over, for every link and device
// that moved something in it, and not again; a minute of nothing writes
// nothing.
func TestAMinuteIsWrittenOnceItIsOver(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	r.now = time.Date(2026, 9, 27, 12, 0, 10, 0, time.UTC)
	r.c.follow(r.now)
	set := func(rx uint64) {
		r.mu.Lock()
		r.links[1].RXBytes = rx
		r.mu.Unlock()
	}
	set(1000)
	r.c.Tick(r.now)
	set(3000)
	r.now = r.now.Add(5 * time.Second)
	r.flows = nil
	r.c.Tick(r.now)
	r.c.dump(r.now)
	r.flows = []netlink.Flow{tcp(1, "10.0.0.5", "198.51.100.7", 443, 10, 20)}
	r.now = r.now.Add(5 * time.Second)
	r.c.Tick(r.now)
	if got := lines(&r.c.linkRecs); len(got) != 0 {
		t.Fatalf("written before the minute was over: %+v", got)
	}
	r.now = r.now.Add(time.Minute)
	r.c.Tick(r.now)
	r.c.Tick(r.now.Add(time.Second))
	links := lines(&r.c.linkRecs)
	if len(links) != 1 || links[0].ID != "eth0" || links[0].Down != 2000 ||
		!links[0].Time.Equal(time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("link minutes = %+v", links)
	}
	if devices := lines(&r.c.deviceRecs); len(devices) != 1 || devices[0].ID != "10.0.0.5" || devices[0].Up != 10 {
		t.Errorf("device minutes = %+v", devices)
	}
}

// Written through the writer and read back into a fresh counter, a link's
// and a device's day and month are what they were, the destinations too;
// the five minutes start empty.
func TestTrafficComesBackFromItsFiles(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	r.cfg.Traffic.Destinations = model.TrafficDestinations{Enabled: true}
	r.cfg.System.Logging.Files = model.LogFiles{Enabled: true}
	r.now = time.Date(2026, 9, 27, 11, 58, 0, 0, time.UTC)
	w := &logfile.Writer{Dir: t.TempDir(), Source: func() *model.Config { return r.cfg },
		Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return r.now },
		Statfs: func(string) (uint64, uint64, error) { return 50, 100, nil }}
	for _, l := range r.c.FileLogs() {
		w.Add(l, logfile.ReadStats{})
	}
	r.c.follow(r.now)
	r.step(0)
	r.c.Names().Answered(netip.MustParseAddr("10.0.0.5"), "example.com", []netip.Addr{netip.MustParseAddr("198.51.100.7")}, r.now)
	for i := range 5 {
		r.mu.Lock()
		r.links[1].RXBytes = uint64(i) * 60000
		r.mu.Unlock()
		r.step(time.Minute, tcp(1, "10.0.0.5", "198.51.100.7", 443, uint64(i+1)*100, uint64(i+1)*1000))
	}
	r.c.Tick(r.now.Add(time.Minute))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	w.Run(ctx)

	fresh := newRouter(t)
	fresh.cfg = r.cfg
	fresh.now = r.now.Add(10 * time.Minute)
	fresh.c.BeginRestore(fresh.now)
	for _, name := range []string{LinksFile, DevicesFile} {
		if _, err := logfile.ReadEach(w.Dir, name, FileVersion, fresh.now.Add(-31*24*time.Hour), ParseMinute,
			func(m MinuteLine) { fresh.c.RestoreMinute(name, m, fresh.now) }); err != nil {
			t.Fatal(err)
		}
	}
	hours, _, err := logfile.Read(w.Dir, DestinationsFile, FileVersion, 100, fresh.now.Add(-7*24*time.Hour), ParseHour)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hours {
		fresh.c.RestoreHour(h)
	}
	fresh.c.EndRestore(fresh.now)

	want := r.c.LinkReports(Window24h)
	got := fresh.c.LinkReports(Window24h)
	if len(got) == 0 || got[0].Totals != want[0].Totals || got[0].Totals.Down == 0 {
		t.Errorf("eth0 over a day = %+v, was %+v", got[0].Totals, want[0].Totals)
	}
	if pts, _ := fresh.c.LinkReports(Window5m)[0], 0; len(pts.Points) != 0 {
		t.Errorf("five minutes came back: %v", pts.Points)
	}
	d, ok := fresh.c.DeviceReport("10.0.0.5", Window31d)
	if !ok || d.Totals.Up != 500 || d.Totals.Down != 5000 || !slices.Equal(d.Addresses, []string{"10.0.0.5"}) {
		t.Errorf("device over a month = %+v, %v", d, ok)
	}
	// The hour of the destination was still open, so it is not in the
	// files yet; a closed one is.
	if len(hours) != 1 || hours[0].Destination != "example.com" || hours[0].Up != 100 {
		t.Errorf("hours = %+v", hours)
	}
	if rows, _, _ := fresh.c.Destinations(24*time.Hour, ""); len(rows) != 1 || rows[0].Port != 443 {
		t.Errorf("destinations = %+v", rows)
	}
}

// What the files hold older than a month is left, and a device that only
// the files know comes back as far as they can say.
func TestTheRestoreSkipsWhatIsTooOld(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	now := r.now
	r.c.BeginRestore(now)
	r.c.RestoreMinute(LinksFile, MinuteLine{Time: now.Add(-40 * 24 * time.Hour), ID: "eth0", Down: 1, Up: 1}, now)
	r.c.RestoreMinute(LinksFile, MinuteLine{Time: now.Add(-2 * time.Hour).Truncate(time.Minute), ID: "eth0", Down: 7, Up: 8}, now)
	r.c.RestoreMinute(DevicesFile, MinuteLine{Time: now.Add(-3 * 24 * time.Hour), ID: "a8:bb:cc:00:00:05", Down: 5, Up: 6}, now)
	r.c.EndRestore(now)
	l := r.c.LinkReports(Window31d)
	if len(l) != 1 || l[0].Totals != (Totals{Down: 7, Up: 8}) {
		t.Errorf("link = %+v", l)
	}
	if day := r.c.LinkReports(Window24h); day[0].Totals != (Totals{Down: 7, Up: 8}) {
		t.Errorf("a day = %+v", day[0].Totals)
	}
	d, ok := r.c.DeviceReport("a8:bb:cc:00:00:05", Window31d)
	if !ok || d.MAC != "a8:bb:cc:00:00:05" || d.Totals != (Totals{Down: 5, Up: 6}) {
		t.Errorf("device only the files knew = %+v, %v", d, ok)
	}
	if day, _ := r.c.DeviceReport("a8:bb:cc:00:00:05", Window24h); day.Totals != (Totals{}) {
		t.Errorf("three days ago counted in a day: %+v", day.Totals)
	}
	// Lines that do not say whose they are are refused.
	if _, _, err := ParseMinute([]byte(`{"time":"2026-09-27T12:00:00Z","down":1}`)); err == nil {
		t.Error("a minute without its id was read")
	}
}
