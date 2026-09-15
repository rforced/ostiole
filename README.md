# Ostiole

Ostiole turns a Linux box into a firewall and router appliance managed from a web UI, with
[nftables](https://netfilter.org/projects/nftables/) underneath. One static binary, no runtime
dependencies beyond `nft` and systemd, runs on any distro that has nftables.

The goal is pfSense-style management, not pfSense feature parity.

**Status: alpha.** It runs real boxes, but expect rough edges.

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

Web tooling is bun only. Do not use npm or yarn in `web/`.

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
