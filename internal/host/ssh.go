package host

// How this router's sshd admits people is a fact about the host rather
// than about Ostiole, so it is reported the way the rest of this package
// reports things: read from the router every time, with the file that
// decided it named, and changed with one action whose result is read
// back the same way.
//
// The reading is `sshd -T`, which prints the effective value of every
// keyword after every Include has been followed, because that is the only
// answer that survives what cloud images do to /etc/ssh: Vultr's
// cloud-init drops a 50-cloud-init.conf into sshd_config.d that turns
// password logins on, whatever the main file says. sshd takes the first
// value it reads for a keyword, and the Include sits at the top of the
// main file, so a drop-in that sorts before that one wins over both. That
// is what "00-ostiole.conf" is.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// SSHState is how sshd on this router lets people in.
type SSHState struct {
	// Present is whether sshd is on this router at all.
	Present bool `json:"present"`
	// Unit is the sshd unit that is running, which is what a change is
	// reloaded into; empty when sshd is socket-activated or not running.
	Unit string `json:"unit,omitempty"`
	// Passwords is whether a password is accepted at the prompt.
	Passwords bool `json:"passwords"`
	// KeyboardInteractive is the other way a password gets typed.
	KeyboardInteractive bool `json:"keyboardInteractive"`
	// RootLogin is sshd's own word for it: yes, prohibit-password, no.
	RootLogin string `json:"rootLogin,omitempty"`
	// SetBy is the file whose PasswordAuthentication line is the one in
	// force, so the page can say who turned it on.
	SetBy string `json:"setBy,omitempty"`
	// Managed is whether Ostiole's own drop-in is in place.
	Managed bool `json:"managed"`
	// Note says why the rest could not be read.
	Note string `json:"note,omitempty"`
}

// Account is somebody who can log in to this router.
type Account struct {
	Name string `json:"name"`
	UID  int    `json:"uid"`
	// Sudo is membership of a group that can become root.
	Sudo bool `json:"sudo"`
	// Keys is how many authorized keys the account has, which is what
	// decides whether requiring keys locks it out.
	Keys  int    `json:"keys"`
	Shell string `json:"shell"`
}

// Where sshd keeps things. Every path is joined onto Deps.FS.
const (
	sshdConfig    = "/etc/ssh/sshd_config"
	sshdConfigDir = "/etc/ssh/sshd_config.d"
	// SSHDropIn is Ostiole's drop-in. The 00 is the whole point: sshd
	// takes the first value it reads, and Include globs are read in name
	// order.
	SSHDropIn = "/etc/ssh/sshd_config.d/00-ostiole.conf"
	// sshdInclude is the line a main file needs for the drop-in to be
	// read at all. Every current distribution ships it; one that does not
	// gets it added.
	sshdInclude = "Include /etc/ssh/sshd_config.d/*.conf"
	// CloudInitSSHDropIn stops cloud-init turning passwords back on: its
	// ssh_pwauth setting is what wrote the 50-cloud-init.conf in the first
	// place, and a re-run would write it again.
	CloudInitSSHDropIn = "/etc/cloud/cloud.cfg.d/99-ostiole-ssh.cfg"
)

// sshDropInContent is what requiring keys writes.
const sshDropInContent = `# Written by ostiole. Password logins over SSH are off on this router;
# keys only. Remove this file, or allow passwords again from Ostiole, to
# undo it.
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
MaxAuthTries 3
LoginGraceTime 30
`

const cloudInitSSHContent = "# Written by ostiole: SSH password logins are managed by Ostiole.\nssh_pwauth: false\n"

// fsPath joins a path onto the filesystem root the dependencies name.
func (d Deps) fsPath(p string) string {
	if d.FS == "" {
		return p
	}
	return filepath.Join(d.FS, p)
}

