package main

import (
	"bytes"
	"io"
	"os"

	"github.com/caddyserver/caddy/v2"

	"github.com/rforced/ostiole/internal/journald"
)

// The proxy logs to stderr through journalWriter, which puts each line's
// syslog priority in front while stderr is the journal: journald takes
// <3> as an error and strips it, and files a line without one as info.
func init() {
	caddy.RegisterModule(journalWriter{})
}

type journalWriter struct{}

var _ caddy.WriterOpener = journalWriter{}

func (journalWriter) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "caddy.logging.writers." + journald.CaddyWriter,
		New: func() caddy.Module { return new(journalWriter) },
	}
}

func (journalWriter) String() string    { return "stderr with priorities" }
func (journalWriter) WriterKey() string { return "ostiole:journal" }

func (journalWriter) OpenWriter() (io.WriteCloser, error) {
	return prioritized{out: os.Stderr, journal: journald.Stream(os.Stderr)}, nil
}

// prioritized writes each line with the priority of the level Caddy's JSON
// puts first on it. A logger writes a line in one call.
type prioritized struct {
	out     io.Writer
	journal bool
}

func (p prioritized) Write(line []byte) (int, error) {
	pri := linePriority(line)
	if !p.journal || pri == 0 {
		return p.out.Write(line)
	}
	out := make([]byte, 0, len(line)+3)
	out = append(append(out, '<', pri, '>'), line...)
	if _, err := p.out.Write(out); err != nil {
		return 0, err
	}
	return len(line), nil
}

// Close leaves stderr open: it is the process's, not the writer's.
func (prioritized) Close() error { return nil }

// linePriority is the syslog priority of a line's level, zero for a line
// that does not start with one.
func linePriority(line []byte) byte {
	rest, ok := bytes.CutPrefix(line, []byte(`{"level":"`))
	if !ok {
		return 0
	}
	level, _, _ := bytes.Cut(rest, []byte(`"`))
	switch string(level) {
	case "debug":
		return '7'
	case "info":
		return '6'
	case "warn":
		return '4'
	case "error":
		return '3'
	case "dpanic", "panic", "fatal":
		return '2'
	}
	return 0
}
