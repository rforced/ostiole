package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Release strings from the distributions Ostiole targets, plus the awkward
// shapes a hand-built kernel produces.
func TestParse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		release string
		want    Version
	}{
		{"6.12.0-211.54.1.el10_2.x86_64", Version{6, 12}}, // Rocky 10, the floor
		{"6.12.48+deb13-amd64", Version{6, 12}},           // Debian 13
		{"5.14.0-503.el9.x86_64", Version{5, 14}},         // RHEL 9
		{"6.1.0-18-amd64", Version{6, 1}},                 // Debian 12
		{"6.8.0-45-generic", Version{6, 8}},               // Ubuntu 24.04
		{"5.15.0-101-generic", Version{5, 15}},            // Ubuntu 22.04
		{"7.2.5-cachyos1.fc44.x86_64", Version{7, 2}},
		{"6.12", Version{6, 12}},     // no patch level at all
		{"6.12.0\n", Version{6, 12}}, // straight from /proc, newline and all
		{"5.10-custom", Version{5, 10}},
	} {
		got, err := Parse(tc.release)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.release, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Parse(%q) = %v, want %v", tc.release, got, tc.want)
		}
	}
}

func TestParseRejectsNonsense(t *testing.T) {
	t.Parallel()
	for _, release := range []string{"", "6", "linux", "x.y", "6.x"} {
		if v, err := Parse(release); err == nil {
			t.Errorf("Parse(%q) = %v, want an error", release, v)
		}
	}
}

func TestSupported(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		v    Version
		want bool
	}{
		{Version{6, 12}, true}, // exactly the floor: Debian 13, Enterprise Linux 10
		{Version{6, 17}, true},
		{Version{7, 2}, true},
		{Version{6, 11}, false}, // one minor below
		{Version{6, 8}, false},  // Ubuntu 24.04
		{Version{6, 1}, false},  // Debian 12
		{Version{5, 14}, false}, // Enterprise Linux 9
		{Version{4, 18}, false}, // Enterprise Linux 8
	} {
		if got := tc.v.Supported(); got != tc.want {
			t.Errorf("Version%v.Supported() = %v, want %v", tc.v, got, tc.want)
		}
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, release string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(release), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	err := check(write("old", "5.10.0-28-amd64\n"))
	if err == nil {
		t.Fatal("check accepted a 5.10 kernel")
	}
	// The message has to name both versions or the admin cannot act on it.
	if !strings.Contains(err.Error(), "5.10") || !strings.Contains(err.Error(), Minimum.String()) {
		t.Errorf("unhelpful message: %v", err)
	}

	if err := check(write("floor", "6.12.0-211.54.1.el10_2.x86_64\n")); err != nil {
		t.Errorf("check rejected Enterprise Linux 10's 6.12: %v", err)
	}
	if err := check(write("el9", "5.14.0-503.el9.x86_64\n")); err == nil {
		t.Error("check accepted Enterprise Linux 9's 5.14")
	}

	// A version Ostiole cannot read or parse must not block anything.
	if err := check(filepath.Join(dir, "absent")); err != nil {
		t.Errorf("missing release file = %v, want nil", err)
	}
	if err := check(write("junk", "not-a-version\n")); err != nil {
		t.Errorf("unparseable release = %v, want nil", err)
	}
}
