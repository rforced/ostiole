package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/s3"
)

// maxRemoteBytes bounds what a restore reads back out of the bucket. It
// is the same bound an upload through the browser has.
const maxRemoteBytes = 8 << 20

// noncurrentDays is how long a hidden copy stays. On a bucket that keeps
// versions — which every Backblaze B2 bucket does — a delete only hides
// the newest version, and this is what frees the space afterwards. On a
// bucket without versions it does nothing.
const noncurrentDays = 1

// Errors a remote backup answers with.
var (
	ErrNotACopy     = errors.New("that is not one of the router's backups")
	ErrNotEncrypted = errors.New("a copy in a bucket is always encrypted; this one has no passphrase")
)

// copyName is the shape Filename writes, read back: the hostname, then
// the time it was taken. Anything else under the prefix belongs to
// somebody else and is neither listed, restored nor deleted.
var copyName = regexp.MustCompile(`^(.+)-(\d{8}-\d{6})\.json` + regexp.QuoteMeta(Suffix) + `$`)

// Remote is the bucket a router copies its backups to.
type Remote struct {
	S3 *s3.Client
	// Prefix is the folder in the bucket, ending in a slash.
	Prefix string
	// Hostname is the router's name as Filename writes it; the prune
	// matches on it, so one bucket can hold several routers' copies.
	Hostname string
	Keep     int
	Days     int
	Log      *slog.Logger
}

// NewRemote builds the bucket from the settings.
func NewRemote(r model.RemoteBackup, hostname, userAgent string) (*Remote, error) {
	client, err := s3.New(r.Endpoint, r.RegionOr(), r.Bucket, r.KeyID, r.Secret)
	if err != nil {
		return nil, err
	}
	client.UserAgent = userAgent
	return &Remote{
		S3:       client,
		Prefix:   r.PrefixOr(),
		Hostname: SafeHostname(hostname),
		Keep:     r.Keep,
		Days:     r.Days,
	}, nil
}

// Copy is one backup in the bucket.
type Copy struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// Hostname is the router that took it, read from the name.
	Hostname string    `json:"hostname"`
	TakenAt  time.Time `json:"takenAt"`
	Size     int64     `json:"size"`
}

// Report is what a run did, for the cron's output line.
type Report struct {
	Key   string
	Bytes int
	// Deleted is how many older copies the prune removed.
	Deleted int
	// Rule is what happened to the retention rule: wrote, unchanged,
	// removed, or nothing at all.
	Rule string
}

func (r Report) String() string {
	var parts []string
	if r.Key != "" {
		parts = append(parts, fmt.Sprintf("uploaded %s (%s)", r.Key, humanSize(r.Bytes)))
	}
	if r.Deleted > 0 {
		parts = append(parts, fmt.Sprintf("deleted %d older %s", r.Deleted, plural(r.Deleted, "copy", "copies")))
	}
	switch r.Rule {
	case "wrote":
		parts = append(parts, "wrote the retention rule")
	case "removed":
		parts = append(parts, "removed the retention rule")
	case "unchanged":
		parts = append(parts, "retention rule unchanged")
	}
	if len(parts) == 0 {
		return "nothing was copied"
	}
	return strings.Join(parts, "; ")
}

// Run uploads a copy, deletes the ones past the limit, and writes the
// retention rule. It does not stop at the first failure: a prune that
// cannot delete is no reason to leave the bucket without a rule, and the
// report says what did happen.
func (r *Remote) Run(ctx context.Context, archive *Archive) (Report, error) {
	var rep Report
	if archive == nil {
		return rep, ErrNoConfig
	}
	// Checked before anything is sent: there is no path here that
	// uploads a configuration anybody could read.
	if !archive.Encrypted() {
		return rep, ErrNotEncrypted
	}
	raw, err := archive.Bytes()
	if err != nil {
		return rep, err
	}

	var errs []error
	key := r.Prefix + archive.Filename()
	if err := r.S3.Put(ctx, key, raw, "application/octet-stream"); err != nil {
		errs = append(errs, err)
	} else {
		rep.Key, rep.Bytes = key, len(raw)
	}
	deleted, err := r.prune(ctx)
	rep.Deleted = deleted
	if err != nil {
		errs = append(errs, err)
	}
	rule, err := r.retention(ctx)
	rep.Rule = rule
	if err != nil {
		errs = append(errs, err)
	}

	// A failed upload is the runner's to log, with the error; this line is
	// for the copy that did land.
	if rep.Key != "" {
		log := r.Log
		if log == nil {
			log = slog.Default()
		}
		log.Info("copied the configuration to the bucket", "bucket", r.S3.Bucket, "key", rep.Key,
			"bytes", rep.Bytes, "deleted", rep.Deleted, "rule", rep.Rule)
	}
	return rep, errors.Join(errs...)
}

