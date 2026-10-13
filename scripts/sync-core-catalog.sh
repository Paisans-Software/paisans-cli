#!/bin/sh
# Copy the core catalog at one commit into internal/blueprints/core, and pin
# the binary to it. docs/specs/2026-10-12-blueprints-and-catalogs.md, "The
# core catalog".
#
# Usage: scripts/sync-core-catalog.sh <commit>
#
# The commit is a full 40 character id, never a branch, so the pin names
# exactly what was copied. The copy replaces internal/blueprints/core
# entirely: a blueprint removed from the catalog is removed here. pin.yaml
# records the catalog's URL, the commit, and a SHA-256 over every file
# copied, which TestTheCoreCopyMatchesItsPin recomputes, so a hand edit to
# the copy fails a test rather than shipping as if it were the catalog's.
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

git -c advice.detachedHead=false clone --quiet --no-checkout "$url" "$work/catalog"
git -C "$work/catalog" checkout --quiet "$commit"

name=$(sed -n 's/^name: *//p' "$work/catalog/catalog.yaml")
[ "$name" = core ] || { echo "$url at $commit is the catalog named '$name', not core" >&2; exit 1; }

rm -rf "$dest"
mkdir -p "$dest"
cp -R "$work/catalog/blueprints/." "$dest/"

# The digest: for each file, sorted bytewise, its SHA-256 and its path
# relative to the copy, then the SHA-256 of that list. internal/blueprints
# computes the same thing.
digest=$(cd "$dest" && find . -type f | sed 's|^\./||' | LC_ALL=C sort | while IFS= read -r f; do
	printf '%s  %s\n' "$(shasum -a 256 < "$f" | cut -d' ' -f1)" "$f"
done | shasum -a 256 | cut -d' ' -f1)

cat > "$dest/pin.yaml" <<PIN
# Written by scripts/sync-core-catalog.sh. The core catalog commit this
# binary carries a copy of, and the SHA-256 of that copy.
url: $url
commit: $commit
sha256: $digest
PIN
echo "core catalog pinned at $commit ($digest)"
