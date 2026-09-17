// Package dnsblock is DNS blocking: it reads the published lists of names
// an appliance is asked to refuse, and renders them as dnsmasq
// configuration.
//
// The lists are large — a quarter of a million names is ordinary — so
// nothing here holds one longer than it must. A fetched list is written to
// its own cache file, sorted, and the merged result is streamed into the
// include file dnsmasq reads. ADR-0005 has the measurements behind that
// choice.
package dnsblock

import (
	"strings"

	"github.com/rforced/ostiole/internal/model"
)

// Limits on what a list may be. These are larger than the address-list
// limits in internal/feeds because domain lists are: an ordinary one runs to
// a few hundred thousand names, and HaGeZi's threat-intelligence feed is
// two and a half million on its own.
const (
	// MaxBytes is the most one list may be over the wire. The largest
	// published list measured 50 MB in September 2026.
	MaxBytes = 128 << 20
	// MaxListDomains is the most one list may contribute.
	MaxListDomains = MaxDomains
	// MaxDomains is the hard ceiling on the merged list, whatever the
	// configuration asks for. At roughly 90 MB of dnsmasq per million
	// names (ADR-0005) this is already more memory than most appliances
	// have; it exists to turn an out-of-memory crash into a clear refusal.
	MaxDomains = 5_000_000
	// DefaultMaxDomains is the ceiling when the configuration names none.
	// A million names costs dnsmasq about 90 MB, which a small router can
	// afford; more than that should be asked for on purpose.
	DefaultMaxDomains = 1_000_000
	// BytesPerName is roughly what a blocked name costs in dnsmasq's
	// resident memory, measured at 250k and 1M names. It is used to tell
	// the operator what a list will cost before they turn it on.
	BytesPerName = 90
)

// notNames are the names hosts-format lists carry for the loopback and for
// IPv6 housekeeping. They are in the file because it is a hosts file, not
// because anyone wants them blocked, and blocking them breaks the router.
var notNames = map[string]bool{
	"localhost":             true,
	"localhost.localdomain": true,
	"local":                 true,
	"broadcasthost":         true,
	"ip6-localhost":         true,
	"ip6-loopback":          true,
	"ip6-localnet":          true,
	"ip6-mcastprefix":       true,
	"ip6-allnodes":          true,
	"ip6-allrouters":        true,
	"ip6-allhosts":          true,
}

// Normalize turns one entry into the name to block, or reports that it is
// not a name at all. It lowercases and drops a trailing dot.
//
// An internationalised name has to arrive already in its xn-- form, which
// is how DNS carries it and how every published list writes it. Converting
// one here would mean depending on golang.org/x/text for its tables, which
// is a lot of binary for a case that does not arise in practice.
func Normalize(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	// "*.example.com" is how OISD and HaGeZi write "this domain and
	// everything under it", which is what blocking does here anyway.
	s = strings.TrimPrefix(s, "*.")
	s = strings.Trim(s, ".")
	if s == "" {
		return "", false
	}
	s = strings.ToLower(s)
	// A bare address is not a name. Hosts files carry a few, and a list
	// that has gone wrong carries many.
	if strings.IndexFunc(s, func(r rune) bool { return r != '.' && r != ':' && (r < '0' || r > '9') }) < 0 {
		return "", false
	}
	if notNames[s] {
		return "", false
	}
	if len(s) > 253 || !validName(s) {
		return "", false
	}
	return s, true
}

// validName accepts what a list may sensibly ask to have blocked: labels of
// letters, digits, hyphens and the underscores that service names like
// _dmarc use. It is deliberately looser than a hostname check and stricter
// than "anything without a space". Anything outside ASCII fails here, which
// is what keeps an unconverted internationalised name out of the file.
func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := range len(label) {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			default:
				return false
			}
		}
	}
	return true
}

// ParseLine reads one line of a list in the given format and returns the
// name it asks to have blocked. A line it cannot read is not an error: a
// list with a few odd lines is still worth having.
//
// A published list always names fully qualified hosts, so a single label is
// rejected here even though Normalize would accept it. That is what stops
// an HTML error page being read as a hosts file and "Not" of "404 Not
// Found" becoming a top-level domain this router refuses to resolve. Blocking
// a whole top-level domain is still possible, by writing it in the deny
// list, where it is unambiguously meant.
func ParseLine(line string, format model.ListFormat) (string, bool) {
	name, ok := parseLine(line, format)
	if !ok || !strings.Contains(name, ".") {
		return "", false
	}
	return name, true
}

