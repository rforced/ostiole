// Package smart reads what a drive says about itself through smartctl's
// JSON output, and starts and stops its self-tests. Nothing is kept
// between calls except the hourly health verdicts.
package smart

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/panics"
)

var (
	// ErrNoTool says smartctl is not on this router, which is the whole
	// answer: nothing about a drive can be read without it.
	ErrNoTool = errors.New("smartctl is not installed")
	// ErrNoDevice says the scan does not know that drive.
	ErrNoDevice = errors.New("no such drive")
	// ErrNoTests says the drive does not offer the test that was asked for.
	ErrNoTests = errors.New("this drive does not support self-tests")
	// ErrBadTest says the kind is not one of the three.
	ErrBadTest = errors.New("unknown self-test")
)

// Client runs smartctl.
type Client struct {
	// Bin is the binary; empty means none was found and every call
	// returns ErrNoTool.
	Bin string
	// Run runs the binary and returns stdout, the exit status and any
	// error that is not the exit status; nil execs for real.
	Run func(ctx context.Context, bin string, args ...string) ([]byte, int, error)

	mu sync.Mutex
	// version is what the last document said smartctl is, so the page can
	// name it without a call of its own.
	version string
}

// New looks for the binary. A miss leaves Bin empty rather than failing,
// so the page can say what is missing instead of disappearing.
func New(bin string) *Client {
	if bin == "" {
		bin = "smartctl"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return &Client{}
	}
	return &Client{Bin: path}
}

// Version is smartctl's own version, as the last call saw it. Empty until
// something has been read.
func (c *Client) Version() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

// Device is one drive the scan found.
type Device struct {
	Name     string `json:"name"`     // sda, nvme0
	Type     string `json:"type"`     // sat, nvme, scsi, as smartctl names it
	Protocol string `json:"protocol"` // ATA, NVMe, SCSI
	// path is what the scan called the device. Nothing else ever reaches
	// the command line, which is what keeps a request out of exec.
	path string
}

// TestKind is one of the self-tests a drive offers.
type TestKind string

// The three a drive is asked for by name. Short is a couple of minutes,
// long reads the whole surface, and conveyance looks for shipping damage.
const (
	TestShort      TestKind = "short"
	TestLong       TestKind = "long"
	TestConveyance TestKind = "conveyance"
)

// Health is one drive's verdict, which is all the hourly poll keeps.
type Health struct {
	Device
	Model  string `json:"model"`
	Passed bool   `json:"passed"`
	// Skipped says there is no verdict: the drive was asleep, or it has
	// SMART switched off and had nothing to say.
	Skipped bool      `json:"skipped"`
	Checked time.Time `json:"checked"`
}

// nameRe is what a kernel device is called. It is checked before a name
// reaches the scan, so a path or a shell character never gets that far.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Scan lists the drives smartctl can open.
func (c *Client) Scan(ctx context.Context) ([]Device, error) {
	doc, err := c.run(ctx, "--json=c", "--scan-open")
	if err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(doc.Devices))
	for _, d := range doc.Devices {
		out = append(out, Device{
			Name:     strings.TrimPrefix(d.Name, "/dev/"),
			Type:     d.Type,
			Protocol: d.Protocol,
			path:     d.Name,
		})
	}
	return out, nil
}

// Read reports everything one drive says about itself. The name is
// resolved against the scan, so an invented one is ErrNoDevice rather
// than a path smartctl is asked to open.
func (c *Client) Read(ctx context.Context, name string) (*Drive, error) {
	d, err := c.device(ctx, name)
	if err != nil {
		return nil, err
	}
	return c.readDevice(ctx, d)
}

// ReadAll reads every drive the scan found, at the same time. A drive
// that cannot be read is left out; when none can be read, the first
// failure is returned, because then the page has nothing to show.
func (c *Client) ReadAll(ctx context.Context) ([]Drive, error) {
	devs, err := c.Scan(ctx)
	if err != nil {
		return nil, err
	}
	drives := make([]*Drive, len(devs))
	errs := make([]error, len(devs))
	var wg sync.WaitGroup
	for i, d := range devs {
		wg.Go(func() {
			// smartctl reports what the drive's firmware says, whatever that is.
			defer panics.Into(&errs[i], nil, "reading "+d.Name)
			drives[i], errs[i] = c.readDevice(ctx, d)
		})
	}
	wg.Wait()
	out := make([]Drive, 0, len(devs))
	for _, d := range drives {
		if d != nil {
			out = append(out, *d)
		}
	}
	if len(out) == 0 {
		return out, errors.Join(errs...)
	}
	return out, nil
}

// Health asks one drive only whether it is failing, and never spins up a
// disk that has gone to sleep to find out.
func (c *Client) Health(ctx context.Context, d Device) (Health, error) {
	h := Health{Device: d, Checked: time.Now()}
	// -i as well as -H: the verdict alone carries no model name, and a
	// warning that cannot name the drive is a warning about nothing. The
	// standby check comes first, so neither flag wakes the disk.
	doc, err := c.exec(ctx, c.args(d, "-H", "-i", "-n", "standby")...)
	if err != nil {
		return h, err
	}
	// A sleeping disk is reported as a failure to open it, which it is
	// not: it is a drive that will answer when it is next awake.
	if doc.Smartctl.ExitStatus&(exitCommandLine|exitOpenFailed) != 0 {
		if strings.Contains(strings.ToUpper(doc.messages()), "STANDBY") {
			h.Skipped = true
			return h, nil
		}
		return h, doc.failure()
	}
	h.Model = doc.ModelName
	// No verdict at all (SMART switched off, or a device that has none)
	// is not a failed one. Reporting it as failing would put a warning on
	// the dashboard about a drive that said nothing.
	if doc.SMARTStatus == nil {
		h.Skipped = true
		return h, nil
	}
	h.Passed = doc.SMARTStatus.Passed
	return h, nil
}

