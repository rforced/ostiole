// Package audit keeps who did what on the router: applies and what became
// of them, sign-ins, accounts and tokens, Clears and the other actions an
// admin answers for. The log lives beside the configuration, outlasts
// restarts and updates, and is written by the daemon and the command line
// alike.
package audit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"ostiole/internal/atomicfile"
	"ostiole/internal/logsearch"
)

// FileName is the log's file in the configuration directory.
const FileName = "audit.jsonl"

// Name is the log among the logs a Clear takes.
const Name = "audit"

const lockFile = ".audit.lock"

// Keep is how long an entry is kept, and MaxEntries how many at most.
const (
	Keep       = 365 * 24 * time.Hour
	MaxEntries = 10000
)

// Kinds of actor. The router itself, undoing an apply nobody confirmed,
// has none.
const (
	Account = "account"
	Token   = "token"
	Shell   = "shell"
)

// Actor is who did something: an account signed in to the UI, an API
// token, or someone at a shell on the router.
type Actor struct {
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
	// Role is the account's or the token's role at the time.
	Role string `json:"role,omitempty"`
	// Address is where the request came from, or a shell's SSH client.
	Address string `json:"address,omitempty"`
}

// Same reports whether a and b are one account, token or shell user.
func (a Actor) Same(b Actor) bool { return a.Kind == b.Kind && a.Name == b.Name }

// Who names the actor as a page shows it.
func (a Actor) Who() string {
	if a.Name == "" {
		return "Router"
	}
	return a.Name
}

// Actions, with what Target and Detail hold for each.
const (
	Apply             = "apply"   // Detail: the confirm window in seconds, empty for none
	Confirm           = "confirm" // Target: who applied
	Revert            = "revert"  // Target: who applied
	Expire            = "expire"  // Target: who applied; Detail: the window in seconds
	Recover           = "recover" // Target: who applied
	Init              = "init"
	SignIn            = "sign-in"
	Password          = "password"
	Setup             = "setup"
	AccountCreate     = "account-create" // Target: the account; Detail: its role
	AccountDelete     = "account-delete" // Target: the account
	AccountRole       = "account-role"   // Target: the account; Detail: the new role
	AccountRename     = "account-rename" // Target: the old name; Detail: the new one
	AccountPassword   = "account-password"
	TokenCreate       = "token-create" // Target: the token; Detail: its role, "metrics" or "certificates"
	TokenDelete       = "token-delete"
	LogClear          = "log-clear" // Target: the log as a sentence names it
	LogsClear         = "logs-clear"
	Reboot            = "reboot"
	Update            = "update" // Detail: the version
	OSUpdate          = "os-update"
	Backup            = "backup"        // Detail: "with accounts" or "without secrets"
	RemoteDelete      = "remote-delete" // Target: the copy
	RemoteDeleteAll   = "remote-delete-all"
	Capture           = "capture" // Target: the interface
	TailscaleLogin    = "tailscale-login"
	TailscaleLogout   = "tailscale-logout"
	CronRun           = "cron-run"           // Target: the job
	CertificateExport = "certificate-export" // Target: the certificate
	LeftoversClear    = "leftovers-clear"    // Detail: the tables
	BlocklistImport   = "blocklist-import"   // Target: the list
)

// Event is one thing someone did.
type Event struct {
	Seq    uint64    `json:"seq"`
	Time   time.Time `json:"time"`
	Action string    `json:"action"`
	By     Actor     `json:"by"`
	Target string    `json:"target,omitempty"`
	Detail string    `json:"detail,omitempty"`
	// Text is the entry as a sentence, filled in when it is read, never
	// kept.
	Text string `json:"text,omitempty"`
}

// Search adds what the row shows, as a search of the log matches it: the
// same values as auditValues in web/src/lib/audit.js.
func (e *Event) Search(a logsearch.Adder) {
	a.Add(e.Sentence())
	a.Add(e.By.Who())
	if e.By.Kind != Account {
		a.Add(e.By.Kind)
	}
	a.Add(e.By.Role)
	a.Add(e.By.Address)
}

