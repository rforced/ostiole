// Package chronytest plays chronyd's command port on the loopback, well
// enough for the Time page: tracking, the sources with their names and
// authentication, and what it served. It checks every request as chronyd
// 4.9 does, so one chronyd would drop goes unanswered here too.
package chronytest

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/chrony"
)

// Source is one server the daemon follows.
type Source struct {
	chrony.Source
	// Auth is what authdata says of it; nil is no authentication.
	Auth *chrony.Auth
	// Unresolved lists it as chronyd lists a name it has not resolved
	// yet: by an ID instead of an address.
	Unresolved bool
}

// Daemon answers on its command port until the test ends.
type Daemon struct {
	// Addr is the command port, for chrony.Client.
	Addr string

	conn     net.PacketConn
	mu       sync.Mutex
	tracking chrony.Tracking
	sources  []Source
	stats    chrony.ServerStats
	refused  map[string]bool
	requests int
	drop     int
}

// New starts a daemon that follows nothing and has never set the clock.
func New(t testing.TB) *Daemon {
	t.Helper()
	var lc net.ListenConfig
	conn, err := lc.ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{Addr: conn.LocalAddr().String(), conn: conn, refused: map[string]bool{},
		tracking: chrony.Tracking{Leap: "Not synchronised"}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.serve()
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-done
	})
	return d
}

// SetTracking sets what tracking says.
func (d *Daemon) SetTracking(t chrony.Tracking) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tracking = t
}

// SetSources sets the sources, in the order they are listed.
func (d *Daemon) SetSources(s ...Source) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sources = append([]Source(nil), s...)
}

// SetServerStats sets what serverstats says.
func (d *Daemon) SetServerStats(s chrony.ServerStats) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stats = s
}

// Refuse answers the commands as not opened to the command port, as a
// chronyd older than 4.7 answers authdata and serverstats.
func (d *Daemon) Refuse(commands ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range commands {
		d.refused[c] = true
	}
}

// Drop leaves the next n requests unanswered, as a lost datagram would.
func (d *Daemon) Drop(n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.drop = n
}

// Requests counts the requests the daemon was sent.
func (d *Daemon) Requests() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.requests
}

func (d *Daemon) serve() {
	buf := make([]byte, 2048)
	for {
		n, from, err := d.conn.ReadFrom(buf)
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			continue
		}
		if rpy := d.answer(buf[:n]); rpy != nil {
			_, _ = d.conn.WriteTo(rpy, from)
		}
	}
}

// command is what the daemon knows of a request: the name opencommands
// gives it, the length chronyd 4.9 requires of it (its fields, padded to
// its reply), and the reply that answers it.
type command struct {
	name   string
	length int
	reply  uint16
}

var commands = map[uint16]command{
	14: {"sources", 32, 2},
	15: {"sources", 76, 3},
	33: {"tracking", 104, 5},
	54: {"serverstats", 196, 25},
	65: {"sourcename", 284, 19},
	67: {"authdata", 52, 20},
}

// Statuses, as candm.h numbers them.
const (
	statusOK            = 0
	statusUnauthorised  = 2
	statusInvalid       = 3
	statusNoSuchSource  = 4
	statusBadPktVersion = 18
	statusBadPktLength  = 19
)

