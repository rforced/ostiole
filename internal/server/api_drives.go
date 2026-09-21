package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/rforced/ostiole/internal/smart"
)

// Reading a drive is a viewer's business; starting a self-test is an
// operator's, because it slows the drive down while it runs.
func (a *api) registerDrives(mux *router) {
	mux.HandleFunc("GET /api/v1/diagnostics/drives", a.readNoEngine(a.drivesList))
	mux.HandleFunc("GET /api/v1/diagnostics/drives/{name}", a.readNoEngine(a.driveRead))
	mux.HandleFunc("POST /api/v1/diagnostics/drives/{name}/self-test", a.write(a.driveStartTest))
	mux.HandleFunc("DELETE /api/v1/diagnostics/drives/{name}/self-test", a.write(a.driveAbortTest))
	mux.HandleFunc("GET /api/v1/diagnostics/drives/{name}/report", a.readNoEngine(a.driveReport))
}

type drivesResponse struct {
	// Tool is smartctl's version, empty when it is not installed.
	Tool string `json:"tool"`
	// Root says the daemon can open the devices. Without it the scan finds
	// nothing, and an empty page has to say which of the two it is.
	Root   bool          `json:"root"`
	Drives []smart.Drive `json:"drives"`
}

// drivesList reads every drive the scan finds. A router with no tool
// answers with an empty list rather than an error: the page explains
// that, and there is nothing to retry.
func (a *api) drivesList(w http.ResponseWriter, r *http.Request) error {
	res := drivesResponse{Root: a.host.Root, Drives: []smart.Drive{}}
	if a.drives == nil || a.drives.Bin == "" {
		writeJSON(w, http.StatusOK, res)
		return nil
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	drives, err := a.drives.ReadAll(ctx)
	if err != nil {
		return driveError(err)
	}
	res.Tool, res.Drives = a.drives.Version(), drives
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (a *api) driveRead(w http.ResponseWriter, r *http.Request) error {
	return a.writeDrive(w, r, r.PathValue("name"))
}

type selfTestRequest struct {
	Kind string `json:"kind"`
}

// driveStartTest runs one of the drive's own tests and answers with the
// drive as it reads once the test has begun, so the page shows progress
// without a second request.
func (a *api) driveStartTest(w http.ResponseWriter, r *http.Request) error {
	var req selfTestRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	kind := smart.TestKind(req.Kind)
	switch kind {
	case smart.TestShort, smart.TestLong, smart.TestConveyance:
	default:
		return &badRequest{fmt.Errorf("unknown test %q: short, long, or conveyance", req.Kind)}
	}
	name := r.PathValue("name")
	if err := a.runTest(r, func(ctx context.Context, c *smart.Client) error {
		return c.StartTest(ctx, name, kind)
	}); err != nil {
		return err
	}
	return a.writeDrive(w, r, name)
}

func (a *api) driveAbortTest(w http.ResponseWriter, r *http.Request) error {
	name := r.PathValue("name")
	if err := a.runTest(r, func(ctx context.Context, c *smart.Client) error {
		return c.AbortTest(ctx, name)
	}); err != nil {
		return err
	}
	return a.writeDrive(w, r, name)
}

// runTest bounds a command that only starts or stops something; the
// drive's answer to it is immediate whatever the test then takes.
func (a *api) runTest(r *http.Request, do func(context.Context, *smart.Client) error) error {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	err := do(ctx, a.driveClient())
	switch {
	case errors.Is(err, smart.ErrNoTests), errors.Is(err, smart.ErrBadTest):
		return &badRequest{err}
	case err != nil:
		return driveError(err)
	}
	return nil
}

// driveReport streams smartctl's whole output as text, which is what a
// vendor or a forum asks for. It goes to the operator's browser and
// nowhere else, serial number included.
func (a *api) driveReport(w http.ResponseWriter, r *http.Request) error {
	name := r.PathValue("name")
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	out, err := a.driveClient().Report(ctx, name)
	if err != nil {
		return driveError(err)
	}
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q",
		"ostiole-"+name+"-smart-"+time.Now().UTC().Format("20060102-150405")+".txt"))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	// The drive's own words, as a plain-text attachment that the browser
	// is told not to sniff; nothing here is rendered as a page.
	_, _ = w.Write(out) //nolint:gosec // G705: text/plain attachment, nosniff, never rendered
	return nil
}

func (a *api) writeDrive(w http.ResponseWriter, r *http.Request, name string) error {
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	d, err := a.driveClient().Read(ctx, name)
	if err != nil {
		return driveError(err)
	}
	writeJSON(w, http.StatusOK, d)
	return nil
}

// driveClient keeps a daemon that was built without one behaving like a
// router that has no smartctl, which is the same page either way.
func (a *api) driveClient() *smart.Client {
	if a.drives == nil {
		return &smart.Client{}
	}
	return a.drives
}

// driveError says which kind of nothing happened: no tool, or a drive
// that stopped answering. A drive that is not there is left to statusFor,
// which makes it a 404.
func driveError(err error) error {
	switch {
	case errors.Is(err, smart.ErrNoTool):
		return &unavailable{err}
	case errors.Is(err, context.DeadlineExceeded):
		return &unavailable{errors.New("the drive did not answer in time")}
	}
	return err
}
