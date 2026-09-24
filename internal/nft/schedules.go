package nft

import (
	"strings"
)

// ScheduleReload returns a script that reads again, in one transaction,
// every chain of a rendered ruleset that matches on the hour, and false
// when no chain does. nft converts a schedule's hours to UTC as it reads
// them, so a new offset needs them read again. Reading only those chains
// leaves the rest of the table alone: the sets refreshed since the load,
// and the chains the port mapping daemon writes its mappings into.
func ScheduleReload(ruleset string) (string, bool) {
	var flush, body strings.Builder
	var chain []string
	name, hours := "", false
	for line := range strings.Lines(ruleset) {
		line = strings.TrimSuffix(line, "\n")
		switch {
		case name == "":
			// The renderer writes each chain as one block at the table's
			// first level of indentation.
			if rest, ok := strings.CutPrefix(line, "\tchain "); ok {
				if n, ok := strings.CutSuffix(rest, " {"); ok {
					name, chain, hours = n, []string{line}, false
				}
			}
		case line == "\t}":
			if hours {
				flush.WriteString("flush chain " + Table + " " + name + "\n")
				body.WriteString(strings.Join(chain, "\n") + "\n" + line + "\n")
			}
			name = ""
		default:
			chain = append(chain, line)
			hours = hours || strings.Contains(line, "meta hour")
		}
	}
	if body.Len() == 0 {
		return "", false
	}
	return flush.String() + "table " + Table + " {\n" + body.String() + "}\n", true
}
