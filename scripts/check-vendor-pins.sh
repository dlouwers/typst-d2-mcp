#!/usr/bin/env bash
# Check the hand-vendored front-end assets against their registry of origin.
#
# internal/web/vendorpins_test.go already proves the manifest matches the
# committed bytes, offline and on every `go test`. Mirrors
# dlouwers/investor-buddy#313; see #85. That is the guarantee that
# matters day to day, and it is deliberately not this script's job.
#
# This adds the two things a hash of our own file cannot tell us:
#
#   1. Correspondence — the bytes we vendored really are what that package
#      published at that version. Without this, "version: 2.0.3" is only an
#      assertion by whoever last copied a file in.
#   2. Staleness — whether a newer version exists. Reported, not enforced:
#      bumping a library that interprets every server response should stay a
#      conscious act, and a job that fails until you upgrade would make it a
#      chore performed under pressure instead.
#
# Exit 1 only on a correspondence failure, which means either the manifest is
# wrong or the vendored file is not what it claims to be.

set -euo pipefail

REPO_ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
MANIFEST="$REPO_ROOT/internal/web/vendorpins.json"
status=0

while IFS=$'\t' read -r file pkg version sha url; do
  printf '%s (%s@%s)\n' "$file" "$pkg" "$version"

  upstream=$(mktemp)
  if ! curl -sfL --max-time 30 "$url" -o "$upstream"; then
    echo "  ::error::could not fetch $url"
    status=1; rm -f "$upstream"; continue
  fi

  got=$(sha256sum "$upstream" | cut -d' ' -f1)
  rm -f "$upstream"
  if [ "$got" != "$sha" ]; then
    echo "  ::error::$file does not match $pkg@$version upstream"
    echo "    manifest: $sha"
    echo "    upstream: $got"
    status=1
  else
    echo "  bytes match upstream ✓"
  fi

  latest=$(curl -sfL --max-time 30 "https://registry.npmjs.org/$pkg" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["dist-tags"]["latest"])' 2>/dev/null || echo "")
  if [ -n "$latest" ] && [ "$latest" != "$version" ]; then
    echo "  ::notice::$pkg $version is behind $latest — bump is a deliberate act, see vendorpins.json"
  elif [ -n "$latest" ]; then
    echo "  latest published ✓"
  fi
done < <(python3 -c '
import json
pins = json.load(open("'"$MANIFEST"'"))
for f, p in pins.items():
    print("\t".join([f, p["package"], p["version"], p["sha256"], p["url"]]))
')

exit $status
