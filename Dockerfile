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
RUN go mod download

COPY . .

# Static-ish build: CGO disabled (modernc.org/sqlite is pure Go, so this
# is safe), trimpath to keep the binary reproducible.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
      -ldflags="-s -w -X main.serverVersion=${VERSION}" \
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
RUN apt-get update \
 && apt-get install --no-install-recommends -y \
      ca-certificates curl tini xz-utils \
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
