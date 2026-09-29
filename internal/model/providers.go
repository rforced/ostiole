package model

import "strings"

// DNSProvider is one DNS service this router writes records to: the
// credentials, and the domains the service holds for it.
type DNSProvider struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	// Kind is one of ProviderKinds.
	Kind string `json:"kind"`
	// Settings are the kind's fields, by name.
	Settings map[string]string `json:"settings,omitempty"`
	// Domains are the zones this provider writes, as it names them. A
	// name under one of them is written through this provider unless
	// something names another.
	Domains []string `json:"domains,omitempty"`
	// PropagationSeconds is how long to wait before the CA looks; 0 is
	// the kind's own default.
	PropagationSeconds int `json:"propagationSeconds,omitempty"`
}

// DNSProvider returns the provider with the given id.
func (c *Config) DNSProvider(id string) (*DNSProvider, bool) {
	for i := range c.DNSProviders {
		if c.DNSProviders[i].ID == id {
			return &c.DNSProviders[i], true
		}
	}
	return nil, false
}

// ProviderFor returns the provider holding the longest domain that is
// name or above it, and that domain. A wildcard belongs where its parent
// does. Names compare as NormalizeDomain writes them.
func (c *Config) ProviderFor(name string) (*DNSProvider, string, bool) {
	name = NormalizeDomain(strings.TrimPrefix(strings.TrimSpace(name), "*."))
	var found *DNSProvider
	zone := ""
	for i := range c.DNSProviders {
		for _, d := range c.DNSProviders[i].Domains {
			d = NormalizeDomain(d)
			if d == "" || len(d) <= len(zone) {
				continue
			}
			if name == d || strings.HasSuffix(name, "."+d) {
				found, zone = &c.DNSProviders[i], d
			}
		}
	}
	return found, zone, found != nil
}

// ProviderField is one setting a DNS provider needs.
type ProviderField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret,omitempty"`
	Required bool   `json:"required,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

// ProviderKind is one DNS service a dns-01 challenge can write to.
type ProviderKind struct {
	Kind   string          `json:"kind"`
	Label  string          `json:"label"`
	Fields []ProviderField `json:"fields"`
	// DynamicDNS marks the kinds that can keep a dynamic DNS record:
	// their clients in internal/dnsprovider keep address records.
	DynamicDNS bool `json:"dynamicDns,omitempty"`
}

// ProviderExec is the kind that runs a program to write a record, as root.
const ProviderExec = "exec"

// ProviderKinds are the DNS services compiled into this binary, the
// dialog's default first. Adding one is a client in internal/dnsprovider
// and a row here; the validation, the redaction and the dialog all read
// this table.
var ProviderKinds = []ProviderKind{
	{Kind: "rfc2136", Label: "RFC 2136 dynamic update", Fields: []ProviderField{
		{Key: "nameserver", Label: "Nameserver", Required: true, Hint: "host:port"},
		{Key: "tsigKey", Label: "TSIG key name", Required: true},
		{Key: "tsigSecret", Label: "TSIG secret", Secret: true, Required: true},
		{Key: "tsigAlgorithm", Label: "TSIG algorithm", Hint: "hmac-sha256."},
	}},
	{Kind: "cloudflare", Label: "Cloudflare", DynamicDNS: true, Fields: []ProviderField{
		{Key: "token", Label: "API token", Secret: true, Required: true, Hint: "A token with Zone:DNS:Edit."},
		{Key: "zoneToken", Label: "Zone token", Secret: true, Hint: "Only when the first token cannot read zones."},
	}},
	{Kind: ProviderExec, Label: "Program", Fields: []ProviderField{
		{Key: "program", Label: "Program", Required: true, Hint: "Run as root with present or cleanup, the record's name and its value."},
		{Key: "mode", Label: "Mode", Hint: "RAW passes --, the domain, the token and the key authorisation instead."},
	}},
	{Kind: "hetzner", Label: "Hetzner", Fields: []ProviderField{
		{Key: "token", Label: "API token", Secret: true, Required: true},
	}},
	{Kind: "porkbun", Label: "Porkbun", Fields: []ProviderField{
		{Key: "apiKey", Label: "API key", Secret: true, Required: true},
		{Key: "secretKey", Label: "Secret key", Secret: true, Required: true},
	}},
	{Kind: "route53", Label: "Amazon Route 53", Fields: []ProviderField{
		{Key: "accessKeyId", Label: "Access key ID", Required: true},
		{Key: "secretAccessKey", Label: "Secret access key", Secret: true, Required: true},
		{Key: "region", Label: "Region", Hint: "us-east-1."},
		{Key: "hostedZoneId", Label: "Hosted zone ID", Hint: "Found when empty."},
	}},
}

// ProviderKindOf returns the kind with the given name.
func ProviderKindOf(kind string) (ProviderKind, bool) {
	for _, k := range ProviderKinds {
		if k.Kind == kind {
			return k, true
		}
	}
	return ProviderKind{}, false
}

// DynamicDNSKinds names the kinds that can keep a dynamic DNS record, for
// an error that says which to use.
func DynamicDNSKinds() string {
	var out []string
	for _, k := range ProviderKinds {
		if k.DynamicDNS {
			out = append(out, k.Label)
		}
	}
	return strings.Join(out, ", ")
}

// Field returns the named field of this kind.
func (k ProviderKind) Field(key string) (ProviderField, bool) {
	for _, f := range k.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return ProviderField{}, false
}
