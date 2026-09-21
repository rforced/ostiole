package model

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
}

// ProviderKinds are the DNS services compiled into this binary. Adding
// one is an import in internal/acme and a row here; the validation, the
// redaction and the dialog all read this table.
var ProviderKinds = []ProviderKind{
	{Kind: "cloudflare", Label: "Cloudflare", Fields: []ProviderField{
		{Key: "token", Label: "API token", Secret: true, Required: true, Hint: "A token with Zone:DNS:Edit."},
		{Key: "zoneToken", Label: "Zone token", Secret: true, Hint: "Only when the first token cannot read zones."},
	}},
	{Kind: "desec", Label: "deSEC", Fields: []ProviderField{
		{Key: "token", Label: "API token", Secret: true, Required: true},
	}},
	{Kind: "rfc2136", Label: "RFC 2136 dynamic update", Fields: []ProviderField{
		{Key: "nameserver", Label: "Nameserver", Required: true, Hint: "host:port"},
		{Key: "tsigKey", Label: "TSIG key name", Required: true},
		{Key: "tsigSecret", Label: "TSIG secret", Secret: true, Required: true},
		{Key: "tsigAlgorithm", Label: "TSIG algorithm", Hint: "hmac-sha256."},
	}},
	{Kind: "exec", Label: "Program", Fields: []ProviderField{
		{Key: "program", Label: "Program", Required: true, Hint: "Run as root with present or cleanup, the name, the token and the key authorisation."},
		{Key: "mode", Label: "Mode", Hint: "Empty, or RAW to receive the record instead."},
	}},
	{Kind: "digitalocean", Label: "DigitalOcean", Fields: []ProviderField{
		{Key: "token", Label: "API token", Secret: true, Required: true},
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

// Field returns the named field of this kind.
func (k ProviderKind) Field(key string) (ProviderField, bool) {
	for _, f := range k.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return ProviderField{}, false
}
