# MCP Server Guide

## What is the MCP Server?

`typst-d2-mcp` is a Model Context Protocol (MCP) server that allows AI assistants like Claude Desktop to render D2 diagrams and compile Typst documents with embedded D2 blocks.

## Installation

### Build from Source

```bash
cd typst-d2-mcp
go build -o typst-d2-mcp ./cmd/typst-d2-mcp
```

### Prerequisites

- D2 CLI: https://d2lang.com/tour/install
- Typst CLI: https://github.com/typst/typst

## Configuration

### Claude Desktop

Add to your Claude Desktop configuration file:

**macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`  
**Linux**: `~/.config/Claude/claude_desktop_config.json`

```json
{
  "mcpServers": {
    "typst-d2": {
      "command": "/absolute/path/to/typst-d2-mcp"
    }
  }
}
```

Replace `/absolute/path/to/typst-d2-mcp` with the actual path to your built binary.

### Other MCP Clients

The server uses stdio transport, so it works with any MCP client that supports stdio:

- Claude Desktop
- Cline (VS Code extension)
- Continue (VS Code/JetBrains extension)
- Any custom MCP client

Configuration format varies by client - refer to their documentation.

## Available Tools

### 1. render_d2

Render D2 diagram code to SVG format.

**Parameters:**
- `d2_code` (required): D2 diagram source code
- `layout` (optional): Layout engine - `elk`, `dagre`, or `tala` (default: `elk`)
- `theme` (optional): Theme ID from 0-200
- `sketch` (optional): Enable hand-drawn style - `true` or `false` (default: `false`)

**Returns:** SVG content as text

**Example:**
```
User: "Render this D2 diagram: x -> y -> z"

Claude calls render_d2 with:
{
  "d2_code": "x -> y -> z",
  "layout": "elk"
}

Returns: <svg>...</svg>
```

### 2. render_d2_base64

Render D2 diagram and encode as base64 for embedding in Typst documents.

**Parameters:** Same as `render_d2`

**Returns:** Base64-encoded SVG plus a ready-to-use Typst code snippet

**Example:**
```
User: "Give me the Typst code for a diagram showing client -> server -> database"

Claude calls render_d2_base64 with:
{
  "d2_code": "client -> server -> database",
  "layout": "elk"
}

Returns:
Base64: PD94bWwgdmVyc2lvbj0iMS4wIj8...

Typst code:
#image(decode64("PD94bWwgdmVyc2lvbj0iMS4wIj..."), format: "svg")
```

### 3. compile_typst_with_d2

Preprocess and compile a Typst document with embedded D2 diagrams.

**Parameters:**
- `file_path` (required): Absolute path to the `.typ` file

**Returns:** Success message with output PDF path

**Example:**
```
User: "Compile my document at /Users/me/doc.typ"

Claude calls compile_typst_with_d2 with:
{
  "file_path": "/Users/me/doc.typ"
}

Returns: "Successfully compiled to /Users/me/doc.pdf"
```

### 4. preprocess_typst

Process Typst content with D2 blocks without compiling to PDF.

**Parameters:**
- `typst_content` (required): Typst source code containing `#d2[...]` blocks

**Returns:** Processed Typst code with D2 blocks replaced by embedded SVG images

**Example:**
```
User: "Process this Typst content with D2 diagrams: ..."

Claude calls preprocess_typst with:
{
  "typst_content": "= Diagram\n\n#d2[x -> y]"
}

Returns processed Typst code with base64-encoded SVG
```

## Usage Examples

### Interactive Diagram Creation

```
User: "Create a D2 diagram showing a web architecture with frontend, backend, and database"

Claude:
1. Calls render_d2 with D2 code
2. Shows you the rendered diagram
3. Can iterate based on your feedback
```

### Document Generation

```
User: "Create a Typst document with a system architecture diagram"

Claude:
1. Writes Typst content with #d2[...] blocks
2. Calls preprocess_typst to get processed code
3. Shows you the result
4. Optionally calls compile_typst_with_d2 to generate PDF
```

### Batch Processing

```
User: "Compile all my Typst files in /path/to/docs"

Claude:
1. Lists files (using file system tools)
2. Calls compile_typst_with_d2 for each .typ file
3. Reports results
```

## Troubleshooting

### "d2 command not found"

Install D2:
```bash
curl -fsSL https://d2lang.com/install.sh | sh -s --
```

### "typst command not found"

Install Typst: https://github.com/typst/typst#installation

### Server not appearing in Claude Desktop

1. Check the config file path is correct
2. Verify the `command` path is absolute and points to the binary
3. Restart Claude Desktop completely
4. Check Claude Desktop logs:
   - macOS: `~/Library/Logs/Claude/mcp*.log`
   - Linux: `~/.config/Claude/logs/mcp*.log`

### Tool calls failing

- Ensure D2 and Typst are installed and in PATH
- Check file paths are absolute (relative paths may not work)
- Verify file permissions

## Technical Details

### Transport

The server uses **stdio transport** (stdin/stdout) for communication with MCP clients. This is the standard transport for local-only MCP servers.

### Architecture

```
MCP Client (Claude Desktop)
    ↕ (JSON-RPC over stdio)
typst-d2-mcp Server
    ↕
Internal Go packages:
  - internal/d2 (D2 CLI integration)
  - internal/preprocessor (Typst preprocessing)
  - internal/typst (Typst CLI integration)
    ↕
External CLIs:
  - d2 (diagram rendering)
  - typst (document compilation)
```

### Security Considerations

- **Local only**: Server runs locally, no network exposure
- **File access**: Server can read/write files with your user permissions
- **Command execution**: Server executes `d2` and `typst` commands
- **Trust**: Only run with trusted AI clients

## Advanced Configuration

### Environment Variables

The server respects environment variables like `PATH` for finding `d2` and `typst` binaries.

### Logging

The server logs errors to stderr. To capture logs when running via MCP clients, check the client's log files (e.g., Claude Desktop logs).

## Development

### Testing Tools Manually

You can test the server manually using the MCP Inspector:

```bash
# Install MCP Inspector
npm install -g @modelcontextprotocol/inspector

# Run the inspector
mcp-inspector /path/to/typst-d2-mcp
```

This opens a web UI where you can test tool calls interactively.

### Building for Distribution

```bash
# Single platform
go build -o typst-d2-mcp ./cmd/typst-d2-mcp

# Cross-compile for macOS
GOOS=darwin GOARCH=arm64 go build -o typst-d2-mcp-darwin-arm64 ./cmd/typst-d2-mcp

# Use GoReleaser for all platforms
goreleaser release --snapshot --clean
```

## See Also

- [MCP Specification](https://spec.modelcontextprotocol.io/)
- [D2 Documentation](https://d2lang.com/tour/intro)
- [Typst Documentation](https://typst.app/docs)
- [Claude Desktop MCP Guide](https://docs.anthropic.com/claude/docs/model-context-protocol)
