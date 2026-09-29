# Security policy

Ostiole runs as root and controls a firewall. Report vulnerabilities privately, never in an issue.

## Reporting

Use [Report a vulnerability](https://github.com/rforced/ostiole/security/advisories/new) on this
repository. Include the version or commit (`ostiole version`), the distribution and kernel, and
steps to reproduce. You will hear back within seven days. A fix ships as a release marked as a
security release, which routers set to automatic security updates install unattended.

## Scope

- The `ostiole` binary: the HTTP API, the UI, the CLI and the built-in updater.
- The install script and the release pipeline.
- Rendered nftables, networkd or daemon configuration that is weaker than the policy the UI shows.

## Supported versions

Alpha: the latest release only.

## Verifying a release

Every release publishes `checksums.txt` signed with an ed25519 key. The public half is compiled
into every Ostiole binary, so the built-in updater refuses anything it cannot verify. `install.sh`
checks the same signature with `openssl` and refuses a release it cannot check unless given
`--no-verify`. By hand:

```sh
curl -fsSLO https://github.com/rforced/ostiole/releases/latest/download/checksums.txt
curl -fsSLO https://github.com/rforced/ostiole/releases/latest/download/checksums.txt.sig
printf -- "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA4a3rf0bCdQNTKO3KODxqMrdT1+T1nq9t+KNN2f9DJ0U=\n-----END PUBLIC KEY-----\n" > ostiole.pem
base64 -d < checksums.txt.sig > checksums.sig
openssl pkeyutl -verify -pubin -inkey ostiole.pem -rawin -in checksums.txt -sigfile checksums.sig
sha256sum --ignore-missing -c checksums.txt
```

Artifacts also carry GitHub build provenance, signed through Sigstore with the release workflow's
identity rather than a stored key:

```sh
gh attestation verify ostiole_<version>_linux_amd64.tar.gz --repo rforced/ostiole
```

## For maintainers

### Marking a security release

A router on **Automatic (Security)** installs a release unattended only when its notes contain

```
Security-Release: yes
```

The release workflow lifts the annotated tag's message into the notes, so this is enough:

```sh
git tag -a v0.4.1 -m "Security-Release: yes

Fixes an authentication bypass in the token middleware."
```

Editing the release body on GitHub afterwards works too; routers read the notes at check time.
Without the line a release still reaches **Automatic (All)** and anyone who presses the button.

### Rotating the signing key

A router verifies the next release with the key it already has, so a new key has to arrive before
it is used:

1. `ostiole-sign -genkey` prints a new pair. Add the public half to `TrustedKeysHex` in
   `internal/update/update.go` and to `OSTIOLE_RELEASE_KEYS` in `internal/install/install.sh`,
   keeping the old key first. Release. Routers that update now trust both keys.
2. Once that release is the oldest still in use, replace the `OSTIOLE_SIGNING_KEY` repository
   secret with the new secret and release again.
3. A release later, drop the old public key from both lists.

`ostiole-sign -public` prints the public half of the configured secret, which is the quickest way
to check that the key a release will be signed with is one that binaries trust.

If a key may be compromised, skip the staged rotation: publish an advisory, rotate immediately, and
expect routers on older releases to need a manual reinstall with `install.sh`.
