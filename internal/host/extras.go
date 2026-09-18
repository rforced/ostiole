package host

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/sysupdate"
)

// A distribution image arrives with the things a general-purpose server
// wants and a router does not: a second updater with a schedule of its
// own, a snap daemon that upgrades itself outside any policy, the
// daemons a desktop needs for USB sticks and batteries. Each is a process
// running as root on the edge of the network for no reason, and the
// updaters are worse than that, since two of them share one package lock
// and neither knows when the other will reboot the router.
//
// This is the list. Every entry says what it is called, what unit betrays
// it, and what it is packaged as on each manager; an entry with no
// packages for a manager is one of that manager's own timers, which is
// masked rather than removed because the package is the manager itself.

// Extra is something a router has no use for.
type Extra struct {
	// Key names it on the command line and in the API.
	Key string `json:"key"`
	// Label is what a page calls it.
	Label string `json:"label"`
	// Why says what it would do to a router left running.
	Why string `json:"why"`
	// Kind is updater or desktop.
	Kind string `json:"kind"`
	// Units are what systemd calls it; all of them are stopped and masked.
	Units []string `json:"units"`
	// packages names it per manager. A manager with an entry of no names
	// ships it as part of something Ostiole needs, so only the units go.
	packages map[string][]string
}

// Packages names what removing this extra takes off a router with the
// given manager, and whether removal is offered there at all: an extra a
// manager does not list is unknown to it, and one it lists with no
// names is masked and kept.
func (e Extra) Packages(manager string) (pkgs []string, known bool) {
	pkgs, known = e.packages[manager]
	return pkgs, known
}

