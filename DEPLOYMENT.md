# Deploying `typst-d2-mcp`

This guide is the minimum recipe for running the HTTP server in production
with GitHub-OAuth-issued API keys and per-user quota. For the stdio /
local-CLI experience see the main `README.md`.

## TL;DR

```sh
docker build -t typst-d2-mcp .
docker run -d --name typst-d2-mcp \
  -p 8080:8080 \
  -v typst-d2-mcp-state:/var/lib/typst-d2-mcp \
  -e TYPST_D2_MCP_AUTH=github \
  -e TYPST_D2_MCP_PUBLIC_URL=https://your.host \
  -e TYPST_D2_MCP_GITHUB_CLIENT_ID=Iv1.xxxxxxxxxxxx \
  -e TYPST_D2_MCP_GITHUB_CLIENT_SECRET=xxxxxxxxxxxxxxxx \
  typst-d2-mcp
```

That gives you a hosted MCP server with a 1-compile-per-UTC-day quota per
authenticated user. Users sign up by visiting `https://your.host/login`
and configure their MCP client with the resulting `Authorization: Bearer
<key>` header.

## Setting up the GitHub OAuth app

1. <https://github.com/settings/developers> → **New OAuth App**.
2. **Application name**: anything (shown to users on the consent screen).
3. **Homepage URL**: `https://your.host`.
4. **Authorization callback URL**: `https://your.host/auth/github/callback`
   (must match `TYPST_D2_MCP_PUBLIC_URL + /auth/github/callback` exactly).
5. After creating: copy the **Client ID** into `TYPST_D2_MCP_GITHUB_CLIENT_ID`,
   generate a **Client secret** and put it in `TYPST_D2_MCP_GITHUB_CLIENT_SECRET`.

The OAuth scope requested is `read:user user:email` — read-only.

## Environment variables (full reference)

| Variable | Default | Purpose |
| --- | --- | --- |
| `TYPST_D2_MCP_TRANSPORT` | `stdio` (image overrides to `http`) | `stdio` or `http`. |
| `TYPST_D2_MCP_ADDR` | `:8080` | HTTP listen address. |
| `TYPST_D2_MCP_PATH` | `/mcp` | URL path the MCP endpoint is served at. |
| `TYPST_D2_MCP_WORKSPACE` | (set in image) | Root for per-user workspace dirs. |
| `TYPST_D2_MCP_AUTH` | `none` | `none` (anonymous, unlimited) or `github`. |
| `TYPST_D2_MCP_DB` | (set in image) | SQLite path for users + api_keys + compiles. |
| `TYPST_D2_MCP_PUBLIC_URL` | _required for github_ | Externally reachable base URL (scheme + host). |
| `TYPST_D2_MCP_GITHUB_CLIENT_ID` | _required for github_ | OAuth Client ID. |
| `TYPST_D2_MCP_GITHUB_CLIENT_SECRET` | _required for github_ | OAuth Client secret. |
| `TYPST_D2_MCP_QUOTA_PER_DAY` | `1` | Compiles per UTC day per non-anonymous user. `0` disables. |
| `TYPST_D2_MCP_COMPILE_TIMEOUT` | `30s` | Per-compile budget (parses Go duration strings). `0` defers to caller. |
| `TYPST_D2_MCP_MAX_INPUT_BYTES` | `1048576` (1 MiB) | Cap on `put_file` and compile input sizes. |
| `TYPST_D2_MCP_LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error`. |
| `TYPST_D2_MCP_LOG_FORMAT` | `json` in http, `text` in stdio | `json` or `text`. |

## docker-compose example

```yaml
services:
  typst-d2-mcp:
    image: typst-d2-mcp
    build: .
    restart: unless-stopped
    ports: ["8080:8080"]
    environment:
      TYPST_D2_MCP_AUTH: github
      TYPST_D2_MCP_PUBLIC_URL: ${PUBLIC_URL}
      TYPST_D2_MCP_GITHUB_CLIENT_ID: ${GITHUB_CLIENT_ID}
      TYPST_D2_MCP_GITHUB_CLIENT_SECRET: ${GITHUB_CLIENT_SECRET}
    volumes:
      - typst-d2-mcp-state:/var/lib/typst-d2-mcp

volumes:
  typst-d2-mcp-state:
```

Front this with a TLS-terminating reverse proxy (nginx, Caddy, traefik) so
the callback URL is `https://...`. Bearer tokens must travel over TLS.

## Persistence

The state volume at `/var/lib/typst-d2-mcp` holds:

- `workspaces/<user_id>/` — each authenticated user's sandboxed working
  tree, including compiled PDFs. Files do **not** auto-expire today
  (sub-issue [#5](https://github.com/dlouwers/typst-d2-mcp/issues/2)
  will add TTL purge); operators should mount this on a volume with
  reasonable size limits and periodically prune.
- `auth.sqlite` — users, hashed API keys, and the per-day compile
  counter. Losing this file logs everyone out and resets quota.

Both paths are derived from env vars at startup; override them if you
prefer a different layout.

## Hardening notes

These are **not** wired into the image today. They belong to the
deployment, and the documentation here is so they aren't forgotten.

- **Network namespace / egress firewall.** The `typst` child fetches
  `@preview/*` packages from `typst.app` by default. For the hosted
  free tier this is a code-execution / SSRF risk: a malicious `.typ`
  could pull arbitrary packages. Run the container in a network
  namespace that denies outbound traffic except to GitHub's OAuth
  endpoints, or pre-populate the package cache and pass
  `--package-cache-path` via a wrapper script. The `@preview/based`
  package the preprocessor needs can be baked into the image.
- **Read-only root filesystem.** Compose users can add
  `read_only: true` with `tmpfs: [/tmp]` and the persistent volume
  mounted writable. The container otherwise needs no write access to
  its own filesystem.
- **Resource limits.** Pair the in-process compile timeout with
  container-level CPU and memory caps (`--cpus`, `--memory`) so a
  single compile can't starve neighbours.
- **TLS.** The image speaks plain HTTP and expects a reverse proxy in
  front of it. The Bearer-auth middleware sets `Secure` on the OAuth
  state cookie only when `request.TLS != nil`, so the proxy must
  terminate TLS and proxy to HTTP, not the other way around.

## Verifying a deployment

```sh
# Health
curl https://your.host/healthz

# Visit /login in a browser to sign in with GitHub and grab a key.
# Then, with the key:
curl -sS https://your.host/mcp \
  -H "Authorization: Bearer ttd2_..." \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"curl","version":"0"},"capabilities":{}}}'
```

Logs are JSON on stderr; pipe them through `jq` or your log aggregator
of choice.
