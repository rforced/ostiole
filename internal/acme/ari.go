package acme

import (
	"context"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// certID names a certificate to the CA's renewal information (RFC 9773
// §4.1): its authority key identifier and its serial, each base64url,
// joined by a dot.
func certID(leaf *x509.Certificate) (string, error) {
	if len(leaf.AuthorityKeyId) == 0 {
		return "", errors.New("the certificate has no authority key identifier")
	}
	der, err := asn1.Marshal(leaf.SerialNumber)
	if err != nil {
		return "", err
	}
	// The serial's own bytes, without the INTEGER tag and length; a
	// serial is 20 bytes at most, so the length is one byte.
	if len(der) < 3 {
		return "", errors.New("the certificate's serial number is empty")
	}
	return b64(leaf.AuthorityKeyId) + "." + b64(der[2:]), nil
}

// RenewalInfo asks the CA when it would like this certificate renewed. It
// needs the CA's directory, not the account.
func (c *Client) RenewalInfo(ctx context.Context, account model.ACMEAccount, leaf []byte) (*Window, error) {
	cert, err := parseLeaf(leaf)
	if err != nil {
		return nil, err
	}
	id, err := certID(cert)
	if err != nil {
		return nil, err
	}
	cn, err := c.dial(ctx, account.Directory, account.CACert, nil)
	if err != nil {
		return nil, err
	}
	if cn.dir.RenewalInfo == "" {
		return nil, ErrNoARI
	}
	a, err := cn.send(ctx, http.MethodGet, strings.TrimSuffix(cn.dir.RenewalInfo, "/")+"/"+id, nil)
	if err != nil {
		return nil, fmt.Errorf("asking when to renew: %w", err)
	}
	if a.status != http.StatusOK {
		return nil, fmt.Errorf("asking when to renew: %w", problemIn(a))
	}
	var info struct {
		Window struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		} `json:"suggestedWindow"`
	}
	if err := a.decode(&info); err != nil {
		return nil, err
	}
	if !info.Window.End.After(info.Window.Start) {
		return nil, fmt.Errorf("the CA's renewal window ends at %s, before it starts", info.Window.End)
	}
	w := &Window{Start: info.Window.Start.UTC(), End: info.Window.End.UTC()}
	if ra := a.retryAfter(); ra > 0 {
		w.RetryAfter = time.Now().UTC().Add(ra)
	}
	return w, nil
}
