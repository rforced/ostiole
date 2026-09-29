package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// validateTimeout bounds the wait for the CA to check one challenge.
const validateTimeout = 5 * time.Minute

// finalizeTimeout bounds the wait for the certificate once the CSR is in.
const finalizeTimeout = 2 * time.Minute

// cleanupTimeout bounds taking the answers down, which happens even when
// the order's own context has run out.
const cleanupTimeout = 2 * time.Minute

// identifier is what a certificate names (RFC 8555 §9.7.7, RFC 8738).
type identifier struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// order is an order as the CA keeps it.
type order struct {
	Status         string       `json:"status"`
	Identifiers    []identifier `json:"identifiers"`
	Authorizations []string     `json:"authorizations"`
	Finalize       string       `json:"finalize"`
	Certificate    string       `json:"certificate"`
	Error          *Problem     `json:"error"`
	url            string
}

// authorization is the CA's check of one identifier.
type authorization struct {
	Status     string      `json:"status"`
	Identifier identifier  `json:"identifier"`
	Wildcard   bool        `json:"wildcard"`
	Challenges []challenge `json:"challenges"`
	url        string
}

// challenge is one way the CA offers to check an identifier.
type challenge struct {
	Type   string   `json:"type"`
	URL    string   `json:"url"`
	Token  string   `json:"token"`
	Status string   `json:"status"`
	Error  *Problem `json:"error"`
}

// name is the identifier as a certificate names it, wildcard included.
func (a *authorization) name() string {
	if a.Wildcard {
		return "*." + a.Identifier.Value
	}
	return a.Identifier.Value
}

// pending is one challenge being answered.
type pending struct {
	authz   *authorization
	chal    challenge
	keyAuth string
	// state is the challenger's own, such as the record dns-01 wrote.
	state any
}

// challenger answers one kind of challenge.
type challenger interface {
	// kind is the challenge type, "http-01" or "dns-01".
	kind() string
	// present puts the answer to one challenge up.
	present(ctx context.Context, p *pending) error
	// ready waits until the CA can see the answer.
	ready(ctx context.Context, p *pending) error
	// remove takes the answer down.
	remove(ctx context.Context, p *pending) error
	// together puts every answer up before the first is checked, so each
	// has the others' time to spread.
	together() bool
	// apart, when set, answers one challenge at a time, this far apart.
	apart() time.Duration
}

