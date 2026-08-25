#!/usr/bin/env bash
# Materialize Stormlantern design-system files into
# internal/web/static/vendor/ — the directory the Go binary embeds.
#
# The result is committed (not gitignored) so the Containerfile and the
# Go test job stay toolchain-free: no bun, no npm, no registry auth at
# image-build time. CI runs this script followed by `git diff
# --exit-code` to catch a forgotten re-sync.
#
# That CI check needs read access to the (private) design-system repo,
# via a GitHub App rather than a personal token — the token is minted per
# run and expires in an hour, and it belongs to the account rather than
# to a person whose access it would otherwise inherit. To enable it:
#
#   1. Create a GitHub App under the dlouwers account. No webhook. Give
#      it exactly one permission: Repository -> Contents -> Read-only.
#   2. Install it, selecting only stormlantern-design-system.
#   3. Generate a private key (downloads a .pem).
#   4. In dlouwers/typst-d2-mcp:
#        gh variable set DS_APP_ID --body "<the app id>"
#        gh secret set DS_APP_PRIVATE_KEY < /path/to/key.pem
#
# Until DS_APP_ID exists the three steps in ci.yml skip, so a fork or a
# fresh clone builds without them.
#
# Source is a local checkout of the design-system repo rather than the
# GitHub Packages registry, because the packages are restricted and the
# build would otherwise need a token. Point STORMLANTERN_DS at the
# checkout if it is not the default sibling directory.
#
# NOTHING HERE PINS A VERSION. This copies from whatever commit the
# checkout is on, and CI checks out the design system's default branch
# HEAD, so the vendored bytes are "whatever main was when someone last
# ran this". There is no tag, version or SHA recorded anywhere in this
# repo, which is why staleness is invisible rather than reported: the CI
# check only fires once main moves past what is committed here, and if
# nothing pushes to this repo in the meantime, nobody finds out.
#
# That is how this repo ended up serving ten WCAG failures for a day
# after they were fixed upstream (#80).
#
# A provenance stamp written into vendor/ was considered and rejected:
# recording the design system's HEAD would fail this repo's CI on every
# unrelated commit there, and recording its nearest tag would be a lie
# whenever main is ahead of it. The real fix is to consume a versioned
# artifact instead of copying files —
# dlouwers/stormlantern-design-system#21 proposes publishing a Go module,
# after which go.mod pins the version and `go mod vendor` verifies it.
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
