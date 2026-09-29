package journalfeed

// journald cuts a line longer than its LineMax into several entries of the
// stream it came from, marking each piece but the last. An entry is one
// line, so its pieces are put back together before it is read. The units
// keep their lines well under the limit; this is for a journald told a
// smaller one.

// piece is a line being put back together. A line longer than maxLine is
// dropped whole, rather than held or read from part way through.
type piece struct {
	Record
	over bool
}

func (p *piece) add(message string, before bool) {
	switch {
	case p.over:
	case len(p.Message)+len(message) > maxLine:
		p.over, p.Message = true, ""
	case before:
		p.Message = message + p.Message
	default:
		p.Message += message
	}
}

// lines puts cut lines back together from entries read oldest first.
type lines struct {
	// open holds each stream's line whose last piece is still to come.
	open map[string]*piece
}

// add takes the next entry and hands back the line it ends, if it ends
// one. A line carries its last piece's cursor, so a reader that stops part
// way through one reads every piece of it again, and its time: the line
// was logged once all of it was.
func (l *lines) add(r Record) (Record, bool) {
	p := l.open[r.Stream]
	if p == nil {
		if !r.Cut {
			return r, true
		}
		if l.open == nil {
			l.open = map[string]*piece{}
		}
		l.open[r.Stream] = &piece{Record: r}
		return Record{}, false
	}
	p.add(r.Message, false)
	p.Cursor, p.Time, p.Cut = r.Cursor, r.Time, r.Cut
	if r.Cut {
		return Record{}, false
	}
	delete(l.open, r.Stream)
	return p.Record, true
}

// backLines puts cut lines back together from entries read newest first.
// There a line's last piece comes first, and the line is whole once the
// entry before it in its stream turns out not to be one of its pieces. It
// keeps that last piece's cursor and time, as lines does.
type backLines struct {
	held map[string]*piece
}

// add takes the next older entry and hands back the line it shows to be
// whole, if any.
func (l *backLines) add(r Record) (Record, bool) {
	p := l.held[r.Stream]
	if p != nil && r.Cut {
		p.add(r.Message, true)
		return Record{}, false
	}
	if l.held == nil {
		l.held = map[string]*piece{}
	}
	l.held[r.Stream] = &piece{Record: r}
	if p == nil {
		return Record{}, false
	}
	return p.Record, true
}

// rest hands back the lines still held, in no order, when the reading
// stops.
func (l *backLines) rest() []Record {
	out := make([]Record, 0, len(l.held))
	for _, p := range l.held {
		out = append(out, p.Record)
	}
	l.held = nil
	return out
}
