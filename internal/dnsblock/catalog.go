package dnsblock

import "github.com/rforced/ostiole/internal/model"

// CatalogEntry is a published list Ostiole offers to subscribe to. The
// catalogue is a convenience, not a dependency: every entry is an ordinary
// URL the operator could have typed, and nothing here is fetched unless it
// is subscribed to.
type CatalogEntry struct {
	// Name is the list name a subscription gets, so it also has to be a
	// valid alias-style name.
	Name        string           `json:"name"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	URL         string           `json:"url"`
	Format      model.ListFormat `json:"format"`
	// Category groups the offer in the UI.
	Category string `json:"category"`
	// Names is roughly how many it carries, so the memory it will cost is
	// visible before it is turned on. Checked 2026-09-16.
	Names int `json:"names"`
}

// Catalogue categories.
const (
	CategoryAds     = "ads and tracking"
	CategoryMalware = "malware and phishing"
	CategoryGeneral = "general purpose"
)

// DoHServersURL is a published list of the addresses public DNS over HTTPS
// resolvers answer on. It is an address list, not a name list, so it is
// subscribed to as a firewall alias and dropped by a rule — DoH hides on
// port 443 and nothing in a resolver can see it.
const DoHServersURL = "https://raw.githubusercontent.com/dibdot/DoH-IP-blocklists/master/doh-ipv4.txt"

// catalog is deliberately short. A handful of well-known lists covers what
// most people want, and a long menu of overlapping lists mostly costs
// memory: the lists overlap so heavily that the second one added rarely
// blocks much the first did not.
var catalog = []CatalogEntry{
	{
		Name:        "stevenblack",
		Title:       "Steven Black unified hosts",
		Description: "The usual starting point: adware and malware hosts, consolidated from several well-kept sources.",
		URL:         "https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts",
		Format:      model.FormatHosts,
		Category:    CategoryGeneral,
		Names:       84_000,
	},
	{
		Name:        "hagezi_light",
		Title:       "HaGeZi light",
		Description: "A small list for routers with little memory, or a first try: the worst offenders only.",
		URL:         "https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/light.txt",
		Format:      model.FormatDomains,
		Category:    CategoryAds,
		Names:       36_000,
	},
	{
		Name:        "hagezi_pro",
		Title:       "HaGeZi pro",
		Description: "Ads, tracking, metrics and telemetry, kept current and unusually careful about false positives.",
		URL:         "https://raw.githubusercontent.com/hagezi/dns-blocklists/main/dnsmasq/pro.txt",
		Format:      model.FormatDnsmasq,
		Category:    CategoryAds,
		Names:       226_000,
	},
	{
		Name:        "oisd_small",
		Title:       "OISD small",
		Description: "Ads and tracking, chosen so that nothing people actually use breaks.",
		URL:         "https://small.oisd.nl/domainswild",
		Format:      model.FormatDomains,
		Category:    CategoryAds,
		Names:       55_000,
	},
	{
		Name:        "oisd_big",
		Title:       "OISD big",
		Description: "The same idea with a much wider net: ads, tracking, malware and phishing.",
		URL:         "https://big.oisd.nl/dnsmasq2",
		Format:      model.FormatDnsmasq,
		Category:    CategoryGeneral,
		Names:       246_000,
	},
	{
		Name:        "adguard_dns",
		Title:       "AdGuard DNS filter",
		Description: "AdGuard's own list, in the adblock syntax. Only the rules that name a whole domain can be used here.",
		URL:         "https://adguardteam.github.io/AdGuardSDNSFilter/Filters/filter.txt",
		Format:      model.FormatAdblock,
		Category:    CategoryAds,
		Names:       181_000,
	},
	{
		Name:        "peter_lowe",
		Title:       "Peter Lowe's ad servers",
		Description: "Small, old and very well kept. Good next to a bigger list rather than on its own.",
		URL:         "https://pgl.yoyo.org/adservers/serverlist.php?hostformat=hosts&showintro=0&mimetype=plaintext",
		Format:      model.FormatHosts,
		Category:    CategoryAds,
		Names:       3_500,
	},
	{
		Name:        "urlhaus",
		Title:       "URLhaus malware hosts",
		Description: "Hosts abuse.ch has seen serving malware recently. Small, and changes daily.",
		URL:         "https://urlhaus.abuse.ch/downloads/hostfile/",
		Format:      model.FormatHosts,
		Category:    CategoryMalware,
		Names:       400,
	},
	{
		Name:        "phishing_army",
		Title:       "Phishing Army",
		Description: "Domains seen phishing. Worth having even where ad blocking is not wanted.",
		URL:         "https://phishing.army/download/phishing_army_blocklist_extended.txt",
		Format:      model.FormatDomains,
		Category:    CategoryMalware,
		Names:       154_000,
	},
}

// Catalog returns the lists Ostiole offers, in the order the UI shows them.
func Catalog() []CatalogEntry {
	out := make([]CatalogEntry, len(catalog))
	copy(out, catalog)
	return out
}
