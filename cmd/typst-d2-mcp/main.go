package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/dlouwers/typst-d2-mcp/internal/preprocessor"
	"github.com/dlouwers/typst-d2-mcp/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	serverName    = "typst-d2-mcp"
	serverVersion = "1.0.0"

	envTransport = "TYPST_D2_MCP_TRANSPORT"
	envAddr      = "TYPST_D2_MCP_ADDR"
	envPath      = "TYPST_D2_MCP_PATH"

	defaultAddr = ":8080"
	defaultPath = "/mcp"
)

// serverInstructions is sent once at the MCP initialize handshake. Moving
// this guidance out of the per-tool description keeps it available to the
// model without re-spending tokens on every tool call. Keep it focused on
// strategy and anti-patterns the model needs across multiple compiles;
// the per-call rule lives in the tool description.
const serverInstructions = `You can author Typst documents containing #d2[...] blocks and compile them
to PDF with the compile_typst_with_d2 tool. The notes below apply across
every diagram in a session — the tool description itself stays brief.

DIAGRAM LAYOUT — A4 PORTRAIT (Typst default):
  Usable area is roughly 17cm wide × 25cm tall, so vertical layouts breathe
  while horizontal layouts get cramped. Prefer "direction: down" inside the
  D2 block whenever a diagram has more than a handful of nodes.

STAR-TOPOLOGY ANTI-PATTERN:
  A central node connecting to 5+ siblings forces the ELK layout engine to
  spread the children horizontally even when "direction: down" is set. The
  fix is a vertical chain:

      // BAD (renders horizontally on A4 portrait)
      center -> a
      center -> b
      center -> c
      center -> d
      center -> e

      // GOOD
      center -> a -> b -> c -> d -> e

  Org charts with two or three direct reports can stay as a star; four or
  more children means convert to a chain or split into multiple diagrams.

A4 LANDSCAPE (#set page(flipped: true)):
  Usable area becomes ~25cm × 17cm. Prefer "direction: right" for wide
  hierarchies; vertical chains still work but waste horizontal space.

PRINT-FRIENDLY DEFAULTS:
  - layout: "elk"  (best automatic layout)
  - theme: "0"     (white background, good contrast on paper)
  - Avoid dark themes (100–200 range) for print.

SYNTAX EXAMPLES:
  Basic:
    #d2(layout: "elk", theme: "0")[
      direction: down
      client -> server -> database
    ]

  Architecture with shapes:
    #d2(layout: "elk", theme: "0")[
      direction: down
      frontend: Frontend {shape: rectangle}
      backend:  Backend  {shape: rectangle}
      database: Database {shape: cylinder}
      frontend -> backend: API calls
      backend  -> database: Queries
    ]

VERIFYING THE RESULT:
  After a successful compile, open the produced PDF if you can. Check that
  text labels are readable and the diagram fits within page margins. If a
  diagram looks cramped, add "direction: down", split it into multiple
  diagrams, or remove non-essential nodes. If you cannot view the PDF
  yourself, advise the user to inspect it.`

func main() {
	s := server.NewMCPServer(
		serverName,
		serverVersion,
		server.WithToolCapabilities(false),
		server.WithInstructions(serverInstructions),
	)

	registerTools(s)

	if err := serve(s); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

// serve selects a transport based on TYPST_D2_MCP_TRANSPORT. Default is
// stdio, preserving the previously installed behavior. Setting the env to
// "http" starts a streamable-HTTP MCP server on TYPST_D2_MCP_ADDR
// (default :8080) at TYPST_D2_MCP_PATH (default /mcp).
func serve(s *server.MCPServer) error {
	switch transport := strings.ToLower(os.Getenv(envTransport)); transport {
	case "", "stdio":
		return server.ServeStdio(s)
	case "http", "streamable-http":
		addr := os.Getenv(envAddr)
		if addr == "" {
			addr = defaultAddr
		}
		path := os.Getenv(envPath)
		if path == "" {
			path = defaultPath
		}
		httpSrv := server.NewStreamableHTTPServer(s, server.WithEndpointPath(path))
		fmt.Fprintf(os.Stderr, "%s listening on http://%s%s\n", serverName, addr, path)
		return httpSrv.Start(addr)
	default:
		return fmt.Errorf("unknown %s=%q (expected stdio or http)", envTransport, transport)
	}
}

func registerTools(s *server.MCPServer) {
	// The bulk of the layout strategy lives in server instructions above so
	// it isn't re-sent on every tool call. The description below carries
	// only the rules the model needs at the moment it decides to call.
	compileTypstTool := mcp.NewTool("compile_typst_with_d2",
		mcp.WithDescription(`Compile a Typst document containing #d2[...] diagram blocks to PDF.

The input file is preprocessed in place: each #d2(opts)[code] block is
rendered to SVG via the d2 CLI, base64-embedded, and the resulting Typst
source is compiled with the typst CLI. The output PDF is written next to
the input .typ file.

Quick rules (full strategy in the server's instructions):
  - Default layout "elk", theme "0" for print-friendly diagrams.
  - On A4 portrait, add 'direction: down' inside the D2 block.
  - A central node with 4+ children renders horizontally even with
    'direction: down' — rewrite as a vertical chain.

After compiling, inspect the PDF if you can; if a diagram looks cramped,
split it, simplify it, or switch to 'direction: down'.`),
		mcp.WithString("file_path",
			mcp.Required(),
			mcp.Description("Absolute path to the Typst source file (.typ) containing #d2[...] blocks"),
		),
	)
	s.AddTool(compileTypstTool, handleCompileTypst)
}

func handleCompileTypst(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	filePath, err := request.RequireString("file_path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	resolver := workspace.LocalFS{}
	resolved, err := workspace.MustExist(resolver, filePath)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	processed, err := preprocessor.Preprocess(resolver, resolved)
	if err != nil {
		return mcp.NewToolResultErrorFromErr("Preprocessing failed", err), nil
	}

	tmpFile, err := os.CreateTemp("", "typst-d2-*.typ")
	if err != nil {
		return mcp.NewToolResultErrorFromErr("Failed to create temp file", err), nil
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(processed); err != nil {
		return mcp.NewToolResultErrorFromErr("Failed to write temp file", err), nil
	}
	tmpFile.Close()

	outPath := strings.TrimSuffix(resolved, ".typ") + ".pdf"

	cmd := exec.CommandContext(ctx, "typst", "compile", tmpFile.Name(), outPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		errMsg := fmt.Sprintf("Typst compilation failed: %s\nOutput: %s", err.Error(), string(output))
		return mcp.NewToolResultError(errMsg), nil
	}

	successMsg := fmt.Sprintf("Successfully compiled to %s\n\n", outPath)
	successMsg += "NEXT STEPS:\n"
	successMsg += "1. Open the PDF to verify diagram layout and readability\n"
	successMsg += "2. Check that diagrams fit within page margins (not cramped)\n"
	successMsg += "3. Verify text labels are readable (not too small)\n"
	successMsg += "4. If diagrams are cramped or text is tiny:\n"
	successMsg += "   - Add 'direction: down' at top of D2 block (for A4 portrait)\n"
	successMsg += "   - Split large diagrams into multiple focused diagrams\n"
	successMsg += "   - Reduce number of nodes or simplify structure\n"
	successMsg += "\nIf you cannot view the PDF yourself, inform the user to check the layout."

	return mcp.NewToolResultText(successMsg), nil
}
