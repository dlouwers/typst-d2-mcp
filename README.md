# Typst D2 Integration

[![GitHub Release](https://img.shields.io/github/v/release/dlouwers/typst-d2-mcp?style=flat-square&logo=github)](https://github.com/dlouwers/typst-d2-mcp/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/dlouwers/typst-d2-mcp?style=flat-square&logo=go)](https://go.dev/)
[![CI Status](https://img.shields.io/github/actions/workflow/status/dlouwers/typst-d2-mcp/release.yml?style=flat-square&logo=github-actions&label=release)](https://github.com/dlouwers/typst-d2-mcp/actions/workflows/release.yml)
[![License](https://img.shields.io/github/license/dlouwers/typst-d2-mcp?style=flat-square)](LICENSE)
[![Issues](https://img.shields.io/github/issues/dlouwers/typst-d2-mcp?style=flat-square)](https://github.com/dlouwers/typst-d2-mcp/issues)

Render [D2 diagrams](https://d2lang.com) in [Typst](https://typst.app) documents with two tools:

## Tools

### 1. typst-d2-prep (CLI Preprocessor)

- ✅ **Zero filesystem clutter** - No intermediate `.svg` files created
- ✅ **Full D2 feature support** - All layouts (ELK, TALA, dagre), themes, sketch mode
- ✅ **Inline syntax** - D2 code embedded directly in `.typ` files
- ✅ **Simple workflow** - One command replaces `typst compile`

### 2. typst-d2-mcp (MCP Server)

- 🤖 **AI Assistant Integration** - Works with Claude Desktop, Cline, OpenCode, and other MCP clients
- 📝 **Encourages Visual Documentation** - AI creates Typst documents with embedded D2 diagrams
- ✨ **Focused tool surface**: `compile_typst_with_d2` to compile, `put_file` to place sources when the client cannot write to the server's filesystem
- 🌐 **Runs locally or hosted** - stdio for a desktop client, or an HTTP server with GitHub sign-in, per-user workspaces and quota (see [Hosted mode](#hosted-mode))
- 🎯 **Best for**: Generating technical documentation, architecture docs, and illustrated guides

## Quick Start

### CLI Preprocessor (typst-d2-prep)

#### Installation

```bash
# Option 1: Homebrew (macOS/Linux)
brew install dlouwers/tap/typst-d2-prep

# Option 2: Download pre-built binary from GitHub Releases
# https://github.com/dlouwers/typst-d2-mcp/releases

# Option 3: Build from source
git clone https://github.com/dlouwers/typst-d2-mcp.git
cd typst-d2-mcp
go build -o typst-d2-prep ./cmd/typst-d2-prep

# Option 4: Install with go install
go install github.com/dlouwers/typst-d2-mcp/cmd/typst-d2-prep@latest

# Verify installation
typst-d2-prep version

# Verify D2 is installed
d2 --version
# If not: curl -fsSL https://d2lang.com/install.sh | sh -s --
```

#### Usage

**Your Typst file (document.typ):**

```typst
= Architecture Diagram

#d2[
  client -> server -> database
]

#d2(layout: "elk", theme: "0")[
  user: User {shape: person}
  app: Application
  user -> app: Uses
]
```

**Compile:**

```bash
typst-d2-prep compile document.typ
# ✅ Creates document.pdf with embedded diagrams
```

### MCP Server (typst-d2-mcp)

The MCP server provides AI assistants with tools to render D2 diagrams and compile Typst documents.

#### Installation

```bash
# Option 1: Homebrew (macOS/Linux)
brew install dlouwers/tap/typst-d2-mcp

# Option 2: Download pre-built binary from GitHub Releases
# https://github.com/dlouwers/typst-d2-mcp/releases

# Option 3: Build from source
git clone https://github.com/dlouwers/typst-d2-mcp.git
cd typst-d2-mcp
go build -o typst-d2-mcp ./cmd/typst-d2-mcp

# Option 4: Install with go install
go install github.com/dlouwers/typst-d2-mcp/cmd/typst-d2-mcp@latest
```

#### Claude Desktop Configuration

Add to your Claude Desktop config file:

**macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
**Linux**: `~/.config/Claude/claude_desktop_config.json`

```json
{
  "mcpServers": {
    "typst-d2": {
      "command": "/opt/homebrew/bin/typst-d2-mcp"
    }
  }
}
```

**Note:** If installed via Homebrew, the binary is at `/opt/homebrew/bin/typst-d2-mcp` (macOS ARM) or `/usr/local/bin/typst-d2-mcp` (macOS Intel/Linux). Adjust path if built from source.

#### Available Tools

**compile_typst_with_d2** - Compile Typst documents with embedded D2 diagrams

The primary tool, and the one whose description steers AI assistants toward
rich, visual documentation.

**Input:**
- `file_path` (required): Typst source file (.typ) containing #d2[...] blocks.
  Absolute in local stdio mode; **workspace-relative** in hosted mode, where
  paths that escape the caller's workspace are rejected.

**Output:**
- A success message, plus a `resource_link` to the PDF
  (`typst-d2://pdf/...`, readable with the standard MCP `resources/read`).
  In hosted mode the result also carries a short-lived HTTPS download URL, for
  clients that render links but not resource links.

**put_file** - Write a file into the server's active workspace

Only needed when your client cannot write to the server's filesystem — that is,
when talking to a hosted server over HTTP. Against a local stdio server, use
your own editor or filesystem tools instead; there is no reason to push file
contents through the MCP channel.

**Input:**
- `path` (required): destination, workspace-relative in hosted mode
- `content` (required), `encoding` (optional): `utf8` (default) or `base64`

**The tool's description guides AI assistants to:**
- Use D2 diagrams for system architectures, flowcharts, ERDs, and technical illustrations
- Embed diagrams directly using #d2[...] syntax
- Support all D2 features (layouts, themes, sketch mode)
- Create clean documentation with no intermediate files

#### Example Usage

```
User: "Create documentation for a microservices architecture"

AI assistant:
1. Creates Typst document with headings and content
2. Embeds D2 diagrams using #d2[...] blocks:
   - System architecture overview
   - Service interaction diagrams
   - Database schema (ERD)
3. Saves to .typ file
4. Calls compile_typst_with_d2 with file path
5. Returns PDF with embedded diagrams
```

```
User: "Document this API flow: client -> gateway -> auth -> service -> database"

AI assistant:
1. Creates Typst document explaining the API flow
2. Adds D2 diagram:
   #d2(layout: "elk")[
     client: Client {shape: person}
     gateway: API Gateway
     auth: Auth Service
     service: Business Service
     database: Database {shape: cylinder}
     
     client -> gateway: HTTPS
     gateway -> auth: Verify token
     auth -> service: Authorized request
     service -> database: Query
   ]
3. Saves and compiles

Result: Professional documentation with visual diagram
```

## Hosted mode

The same binary runs as an HTTP server, which is how you would offer it to
people who are not on your machine. Run your own — this repository does not
point at a shared instance.

In this mode the server:

- serves the MCP Streamable HTTP transport at **`/mcp`** (configurable via
  `TYPST_D2_MCP_PATH`). That URL is the only thing a user needs: the server also
  acts as an OAuth 2.1 authorization server, so a client like Claude.ai
  discovers it from the `401` challenge, registers itself, and walks the user
  through **GitHub sign-in** — no token to copy and paste
- gives each authenticated user a **sandboxed workspace**; `file_path` and
  `put_file` paths are resolved inside it, and traversal is rejected
- meters compiles with a **per-day quota**, with a per-user override
- mints **short-lived download links** for compiled PDFs, for clients that
  render HTTPS links but not MCP resource links
- offers an **admin UI at `/admin`** for the operator: invite users, set or
  remove a user's quota, revoke access and API keys, delete a user, and read an
  audit log of all of it
- **garbage-collects** expired links and aged-out workspace files on a timer

Access is invite-only once you name an administrator, and an operator manages
who gets in from `/admin`.

See **[DEPLOYMENT.md](DEPLOYMENT.md)** for the full recipe: GitHub OAuth app
setup, every environment variable, docker-compose and Kubernetes manifests,
schema migrations, and hardening notes.

## How It Works

1. **Parse** - Scans your `.typ` file for `#d2[...]` blocks
2. **Extract** - Pulls out D2 code and options from each block
3. **Render** - For each diagram, calls `d2 - -` (stdin→stdout streaming)
4. **Encode** - Converts SVG to base64
5. **Import** - Adds `#import "@preview/based:0.2.0": decode64` at the top
6. **Replace** - Substitutes `#d2[...]` with `#image(decode64("..."), format: "svg")`
7. **Compile** - Runs `typst compile` on the processed document
8. **Cleanup** - Deletes temporary `.typ` file, keeps only your original + PDF

**Result:** Your PDF contains embedded SVGs, no leftover files, clean filesystem.

## Requirements

- **Go 1.25+** (for building from source, optional — see `go.mod` for the exact directive)
- **D2 CLI** installed and in PATH: https://d2lang.com/tour/install
- **Typst 0.14.2+**: https://github.com/typst/typst
- **Typst `based` package**: Automatically imported (no manual setup needed)

## Syntax Reference

### Basic Diagram

```typst
#d2[
  x -> y -> z
]
```

### With Options

```typst
#d2(layout: "elk", theme: "0", sketch: "true")[
  direction: right
  
  user: User {
    shape: person
  }
  
  app: Application {
    ui: Web Interface
    api: REST API
  }
  
  user -> app.ui: Browse
]
```

### Available Options

| Option | Values | Default | Description |
|--------|--------|---------|-------------|
| `layout` | `"elk"`, `"tala"`, `"dagre"` | `"elk"` | Layout engine |
| `theme` | `"0"`-`"200"` | default | Theme ID |
| `sketch` | `"true"`, `"false"` | `"false"` | Hand-drawn style |
| `center` | `"true"`, `"false"` | `"false"` | Center in viewbox |
| `scale` | number or `"auto"` | `"auto"` | Scale factor |
| `pad` | Typst length (e.g., `"10pt"`) | `none` | Padding around diagram |

## Examples

See `example.typ` for a complete demo with multiple diagrams, including:
- Simple connections
- Styled diagrams with ELK layout, themes, and sketch mode
- Complex architecture with multi-level containers

Compile it:
```bash
typst-d2-prep compile example.typ
```

## Technical Details

### Base64 Encoding with `based` Package

The preprocessor uses the [`based`](https://typst.app/universe/package/based) package to decode base64-encoded SVG data:

```typst
#import "@preview/based:0.2.0": decode64

#image(decode64("PD94bWwgdmVyc2lvbj0iMS4wIj..."), format: "svg")
```

This approach:
- ✅ Avoids escaping issues with raw SVG strings
- ✅ Works reliably with all SVG content
- ✅ Uses an official Typst package (no custom code)
- ✅ Handles binary data correctly

See [IMPLEMENTATION.md](IMPLEMENTATION.md) for detailed technical documentation.

## Comparison to Alternatives

| Feature | typst-d2 (this) | Manual workflow | WASM plugin |
|---------|----------------|-----------------|-------------|
| **Setup** | Install script + D2 | Install D2 | N/A (impossible) |
| **Syntax** | `#d2[code]` | `#image("out.svg")` | `#d2[code]` |
| **Filesystem** | ✅ Clean | ❌ SVG files everywhere | ✅ Clean |
| **D2 Features** | ✅ 100% | ✅ 100% | ❌ 0% |
| **Build** | `typst-d2-prep compile` | `d2 ... && typst compile` | `typst compile` |

## Troubleshooting

### "d2 command not found"

Install D2:
```bash
curl -fsSL https://d2lang.com/install.sh | sh -s --
```

### "No D2 diagrams found"

Make sure you're using the `#d2[...]` syntax (not `#import "lib.typ"`).

## Development

### Building from Source

```bash
git clone https://github.com/dlouwers/typst-d2-mcp.git
cd typst-d2-mcp
go build -o typst-d2-prep ./cmd/typst-d2-prep
```

### Running Tests

```bash
go test ./...
```

Browser-level checks for the admin UI live in `playwright/` and run in CI. They
cover what a Go test cannot reach — cookie `SameSite` behaviour, htmx swaps, and
that every action still works with JavaScript disabled:

```bash
cd playwright && bun install && bunx playwright install chromium
bunx playwright test
```

The suite starts the server itself; no running instance is needed.

### Using Devcontainer

The project includes a devcontainer — a plain Debian base with everything added
as devcontainer features, so each tool's version is a one-line change:

- Go 1.26 (via the Go feature; `go.mod` keeps its own language-version directive)
- D2 and Typst CLIs, pinned to the versions the production image ships
- golangci-lint, pinned to the version CI runs, plus gopls and govulncheck
- kubectl and sqlite3, for inspecting a running deployment
- Node and bun, for the Playwright suite

Open in VS Code with the Dev Containers extension for instant setup.

## Limitations

- **No watch mode yet** - Currently only supports single compilation
- **No incremental builds** - Every compile re-renders all diagrams

## Future Improvements

- [ ] Watch mode with smart caching
- [ ] Incremental rendering (only changed diagrams)
- [ ] Parallel diagram rendering
- [x] Native binary (no Python dependency) - **COMPLETED**
- [ ] Typst package integration

## Contributing

Contributions welcome! Please open an issue or PR.

## License

MIT License - see [LICENSE](LICENSE) for details.

## Credits

- **D2**: https://github.com/terrastruct/d2
- **Typst**: https://github.com/typst/typst
- **based package**: https://github.com/EpicEricEE/typst-based

## Related Documentation

- [QUICKSTART.md](QUICKSTART.md) - Quick start guide
- [DEPLOYMENT.md](DEPLOYMENT.md) - Running the hosted server: OAuth, quota, admin UI, garbage collection, Kubernetes
- [MCP_GUIDE.md](MCP_GUIDE.md) - Working with the MCP server
- [HOMEBREW_SETUP.md](HOMEBREW_SETUP.md) - Homebrew tap maintenance
- [IMPLEMENTATION.md](IMPLEMENTATION.md) - Technical implementation details
