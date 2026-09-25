package sysupdate

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
)

// zypper drives openSUSE and SLE. It is the one manager that thinks in
// patches rather than packages: a security fix is a patch that may pull
// in several packages, so the list on the page is still packages but the
// security count is patches, and `zypper patch --category security` is
// what installs them.
type zypper struct{}

func (zypper) Name() string          { return "zypper" }
func (zypper) SecurityCapable() bool { return true }

// ExcludeSupported is true only for a full update: naming the packages to
// update is how an exclude is honoured, and a patch cannot be told to skip
// one.
func (zypper) ExcludeSupported(security bool) bool { return !security }

type zypperStream struct {
	XMLName xml.Name       `xml:"stream"`
	Updates []zypperUpdate `xml:"update-status>update-list>update"`
}

type zypperUpdate struct {
	Name       string `xml:"name,attr"`
	Edition    string `xml:"edition,attr"`
	EditionOld string `xml:"edition-old,attr"`
	Kind       string `xml:"kind,attr"`
	Category   string `xml:"category,attr"`
	Severity   string `xml:"severity,attr"`
	Source     struct {
		Alias string `xml:"alias,attr"`
	} `xml:"source"`
}

func (z zypper) Check(ctx context.Context, run Runner) (Pending, error) {
	_, _ = run.Run(ctx, z.Name(), "--non-interactive", "refresh")

	updates, err := z.list(ctx, run, "list-updates")
	if err != nil {
		return Pending{}, err
	}
	pkgs := make([]Package, 0, len(updates))
	for _, u := range updates {
		pkgs = append(pkgs, Package{
			Name: u.Name, From: u.EditionOld, To: u.Edition, Repo: u.Source.Alias,
		})
	}

	patches, err := z.list(ctx, run, "list-patches")
	if err != nil {
		return Pending{}, err
	}
	security := 0
	for _, p := range patches {
		if strings.EqualFold(p.Category, "security") {
			security++
		}
	}
	out := Pending{Packages: pkgs, Security: security}
	switch {
	case security > 0:
		out.Note = "SUSE groups fixes into patches, so one security patch can carry several packages"
	case len(patches) == 0 && len(pkgs) > 0:
		// Tumbleweed publishes no patch metadata at all, so a security
		// run would faithfully install nothing.
		out.Note = "this router publishes no patch metadata, so security-only updates would install nothing; choose All"
	}
	sortPackages(out.Packages)
	return out, nil
}

func (z zypper) list(ctx context.Context, run Runner, command string) ([]zypperUpdate, error) {
	out, err := run.Run(ctx, z.Name(), "--xmlout", "--non-interactive", command)
	// zypper reports "there is something to do" with a status of its
	// own; the document is written either way.
	if err != nil && exitCode(err) != 100 && exitCode(err) != 101 {
		return nil, fmt.Errorf("zypper %s: %w: %s", command, err, tail(out))
	}
	var stream zypperStream
	if err := xml.Unmarshal(trimToStream(out), &stream); err != nil {
		return nil, fmt.Errorf("zypper %s: %w", command, err)
	}
	return stream.Updates, nil
}

// trimToStream drops anything zypper printed before the document, which
// is where a repository warning lands.
func trimToStream(out []byte) []byte {
	if i := strings.Index(string(out), "<?xml"); i > 0 {
		return out[i:]
	}
	return out
}

func (z zypper) UpgradeArgv(security bool, exclude []string, pending Pending) []string {
	argv := []string{z.Name(), "--non-interactive"}
	if security {
		return append(argv, "patch", "--category", "security")
	}
	if len(exclude) == 0 {
		return append(argv, "update")
	}
	wanted := keepWanted(pending.Packages, false, exclude)
	if len(wanted) == 0 {
		return nil
	}
	argv = append(argv, "update")
	return append(argv, names(wanted)...)
}

func (z zypper) RebootRequired(ctx context.Context, run Runner) (bool, string) {
	out, err := run.Run(ctx, z.Name(), "needs-rebooting")
	text := strings.ToLower(string(out))
	switch {
	case strings.Contains(text, "reboot is required"):
		return true, "zypper needs-rebooting says core libraries or services were updated"
	case strings.Contains(text, "not necessary"), strings.Contains(text, "not required"):
		return false, ""
	case err == nil && strings.TrimSpace(text) == "":
		// Older zypper says nothing and answers with its status alone.
		return false, ""
	}
	return kernelRebootHint(ctx, run)
}
