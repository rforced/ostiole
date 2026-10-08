package acme

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"ostiole/internal/atomicfile"
	"ostiole/internal/model"
)

// account finds the account the key belongs to, or creates it, and signs
// with its URL from then on. Saving an account in the dialog is what
// agrees to the CA's terms; the dialog says so.
func (c *Client) account(ctx context.Context, cn *conn, a model.ACMEAccount) error {
	if uri := c.cached(a); uri != "" {
		cn.kid = uri
		return nil
	}
	found, err := cn.postJSON(ctx, cn.dir.NewAccount, map[string]bool{"onlyReturnExisting": true})
	if err == nil {
		return c.adopt(cn, a, found)
	}
	// Only a CA saying the account is not there is a reason to make one;
	// any other refusal is the answer.
	if !is(err, "accountDoesNotExist") {
		return fmt.Errorf("looking the account up: %w", err)
	}
	req := struct {
		Terms   bool            `json:"termsOfServiceAgreed"`
		Contact []string        `json:"contact,omitempty"`
		Binding json.RawMessage `json:"externalAccountBinding,omitempty"`
	}{Terms: true}
	if a.Email != "" {
		req.Contact = []string{"mailto:" + a.Email}
	}
	if a.EABKeyID != "" {
		mac, err := decodeMAC(a.EABHMAC)
		if err != nil {
			return err
		}
		if req.Binding, err = cn.signer.bind(cn.dir.NewAccount, a.EABKeyID, mac); err != nil {
			return err
		}
	}
	created, err := cn.postJSON(ctx, cn.dir.NewAccount, req)
	// A conflict is an account that is there after all.
	if err != nil && (created == nil || created.status != http.StatusConflict) {
		return fmt.Errorf("creating the account: %w", err)
	}
	return c.adopt(cn, a, created)
}

// adopt signs with the account URL the CA gave, and keeps it.
func (c *Client) adopt(cn *conn, a model.ACMEAccount, found *answer) error {
	uri := found.header.Get("Location")
	if uri == "" {
		return errors.New("the CA gave the account no URL")
	}
	cn.kid = uri
	c.cache(a, uri)
	return nil
}

// decodeMAC reads the MAC key of an external account binding, which CAs
// hand out in base64url with or without padding.
func decodeMAC(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if mac, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "=")); err == nil && len(mac) > 0 {
		return mac, nil
	}
	return nil, errors.New("the external account binding's MAC key is not base64url")
}

// cachedRegistration is the account URL a key resolved to, kept so that
// an hourly pass does not ask the CA who it is every time.
type cachedRegistration struct {
	Key string `json:"key"`
	URI string `json:"uri"`
}

func (c *Client) cached(a model.ACMEAccount) string {
	if c.AccountsDir == "" {
		return ""
	}
	raw, err := os.ReadFile(c.accountPath(a.ID))
	if err != nil {
		return ""
	}
	var got cachedRegistration
	if err := json.Unmarshal(raw, &got); err != nil || got.URI == "" {
		return ""
	}
	// A new key is a new account, whatever the file says.
	if got.Key != fingerprint(a.PrivateKey) {
		_ = os.Remove(c.accountPath(a.ID))
		return ""
	}
	return got.URI
}

func (c *Client) cache(a model.ACMEAccount, uri string) {
	if c.AccountsDir == "" || uri == "" {
		return
	}
	raw, err := json.Marshal(cachedRegistration{Key: fingerprint(a.PrivateKey), URI: uri})
	if err != nil {
		return
	}
	if err := os.MkdirAll(c.AccountsDir, 0o700); err != nil {
		return
	}
	_ = atomicfile.Write(c.accountPath(a.ID), raw, 0o600)
}

func (c *Client) accountPath(id string) string {
	return filepath.Join(c.AccountsDir, id+".json")
}