// answer is chronyd's read_from_cmd_socket: it returns the reply, or nil
// for a request chronyd would not answer.
func (d *Daemon) answer(req []byte) []byte {
	if len(req) < 28 || len(req) > 896 || req[1] != 1 || req[2] != 0 || req[3] != 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests++
	if d.drop > 0 {
		d.drop--
		return nil
	}
	rpy := make([]byte, 28, 28+256)
	rpy[0], rpy[1] = 6, 2
	copy(rpy[4:6], req[4:6])
	binary.BigEndian.PutUint16(rpy[6:], 1)
	copy(rpy[16:20], req[8:12])
	status := func(s uint16) []byte {
		binary.BigEndian.PutUint16(rpy[8:], s)
		return rpy
	}
	cmd, known := commands[binary.BigEndian.Uint16(req[4:])]
	switch {
	case req[0] != 6:
		return status(statusBadPktVersion)
	case !known:
		return status(statusInvalid)
	case len(req) < cmd.length:
		return status(statusBadPktLength)
	case d.refused[cmd.name]:
		return status(statusUnauthorised)
	}
	var data []byte
	switch binary.BigEndian.Uint16(req[4:]) {
	case 14:
		data = binary.BigEndian.AppendUint32(nil, uint32(len(d.sources)))
	case 15:
		i := binary.BigEndian.Uint32(req[20:])
		if int(i) >= len(d.sources) {
			return status(statusNoSuchSource)
		}
		data = d.sourceData(int(i))
	case 33:
		data = d.trackingData()
	case 54:
		data = make([]byte, 168)
		binary.BigEndian.PutUint64(data, uint64(d.stats.NTPReceived))
		binary.BigEndian.PutUint64(data[24:], uint64(d.stats.NTPDropped))
	case 65, 67:
		i := d.find(req[20:40])
		if i < 0 {
			return status(statusNoSuchSource)
		}
		data = make([]byte, 256)
		if binary.BigEndian.Uint16(req[4:]) == 65 {
			copy(data[:255], d.sources[i].Name)
		} else {
			data = authData(d.sources[i].Auth)
		}
	}
	binary.BigEndian.PutUint16(rpy[6:], cmd.reply)
	rpy = append(rpy, data...)
	// chronyd sends nothing longer than the request.
	if len(rpy) > len(req) {
		return nil
	}
	return rpy
}

func (d *Daemon) find(addr []byte) int {
	for i := range d.sources {
		if string(d.addrOf(i)) == string(addr) {
			return i
		}
	}
	return -1
}

// addrOf is the IPAddr the daemon lists a source by.
func (d *Daemon) addrOf(i int) []byte {
	s := d.sources[i]
	b := make([]byte, 20)
	if s.Unresolved {
		binary.BigEndian.PutUint32(b, uint32(i+1))
		binary.BigEndian.PutUint16(b[16:], 3)
		return b
	}
	putAddr(b, s.Address)
	return b
}

func putAddr(b []byte, address string) {
	ip, err := netip.ParseAddr(address)
	switch {
	case err != nil:
	case ip.Is4():
		a := ip.As4()
		copy(b, a[:])
		binary.BigEndian.PutUint16(b[16:], 1)
	default:
		a := ip.As16()
		copy(b, a[:])
		binary.BigEndian.PutUint16(b[16:], 2)
	}
}

var states = map[string]uint16{"selected": 0, "unusable": 1, "falseticker": 2, "jittery": 3, "combined": 4, "excluded": 5}

func (d *Daemon) sourceData(i int) []byte {
	s := d.sources[i]
	b := make([]byte, 48)
	copy(b, d.addrOf(i))
	if s.Poll > 0 {
		binary.BigEndian.PutUint16(b[20:], uint16(int16(math.Round(math.Log2(s.Poll.Seconds())))))
	}
	binary.BigEndian.PutUint16(b[22:], uint16(s.Stratum))
	binary.BigEndian.PutUint16(b[24:], states[s.State])
	binary.BigEndian.PutUint16(b[30:], uint16(s.Reach))
	binary.BigEndian.PutUint32(b[32:], seconds(s.LastRx))
	binary.BigEndian.PutUint32(b[36:], encodeFloat(s.Offset))
	binary.BigEndian.PutUint32(b[40:], encodeFloat(s.Offset))
	binary.BigEndian.PutUint32(b[44:], encodeFloat(s.Error))
	return b
}

var leaps = map[string]uint16{"Normal": 0, "Insert second": 1, "Delete second": 2, "Not synchronised": 3}

