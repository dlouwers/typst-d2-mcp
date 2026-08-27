#!/bin/sh
# Install the curated typeface collection into the runtime image.
#
# Variable fonts throughout: typst honours the axes, so one ~1MB file
# covers a whole weight range that would take several megabytes as
# statics — smaller and more capable at once.
#
# Pinned release archives, fetched directly. NOT an install script and
# not "latest": #103 was exactly that mistake, where CI resolved a
# version through a rate-limited API and then silently drifted from the
# image. Bump the versions below deliberately.
#
# Every face is SIL OFL 1.1, which permits redistribution here. The
# licence text ships beside the fonts and fonts/manifest.json records
# the licence per family, so "may we ship this?" stays answerable
# without archaeology.
set -eu

INTER=${FONT_INTER:-4.1}
SOURCE_SERIF=${FONT_SOURCE_SERIF:-4.005R}
SOURCE_SANS=${FONT_SOURCE_SANS:-3.052R}
JETBRAINS_MONO=${FONT_JETBRAINS_MONO:-2.304}

DEST=${FONT_DEST:-/usr/local/share/typst-d2/fonts}
mkdir -p "$DEST"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fetch() { curl -fsSL --retry 3 --retry-delay 2 -o "$2" "$1"; }

echo "==> Inter $INTER"
# Statics. The variable pair reports itself as "Inter Variable", so
# `#set text(font: "Inter")` — the name anyone would actually write —
# would silently substitute. That is the failure this collection exists
# to prevent, so the family has to be callable by its own name.
fetch "https://github.com/rsms/inter/releases/download/v${INTER}/Inter-${INTER}.zip" "$tmp/inter.zip"
unzip -q -j "$tmp/inter.zip" \
  "extras/ttf/Inter-Regular.ttf" "extras/ttf/Inter-Italic.ttf" \
  "extras/ttf/Inter-SemiBold.ttf" "extras/ttf/Inter-Bold.ttf" \
  "LICENSE.txt" -d "$tmp/inter"
cp "$tmp/inter"/Inter-*.ttf "$DEST/"
cp "$tmp/inter/LICENSE.txt" "$DEST/Inter-LICENSE.txt"

echo "==> Source Serif $SOURCE_SERIF"
# Statics, not the variable file. Adobe's variable builds report
# themselves as "Source Serif 4 Variable" and "SourceSans3VF", which
# nobody searching for "source serif" will find — and a family a caller
# cannot name is a family they cannot use. The statics carry the plain
# names. Four weights rather than a continuous axis is the price.
# The asset name drops the R suffix the tag carries.
fetch "https://github.com/adobe-fonts/source-serif/releases/download/${SOURCE_SERIF}/source-serif-${SOURCE_SERIF%R}_Desktop.zip" "$tmp/ss.zip"
unzip -q -j "$tmp/ss.zip" \
  "*/TTF/SourceSerif4-Regular.ttf" "*/TTF/SourceSerif4-It.ttf" \
  "*/TTF/SourceSerif4-Semibold.ttf" "*/TTF/SourceSerif4-Bold.ttf" -d "$tmp/ss"
cp "$tmp/ss"/*.ttf "$DEST/"

echo "==> Source Sans $SOURCE_SANS"
fetch "https://github.com/adobe-fonts/source-sans/releases/download/${SOURCE_SANS}/TTF-source-sans-${SOURCE_SANS}.zip" "$tmp/sa.zip"
unzip -q -j "$tmp/sa.zip" \
  "TTF/SourceSans3-Regular.ttf" "TTF/SourceSans3-It.ttf" \
  "TTF/SourceSans3-Semibold.ttf" "TTF/SourceSans3-Bold.ttf" -d "$tmp/sa"
cp "$tmp/sa"/*.ttf "$DEST/"

echo "==> Adobe OFL texts"
fetch "https://raw.githubusercontent.com/adobe-fonts/source-serif/release/LICENSE.md" "$DEST/SourceSerif-LICENSE.md"
fetch "https://raw.githubusercontent.com/adobe-fonts/source-sans/release/LICENSE.md" "$DEST/SourceSans-LICENSE.md"

echo "==> JetBrains Mono $JETBRAINS_MONO"
fetch "https://github.com/JetBrains/JetBrainsMono/releases/download/v${JETBRAINS_MONO}/JetBrainsMono-${JETBRAINS_MONO}.zip" "$tmp/jb.zip"
unzip -q -j "$tmp/jb.zip" "fonts/variable/*.ttf" "OFL.txt" -d "$tmp/jb"
cp "$tmp/jb"/*.ttf "$DEST/"
cp "$tmp/jb/OFL.txt" "$DEST/JetBrainsMono-OFL.txt"

echo "==> installed:"
du -ch "$DEST"/*.ttf "$DEST"/*.otf 2>/dev/null | tail -1

# The build fails here rather than shipping an image whose manifest
# promises families typst cannot see.
if command -v typst >/dev/null 2>&1; then
  echo "==> families typst resolves from the collection:"
  typst fonts --font-path "$DEST" --ignore-system-fonts --ignore-embedded-fonts | sed 's/^/    /'

  # A manifest that names a family typst cannot resolve is the failure
  # this whole area keeps producing — the server saying one thing and
  # typst doing another. Fail the build rather than ship it.
  if [ -f "$DEST/manifest.json" ]; then
    resolved=$(typst fonts --font-path "$DEST" --ignore-system-fonts --ignore-embedded-fonts)
    missing=0
    # Read line by line: family names contain spaces, and a for-loop
    # over command substitution would split "Source Sans 3" into three.
    sed -n 's/.*"family": *"\([^"]*\)".*/\1/p' "$DEST/manifest.json" | while IFS= read -r fam; do
      echo "$resolved" | grep -qxF "$fam" || { echo "    MISSING: manifest promises \"$fam\""; exit 1; }
    done || missing=1
    [ "$missing" -eq 0 ] || { echo "manifest and reality disagree"; exit 1; }
  fi
fi
