# MCP Server Guide

## What is the MCP Server?

`typst-d2-mcp` is a Model Context Protocol (MCP) server that enables AI assistants to create professional documentation with embedded D2 diagrams in Typst documents.

## Philosophy

This MCP server provides **one focused tool** that encourages AI assistants to produce rich, visual documentation. Instead of separate tools for rendering diagrams, the server guides AI to:

1. Write Typst content with narrative structure
2. Embed diagrams directly using `#d2[...]` syntax
3. Compile the complete document to PDF in one step

This approach results in better documentation because the AI thinks about diagrams in context with the surrounding text.

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

### OpenCode

Add to `~/.config/opencode/opencode.json`:

```json
{
  "mcp": {
    "typst-d2": {
      "type": "local",
      "command": "/absolute/path/to/typst-d2-mcp"
    }
  }
}
```

### Other MCP Clients

The server uses stdio transport, so it works with any MCP client that supports stdio:

- Claude Desktop
- OpenCode
- Cline (VS Code extension)
- Continue (VS Code/JetBrains extension)
- Any custom MCP client

Configuration format varies by client - refer to their documentation.

## Available Tool

### compile_typst_with_d2

**Purpose**: Compile Typst documents with embedded D2 diagrams to PDF.

**Parameters:**
- `file_path` (required): Absolute path to the Typst source file (.typ) containing #d2[...] blocks

**Returns:** Success message with output PDF path

**Tool Description (Visible to AI):**

The tool provides extensive guidance to AI assistants through its description:

- **Best Practices**: Use D2 for system architectures, flowcharts, ERDs, technical illustrations
- **Syntax Examples**: Shows basic and advanced #d2[...] usage with options
- **Typical Workflow**: Guides AI through the complete documentation creation process
- **Feature Highlights**: All D2 layouts (elk/dagre/tala), themes, sketch mode, clean output

This rich description encourages AI assistants to produce high-quality visual documentation.

## Usage Examples

### System Architecture Documentation

```
User: "Create documentation for our microservices architecture"

AI Assistant:
1. Creates Typst document with sections:
   - Overview
   - Architecture Diagram (using #d2[...])
   - Service Descriptions
   - Data Flow (using #d2[...])
   - Deployment

2. Saves to architecture.typ

3. Calls compile_typst_with_d2 with file path

Result: Professional PDF with embedded architecture diagrams
```

### API Documentation

```
User: "Document this REST API flow: client authenticates, 
      fetches user data, and updates profile"

AI Assistant:
1. Creates Typst document with:
   - API Overview
   - Authentication Flow Diagram (#d2 with sequence)
   - Endpoint Descriptions
   - Request/Response Examples
   - State Diagram (#d2 showing user states)

2. Compiles to PDF

Result: Complete API documentation with visual flow diagrams
```

### Database Schema Documentation

```
User: "Create ER diagram documentation for our database schema"

AI Assistant:
1. Creates Typst document with:
   - Schema Overview
   - ER Diagram using #d2[...] with:
     users: Users {
       shape: sql_table
       id: int
       name: string
       email: string
     }
     posts: Posts {
       shape: sql_table
       id: int
       user_id: int
       content: text
     }
     users.id -> posts.user_id
   - Table Descriptions
   - Relationships

2. Compiles to professional database documentation

Result: ER diagram with detailed schema documentation
```

## D2 Syntax Reference

The tool supports all D2 syntax features:

### Basic Diagram

```typst
#d2[
  client -> server -> database
]
```

### With Options

```typst
#d2(layout: "elk", theme: "0", sketch: "true")[
  frontend: Frontend {
    shape: rectangle
    style.fill: "#b8d4ff"
  }
  backend: Backend {
    shape: rectangle
    style.fill: "#ffd4b8"
  }
  frontend -> backend: API Calls {
    style.stroke-dash: 3
  }
]
```

## Best Practices for Diagram Layout

### Understanding Page Constraints

When creating diagrams, always consider the target page format:

**A4 Portrait (default Typst):**
- **Usable width:** ~17cm (limited horizontal space)
- **Usable height:** ~25cm (ample vertical space)
- **Best approach:** Vertical layouts (`direction: down`)
- **Problem:** Wide horizontal diagrams get cramped, text becomes tiny and unreadable

**A4 Landscape** (`#set page(flipped: true)`):
- **Usable width:** ~25cm (ample horizontal space)
- **Usable height:** ~17cm (limited vertical space)
- **Best approach:** Horizontal layouts (`direction: right`)

### Choosing Direction

**Use `direction: down` (RECOMMENDED for most cases):**
```typst
#d2(layout: "elk", theme: "0")[
  direction: down  // Vertical flow
  
  frontend -> backend -> database -> cache
]
```
- ✅ Works well with A4 portrait (default)
- ✅ Maximizes use of available page height
- ✅ Prevents text from becoming too small
- ✅ Better readability for complex diagrams with many nodes

