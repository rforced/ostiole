package dnsclient

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"ostiole/internal/dnsclient/dnstest"
)

// A truncated answer over UDP is asked again over TCP, where the whole of
// it fits.
func TestExchangeRetriesATruncatedAnswerOverTCP(t *testing.T) {
	t.Parallel()
	srv := dnstest.New(t)
	srv.Add(dnstest.TXT("big.example.test", strings.Repeat("a", 255), strings.Repeat("b", 255)))
	srv.Truncate()

	got, err := TXT(context.Background(), srv.Addr, "big.example.test", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Repeat("a", 255) + strings.Repeat("b", 255); len(got) != 1 || got[0] != want {
		t.Errorf("TXT = %q, want the two strings joined", got)
	}
	want := []string{"udp big.example.test. TXT", "tcp big.example.test. TXT"}
	if asked := srv.Asked(); !slices.Equal(asked, want) {
		t.Errorf("asked %q, want %q", asked, want)
	}
}

// A question lost over UDP goes again.
func TestExchangeSendsALostQuestionAgain(t *testing.T) {
	t.Parallel()
	srv := dnstest.New(t)
	srv.Add(dnstest.TXT("a.example.test", "x"))
	srv.Drop(1)

	got, err := TXT(context.Background(), srv.Addr, "a.example.test", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "x" {
		t.Errorf("TXT = %q", got)
	}
	if n := len(srv.Asked()); n != 2 {
		t.Errorf("asked %d times, want 2", n)
	}
}

// An answer with another ID is not the answer, and the real one after it
// is taken.
func TestExchangeIgnoresAnAnswerToAnotherQuestion(t *testing.T) {
	t.Parallel()
	srv := dnstest.New(t)
	srv.Add(dnstest.TXT("a.example.test", "real"))
	srv.Spoof()

	got, err := TXT(context.Background(), srv.Addr, "a.example.test", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "real" {
		t.Errorf("TXT = %q, want the answer with the right ID", got)
	}
}

// A server that never answers costs the caller its own deadline, not the
// exchange's ten seconds.
func TestExchangeEndsWithTheContext(t *testing.T) {
	t.Parallel()
	srv := dnstest.New(t)
	srv.Drop(1000)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := TXT(ctx, srv.Addr, "a.example.test", true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("took %s", took)
	}
}

// Every question carries EDNS(0), so a set of TXT values larger than 512
// bytes can come back over UDP.
func TestQueryOffersEDNS(t *testing.T) {
	t.Parallel()
	q, err := Query("example.test", dnsmessage.TypeTXT, false)
	if err != nil {
		t.Fatal(err)
	}
	if q.RecursionDesired {
		t.Error("an authoritative question asks for recursion")
	}
	if len(q.Additionals) != 1 || q.Additionals[0].Header.Type != dnsmessage.TypeOPT || q.Additionals[0].Header.Class != payload {
		t.Errorf("additionals = %+v, want one OPT offering %d bytes", q.Additionals, payload)
	}
	if got := q.Questions[0].Name.String(); got != "example.test." {
		t.Errorf("name = %q, want it fully qualified", got)
	}
	if _, err := Query(strings.Repeat("a", 300), dnsmessage.TypeTXT, true); err == nil {
		t.Error("a name longer than DNS allows was taken")
	}
}

func TestNames(t *testing.T) {
	t.Parallel()
	if got := RCodeName(dnsmessage.RCodeServerFailure); got != "SERVFAIL" {
		t.Errorf("RCodeName = %q", got)
	}
	if got := RCodeName(23); got != "RCODE23" {
		t.Errorf("RCodeName(23) = %q", got)
	}
	if got := TypeName(dnsmessage.TypeTXT); got != "TXT" {
		t.Errorf("TypeName = %q", got)
	}
	if got := TypeName(250); got != "TYPE250" {
		t.Errorf("TypeName(250) = %q", got)
	}
}
