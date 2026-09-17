package host

// Taking a competitor's package off the router is the one step here that
// cannot be undone with a systemctl command, so it is opt-in, it is
// previewed first, and it is offered only for the units below. Everything
// else is disabled and masked, which is enough to stop it and leaves a
// way back.
//
// The table is per package manager because the same service is packaged
// under different names — NetworkManager on the Red Hat family,
// network-manager on Debian, networkmanager on Arch — and a router told
// to remove a name that does not exist there is told nothing useful.
//
// Two absences are deliberate. There is no entry for nftables: the unit
// competes with Ostiole and is masked, but the package brings the nft
// command Ostiole itself runs, so removing it would take the firewall
// with it. And nothing is offered for a manager that is not listed for a
// service, because "we do not know what it is called here" is a better
// answer than a guess that removes the wrong package.

// competitorPackages maps a competing unit to what it is packaged as.
var competitorPackages = map[string]map[string][]string{
	"firewalld": {
		"dnf": {"firewalld"}, "apt-get": {"firewalld"}, "pacman": {"firewalld"},
		"zypper": {"firewalld"}, "apk": {"firewalld"},
	},
	"ufw": {
		"apt-get": {"ufw"}, "pacman": {"ufw"}, "dnf": {"ufw"}, "apk": {"ufw"},
	},
	"iptables": {
		// The service that restores a saved ruleset at boot, not the
		// command: iptables-nft is part of what nft-based distributions
		// ship, and Ostiole does not remove it.
		"dnf": {"iptables-services"}, "apt-get": {"iptables-persistent"},
	},
	"ip6tables": {
		"dnf": {"iptables-services"},
	},
	"netfilter-persistent": {
		"apt-get": {"netfilter-persistent", "iptables-persistent"},
	},
	"shorewall": {
		"dnf": {"shorewall"}, "apt-get": {"shorewall"}, "zypper": {"shorewall"},
	},
	"shorewall6": {
		"dnf": {"shorewall6"}, "apt-get": {"shorewall6"}, "zypper": {"shorewall6"},
	},
	"NetworkManager": {
		"dnf": {"NetworkManager"}, "apt-get": {"network-manager"},
		"pacman": {"networkmanager"}, "zypper": {"NetworkManager"},
		"apk": {"networkmanager"},
	},
	"netplan": {
		"apt-get": {"netplan.io"},
	},
	"dhcpcd": {
		"apt-get": {"dhcpcd-base"}, "pacman": {"dhcpcd"}, "apk": {"dhcpcd"},
		"dnf": {"dhcpcd"}, "zypper": {"dhcpcd"},
	},
	"connman": {
		"apt-get": {"connman"}, "pacman": {"connman"}, "apk": {"connman"},
	},
	"wicked": {
		"zypper": {"wicked", "wicked-service"},
	},
	"ifupdown": {
		"apt-get": {"ifupdown"},
	},
}

// nftablesNote is why the one competitor that is never removed is never
// removed.
const nftablesNote = "The unit is retired if it competes, since stopping it flushes every ruleset in the " +
	"kernel; the package stays either way, because it brings the nft command Ostiole loads its own ruleset with."

// CompetitorPackages names what removing a competitor would take off this
// router, and explains an empty answer. An empty list means the UI offers
// disable and mask and nothing else, which is the default for everything
// this table does not cover.
func CompetitorPackages(unit, manager string) (pkgs []string, note string) {
	if unit == "nftables" {
		return nil, nftablesNote
	}
	byManager, known := competitorPackages[unit]
	if !known {
		return nil, ""
	}
	if manager == "" {
		return nil, "Removing packages needs a package manager Ostiole can drive."
	}
	pkgs, ok := byManager[manager]
	if !ok {
		return nil, "Ostiole does not know what " + unit + " is packaged as on a " + manager + " router; " +
			"disable and mask it, or remove it by hand."
	}
	return pkgs, ""
}
