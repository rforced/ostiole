package model

import (
	"errors"
	"testing"
)

func TestRemoteBackupDefaults(t *testing.T) {
	t.Parallel()
	var r RemoteBackup
	if got := r.ScheduleOr(); got != DefaultRemoteBackupSchedule {
		t.Errorf("ScheduleOr = %q, want %q", got, DefaultRemoteBackupSchedule)
	}
	r.Schedule = "@daily"
	if got := r.ScheduleOr(); got != "@daily" {
		t.Errorf("ScheduleOr = %q, want @daily", got)
	}
	for _, tc := range []struct{ prefix, want string }{
		{"", DefaultBackupPrefix},
		{"routers", "routers/"},
		{"routers/", "routers/"},
		{" routers/one ", "routers/one/"},
	} {
		if got := (RemoteBackup{Prefix: tc.prefix}).PrefixOr(); got != tc.want {
			t.Errorf("PrefixOr(%q) = %q, want %q", tc.prefix, got, tc.want)
		}
	}
}

func TestRemoteBackupRegionComesFromTheEndpoint(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ endpoint, region, want string }{
		{"https://s3.us-west-004.backblazeb2.com", "", "us-west-004"},
		{"https://s3.eu-central-1.amazonaws.com", "", "eu-central-1"},
		{"https://s3.us-east-005.backblazeb2.com/", "", "us-east-005"},
		{"https://minio.example.net", "", DefaultRemoteRegion},
		{"https://s3.example.net", "", DefaultRemoteRegion},
		{"https://s3.us-west-004.backblazeb2.com", "somewhere", "somewhere"},
		{"", "", DefaultRemoteRegion},
	} {
		got := (RemoteBackup{Endpoint: tc.endpoint, Region: tc.region}).RegionOr()
		if got != tc.want {
			t.Errorf("RegionOr(%q, %q) = %q, want %q", tc.endpoint, tc.region, got, tc.want)
		}
	}
}

// remoteConfig is a starter with the bucket filled in and switched on.
func remoteConfig(t *testing.T) *Config {
	t.Helper()
	cfg := Starter(StarterOptions{Hostname: "router", LAN: "eth0", LANAddress: "192.168.1.1/24"})
	cfg.Backup.Remote = RemoteBackup{
		Enabled:    true,
		Endpoint:   "https://s3.us-west-004.backblazeb2.com",
		Bucket:     "router-backups",
		KeyID:      "0055abc",
		Secret:     "not-a-real-key",
		Passphrase: "correct horse",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the base configuration is invalid: %v", err)
	}
	return cfg
}

func TestValidateRemoteBackup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(r *RemoteBackup)
		path string
	}{
		{"plain http", func(r *RemoteBackup) { r.Endpoint = "http://s3.example.net" }, "backup.remote.endpoint"},
		{"a path on the endpoint", func(r *RemoteBackup) { r.Endpoint += "/router-backups" }, "backup.remote.endpoint"},
		{"no endpoint", func(r *RemoteBackup) { r.Endpoint = "" }, "backup.remote.endpoint"},
		{"a URL in the bucket field", func(r *RemoteBackup) { r.Bucket = "https://b2/bucket" }, "backup.remote.bucket"},
		{"no bucket", func(r *RemoteBackup) { r.Bucket = "" }, "backup.remote.bucket"},
		{"no key", func(r *RemoteBackup) { r.KeyID = "" }, "backup.remote.keyId"},
		{"no secret", func(r *RemoteBackup) { r.Secret = "" }, "backup.remote.secret"},
		{"no passphrase", func(r *RemoteBackup) { r.Passphrase = "" }, "backup.remote.passphrase"},
		{"a rooted prefix", func(r *RemoteBackup) { r.Prefix = "/ostiole/" }, "backup.remote.prefix"},
		{"a prefix going up", func(r *RemoteBackup) { r.Prefix = "ostiole/../etc/" }, "backup.remote.prefix"},
		{"an empty folder", func(r *RemoteBackup) { r.Prefix = "ostiole//copies/" }, "backup.remote.prefix"},
		{"a schedule of four fields", func(r *RemoteBackup) { r.Schedule = "0 3 * *" }, "backup.remote.schedule"},
		{"too many copies", func(r *RemoteBackup) { r.Keep = 1001 }, "backup.remote.keep"},
		{"too many days", func(r *RemoteBackup) { r.Days = 4000 }, "backup.remote.days"},
	} {
		cfg := remoteConfig(t)
		tc.edit(&cfg.Backup.Remote)
		err := cfg.Validate()
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: error %v, want a validation error on %s", tc.name, err, tc.path)
			continue
		}
		if !hasPath(ve, tc.path) {
			t.Errorf("%s: issues %v, want one on %s", tc.name, ve.Issues, tc.path)
		}
	}
}

// The form is filled in over several applies, so a block that is not
// switched on is never in anyone's way.
func TestValidateAcceptsAnUnfinishedRemoteBackup(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{Hostname: "router", LAN: "eth0", LANAddress: "192.168.1.1/24"})
	cfg.Backup.Remote = RemoteBackup{Endpoint: "https://s3.us-west-004.backblazeb2.com"}
	if err := cfg.Validate(); err != nil {
		t.Errorf("a disabled block with only an endpoint was refused: %v", err)
	}
	// The prefix and the numbers are checked whether it is on or off, so
	// turning it on later cannot fail on something typed long ago.
	cfg.Backup.Remote.Prefix = "/nope"
	if err := cfg.Validate(); err == nil {
		t.Error("a rooted prefix was accepted while the copies were off")
	}
}

func TestRedactedBlanksTheBucketKey(t *testing.T) {
	t.Parallel()
	cfg := &Config{Backup: Backup{Remote: RemoteBackup{
		Enabled: true, Bucket: "router-backups",
		KeyID: "0055abc", Secret: "not-a-real-key", Passphrase: "correct horse",
	}}}
	r := cfg.Redacted().Backup.Remote
	if r.KeyID != "" || r.Secret != "" || r.Passphrase != "" {
		t.Errorf("redacted remote backup = %+v, want the key, secret and passphrase blank", r)
	}
	if r.Bucket != "router-backups" || !r.Enabled {
		t.Errorf("redacted remote backup = %+v, want the rest of the settings kept", r)
	}
}
