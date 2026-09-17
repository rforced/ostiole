# Ostiole

Ostiole turns a Linux machine into a firewall and router appliance managed from a web UI, with
[nftables](https://netfilter.org/projects/nftables/) underneath. One static binary, no runtime
dependencies beyond `nft` and systemd.

**Status: alpha.** It runs, but expect bugs.

## Requirements

- **Linux 5.14 or newer**, on x86-64 or arm64.
- **systemd** and **nftables** (the `nft` command).
- **`tc`** (iproute2), only to shape traffic. Red Hat family distributions ship it in a package of
  its own (`iproute-tc`); `ostiole install` fetches it, and a router that shapes nothing never
  needs it.

The kernel floor is where the distributions sit, not where any particular feature does: 5.14 is what
RHEL 9 and its rebuilds ship, so the line covers every enterprise distribution still in support along
with current Debian, Ubuntu, Fedora, Alpine, and Arch.

| Distribution | Kernel | |
| --- | --- | --- |
| RHEL 9, Rocky 9, AlmaLinux 9 | 5.14 | supported (the floor) |
| Ubuntu 22.04 LTS | 5.15 | supported |
| Debian 12 | 6.1 | supported |
| Ubuntu 24.04 LTS | 6.8 | supported |
| RHEL 10, Rocky 10, Debian 13, Alpine 3.21 | 6.12 | supported |
| Fedora, Arch | current | supported |
| RHEL 8, Debian 11, Ubuntu 20.04 | 4.18–5.10 | not supported |

`ostiole install` refuses to run on an older kernel; `--ignore-kernel-version` overrides it. Ostiole
never refuses to *filter* over a kernel version, though — a router booted onto an old kernel after
installation keeps working and says so on the dashboard instead.

## Install

```sh
curl -fsSL https://github.com/rforced/ostiole/releases/latest/download/install.sh | sudo sh
```

That downloads the latest release into `/usr/local/bin`, verifies its checksum, and runs
`ostiole install`, which writes the systemd units and starts the web UI on `https://<host>/`.
Packages (deb, rpm, apk, Arch) are attached to every release; after installing one, run
`ostiole install` yourself. Then create the admin account in the UI, run the setup wizard,
and use `ostiole takeover` and `ostiole takeover --network` to retire the previous firewall
and network manager. Updates are a click away under System, verified against signed checksums.

## Design in one paragraph

Ostiole runs as root on dedicated firewall hardware and owns the network stack: interface addressing,
routes, the `inet ostiole` nftables table, and later DHCP and DNS. Configuration is a declarative model
stored under `/etc/ostiole`, rendered to nftables text and applied atomically. Every change that could
lock you out goes through a commit-confirmed flow that auto-reverts unless you confirm from the UI.
Installation disables competing firewall and network managers (firewalld, ufw, NetworkManager, netplan
and friends). Foreign nftables tables are never touched by default.

## Development

Requirements: Go 1.27+, [bun](https://bun.sh), [task](https://taskfile.dev), and for linting
`golangci-lint` v2 built with the same Go version.

```sh
task build        # bun build -> internal/web/dist, then go build -> bin/ostiole
task dev          # Go API on :8080 and Vite dev server on :5173 (proxies /api)
task lint         # golangci-lint, eslint, prettier
task test         # go test -race, vitest
task ci           # lint + test + build, same as GitHub Actions
```

## Layout

```
cmd/ostiole/        entry point
internal/cli/       cobra commands (serve, version)
internal/server/    HTTP API and middleware
internal/web/       embedded SPA (dist/ is generated)
internal/version/   build metadata
web/                Vue 3 (JavaScript) + Tailwind v4 + Reka UI
```

## License

AGPL-3.0-or-later. See [LICENSE](LICENSE).
