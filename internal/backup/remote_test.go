package backup_test

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/backup"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/s3"
	"github.com/rforced/ostiole/internal/s3/s3test"
)

const passphrase = "correct horse battery staple"

var taken = time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)

// remote is a bucket and a Remote pointed at it.
func remote(t *testing.T, keep, days int) (*s3test.Bucket, *backup.Remote) {
	t.Helper()
	b := s3test.New(t, "key", "secret")
	r := &backup.Remote{
		S3:       b.Client(),
		Prefix:   model.DefaultBackupPrefix,
		Hostname: "router",
		Keep:     keep,
		Days:     days,
		Log:      slog.New(slog.DiscardHandler),
	}
	return b, r
}

func archive(t *testing.T, when time.Time) *backup.Archive {
	t.Helper()
	cfg := model.Starter(model.StarterOptions{Hostname: "router", LAN: "eth0", LANAddress: "192.168.1.1/24"})
	a, err := backup.Create(cfg, backup.Options{
		Passphrase: passphrase,
		Now:        func() time.Time { return when },
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRunUploadsOneEncryptedCopy(t *testing.T) {
	b, r := remote(t, 0, 0)
	rep, err := r.Run(t.Context(), archive(t, taken))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	const key = "ostiole/router-20260920-030000.json.age"
	if rep.Key != key {
		t.Errorf("uploaded %q, want %q", rep.Key, key)
	}
	stored, ok := b.Object(key)
	if !ok {
		t.Fatalf("the bucket holds %v", b.Keys())
	}
	if !backup.IsEncrypted(stored.Body) {
		t.Error("what was uploaded is not encrypted")
	}
	if stored.ContentType != "application/octet-stream" {
		t.Errorf("content type %q", stored.ContentType)
	}
	// Neither knob is set, so the bucket's rules are left alone.
	if rep.Rule != "" || b.Rules() != nil {
		t.Errorf("rule %q, bucket rules %s", rep.Rule, b.Rules())
	}
	if !strings.HasPrefix(rep.String(), "uploaded "+key+" (") {
		t.Errorf("report = %q", rep.String())
	}
}

func TestRunRefusesAnUnencryptedArchive(t *testing.T) {
	b, r := remote(t, 0, 0)
	cfg := model.Starter(model.StarterOptions{Hostname: "router", LAN: "eth0", LANAddress: "192.168.1.1/24"})
	plain, err := backup.Create(cfg, backup.Options{Hostname: "router"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(t.Context(), plain); !errors.Is(err, backup.ErrNotEncrypted) {
		t.Fatalf("Run: %v, want ErrNotEncrypted", err)
	}
	if n := len(b.Seen()); n != 0 {
		t.Errorf("the bucket saw %d requests, want none", n)
	}
}

func TestPruneKeepsTheNewestOfThisRouterOnly(t *testing.T) {
	b, r := remote(t, 2, 0)
	for i := range 4 {
		when := taken.AddDate(0, 0, -1-i)
		b.Store("ostiole/router-"+when.Format(backup.Stamp)+".json.age", []byte("old"), when)
	}
	b.Store("ostiole/other-20260919-030000.json.age", []byte("not ours"), taken)
	b.Store("ostiole/notes.txt", []byte("hands off"), taken)

	rep, err := r.Run(t.Context(), archive(t, taken))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Deleted != 3 {
		t.Errorf("deleted %d, want 3", rep.Deleted)
	}
	want := []string{
		"ostiole/notes.txt",
		"ostiole/other-20260919-030000.json.age",
		"ostiole/router-20260919-030000.json.age",
		"ostiole/router-20260920-030000.json.age",
	}
	if got := b.Keys(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the bucket holds\n%v\nwant\n%v", got, want)
	}
}

func TestRetentionWritesOstiolesRuleOnceAndKeepsForeignOnes(t *testing.T) {
	b, r := remote(t, 0, 30)
	const foreign = `<ID>someone-elses</ID><Filter><Prefix>logs/</Prefix></Filter>` +
		`<Status>Enabled</Status><Expiration><Days>7</Days></Expiration>`
	b.SetRules(`<LifecycleConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
		`<Rule>` + foreign + `</Rule></LifecycleConfiguration>`)

	rep, err := r.Run(t.Context(), archive(t, taken))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Rule != "wrote" {
		t.Fatalf("rule %q, want wrote", rep.Rule)
	}
	rules := string(b.Rules())
	if !strings.Contains(rules, foreign) {
		t.Errorf("the foreign rule was rewritten:\n%s", rules)
	}
	for _, want := range []string{
		"<ID>ostiole-ostiole</ID>", "<Prefix>ostiole/</Prefix>",
		"<Days>30</Days>", "<NoncurrentDays>1</NoncurrentDays>",
		// The companion an expiry in days needs, as its own rule on the
		// same prefix: neither service takes it beside the days.
		"<ID>ostiole-ostiole-markers</ID>", "<ExpiredObjectDeleteMarker>true</ExpiredObjectDeleteMarker>",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("rules missing %s:\n%s", want, rules)
		}
	}
	if n := strings.Count(rules, "<Rule>"); n != 3 {
		t.Errorf("%d rules written, want the foreign one and Ostiole's pair:\n%s", n, rules)
	}

	// The service keeps the rule in its own words; a second run has to
	// recognise its own rule and write nothing.
	before := len(b.Seen())
	rep, err = r.Run(t.Context(), archive(t, taken.Add(time.Hour)))
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if rep.Rule != "unchanged" {
		t.Errorf("rule %q on the second run, want unchanged", rep.Rule)
	}
	for _, req := range b.Seen()[before:] {
		if req.Method == http.MethodPut && strings.Contains(req.Query, "lifecycle") {
			t.Error("the second run wrote the rules again")
		}
	}
}

// A service may store the companion under a name of its own — Backblaze
// renames it — so the next write has to find it by its shape rather than
// leave a second one behind.
func TestRetentionReplacesACompanionTheServiceRenamed(t *testing.T) {
	b, r := remote(t, 0, 30)
	if _, err := r.Run(t.Context(), archive(t, taken)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b.SetRules(strings.ReplaceAll(string(b.Rules()), "ostiole-ostiole-markers", "ostiole-ostiole_marker"))

	r.Days = 60
	rep, err := r.Run(t.Context(), archive(t, taken.Add(time.Hour)))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Rule != "wrote" {
		t.Fatalf("rule %q, want wrote", rep.Rule)
	}
	rules := string(b.Rules())
	if n := strings.Count(rules, "<ExpiredObjectDeleteMarker>"); n != 1 {
		t.Errorf("%d delete-marker rules, want one:\n%s", n, rules)
	}
	if !strings.Contains(rules, "<Days>60</Days>") {
		t.Errorf("the new expiry was not written:\n%s", rules)
	}
}

func TestRetentionRemovesOstiolesRuleWhenNeitherKnobIsSet(t *testing.T) {
	b, r := remote(t, 0, 30)
	if _, err := r.Run(t.Context(), archive(t, taken)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if b.Rules() == nil {
		t.Fatal("the first run wrote no rule")
	}
	r.Days = 0
	rep, err := r.Run(t.Context(), archive(t, taken.Add(time.Hour)))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Rule != "removed" {
		t.Errorf("rule %q, want removed", rep.Rule)
	}
	// Ostiole's was the only rule, so the whole configuration goes.
	if b.Rules() != nil {
		t.Errorf("rules left behind: %s", b.Rules())
	}
}

func TestARefusedRuleWriteStillReportsTheUpload(t *testing.T) {
	b, r := remote(t, 0, 30)
	b.FailOnce(s3.OpWriteRule, http.StatusForbidden, "AccessDenied")
	rep, err := r.Run(t.Context(), archive(t, taken))
	if err == nil {
		t.Fatal("Run: no error, want the refusal")
	}
	if got := err.Error(); !strings.Contains(got, "the key may not write the retention rule") {
		t.Errorf("error = %q", got)
	}
	if rep.Key == "" {
		t.Error("the upload was not reported")
	}
	if _, ok := b.Object("ostiole/router-20260920-030000.json.age"); !ok {
		t.Error("the copy did not land")
	}
}

func TestListIsNewestFirstAndSkipsWhatIsNotABackup(t *testing.T) {
	b, r := remote(t, 0, 0)
	for _, when := range []time.Time{taken, taken.AddDate(0, 0, -2), taken.AddDate(0, 0, -1)} {
		b.Store("ostiole/router-"+when.Format(backup.Stamp)+".json.age", []byte("x"), when)
	}
	b.Store("ostiole/notes.txt", []byte("x"), taken)
	b.Store("ostiole/plain-20260920-030000.json", []byte("x"), taken)
	b.Store("ostiole/older/router-20260918-030000.json.age", []byte("x"), taken)

	copies, err := r.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(copies) != 3 {
		t.Fatalf("listed %d copies, want 3: %+v", len(copies), copies)
	}
	for i := 1; i < len(copies); i++ {
		if copies[i].TakenAt.After(copies[i-1].TakenAt) {
			t.Errorf("copies are not newest first: %+v", copies)
		}
	}
	first := copies[0]
	if first.Hostname != "router" || first.Name != "router-20260920-030000.json.age" {
		t.Errorf("first copy = %+v", first)
	}
	if !first.TakenAt.Equal(taken) {
		t.Errorf("taken at %v, want %v", first.TakenAt, taken)
	}
}

func TestFetchOnlyReadsABackup(t *testing.T) {
	b, r := remote(t, 0, 0)
	b.Store("ostiole/router-20260920-030000.json.age", []byte("encrypted"), taken)
	got, err := r.Fetch(t.Context(), "ostiole/router-20260920-030000.json.age")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != "encrypted" {
		t.Errorf("read %q", got)
	}
	before := len(b.Seen())
	for _, key := range []string{
		"../etc/shadow",
		"ostiole/../secrets.json.age",
		"ostiole/notes.txt",
		"ostiole/older/router-20260918-030000.json.age",
		"elsewhere/router-20260920-030000.json.age",
	} {
		if _, err := r.Fetch(t.Context(), key); !errors.Is(err, backup.ErrNotACopy) {
			t.Errorf("Fetch(%q) = %v, want ErrNotACopy", key, err)
		}
	}
	if n := len(b.Seen()) - before; n != 0 {
		t.Errorf("the bucket saw %d requests for keys that are not backups", n)
	}
}

func TestUploadBuildsTheBucketFromTheSettings(t *testing.T) {
	// Only the settings that need no network are exercised here; the
	// endpoint is a real one, so nothing is sent.
	_, err := backup.Upload(t.Context(), model.RemoteBackup{
		Endpoint: "not a url", Bucket: "b", KeyID: "k", Secret: "s",
	}, "router", archive(t, taken))
	if err == nil {
		t.Fatal("a broken endpoint was accepted")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error = %v", err)
	}
}

func TestReportReadsAsASentence(t *testing.T) {
	rep := backup.Report{Key: "ostiole/router-20260920-030000.json.age", Bytes: 21000, Deleted: 2, Rule: "unchanged"}
	want := "uploaded ostiole/router-20260920-030000.json.age (21.0 kB); deleted 2 older copies; retention rule unchanged"
	if got := rep.String(); got != want {
		t.Errorf("report = %q, want %q", got, want)
	}
	if got := (backup.Report{}).String(); got != "nothing was copied" {
		t.Errorf("empty report = %q", got)
	}
	if got := (backup.Report{Deleted: 1}).String(); got != "deleted 1 older copy" {
		t.Errorf("one deletion = %q", got)
	}
	if got := backup.RuleID("routers/one/"); got != "ostiole-routers/one" {
		t.Errorf("RuleID = %q", got)
	}
}

// versioned is a bucket that keeps versions, as every B2 bucket does.
func versioned(t *testing.T) (*s3test.Bucket, *backup.Remote) {
	t.Helper()
	b, r := remote(t, 0, 0)
	b.SetVersioned(true)
	return b, r
}

// deletedVersions reads the ids of the versioned deletes the bucket was
// sent, in order.
func deletedVersions(t *testing.T, reqs []s3test.Request) []string {
	t.Helper()
	var out []string
	for _, req := range reqs {
		if req.Method != http.MethodDelete {
			continue
		}
		q, err := url.ParseQuery(req.Query)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, q.Get("versionId"))
	}
	return out
}

// A copy's versions go oldest first and its hide markers after them, so a
// delete cut short never brings an older version back into the list or
// unhides a copy. That holds whether the service interleaves the two
// kinds, as Amazon does, or lists a page's markers first, as Backblaze
// does.
func TestDeleteRemovesVersionsOldestFirstThenMarkers(t *testing.T) {
	const key = "ostiole/router-20260920-030000.json.age"
	for _, answer := range []s3test.Answer{s3test.VersionsListed, s3test.VersionsMarkersFirst} {
		for name, hides := range map[string][]bool{
			"a current copy with an older marker": {false, true, false},
			"a hidden copy":                       {false, false, true},
		} {
			b, r := versioned(t)
			b.AnswerVersions(answer)
			for i, hide := range hides {
				when := taken.Add(time.Duration(i) * time.Minute)
				if hide {
					b.Hide(key, when)
				} else {
					b.Store(key, []byte("x"), when)
				}
			}
			b.Store("ostiole/router-20260919-030000.json.age", []byte("older"), taken.AddDate(0, 0, -1))
			var want, markers []string
			for _, v := range b.Versions(key) {
				if v.Marker {
					markers = append(markers, v.ID)
				} else {
					want = append(want, v.ID)
				}
			}
			want = append(want, markers...)

			removed, err := r.Delete(t.Context(), key)
			if err != nil {
				t.Fatalf("answer %d, %s: Delete: %v", answer, name, err)
			}
			if removed != (backup.Removed{Copies: 1, Versions: len(hides)}) {
				t.Errorf("answer %d, %s: removed %+v", answer, name, removed)
			}
			if got := deletedVersions(t, b.Seen()); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("answer %d, %s: deleted %v, want %v", answer, name, got, want)
			}
			if held := b.Versions(key); len(held) != 0 {
				t.Errorf("answer %d, %s: the bucket still holds %+v", answer, name, held)
			}
			if got := b.Keys(); len(got) != 1 || got[0] != "ostiole/router-20260919-030000.json.age" {
				t.Errorf("answer %d, %s: the other copy went too: %v", answer, name, got)
			}
		}
	}
}

func TestDeleteOnABucketWithoutVersionsSaysNull(t *testing.T) {
	b, r := remote(t, 0, 0)
	const key = "ostiole/router-20260920-030000.json.age"
	b.Store(key, []byte("x"), taken)
	if _, err := r.Delete(t.Context(), key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := deletedVersions(t, b.Seen()); len(got) != 1 || got[0] != "null" {
		t.Errorf("deleted %v, want the null version", got)
	}
	if held := b.Held(); len(held) != 0 {
		t.Errorf("the bucket still holds %v", held)
	}
}

// A service that keeps no versions removes a copy for good with a plain
// delete, whether it says it has none or ignores the question.
func TestDeleteOnAServiceWithoutVersionsIsPlain(t *testing.T) {
	for _, answer := range []s3test.Answer{s3test.VersionsNotImplemented, s3test.VersionsIgnored} {
		b, r := remote(t, 0, 0)
		b.AnswerVersions(answer)
		const key = "ostiole/router-20260920-030000.json.age"
		b.Store(key, []byte("x"), taken)
		removed, err := r.Delete(t.Context(), key)
		if err != nil {
			t.Fatalf("answer %d: Delete: %v", answer, err)
		}
		if removed != (backup.Removed{Copies: 1, Versions: 1}) {
			t.Errorf("answer %d: removed %+v", answer, removed)
		}
		seen := b.Seen()
		if last := seen[len(seen)-1]; last.Method != http.MethodDelete || last.Query != "" {
			t.Errorf("answer %d: last request %+v, want a plain DELETE", answer, last)
		}
		if held := b.Held(); len(held) != 0 {
			t.Errorf("answer %d: the bucket still holds %v", answer, held)
		}
	}
}

func TestDeleteAllTakesTheNamedAndTheHiddenOnly(t *testing.T) {
	b, r := versioned(t)
	named := []string{
		"ostiole/router-20260920-030000.json.age",
		"ostiole/router-20260919-030000.json.age",
	}
	b.Store(named[0], []byte("x"), taken)
	b.Store(named[0], []byte("xx"), taken.Add(time.Minute))
	b.Store(named[1], []byte("x"), taken.AddDate(0, 0, -1))
	// Another router's copy, hidden by its prune and not in the list the
	// page showed.
	const hidden = "ostiole/other-20260918-030000.json.age"
	b.Store(hidden, []byte("x"), taken.AddDate(0, 0, -2))
	b.Hide(hidden, taken.AddDate(0, 0, -1))
	// Uploaded after the page read the list.
	const fresh = "ostiole/router-20260920-040000.json.age"
	b.Store(fresh, []byte("x"), taken.Add(time.Hour))
	b.Store("ostiole/notes.txt", []byte("hands off"), taken)
	b.Store("ostiole/older/router-20260917-030000.json.age", []byte("x"), taken)
	b.Hide("ostiole/older/router-20260917-030000.json.age", taken)

	removed, err := r.DeleteAll(t.Context(), named)
	if err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if removed != (backup.Removed{Copies: 3, Versions: 5}) {
		t.Errorf("removed %+v, want three copies of five versions", removed)
	}
	want := []string{"ostiole/notes.txt", "ostiole/older/router-20260917-030000.json.age", fresh}
	if got := b.Held(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the bucket holds\n%v\nwant\n%v", got, want)
	}
	if n := len(b.Versions("ostiole/older/router-20260917-030000.json.age")); n != 2 {
		t.Errorf("the folder under the prefix lost versions: %d left", n)
	}
}

func TestDeleteSendsNothingForAKeyThatIsNotACopy(t *testing.T) {
	b, r := versioned(t)
	b.Store("ostiole/router-20260920-030000.json.age", []byte("x"), taken)
	b.Store("ostiole/notes.txt", []byte("x"), taken)
	for _, key := range []string{
		"../etc/shadow",
		"ostiole/notes.txt",
		"ostiole/older/router-20260918-030000.json.age",
		"elsewhere/router-20260920-030000.json.age",
	} {
		if _, err := r.Delete(t.Context(), key); !errors.Is(err, backup.ErrNotACopy) {
			t.Errorf("Delete(%q) = %v, want ErrNotACopy", key, err)
		}
	}
	// One bad key refuses the lot.
	_, err := r.DeleteAll(t.Context(), []string{"ostiole/router-20260920-030000.json.age", "ostiole/notes.txt"})
	if !errors.Is(err, backup.ErrNotACopy) {
		t.Errorf("DeleteAll = %v, want ErrNotACopy", err)
	}
	if _, err := r.DeleteAll(t.Context(), nil); !errors.Is(err, backup.ErrNoCopies) {
		t.Errorf("DeleteAll with no keys = %v, want ErrNoCopies", err)
	}
	if n := len(b.Seen()); n != 0 {
		t.Errorf("the bucket saw %d requests", n)
	}
}

// A key that may not touch versions is told so. Nothing is hidden in
// place of the delete it asked for.
func TestARefusedDeleteHidesNothing(t *testing.T) {
	const key = "ostiole/router-20260920-030000.json.age"
	for op, want := range map[string]string{
		s3.OpListVersions:  "the key may not list versions",
		s3.OpDeleteVersion: "the key may not delete versions",
	} {
		b, r := versioned(t)
		b.Store(key, []byte("x"), taken)
		b.FailOnce(op, http.StatusForbidden, "AccessDenied")
		_, err := r.Delete(t.Context(), key)
		if got := errString(err); got != want {
			t.Errorf("%s refused: %q, want %q", op, got, want)
		}
		if vs := b.Versions(key); len(vs) != 1 || vs[0].Marker {
			t.Errorf("%s refused: the copy is now %+v", op, vs)
		}
	}
}

func TestAVersionAlreadyGoneIsNoFailure(t *testing.T) {
	b, r := versioned(t)
	const key = "ostiole/router-20260920-030000.json.age"
	b.Store(key, []byte("x"), taken)
	b.Store(key, []byte("xx"), taken.Add(time.Minute))
	b.FailOnce(s3.OpDeleteVersion, http.StatusNotFound, "NoSuchVersion")
	removed, err := r.Delete(t.Context(), key)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if removed != (backup.Removed{Copies: 1, Versions: 1}) {
		t.Errorf("removed %+v, want the one version it did remove", removed)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
