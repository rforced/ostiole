# Contributing

## Setup

- Go 1.27 or newer, bun, task, golangci-lint v2 (built with the same Go version you use).
- `task ci` runs everything CI runs. Run it before opening a pull request.
- Optional: `lefthook install` wires the pre-commit checks in `lefthook.yml`.

## Conventions

- Web tooling is bun only. Commit `web/bun.lock`.
- Frontend is plain JavaScript. Use JSDoc for types where it helps; no TypeScript unless a dependency forces it.
- Go: `gofmt`, `goimports` with local prefix `github.com/rforced/ostiole`, no cgo.
- Keep `internal/web/dist/.gitkeep`; the rest of that directory is generated.
- Anything that can lock an admin out of a remote router must use the commit-confirmed flow.
- Never test `ostiole install` on a workstation you care about. It disables the network manager.

## Pull requests

Small, focused, with tests. Describe the user-visible behaviour change in the description.