// Extras are the things a router does not need, in the order a page
// lists them: the updaters first, because two of them on one router is
// a fault rather than a waste, then the desktop leftovers.
func Extras() []Extra {
	all := map[string][]string{
		"dnf": nil, "apt-get": nil, "pacman": nil, "zypper": nil,
	}
	same := func(name string) map[string][]string {
		out := map[string][]string{}
		for m := range all {
			out[m] = []string{name}
		}
		return out
	}
	return []Extra{{
		Key: "unattended-upgrades", Label: "unattended-upgrades", Kind: "updater",
		Why:      "installs updates and reboots on a schedule of its own, next to the one Ostiole keeps",
		Units:    []string{"unattended-upgrades.service"},
		packages: map[string][]string{"apt-get": {"unattended-upgrades"}},
	}, {
		Key: "apt-daily", Label: "apt's daily timers", Kind: "updater",
		Why:      "refresh and upgrade on apt's own clock, holding the lock while Ostiole's updater waits for it",
		Units:    []string{"apt-daily.timer", "apt-daily-upgrade.timer"},
		packages: map[string][]string{"apt-get": {}},
	}, {
		Key: "dnf-automatic", Label: "dnf-automatic", Kind: "updater",
		Why:      "installs updates on a schedule of its own, next to the one Ostiole keeps",
		Units:    []string{"dnf-automatic.timer", "dnf-automatic-install.timer", "dnf-automatic-download.timer", "dnf-automatic-notifyonly.timer"},
		packages: map[string][]string{"dnf": {"dnf-automatic"}},
	}, {
		Key: "dnf-makecache", Label: "dnf's cache timer", Kind: "updater",
		Why:      "refreshes the package index on dnf's own clock, holding the lock while Ostiole's updater waits for it",
		Units:    []string{"dnf-makecache.timer"},
		packages: map[string][]string{"dnf": {}},
	}, {
		Key: "yum-cron", Label: "yum-cron", Kind: "updater",
		Why:      "installs updates on a schedule of its own",
		Units:    []string{"yum-cron.service"},
		packages: map[string][]string{"dnf": {"yum-cron"}},
	}, {
		Key: "packagekit", Label: "PackageKit", Kind: "updater",
		Why:      "a package manager for desktops, with a refresh timer of its own",
		Units:    []string{"packagekit.service", "packagekit-offline-update.service"},
		packages: map[string][]string{"apt-get": {"packagekit"}, "dnf": {"PackageKit"}, "zypper": {"PackageKit"}, "pacman": {"packagekit"}},
	}, {
		Key: "transactional-update", Label: "transactional-update's timer", Kind: "updater",
		Why:      "applies updates on SUSE's own clock",
		Units:    []string{"transactional-update.timer"},
		packages: map[string][]string{"zypper": {}},
	}, {
		Key: "snapd", Label: "snapd", Kind: "desktop",
		Why:      "runs and upgrades snaps on its own schedule, outside any update policy, and mounts each one as a loop device",
		Units:    []string{"snapd.service", "snapd.socket", "snapd.seeded.service", "snapd.apparmor.service"},
		packages: map[string][]string{"apt-get": {"snapd"}},
	}, {
		Key: "modemmanager", Label: "ModemManager", Kind: "desktop",
		Why:      "drives cellular modems; keep it only if a USB modem is this router's line",
		Units:    []string{"ModemManager.service"},
		packages: map[string][]string{"apt-get": {"modemmanager"}, "dnf": {"ModemManager"}, "pacman": {"modemmanager"}, "zypper": {"ModemManager"}},
	}, {
		Key: "udisks2", Label: "udisks2", Kind: "desktop",
		Why:      "mounts removable drives for a desktop session",
		Units:    []string{"udisks2.service"},
		packages: same("udisks2"),
	}, {
		Key: "upower", Label: "upower", Kind: "desktop",
		Why:      "watches batteries for a desktop session",
		Units:    []string{"upower.service"},
		packages: same("upower"),
	}, {
		Key: "fwupd", Label: "fwupd", Kind: "desktop",
		Why:      "fetches firmware updates on its own timer; keep it on bare metal that wants them",
		Units:    []string{"fwupd.service", "fwupd-refresh.timer"},
		packages: same("fwupd"),
	}, {
		Key: "multipath", Label: "multipathd", Kind: "desktop",
		Why:      "manages multipath storage; keep it only on a router that boots from a SAN",
		Units:    []string{"multipathd.service", "multipathd.socket"},
		packages: map[string][]string{"apt-get": {"multipath-tools"}, "dnf": {"device-mapper-multipath"}, "zypper": {"multipath-tools"}},
	}, {
		Key: "lxd-installer", Label: "lxd installer", Kind: "desktop",
		Why:      "installs the lxd snap the first time anybody types lxd",
		Units:    []string{"lxd-installer.socket"},
		packages: map[string][]string{"apt-get": {"lxd-installer"}},
	}}
}

// ExtraByKey finds an extra by key, case-insensitively, because the
// keys are typed on a command line.
func ExtraByKey(key string) (Extra, bool) {
	for _, e := range Extras() {
		if strings.EqualFold(e.Key, key) {
			return e, true
		}
	}
	return Extra{}, false
}

// ExtraState is one extra as this router has it.
type ExtraState struct {
	Extra
	// Present is whether systemd knows any of its units, which is how an
	// installed package shows itself without asking the package database.
	Present bool `json:"present"`
	// Active and Enabled are the worst state among its units.
	Active  bool   `json:"active"`
	Enabled string `json:"enabled,omitempty"`
	// Packages is what a removal takes, empty when only the units go.
	Packages []string `json:"packages,omitempty"`
	// MaskOnly is an extra whose package is the package manager itself.
	MaskOnly bool `json:"maskOnly"`
	// Removed is a unit systemd lists only because of the mask Ostiole
	// left: the package is gone.
	Removed bool `json:"removed,omitempty"`
	// Note explains an extra this manager cannot remove.
	Note string `json:"note,omitempty"`
}