// identifiers are the names as an order asks for them: addresses as ip
// identifiers in their usual form, names lowercase.
func identifiers(names []string) []identifier {
	var out []identifier
	for _, n := range names {
		id := identifier{Type: "dns", Value: strings.ToLower(strings.TrimSpace(n))}
		if ip, err := netip.ParseAddr(strings.TrimSpace(n)); err == nil {
			id = identifier{Type: "ip", Value: ip.String()}
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// place orders one certificate and sees it through: the challenges, the
// CSR, the certificate. A failure before the CSR deactivates what the
// order did not get validated.
func (c *Client) place(ctx context.Context, cn *conn, req Request, ch challenger) (*Issued, error) {
	ids := identifiers(req.Names)
	if len(ids) == 0 {
		return nil, errors.New("no names to ask for")
	}
	o, err := c.newOrder(ctx, cn, req, ids)
	if err != nil {
		return nil, err
	}
	c.log().Info("certificate order placed", "names", req.Names, "order", o.url)
	authzs, err := c.authorizations(ctx, cn, o)
	if err == nil {
		err = c.solve(ctx, cn, ch, authzs)
	}
	if err != nil {
		c.deactivate(ctx, cn, o)
		return nil, err
	}
	return c.finalize(ctx, cn, req, o, ids)
}

func (c *Client) newOrder(ctx context.Context, cn *conn, req Request, ids []identifier) (*order, error) {
	body := struct {
		Identifiers []identifier `json:"identifiers"`
		Profile     string       `json:"profile,omitempty"`
		Replaces    string       `json:"replaces,omitempty"`
	}{Identifiers: ids, Profile: req.Profile}
	if cn.dir.RenewalInfo != "" && len(req.Replaces) > 0 {
		if leaf, err := parseLeaf(req.Replaces); err == nil {
			body.Replaces, _ = certID(leaf)
		}
	}
	a, err := cn.postJSON(ctx, cn.dir.NewOrder, body)
	// A certificate the CA has already seen replaced is renewed plainly,
	// and so is one another account ordered: Let's Encrypt lets only the
	// account that ordered a certificate replace it, so a certificate
	// moved to another account would never renew.
	if (is(err, "alreadyReplaced") || is(err, "unauthorized")) && body.Replaces != "" {
		body.Replaces = ""
		a, err = cn.postJSON(ctx, cn.dir.NewOrder, body)
	}
	if err != nil {
		return nil, fmt.Errorf("placing the order: %w", err)
	}
	o := &order{url: a.header.Get("Location")}
	if err := a.decode(o); err != nil {
		return nil, err
	}
	if o.url == "" {
		return nil, errors.New("the CA gave the order no URL")
	}
	sortIDs := func(s []identifier) []identifier {
		out := slices.Clone(s)
		slices.SortFunc(out, func(a, b identifier) int { return strings.Compare(a.Type+" "+a.Value, b.Type+" "+b.Value) })
		return out
	}
	if !slices.Equal(sortIDs(ids), sortIDs(o.Identifiers)) {
		return nil, fmt.Errorf("the CA changed the names asked for: %v, not %v", o.Identifiers, ids)
	}
	return o, nil
}

// authorizations reads each of the order's authorizations.
func (c *Client) authorizations(ctx context.Context, cn *conn, o *order) ([]*authorization, error) {
	var out []*authorization
	for _, url := range o.Authorizations {
		az, _, err := c.authorization(ctx, cn, url)
		if err != nil {
			return nil, err
		}
		out = append(out, az)
	}
	return out, nil
}

func (c *Client) authorization(ctx context.Context, cn *conn, url string) (*authorization, *answer, error) {
	a, err := cn.post(ctx, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("reading an authorization: %w", err)
	}
	az := &authorization{url: url}
	if err := a.decode(az); err != nil {
		return nil, nil, err
	}
	return az, a, nil
}

// solve answers each authorization not yet valid. dns-01's records all go
// up before the first is checked, and come down at the
// end whatever happened; http-01's answers, and those of a provider that
// takes one at a time, go up, get checked and come down in turn.
func (c *Client) solve(ctx context.Context, cn *conn, ch challenger, authzs []*authorization) error {
	var work []*pending
	for _, az := range authzs {
		if az.Status == "valid" {
			continue
		}
		i := slices.IndexFunc(az.Challenges, func(chal challenge) bool { return chal.Type == ch.kind() })
		if i < 0 {
			return fmt.Errorf("the CA offers no %s challenge for %s", ch.kind(), az.name())
		}
		chal := az.Challenges[i]
		work = append(work, &pending{authz: az, chal: chal, keyAuth: cn.signer.keyAuthorization(chal.Token)})
	}
	if ch.together() && ch.apart() == 0 {
		var up []*pending
		defer func() {
			for _, p := range up {
				c.takeDown(ctx, ch, p)
			}
		}()
		for _, p := range work {
			if err := ch.present(ctx, p); err != nil {
				return fmt.Errorf("answering %s: %w", p.authz.name(), err)
			}
			up = append(up, p)
			c.log().Info("certificate challenge presented", "name", p.authz.name(), "type", ch.kind())
		}
		for _, p := range work {
			if err := c.check(ctx, cn, ch, p); err != nil {
				return err
			}
		}
		return nil
	}
	for i, p := range work {
		if i > 0 && ch.apart() > 0 {
			if err := sleep(ctx, ch.apart()); err != nil {
				return err
			}
		}
		if err := c.one(ctx, cn, ch, p); err != nil {
			return err
		}
	}
	return nil
}

// one puts one answer up, has it checked and takes it down.
func (c *Client) one(ctx context.Context, cn *conn, ch challenger, p *pending) error {
	if err := ch.present(ctx, p); err != nil {
		return fmt.Errorf("answering %s: %w", p.authz.name(), err)
	}
	defer c.takeDown(ctx, ch, p)
	c.log().Info("certificate challenge presented", "name", p.authz.name(), "type", ch.kind())
	return c.check(ctx, cn, ch, p)
}

// check waits until the answer can be seen, then asks the CA to check it
// and waits for the verdict.
func (c *Client) check(ctx context.Context, cn *conn, ch challenger, p *pending) error {
	name := p.authz.name()
	if err := ch.ready(ctx, p); err != nil {
		return fmt.Errorf("answering %s: %w", name, err)
	}
	a, err := cn.post(ctx, p.chal.URL, []byte("{}"))
	if err != nil {
		return fmt.Errorf("asking the CA to check %s: %w", name, err)
	}
	var chal challenge
	if err := a.decode(&chal); err != nil {
		return err
	}
	give := time.Now().Add(validateTimeout)
	// The first look is at once: a CA that checks quickly has often
	// finished by the time it answers.
	wait := time.Duration(0)
	for {
		switch chal.Status {
		case "valid":
			c.log().Info("certificate challenge validated", "name", name)
			return nil
		case "invalid":
			if chal.Error != nil {
				return fmt.Errorf("the CA could not check %s: %w", name, chal.Error)
			}
			return fmt.Errorf("the CA could not check %s", name)
		}
		if ra := a.retryAfter(); ra > 0 {
			wait = min(ra, time.Minute)
		}
		if time.Now().Add(wait).After(give) {
			return fmt.Errorf("the CA had not checked %s after %s", name, validateTimeout)
		}
		if err := sleep(ctx, wait); err != nil {
			return err
		}
		wait = max(min(2*wait, 10*time.Second), time.Second)
		az, got, err := c.authorization(ctx, cn, p.authz.url)
		if err != nil {
			return err
		}
		a = got
		switch az.Status {
		case "valid":
			chal.Status = "valid"
		case "invalid":
			chal.Status = "invalid"
			if i := slices.IndexFunc(az.Challenges, func(x challenge) bool { return x.URL == p.chal.URL }); i >= 0 {
				chal.Error = az.Challenges[i].Error
			}
		case "pending", "processing":
		default:
			return fmt.Errorf("the CA's authorization for %s is %s", name, az.Status)
		}
	}
}

// takeDown removes an answer, even after the order's context has ended; a
// failure is logged, as the order's outcome does not depend on it.
func (c *Client) takeDown(ctx context.Context, ch challenger, p *pending) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := ch.remove(ctx, p); err != nil {
		c.log().Warn("could not take a certificate challenge down", "name", p.authz.name(), "err", err)
	}
}

// deactivate gives up the order's authorizations that are not valid, so
// the CA does not hold them for the next order.
func (c *Client) deactivate(ctx context.Context, cn *conn, o *order) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	for _, url := range o.Authorizations {
		az, _, err := c.authorization(ctx, cn, url)
		if err != nil || az.Status == "valid" {
			continue
		}
		if _, err := cn.postJSON(ctx, url, map[string]string{"status": "deactivated"}); err != nil {
			c.log().Warn("could not deactivate a certificate authorization", "name", az.name(), "err", err)
		}
	}
}

// finalize sends the CSR for a new key and fetches the certificate.
func (c *Client) finalize(ctx context.Context, cn *conn, req Request, o *order, ids []identifier) (*Issued, error) {
	key, keyPEM, err := newKey(req.KeyType)
	if err != nil {
		return nil, err
	}
	csr, err := makeCSR(key, ids)
	if err != nil {
		return nil, err
	}
	a, err := cn.postJSON(ctx, o.Finalize, map[string]string{"csr": b64(csr)})
	if err != nil {
		return nil, fmt.Errorf("sending the CSR: %w", err)
	}
	if err := a.decode(o); err != nil {
		return nil, err
	}
	give := time.Now().Add(finalizeTimeout)
	wait := 500 * time.Millisecond
	for o.Status != "valid" {
		if o.Status == "invalid" {
			if o.Error != nil {
				return nil, fmt.Errorf("the CA refused the order: %w", o.Error)
			}
			return nil, errors.New("the CA refused the order")
		}
		if ra := a.retryAfter(); ra > 0 {
			wait = min(ra, 10*time.Second)
		}
		if time.Now().Add(wait).After(give) {
			return nil, fmt.Errorf("the CA had not issued the certificate after %s", finalizeTimeout)
		}
		if err := sleep(ctx, wait); err != nil {
			return nil, err
		}
		wait = min(2*wait, 10*time.Second)
		if a, err = cn.post(ctx, o.url, nil); err != nil {
			return nil, fmt.Errorf("reading the order: %w", err)
		}
		if err := a.decode(o); err != nil {
			return nil, err
		}
	}
	chain, err := cn.post(ctx, o.Certificate, nil)
	if err != nil {
		return nil, fmt.Errorf("fetching the certificate: %w", err)
	}
	leaf, rest := splitLeaf(chain.body)
	cert, err := parseLeaf(leaf)
	if err != nil {
		return nil, fmt.Errorf("the CA's certificate: %w", err)
	}
	if !samePublicKey(cert.PublicKey, key.Public()) {
		return nil, errors.New("the CA's certificate is not for the key the order sent")
	}
	c.log().Info("certificate issued", "names", req.Names, "notAfter", cert.NotAfter)
	return &Issued{Cert: leaf, Chain: rest, FullChain: chain.body, Key: keyPEM, CertURL: o.Certificate}, nil
}

func samePublicKey(a, b crypto.PublicKey) bool {
	k, ok := a.(interface{ Equal(crypto.PublicKey) bool })
	return ok && k.Equal(b)
}

// newKey makes the certificate's key, written as the store has always held
// keys: SEC 1 for EC, PKCS #1 for RSA.
func newKey(keyType string) (crypto.Signer, []byte, error) {
	var block *pem.Block
	var key crypto.Signer
	switch keyType {
	case "rsa2048", "rsa4096":
		bits := 2048
		if keyType == "rsa4096" {
			bits = 4096
		}
		k, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			return nil, nil, err
		}
		key, block = k, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}
	default:
		curve := elliptic.P256()
		if keyType == "ec384" {
			curve = elliptic.P384()
		}
		k, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return nil, nil, err
		}
		key, block = k, &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}
	}
	return key, pem.EncodeToMemory(block), nil
}

// makeCSR asks for the order's names. The first name is the common name
// unless an address is among them: a CA refuses an
// address there, and a name longer than 64 characters does not fit.
func makeCSR(key crypto.Signer, ids []identifier) ([]byte, error) {
	var tmpl x509.CertificateRequest
	for _, id := range ids {
		if id.Type == "ip" {
			ip, err := netip.ParseAddr(id.Value)
			if err != nil {
				return nil, err
			}
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip.AsSlice())
			continue
		}
		tmpl.DNSNames = append(tmpl.DNSNames, id.Value)
	}
	if len(tmpl.IPAddresses) == 0 && len(ids[0].Value) <= 64 {
		tmpl.Subject = pkix.Name{CommonName: ids[0].Value}
	}
	return x509.CreateCertificateRequest(rand.Reader, &tmpl, key)
}

// sleep waits for d or the context, whichever ends first.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
