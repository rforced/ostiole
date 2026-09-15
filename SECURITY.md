# Security policy

Ostiole runs as root and controls a firewall. Please report vulnerabilities privately.

## Reporting

Use GitHub private vulnerability reporting on this repository
(Security tab, "Report a vulnerability"). Include steps to reproduce and the version or commit.
You should hear back within seven days.

## Scope

- The `ostiole` binary, its HTTP API and UI, installer, and updater.
- Rendered nftables, networkd, and daemon configuration that weakens the intended policy.

## Supported versions

Pre-alpha: only the `dev` branch. Once releases exist, the latest stable release is supported.
