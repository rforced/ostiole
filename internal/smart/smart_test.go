package smart

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fixture reads one of the documents smartctl printed on the router.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// runner stands in for smartctl: it answers by what the arguments ask
// for and records every command line, so a test can say both what was
// run and what never reached exec.
type runner struct {
	mu     sync.Mutex
	calls  [][]string
	scan   []byte
	read   []byte
	health []byte
	report []byte
}

func (r *runner) run(_ context.Context, _ string, args ...string) ([]byte, int, error) {
	r.mu.Lock()
	r.calls = append(r.calls, slices.Clone(args))
	r.mu.Unlock()
	switch {
	case slices.Contains(args, "--scan-open"):
		return r.scan, 0, nil
	case slices.Contains(args, "-H"):
		return r.health, 0, nil
	case slices.Contains(args, "-t"), slices.Contains(args, "-X"):
		return []byte(`{"smartctl":{"version":[7,5],"exit_status":0}}`), 0, nil
	case !slices.Contains(args, "--json=c"):
		return r.report, 0, nil
	default:
		return r.read, 0, nil
	}
}

func (r *runner) commands() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// ran reports whether any command line held the given argument.
func (r *runner) ran(arg string) bool {
	for _, c := range r.commands() {
		if slices.Contains(c, arg) {
			return true
		}
	}
	return false
}

func newClient(t *testing.T, scan, read string) (*Client, *runner) {
	t.Helper()
	r := &runner{
		scan:   fixture(t, scan),
		read:   fixture(t, read),
		health: fixture(t, read),
		report: []byte("smartctl 7.5 2025-04-30 r5714\nDevice Model: GOFATOO 256GB SSD\n"),
	}
	return &Client{Bin: "smartctl", Run: r.run}, r
}

// The one drive on the router, read through the document it printed: the
// identity, the figures, the logs, and the attributes that mean sectors
// are going.
func TestReadSATASSD(t *testing.T) {
	t.Parallel()
	c, r := newClient(t, "scan.json", "sat-ssd.json")

	devs, err := c.Scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].Name != "sda" || devs[0].Type != "sat" || devs[0].Protocol != "ATA" {
		t.Fatalf("scan = %+v", devs)
	}

	d, err := c.Read(t.Context(), "sda")
	if err != nil {
		t.Fatal(err)
	}
	if d.Model != "GOFATOO 256GB SSD" || d.Kind != "ssd" || d.Health != "passed" || !d.SMARTOn {
		t.Errorf("drive = %+v", d)
	}
	if d.Interface != "SATA 3.2 at 6.0 Gb/s" || d.FormFactor != "M.2" || d.InDatabase {
		t.Errorf("attachment = %q %q %v", d.Interface, d.FormFactor, d.InDatabase)
	}
	if d.Capacity != 256060514304 || d.Serial != "AA00000000000000TEST" || d.Firmware != "X0528A0" {
		t.Errorf("identity = %+v", d)
	}
	if d.Temperature == nil || *d.Temperature != 40 {
		t.Errorf("temperature = %v", d.Temperature)
	}
	if d.PowerOnHours == nil || *d.PowerOnHours != 57 || d.PowerCycles == nil || *d.PowerCycles != 47 {
		t.Errorf("hours = %v, cycles = %v", d.PowerOnHours, d.PowerCycles)
	}
	if d.Wear == nil || *d.Wear != 0 || d.Spare == nil || *d.Spare != 100 {
		t.Errorf("wear = %v, spare = %v", d.Wear, d.Spare)
	}
	// This drive has no device statistics page, so there is nothing to
	// claim about what has been written.
	if d.Written != nil || d.Read != nil {
		t.Errorf("written = %v, read = %v, want neither", d.Written, d.Read)
	}
	if d.Partial {
		t.Error("partial on a document with exit status 0")
	}
	if len(d.TestLog) != 1 || !d.TestLog[0].Passed || d.TestLog[0].Hours != 57 ||
		d.TestLog[0].Type != "Short offline" {
		t.Errorf("test log = %+v", d.TestLog)
	}
	if len(d.Errors) != 0 || d.ErrorCount != 0 {
		t.Errorf("errors = %+v (%d)", d.Errors, d.ErrorCount)
	}
	if d.SelfTest.Running || !d.SelfTest.Supported || d.SelfTest.Conveyance {
		t.Errorf("self-test = %+v", d.SelfTest)
	}
	if d.SelfTest.Status != "completed without error" || d.SelfTest.Passed == nil || !*d.SelfTest.Passed {
		t.Errorf("self-test status = %+v", d.SelfTest)
	}

	if len(d.Attributes) != 30 {
		t.Fatalf("attributes = %d, want 30", len(d.Attributes))
	}
	byID := map[int]Attribute{}
	for _, a := range d.Attributes {
		byID[a.ID] = a
	}
	for _, id := range []int{5, 197, 198} {
		if !byID[id].Critical {
			t.Errorf("attribute %d is not marked critical: %+v", id, byID[id])
		}
	}
	if byID[175].Name != "Program_Fail_Count_Chip" || byID[175].Critical {
		t.Errorf("attribute 175 = %+v, want a plain one", byID[175])
	}
	if !byID[161].Prefail || byID[161].Raw != 100 || byID[161].RawString != "100" {
		t.Errorf("attribute 161 = %+v", byID[161])
	}
	if c.Version() != "7.5" {
		t.Errorf("version = %q", c.Version())
	}
	// Nothing but --json=c, the scan's type, and the scan's device.
	last := r.commands()[len(r.commands())-1]
	if want := []string{"--json=c", "-x", "-d", "sat", "/dev/sda"}; !slices.Equal(last, want) {
		t.Errorf("read args = %v, want %v", last, want)
	}
}