// StartTest runs one of the drive's self-tests. The drive is read first,
// because asking for a test it does not have is a mistake worth naming.
func (c *Client) StartTest(ctx context.Context, name string, kind TestKind) error {
	switch kind {
	case TestShort, TestLong, TestConveyance:
	default:
		return fmt.Errorf("%w: %q", ErrBadTest, kind)
	}
	d, err := c.device(ctx, name)
	if err != nil {
		return err
	}
	drive, err := c.readDevice(ctx, d)
	if err != nil {
		return err
	}
	if !drive.SelfTest.Supported || (kind == TestConveyance && !drive.SelfTest.Conveyance) {
		return ErrNoTests
	}
	_, err = c.run(ctx, c.args(d, "-t", string(kind))...)
	return err
}

// AbortTest stops the self-test that is running. A drive with none
// running says so and is not an error.
func (c *Client) AbortTest(ctx context.Context, name string) error {
	d, err := c.device(ctx, name)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, c.args(d, "-X")...)
	return err
}

// Report is the whole of smartctl's output as text, which is what a
// vendor or a forum asks for.
func (c *Client) Report(ctx context.Context, name string) ([]byte, error) {
	d, err := c.device(ctx, name)
	if err != nil {
		return nil, err
	}
	out, status, err := c.raw(ctx, "-x", "-d", d.Type, d.path)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, fmt.Errorf("smartctl said nothing about %s (exit %d)", name, status)
	}
	return out, nil
}

// device resolves a name against the scan. Names are never joined to
// /dev from anything but a scan result.
func (c *Client) device(ctx context.Context, name string) (Device, error) {
	if c.Bin == "" {
		return Device{}, ErrNoTool
	}
	if !nameRe.MatchString(name) {
		return Device{}, fmt.Errorf("%w: %q", ErrNoDevice, name)
	}
	devs, err := c.Scan(ctx)
	if err != nil {
		return Device{}, err
	}
	for _, d := range devs {
		if d.Name == name {
			return d, nil
		}
	}
	return Device{}, fmt.Errorf("%w: %q", ErrNoDevice, name)
}

func (c *Client) readDevice(ctx context.Context, d Device) (*Drive, error) {
	doc, err := c.run(ctx, c.args(d, "-x")...)
	if err != nil {
		return nil, err
	}
	return doc.drive(d), nil
}

// args builds a command line: the JSON flag first, then the type the scan
// reported, then the device it reported.
func (c *Client) args(d Device, rest ...string) []string {
	args := make([]string, 0, len(rest)+4)
	args = append(args, "--json=c")
	args = append(args, rest...)
	if d.Type != "" {
		args = append(args, "-d", d.Type)
	}
	return append(args, d.path)
}

// The bits smartctl's exit status carries. The rest (3 to 7) say a drive
// failed, an attribute is low, or a log has entries, all of which the
// document already reports in full.
const (
	exitCommandLine   = 1 << 0
	exitOpenFailed    = 1 << 1
	exitCommandFailed = 1 << 2
)

// run parses a document and refuses the two statuses that mean there is
// no document worth reading: a bad command line and a device that would
// not open.
func (c *Client) run(ctx context.Context, args ...string) (*document, error) {
	doc, err := c.exec(ctx, args...)
	if err != nil {
		return nil, err
	}
	if err := doc.failure(); err != nil {
		return nil, err
	}
	return doc, nil
}

// exec runs smartctl and parses what it printed. A non-zero exit with a
// document on stdout is data, not failure: that is how a drive reports
// that it is dying.
func (c *Client) exec(ctx context.Context, args ...string) (*document, error) {
	out, status, err := c.raw(ctx, args...)
	if err != nil {
		return nil, err
	}
	var doc document
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("smartctl exited %d without readable JSON: %w", status, err)
	}
	if v := doc.version(); v != "" {
		c.mu.Lock()
		c.version = v
		c.mu.Unlock()
	}
	return &doc, nil
}

func (c *Client) raw(ctx context.Context, args ...string) ([]byte, int, error) {
	if c.Bin == "" {
		return nil, 0, ErrNoTool
	}
	runner := c.Run
	if runner == nil {
		runner = execRun
	}
	out, status, err := runner(ctx, c.Bin, args...)
	// A drive that hangs on a SMART command is exactly the drive this
	// page is for, so the deadline is reported as itself rather than as
	// whatever the killed process left behind.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, 0, ctxErr
	}
	return out, status, err
}

// execRun is the real runner: stdout and stderr apart, the C locale so
// the strings are the ones this package matches on, and the exit status
// pulled out rather than treated as a failure.
func execRun(ctx context.Context, bin string, args ...string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return stdout.Bytes(), 0, nil
	case errors.As(err, &ee):
		return stdout.Bytes(), ee.ExitCode(), nil
	default:
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, 0, fmt.Errorf("%s: %w: %s", bin, err, msg)
		}
		return nil, 0, fmt.Errorf("%s: %w", bin, err)
	}
}
