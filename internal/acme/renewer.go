package acme

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/certs"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/panics"
)

// failureBackoff is how long a certificate waits after the CA refused it.
// Let's Encrypt allows five failed validations an hour; spending them on
// a name that is still wrong helps nobody.
const failureBackoff = 6 * time.Hour

// defaultCheckAfter is how long to leave ARI alone when the CA gives no
// Retry-After of its own.
const defaultCheckAfter = time.Hour

// orderTimeout bounds what an order waits on before it starts. The order
// itself takes no context: lego's own HTTP and propagation timeouts end
// it, minutes rather than seconds for a dns-01 challenge.
const orderTimeout = 10 * time.Minute

// Renewer decides when a certificate is asked for. Issuer does the
// asking.
type Renewer struct {
	Store  *certs.Store
	Issuer Issuer
	Config func() *model.Config
	// Addresses lists the public addresses of an interface now.
	Addresses func(iface string) []string
	Log       *slog.Logger

	mu      sync.Mutex
	running map[string]bool
}

// ErrIssuing says a certificate is already being ordered.
var ErrIssuing = errors.New("this certificate is already being issued")

// ErrUnknownCertificate says the configuration has no such certificate.
var ErrUnknownCertificate = errors.New("no such certificate")

// ErrUploaded says there is nothing to order: the certificate was pasted in.
var ErrUploaded = errors.New("this certificate was uploaded; there is nothing to order")