// sshState reads how sshd is set up, and never fails: a page that cannot
// say is told why.
func sshState(ctx context.Context, d Deps) SSHState {
	st := SSHState{}
	bin := d.locate("sshd")
	if bin == "" {
		return st
	}
	st.Present = true
	if _, err := os.Stat(d.fsPath(SSHDropIn)); err == nil {
		st.Managed = true
	}
	st.Unit = sshdUnit(ctx, d)
	out, err := d.run().Run(ctx, bin, "-T")
	if err != nil {
		st.Note = "could not read the effective sshd configuration: " + strings.TrimSpace(string(out))
		return st
	}
	eff := parseSSHDump(string(out))
	st.Passwords = eff["passwordauthentication"] == "yes"
	st.KeyboardInteractive = eff["kbdinteractiveauthentication"] == "yes"
	st.RootLogin = eff["permitrootlogin"]
	st.SetBy = sshSetBy(d.fsPath(sshdConfig), d.FS, "passwordauthentication")
	return st
}

// parseSSHDump reads `sshd -T`: one lower-case keyword and its value per
// line.
func parseSSHDump(out string) map[string]string {
	eff := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok {
			eff[strings.ToLower(key)] = strings.TrimSpace(value)
		}
	}
	return eff
}

// sshSetBy names the file whose line for keyword is the one sshd uses:
// the first one met, following each Include in order, and stopping at a
// Match block because everything after one is conditional. It returns ""
// when no file says anything and the compiled-in default is in force.
func sshSetBy(path, root, keyword string) string {
	return sshSetByDepth(path, root, keyword, 0)
}

func sshSetByDepth(path, root, keyword string, depth int) string {
	if depth > 8 {
		return ""
	}
	raw, err := os.ReadFile(path) //nolint:gosec // paths come from sshd's own Include lines, read as root by design
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, rest := sshKeyword(line)
		switch key {
		case "match":
			return ""
		case "include":
			for _, pattern := range strings.Fields(rest) {
				if !filepath.IsAbs(pattern) {
					pattern = filepath.Join("/etc/ssh", pattern)
				}
				files, _ := filepath.Glob(filepath.Join(root, pattern))
				sort.Strings(files)
				for _, f := range files {
					if found := sshSetByDepth(f, root, keyword, depth+1); found != "" {
						return found
					}
				}
			}
		case keyword:
			return strings.TrimPrefix(path, root)
		}
	}
	return ""
}

// sshKeyword splits a configuration line into its lower-case keyword and
// the rest. sshd accepts a space, a tab or an equals sign between them.
func sshKeyword(line string) (string, string) {
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return strings.ToLower(line), ""
	}
	return strings.ToLower(line[:i]), strings.TrimSpace(strings.TrimLeft(line[i+1:], " \t="))
}

// sshdUnit is the sshd unit that is running: ssh.service on Debian and
// Ubuntu, sshd.service everywhere else. A socket-activated sshd has no
// running service between connections and needs no reload, since each
// connection reads the configuration afresh.
func sshdUnit(ctx context.Context, d Deps) string {
	if d.Units == nil {
		return ""
	}
	for _, unit := range []string{"ssh.service", "sshd.service"} {
		out, err := d.Units.Run(ctx, "is-active", unit)
		if err == nil && strings.TrimSpace(out) == "active" {
			return unit
		}
	}
	return ""
}

// SetSSHPasswords turns password logins over SSH off (keys only) or back
// on, by writing or removing Ostiole's drop-in and the cloud-init pin
// beside it. The new configuration is checked with sshd itself before it
// is reloaded, and put back if sshd rejects it. Nothing checks whether
// anybody has a key: that is the operator's call, and the accounts are
// listed on the page with their key counts for exactly that reason.
func SetSSHPasswords(ctx context.Context, d Deps, allow bool) (string, error) {
	if !d.Root {
		return "", ErrNotRoot
	}
	bin := d.locate("sshd")
	if bin == "" {
		return "", errors.New("sshd is not on this router")
	}
	said, err := writeSSHFiles(d, allow)
	if err != nil {
		if !readOnly(err) || d.FS != "" {
			return "", err
		}
		// The daemon's sandbox cannot write /etc/ssh on a router whose unit
		// predates the paths being opened; the command line can.
		verb := "require-keys"
		if allow {
			verb = "allow-passwords"
		}
		return Drive(ctx, d, "host", "ssh", verb)
	}
	if out, err := d.run().Run(ctx, bin, "-t"); err != nil {
		// Whatever was there before is better than a configuration sshd
		// will not start with.
		_, _ = writeSSHFiles(d, !allow)
		return "", fmt.Errorf("sshd rejected the configuration, so it was put back: %s", strings.TrimSpace(string(out)))
	}
	if unit := sshdUnit(ctx, d); unit != "" {
		if out, err := d.Units.Run(ctx, "reload", unit); err != nil {
			return said, fmt.Errorf("reload %s: %w: %s", unit, err, out)
		}
		said += "; " + unit + " reloaded"
	}
	return said, nil
}

