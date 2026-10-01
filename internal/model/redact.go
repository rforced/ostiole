package model

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode"
)

// Redacted returns a copy with every secret blanked: what a backup for
// sharing carries. This function is the one place the secrets in a
// configuration are enumerated, and redact_test.go fails when a new one
// is added without being listed here.
func (c *Config) Redacted() *Config {
	if c == nil {
		return nil
	}
	out := c.clone()
	if out == nil {
		return nil
	}
	for i := range out.Interfaces {
		in := &out.Interfaces[i]
		if in.WireGuard != nil {
			in.WireGuard.PrivateKey = ""
			for j := range in.WireGuard.Peers {
				in.WireGuard.Peers[j].PresharedKey = ""
			}
		}
		if in.Wireless != nil {
			in.Wireless.Passphrase = ""
		}
		if in.PPPoE != nil {
			in.PPPoE.Password = ""
		}
	}
	for i := range out.Crons {
		out.Crons[i].Passphrase = ""
	}
	out.Backup.Remote.Secret = ""
	out.Backup.Remote.Passphrase = ""
	// The key id names the account the bucket belongs to, which a shared
	// backup has no business carrying either.
	out.Backup.Remote.KeyID = ""
	for i := range out.ACME.Accounts {
		out.ACME.Accounts[i].PrivateKey = ""
		out.ACME.Accounts[i].EABHMAC = ""
	}
	for i := range out.DNSProviders {
		p := &out.DNSProviders[i]
		kind, known := ProviderKindOf(p.Kind)
		for key := range p.Settings {
			// An unknown kind is one this build cannot read, so every
			// setting it holds is treated as a secret. The keys stay so
			// that whoever restores the backup can see what to fill in.
			if f, _ := kind.Field(key); !known || f.Secret {
				p.Settings[key] = ""
			}
		}
	}
	for i := range out.Certificates {
		out.Certificates[i].KeyPEM = ""
	}
	out.Notifications.Email.Password = ""
	out.Notifications.Webhook.Token = ""
	// A chat service's webhook URL is the key to its channel.
	out.Notifications.Webhook.URL = ""
	for i := range out.Aliases {
		out.Aliases[i].URL = RedactURL(out.Aliases[i].URL)
	}
	for i := range out.Blocking.Lists {
		out.Blocking.Lists[i].URL = RedactURL(out.Blocking.Lists[i].URL)
	}
	s := &out.System
	for _, u := range []*string{&s.GeoIPv4URL, &s.GeoIPv6URL, &s.ASNURL, &s.ASNNamesURL, &s.BogonV4URL, &s.BogonV6URL} {
		*u = RedactURL(*u)
	}
	return out
}

// RedactURL blanks what in a URL is a credential: its user part, and the
// value of each query parameter named like a key or a token, the way a
// private list or a GeoIP licence carries one. The rest is left exactly
// as written, so a template keeps its {country}.
func RedactURL(raw string) string {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return raw
	}
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	if at := strings.LastIndex(rest[:end], "@"); at >= 0 {
		rest = rest[at+1:]
	}
	out, fragment, hasFragment := strings.Cut(rest, "#")
	out, query, hasQuery := strings.Cut(out, "?")
	if hasQuery {
		pairs := strings.Split(query, "&")
		for i, p := range pairs {
			name, _, ok := strings.Cut(p, "=")
			if n, err := url.QueryUnescape(name); ok && err == nil && secretParam(n) {
				pairs[i] = name + "="
			}
		}
		out += "?" + strings.Join(pairs, "&")
	}
	if hasFragment {
		out += "#" + fragment
	}
	return scheme + "://" + out
}

// secretParam says whether a query parameter is named like a credential.
// Hiding one that was not costs a viewer little.
func secretParam(name string) bool {
	n := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, name)
	for _, part := range []string{"key", "token", "secret", "pass", "pwd", "auth", "sig", "credential", "licen", "session"} {
		if strings.Contains(n, part) {
			return true
		}
	}
	return false
}

// clone deep-copies through JSON, which is the shape the configuration is
// defined in anyway: anything that survives a round trip to disk survives
// this.
func (c *Config) clone() *Config {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil
	}
	var out Config
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return &out
}
