package model

import "encoding/json"

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
	for i := range out.ACME.Providers {
		p := &out.ACME.Providers[i]
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
	return out
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