// The document -x returns on the same drive: the logs come back under
// their general-purpose names, the statistics page says what has been
// written, and a SMART command that failed leaves the rest usable.
func TestReadExtended(t *testing.T) {
	t.Parallel()
	c, _ := newClient(t, "scan.json", "sat-ssd-x.json")
	d, err := c.Read(t.Context(), "sda")
	if err != nil {
		t.Fatal(err)
	}
	// Exit status 4: this drive has no error log to read, and the rest of
	// the document is still the drive.
	if !d.Partial || d.Health != "passed" {
		t.Errorf("drive = health %q, partial %v", d.Health, d.Partial)
	}
	if len(d.TestLog) != 2 || d.TestLog[0].Passed || !d.TestLog[1].Passed {
		t.Fatalf("test log = %+v, want the aborted test newest", d.TestLog)
	}
	if d.TestLog[0].Status != "Aborted by host" || d.TestLog[0].Hours != 57 {
		t.Errorf("newest test = %+v", d.TestLog[0])
	}
	if d.ErrorCount != 0 || len(d.Errors) != 0 {
		t.Errorf("errors = %d %+v", d.ErrorCount, d.Errors)
	}
	// 512-byte sectors off the General Statistics page.
	if d.Written == nil || *d.Written != 297608206*512 {
		t.Errorf("written = %v", d.Written)
	}
	if d.Read == nil || *d.Read != 595965108*512 {
		t.Errorf("read = %v", d.Read)
	}
	if d.TempMax == nil || *d.TempMax != 50 {
		t.Errorf("lifetime high = %v", d.TempMax)
	}
	if d.SelfTest.Running || d.SelfTest.Status != "was aborted by the host" {
		t.Errorf("self-test = %+v", d.SelfTest)
	}
}

// A test that is running reads as a percentage remaining, in tens, and
// the drive says how long each of its tests takes.
func TestReadWhileTesting(t *testing.T) {
	t.Parallel()
	c, _ := newClient(t, "scan.json", "sat-ssd-testing.json")
	d, err := c.Read(t.Context(), "sda")
	if err != nil {
		t.Fatal(err)
	}
	if !d.SelfTest.Running || d.SelfTest.Remaining != 90 {
		t.Errorf("self-test = %+v", d.SelfTest)
	}
	if d.SelfTest.ShortMinutes != 2 || d.SelfTest.ExtendedMinutes != 10 {
		t.Errorf("polling minutes = %+v", d.SelfTest)
	}
	if len(d.TestLog) != 0 {
		t.Errorf("test log = %+v, want an empty one rather than null", d.TestLog)
	}
}

