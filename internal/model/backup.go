package model

import "strings"

// Backup is the router's own configuration, kept somewhere else.
type Backup struct {
	// Remote is a copy in an S3 bucket.
	Remote RemoteBackup `json:"remote,omitzero"`
}

// RemoteBackup is an S3-compatible bucket the router copies its
// configuration to, encrypted before it leaves.
type RemoteBackup struct {
	Enabled bool `json:"enabled"`
	// Endpoint is the service URL, https only, no path:
	// https://s3.us-west-004.backblazeb2.com
	Endpoint string `json:"endpoint,omitempty"`
	// Region signs the requests. Empty reads it from the endpoint's host
	// name and otherwise uses us-east-1, which every S3-compatible
	// service accepts.
	Region string `json:"region,omitempty"`
	Bucket string `json:"bucket,omitempty"`
	// Prefix is the folder in the bucket the copies go under; empty is
	// DefaultBackupPrefix. Retention is scoped to it, so it is never
	// nothing.
	Prefix string `json:"prefix,omitempty"`
	// KeyID and Secret are the application key.
	KeyID  string `json:"keyId,omitempty"`
	Secret string `json:"secret,omitempty"`
	// Passphrase locks every copy. Required.
	Passphrase string `json:"passphrase,omitempty"`
	// Schedule is when a copy is taken; empty is
	// DefaultRemoteBackupSchedule.
	Schedule string `json:"schedule,omitempty"`
	// Keep is how many of this router's copies stay in the bucket; the
	// rest are deleted after each upload. Zero deletes nothing.
	Keep int `json:"keep,omitempty"`
	// Days is how long the bucket keeps a copy, as a retention rule on
	// the prefix that each run writes. Zero writes no expiry.
	Days int `json:"days,omitempty"`
}

const (
	// DefaultBackupPrefix is the folder the copies go under.
	DefaultBackupPrefix = "ostiole/"
	// DefaultRemoteBackupSchedule is three in the morning, an hour before
	// the update check, so the copy is of the configuration the night
	// before anything moves.
	DefaultRemoteBackupSchedule = "0 3 * * *"
	// CronIDRemoteBackup is the derived cron that takes the copy.
	CronIDRemoteBackup = "system:remote-backup"
	// DefaultRemoteRegion is what a service with no regions is told it
	// is; every S3-compatible implementation accepts it.
	DefaultRemoteRegion = "us-east-1"
)

// ScheduleOr is when a copy is taken.
func (r RemoteBackup) ScheduleOr() string {
	if r.Schedule == "" {
		return DefaultRemoteBackupSchedule
	}
	return r.Schedule
}

// PrefixOr is the folder the copies go under, always ending in a slash
// so that retention scoped to it cannot reach the rest of the bucket.
func (r RemoteBackup) PrefixOr() string {
	p := strings.TrimSpace(r.Prefix)
	if p == "" {
		return DefaultBackupPrefix
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// RegionOr is the region the requests are signed for. An endpoint like
// s3.us-west-004.backblazeb2.com carries its region in the host name, so
// an operator who copied the endpoint out of a console does not have to
// find it twice.
func (r RemoteBackup) RegionOr() string {
	if r.Region != "" {
		return r.Region
	}
	host := r.Endpoint
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	host, _, _ = strings.Cut(host, "/")
	host, _, _ = strings.Cut(host, ":")
	labels := strings.Split(host, ".")
	if len(labels) >= 4 && labels[0] == "s3" && labels[1] != "" {
		return labels[1]
	}
	return DefaultRemoteRegion
}
