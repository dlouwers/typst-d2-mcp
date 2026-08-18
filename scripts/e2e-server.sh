#!/usr/bin/env bash
# Start typst-d2-mcp for the Playwright suite.
#
# Configuration is deliberately fixed and fake: a throwaway SQLite file
# and workspace under a temp dir, a known session-signing key so tests
# can forge an admin session without driving GitHub's OAuth flow, and a
# GitHub client id/secret that are never used (no test reaches the
# token endpoint).
#
# PUBLIC_URL is http, not https, so the session cookie is issued without
# the Secure flag — a Secure cookie would be dropped by the browser over
# plain http and every test would fail at the sign-in step.
#
# TYPST_D2_MCP_ADMINS is the login the tests forge sessions for.

set -euo pipefail

REPO_ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)

# Fixed path, not mktemp: the Playwright suite needs to find auth.sqlite
# to seed a signed-in user, because the quota and key actions only render
# for accounts that have completed the OAuth flow. Wiped on every start,
# so a run never inherits the previous run's rows.
STATE="${E2E_STATE_DIR:-/tmp/typst-d2-mcp-e2e}"
rm -rf "$STATE"
mkdir -p "$STATE"

# Prebuilt binary wins (lets a machine without a Go toolchain run the
# suite against a binary built elsewhere); otherwise build here.
BIN="${TYPST_D2_MCP_E2E_BIN:-}"
if [ -z "$BIN" ]; then
  command -v go >/dev/null || {
    echo "no go toolchain and no TYPST_D2_MCP_E2E_BIN set" >&2
    exit 1
  }
  BIN="$STATE/typst-d2-mcp"
  go build -o "$BIN" "$REPO_ROOT/cmd/typst-d2-mcp"
fi

export TYPST_D2_MCP_TRANSPORT=http
export TYPST_D2_MCP_ADDR="127.0.0.1:${E2E_PORT:-18080}"
export TYPST_D2_MCP_METRICS_ADDR="127.0.0.1:${E2E_METRICS_PORT:-19090}"
export TYPST_D2_MCP_AUTH=github
export TYPST_D2_MCP_DB="$STATE/auth.sqlite"
export TYPST_D2_MCP_WORKSPACE="$STATE/workspaces"
export TYPST_D2_MCP_PUBLIC_URL="http://admin.test:${E2E_PORT:-18080}"
export TYPST_D2_MCP_GITHUB_CLIENT_ID=e2e-client
export TYPST_D2_MCP_GITHUB_CLIENT_SECRET=e2e-secret
export TYPST_D2_MCP_ADMINS="${E2E_ADMIN:-e2eadmin}"
export TYPST_D2_MCP_SESSION_KEY="${E2E_SESSION_KEY:-e2e-fixed-session-key}"
export TYPST_D2_MCP_LOG_FORMAT=text

exec "$BIN"
