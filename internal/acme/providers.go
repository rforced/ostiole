package acme

import (
	"fmt"
	"time"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/desec"
	"github.com/go-acme/lego/v4/providers/dns/digitalocean"
	"github.com/go-acme/lego/v4/providers/dns/exec"
	"github.com/go-acme/lego/v4/providers/dns/hetzner"
	"github.com/go-acme/lego/v4/providers/dns/porkbun"
	"github.com/go-acme/lego/v4/providers/dns/rfc2136"
	"github.com/go-acme/lego/v4/providers/dns/route53"

	"github.com/rforced/ostiole/internal/model"
)

// Providers builds the dns-01 solver for each kind in
// model.ProviderKinds. Each one is imported by name: lego's own registry
// pulls in every provider it has, and most of their cloud SDKs with them.
var Providers = map[string]func(p model.DNSProvider) (challenge.Provider, error){
	"cloudflare": func(p model.DNSProvider) (challenge.Provider, error) {
		c := cloudflare.NewDefaultConfig()
		c.AuthToken, c.ZoneToken = p.Settings["token"], p.Settings["zoneToken"]
		c.PropagationTimeout = waitOr(p, c.PropagationTimeout)
		return cloudflare.NewDNSProviderConfig(c)
	},
	"desec": func(p model.DNSProvider) (challenge.Provider, error) {
		c := desec.NewDefaultConfig()
		c.Token = p.Settings["token"]
		c.PropagationTimeout = waitOr(p, c.PropagationTimeout)
		return desec.NewDNSProviderConfig(c)
	},
	"rfc2136": func(p model.DNSProvider) (challenge.Provider, error) {
		c := rfc2136.NewDefaultConfig()
		c.Nameserver = p.Settings["nameserver"]
		c.TSIGKey, c.TSIGSecret = p.Settings["tsigKey"], p.Settings["tsigSecret"]
		if alg := p.Settings["tsigAlgorithm"]; alg != "" {
			c.TSIGAlgorithm = alg
		} else {
			// lego's default is HMAC-SHA1, which no server has wanted for
			// fifteen years.
			c.TSIGAlgorithm = "hmac-sha256."
		}
		c.PropagationTimeout = waitOr(p, c.PropagationTimeout)
		return rfc2136.NewDNSProviderConfig(c)
	},
	"exec": func(p model.DNSProvider) (challenge.Provider, error) {
		c := exec.NewDefaultConfig()
		c.Program, c.Mode = p.Settings["program"], p.Settings["mode"]
		c.PropagationTimeout = waitOr(p, c.PropagationTimeout)
		return exec.NewDNSProviderConfig(c)
	},
	"digitalocean": func(p model.DNSProvider) (challenge.Provider, error) {
		c := digitalocean.NewDefaultConfig()
		c.AuthToken = p.Settings["token"]
		c.PropagationTimeout = waitOr(p, c.PropagationTimeout)
		return digitalocean.NewDNSProviderConfig(c)
	},
	"hetzner": func(p model.DNSProvider) (challenge.Provider, error) {
		c := hetzner.NewDefaultConfig()
		c.APIToken = p.Settings["token"]
		c.PropagationTimeout = waitOr(p, c.PropagationTimeout)
		return hetzner.NewDNSProviderConfig(c)
	},
	"porkbun": func(p model.DNSProvider) (challenge.Provider, error) {
		c := porkbun.NewDefaultConfig()
		c.APIKey, c.SecretAPIKey = p.Settings["apiKey"], p.Settings["secretKey"]
		c.PropagationTimeout = waitOr(p, c.PropagationTimeout)
		return porkbun.NewDNSProviderConfig(c)
	},
	"route53": func(p model.DNSProvider) (challenge.Provider, error) {
		c := route53.NewDefaultConfig()
		c.AccessKeyID, c.SecretAccessKey = p.Settings["accessKeyId"], p.Settings["secretAccessKey"]
		c.Region, c.HostedZoneID = p.Settings["region"], p.Settings["hostedZoneId"]
		c.PropagationTimeout = waitOr(p, c.PropagationTimeout)
		return route53.NewDNSProviderConfig(c)
	},
}

func waitOr(p model.DNSProvider, fallback time.Duration) time.Duration {
	if p.PropagationSeconds > 0 {
		return time.Duration(p.PropagationSeconds) * time.Second
	}
	return fallback
}

// checkProviders fails the build's tests if the kinds table and the
// providers compiled in ever disagree.
func checkProviders() error {
	for _, k := range model.ProviderKinds {
		if _, ok := Providers[k.Kind]; !ok {
			return fmt.Errorf("%s is offered but not built in", k.Kind)
		}
	}
	for kind := range Providers {
		if _, ok := model.ProviderKindOf(kind); !ok {
			return fmt.Errorf("%s is built in but not offered", kind)
		}
	}
	return nil
}
