#!/bin/sh
# Pin the binary to one core catalog commit, and copy that commit's
# blueprints into internal/blueprints/core. docs/specs/2026-10-13-core-
# catalog-fetched.md, "What the binary carries".
#
# Usage: scripts/pin-core-catalog.sh <commit>
#
# The commit is a full 40 character id, never a branch, so the pin names
# exactly what was copied. The copy replaces internal/blueprints/core
# entirely: a blueprint removed from the catalog is removed here. pin.yaml
# records the catalog's URL, the commit, and a SHA-256 per blueprint, which
# TestTheCoreCopyMatchesItsPin recomputes, so a hand edit to the copy fails a
# test rather than shipping as if it were the catalog's. The digest is per
# blueprint so that a checkout of one blueprint can be checked alone.
set -eu

url=${CORE_CATALOG_URL:-https://github.com/Paisans-Software/paisans-blueprints}
commit=${1:?usage: $0 <40 character commit id>}
case $commit in
*[!0-9a-f]*) echo "$commit is not a lowercase hex commit id" >&2; exit 2 ;;
esac
[ ${#commit} -eq 40 ] || { echo "$commit is not a full 40 character commit id" >&2; exit 2; }

root=$(git rev-parse --show-toplevel)
dest=$root/internal/blueprints/core
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

git init --quiet "$work/catalog"
git -C "$work/catalog" fetch --quiet --depth 1 "$url" "$commit"
git -C "$work/catalog" -c advice.detachedHead=false checkout --quiet FETCH_HEAD

name=$(sed -n 's/^name: *//p' "$work/catalog/catalog.yaml")
[ "$name" = core ] || { echo "$url at $commit is the catalog named '$name', not core" >&2; exit 1; }

rm -rf "$dest"
mkdir -p "$dest"
cp -R "$work/catalog/blueprints/." "$dest/"

# A blueprint's digest: for each of its files, sorted bytewise, its SHA-256
# and its path relative to the blueprint's directory, then the SHA-256 of
# that list. blueprints.Digest computes the same thing.
digest() {
	(cd "$1" && find . -type f | sed 's|^\./||' | LC_ALL=C sort | while IFS= read -r f; do
		printf '%s  %s\n' "$(shasum -a 256 < "$f" | cut -d' ' -f1)" "$f"
	done | shasum -a 256 | cut -d' ' -f1)
}

{
	cat <<PIN
# Written by scripts/pin-core-catalog.sh. The core catalog commit this
# binary is pinned to, and the SHA-256 of each of its blueprints.
url: $url
commit: $commit
blueprints:
PIN
	for b in $(cd "$dest" && find . -mindepth 1 -maxdepth 1 -type d | sed 's|^\./||' | LC_ALL=C sort); do
		printf '  %s: %s\n' "$b" "$(digest "$dest/$b")"
	done
} > "$work/pin.yaml"
mv "$work/pin.yaml" "$dest/pin.yaml"
echo "core catalog pinned at $commit"
