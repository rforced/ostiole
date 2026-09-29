package main

import (
	"bytes"
	"testing"
)

// Under journald each of Caddy's lines starts with its level's priority;
// a line with no level, and every line anywhere else, is written as it is.
func TestLinesCarryTheirLevelsPriority(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ line, want string }{
		{`{"level":"debug","ts":1,"msg":"m"}`, `<7>{"level":"debug","ts":1,"msg":"m"}`},
		{`{"level":"info","msg":"m"}`, `<6>{"level":"info","msg":"m"}`},
		{`{"level":"warn","msg":"m"}`, `<4>{"level":"warn","msg":"m"}`},
		{`{"level":"error","msg":"m"}`, `<3>{"level":"error","msg":"m"}`},
		{`{"level":"fatal","msg":"m"}`, `<2>{"level":"fatal","msg":"m"}`},
		{`{"msg":"no level"}`, `{"msg":"no level"}`},
		{`ostiole-waf {"level":"error"}`, `ostiole-waf {"level":"error"}`},
	} {
		var out bytes.Buffer
		n, err := prioritized{out: &out, journal: true}.Write([]byte(c.line))
		if err != nil || n != len(c.line) || out.String() != c.want {
			t.Errorf("%s wrote %q (%d, %v), want %q", c.line, out.String(), n, err, c.want)
		}
		out.Reset()
		if _, err := (prioritized{out: &out}).Write([]byte(c.line)); err != nil || out.String() != c.line {
			t.Errorf("off the journal %s wrote %q", c.line, out.String())
		}
	}
}