// A device that will not open is an error with smartctl's own sentence,
// because there is no document behind it worth showing.
func TestOpenFailedIsAnError(t *testing.T) {
	t.Parallel()
	r := &runner{scan: fixture(t, "scan.json"), read: fixture(t, "open-failed.json")}
	c := &Client{Bin: "smartctl", Run: r.run}
	_, err := c.Read(t.Context(), "sda")
	if err == nil || !strings.Contains(err.Error(), "No such device") {
		t.Fatalf("err = %v, want the message smartctl printed", err)
	}
}

// Exit status 8 is the drive saying it is failing, which is a page, not
// an error; status 4 is a SMART command that failed, which leaves gaps
// in an otherwise usable document.
func TestExitStatusIsData(t *testing.T) {
	t.Parallel()
	raw := fixture(t, "sat-ssd.json")
	failing := bytes.Replace(raw, []byte(`"exit_status": 0`), []byte(`"exit_status": 8`), 1)
	failing = bytes.Replace(failing,
		[]byte("\"smart_status\": {\n  \"passed\": true\n }"),
		[]byte("\"smart_status\": {\n  \"passed\": false\n }"), 1)
	if bytes.Equal(failing, raw) {
		t.Fatal("the fixture no longer has the fields this test edits")
	}
	r := &runner{scan: fixture(t, "scan.json"), read: failing}
	c := &Client{Bin: "smartctl", Run: r.run}
	d, err := c.Read(t.Context(), "sda")
	if err != nil {
		t.Fatalf("a failing drive is not an error: %v", err)
	}
	if d.Health != "failed" || d.Partial {
		t.Errorf("drive = health %q, partial %v", d.Health, d.Partial)
	}

	r.read = bytes.Replace(raw, []byte(`"exit_status": 0`), []byte(`"exit_status": 4`), 1)
	d, err = c.Read(t.Context(), "sda")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Partial || d.Health != "passed" {
		t.Errorf("drive = health %q, partial %v", d.Health, d.Partial)
	}
}

// Nothing from a request reaches the command line: a name is resolved
// against the scan, and one that is not a kernel device never gets even
// that far.
func TestNamesAreResolvedAgainstTheScan(t *testing.T) {
	t.Parallel()
	c, r := newClient(t, "scan.json", "sat-ssd.json")
	if _, err := c.Read(t.Context(), "sdz"); !errors.Is(err, ErrNoDevice) {
		t.Errorf("err = %v, want ErrNoDevice", err)
	}
	if got := len(r.commands()); got != 1 {
		t.Errorf("%d commands for an unknown drive, want only the scan", got)
	}
	for _, name := range []string{"../../dev/sda", "sda;reboot", "sda.1", "/dev/sda", ""} {
		if _, err := c.Read(t.Context(), name); !errors.Is(err, ErrNoDevice) {
			t.Errorf("%q: err = %v, want ErrNoDevice", name, err)
		}
	}
	if got := len(r.commands()); got != 1 {
		t.Errorf("%d commands, want the scan and nothing a name could reach", got)
	}
}

// Starting and aborting a test go to the drive the scan named, with the
// type it reported, and a test the drive does not have is refused here
// rather than by smartctl.
func TestSelfTests(t *testing.T) {
	t.Parallel()
	c, r := newClient(t, "scan.json", "sat-ssd.json")
	if err := c.StartTest(t.Context(), "sda", TestShort); err != nil {
		t.Fatal(err)
	}
	last := r.commands()[len(r.commands())-1]
	if want := []string{"--json=c", "-t", "short", "-d", "sat", "/dev/sda"}; !slices.Equal(last, want) {
		t.Errorf("start args = %v, want %v", last, want)
	}
	if err := c.AbortTest(t.Context(), "sda"); err != nil {
		t.Fatal(err)
	}
	last = r.commands()[len(r.commands())-1]
	if want := []string{"--json=c", "-X", "-d", "sat", "/dev/sda"}; !slices.Equal(last, want) {
		t.Errorf("abort args = %v, want %v", last, want)
	}
	// This drive has no conveyance test, and no attempt is made to run one.
	if err := c.StartTest(t.Context(), "sda", TestConveyance); !errors.Is(err, ErrNoTests) {
		t.Errorf("conveyance: err = %v, want ErrNoTests", err)
	}
	if r.ran("conveyance") {
		t.Error("a test the drive does not have reached the command line")
	}
	if err := c.StartTest(t.Context(), "sda", TestKind("destroy")); !errors.Is(err, ErrBadTest) {
		t.Errorf("unknown kind: err = %v, want ErrBadTest", err)
	}
}

