package model

import (
	"encoding/json"
	"maps"
)

// AdminChanges names what in the change from old to next only an
// administrator may make: what runs as root, what carries the accounts or
// word of the router off it, and who gets in to manage it. An operator
// changes and applies everything else. old is nil for a router with no
// configuration.
func AdminChanges(old, next *Config) []string {
	if old == nil {
		old = &Config{}
	}
	var out []string
	if !sameJSON(adminCrons(old), adminCrons(next)) {
		out = append(out, "cron jobs that run a command or back up accounts")
	}
	if !sameJSON(old.Updates, next.Updates) {
		out = append(out, "updates")
	}
	if old.Backup.Remote != next.Backup.Remote {
		// A remote backup carries the accounts, password hashes included.
		out = append(out, "remote backup")
	}
	if !sameJSON(old.Notifications, next.Notifications) {
		// What the router says about itself, and to whom, leaves it.
		out = append(out, "notifications")
	}
	om, nm := old.System.Management, next.System.Management
	if om.WebPort != nm.WebPort || om.SSHPort != nm.SSHPort || om.SSHPasswords != nm.SSHPasswords ||
		om.Certificate != nm.Certificate {
		out = append(out, "management access")
	}
	if !maps.Equal(antiLockout(old), antiLockout(next)) {
		out = append(out, "anti-lockout")
	}
	return out
}

// AdminOnly reports whether a cron is the administrator's to change: one
// that runs a command runs it as root, and one that backs up the accounts
// writes their password hashes out.
func (c Cron) AdminOnly() bool {
	return c.Kind == CronCommand || c.WithUsers
}

// adminCrons keys the crons only an administrator may change by ID.
func adminCrons(cfg *Config) map[string]Cron {
	out := map[string]Cron{}
	for _, c := range cfg.Crons {
		if c.AdminOnly() {
			out[c.ID] = c
		}
	}
	return out
}

func antiLockout(cfg *Config) map[string]bool {
	out := map[string]bool{}
	for _, z := range cfg.Zones {
		if z.AntiLockout {
			out[z.Name] = true
		}
	}
	return out
}

// sameJSON compares two values the way the store keeps them, so a list
// that is empty on one side and missing on the other is no change.
func sameJSON(a, b any) bool {
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ra) == string(rb)
}
