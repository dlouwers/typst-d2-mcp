#!/usr/bin/env bash
# Materialize Stormlantern design-system files into
# internal/web/static/vendor/ — the directory the Go binary embeds.
#
# The result is committed (not gitignored) so the Containerfile and the
# Go test job stay toolchain-free: no bun, no npm, no registry auth at
# image-build time. CI runs this script followed by `git diff
# --exit-code` to catch a forgotten re-sync.
#
# Source is a local checkout of the design-system repo rather than the
# GitHub Packages registry, because the packages are restricted and the
# build would otherwise need a token. Point STORMLANTERN_DS at the
# checkout if it is not the default sibling directory.
#
# htmx is NOT synced here. It is pinned and vendored once by hand
# (currently 2.0.3, matching investor-buddy); bumping it is a deliberate
# act, not a side effect of syncing the design system.

set -euo pipefail

REPO_ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
DS="${STORMLANTERN_DS:-$REPO_ROOT/../stormlantern-design-system}"
DEST="$REPO_ROOT/internal/web/static/vendor"

# Only the components the admin UI actually uses — each one embedded is
# bytes in every binary. Keep in step with the templates.
COMPONENTS=(sl-table sl-card sl-badge sl-alert)

[ -d "$DS" ] || {
  echo "design system not found at $DS" >&2
  echo "clone it or set STORMLANTERN_DS=/path/to/stormlantern-design-system" >&2
  exit 1
}

TOKENS="$DS/tokens/dist/tokens.css"
[ -f "$TOKENS" ] || {
  echo "missing $TOKENS — run 'bun run build' in the design system first" >&2
  exit 1
}

mkdir -p "$DEST"
cp "$TOKENS" "$DEST/stormlantern-tokens.css"
cp "$DS/assets/lantern-mark.svg" "$DEST/lantern-mark.svg"

for component in "${COMPONENTS[@]}"; do
  src="$DS/components/src/$component.js"
  [ -f "$src" ] || { echo "missing component $src" >&2; exit 1; }
  cp "$src" "$DEST/$component.js"
done

echo "synced ${#COMPONENTS[@]} component(s) + tokens from $DS into $DEST"
