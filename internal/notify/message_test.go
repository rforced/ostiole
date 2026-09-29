package notify

import (
	"strings"
	"testing"
	"time"
)

var at = time.Date(2026, 9, 24, 13, 4, 0, 0, time.UTC)

func TestAMessageReadsAsItsNotices(t *testing.T) {
	t.Parallel()
	one := Message{Router: "fw", Events: []Event{{Title: "Gateway wan is down", Detail: "It stopped answering.", Time: at, Level: LevelWarn}}}
	if got := one.Subject(); got != "fw: Gateway wan is down" {
		t.Errorf("subject = %q", got)
	}
	if got := one.Text(); got != "Gateway wan is down\nIt stopped answering.\n2026-09-24 13:04 UTC" {
		t.Errorf("text = %q", got)
	}

	ny, _ := time.LoadLocation("America/New_York")
	two := Message{Router: "fw", Location: ny, Dropped: 3, Events: []Event{
		{Title: "Gateway wan is down", Time: at, Level: LevelOK},
		{Title: "DNS is not running", Time: at, Level: LevelWarn},
	}}
	if got := two.Subject(); got != "fw: 2 notices" {
		t.Errorf("subject = %q", got)
	}
	text := two.Text()
	for _, want := range []string{"Resolved: Gateway wan is down\n2026-09-24 09:04 EDT", "\n\nDNS is not running", "3 before these were dropped"} {
		if !strings.Contains(text, want) {
			t.Errorf("text %q lacks %q", text, want)
		}
	}
	if two.level() != LevelWarn || (Message{Events: []Event{{Level: LevelOK}}}).level() != LevelOK {
		t.Error("level is not the worst of the notices")
	}
}

// A title is one line whatever went into it, and a detail passed on from a
// daemon is cut short on a character boundary.
func TestAMessageKeepsItsShape(t *testing.T) {
	t.Parallel()
	m := Message{Router: "fw", Events: []Event{{Title: "a\r\nBcc: x", Detail: strings.Repeat("é", maxDetail), Time: at}}}
	if got := m.Subject(); got != "fw: a Bcc: x" {
		t.Errorf("subject = %q", got)
	}
	detail := strings.Split(m.Text(), "\n")[1]
	if !strings.HasSuffix(detail, "é…") || len(detail) > maxDetail+len("…") {
		t.Errorf("detail is %d bytes, ends %q", len(detail), detail[len(detail)-8:])
	}
}