**Use `direction: right` (sparingly):**
```typst
#d2(layout: "elk", theme: "0")[
  direction: right  // Horizontal flow
  
  user -> app -> db
]
```
- ⚠️ Only for simple diagrams (≤5 nodes) or landscape pages
- ⚠️ Can cause cramping on portrait pages
- ✅ Good for wide organizational hierarchies on landscape

### Rule of Thumb

**If your diagram has more than 5 nodes at the same level, use `direction: down`.**

**Bad (cramped on A4 portrait):**
```typst
#d2(layout: "elk", theme: "0")[
  // No direction specified - defaults to horizontal
  ceo
  cto
  cfo
  coo
  cmo
  cto -> engineering
  cfo -> finance
  // ... gets wide and cramped
]
```

**Good (readable on A4 portrait):**
```typst
#d2(layout: "elk", theme: "0")[
  direction: down  // Vertical stacking
  
  ceo
  cto
  cfo
  coo
  cmo
  cto -> engineering
  cfo -> finance
  // ... stacks vertically, plenty of space
]
```

### Visual Verification Workflow

After generating a document:

1. **Open the PDF** to check diagram layout
2. **Verify text is readable** - labels should not be tiny (<8pt)
3. **Check margins** - diagrams should not touch page edges
4. **Assess complexity** - if cramped, split into multiple diagrams or use `direction: down`

If you cannot view the PDF yourself (as an AI assistant), **inform the user** to check the layout and suggest:
- Adding `direction: down` if diagrams appear cramped
- Splitting large diagrams into focused sub-diagrams
- Reducing node count or simplifying structure

### Splitting Large Diagrams

Instead of one massive diagram:

**Bad:**
```typst
#d2[
  // 50 nodes in one diagram - unreadable!
  frontend...
  backend...
  databases...
  infrastructure...
]
```

**Good:**
```typst
=== Frontend Architecture
#d2(layout: "elk", theme: "0")[
  direction: down
  // 10-15 focused frontend nodes
]

=== Backend Services
#d2(layout: "elk", theme: "0")[
  direction: down
  // 10-15 focused backend nodes
]

=== Data Layer
#d2(layout: "elk", theme: "0")[
  direction: down
  // 10-15 focused data nodes
]
```


### Layout Engines

- `elk` (default): Best for hierarchical layouts
- `dagre`: Good for directed graphs
- `tala`: Experimental layout engine

### Themes

- Theme IDs from `0` to `200`
- Examples: `0` (default), `100` (dark mode), `200` (colorblind-friendly)

### Sketch Mode

```typst
#d2(sketch: "true")[
  // Hand-drawn style diagram
]
```

### Containers

```typst
#d2[
  network: Corporate Network {
    web: Web Tier {
      nginx
      apache
    }
    app: Application Tier {
      api
      workers
    }
    web.nginx -> app.api
  }
]
```

## Workflow Details

### What the Tool Does

1. **Preprocessing**: Scans .typ file for `#d2[...]` blocks
2. **Rendering**: Calls D2 CLI for each diagram (stdin→stdout streaming)
3. **Encoding**: Converts SVG to base64
4. **Embedding**: Replaces #d2[...] with `#image(decode64("..."), format: "svg")`
5. **Importing**: Adds `#import "@preview/based:0.2.0": decode64` automatically
6. **Compilation**: Runs `typst compile` on processed content
7. **Cleanup**: Removes temporary files, keeps only original .typ and output .pdf

### No Filesystem Clutter

- No intermediate `.svg` files created
- No temporary files left behind
- Only input `.typ` and output `.pdf` remain

## Troubleshooting

### "d2 command not found"

Install D2:
```bash
curl -fsSL https://d2lang.com/install.sh | sh -s --
```

### "typst command not found"

Install Typst: https://github.com/typst/typst#installation

### Server not appearing in MCP client

1. Check the config file path is correct
2. Verify the `command` path is absolute and points to the binary
3. Restart the MCP client completely
4. Check client logs for MCP connection errors

### Compilation fails

- Ensure file path is absolute (relative paths may not work)
- Check file exists and is readable
- Verify D2 syntax in #d2[...] blocks is valid
- Check Typst syntax outside #d2[...] blocks

## Advanced Usage

### Custom D2 Configurations

Use D2 options to control appearance:

```typst
#d2(
  layout: "elk",
  theme: "0",
  sketch: "true"
)[
  // Your diagram code
]
```

### Multiple Diagrams

A single document can have many diagrams:

```typst
= Overview
#d2[high-level architecture]

= Component Details
#d2[detailed component diagram]

= Data Flow
#d2[sequence diagram]
```

Each is processed independently with its own options.

## Technical Details

### Transport

The server uses **stdio transport** (stdin/stdout) for communication with MCP clients. This is the standard transport for local-only MCP servers.

### Architecture

```
MCP Client (Claude Desktop, OpenCode, etc.)
    ↕ (JSON-RPC over stdio)
typst-d2-mcp Server
    ↕
Internal Go packages:
  - internal/preprocessor (Typst preprocessing)
  - internal/d2 (D2 CLI integration)
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

## Development

### Testing Manually

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