// A drive that has spun down is left asleep: the verdict is skipped, not
// a failure to open.
func TestHealthSkipsASleepingDrive(t *testing.T) {
	t.Parallel()
	c, r := newClient(t, "scan.json", "sat-ssd.json")
	devs, err := c.Scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.Health(t.Context(), devs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !h.Passed || h.Skipped || h.Model != "GOFATOO 256GB SSD" {
		t.Errorf("health = %+v", h)
	}
	last := r.commands()[len(r.commands())-1]
	if want := []string{"--json=c", "-H", "-i", "-n", "standby", "-d", "sat", "/dev/sda"}; !slices.Equal(last, want) {
		t.Errorf("health args = %v, want %v", last, want)
	}

	r.health = []byte(`{"smartctl":{"version":[7,5],"exit_status":2,"messages":[` +
		`{"string":"Device is in STANDBY mode, exit(2)","severity":"error"}]}}`)
	h, err = c.Health(t.Context(), devs[0])
	if err != nil {
		t.Fatalf("a sleeping drive is not an error: %v", err)
	}
	if !h.Skipped || h.Passed {
		t.Errorf("health = %+v, want skipped", h)
	}

	r.health = []byte(`{"smartctl":{"version":[7,5],"exit_status":2,"messages":[` +
		`{"string":"Smartctl open device: /dev/sda failed","severity":"error"}]}}`)
	if _, err := c.Health(t.Context(), devs[0]); err == nil {
		t.Error("a device that would not open is an error")
	}
}

// A drive with SMART switched off answers -H with exit bit 2 and no
// verdict. That is a drive that said nothing, not one that said it is
// failing, and the poll must not warn about it.
func TestHealthWithoutAVerdictIsSkipped(t *testing.T) {
	t.Parallel()
	c, r := newClient(t, "scan.json", "sat-ssd.json")
	devs, err := c.Scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r.health = []byte(`{"smartctl":{"version":[7,5],"exit_status":4},"model_name":"GOFATOO 256GB SSD",` +
		`"smart_support":{"available":true,"enabled":false}}`)
	h, err := c.Health(t.Context(), devs[0])
	if err != nil {
		t.Fatalf("no verdict is not an error: %v", err)
	}
	if !h.Skipped || h.Passed || h.Model != "GOFATOO 256GB SSD" {
		t.Errorf("health = %+v, want skipped and named", h)
	}
	m := &Monitor{Client: c}
	m.Check(t.Context())
	if got := m.Failing(); len(got) != 0 {
		t.Errorf("failing = %+v, want none for a drive with no verdict", got)
	}
}

// Without the binary there is nothing to say and nothing to run.
func TestNoToolEverywhere(t *testing.T) {
	t.Parallel()
	c := &Client{}
	ctx := t.Context()
	if _, err := c.Scan(ctx); !errors.Is(err, ErrNoTool) {
		t.Errorf("scan: %v", err)
	}
	if _, err := c.Read(ctx, "sda"); !errors.Is(err, ErrNoTool) {
		t.Errorf("read: %v", err)
	}
	if _, err := c.ReadAll(ctx); !errors.Is(err, ErrNoTool) {
		t.Errorf("read all: %v", err)
	}
	if _, err := c.Health(ctx, Device{Name: "sda"}); !errors.Is(err, ErrNoTool) {
		t.Errorf("health: %v", err)
	}
	if err := c.StartTest(ctx, "sda", TestShort); !errors.Is(err, ErrNoTool) {
		t.Errorf("start: %v", err)
	}
	if err := c.AbortTest(ctx, "sda"); !errors.Is(err, ErrNoTool) {
		t.Errorf("abort: %v", err)
	}
	if _, err := c.Report(ctx, "sda"); !errors.Is(err, ErrNoTool) {
		t.Errorf("report: %v", err)
	}
	if v := c.Version(); v != "" {
		t.Errorf("version = %q", v)
	}
}

// The full report is text, not JSON, and it is what a vendor asks for.
func TestReportIsText(t *testing.T) {
	t.Parallel()
	c, r := newClient(t, "scan.json", "sat-ssd.json")
	out, err := c.Report(t.Context(), "sda")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("GOFATOO")) {
		t.Errorf("report = %q", out)
	}
	last := r.commands()[len(r.commands())-1]
	if want := []string{"-x", "-d", "sat", "/dev/sda"}; !slices.Equal(last, want) {
		t.Errorf("report args = %v, want %v", last, want)
	}
}

