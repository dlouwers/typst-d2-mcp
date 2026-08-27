# syntax=docker/dockerfile:1
#
# Production image for typst-d2-mcp.
#
# Designed for the hosted free-tier deployment: HTTP transport, GitHub
# OAuth, SQLite-backed quota. Self-hosted operators can run the same
# image with TYPST_D2_MCP_AUTH=none and get the anonymous experience.

# --- build stage ----------------------------------------------------------
FROM golang:1.25-bookworm AS build

WORKDIR /src

# Download deps separately so the layer is reused across source-only edits.
COPY go.mod go.sum ./

# github.com/dlouwers/stormlantern-design-system is a private module (#80), so
# this needs a credential. It arrives as a BuildKit secret, which is mounted
# for the life of the RUN and never written to a layer.
#
# The credential is passed through GIT_CONFIG_COUNT/KEY/VALUE rather than
# `git config --global`, deliberately: the latter writes the token into
# /root/.gitconfig, and that file IS part of the layer. Env vars set on a
# single RUN are not.
#
# A build without the secret fails here rather than later, which is the right
# place: without it the module cannot be resolved at all. Locally:
#   podman build --secret id=gh_token,env=GH_TOKEN .
RUN --mount=type=secret,id=gh_token \
    GOPRIVATE=github.com/dlouwers/* \
    GIT_CONFIG_COUNT=1 \
    GIT_CONFIG_KEY_0="url.https://x-access-token:$(cat /run/secrets/gh_token)@github.com/.insteadOf" \
    GIT_CONFIG_VALUE_0="https://github.com/" \
    go mod download

COPY . .

# Static-ish build: CGO disabled (modernc.org/sqlite is pure Go, so this
# is safe), trimpath to keep the binary reproducible.
ARG VERSION=dev
# Passed by .github/workflows/image.yml. Declared here or the build-args
# are silently dropped — which is what used to happen, leaving the binary
# with no way to name the commit it was built from.
ARG GIT_SHA=unknown
ARG BUILD_TIME=unknown
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
      -ldflags="-s -w -X main.serverVersion=${VERSION} -X main.gitSHA=${GIT_SHA} -X main.buildTime=${BUILD_TIME}" \
      -o /out/typst-d2-mcp \
      ./cmd/typst-d2-mcp

# --- runtime stage --------------------------------------------------------
FROM debian:bookworm-slim AS runtime

# Pinned upstream versions; bump these together with the devcontainer.
ARG D2_VERSION=v0.7.1
ARG TYPST_VERSION=v0.14.2

# curl for the HEALTHCHECK probe, ca-certificates so the typst child
# trusts TLS roots if it needs them, tini as a minimal PID 1 so signals
# reach the Go process cleanly.
#
# Fonts, because typst ships only three families and none of them is a
# proportional sans — so a document asking for one got a silent
# substitution and a PDF that looks fine and is wrong. DejaVu brings a
# proportional sans and serif; Liberation adds faces metric-compatible
# with Arial / Times / Courier, which is what a document written
# elsewhere will name. Both are freely redistributable and together add
# roughly 2.5MB compressed. An organisation's own faces belong in its
# workspace fonts/ directory, where the licensing stays with the tenant.
RUN apt-get update \
 && apt-get install --no-install-recommends -y \
      ca-certificates curl tini xz-utils unzip \
      fonts-dejavu-core fonts-liberation2 \
 && rm -rf /var/lib/apt/lists/*

# Install d2 via its official install script, pinned to D2_VERSION.
RUN curl -fsSL "https://github.com/terrastruct/d2/releases/download/${D2_VERSION}/d2-${D2_VERSION}-linux-amd64.tar.gz" \
      | tar -xz -C /tmp \
 && cp "/tmp/d2-${D2_VERSION}/bin/d2" /usr/local/bin/d2 \
 && rm -rf /tmp/d2-* \
 && d2 --version

# Install typst from the official release tarball.
RUN curl -fsSL "https://github.com/typst/typst/releases/download/${TYPST_VERSION}/typst-x86_64-unknown-linux-musl.tar.xz" \
      | tar -xJ -C /usr/local/bin --strip-components=1 \
 && typst --version

# A curated set of open-licensed typefaces, so a document has something
# to be set in beyond the four faces typst embeds. See the script for
# why these families and why statics over variable builds; the short
# version is that a family must be callable by the name people write.
#
# Versions are pinned and archives fetched directly — not an install
# script resolving "latest", which is the mistake #103 was about.
ARG FONT_INTER=4.1
ARG FONT_SOURCE_SERIF=4.005R
ARG FONT_SOURCE_SANS=3.052R
ARG FONT_JETBRAINS_MONO=2.304
COPY fonts/manifest.json /usr/local/share/typst-d2/fonts/manifest.json
COPY scripts/install-fonts.sh /tmp/install-fonts.sh
RUN sh /tmp/install-fonts.sh && rm /tmp/install-fonts.sh

# House document templates, resolved by typst as `@house/templates:0.1.0`.
#
# typst looks for local packages under $XDG_DATA_HOME/typst/packages/
# <namespace>/<name>/<version>, so the directory layout *is* the import
# path. The namespace is an owner slug: one baked-in owner today, per
# organisation later, without the import path in anyone's document
# changing.
#
# The templates are embedded in the binary (package ./templates) and
# seeded onto the data volume on startup, so XDG_DATA_HOME points at the
# volume rather than a read-only image path. That is what lets house style
# change on the volume without an image rebuild (#63 step 2). The seed
# target sits beside — not inside — the workspace tree, so the sweeper
# never purges it. typst's writable @preview cache uses $HOME (XDG_CACHE_HOME),
# unaffected by this.
ENV XDG_DATA_HOME=/var/lib/typst-d2-mcp/data

# Drop privileges. UID/GID match the convention used by distroless's
# "nonroot" so swapping bases later is painless.
RUN groupadd --system --gid 65532 nonroot \
 && useradd --system --uid 65532 --gid nonroot --home /home/nonroot --create-home nonroot

# State directory: the workspace tree and SQLite DB live here. Mount
# this as a volume in production so per-user files and quota survive
# container restarts.
RUN mkdir -p /var/lib/typst-d2-mcp && chown nonroot:nonroot /var/lib/typst-d2-mcp
VOLUME /var/lib/typst-d2-mcp

COPY --from=build /out/typst-d2-mcp /usr/local/bin/typst-d2-mcp

USER nonroot
WORKDIR /home/nonroot

# Sensible defaults for the hosted shape. Operators override AUTH +
# credentials at run time. Quota stays at the documented 1/day; raise
# via TYPST_D2_MCP_QUOTA_PER_DAY when shipping the paid tier.
ENV TYPST_D2_MCP_TRANSPORT=http \
    TYPST_D2_MCP_ADDR=:8080 \
    TYPST_D2_MCP_PATH=/mcp \
    TYPST_D2_MCP_METRICS_ADDR=:9090 \
    TYPST_D2_MCP_WORKSPACE=/var/lib/typst-d2-mcp/workspaces \
    TYPST_D2_MCP_DB=/var/lib/typst-d2-mcp/auth.sqlite \
    TYPST_D2_MCP_LOG_FORMAT=json \
    TYPST_D2_MCP_LOG_LEVEL=info

EXPOSE 8080 9090

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/typst-d2-mcp"]