// writeSSHFiles puts the drop-in and the cloud-init pin in place, or
// takes them away, and says what it did.
func writeSSHFiles(d Deps, allow bool) (string, error) {
	dropIn := d.fsPath(SSHDropIn)
	pin := d.fsPath(CloudInitSSHDropIn)
	if allow {
		for _, p := range []string{dropIn, pin} {
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return "", err
			}
		}
		return "password logins over SSH are allowed again; " + SSHDropIn + " removed", nil
	}
	if err := ensureSSHInclude(d.fsPath(sshdConfig)); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dropIn), 0o755); err != nil { //nolint:gosec // sshd's own directory
		return "", err
	}
	if err := writeAtomic(dropIn, sshDropInContent, 0o600); err != nil {
		return "", err
	}
	said := "password logins over SSH are off; keys only (" + SSHDropIn + ")"
	if _, err := os.Stat(filepath.Dir(pin)); err == nil {
		if err := writeAtomic(pin, cloudInitSSHContent, 0o644); err != nil {
			return "", err
		}
		said += "; cloud-init told to leave it alone"
	}
	return said, nil
}

// ensureSSHInclude adds the Include line to a main file that lacks it, at
// the top, which is the only place it means "the drop-ins win".
func ensureSSHInclude(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "Include") &&
			strings.Contains(fields[1], "sshd_config.d") {
			return nil
		}
	}
	return writeAtomic(path, "# Added by ostiole so the drop-ins below are read.\n"+sshdInclude+"\n"+string(raw), 0o600)
}

// readOnly reports whether a write failed because the daemon's sandbox
// forbids it.
func readOnly(err error) bool {
	return errors.Is(err, syscall.EROFS) || errors.Is(err, fs.ErrPermission)
}

// writeAtomic writes a file in one rename, so sshd never reads half of one.
func writeAtomic(path, content string, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// sudoGroups are the groups whose members can become root, by
// distribution family.
var sudoGroups = map[string]bool{"sudo": true, "wheel": true, "admin": true}

// accounts lists who can log in: root and every account with a real
// shell, with whether it can sudo and how many authorized keys it holds.
// A provider's leftover user with sudo and a key is exactly the kind of
// thing an operator should see listed.
func accounts(d Deps) []Account {
	raw, err := os.ReadFile(d.fsPath("/etc/passwd"))
	if err != nil {
		return []Account{}
	}
	sudoers := map[string]bool{}
	if groups, err := os.ReadFile(d.fsPath("/etc/group")); err == nil {
		for _, line := range strings.Split(string(groups), "\n") {
			fields := strings.Split(line, ":")
			if len(fields) < 4 || !sudoGroups[fields[0]] {
				continue
			}
			for _, member := range strings.Split(fields[3], ",") {
				if member = strings.TrimSpace(member); member != "" {
					sudoers[member] = true
				}
			}
		}
	}
	out := []Account{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}
		uid, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		shell := fields[6]
		if uid != 0 && (uid < 1000 || uid >= 65534) {
			continue
		}
		if strings.HasSuffix(shell, "nologin") || strings.HasSuffix(shell, "/false") {
			continue
		}
		acct := Account{Name: fields[0], UID: uid, Shell: shell, Sudo: uid == 0 || sudoers[fields[0]]}
		for _, name := range []string{"authorized_keys", "authorized_keys2"} {
			acct.Keys += countKeys(d.fsPath(filepath.Join(fields[5], ".ssh", name)))
		}
		out = append(out, acct)
	}
	return out
}

// countKeys counts the keys in an authorized_keys file: every line that
// is not blank and not a comment.
func countKeys(path string) int {
	raw, err := os.ReadFile(path) //nolint:gosec // home directories from passwd, read as root by design
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			n++
		}
	}
	return n
}
