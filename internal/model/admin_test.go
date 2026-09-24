package model

import (
	"slices"
	"testing"
)

func TestAdminChanges(t *testing.T) {
	t.Parallel()
	base := func() *Config {
		cfg := Starter(StarterOptions{Hostname: "fw", LAN: "eth1", LANAddress: "10.0.0.1/24", WAN: "eth0"})
		cfg.Crons = []Cron{
			{ID: "nightly", Enabled: true, Schedule: "0 3 * * *", Kind: CronBackup},
			{ID: "kick", Enabled: true, Schedule: "0 4 * * *", Kind: CronRestartService, Service: "dnsmasq"},
			{ID: "hook", Enabled: true, Schedule: "0 5 * * *", Kind: CronCommand, Command: "/usr/local/bin/hook", Args: []string{}},
		}
		return cfg
	}
	for _, tc := range []struct {
		name   string
		change func(*Config)
		want   []string
	}{
		{"nothing", func(*Config) {}, nil},
		// What an operator is for.
		{"rules and a restart cron", func(c *Config) {
			c.Rules = nil
			c.Crons[1].Schedule = "0 6 * * *"
			c.System.Management.LogDefaultDrops = true
			c.Zones = append(c.Zones, Zone{Name: "dmz"})
		}, nil},
		{"a backup without accounts", func(c *Config) { c.Crons[0].Keep = 3 }, nil},
		{"args written out empty", func(c *Config) { c.Crons[2].Args = nil }, nil},
		// What runs as root, or carries the accounts away.
		{"a new command", func(c *Config) {
			c.Crons = append(c.Crons, Cron{ID: "shell", Kind: CronCommand, Command: "/bin/sh", Args: []string{"-c", "id"}})
		}, []string{"crons that run a command or back up accounts"}},
		{"an existing command", func(c *Config) { c.Crons[2].Args = []string{"--now"} }, []string{"crons that run a command or back up accounts"}},
		{"a restart cron turned into a command", func(c *Config) {
			c.Crons[1].Kind, c.Crons[1].Command = CronCommand, "/bin/sh"
		}, []string{"crons that run a command or back up accounts"}},
		{"a backup that takes the accounts", func(c *Config) { c.Crons[0].WithUsers = true }, []string{"crons that run a command or back up accounts"}},
		{"updates", func(c *Config) { c.Updates.Ostiole.Mode = "all" }, []string{"updates"}},
		{"the remote backup", func(c *Config) { c.Backup.Remote.Bucket = "elsewhere" }, []string{"remote backup"}},
		// Who gets in.
		{"ssh passwords", func(c *Config) { c.System.Management.SSHPasswords = !c.System.Management.SSHPasswords }, []string{"management access"}},
		{"the web port", func(c *Config) { c.System.Management.WebPort = 8443 }, []string{"management access"}},
		{"anti-lockout on the wan", func(c *Config) { c.Zones[0].AntiLockout = true }, []string{"anti-lockout"}},
		{"anti-lockout off the lan", func(c *Config) { c.Zones[1].AntiLockout = false }, []string{"anti-lockout"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			next := base()
			tc.change(next)
			if got := AdminChanges(base(), next); !slices.Equal(got, tc.want) {
				t.Errorf("AdminChanges = %q, want %q", got, tc.want)
			}
		})
	}
	// A first configuration decides who gets in.
	if got := AdminChanges(nil, base()); !slices.Contains(got, "management access") || !slices.Contains(got, "anti-lockout") {
		t.Errorf("first configuration: %q", got)
	}
}
