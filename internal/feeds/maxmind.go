package feeds

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"ostiole/internal/atomicfile"
	"ostiole/internal/model"
	"ostiole/internal/retry"
)

const (
	// MaxMindSource begins a country's source when the lists come from
	// MaxMind. One download holds every country, and the licence key stays
	// out of anything shown or cached.
	MaxMindSource = "maxmind:GeoLite2-Country/"
	// DefaultMaxMindURL is the GeoLite2 country download, which takes the
	// licence key alone; the newer endpoint also wants the account number.
	DefaultMaxMindURL = "https://download.maxmind.com/app/geoip_download?edition_id=GeoLite2-Country-CSV"
	// maxMindFile is the database kept beside the cached lists. Not a
	// .json file, so the cache never reads it as one.
	maxMindFile = "maxmind-GeoLite2-Country-CSV.zip"
	// maxMindRecheck lets the aliases of one pass share one look at the
	// checksum.
	maxMindRecheck = MinRefresh
	// maxMindRetry is how long a failure stands for the other aliases of a
	// pass, so one bad download is not repeated by each: MaxMind allows a
	// GeoLite account 30 downloads a day.
	maxMindRetry = time.Minute
	// maxMindUnzipped bounds each file in the archive; the address tables
	// were about 21 MB each in 2026-10.
	maxMindUnzipped = 128 << 20
)

// usesMaxMind reports whether cfg reads a country list from MaxMind.
func usesMaxMind(cfg *model.Config) bool {
	if cfg.System.MaxMindLicenceKey == "" {
		return false
	}
	return slices.ContainsFunc(cfg.Aliases, func(a model.Alias) bool { return a.Type == model.AliasGeoIP })
}

// maxMindState is what a fetcher remembers of MaxMind between aliases.
type maxMindState struct {
	mu  sync.Mutex
	key string
	err error
	at  time.Time
}

// maxMind reads the countries of parts out of MaxMind's database, kept in
// dir when there is one. One call at a time, so two refreshes never both
// download it.
func (f *Fetcher) maxMind(ctx context.Context, key, dir string, parts []Part) (map[string][]string, error) {
	m := &f.maxMindState
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil && m.key == key && time.Since(m.at) < maxMindRetry {
		return nil, m.err
	}
	want := map[string]bool{}
	for _, p := range parts {
		want[p.Country] = true
	}
	db, kept, err := f.countryDB(ctx, key, dir)
	var out map[string][]string
	if err == nil {
		out, err = countriesIn(db, want)
		if err != nil && kept {
			// A copy that no longer reads is downloaded again.
			_ = os.Remove(filepath.Join(dir, maxMindFile))
			if db, _, err = f.countryDB(ctx, key, dir); err == nil {
				out, err = countriesIn(db, want)
			}
		}
	}
	if ctx.Err() == nil {
		// A request given up on says nothing about MaxMind.
		m.key, m.err, m.at = key, err, time.Now()
	}
	return out, err
}

// countryDB is the database, downloaded again only when MaxMind's checksum
// no longer matches the copy in dir, and whether it is that copy. MaxMind
// publishes twice a week.
func (f *Fetcher) countryDB(ctx context.Context, key, dir string) ([]byte, bool, error) {
	var path string
	var have []byte
	if dir != "" {
		path = filepath.Join(dir, maxMindFile)
		if st, err := os.Stat(path); err == nil {
			if raw, err := os.ReadFile(path); err == nil {
				if time.Since(st.ModTime()) < maxMindRecheck {
					return raw, true, nil
				}
				have = raw
			}
		}
	}
	base := f.MaxMindURL
	if base == "" {
		base = DefaultMaxMindURL
	}
	base += "&license_key=" + url.QueryEscape(key) + "&suffix="
	raw, err := f.get(ctx, base+"zip.sha256")
	if err != nil {
		return nil, false, maxMindError(err)
	}
	want, ok := checksum(raw)
	if !ok {
		return nil, false, errors.New("MaxMind's checksum does not read as one")
	}
	if have != nil && sha256.Sum256(have) == want {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return have, true, nil
	}
	db, err := f.get(ctx, base+"zip")
	if err != nil {
		return nil, false, maxMindError(err)
	}
	if sha256.Sum256(db) != want {
		return nil, false, errors.New("the MaxMind download does not match its checksum")
	}
	if path != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, false, err
		}
		if err := atomicfile.Write(path, db, 0o600); err != nil {
			return nil, false, err
		}
	}
	return db, false, nil
}