func TestReadAll(t *testing.T) {
	t.Parallel()
	c, _ := newClient(t, "scan.json", "sat-ssd.json")
	drives, err := c.ReadAll(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(drives) != 1 || drives[0].Name != "sda" {
		t.Errorf("drives = %+v", drives)
	}
}

// An NVMe drive keeps a health log where an ATA drive keeps attributes,
// and counts what it has written in units of 512,000 bytes.
func TestReadNVMe(t *testing.T) {
	t.Parallel()
	c, _ := newClient(t, "scan-nvme.json", "nvme-ssd.json")
	d, err := c.Read(t.Context(), "nvme0")
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != "nvme" || d.Health != "passed" || d.Interface != "NVMe 1.4" {
		t.Errorf("drive = %+v", d)
	}
	if d.Capacity != 1000204886016 || d.Model != "CT1000P5SSD8" {
		t.Errorf("identity = %+v", d)
	}
	if d.Wear == nil || *d.Wear != 3 || d.Spare == nil || *d.Spare != 100 {
		t.Errorf("wear = %v, spare = %v", d.Wear, d.Spare)
	}
	if d.Written == nil || *d.Written != 8340662*dataUnit {
		t.Errorf("written = %v", d.Written)
	}
	if d.Read == nil || *d.Read != 12655201*dataUnit {
		t.Errorf("read = %v", d.Read)
	}
	if d.Temperature == nil || *d.Temperature != 38 || d.PowerOnHours == nil || *d.PowerOnHours != 4412 {
		t.Errorf("figures = %v %v", d.Temperature, d.PowerOnHours)
	}
	if d.Attributes != nil {
		t.Errorf("attributes = %+v, want none on an NVMe drive", d.Attributes)
	}
	if d.NVMe == nil {
		t.Fatal("no health log")
	}
	if d.NVMe.PercentageUsed != 3 || d.NVMe.AvailableSpare != 100 || d.NVMe.SpareThreshold != 5 {
		t.Errorf("health log = %+v", d.NVMe)
	}
	if d.NVMe.UnsafeShutdowns != 41 || d.NVMe.MediaErrors != 0 || d.NVMe.ErrorLogEntries != 2 {
		t.Errorf("health log = %+v", d.NVMe)
	}
	if !slices.Equal(d.NVMe.Sensors, []int{38, 42}) {
		t.Errorf("sensors = %v", d.NVMe.Sensors)
	}
	if !d.SelfTest.Supported || d.SelfTest.Running || d.SelfTest.Conveyance {
		t.Errorf("self-test = %+v", d.SelfTest)
	}
	if len(d.TestLog) != 2 || !d.TestLog[0].Passed || d.TestLog[1].Passed {
		t.Errorf("test log = %+v", d.TestLog)
	}
	if d.ErrorCount != 2 || len(d.Errors) != 2 || d.Errors[0].Description != "Invalid Field in Command" {
		t.Errorf("errors = %d %+v", d.ErrorCount, d.Errors)
	}
}
