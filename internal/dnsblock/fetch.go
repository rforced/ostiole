package dnsblock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// DefaultTimeout is how long one fetch may take. Some of these lists are
// large and served slowly.
const DefaultTimeout = 2 * time.Minute

// sampleBytes is how much of a list is read before deciding how it is
// written. Every list is consistent from its first entry, so this is
// plenty, and it is peeked rather than consumed.
const sampleBytes = 64 << 10

// Fetcher downloads lists. It is deliberately separate from the fetcher in
// internal/feeds: that one reads addresses into memory as JSON, and this
// one streams names to disk, because the lists are two orders of magnitude
// bigger.
type Fetcher struct {
	Client  *http.Client
	Timeout time.Duration
	// UserAgent identifies this box to the publisher, several of whom ask
	// for one.
	UserAgent string
}

// NewFetcher returns a fetcher with production defaults.
func NewFetcher(version string) *Fetcher {
	return &Fetcher{
		Client:    &http.Client{Timeout: DefaultTimeout},
		Timeout:   DefaultTimeout,
		UserAgent: "ostiole/" + version,
	}
}

// Fetch downloads one list and returns the names in it, how many lines held
// nothing usable, and how the list turned out to be written.
func (f *Fetcher) Fetch(ctx context.Context, l model.BlockList) ([]string, int, model.ListFormat, error) {
	if l.URL == "" {
		return nil, 0, "", ErrNoSource
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.URL, nil)
	if err != nil {
		return nil, 0, "", err
	}
	if f.UserAgent != "" {
		req.Header.Set("User-Agent", f.UserAgent)
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return ParseStream(resp.Body, l.FormatOrAuto())
}

// ParseStream reads a list and returns the names in it. It is shared by the
// fetcher and by the upload path, so a list pasted into the UI is read
// exactly the way a fetched one is.
//
// A line that will not parse is counted, not fatal: lists carry titles,
// licences and the occasional broken entry. A list where nothing at all
// parses is an error, because that is what an HTML error page looks like
// from in here, and quietly replacing yesterday's list with nothing would
// turn blocking off without saying so.
func ParseStream(r io.Reader, format model.ListFormat) ([]string, int, model.ListFormat, error) {
	br := bufio.NewReaderSize(io.LimitReader(r, MaxBytes+1), sampleBytes*2)
	if format == "" || format == model.FormatAuto {
		sample, err := br.Peek(sampleBytes)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
			return nil, 0, "", err
		}
		format = DetectFormat(string(sample))
	}

	seen := make(map[string]struct{}, 1024)
	skipped := 0
	read := int64(0)
	sc := bufio.NewScanner(br)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		read += int64(len(line)) + 1
		if read > MaxBytes {
			return nil, 0, "", fmt.Errorf("larger than %d bytes", MaxBytes)
		}
		name, ok := ParseLine(line, format)
		if !ok {
			if strip(line) != "" {
				skipped++
			}
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		if len(seen) > MaxListDomains {
			return nil, 0, "", fmt.Errorf("more than %d names", MaxListDomains)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, "", err
	}
	if len(seen) == 0 {
		return nil, 0, "", fmt.Errorf("nothing usable in this list (%d unreadable lines); is it really %s?", skipped, format)
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out, skipped, format, nil
}
