package discovery

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// SSDP is a parsed SSDP datagram.
type SSDP struct {
	Kind     Kind
	ST       string // search target (M-SEARCH, reply)
	NT       string // notification type (NOTIFY)
	USN      string
	Location string
	Server   string
	MX       int // M-SEARCH; 0 when absent or unparsable
	MaxAge   int // seconds from CACHE-CONTROL max-age; 0 when absent
}

// ParseSSDP reads an SSDP datagram: "M-SEARCH * HTTP/1.1" with MAN "ssdp:discover", "NOTIFY *
// HTTP/1.1" with NTS ssdp:alive, ssdp:byebye or ssdp:update, or "HTTP/1.1 200 OK". Header names
// are matched without case, values trimmed; CRLF or LF line ends; a missing final blank line is
// fine. Anything else is an error.
func ParseSSDP(payload []byte) (*SSDP, error) {
	start, rest, _ := bytes.Cut(payload, []byte("\n"))
	start = bytes.TrimSpace(start)
	if len(start) == 0 {
		return nil, errors.New("ssdp: no start line")
	}
	fields := strings.Fields(string(start))
	if len(fields) < 2 {
		return nil, fmt.Errorf("ssdp: bad start line %q", truncate(start))
	}
	s := &SSDP{}
	switch {
	case len(fields) == 3 && fields[0] == "M-SEARCH" && fields[1] == "*" && isHTTP11(fields[2]):
		s.Kind = KindSearch
	case len(fields) == 3 && fields[0] == "NOTIFY" && fields[1] == "*" && isHTTP11(fields[2]):
		s.Kind = KindAlive // settled by NTS below
	case isHTTP11(fields[0]) && fields[1] == "200":
		s.Kind = KindReply
	default:
		return nil, fmt.Errorf("ssdp: bad start line %q", truncate(start))
	}

	var man, nts, cacheControl, mx string
	seen := map[string]bool{}
	for len(rest) > 0 {
		var line []byte
		line, rest, _ = bytes.Cut(rest, []byte("\n"))
		line = bytes.TrimRight(line, "\r")
		if len(bytes.TrimSpace(line)) == 0 {
			break
		}
		name, value, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			continue
		}
		key := strings.ToUpper(string(bytes.TrimSpace(name)))
		if seen[key] {
			continue
		}
		var dst *string
		switch key {
		case "ST":
			dst = &s.ST
		case "NT":
			dst = &s.NT
		case "NTS":
			dst = &nts
		case "USN":
			dst = &s.USN
		case "LOCATION":
			dst = &s.Location
		case "SERVER":
			dst = &s.Server
		case "MAN":
			dst = &man
		case "MX":
			dst = &mx
		case "CACHE-CONTROL":
			dst = &cacheControl
		default:
			continue
		}
		seen[key] = true
		*dst = string(bytes.TrimSpace(value))
	}

	switch s.Kind {
	case KindSearch:
		if !strings.EqualFold(strings.Trim(man, `"`), "ssdp:discover") {
			return nil, fmt.Errorf("ssdp: M-SEARCH with MAN %q", man)
		}
		s.MX = nonNegative(mx)
	case KindAlive:
		switch strings.ToLower(nts) {
		case "ssdp:alive":
			s.Kind = KindAlive
		case "ssdp:byebye":
			s.Kind = KindByebye
		case "ssdp:update":
			s.Kind = KindUpdate
		default:
			return nil, fmt.Errorf("ssdp: NOTIFY with NTS %q", nts)
		}
	}
	s.MaxAge = maxAge(cacheControl)
	return s, nil
}

// Name is USN, else ST or NT, for the log and the Announcements tab.
func (s *SSDP) Name() string {
	switch {
	case s.USN != "":
		return s.USN
	case s.ST != "":
		return s.ST
	}
	return s.NT
}

// Type is NT for a NOTIFY and ST otherwise.
func (s *SSDP) Type() string {
	switch s.Kind {
	case KindAlive, KindByebye, KindUpdate:
		return s.NT
	}
	return s.ST
}

func isHTTP11(v string) bool {
	return strings.EqualFold(v, "HTTP/1.1")
}

func nonNegative(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func maxAge(cacheControl string) int {
	for directive := range strings.SplitSeq(cacheControl, ",") {
		name, value, ok := strings.Cut(directive, "=")
		if ok && strings.EqualFold(strings.TrimSpace(name), "max-age") {
			return nonNegative(strings.Trim(strings.TrimSpace(value), `"`))
		}
	}
	return 0
}

func truncate(b []byte) []byte {
	if len(b) > 64 {
		return b[:64]
	}
	return b
}