// Sentence is the entry as the UI and the command line say it.
func (e *Event) Sentence() string {
	t, d := e.Target, e.Detail
	switch e.Action {
	case Apply:
		if w := window(d); w != "" {
			return "Applied the configuration, to be confirmed within " + w
		}
		return "Applied the configuration"
	case Confirm:
		return "Confirmed " + e.whose()
	case Revert:
		return "Reverted " + e.whose()
	case Expire:
		s := "Undid " + e.whose()
		if w := window(d); w != "" {
			s += ", not confirmed within " + w
		}
		return s
	case Recover:
		if t == "" {
			return "Undid an unfinished apply at start"
		}
		return "Undid " + t + "'s unfinished apply at start"
	case Init:
		return "Wrote a starter configuration"
	case SignIn:
		return "Signed in"
	case Password:
		return "Changed their password"
	case Setup:
		return "Created the first account"
	case AccountCreate:
		return "Created the account " + t + as(d)
	case AccountDelete:
		return "Deleted the account " + t
	case AccountRole:
		return "Changed " + t + "'s role to " + d
	case AccountRename:
		return "Renamed the account " + t + " to " + d
	case AccountPassword:
		return "Set a new password for " + t
	case TokenCreate:
		switch d {
		case "metrics":
			return "Created the token " + t + " for the metrics"
		case "certificates":
			return "Created the token " + t + " for certificates"
		}
		return "Created the token " + t + as(d)
	case TokenDelete:
		return "Deleted the token " + t
	case LogClear:
		return "Cleared the " + t
	case LogsClear:
		return "Cleared every log"
	case Reboot:
		return "Rebooted the router"
	case Update:
		if d == "" {
			return "Started updating Ostiole"
		}
		return "Started updating Ostiole to " + d
	case OSUpdate:
		return "Started installing operating system updates"
	case Backup:
		if d == "" {
			return "Downloaded a backup"
		}
		return "Downloaded a backup " + d
	case RemoteDelete:
		return "Deleted the copy " + t + " from the bucket"
	case RemoteDeleteAll:
		return "Deleted all copies from the bucket"
	case Capture:
		return "Captured packets on " + t
	case TailscaleLogin:
		return "Signed the router in to Tailscale"
	case TailscaleLogout:
		return "Signed the router out of Tailscale"
	case CronRun:
		return "Ran the cron job " + t
	case CertificateExport:
		return "Downloaded the certificate " + t + " with its key"
	case LeftoversClear:
		if d == "" {
			return "Cleared leftover rules"
		}
		return "Cleared leftover rules: " + d
	case BlocklistImport:
		return "Imported the blocklist " + t
	}
	return e.Action
}

// whose names the apply a confirm, revert or expiry was about.
func (e *Event) whose() string {
	switch e.Target {
	case "":
		return "an apply"
	case e.By.Name:
		return "their apply"
	}
	return e.Target + "'s apply"
}

func as(role string) string {
	if role == "" {
		return ""
	}
	return " as " + role
}

// window says a confirm window given in seconds the way a person would.
func window(seconds string) string {
	n, err := strconv.Atoi(seconds)
	switch {
	case err != nil || n <= 0:
		return ""
	case n < 60:
		return strconv.Itoa(n) + " seconds"
	case n < 90:
		return "a minute"
	}
	return strconv.Itoa((n+30)/60) + " minutes"
}

