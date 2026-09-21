package tailscale

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// Binary is the command this package drives.
const Binary = "tailscale"

// Runner runs the tailscale command line. The real one execs it; tests
// hand over canned output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	// Stream starts the command and returns its stdout. The caller reads
	// to the end and then waits.
	Stream(ctx context.Context, name string, args ...string) (io.ReadCloser, func() error, error)
}

// Client talks to tailscaled through the CLI.
type Client struct {
	// Bin is the command; empty is Binary on PATH.
	Bin string
	// Run executes it; nil execs.
	Run Runner
}

// New returns a client that runs the installed tailscale command.
func New() *Client { return &Client{Bin: Binary, Run: execRunner{}} }

func (c *Client) bin() string {
	if c.Bin == "" {
		return Binary
	}
	return c.Bin
}

func (c *Client) runner() Runner {
	if c.Run == nil {
		return execRunner{}
	}
	return c.Run
}

// Status asks the daemon what it is doing. A daemon that is not running
// makes this an error, which is what the page turns into "not running".
func (c *Client) Status(ctx context.Context) (*Status, error) {
	out, err := c.runner().Run(ctx, c.bin(), "status", "--json")
	if err != nil {
		return nil, fmt.Errorf("tailscale status: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return ParseStatus(out)
}

// Set pushes the preferences. It works before a login, so an apply does not
// have to wait for one.
func (c *Client) Set(ctx context.Context, args []string) error {
	out, err := c.runner().Run(ctx, c.bin(), append([]string{"set"}, args...)...)
	if err != nil {
		return fmt.Errorf("tailscale set: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Logout drops the node's key. The node stays in the tailnet's admin
// console, so a later login puts this router back as it was.
func (c *Client) Logout(ctx context.Context) error {
	out, err := c.runner().Run(ctx, c.bin(), "logout")
	if err != nil {
		return fmt.Errorf("tailscale logout: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// upEvent is one line of `tailscale up --json`: the command prints an
// object per event rather than one document at the end.
type upEvent struct {
	AuthURL      string `json:"AuthURL"`
	BackendState string `json:"BackendState"`
	Error        string `json:"Error"`
}

// Login runs `tailscale up` and returns as soon as it knows whether a
// browser is needed. authURL is the link to open, empty when the node came
// up without one; done carries the command's own verdict later.
//
// The auth key is an argument of a root-only process for the length of the
// login and is written nowhere.
func (c *Client) Login(ctx context.Context, args []string, loginServer, authKey string) (string, <-chan error, error) {
	argv := append([]string{"up", "--reset", "--json", "--timeout=3m"}, args...)
	if loginServer != "" {
		argv = append(argv, "--login-server="+loginServer)
	}
	if authKey != "" {
		argv = append(argv, "--auth-key="+authKey)
	}
	stdout, wait, err := c.runner().Stream(ctx, c.bin(), argv...)
	if err != nil {
		return "", nil, fmt.Errorf("tailscale up: %w", err)
	}

	done := make(chan error, 1)
	first := make(chan result, 1)
	go func() {
		defer stdout.Close()
		var once sync.Once
		answer := func(r result) { once.Do(func() { first <- r }) }
		scan := bufio.NewScanner(stdout)
		for scan.Scan() {
			var ev upEvent
			if err := json.Unmarshal(scan.Bytes(), &ev); err != nil {
				continue
			}
			switch {
			case ev.Error != "":
				answer(result{err: errors.New(ev.Error)})
			case ev.AuthURL != "":
				answer(result{url: ev.AuthURL})
			case ev.BackendState == StateRunning:
				answer(result{})
			}
		}
		err := wait()
		// Nothing usable was printed, so the exit status is the answer.
		answer(result{err: err})
		done <- err
	}()

	r := <-first
	if r.err != nil {
		return "", nil, r.err
	}
	return r.url, done, nil
}

type result struct {
	url string
	err error
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (execRunner) Stream(ctx context.Context, name string, args ...string) (io.ReadCloser, func() error, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	wait := func() error {
		err := cmd.Wait()
		if err != nil && errBuf.Len() > 0 {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(errBuf.String()))
		}
		return err
	}
	return out, wait, nil
}