func (d *Daemon) trackingData() []byte {
	t := d.tracking
	b := make([]byte, 76)
	if ip, err := netip.ParseAddr(t.Address); err == nil {
		putAddr(b[4:], t.Address)
		// chronyd's ID for an IPv4 source is its address; any other ID
		// will do for one on IPv6.
		id := uint32(1)
		if ip.Is4() {
			id = binary.BigEndian.Uint32(b[4:])
		}
		binary.BigEndian.PutUint32(b, id)
	} else {
		var id [4]byte
		copy(id[:], t.Address)
		binary.BigEndian.PutUint32(b, binary.BigEndian.Uint32(id[:]))
	}
	if t.Local {
		// chronyd's own clock, 127.127.1.1, with no address.
		binary.BigEndian.PutUint32(b, 0x7f7f0101)
	}
	binary.BigEndian.PutUint16(b[24:], uint16(t.Stratum))
	binary.BigEndian.PutUint16(b[26:], leaps[t.Leap])
	if !t.RefTime.IsZero() {
		sec := t.RefTime.Unix()
		binary.BigEndian.PutUint32(b[28:], uint32(sec>>32))
		binary.BigEndian.PutUint32(b[32:], uint32(sec))
		binary.BigEndian.PutUint32(b[36:], uint32(t.RefTime.Nanosecond()))
	}
	binary.BigEndian.PutUint32(b[40:], encodeFloat(-t.Offset))
	binary.BigEndian.PutUint32(b[64:], encodeFloat(t.RootDelay))
	binary.BigEndian.PutUint32(b[68:], encodeFloat(t.RootDispersion))
	return b
}

var authModes = map[string]uint16{"": 0, "SK": 1, "NTS": 2}

func authData(a *chrony.Auth) []byte {
	b := make([]byte, 24)
	if a == nil {
		a = &chrony.Auth{LastKE: -1}
	}
	binary.BigEndian.PutUint16(b, authModes[a.Mode])
	binary.BigEndian.PutUint16(b[10:], uint16(a.Attempts))
	binary.BigEndian.PutUint32(b[12:], seconds(a.LastKE))
	binary.BigEndian.PutUint16(b[16:], uint16(a.Cookies))
	if a.NAK {
		binary.BigEndian.PutUint16(b[20:], 1)
	}
	return b
}

// seconds is a duration as chronyd counts one; negative is never.
func seconds(d time.Duration) uint32 {
	if d < 0 {
		return math.MaxUint32
	}
	return uint32(d / time.Second)
}

// encodeFloat is chrony's UTI_FloatHostToNetwork, as a host-order word: a
// 7-bit signed exponent and a 25-bit signed coefficient.
func encodeFloat(x float64) uint32 {
	const expBits, coefBits = 7, 25
	const expMin, expMax = -(1 << (expBits - 1)), 1<<(expBits-1) - 1
	const coefMax = 1<<(coefBits-1) - 1
	var neg int32
	switch {
	case x < 0:
		x, neg = -x, 1
	case !(x >= 0):
		x = 0
	}
	var exp, coef int32
	switch {
	case x < 1e-100:
	case x > 1e100:
		exp, coef = expMax, coefMax+neg
	default:
		exp = int32(math.Log(x)/math.Log(2) + 1)
		coef = int32(x*math.Pow(2, float64(-exp+coefBits)) + 0.5)
		for coef > coefMax+neg {
			coef >>= 1
			exp++
		}
		switch {
		case exp > expMax:
			exp, coef = expMax, coefMax+neg
		case exp < expMin && exp+coefBits >= expMin:
			coef >>= expMin - exp
			exp = expMin
		case exp < expMin:
			exp, coef = 0, 0
		}
	}
	if neg != 0 {
		coef = int32(uint32(-coef) << expBits >> expBits)
	}
	return uint32(exp)<<coefBits | uint32(coef)
}