// Seconds writes a confirm window as an apply's Detail.
func Seconds(d time.Duration) string {
	n := int(d.Round(time.Second) / time.Second)
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// Log is the audit log of one configuration directory.
type Log struct {
	dir   string
	now   func() time.Time
	limit int

	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// Open returns the log kept in dir. Nothing is read until asked.
func Open(dir string) *Log {
	return &Log{dir: dir, now: time.Now, limit: MaxEntries, subs: map[chan Event]struct{}{}}
}

// line is a line of the file: an entry, or the mark a Clear leaves of
// where the numbering got to.
type line struct {
	Event
	Next uint64 `json:"next,omitempty"`
}

// Add records what someone did, numbered after the newest entry and
// stamped now unless it has a time. A failure is logged, not returned: the
// action has happened either way. A nil log records nothing.
func (l *Log) Add(ev Event) {
	if l == nil {
		return
	}
	ev, err := l.add(ev)
	if err != nil {
		slog.Warn("could not write to the audit log", "action", ev.Action, "err", err)
		return
	}
	ev.Text = ev.Sentence()
	l.mu.Lock()
	defer l.mu.Unlock()
	for ch := range l.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (l *Log) add(ev Event) (Event, error) {
	unlock, err := l.lock()
	if err != nil {
		return ev, err
	}
	defer unlock()
	if ev.Time.IsZero() {
		ev.Time = l.now()
	}
	ev.Time = ev.Time.UTC()
	ev.Text = ""
	events, next, dropped, err := l.read()
	if err != nil {
		return ev, err
	}
	ev.Seq = next
	if dropped || len(events) >= l.limit {
		events = append(events[max(0, len(events)-l.limit+1):], ev)
		return ev, l.write(events, 0)
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return ev, err
	}
	f, err := os.OpenFile(l.path(), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return ev, err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		_ = f.Close()
		return ev, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return ev, err
	}
	return ev, f.Close()
}

// Events reads the log, oldest first, without the entries past Keep.
func (l *Log) Events() ([]Event, error) {
	events, _, _, err := l.read()
	return events, err
}

// Query reads a page of the log, newest first: the entries before the
// one numbered before, or from the newest when it is zero, that q matches,
// limit at most. held and oldest say what the log holds.
func (l *Log) Query(ctx context.Context, q logsearch.Query, before uint64, limit int) (page logsearch.Page[Event], held int, oldest time.Time, err error) {
	events, err := l.Events()
	if err != nil {
		return page, 0, time.Time{}, err
	}
	row := q.Row()
	match := func(e *Event) bool {
		if q.Empty() {
			return true
		}
		row.Reset()
		e.Search(&row)
		return row.Match()
	}
	page, err = logsearch.Walk(ctx, entries(events), before, limit, logsearch.DefaultBudget,
		func(e *Event) uint64 { return e.Seq }, func(e *Event) time.Time { return e.Time }, match)
	for i := range page.Entries {
		page.Entries[i].Text = page.Entries[i].Sentence()
	}
	if len(events) > 0 {
		oldest = events[0].Time
	}
	return page, len(events), oldest, err
}

// entries is the log as a walk reads it.
type entries []Event

func (s entries) Before(seq uint64, buf []Event) (int, bool) {
	end := len(s)
	if seq != 0 {
		end = sort.Search(len(s), func(i int) bool { return s[i].Seq >= seq })
	}
	n := 0
	for i := end - 1; i >= 0 && n < len(buf); i-- {
		buf[n] = s[i]
		n++
	}
	return n, end > n
}

// Clear empties the log. The numbering carries on, so what is recorded
// next, the Clear itself among it, comes after every entry a page showed.
func (l *Log) Clear() error {
	if l == nil {
		return nil
	}
	unlock, err := l.lock()
	if err != nil {
		return err
	}
	defer unlock()
	_, next, _, err := l.read()
	if err != nil {
		return err
	}
	return l.write(nil, next)
}

// Subscribe hands over what this process records from now on, until
// cancel. What another process records shows at the next read.
func (l *Log) Subscribe(buffer int) (<-chan Event, func()) {
	ch := make(chan Event, buffer)
	l.mu.Lock()
	l.subs[ch] = struct{}{}
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if _, ok := l.subs[ch]; ok {
			delete(l.subs, ch)
			close(ch)
		}
	}
}

func (l *Log) path() string { return filepath.Join(l.dir, FileName) }

// read parses the file: the entries within Keep, oldest first, the number
// the next one takes, and whether any were past Keep. A line cut short by
// a crash is skipped.
func (l *Log) read() (events []Event, next uint64, dropped bool, err error) {
	raw, err := os.ReadFile(l.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, 1, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	cutoff := l.now().Add(-Keep)
	next = 1
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var ln line
		if err := json.Unmarshal(sc.Bytes(), &ln); err != nil {
			continue
		}
		if ln.Action == "" {
			next = max(next, ln.Next)
			continue
		}
		next = max(next, ln.Seq+1)
		if ln.Time.Before(cutoff) {
			dropped = true
			continue
		}
		events = append(events, ln.Event)
	}
	if err := sc.Err(); err != nil {
		return nil, 0, false, fmt.Errorf("read %s: %w", l.path(), err)
	}
	return events, next, dropped, nil
}

// write replaces the file with events, led by a mark of the numbering when
// mark is set.
func (l *Log) write(events []Event, mark uint64) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	if mark > 0 {
		if err := enc.Encode(line{Next: mark}); err != nil {
			return err
		}
	}
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return atomicfile.Write(l.path(), b.Bytes(), 0o600)
}

// lock takes the log's lock, which the daemon and the command line share.
func (l *Log) lock() (func(), error) {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(l.dir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock the audit log: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
