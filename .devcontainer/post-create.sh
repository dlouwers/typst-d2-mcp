#!/usr/bin/env bash
# Only what no trustworthy devcontainer feature provides.
set -euo pipefail

# One of the feature installs leaves /tmp as root:root 0755 instead of
# the standard sticky 1777. That breaks more than it looks: `go build`
# creates its work directory under /tmp, so without this the container
# cannot compile anything as the vscode user.
sudo chmod 1777 /tmp

D2_VERSION="v0.7.1"
TYPST_VERSION="v0.14.2"

# d2 and typst are the compile pipeline's two external binaries, pinned
# to the versions ../Dockerfile ships, so a local compile matches what
# CI and the cluster produce.
#
# Both archives are extracted a single member at a time. The d2 tarball
# is built on macOS and carries AppleDouble "._" entries, which a
# wholesale extraction as a non-root user fails on.
echo "==> d2 ${D2_VERSION}"
tmp=$(mktemp -d)
curl -fsSL "https://github.com/terrastruct/d2/releases/download/${D2_VERSION}/d2-${D2_VERSION}-linux-amd64.tar.gz" \
  | tar -xz -C "$tmp" --strip-components=2 "d2-${D2_VERSION}/bin/d2"
sudo install -m 0755 "$tmp/d2" /usr/local/bin/d2
rm -rf "$tmp"

echo "==> typst ${TYPST_VERSION}"
tmp=$(mktemp -d)
curl -fsSL "https://github.com/typst/typst/releases/download/${TYPST_VERSION}/typst-x86_64-unknown-linux-musl.tar.xz" \
  | tar -xJ -C "$tmp" --strip-components=1 "typst-x86_64-unknown-linux-musl/typst"
sudo install -m 0755 "$tmp/typst" /usr/local/bin/typst
rm -rf "$tmp"

# sqlite3 for inspecting auth.sqlite — the app's own driver is pure Go,
# so the CLI is not present anywhere otherwise. Plain apt rather than the
# devcontainers-extra apt-get-packages feature: that one is built on
# nanolayer, which leaves /tmp unusable (see devcontainer.json).
echo "==> sqlite3"
sudo apt-get update -qq
sudo apt-get install -y -qq --no-install-recommends sqlite3

# bun runs the Playwright suite. Same reasoning as sqlite3: the
# community feature is a nanolayer wrapper. This is the installer the
# experiments devcontainer uses.
echo "==> bun"
curl -fsSL https://bun.sh/install | bash > /dev/null
sudo ln -sf "$HOME/.bun/bin/bun" /usr/local/bin/bun
sudo ln -sf "$HOME/.bun/bin/bunx" /usr/local/bin/bunx

# golangci-lint is pinned to the version CI runs (what
# golangci-lint-action resolves `latest` to at its pinned version). v1
# and v2 disagree about config format and default linters, so an
# unpinned install would mean a clean local run does not imply a clean
# CI run. Moving both to 2.x is a separate job.
echo "==> Go tools"
go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8
go install golang.org/x/tools/gopls@latest
go install golang.org/x/vuln/cmd/govulncheck@latest

echo "==> go mod download"
go mod download

echo "✅ $(go version | cut -d' ' -f3) · d2 $(d2 --version) · typst $(typst --version | cut -d' ' -f2) · golangci-lint $(golangci-lint version 2>&1 | grep -oE 'version [0-9.]+' | head -1)"