// Status is one certificate as the API reports it: what the
// configuration asks for, and what is on disk.
type Status struct {
	ID          string           `json:"id"`
	Description string           `json:"description,omitempty"`
	Source      model.CertSource `json:"source"`
	Enabled     bool             `json:"enabled"`
	Names       []string         `json:"names,omitempty"`
	// Wanted is what an order would ask for now, the interfaces resolved
	// to the addresses they have.
	Wanted      []string   `json:"wanted,omitempty"`
	Issued      bool       `json:"issued"`
	NotBefore   time.Time  `json:"notBefore,omitzero"`
	NotAfter    time.Time  `json:"notAfter,omitzero"`
	Issuer      string     `json:"issuer,omitempty"`
	Fingerprint string     `json:"fingerprint,omitempty"`
	Expired     bool       `json:"expired,omitempty"`
	ExpiresSoon bool       `json:"expiresSoon,omitempty"`
	RenewAfter  *time.Time `json:"renewAfter,omitempty"`
	LastAttempt *time.Time `json:"lastAttempt,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
	Running     bool       `json:"running,omitempty"`
	// Serving is true for the certificate the web UI is set to.
	Serving bool `json:"serving,omitempty"`
}

// Pass issues what is missing and renews what is due. It is the hourly
// cron.
func (r *Renewer) Pass(ctx context.Context) (string, error) {
	cfg := r.config()
	checked, renewed, failed := 0, 0, 0
	var first error
	for _, cert := range cfg.Certificates {
		if !cert.Enabled || cert.Source != model.SourceACME {
			continue
		}
		if !r.claim(cert.ID) {
			continue
		}
		checked++
		ordered, err := r.renew(ctx, cfg, cert)
		switch {
		case err != nil:
			failed++
			if first == nil {
				first = fmt.Errorf("%s: %w", cert.ID, err)
			}
		case ordered:
			renewed++
		}
	}
	return fmt.Sprintf("%d checked, %d renewed, %d failed", checked, renewed, failed), first
}

// renew orders a claimed certificate when it is due, and gives the claim
// back however that ends. A claim kept would skip the certificate on every
// pass after, until it expired.
func (r *Renewer) renew(ctx context.Context, cfg *model.Config, cert model.Certificate) (ordered bool, err error) {
	defer r.release(cert.ID)
	// The renewal check reads what the CA sends as well.
	defer panics.Into(&err, r.log(), "certificate "+cert.ID)
	due, reason := r.due(ctx, cfg, cert)
	if !due {
		return false, nil
	}
	r.log().Info("issuing certificate", "id", cert.ID, "reason", reason)
	return true, r.issue(ctx, cfg, cert)
}

// IssueNow orders one certificate whatever the schedule says, in the
// background: the CA can take a minute and the button should not. The
// order outlives the request that asked for it.
func (r *Renewer) IssueNow(ctx context.Context, id string) error {
	cfg := r.config()
	cert, ok := cfg.Certificate(id)
	if !ok {
		return ErrUnknownCertificate
	}
	if cert.Source != model.SourceACME {
		return ErrUploaded
	}
	if !r.claim(id) {
		return ErrIssuing
	}
	want := *cert
	go func() {
		defer r.release(id)
		defer panics.Recover(r.log(), "certificate order")
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), orderTimeout)
		defer cancel()
		if err := r.issue(ctx, cfg, want); err != nil {
			r.log().Warn("could not issue certificate", "id", id, "err", err)
		}
	}()
	return nil
}

// Status reports every certificate in cfg, issued or not.
func (r *Renewer) Status(cfg *model.Config) []Status {
	out := []Status{}
	if cfg == nil {
		return out
	}
	for _, cert := range cfg.Certificates {
		st := Status{
			ID:          cert.ID,
			Description: cert.Description,
			Source:      cert.Source,
			Enabled:     cert.Enabled,
			Names:       cert.Names,
			Wanted:      r.wanted(cert),
			Running:     r.isRunning(cert.ID),
			Serving:     cfg.System.Management.Certificate == cert.ID,
		}
		// The state is read on its own: a certificate the CA refused has
		// one and no files, and that is exactly the row worth explaining.
		if state, err := r.Store.ReadState(cert.ID); err == nil {
			st.RenewAfter, st.LastAttempt = state.RenewAfter, state.LastAttempt
			st.LastError = state.LastError
		}
		if f, err := r.Store.Read(cert.ID); err == nil {
			st.Issued = true
			if info, err := certs.Describe(f.Cert); err == nil {
				st.NotBefore, st.NotAfter = info.NotBefore, info.NotAfter
				st.Issuer, st.Fingerprint = info.Issuer, info.Fingerprint
				st.Expired, st.ExpiresSoon = info.Expired, info.ExpiresSoon
			}
		}
		out = append(out, st)
	}
	return out
}

// due decides whether to ask the CA for this certificate now, and says
// why for the journal.
func (r *Renewer) due(ctx context.Context, cfg *model.Config, cert model.Certificate) (bool, string) {
	// The backoff is read before the files, because a certificate the CA
	// refused has no files at all and would otherwise be asked for again
	// every hour. "Issue now" does not come through here, so an operator
	// who has fixed the name does not have to wait.
	if st, err := r.Store.ReadState(cert.ID); err == nil &&
		st.LastError != "" && st.LastAttempt != nil && time.Since(*st.LastAttempt) < failureBackoff {
		return false, ""
	}
	want := r.wanted(cert)
	f, err := r.Store.Read(cert.ID)
	if err != nil {
		return true, "nothing issued yet"
	}
	if !slices.Equal(f.State.Names, want) {
		return true, "the names have changed"
	}
	now := time.Now().UTC()
	if f.State.CheckAfter != nil && now.Before(*f.State.CheckAfter) {
		// The CA asked not to be asked again yet; the window it gave
		// last time still decides.
		return f.State.RenewAfter != nil && now.After(*f.State.RenewAfter), "the renewal window has started"
	}
	account, ok := cfg.ACMEAccount(cert.Account)
	if !ok {
		return false, ""
	}
	window, err := r.Issuer.RenewalInfo(ctx, *account, f.Cert)
	if err != nil {
		if !errors.Is(err, ErrNoARI) {
			r.log().Warn("could not ask when to renew", "id", cert.ID, "err", err)
		}
		// Without a window, the last third of the lifetime is the rule.
		info, derr := certs.Describe(f.Cert)
		return derr == nil && (info.ExpiresSoon || info.Expired), "the last third of its life"
	}
	state := f.State
	start, check := window.Start, window.RetryAfter
	if check.IsZero() {
		check = now.Add(defaultCheckAfter)
	}
	state.RenewAfter, state.CheckAfter = &start, &check
	if err := r.Store.WriteState(cert.ID, state); err != nil {
		r.log().Warn("could not record the renewal window", "id", cert.ID, "err", err)
	}
	return now.After(start), "the renewal window has started"
}

// issue orders the certificate and writes what came back. A failure
// records itself and leaves the files that are there alone: an expiring
// certificate still works.
func (r *Renewer) issue(ctx context.Context, cfg *model.Config, cert model.Certificate) error {
	err := r.order(ctx, cfg, cert)
	if err == nil {
		return nil
	}
	state := certs.State{}
	if f, rerr := r.Store.Read(cert.ID); rerr == nil {
		state = f.State
	}
	now := time.Now().UTC()
	state.LastAttempt, state.LastError = &now, err.Error()
	if werr := r.Store.WriteState(cert.ID, state); werr != nil {
		r.log().Warn("could not record the failure", "id", cert.ID, "err", werr)
	}
	return err
}

func (r *Renewer) order(ctx context.Context, cfg *model.Config, cert model.Certificate) (err error) {
	// lego reads what the CA sends: a panic there is this order failing,
	// recorded like any other failure.
	defer panics.Into(&err, r.log(), "certificate order "+cert.ID)
	account, ok := cfg.ACMEAccount(cert.Account)
	if !ok {
		return fmt.Errorf("unknown ACME account %q", cert.Account)
	}
	names := r.wanted(cert)
	if len(names) == 0 {
		return errors.New("no names to ask for")
	}
	req := Request{
		Account:   *account,
		Names:     names,
		Challenge: cert.Challenge,
		KeyType:   cert.KeyType,
		Profile:   cert.EffectiveProfile(),
	}
	if cert.Challenge == model.ChallengeDNS {
		p, ok := cfg.DNSProvider(cert.Provider)
		if !ok {
			return fmt.Errorf("unknown DNS provider %q", cert.Provider)
		}
		req.Provider = p
	}
	issued, err := r.Issuer.Issue(ctx, req)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	state := certs.State{IssuedAt: now, Names: names, CertURL: issued.CertURL}
	if info, err := certs.Describe(issued.Cert); err == nil {
		state.NotAfter = info.NotAfter
	}
	return r.Store.Write(cert.ID, certs.Files{
		Cert:      issued.Cert,
		Chain:     issued.Chain,
		FullChain: issued.FullChain,
		Key:       issued.Key,
		State:     state,
	})
}

// wanted is what an order asks for: the names written out, plus whatever
// public addresses the named interfaces carry at this moment. Sorted, so
// a re-ordered list is not a changed one.
func (r *Renewer) wanted(cert model.Certificate) []string {
	var out []string
	for _, n := range cert.Names {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	for _, iface := range cert.InterfaceAddresses {
		if r.Addresses == nil {
			break
		}
		for _, addr := range r.Addresses(iface) {
			if Public(addr) && !slices.Contains(out, addr) {
				out = append(out, addr)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Public reports whether an address is one a CA would issue for: a
// global unicast address that is not private, unique local or carrier
// grade NAT.
func Public(addr string) bool {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	switch {
	case !ip.IsGlobalUnicast(), ip.IsPrivate(), ip.IsLoopback(), ip.IsLinkLocalUnicast():
		return false
	case ip.Is4() && cgnat.Contains(ip):
		return false
	}
	return true
}

// cgnat is the space an ISP hands out behind its own NAT, which reaches
// nothing from outside.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func (r *Renewer) claim(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running[id] {
		return false
	}
	if r.running == nil {
		r.running = map[string]bool{}
	}
	r.running[id] = true
	return true
}

func (r *Renewer) release(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.running, id)
}

func (r *Renewer) isRunning(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running[id]
}

func (r *Renewer) config() *model.Config {
	if r.Config == nil {
		return &model.Config{}
	}
	if cfg := r.Config(); cfg != nil {
		return cfg
	}
	return &model.Config{}
}

func (r *Renewer) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}