// maxMindError says what MaxMind's refusal means.
func maxMindError(err error) error {
	if status, ok := errors.AsType[retry.StatusError](err); ok && status == http.StatusUnauthorized {
		return errors.New("MaxMind refused the licence key")
	}
	return fmt.Errorf("MaxMind: %w", err)
}

// checksum reads a sha256sum line: the digest, then the file name.
func checksum(raw []byte) ([sha256.Size]byte, bool) {
	var sum [sha256.Size]byte
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return sum, false
	}
	b, err := hex.DecodeString(fields[0])
	if err != nil || len(b) != sha256.Size {
		return sum, false
	}
	copy(sum[:], b)
	return sum, true
}

// countriesIn reads the networks of each wanted country, by lower-case
// code, out of the database. A network goes to the country it is used in,
// or the one it is registered in where MaxMind does not place it. A code
// MaxMind has no networks for is an empty list.
func countriesIn(db []byte, want map[string]bool) (map[string][]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(db), int64(len(db)))
	if err != nil {
		return nil, fmt.Errorf("the MaxMind download is not a zip archive: %w", err)
	}
	const locations, v4, v6 = "-Country-Locations-en.csv", "-Country-Blocks-IPv4.csv", "-Country-Blocks-IPv6.csv"
	files := map[string]*zip.File{}
	for _, zf := range zr.File {
		for _, suffix := range []string{locations, v4, v6} {
			if strings.HasSuffix(zf.Name, suffix) {
				files[suffix] = zf
			}
		}
	}
	for _, suffix := range []string{locations, v4, v6} {
		if files[suffix] == nil {
			return nil, fmt.Errorf("the MaxMind download has no file ending %s", suffix)
		}
	}
	codes := map[string]string{}
	err = eachRow(files[locations], []string{"geoname_id", "country_iso_code"}, func(row []string) error {
		if row[1] != "" {
			codes[row[0]] = strings.ToLower(row[1])
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	nets := map[string][]netip.Prefix{}
	for _, suffix := range []string{v4, v6} {
		err := eachRow(files[suffix], []string{"network", "geoname_id", "registered_country_geoname_id"}, func(row []string) error {
			code := codes[row[1]]
			if code == "" {
				code = codes[row[2]]
			}
			if !want[code] {
				return nil
			}
			p, err := netip.ParsePrefix(row[0])
			if err != nil {
				return fmt.Errorf("%q is not a network", row[0])
			}
			nets[code] = append(nets[code], p)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	out := make(map[string][]string, len(want))
	for code := range want {
		list := []string{}
		for _, p := range collapse(nets[code]) {
			if p.Bits() > 0 {
				list = append(list, prefixString(p))
			}
		}
		out[code] = list
	}
	return out, nil
}

// eachRow calls fn with the named columns of every row of a CSV file in
// the archive, in the order named.
func eachRow(zf *zip.File, columns []string, fn func([]string) error) error {
	if zf.UncompressedSize64 > maxMindUnzipped {
		return fmt.Errorf("%s is larger than %d bytes", zf.Name, maxMindUnzipped)
	}
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	r := csv.NewReader(io.LimitReader(rc, maxMindUnzipped))
	r.ReuseRecord = true
	header, err := r.Read()
	if err != nil {
		return fmt.Errorf("%s: %w", zf.Name, err)
	}
	at := make([]int, len(columns))
	for i, c := range columns {
		if at[i] = slices.Index(header, c); at[i] < 0 {
			return fmt.Errorf("%s has no %s column", zf.Name, c)
		}
	}
	row := make([]string, len(columns))
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", zf.Name, err)
		}
		for i, n := range at {
			row[i] = rec[n]
		}
		if err := fn(row); err != nil {
			return fmt.Errorf("%s: %w", zf.Name, err)
		}
	}
}