// prune deletes this router's copies past the limit, oldest first.
// Another router's copies under the same prefix are none of its
// business, and neither is anything that is not shaped like a backup.
func (r *Remote) prune(ctx context.Context) (int, error) {
	if r.Keep <= 0 {
		return 0, nil
	}
	copies, err := r.List(ctx)
	if err != nil {
		return 0, err
	}
	var mine []Copy
	for _, c := range copies {
		if c.Hostname == r.Hostname {
			mine = append(mine, c)
		}
	}
	if len(mine) <= r.Keep {
		return 0, nil
	}
	deleted := 0
	for _, c := range mine[r.Keep:] {
		if err := r.S3.Delete(ctx, c.Key); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

// RuleID is what the rule Ostiole owns is called in the bucket. It
// carries the prefix, so two routers writing to one bucket under
// different prefixes each own their own rule.
func RuleID(prefix string) string {
	return "ostiole-" + strings.TrimSuffix(prefix, "/")
}

// retention writes the rules the bucket expires copies under. Every rule
// Ostiole does not own goes back exactly as it came, and the comparison
// is of what a rule means rather than of its text: a service stores a
// rule in its own words, so comparing XML would rewrite the bucket every
// night.
func (r *Remote) retention(ctx context.Context) (string, error) {
	id := RuleID(r.Prefix)
	rules, err := r.S3.Lifecycle(ctx)
	if err != nil {
		return "", err
	}
	existing, found := rules.Rule(id)
	_, marked := r.marker(rules)

	if r.Keep > 0 || r.Days > 0 {
		want := s3.Expiry{
			Prefix:         r.Prefix,
			Days:           r.Days,
			NoncurrentDays: noncurrentDays,
			Status:         s3.Enabled,
		}
		// An expiry in days needs the companion below; nothing else does.
		wantMarker := r.Days > 0
		if found && marked == wantMarker {
			if got, err := existing.Expiry(); err == nil && got.Matches(want) {
				return "unchanged", nil
			}
		}
		rest, _ := r.strip(rules, id)
		rest.Rules = append(rest.Rules, s3.ExpiryRule(id, want))
		if wantMarker {
			rest.Rules = append(rest.Rules, markerRule(id, r.Prefix))
		}
		if err := r.S3.PutLifecycle(ctx, rest); err != nil {
			return "", err
		}
		return "wrote", nil
	}

	rest, had := r.strip(rules, id)
	if !had {
		return "", nil
	}
	if len(rest.Rules) == 0 {
		if err := r.S3.DeleteLifecycle(ctx); err != nil {
			return "", err
		}
		return "removed", nil
	}
	if err := r.S3.PutLifecycle(ctx, rest); err != nil {
		return "", err
	}
	return "removed", nil
}

// markerRule is the companion an expiry in days needs on a bucket that
// keeps versions: it removes the hide marker once it is the last version
// left. Neither Amazon nor Backblaze takes it in the same rule as the
// days — one refuses the pair outright, the other refuses the days
// without it — and both take it as a second rule on the same prefix.
func markerRule(id, prefix string) s3.Rule {
	return s3.ExpiryRule(id+"-markers", s3.Expiry{Prefix: prefix, DeleteMarker: true})
}

// marker finds the companion. Backblaze stores it under a name of its
// own, so it is found by its shape: a rule that does nothing but remove
// the hide markers under Ostiole's prefix.
func (r *Remote) marker(l *s3.Lifecycle) (s3.Rule, bool) {
	for _, rule := range l.Rules {
		e, err := rule.Expiry()
		if err == nil && e.Prefix == r.Prefix && e.DeleteMarker && e.Days == 0 && e.NoncurrentDays == 0 {
			return rule, true
		}
	}
	return s3.Rule{}, false
}

// strip returns the rules without the ones Ostiole owns, and whether it
// owned any. Writing starts from this, so a companion the service
// renamed is replaced rather than joined by a second one.
func (r *Remote) strip(l *s3.Lifecycle, id string) (*s3.Lifecycle, bool) {
	out := &s3.Lifecycle{}
	found := false
	companion, marked := r.marker(l)
	for _, rule := range l.Rules {
		if rule.ID == id || (marked && rule.ID == companion.ID) {
			found = true
			continue
		}
		out.Rules = append(out.Rules, rule)
	}
	return out, found
}

// List is every backup under the prefix, newest first, whichever router
// took it.
func (r *Remote) List(ctx context.Context) ([]Copy, error) {
	objects, err := r.S3.List(ctx, r.Prefix)
	if err != nil {
		return nil, err
	}
	out := []Copy{}
	for _, o := range objects {
		c, ok := r.copyOf(o.Key)
		if !ok {
			continue
		}
		c.Size = o.Size
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].TakenAt.Equal(out[j].TakenAt) {
			return out[i].TakenAt.After(out[j].TakenAt)
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}

// Fetch reads one copy back. A key that is not one of this router's
// backups is refused before any request: nothing a browser sends decides
// what is read out of the bucket.
func (r *Remote) Fetch(ctx context.Context, key string) ([]byte, error) {
	if _, ok := r.copyOf(key); !ok {
		return nil, ErrNotACopy
	}
	return r.S3.Get(ctx, key, maxRemoteBytes)
}

// copyOf reads a key. It never descends past the prefix's own level: a
// folder under it is somebody else's arrangement.
func (r *Remote) copyOf(key string) (Copy, bool) {
	name, ok := strings.CutPrefix(key, r.Prefix)
	if !ok || strings.Contains(name, "/") {
		return Copy{}, false
	}
	m := copyName.FindStringSubmatch(name)
	if m == nil {
		return Copy{}, false
	}
	when, err := time.Parse(Stamp, m[2])
	if err != nil {
		return Copy{}, false
	}
	return Copy{Key: key, Name: name, Hostname: m[1], TakenAt: when.UTC()}, true
}

// Upload is what the cron calls: build the bucket, run, report.
func Upload(ctx context.Context, r model.RemoteBackup, hostname, userAgent string, archive *Archive) (string, error) {
	remote, err := NewRemote(r, hostname, userAgent)
	if err != nil {
		return "", err
	}
	report, err := remote.Run(ctx, archive)
	return report.String(), err
}

func humanSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%d KB", (n+1023)/1024)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
