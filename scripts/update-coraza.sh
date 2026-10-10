#!/bin/sh
#   scripts/update-coraza.sh v3.8.2
set -eu

version=${1:?usage: scripts/update-coraza.sh <version>, e.g. v3.8.2}
module=github.com/corazawaf/coraza/v3
root=$(cd "$(dirname "$0")/.." && pwd)
dest=$root/third_party/coraza
patches=$root/scripts/coraza

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
# Inside the repo go mod download follows the replace, so ask from a
# throwaway module.
if ! json=$(cd "$work" && go mod init scratch >/dev/null 2>&1 &&
	GOWORK=off GOFLAGS=-mod=mod go mod download -json "$module@$version"); then
	echo "go mod download $module@$version failed:" >&2
	echo "$json" >&2
	exit 1
fi
dir=$(printf '%s\n' "$json" | sed -n 's/^[[:space:]]*"Dir": "\(.*\)",\{0,1\}$/\1/p')
sum=$(printf '%s\n' "$json" | sed -n 's/^[[:space:]]*"Sum": "\(.*\)",\{0,1\}$/\1/p')
if [ -z "$dir" ] || [ -z "$sum" ]; then
	echo "go mod download gave no Dir or Sum for $module@$version" >&2
	exit 1
fi

rsync -a --delete --exclude=/VERSION --chmod=Du=rwx,Dgo=rx,Fu=rw,Fgo=r \
	--exclude='*_test.go' --exclude='testdata/' --exclude='/testing/' \
	--exclude='/http/e2e/' --exclude='.*' --exclude=/docs/ \
	--exclude=/magefile.go --exclude=/magefile_adr.go --exclude=/mage.go \
	--exclude=/codecov.yml --exclude=/sonar-project.properties \
	--exclude=/renovate.json --exclude=/go.work --exclude=/CHANGELOG.md \
	--exclude=/CODE_OF_CONDUCT.md --exclude=/CONTRIBUTING.md \
	--exclude=/SECURITY.md --exclude=/RATIONALE.md --exclude=/AGENTS.md \
	--exclude=/CLAUDE.md \
	"$dir/" "$dest/"

for p in exceptions-copy:1723 newwaf-close:1738; do
	name=${p%%:*}.patch
	issue=${p#*:}
	if ! out=$(patch -p1 -d "$dest" --forward --fuzz=0 --dry-run <"$patches/$name" 2>&1); then
		printf '%s\n' "$out" >&2
		echo "scripts/coraza/$name does not apply to Coraza $version." >&2
		echo "Check upstream issue #$issue: if the fix is in, drop the patch file and its note in this script; if not, redo the patch." >&2
		exit 1
	fi
	patch -p1 -d "$dest" --forward --fuzz=0 --no-backup-if-mismatch --quiet <"$patches/$name"
done

for f in waf.go internal/corazawaf/rule.go; do
	if [ "$(grep -c 'Changed by Ostiole' "$dest/$f")" != 1 ]; then
		echo "$f does not have exactly one \"Changed by Ostiole\" marker after patching" >&2
		exit 1
	fi
done

cat >"$dest/VERSION" <<EOF
# Coraza $version from the Go module proxy, $sum,
# without its tests, testdata, testing/ and http/e2e/ packages, docs, agent
# guides, or dev and CI files. It is updated as soon as upstream releases,
# by scripts/update-coraza.sh, which copies the release the same way and
# applies the changes below from scripts/coraza/, less any upstream took.
# govulncheck sees this copy without a version, so Coraza's advisories
# (github.com/corazawaf/coraza/security/advisories) are checked by hand.
#
# Changes, each marked "Changed by Ostiole" in the source:
# - internal/corazawaf/rule.go: a transaction appended its
#   ctl:ruleRemoveTargetById removals into the rule's shared exception
#   list where that had room, so concurrent requests removing different
#   targets from one rule overwrote each other. The capacity is capped so
#   the append copies. Upstream: issue #1723, open at $version; closed,
#   unmerged PR #1509 had the same fix.
#   Patch: scripts/coraza/exceptions-copy.patch.
# - waf.go: NewWAF returned on a rule, audit log or Validate error without
#   closing the WAF, so the patterns its rules had compiled stayed in the
#   shared memoize cache with a dead owner for the life of the process, one
#   more owner per failed reload. A deferred Close on error releases them.
#   Upstream: issue #1738, reported 2026-10-09, open at $version.
#   Patch: scripts/coraza/newwaf-close.patch.
$version
EOF

cat <<EOF
Coraza $version is in third_party/coraza. Next:
  go build ./... && task --force build:proxy && go test ./internal/services -run 'TestConcurrentExclusionsKeepTheirOwnTargets|TestProxyGoldenValidates'
Read Coraza's advisories by hand (github.com/corazawaf/coraza/security/advisories),
and the upstream compare for "Merge commit from fork" commits, which are GHSA fixes.
EOF
