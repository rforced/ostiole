package model

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
)

// accountKey mints a throwaway P-256 key. It is generated rather than
// written out so that no private key is in the repository at all: the
// validator parses this one and nothing signs with it.
func accountKey() string {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

func certConfig() *Config {
	return &Config{
		Version: SchemaVersion,
		Zones:   []Zone{{Name: "wan", External: true}, {Name: "lan"}},
		Interfaces: []Interface{
			{Name: "wan0", Zone: "wan", Enabled: true, IPv4: IPv4{Mode: AddrDHCP}, IPv6: IPv6{Mode: AddrSLAAC}},
			{Name: "lan0", Zone: "lan", Enabled: true,
				IPv4: IPv4{Mode: AddrStatic, Address: "192.168.1.1/24"}, IPv6: IPv6{Mode: AddrNone}},
		},
		NAT: NAT{Outbound: OutboundNAT{Mode: OutboundAutomatic}},
		ACME: ACME{
			Accounts: []ACMEAccount{{ID: "le", Directory: ACMEDirectoryLetsEncryptStaging, PrivateKey: accountKey()}},
			Providers: []DNSProvider{
				{ID: "dns", Kind: "exec", Settings: map[string]string{"program": "/bin/true"}},
			},
		},
	}
}

func issuesByPath(t *testing.T, cfg *Config) map[string]string {
	t.Helper()
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type %T", err)
	}
	got := map[string]string{}
	for _, i := range ve.Issues {
		got[i.Path] = i.Message
	}
	return got
}

func TestValidateAcceptsCertificates(t *testing.T) {
	t.Parallel()
	cfg := certConfig()
	cfg.Certificates = []Certificate{
		{ID: "wildcard", Enabled: true, Source: SourceACME, Names: []string{"*.example.test", "example.test"},
			Account: "le", Challenge: ChallengeDNS, Provider: "dns", KeyType: "ec384"},
		{ID: "wan-address", Enabled: true, Source: SourceACME, InterfaceAddresses: []string{"wan0"},
			Account: "le", Challenge: ChallengeHTTP, Profile: ProfileShortlived},
	}
	cfg.System.Management.Certificate = "wildcard"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("certificates rejected: %v", err)
	}
}

func TestValidateCatchesCertificateMistakes(t *testing.T) {
	t.Parallel()
	cfg := certConfig()
	cfg.ACME.Accounts = append(cfg.ACME.Accounts,
		ACMEAccount{ID: "broken", Directory: "acme.example.test", Email: "nobody", EABKeyID: "k", PrivateKey: "not a key"})
	cfg.ACME.Providers = append(cfg.ACME.Providers,
		DNSProvider{ID: "cf", Kind: "cloudflare", Settings: map[string]string{"apiKey": "x"}, PropagationSeconds: 900},
		DNSProvider{ID: "nope", Kind: "invented"})
	cfg.Certificates = []Certificate{
		// A wildcard cannot go over http-01, and an uploaded certificate
		// has no PEM in it.
		{ID: "Wild Card", Enabled: true, Source: SourceACME, Names: []string{"*.example.test"},
			Account: "missing", Challenge: ChallengeHTTP, Provider: "gone", KeyType: "ed25519", Profile: "quick"},
		// An address is http-01 and shortlived, whatever it says here.
		{ID: "addr", Enabled: true, Source: SourceACME, Names: []string{"192.0.2.1"},
			Account: "le", Challenge: ChallengeDNS, Provider: "dns", Profile: "classic"},
		{ID: "upload", Enabled: true, Source: SourceUploaded, Names: []string{"example.test"}},
		{ID: "elsewhere", Enabled: true, Source: "magic"},
	}
	cfg.System.Management.Certificate = "gone"

	got := issuesByPath(t, cfg)
	for _, p := range []string{
		"acme.accounts[1].directory",
		"acme.accounts[1].email",
		"acme.accounts[1].eabKeyId",
		"acme.accounts[1].privateKey",
		"acme.providers[1].settings.token",
		"acme.providers[1].settings.apiKey",
		"acme.providers[1].propagationSeconds",
		"acme.providers[2].kind",
		"certificates[0].id",
		"certificates[0].challenge",
		"certificates[0].account",
		"certificates[0].keyType",
		"certificates[0].profile",
		"certificates[1].challenge",
		"certificates[1].profile",
		"certificates[2].certPem",
		"certificates[2].names",
		"certificates[3].source",
		"system.management.certificate",
	} {
		if _, ok := got[p]; !ok {
			t.Errorf("missing issue at %s (got %v)", p, keysOf(got))
		}
	}
	if msg := got["certificates[0].challenge"]; !strings.Contains(msg, string(ChallengeDNS)) {
		t.Errorf("wildcard challenge message = %q, want it to name dns-01", msg)
	}
}

