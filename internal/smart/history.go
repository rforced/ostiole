package smart

import (
	"strconv"
	"time"

	"github.com/rforced/ostiole/internal/logfile"
	"github.com/rforced/ostiole/internal/logring"
	"github.com/rforced/ostiole/internal/logsearch"
	"github.com/rforced/ostiole/internal/model"
)

// Reading is what one hourly check found on a drive: its history, a line
// an hour. It is about the hardware, not the people using it, so it is
// kept at every log level.
type Reading struct {
	logring.Stamp
	Drive  string `json:"drive"`
	Model  string `json:"model,omitempty"`
	Serial string `json:"serial,omitempty"`
	// Health is passed, failed, or unknown when the drive gave no verdict.
	Health      string `json:"health"`
	Temperature *int   `json:"temperature,omitempty"`
	Wear        *int   `json:"wear,omitempty"`
	Spare       *int   `json:"spare,omitempty"`
	// Bad is what the drive counts of what it lost: the raw values of the
	// attributes that count sectors, or an NVMe drive's media errors.
	Bad          *uint64 `json:"bad,omitempty"`
	PowerOnHours *int    `json:"powerOnHours,omitempty"`
}

// History is the drives' readings in memory: a week of them, with no
// setting, and more in the files while they are on.
type History = logring.Ring[Reading, *Reading]

// historySize bounds the readings memory holds: an hour's for a hundred
// drives for a week, well past what a router has.
const historySize = 24 * 7 * 100

// NewHistory returns an empty history.
func NewHistory() *History {
	return logring.New[Reading, *Reading](historySize, model.DefaultLogDays*24*time.Hour)
}

// HistorySettings is the history's size and days, the same in any
// configuration.
func HistorySettings(*model.Config) (int, time.Duration) {
	return historySize, model.DefaultLogDays * 24 * time.Hour
}

// The history's directory under logfile.Dir, and the format of its lines:
// a reading as the API serves it.
const (
	HistoryFileName    = "drives"
	HistoryFileVersion = 1
)

// HistoryFiles describes the history to the writer that keeps it in files,
// for the files' days, since it has none of its own.
func HistoryFiles(h *History) logfile.Log {
	return h.Files(HistoryFileName, HistoryFileVersion,
		func(*model.Config) bool { return true }, func(*model.Config) int { return 0 })
}

// ParseHistoryLine reads a line of the history's files.
func ParseHistoryLine(line []byte) (Reading, time.Time, error) {
	return logring.Parse[Reading, *Reading](line)
}

// readingOf is what a check of a drive found, from what it said.
func readingOf(d *Drive, at time.Time) Reading {
	r := Reading{
		Stamp: logring.Stamp{Time: at}, Drive: d.Name, Model: d.Model, Serial: d.Serial, Health: d.Health,
		Temperature: d.Temperature, Wear: d.Wear, Spare: d.Spare, PowerOnHours: d.PowerOnHours,
	}
	switch {
	case d.NVMe != nil:
		bad := d.NVMe.MediaErrors
		r.Bad = &bad
	case len(d.Attributes) > 0:
		var bad uint64
		for _, a := range d.Attributes {
			if a.Critical {
				bad += a.Raw
			}
		}
		r.Bad = &bad
	}
	return r
}

// Search hands a the values the History card shows for a reading. buf is
// scratch, handed back to be used again.
func (r *Reading) Search(a logsearch.Adder, buf []byte) []byte {
	a.Add(r.Drive)
	a.Add(r.Model)
	a.Add(r.Serial)
	a.Add(r.Health)
	if r.Temperature != nil {
		buf = append(strconv.AppendInt(buf[:0], int64(*r.Temperature), 10), " °C"...)
		a.AddBytes(buf)
	}
	return buf
}

// HistoryMatcher is a search's test of a reading, the same for the files
// as for memory.
func HistoryMatcher(q logsearch.Query) func(*Reading) bool {
	row := q.Row()
	var buf []byte
	return func(r *Reading) bool {
		if q.Empty() {
			return true
		}
		row.Reset()
		buf = r.Search(&row, buf)
		return row.Match()
	}
}
