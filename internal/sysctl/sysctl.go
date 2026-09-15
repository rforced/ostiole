// Package sysctl turns on the kernel settings a router needs and persists
// them for boot.
package sysctl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Forwarding settings a router needs. Keys are /proc/sys paths relative
// to /proc/sys; values are what Ostiole sets.
var Forwarding = map[string]string{
	"net/ipv4/ip_forward":          "1",
	"net/ipv6/conf/all/forwarding": "1",
	// Reverse path filtering in loose mode: strict mode drops legitimate
	// asymmetric traffic on multi-homed routers.
	"net/ipv4/conf/all/rp_filter":     "2",
	"net/ipv4/conf/default/rp_filter": "2",
	// Do not accept ICMP redirects or source-routed packets on a router.
	"net/ipv4/conf/all/accept_redirects":     "0",
	"net/ipv4/conf/default/accept_redirects": "0",
	"net/ipv6/conf/all/accept_redirects":     "0",
	"net/ipv4/conf/all/send_redirects":       "0",
	"net/ipv4/conf/all/accept_source_route":  "0",
	"net/ipv6/conf/all/accept_source_route":  "0",
	"net/ipv4/tcp_syncookies":                "1",
}

// ConfFile is where the settings are persisted for systemd-sysctl.
const ConfFile = "/etc/sysctl.d/90-ostiole.conf"

// Applier sets kernel parameters; the engine uses it after every apply.
type Applier interface {
	Apply() error
}

// Proc writes settings straight into /proc/sys. Root is the base path,
// overridden in tests.
type Proc struct {
	Root string
}

func (p Proc) root() string {
	if p.Root == "" {
		return "/proc/sys"
	}
	return p.Root
}

// Apply implements Applier. Missing keys (a kernel without IPv6, say) are
// skipped; other errors are reported together.
func (p Proc) Apply() error {
	var errs []error
	for key, value := range Forwarding {
		path := filepath.Join(p.root(), key)
		err := os.WriteFile(path, []byte(value+"\n"), 0o644) //nolint:gosec // sysfs files, mode is ignored
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

// Content renders the persisted sysctl.d file.
func Content() string {
	keys := make([]string, 0, len(Forwarding))
	for k := range Forwarding {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var b strings.Builder
	b.WriteString("# Written by ostiole: settings a router needs. Overwritten on install.\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = %s\n", strings.ReplaceAll(k, "/", "."), Forwarding[k])
	}
	return b.String()
}

// Persist writes the sysctl.d file so the settings hold from boot.
func Persist(path string) error {
	if path == "" {
		path = ConfFile
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // system config dir
		return err
	}
	return os.WriteFile(path, []byte(Content()), 0o644) //nolint:gosec // world-readable like its siblings
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