func parseLine(line string, format model.ListFormat) (string, bool) {
	line = strip(line)
	if line == "" {
		return "", false
	}
	switch format {
	case model.FormatHosts:
		return hostsLine(line)
	case model.FormatAdblock:
		return adblockLine(line)
	case model.FormatDnsmasq:
		return dnsmasqLine(line)
	case model.FormatUnbound:
		return unboundLine(line)
	default: // FormatDomains
		fields := strings.Fields(line)
		if len(fields) != 1 {
			return "", false
		}
		return Normalize(fields[0])
	}
}

// strip removes a comment and the whitespace around what is left. Lists use
// # and ! and //; a semicolon is left alone because it never introduces a
// comment in a domain list and does appear in adblock options.
func strip(line string) string {
	for _, marker := range []string{"#", "!", "//"} {
		if i := strings.Index(line, marker); i >= 0 {
			line = line[:i]
		}
	}
	return strings.TrimSpace(line)
}

// hostsLine reads "0.0.0.0 example.com" and the several names per line that
// some hosts files use. The address is skipped: what it points at is the
// list's business, not ours.
func hostsLine(line string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", false
	}
	if len(fields) == 1 {
		// Some "hosts" lists are really domain lists with a header.
		return Normalize(fields[0])
	}
	return Normalize(fields[1])
}

// adblockLine reads the "||example.com^" rules that name a whole domain.
// Everything else in an adblock list — paths, wildcards, element hiding,
// per-site options — means nothing to a resolver and is skipped. So are
// the "@@" exception rules: an allowlist is the operator's to write.
func adblockLine(line string) (string, bool) {
	if !strings.HasPrefix(line, "||") {
		return "", false
	}
	rest := line[2:]
	// Options after a $ are for a browser, and a rule that carries them
	// usually does not mean "block this domain outright".
	if strings.ContainsAny(rest, "$*/") {
		return "", false
	}
	rest = strings.TrimSuffix(rest, "^")
	if strings.Contains(rest, "^") {
		return "", false
	}
	return Normalize(rest)
}

// dnsmasqLine reads a list already written for dnsmasq: address=/x/0.0.0.0,
// local=/x/ and server=/x/ all name a domain the same way.
func dnsmasqLine(line string) (string, bool) {
	for _, prefix := range []string{"address=/", "local=/", "server=/"} {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := line[len(prefix):]
		// One entry may name several domains; the first is enough, the
		// rest arrive on their own lines in every list that does this.
		if i := strings.Index(rest, "/"); i >= 0 {
			rest = rest[:i]
		}
		return Normalize(rest)
	}
	return "", false
}

// unboundLine reads `local-zone: "example.com." always_nxdomain`.
func unboundLine(line string) (string, bool) {
	const prefix = "local-zone:"
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	rest := strings.TrimSpace(line[len(prefix):])
	rest = strings.TrimPrefix(rest, `"`)
	if i := strings.IndexAny(rest, "\" \t"); i >= 0 {
		rest = rest[:i]
	}
	return Normalize(rest)
}

// DetectFormat works out how a list is written by reading the start of it.
// Lists rarely say what they are, but they are consistent, so the first
// lines that parse as something decide it.
func DetectFormat(sample string) model.ListFormat {
	counts := map[model.ListFormat]int{}
	lines := 0
	for line := range strings.SplitSeq(sample, "\n") {
		if lines >= 200 {
			break
		}
		s := strip(line)
		if s == "" {
			continue
		}
		lines++
		switch {
		case strings.HasPrefix(s, "||"):
			counts[model.FormatAdblock]++
		case strings.HasPrefix(s, "address=/"), strings.HasPrefix(s, "local=/"), strings.HasPrefix(s, "server=/"):
			counts[model.FormatDnsmasq]++
		case strings.HasPrefix(s, "local-zone:"):
			counts[model.FormatUnbound]++
		case len(strings.Fields(s)) >= 2:
			counts[model.FormatHosts]++
		default:
			counts[model.FormatDomains]++
		}
	}
	best, bestN := model.FormatDomains, 0
	// Ordered, so a tie is broken the same way every time.
	for _, f := range []model.ListFormat{
		model.FormatDomains, model.FormatHosts, model.FormatAdblock,
		model.FormatDnsmasq, model.FormatUnbound,
	} {
		if counts[f] > bestN {
			best, bestN = f, counts[f]
		}
	}
	return best
}

// reverseLabels turns example.com into com.example, which sorts a parent
// immediately before everything under it. The whole merge depends on that:
// it is how a list that blocks both example.com and ads.example.com is
// reduced to the one entry that covers both.
func reverseLabels(name string) string {
	if !strings.Contains(name, ".") {
		return name
	}
	parts := strings.Split(name, ".")
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, ".")
}

// covers reports whether parent is the same name as child or sits above it,
// both given in reversed form.
func covers(parent, child string) bool {
	return child == parent || (strings.HasPrefix(child, parent) && len(child) > len(parent) && child[len(parent)] == '.')
}