// extras reports every extra this router has.
func extras(ctx context.Context, d Deps, manager string) []ExtraState {
	out := []ExtraState{}
	if d.Units == nil {
		return out
	}
	for _, e := range Extras() {
		st := ExtraState{Extra: e}
		masked := 0
		for _, unit := range e.Units {
			enabled, _ := d.Units.Run(ctx, "is-enabled", unit)
			enabled = firstLine(enabled)
			if enabled == "" || enabled == "not-found" || strings.Contains(enabled, "No such file") || strings.Contains(enabled, "not found") {
				continue
			}
			st.Present = true
			if enabled == "masked" {
				masked++
			}
			if st.Enabled == "" || enabled == "enabled" {
				st.Enabled = enabled
			}
			if active, _ := d.Units.Run(ctx, "is-active", unit); firstLine(active) == "active" {
				st.Active = true
			}
		}
		if !st.Present {
			continue
		}
		pkgs, known := e.Packages(manager)
		switch {
		case manager == "":
			st.Note = "Removing packages needs a package manager Ostiole can drive."
		case !known:
			st.Note = "Ostiole does not know what " + e.Label + " is packaged as on a " + manager + " router; its units are masked instead."
		case len(pkgs) == 0:
			st.MaskOnly = true
		default:
			st.Packages = pkgs
		}
		// Every unit masked is what a removal leaves behind; the package
		// database says whether the package went with it. Only asked in
		// that state, so the report stays quick.
		if masked == len(e.Units) && len(st.Packages) > 0 && d.Packages != nil && d.Root {
			installed := false
			checked := true
			for _, p := range st.Packages {
				ok, err := d.Packages.Installed(ctx, p)
				if err != nil {
					checked = false
					break
				}
				if ok {
					installed = true
					break
				}
			}
			st.Removed = checked && !installed
		}
		out = append(out, st)
	}
	return out
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}

// RemoveExtras stops and masks the named extras and takes their packages
// off the router. With preview nothing changes and the package manager's
// own account of what would go is returned, which is what the operator
// agrees to. An extra whose package is the package manager itself is
// masked and kept.
func RemoveExtras(ctx context.Context, d Deps, keys []string, preview bool) (string, error) {
	if !d.Root {
		return "", ErrNotRoot
	}
	if len(keys) == 0 {
		return "", errors.New("name at least one extra to remove")
	}
	rep := Status(ctx, d)
	var units, pkgs, said []string
	for _, key := range keys {
		st, ok := findExtra(rep.Extras, key)
		if !ok {
			return "", fmt.Errorf("%s is not something this router has", key)
		}
		if st.Note != "" && !st.MaskOnly {
			return "", fmt.Errorf("%s cannot be removed by Ostiole. %s", key, st.Note)
		}
		units = append(units, st.Units...)
		pkgs = append(pkgs, st.Packages...)
	}
	plan := ""
	if len(pkgs) > 0 {
		if d.Packages == nil {
			return "", sysupdate.ErrNoManager
		}
		var err error
		plan, err = d.Packages.Remove(ctx, pkgs, true)
		if err != nil {
			return plan, err
		}
		if err := refuseRemoval(plan, keys, protectedPackages(rep)); err != nil {
			return plan, err
		}
	}
	if preview {
		if plan == "" {
			return "masked and kept: " + strings.Join(units, ", "), nil
		}
		return plan, nil
	}
	if err := install.Takeover(ctx, d.Units, units, d.log()); err != nil {
		return "", err
	}
	said = append(said, "masked and stopped: "+strings.Join(units, ", "))
	if len(pkgs) > 0 {
		out, err := d.Packages.Remove(ctx, pkgs, false)
		if out != "" {
			said = append(said, out)
		}
		if err != nil {
			return strings.Join(said, "\n"), err
		}
		said = append(said, "removed: "+strings.Join(pkgs, ", "))
	}
	return strings.Join(said, "\n"), nil
}

func findExtra(all []ExtraState, key string) (ExtraState, bool) {
	for _, e := range all {
		if strings.EqualFold(e.Key, key) {
			return e, true
		}
	}
	return ExtraState{}, false
}
