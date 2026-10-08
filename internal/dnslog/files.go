package dnslog

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"ostiole/internal/logfile"
	"ostiole/internal/model"
)

// The query log's directory under logfile.Dir, and the format of its
// lines. Format 1 had no numbers.
const (
	FileName    = "queries"
	FileVersion = 2
)

// fileRow is an answer as the files keep it: the API's row without the
// device's name, which is put on when it is read, and with its lists by
// name rather than by bits of a table in memory. Its number is kept for
// good.
type fileRow struct {
	Seq    uint64    `json:"seq"`
	Time   time.Time `json:"time"`
	Client string    `json:"client"`
	Name   string    `json:"name"`
	Type   string    `json:"type"`
	Status string    `json:"status"`
	Reason string    `json:"reason,omitempty"`
	Lists  []string  `json:"lists,omitempty"`
	Answer string    `json:"answer,omitempty"`
}

// Files describes the log to the writer that keeps it in files, while it
// is on.
func (l *Log) Files() logfile.Log {
	return logfile.Log{
		Name: FileName, Version: FileVersion,
		On: func(c *model.Config) bool { return c.Services.DNS.QueryLog.Enabled },
		Days: func(c *model.Config) int {
			return int(c.Services.DNS.QueryLog.Retention() / (24 * time.Hour))
		},
		Newest: l.Newest,
		Size:   l.Size,
		Lines: logfile.Lines(l.After, func(e *Entry) uint64 { return e.Seq }, func(e *Entry) time.Time { return e.Time },
			func() func([]byte, *Entry) []byte {
				// The table only grows while the log is on, so one read
				// of it serves until a bit turns up past its end.
				names := l.Lists()
				return func(buf []byte, e *Entry) []byte {
					if e.Lists>>uint(len(names)) != 0 {
						names = l.Lists()
					}
					return appendRow(buf, e, names)
				}
			}),
	}
}

// appendRow appends an answer's line.
func appendRow(buf []byte, e *Entry, names []string) []byte {
	r := fileRow{
		Seq: e.Seq, Time: e.Time, Name: e.Name,
		Type: TypeName(e.Type), Status: e.Status.String(), Reason: e.Reason.String(),
	}
	if e.Client.IsValid() {
		r.Client = e.Client.String()
	}
	if e.Answer.IsValid() {
		r.Answer = e.Answer.String()
	}
	for i, name := range names {
		if e.Lists&(1<<uint(i)) != 0 {
			r.Lists = append(r.Lists, name)
		}
	}
	raw, err := json.Marshal(&r)
	if err != nil {
		return buf
	}
	return append(buf, raw...)
}

// ParseLine reads a line of the query log's files.
func ParseLine(line []byte) (Stored, time.Time, error) {
	var r fileRow
	if err := json.Unmarshal(line, &r); err != nil {
		return Stored{}, time.Time{}, err
	}
	e := Entry{Seq: r.Seq, Time: r.Time, Name: r.Name, Reason: parseReason(r.Reason)}
	var err error
	if r.Client != "" {
		if e.Client, err = netip.ParseAddr(r.Client); err != nil {
			return Stored{}, time.Time{}, err
		}
	}
	if r.Answer != "" {
		if e.Answer, err = netip.ParseAddr(r.Answer); err != nil {
			return Stored{}, time.Time{}, err
		}
	}
	var ok bool
	if e.Type, ok = ParseType(r.Type); !ok || e.Type == 0 {
		return Stored{}, time.Time{}, fmt.Errorf("record type %q", r.Type)
	}
	if e.Status, ok = ParseStatus(r.Status); !ok || e.Status == StatusNone {
		return Stored{}, time.Time{}, fmt.Errorf("status %q", r.Status)
	}
	if r.Time.IsZero() {
		return Stored{}, time.Time{}, errors.New("no time")
	}
	return Stored{Entry: e, ListNames: r.Lists}, r.Time, nil
}

// parseReason reads a reason as Reason.String writes it.
func parseReason(s string) Reason {
	for i, name := range reasonNames {
		if i > 0 && name == s {
			return Reason(i)
		}
	}
	return ReasonNone
}
