package dnslog

import (
	"net/netip"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/model"
)

// reply is what a parsed answer comes to before the lists have their say.
type reply struct {
	name        string
	qtype       uint16
	rcode       dnsmessage.RCode
	answer      netip.Addr
	answers     int
	authorities int
}

// classify turns one DNS reply into an entry, and names the lists that
// carried it. It reports false for anything that is not this router
// answering a single question.
//
// Two things have to agree before an answer is called blocked: the lists
// say the name is refused, and the answer has the shape dnsmasq gives a
// name it made the answer up for. The lists alone would be wrong in the
// seconds between installing a list and the resolver reading it; the shape
// alone cannot tell a blocked name from bogus-priv or domain-needed.
func classify(x *Index, msg []byte) (Entry, []string, bool) {
	r, ok := parseReply(msg)
	if !ok {
		return Entry{}, nil, false
	}
	e := Entry{Name: r.name, Type: r.qtype}

	var f dnsblock.Finding
	if x != nil {
		f = x.Decide(r.name)
	}
	null := x != nil && x.Options().Mode == model.BlockNull

	switch {
	case r.rcode == dnsmessage.RCodeSuccess && r.answers > 0:
		e.Status, e.Answer = StatusOK, r.answer
		// Null mode answers a blocked name with 0.0.0.0 or ::.
		if f.Blocked && null && r.answer.IsValid() && r.answer.IsUnspecified() {
			e.Status, e.Answer = StatusBlocked, netip.Addr{}
		}
	case r.rcode == dnsmessage.RCodeSuccess:
		e.Status = StatusNoData
		if f.Blocked && r.authorities == 0 {
			e.Status = StatusBlocked
		}
	case r.rcode == dnsmessage.RCodeNameError:
		e.Status = StatusNXDomain
		if f.Blocked && r.authorities == 0 {
			e.Status = StatusBlocked
		}
	case r.rcode == dnsmessage.RCodeServerFailure:
		e.Status = StatusServFail
	case r.rcode == dnsmessage.RCodeRefused:
		e.Status = StatusRefused
	default:
		e.Status = StatusOther
	}
	if e.Status != StatusBlocked {
		return e, nil, true
	}
	switch f.Reason {
	case dnsblock.ReasonDeny:
		e.Reason = ReasonDeny
	case dnsblock.ReasonCanary:
		e.Reason = ReasonCanary
	default:
		e.Reason = ReasonList
	}
	return e, f.Lists, true
}

// parseReply reads the question, the outcome and the first address out of
// a reply, and counts the authority section: an answer this router made up
// carries nothing there, one that came from upstream carries a SOA.
func parseReply(msg []byte) (reply, bool) {
	var p dnsmessage.Parser
	hdr, err := p.Start(msg)
	if err != nil || !hdr.Response || hdr.OpCode != 0 {
		return reply{}, false
	}
	qs, err := p.AllQuestions()
	if err != nil || len(qs) != 1 {
		return reply{}, false
	}
	name, ok := dnsblock.Normalize(qs[0].Name.String())
	if !ok {
		return reply{}, false
	}
	r := reply{name: name, qtype: uint16(qs[0].Type), rcode: hdr.RCode}
	for {
		h, err := p.AnswerHeader()
		if err != nil {
			break
		}
		r.answers++
		switch {
		case h.Type == dnsmessage.TypeA && !r.answer.IsValid():
			a, err := p.AResource()
			if err != nil {
				return r, true
			}
			r.answer = netip.AddrFrom4(a.A)
		case h.Type == dnsmessage.TypeAAAA && !r.answer.IsValid():
			a, err := p.AAAAResource()
			if err != nil {
				return r, true
			}
			r.answer = netip.AddrFrom16(a.AAAA)
		default:
			if p.SkipAnswer() != nil {
				return r, true
			}
		}
	}
	for {
		if _, err := p.AuthorityHeader(); err != nil {
			break
		}
		r.authorities++
		if p.SkipAuthority() != nil {
			break
		}
	}
	return r, true
}
