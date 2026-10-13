#!/bin/sh
# Copy one release of the core catalog into internal/blueprints/testdata/core,
# the copy the tests read instead of the network, and record its tag in
# internal/blueprints/testdata/core.release. docs/specs/2026-10-13-images-
# follow-tracks.md, "The core catalog follows its newest release".
#
# Usage: scripts/fetch-core-testdata.sh <tag>
#
# The tag is a release, vMAJOR.MINOR.PATCH, so the copy is one the toolkit
# would choose. internal/blueprints/coretest makes the copy a repository
# tagged with it, which the toolkit fetches as it would the real catalog.
# CORE_CATALOG_DIR copies a local checkout instead, for a change made to the
# catalog and the toolkit together, before the catalog's release exists.
set -eu

url=${CORE_CATALOG_URL:-https://github.com/Paisans-Software/paisans-blueprints}
tag=${1:?usage: $0 <release tag, vMAJOR.MINOR.PATCH>}
echo "$tag" | grep -Eq '^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' ||
	{ echo "$tag is not a release tag, vMAJOR.MINOR.PATCH" >&2; exit 2; }

root=$(git rev-parse --show-toplevel)
dest=$root/internal/blueprints/testdata/core
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

if [ -n "${CORE_CATALOG_DIR:-}" ]; then
	src=$CORE_CATALOG_DIR
else
	git -c advice.detachedHead=false clone --quiet --depth 1 --branch "$tag" "$url" "$work/catalog"
	src=$work/catalog
fi

name=$(sed -n 's/^name: *//p' "$src/catalog.yaml")
[ "$name" = core ] || { echo "$src is the catalog named '$name', not core" >&2; exit 1; }

rm -rf "$dest"
mkdir -p "$dest/blueprints"
cp "$src/catalog.yaml" "$src/foundation.yaml" "$dest/"
cp -R "$src/blueprints/." "$dest/blueprints/"
echo "$tag" > "$root/internal/blueprints/testdata/core.release"