func TestValidateCertificateInterfaces(t *testing.T) {
	t.Parallel()
	cfg := certConfig()
	cfg.Certificates = []Certificate{
		{ID: "inside", Enabled: true, Source: SourceACME, InterfaceAddresses: []string{"lan0", "eth9"},
			Account: "le", Challenge: ChallengeHTTP},
	}
	got := issuesByPath(t, cfg)
	for _, p := range []string{
		"certificates[0].interfaceAddresses[0]",
		"certificates[0].interfaceAddresses[1]",
	} {
		if _, ok := got[p]; !ok {
			t.Errorf("missing issue at %s (got %v)", p, keysOf(got))
		}
	}
}

// A forward that has taken port 80 leaves nothing for the challenge to
// answer with, so the certificate is refused rather than failing at the
// CA every hour.
func TestValidateRefusesHTTP01BehindAPortForward(t *testing.T) {
	t.Parallel()
	cfg := certConfig()
	cfg.NAT.PortForwards = []PortForward{{
		ID: "web", Enabled: true, Zone: "wan", Protocol: ProtocolTCP,
		Ports: []string{"79-81"}, Target: "192.168.1.10",
	}}
	cfg.Certificates = []Certificate{
		{ID: "wan-address", Enabled: true, Source: SourceACME, InterfaceAddresses: []string{"wan0"},
			Account: "le", Challenge: ChallengeHTTP},
	}
	got := issuesByPath(t, cfg)
	if msg := got["certificates[0].challenge"]; !strings.Contains(msg, "web") {
		t.Errorf("message = %q, want the forward named", msg)
	}

	// Off, it takes nothing.
	cfg.NAT.PortForwards[0].Enabled = false
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a disabled forward blocked the challenge: %v", err)
	}
}

func TestCertificateProfileAndSchedule(t *testing.T) {
	t.Parallel()
	named := Certificate{Names: []string{"example.test"}, Profile: "classic"}
	if got := named.EffectiveProfile(); got != "classic" {
		t.Errorf("EffectiveProfile = %q, want classic", got)
	}
	addressed := Certificate{Names: []string{"2001:db8::1"}, Profile: "classic"}
	if !addressed.HasIP() {
		t.Error("HasIP = false for an address")
	}
	if got := addressed.EffectiveProfile(); got != ProfileShortlived {
		t.Errorf("EffectiveProfile = %q, want %s", got, ProfileShortlived)
	}

	// Two routers knock at different minutes of the hour.
	one := &Config{System: System{Hostname: "one"}}
	two := &Config{System: System{Hostname: "two"}}
	if one.CertificateSchedule() == two.CertificateSchedule() {
		t.Errorf("both routers renew at %q", one.CertificateSchedule())
	}
	if !strings.HasSuffix(one.CertificateSchedule(), " * * * *") {
		t.Errorf("schedule = %q, want it hourly", one.CertificateSchedule())
	}
}

// A CA signs account requests with EC or RSA; any other key would fail at
// the first order instead of at apply.
func TestValidateRefusesAnEd25519AccountKey(t *testing.T) {
	t.Parallel()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := certConfig()
	cfg.ACME.Accounts[0].PrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if msg, ok := issuesByPath(t, cfg)["acme.accounts[0].privateKey"]; !ok || !strings.Contains(msg, "EC or RSA") {
		t.Errorf("privateKey issue = %q, %v", msg, ok)
	}
}
