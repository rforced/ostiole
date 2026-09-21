# Contributing

## Setup

- Go 1.27 or newer, [bun](https://bun.sh), [task](https://taskfile.dev), and golangci-lint v2
  built with the same Go.
- `task ci` runs what CI runs: lint, test, build. Run it before opening a pull request.
- `task e2e` builds the binary and runs Playwright against it.
- `scripts/ci/test-systems.sh [image…]` runs the per-distribution container tests locally with
  podman and goreleaser.
- `lefthook install` wires the pre-commit checks in `lefthook.yml`.

## Layout

```
cmd/ostiole/        entry point
internal/cli/       cobra commands
internal/server/    HTTP API and middleware
internal/nft/       ruleset rendering
internal/network/   systemd-networkd files and netlink
internal/services/  dnsmasq, unbound, miniupnpd, pppd
internal/install/   install.sh, units, takeover
internal/web/       embedded SPA (dist/ is generated)
web/                Vue 3, Tailwind v4, Reka UI
```

## Conventions

- Web tooling is bun only. Commit `web/bun.lock`.
- Frontend is plain JavaScript with JSDoc where a type helps. No TypeScript unless a dependency
  forces it.
- Go: `gofmt`, `goimports` with local prefix `github.com/rforced/ostiole`, `CGO_ENABLED=0`,
  pure-Go dependencies only.
- Ostiole owns `table inet ostiole` and nothing else. Anything that can lock an admin out of a
  remote router goes through the commit-confirmed flow.
- Keep `internal/web/dist/.gitkeep`; the rest of that directory is generated.
- Never run `install.sh` or `ostiole install` on a workstation you care about. It removes the
  network manager.

## Pull requests

Small, focused, with tests. Say what changes for the user.
