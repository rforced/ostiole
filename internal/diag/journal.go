package diag

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// unitRe keeps a unit name to what systemd allows, since it reaches the
// journalctl command line.
var unitRe = regexp.MustCompile(`^[A-Za-z0-9:_.\\@-]+$`)

// JournalOptions parameterise a journal query.
type JournalOptions struct {
	// Unit filters to one systemd unit; empty reads everything.
	Unit string
	// Own keeps the read to Ostiole's units: Unit has to be one, and
	// without Unit it reads all of them rather than the whole host.
	Own bool
	// Lines is how many of the newest entries to return.
	Lines int
	// Since is a systemd time expression like "-1h"; empty means no limit.
	Since string
	// Priority keeps entries at or below this syslog level (3 = errors).
	Priority int
}

// JournalEntry is one line of the journal.
type JournalEntry struct {
	Time     time.Time `json:"time"`
	Unit     string    `json:"unit,omitempty"`
	Host     string    `json:"host,omitempty"`
	Priority int       `json:"priority"`
	Message  string    `json:"message"`
}

// MaxJournalLines caps one query.
const MaxJournalLines = 2000

// ownUnits are Ostiole's units as journalctl patterns: the daemon, what it
// runs, and the network backend it drives.
var ownUnits = []string{"ostiole.service", "ostiole-*", "systemd-networkd.service"}

// OwnUnit reports whether a unit is one of Ostiole's, as ownUnits has them.
func OwnUnit(unit string) bool {
	name := strings.TrimSuffix(unit, ".service")
	return name == "ostiole" || name == "systemd-networkd" || strings.HasPrefix(name, "ostiole-")
}

// Journal reads recent entries with journalctl, newest first. Reading the
// binary journal format directly would need cgo or a reimplementation; the
// tool is always there on a systemd router, and it is run without a shell.
func Journal(ctx context.Context, o JournalOptions) ([]JournalEntry, error) {
	args, err := journalArgs(o)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("journalctl: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseJournal(out), nil
}

// sinceRe is what --since may be given: -1h, 2026-09-15 10:00:00, or @ and
// the seconds since 1970.
var sinceRe = regexp.MustCompile(`^[-+@]?[0-9a-zA-Z: -]{1,32}$`)

// journalArgs builds the command line. --lines takes the newest entries
// and --reverse hands them back newest first, which is the order every log
// in the UI reads in.
func journalArgs(o JournalOptions) ([]string, error) {
	lines := o.Lines
	if lines <= 0 || lines > MaxJournalLines {
		lines = 200
	}
	// --all, because without it journalctl reports a field over a few
	// kilobytes as null rather than printing it, and a WAF audit entry is
	// tens of kilobytes.
	args := []string{"--output=json", "--no-pager", "--all", "--reverse", "--lines=" + strconv.Itoa(lines)}
	switch {
	case o.Unit != "":
		if !unitRe.MatchString(o.Unit) {
			return nil, fmt.Errorf("invalid unit name %q", o.Unit)
		}
		if o.Own && !OwnUnit(o.Unit) {
			return nil, fmt.Errorf("%s is not one of Ostiole's units", o.Unit)
		}
		args = append(args, "--unit="+o.Unit)
	case o.Own:
		for _, u := range ownUnits {
			args = append(args, "--unit="+u)
		}
	}
	if o.Since != "" {
		if !sinceRe.MatchString(o.Since) {
			return nil, fmt.Errorf("invalid time %q: use something like -1h or 2026-09-15", o.Since)
		}
		args = append(args, "--since="+o.Since)
	}
	if o.Priority > 0 {
		args = append(args, "--priority="+strconv.Itoa(o.Priority))
	}
	return args, nil
}

// parseJournal reads journalctl's line-per-entry JSON.
func parseJournal(raw []byte) []JournalEntry {
	out := []JournalEntry{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var e struct {
			Realtime string          `json:"__REALTIME_TIMESTAMP"`
			Unit     string          `json:"_SYSTEMD_UNIT"`
			Host     string          `json:"_HOSTNAME"`
			Priority string          `json:"PRIORITY"`
			Message  json.RawMessage `json:"MESSAGE"`
			Comm     string          `json:"_COMM"`
		}
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		entry := JournalEntry{Unit: e.Unit, Host: e.Host, Message: decodeMessage(e.Message)}
		if entry.Unit == "" {
			entry.Unit = e.Comm
		}
		if us, err := strconv.ParseInt(e.Realtime, 10, 64); err == nil {
			entry.Time = time.UnixMicro(us).UTC()
		}
		if p, err := strconv.Atoi(e.Priority); err == nil {
			entry.Priority = p
		}
		out = append(out, entry)
	}
	return out
}

// decodeMessage handles both forms journalctl emits: a string, or an
// array of bytes when the message is not valid UTF-8.
func decodeMessage(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var b []byte
	if err := json.Unmarshal(raw, &b); err == nil {
		return string(bytes.ToValidUTF8(b, []byte("?")))
	}
	return ""
}
