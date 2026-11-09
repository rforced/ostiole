#!/bin/sh
#   scripts/update-coraza.sh v3.8.2
#   scripts/update-coraza.sh 08aa5114730064b86e62d15bbc1fa7e6fdf94f57
#
# Copies a Coraza release or a commit on main into third_party/coraza and
# applies scripts/coraza/*.patch in name order. Each patch starts with its
# VERSION note as "# " lines and adds a "Changed by Ostiole" line to every
# file it touches (Apache-2.0 wants changed files marked).
set -eu

rev=${1:?usage: scripts/update-coraza.sh <tag or commit>, e.g. v3.8.2}
module=github.com/corazawaf/coraza/v3
root=$(cd "$(dirname "$0")/.." && pwd)
dest=$root/third_party/coraza
patches=$root/scripts/coraza

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
# Inside the repo go mod download follows the replace, so ask from a
# throwaway module.
if ! json=$(cd "$work" && go mod init scratch >/dev/null 2>&1 &&
	GOWORK=off GOFLAGS=-mod=mod go mod download -json "$module@$rev"); then
	echo "go mod download $module@$rev failed:" >&2
	echo "$json" >&2
	exit 1
fi
field() {
	printf '%s\n' "$json" | sed -n "s/^[[:space:]]*\"$1\": \"\\(.*\\)\",\\{0,1\\}\$/\\1/p"
}
version=$(field Version)
dir=$(field Dir)
sum=$(field Sum)
if [ -z "$version" ] || [ -z "$dir" ] || [ -z "$sum" ]; then
	echo "go mod download gave no Version, Dir or Sum for $module@$rev" >&2
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

notes=$work/notes
: >"$notes"
for p in "$patches"/*.patch; do
	[ -e "$p" ] || continue
	name=scripts/coraza/${p##*/}
	note=$(sed -n '/^#/!q; p' "$p")
	if [ -z "$note" ]; then
		echo "$name has no \"# \" note at the top" >&2
		exit 1
	fi
	unmarked=$(awk '
		/^\+\+\+ b\// { if (f != "" && !m) print f; f = substr($2, 3); m = 0; next }
		/^\+.*Changed by Ostiole/ { m = 1 }
		END { if (f != "" && !m) print f }
	' "$p")
	if [ -n "$unmarked" ]; then
		echo "$name adds no \"Changed by Ostiole\" line to: $unmarked" >&2
		exit 1
	fi
	if ! out=$(patch -p1 -d "$dest" --forward --fuzz=0 --dry-run <"$p" 2>&1); then
		printf '%s\n' "$out" >&2
		echo "$name does not apply to Coraza $version." >&2
		echo "If upstream took it (its note names the PR or issue), delete it; if not, redo it." >&2
		exit 1
	fi
	patch -p1 -d "$dest" --forward --fuzz=0 --no-backup-if-mismatch --quiet <"$p"
	printf '%s\n' "$note" >>"$notes"
done
[ -s "$notes" ] || echo "# - None." >"$notes"

{
	cat <<EOF
# Coraza $version from the Go module proxy, $sum,
# without its tests, testdata, testing/ and http/e2e/ packages, docs, agent
# guides, or dev and CI files. scripts/update-coraza.sh copies a release or
# a commit on main the same way each time and applies scripts/coraza/*.patch
# in name order: upstream PRs not merged yet and our own fixes. A patch
# upstream took no longer applies and is deleted.
# govulncheck sees this copy without a version, so Coraza's advisories
# (github.com/corazawaf/coraza/security/advisories) are checked by hand.
#
# Changes, each marked "Changed by Ostiole" in the source:
EOF
	cat "$notes"
	echo "$version"
} >"$dest/VERSION"

cat <<EOF
Coraza $version is in third_party/coraza. Next:
  go mod tidy && git diff --stat go.mod go.sum
  go build ./... && task --force build:proxy && go test ./internal/services -run 'Proxy|Sidecar|Concurrent'
Read Coraza's advisories by hand (github.com/corazawaf/coraza/security/advisories),
and the upstream compare for "Merge commit from fork" commits, which are GHSA fixes.
EOF
