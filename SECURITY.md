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

## Marking a release as a security release

A router set to **Automatic (Security)** installs a new Ostiole release only when something published
since the version it runs is marked as a security fix. The mark is a line in the release notes:

```
Security-Release: yes
```

The release workflow lifts the annotated tag's message into the notes, so

```sh
git tag -a v0.4.1 -m "Security-Release: yes

Fixes an authentication bypass in the token middleware."
```

is enough. Editing the release body on GitHub afterwards works just as well; routers read the notes at
check time, not at tag time. Without the line a release still reaches everyone on **Automatic (All)**
and anyone who presses the button — the mark only decides what gets installed unattended.

## Release integrity

Every release publishes `checksums.txt` signed with an ed25519 key. The public half is compiled
into every Ostiole binary, so the built-in updater refuses anything it cannot verify, and
`install.sh` checks the same signature with `openssl` when it is available.

Verify a release by hand:

```sh
curl -fsSLO https://github.com/rforced/ostiole/releases/latest/download/checksums.txt
curl -fsSLO https://github.com/rforced/ostiole/releases/latest/download/checksums.txt.sig
printf -- "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA4a3rf0bCdQNTKO3KODxqMrdT1+T1nq9t+KNN2f9DJ0U=\n-----END PUBLIC KEY-----\n" > ostiole.pem
base64 -d < checksums.txt.sig > checksums.sig
openssl pkeyutl -verify -pubin -inkey ostiole.pem -rawin -in checksums.txt -sigfile checksums.sig
sha256sum --ignore-missing -c checksums.txt
```

Artifacts also carry GitHub build provenance, signed through Sigstore with the release workflow's
own identity rather than a stored key:

```sh
gh attestation verify ostiole_<version>_linux_amd64.tar.gz --repo rforced/ostiole
```

### Rotating the signing key

A router updates by verifying the *next* release with the key it already has, so a new key has to
arrive before it is used:

1. `ostiole-sign -genkey` prints a new pair. Add the public half to `TrustedKeysHex` in
   `internal/update/update.go` and to `OSTIOLE_RELEASE_KEYS` in `internal/install/install.sh`, keeping the
   old key first. Release. Routers that update now trust both keys.
2. Once that release is the oldest one still in use, replace the `OSTIOLE_SIGNING_KEY` repository
   secret with the new secret and release again, signed by the new key.
3. A release later, drop the old public key from both lists.

`ostiole-sign -public` prints the public half of the configured secret, which is the quickest way
to check that the key a release will be signed with is one that binaries trust.

If a key is believed to be compromised, skip the staged rotation: publish an advisory, rotate
immediately, and expect routers on older releases to need a manual reinstall with `install.sh`.
