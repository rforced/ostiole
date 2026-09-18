<p align="center"><img src=".github/logo.svg" width="96" alt=""></p>
<h1 align="center">Ostiole</h1>
<p align="center">A firewall and router appliance for Linux, managed from a web UI.</p>
<p align="center">
  <a href="https://github.com/rforced/ostiole/actions/workflows/ci.yml"><img src="https://github.com/rforced/ostiole/actions/workflows/ci.yml/badge.svg?branch=dev" alt="CI"></a>
  <a href="https://github.com/rforced/ostiole/releases/latest"><img src="https://img.shields.io/github/v/release/rforced/ostiole" alt="Latest release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-AGPL--3.0-blue" alt="License: AGPL-3.0"></a>
</p>

Ostiole turns a Linux machine into a firewall and router: one static binary with a web UI,
[nftables](https://netfilter.org/projects/nftables/) underneath, and the standard daemons
(systemd-networkd, dnsmasq, unbound, miniupnpd, pppd) driven from one declarative configuration.
Think pfSense, on the distribution you already run.

**Status: alpha.** It works; expect bugs, and expect the configuration format to change between
releases.

## What it does

- **Firewall**: zones, rules with aliases (addresses, ports, URL feeds, countries) and schedules,
  NAT, rate limits and edge protection, a live log.
- **Interfaces**: physical, VLAN, bridge, bond, PPPoE and WireGuard, static or DHCP, IPv6.
- **Routing**: static routes, multi-WAN with gateway monitoring, policy routing by rule.
- **Services**: DHCP and DNS through dnsmasq and unbound, DNS block lists, UPnP IGD and PCP/NAT-PMP
  port mapping.
- **Traffic shaping**: a line speed per interface, CAKE queues, priorities set by rule.
- **Operations**: commit-confirmed applies with auto-revert, configuration revisions, backup and
  restore with diff, crons, API tokens, roles, packet capture, ping, traceroute, connection and
  neighbour tables.
- **Updates**: Ostiole updates itself from signed releases and keeps the distribution patched
  through its package manager, on a schedule you set.

## Requirements

- **Linux 5.14 or newer** on x86-64 or arm64. That is RHEL 9's kernel; the rendered ruleset needs
  nothing newer.
- **systemd.** The install script brings the rest: nftables, systemd-networkd, dnsmasq, unbound,
  miniupnpd, ppp and tc.
- **A machine that is the router.** Ostiole runs as root, becomes the network manager and the
  firewall, and removes the ones it replaces. Do not run it on a workstation.

The installer refuses an older kernel (`OSTIOLE_IGNORE_KERNEL=1` overrides). A router booted onto
an old kernel after installation keeps filtering and shows a warning on the dashboard.

## What is tested

CI runs on every push: [ci.yml](.github/workflows/ci.yml),
[security.yml](.github/workflows/security.yml), [codeql.yml](.github/workflows/codeql.yml).

| Check | Covers |
| --- | --- |
| `go test -race`, `go vet`, golangci-lint | The backend, with the nftables and networkd renderers checked against golden files |
| eslint, prettier, vitest | The frontend |
| Playwright on Chromium | The built binary driven through the browser, from first run through every section |
| Test systems | On each distribution below: the package installs and runs, the install script verifies signatures and refuses tampered builds, and a full install followed by `ostiole repair` under systemd in a container |
| CodeQL, govulncheck, bun audit, gitleaks, actionlint, zizmor, shellcheck | Code, dependencies, secrets, workflows and shell scripts |

The test systems are the stock images in [test-systems.json](.github/test-systems.json):

| Distribution | Kernel | Notes |
| --- | --- | --- |
| Rocky Linux 9 | 5.14 | The floor. RHEL 9 and AlmaLinux 9 are the same |
| Rocky Linux 10 | 6.12 | RHEL 10 and AlmaLinux 10 are the same |
| Ubuntu 24.04 LTS | 6.8 | |
| Ubuntu 26.04 LTS | current | |
| Fedora 44 | current | |
| Arch Linux | current | miniupnpd is built from the AUR during install |
| Alpine 3.24 | current | Package and script only: no systemd, so the script places the binary and stops |

Debian 12 and 13, Ubuntu 22.04 and openSUSE meet the requirements and should work, but are not in
the matrix. RHEL 8, Debian 11 and Ubuntu 20.04 are below the kernel requirement.

## Install

On the machine that will be the router:

```sh
curl -fsSL https://github.com/rforced/ostiole/releases/latest/download/install.sh | sudo sh
```

The script prints its plan and waits for a yes. The plan is: install the packages above, download
the release and verify its checksum and signature, write the units, load a bootstrap ruleset, hand
addressing to systemd-networkd keeping the addresses the machine has now, then remove the
firewalls, network managers, updaters and desktop services a router has no use for (firewalld, ufw,
NetworkManager, netplan, unattended-upgrades, snapd and the like).

- `--dry-run` prints the plan and stops.
- `--yes` agrees in advance, for provisioning: `curl -fsSL https://github.com/rforced/ostiole/releases/latest/download/install.sh | sudo sh -s -- --yes`
- `--keep <package>` exempts a package from removal. Repeatable.
- `OSTIOLE_VERSION=v0.8.2` pins a release.

Then open `https://<host>/`, create the admin account and run the wizard to choose WAN and LAN.
Until that first apply is confirmed the router forwards nothing.

**Packages.** deb, rpm, apk and Arch packages are attached to every release. After installing one,
run `ostiole repair`: it runs the same script with the binary already in place, and is also the
command for a router whose packages were changed by hand.

**Uninstall.** `ostiole uninstall` stops the service and removes the units and the `inet ostiole`
table. It does not put back what the script removed.

## How it works

The configuration is one JSON model under `/etc/ostiole`. Ostiole renders it into an nftables
ruleset, systemd-networkd files and daemon configuration, validates the result, and applies it as
one transaction. Applies that could lock you out are commit-confirmed: they revert on their own
unless confirmed from the UI within the window. Ostiole owns one table, `inet ostiole`, and never
flushes another. The UI serves no third-party assets and there is no telemetry.

## SELinux and AppArmor

Ostiole changes neither. It loads no policy module, ships no profile, and runs neither `setenforce`
nor `semanage`: a router that boots with SELinux enforcing stays that way. The generated files go
where the policy your distribution already ships expects to find them.

- Daemon configuration goes in the daemon's own directory — `/etc/dnsmasq.d`, `/etc/unbound`,
  `/etc/miniupnpd` — never in `/etc/ostiole`, which is `0700` and holds only Ostiole's own files.
  dnsmasq drops privileges before reading its hosts file, so it could not read one from there even
  when started as root.
- State goes where the package keeps it: `/var/lib/dnsmasq/ostiole.leases`, and the DNSSEC trust
  anchor at `/var/lib/unbound/root.key`, owned by `unbound` so it can roll the key over in place.
- unbound listens on `127.0.0.53:53` rather than a port of its own, because `unbound_t` may bind
  only DNS-labelled ports and is refused any other even as root. dnsmasq keeps `127.0.0.1:53`.
- The binary belongs in a system bin directory, which is why `ostiole install` copies it to
  `/usr/local/bin`. systemd will not execute a copy left in `/root`, labelled `admin_home_t`, from
  the transient units the network handover and the updater run in: it fails with 203/EXEC.

The same rule satisfies AppArmor. Ubuntu enforces a profile for unbound that allows exactly
`/etc/unbound` and the files under `/var/lib/unbound` that unbound owns; it ships none for dnsmasq.

A denial looks like a daemon that will not start, reporting "Permission denied" for a file root can
read. `journalctl -u ostiole-dnsmasq -u ostiole-unbound` shows the failure, and `ausearch -m AVC -ts
recent` the reason.

## Security

Ostiole runs as root and is the firewall. Report vulnerabilities privately with
[Report a vulnerability](https://github.com/rforced/ostiole/security/advisories/new), not in an
issue. [SECURITY.md](SECURITY.md) has the scope, what to expect back, and how to verify a release.

## Development

Go 1.27, [bun](https://bun.sh), [task](https://taskfile.dev) and golangci-lint v2.

```sh
task dev    # Go API on :8080, Vite on :5173
task ci     # lint, test and build, the same as CI
task e2e    # build, then Playwright against the binary
```

`scripts/ci/test-systems.sh` runs the per-distribution tests locally with podman. See
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

[AGPL-3.0-or-later](LICENSE).
