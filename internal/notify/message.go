package notify

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// maxDetail is how much of one notice's detail a message carries. An error
// passed on from a daemon can run to pages.
const maxDetail = 1000

// Headline is how a notice reads on its own line.
func (e Event) Headline() string {
	if e.Level == LevelOK {
		return "Resolved: " + e.Title
	}
	return e.Title
}

// Subject is the one line a message is known by: the router, and the
// notice or how many there are.
func (m Message) Subject() string {
	if len(m.Events) == 1 {
		return oneLine(m.Router + ": " + m.Events[0].Headline())
	}
	return fmt.Sprintf("%s: %d notices", oneLine(m.Router), len(m.Events))
}

// Text is the message as plain text: each notice's headline, what it said
// and when, and how many before them were dropped.
func (m Message) Text() string {
	loc := m.Location
	if loc == nil {
		loc = time.UTC
	}
	var b strings.Builder
	for i, e := range m.Events {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(oneLine(e.Headline()))
		if e.Detail != "" {
			b.WriteString("\n" + clip(e.Detail, maxDetail))
		}
		b.WriteString("\n" + e.Time.In(loc).Format("2006-01-02 15:04 MST"))
	}
	switch {
	case m.Dropped == 1:
		b.WriteString("\n\nToo many notices came at once: one before these was dropped.")
	case m.Dropped > 1:
		fmt.Fprintf(&b, "\n\nToo many notices came at once: %d before these were dropped.", m.Dropped)
	}
	return b.String()
}

// level is the worst level among the notices.
func (m Message) level() string {
	worst := LevelOK
	for _, e := range m.Events {
		switch e.Level {
		case LevelWarn:
			return LevelWarn
		case LevelInfo:
			worst = LevelInfo
		}
	}
	return worst
}

// oneLine keeps a title to one line, whatever went into it.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clip cuts s to at most n bytes on a character boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
